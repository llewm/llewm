package pmadapter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func fixture() Packet {
	p := Project{"example", "tenant", "project"}
	return Packet{Version, Manifest{"adapter", "1", "example", []int{1}, []string{"read_snapshot", "propose_import"}, []string{"work_item"}, "basic", "1"}, p,
		Snapshot{true, "", []Resource{{Reference{p, "work_item", "A", "https://example.test/A", "r1", "2026-10-07T00:00:00Z"}, false, map[string]any{"title": "Example"}}}, []Relationship{}},
		[]Proposal{{"proposal-A", "A", "r1", []Mapping{{"title", "Example", "provider", false}, {"status", "active", "neither", true}}}}}
}
func inspectFixture(p Packet) (Report, error) {
	data, _ := json.Marshal(p)
	return Inspect(data, p.Project)
}

func TestInspectRejectsInvalidSemantics(t *testing.T) {
	cases := map[string]func(*Packet){
		"version":                func(p *Packet) { p.SchemaVersion = 2 },
		"manifest version":       func(p *Packet) { p.Manifest.ContractVersions = []int{2} },
		"capability":             func(p *Packet) { p.Manifest.Capabilities = []string{"read_snapshot"} },
		"unsupported capability": func(p *Packet) { p.Manifest.Capabilities = append(p.Manifest.Capabilities, "apply_export") },
		"cross project":          func(p *Packet) { p.Snapshot.Resources[0].Ref.Project.Project = "other" },
		"duplicate identity":     func(p *Packet) { p.Snapshot.Resources = append(p.Snapshot.Resources, p.Snapshot.Resources[0]) },
		"stale":                  func(p *Packet) { p.Proposals[0].SourceRevision = "r0" },
		"conflicting mapping":    func(p *Packet) { p.Proposals[0].Mappings = append(p.Proposals[0].Mappings, p.Proposals[0].Mappings[0]) },
		"unknown mapping":        func(p *Packet) { p.Proposals[0].Mappings[0].Field = "private" },
		"status":                 func(p *Packet) { p.Proposals[0].Mappings[1].Value = "closed" },
		"tombstone proposal":     func(p *Packet) { p.Snapshot.Resources[0].Deleted = true },
		"complete cursor":        func(p *Packet) { p.Snapshot.Cursor = "next" },
		"dangling relation":      func(p *Packet) { p.Snapshot.Relationships = []Relationship{{"A", "B", "depends_on"}} },
		"resource cap": func(p *Packet) {
			for len(p.Snapshot.Resources) < 257 {
				p.Snapshot.Resources = append(p.Snapshot.Resources, p.Snapshot.Resources[0])
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := fixture()
			change(&p)
			if _, err := inspectFixture(p); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	p := fixture()
	data, _ := json.Marshal(p)
	if _, err := Inspect(data, Project{"example", "tenant", "other"}); err == nil {
		t.Fatal("expected project ignored")
	}
}

func TestInspectRejectsHostileJSON(t *testing.T) {
	p := fixture()
	data, _ := json.Marshal(p)
	for _, data := range [][]byte{
		[]byte(`{"schema_version":1,"schema_version":1}`),
		[]byte(`{"a":1,"\u0061":2}`),
		bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"SCHEMA_VERSION":1`), 1),
		append(append([]byte{}, data...), []byte(` {}`)...),
		bytes.Replace(data, []byte(`"complete":true,`), nil, 1),
		bytes.Replace(data, []byte(`"title":"Example"`), []byte(`"title":null`), 1),
		bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"secret=DO_NOT_PRINT":1`), 1),
		[]byte(strings.Repeat("[", MaxDepth+2) + "1" + strings.Repeat("]", MaxDepth+2)),
		bytes.Repeat([]byte(" "), MaxBytes+1),
		[]byte(`{"x":"` + strings.Repeat("x", MaxTextBytes+1) + `"}`),
	} {
		if _, err := Inspect(data, p.Project); err == nil {
			t.Fatal("accepted malformed data")
		} else if strings.Contains(err.Error(), "DO_NOT_PRINT") {
			t.Fatal("leaked input")
		}
	}
}

func TestInspectRedactsAndPreservesOpaqueNumbers(t *testing.T) {
	p := fixture()
	p.Snapshot.Resources[0].Ref.URL = "https://user:DO_NOT_PRINT@example.test/A?custom=DO_NOT_PRINT#DO_NOT_PRINT"
	p.Snapshot.Resources[0].Fields = map[string]any{"token": "DO_NOT_PRINT", "nested": map[string]any{"credentials": "DO_NOT_PRINT", "link": "HTTPS://user@example.test/?x=DO_NOT_PRINT"}, "big": json.Number("9007199254740993")}
	p.Proposals[0].Mappings[0].Value = "password=DO_NOT_PRINT"
	r, err := inspectFixture(p)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(r)
	if bytes.Contains(data, []byte("DO_NOT_PRINT")) || bytes.Contains(data, []byte("user@")) {
		t.Fatalf("leaked: %s", data)
	}
	if !bytes.Contains(data, []byte("9007199254740993")) {
		t.Fatal("lost opaque number precision")
	}
	if len(r.ContentSHA256) != 64 {
		t.Fatal("missing hash")
	}
}

func TestInspectDeterminismAndObservations(t *testing.T) {
	p := fixture()
	p.Snapshot.Complete = false
	p.Snapshot.Cursor = "next"
	r, err := inspectFixture(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Diagnostics) != 4 {
		t.Fatalf("diagnostics: %+v", r.Diagnostics)
	}
	p.Manifest.Capabilities[0], p.Manifest.Capabilities[1] = p.Manifest.Capabilities[1], p.Manifest.Capabilities[0]
	p.Proposals[0].Mappings[0], p.Proposals[0].Mappings[1] = p.Proposals[0].Mappings[1], p.Proposals[0].Mappings[0]
	s, err := inspectFixture(p)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(s)
	if !bytes.Equal(a, b) {
		t.Fatalf("not deterministic\n%s\n%s", a, b)
	}
	p.Snapshot.Resources[0].Deleted = true
	p.Proposals = []Proposal{}
	r, err = inspectFixture(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(r)), "provider_tombstone_only") {
		t.Fatal("missing tombstone diagnostic")
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
