package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// LocalEnvelopesForImport validates the complete curated snapshot and its
// invocation dependency closure, including incoming identities. The caller's
// transaction protects every read and comparison. Unrelated raw attribution
// remains stored but cannot affect this snapshot.
func LocalEnvelopesForImport(ctx context.Context, tx *sql.Tx, projectID string, objects []EncodedEnvelope) (map[string]EncodedEnvelope, error) {
	return localEnvelopesForImport(ctx, tx, projectID, objects)
}

// source must already be a consistent planning or caller-owned transaction.
func localEnvelopesForImport(ctx context.Context, source queryer, projectID string, objects []EncodedEnvelope) (map[string]EncodedEnvelope, error) {
	identity, err := identityFrom(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	root, err := projectRootFrom(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	var schema string
	if err := source.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&schema); err != nil {
		return nil, err
	}
	roots := make(map[string]struct{})
	for _, object := range objects {
		if _, err := addImportObjectInvocationRoots(roots, object); err != nil {
			return nil, err
		}
	}
	recovered, err := RecoveredEnvelopes(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	for _, object := range recovered {
		addImportInvocationRoots(roots, object.Envelope)
	}
	rows, err := source.QueryContext(ctx, `SELECT record_id FROM portable_envelopes WHERE project_id=? AND record_type='actor_invocation'`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		roots[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	cache := atomicSnapshotFor(ctx, source, projectID)
	if local, ok := cache.load(ctx, roots); ok {
		return local, nil
	}
	selected := &importInvocationSource{queryer: source, project: projectID, roots: roots}
	for {
		if len(roots) > MaxRecordCount {
			return nil, fmt.Errorf("invocation dependency count exceeds bounds")
		}
		syncwork.RecordWork(ctx, "import_invocation_closure_rounds", 1)
		validated := make(snapshotEncodings)
		specs, _, err := loadOperationalSpecs(ctx, selected, projectID, root, identity.LineageID, true, validated, roots)
		if err != nil {
			return nil, err
		}
		changed := false
		for _, spec := range specs {
			if addImportInvocationRoots(roots, Envelope{RecordType: spec.RecordType, RecordID: spec.RecordID, Refs: spec.Refs}) {
				changed = true
			}
			if spec.RecordType == "redaction_recovery" {
				object, err := validated.encode(ctx, spec)
				if err != nil {
					return nil, err
				}
				expanded, err := addImportObjectInvocationRoots(roots, object)
				if err != nil {
					return nil, err
				}
				changed = changed || expanded
			}
		}
		// Retained/recovered wire references may differ from the operational binding.
		// Keep this same transaction and reach the complete closure before comparing.
		if changed {
			continue
		}
		result := make(map[string]EncodedEnvelope, len(specs))
		for _, spec := range specs {
			encoded, err := validated.encode(ctx, spec)
			if err != nil {
				return nil, err
			}
			result[recordKey(encoded.Envelope)] = encoded
		}
		cache.store(ctx, roots, result)
		return result, nil
	}
}

// Recovery observations carry both dependency interpretations inside their
// payload, rather than in the outer envelope's reference list.
func addImportObjectInvocationRoots(roots map[string]struct{}, object EncodedEnvelope) (bool, error) {
	changed := addImportInvocationRoots(roots, object.Envelope)
	if object.Envelope.RecordType != "redaction_recovery" || !SupportedEnvelope(object.Envelope) {
		return changed, nil
	}
	proof, err := decodeRedactionRecovery(object)
	if err != nil {
		return false, err
	}
	for _, body := range [][]byte{proof.Original, proof.Derived} {
		nested, err := recoveryEnvelope(body)
		if err != nil {
			return false, err
		}
		changed = addImportInvocationRoots(roots, nested.Envelope) || changed
	}
	return changed, nil
}

func addImportInvocationRoots(roots map[string]struct{}, e Envelope) bool {
	changed := false
	add := func(id string) {
		if id != "" {
			if _, ok := roots[id]; !ok {
				roots[id] = struct{}{}
				changed = true
			}
		}
	}
	if e.RecordType == "actor_invocation" {
		add(e.RecordID)
	}
	for _, ref := range e.Refs {
		if ref.Kind == "actor_invocation" {
			add(ref.ID)
		}
	}
	return changed
}

// Only the package-owned complete invocation query is restricted. Every other
// curated read keeps its ordinary scope. Unknown actor query shapes fail closed.
// Parent discovery reads only identity metadata; the existing row loader still
// validates all selected bodies and preserves its original SQL time/ID order.
type importInvocationSource struct {
	queryer
	project string
	roots   map[string]struct{}
}

func (s *importInvocationSource) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if !strings.Contains(strings.ToLower(query), "actor_invocations") {
		return s.queryer.QueryContext(ctx, query, args...)
	}
	if query != invocationRecordsQuery+" ORDER BY occurred_at,id" || len(args) != 1 || args[0] != s.project {
		return nil, fmt.Errorf("unqualified import invocation query")
	}
	if err := s.closeParents(ctx); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(s.roots))
	for id := range s.roots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	body, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	return s.queryer.QueryContext(ctx, invocationRecordsQuery+" AND id IN (SELECT value FROM json_each(?)) ORDER BY occurred_at,id", s.project, string(body))
}

func (s *importInvocationSource) closeParents(ctx context.Context) error {
	pending := make(map[string]bool, len(s.roots))
	for id := range s.roots {
		pending[id] = true
	}
	seen := make(map[string]bool)
	for len(pending) > 0 {
		if len(s.roots) > MaxRecordCount {
			return fmt.Errorf("invocation dependency count exceeds bounds")
		}
		ids := make([]string, 0, len(pending))
		for id := range pending {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) > 512 {
			ids = ids[:512]
		}
		args := []any{s.project}
		for _, id := range ids {
			if !utf8.ValidString(id) {
				return fmt.Errorf("invocation identity must be valid UTF-8")
			}
			delete(pending, id)
			seen[id] = true
			args = append(args, id)
		}
		err := func() error {
			rows, err := s.queryer.QueryContext(ctx, `SELECT id,COALESCE(parent_invocation_id,'') FROM actor_invocations WHERE project_id=? AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id, parent string
				if err := rows.Scan(&id, &parent); err != nil {
					return err
				}
				syncwork.RecordWork(ctx, "import_invocation_metadata_rows", 1)
				s.roots[id] = struct{}{}
				if parent != "" && !seen[parent] {
					s.roots[parent] = struct{}{}
					pending[parent] = true
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
