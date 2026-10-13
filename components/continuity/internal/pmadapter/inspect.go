package pmadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,255}$`)
var webURL = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

// Invalid errors contain only static paths/codes, never input values or keys.
func invalid(path, code string) error { return fmt.Errorf("pm input %s: %s", path, code) }

// Inspect validates one packet and returns only sanitized observations. Expected
// is independently supplied by the caller; it is not inferred from the packet.
func Inspect(data []byte, expected Project) (Report, error) {
	var empty Report
	if len(data) == 0 || len(data) > MaxBytes || !utf8.Valid(data) {
		return empty, invalid("$", "invalid_size_or_encoding")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	value, err := readValue(d, 0)
	if err != nil {
		return empty, invalid("$", "invalid_json_structure")
	}
	if _, err = d.Token(); err != io.EOF {
		return empty, invalid("$", "trailing_data")
	}
	if err := shape(value, reflect.TypeFor[Packet](), "$", 0); err != nil {
		return empty, err
	}
	var p Packet
	d = json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return empty, invalid("$", "invalid_schema")
	}
	if err := validate(p, expected); err != nil {
		return empty, err
	}
	// Resource IDs are unique within the packet, so relationship/source ordering
	// is unambiguous even when the provider has several resource types.
	sort.Slice(p.Snapshot.Resources, func(i, j int) bool { return p.Snapshot.Resources[i].Ref.ID < p.Snapshot.Resources[j].Ref.ID })
	sort.Slice(p.Snapshot.Relationships, func(i, j int) bool {
		a, b := p.Snapshot.Relationships[i], p.Snapshot.Relationships[j]
		return a.From+"\x00"+a.Kind+"\x00"+a.To < b.From+"\x00"+b.Kind+"\x00"+b.To
	})
	sort.Strings(p.Manifest.Capabilities)
	sort.Strings(p.Manifest.ResourceTypes)
	sort.Ints(p.Manifest.ContractVersions)
	sort.Slice(p.Proposals, func(i, j int) bool { return p.Proposals[i].ID < p.Proposals[j].ID })
	diagnostics := []Diagnostic{}
	add := func(path, code string) { diagnostics = append(diagnostics, Diagnostic{path, code}) }
	if !p.Snapshot.Complete {
		add("$.snapshot", "incomplete_snapshot_no_deletion_inference")
	}
	for i, r := range p.Snapshot.Resources {
		path := fmt.Sprintf("$.snapshot.resources[%d]", i)
		if len(r.Fields) > 0 {
			add(path+".fields", "opaque_provider_fields")
		}
		if r.Deleted {
			add(path+".deleted", "provider_tombstone_only")
		}
	}
	for i := range p.Proposals {
		sort.Slice(p.Proposals[i].Mappings, func(a, b int) bool { return p.Proposals[i].Mappings[a].Field < p.Proposals[i].Mappings[b].Field })
		for j, m := range p.Proposals[i].Mappings {
			path := fmt.Sprintf("$.proposals[%d].mappings[%d]", i, j)
			if m.Lossy {
				add(path, "lossy_mapping")
			}
			if m.Authority == "neither" {
				add(path, "unresolved_field_authority")
			}
		}
	}
	raw, _ := json.Marshal(p)
	var normalized any
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&normalized); err != nil {
		return empty, errors.New("pm normalization failed")
	}
	safe := sanitize(normalized)
	encoded, err := json.Marshal(safe)
	if err != nil {
		return empty, errors.New("pm sanitization failed")
	}
	before, _ := json.Marshal(normalized)
	if !bytes.Equal(before, encoded) {
		add("$", "redacted")
	}
	hash := sha256.Sum256(encoded)
	return Report{Version, "valid", hex.EncodeToString(hash[:]), safe, diagnostics, "provider_observations_and_unreviewed_adapter_interpretations"}, nil
}

// readValue rejects duplicate keys (including escaped equivalents), nulls,
// excessive nesting and oversized strings before typed decoding. Paths from
// untrusted object keys never enter errors. UseNumber preserves opaque numbers.
func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > MaxDepth {
		return nil, errors.New("depth")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch v := t.(type) {
	case nil:
		return nil, errors.New("null")
	case string:
		if len(v) > MaxTextBytes || strings.ContainsRune(v, 0) {
			return nil, errors.New("text")
		}
		return v, nil
	case json.Delim:
		switch v {
		case '{':
			out := map[string]any{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := k.(string)
				if !ok || len(key) > MaxTextBytes || strings.ContainsRune(key, 0) {
					return nil, errors.New("key")
				}
				if _, ok := out[key]; ok {
					return nil, errors.New("duplicate")
				}
				item, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				out[key] = item
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return out, nil
		case '[':
			out := []any{}
			for d.More() {
				item, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, item)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return out, nil
		default:
			return nil, errors.New("delimiter")
		}
	default:
		return t, nil
	}
}

// shape enforces exact JSON tag spelling and required fields. encoding/json
// otherwise accepts case-insensitive aliases, even with DisallowUnknownFields.
// Only names from trusted Go tags are used in diagnostic paths.
func shape(v any, typ reflect.Type, path string, depth int) error {
	if depth > MaxDepth {
		return invalid(path, "invalid_schema")
	}
	switch typ.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return invalid(path, "object_required")
		}
		known := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			known[name] = true
			item, exists := m[name]
			if !exists {
				if contains(tag[1:], "omitempty") {
					continue
				}
				return invalid(path+"."+name, "required")
			}
			if err := shape(item, field.Type, path+"."+name, depth+1); err != nil {
				return err
			}
		}
		for key := range m {
			if !known[key] {
				return invalid(path, "invalid_schema")
			}
		}
	case reflect.Slice:
		list, ok := v.([]any)
		if !ok {
			return invalid(path, "array_required")
		}
		for i, item := range list {
			if err := shape(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if _, ok := v.(map[string]any); !ok {
			return invalid(path, "object_required")
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, s := range values {
		if s == value {
			return true
		}
	}
	return false
}
func validProject(p Project) bool {
	return identifier.MatchString(p.Provider) && identifier.MatchString(p.Instance) && identifier.MatchString(p.Project)
}
func validate(p Packet, expected Project) error {
	if p.SchemaVersion != Version {
		return invalid("$.schema_version", "unsupported_version")
	}
	if !validProject(expected) || !validProject(p.Project) || p.Project != expected {
		return invalid("$.project", "project_mismatch")
	}
	m := p.Manifest
	if !identifier.MatchString(m.ID) || !identifier.MatchString(m.Version) || !identifier.MatchString(m.MappingProfile) || !identifier.MatchString(m.MappingVersion) || m.Provider != p.Project.Provider {
		return invalid("$.manifest", "invalid_identity")
	}
	if len(m.ContractVersions) != 1 || m.ContractVersions[0] != Version {
		return invalid("$.manifest.contract_versions", "unsupported_version")
	}
	for _, set := range []struct {
		path            string
		values, allowed []string
	}{
		{"capabilities", m.Capabilities, []string{"read_snapshot", "propose_import"}},
		{"resource_types", m.ResourceTypes, []string{"work_item", "initiative", "milestone", "research", "release"}},
	} {
		if len(set.values) == 0 || len(set.values) > len(set.allowed) {
			return invalid("$.manifest."+set.path, "invalid_set")
		}
		seen := map[string]bool{}
		for _, v := range set.values {
			if seen[v] || !contains(set.allowed, v) {
				return invalid("$.manifest."+set.path, "unsupported_or_duplicate")
			}
			seen[v] = true
		}
	}
	if !contains(m.Capabilities, "read_snapshot") || len(p.Proposals) > 0 && !contains(m.Capabilities, "propose_import") {
		return invalid("$.manifest.capabilities", "missing_capability")
	}
	if len(p.Snapshot.Resources) > 256 || len(p.Snapshot.Relationships) > 512 || len(p.Proposals) > 256 {
		return invalid("$", "record_limit")
	}
	if p.Snapshot.Complete && p.Snapshot.Cursor != "" {
		return invalid("$.snapshot.cursor", "complete_with_cursor")
	}
	resources := map[string]Resource{}
	for i, r := range p.Snapshot.Resources {
		path := fmt.Sprintf("$.snapshot.resources[%d]", i)
		if r.Ref.Project != expected || !identifier.MatchString(r.Ref.ID) || !contains(m.ResourceTypes, r.Ref.Type) || strings.TrimSpace(r.Ref.Revision) == "" {
			return invalid(path+".ref", "invalid_reference")
		}
		if _, err := time.Parse(time.RFC3339, r.Ref.ObservedAt); err != nil {
			return invalid(path+".ref.observed_at", "invalid_time")
		}
		if r.Ref.URL != "" {
			u, err := url.Parse(r.Ref.URL)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				return invalid(path+".ref.url", "invalid_url")
			}
		}
		if _, ok := resources[r.Ref.ID]; ok {
			return invalid(path+".ref.id", "duplicate_identity_or_revision")
		}
		resources[r.Ref.ID] = r
	}
	seenRelations := map[Relationship]bool{}
	for i, r := range p.Snapshot.Relationships {
		path := fmt.Sprintf("$.snapshot.relationships[%d]", i)
		_, a := resources[r.From]
		_, b := resources[r.To]
		if !a || !b || r.From == r.To || !contains([]string{"parent_of", "depends_on", "blocks", "parallel_with", "explores"}, r.Kind) || seenRelations[r] {
			return invalid(path, "invalid_relationship")
		}
		seenRelations[r] = true
	}
	seen := map[string]bool{}
	sources := map[string]bool{}
	for i, p := range p.Proposals {
		path := fmt.Sprintf("$.proposals[%d]", i)
		if !identifier.MatchString(p.ID) || seen[p.ID] || sources[p.Source] {
			return invalid(path, "duplicate_or_invalid_proposal")
		}
		seen[p.ID] = true
		sources[p.Source] = true
		r, ok := resources[p.Source]
		if !ok || r.Deleted {
			return invalid(path+".source", "missing_or_deleted_source")
		}
		if p.SourceRevision != r.Ref.Revision {
			return invalid(path+".source_revision", "stale_revision")
		}
		if len(p.Mappings) == 0 || len(p.Mappings) > 7 {
			return invalid(path+".mappings", "invalid_count")
		}
		fields := map[string]bool{}
		for j, f := range p.Mappings {
			fp := fmt.Sprintf("%s.mappings[%d]", path, j)
			if fields[f.Field] || !contains([]string{"title", "status", "kind", "parent", "dependency", "release", "target_date"}, f.Field) {
				return invalid(fp+".field", "unsupported_or_conflicting_mapping")
			}
			fields[f.Field] = true
			if strings.TrimSpace(f.Value) == "" || !contains([]string{"provider", "llewm", "neither"}, f.Authority) {
				return invalid(fp, "invalid_value_or_authority")
			}
			// Match the existing roadmap vocabulary without coupling pure input
			// validation to its database-backed service.
			if f.Field == "status" && !contains([]string{"proposed", "accepted", "active", "blocked", "completed", "deferred", "superseded"}, f.Value) {
				return invalid(fp+".value", "invalid_status")
			}
			if f.Field == "kind" && !contains([]string{"initiative", "milestone", "work_item", "research"}, f.Value) {
				return invalid(fp+".value", "invalid_kind")
			}
			if f.Field == "parent" || f.Field == "dependency" {
				if _, ok := resources[f.Value]; !ok || f.Value == p.Source {
					return invalid(fp+".value", "invalid_target")
				}
			}
			if f.Field == "target_date" {
				if _, err := time.Parse("2006-01-02", f.Value); err != nil {
					return invalid(fp+".value", "invalid_date")
				}
			}
		}
	}
	return nil
}

func sanitize(v any) any {
	switch x := v.(type) {
	case string:
		// Strip every query/fragment and all userinfo, including user-only auth.
		// Unknown provider query names are not a reliable credential allowlist.
		x = webURL.ReplaceAllStringFunc(x, func(s string) string {
			u, err := url.Parse(s)
			if err != nil {
				return "[REDACTED_URL]"
			}
			u.User = nil
			u.RawQuery = ""
			u.ForceQuery = false
			u.Fragment = ""
			u.RawFragment = ""
			return u.String()
		})
		return redact.String(x)
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = sanitize(v)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			clean := sanitize(k).(string)
			// Opaque keys may themselves contain credentials. Avoid collisions
			// after redaction by withholding the whole object.
			if _, exists := out[clean]; exists {
				return "[REDACTED_KEY_COLLISION]"
			}
			probe := redact.Value(map[string]any{k: "safe-marker"}).(map[string]any)
			if probe[redact.String(k)] == "[REDACTED]" {
				out[clean] = "[REDACTED]"
			} else {
				out[clean] = sanitize(v)
			}
		}
		return out
	default:
		return v
	}
}
