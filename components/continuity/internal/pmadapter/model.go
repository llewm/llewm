// Package pmadapter defines the untrusted, read-only PM interchange boundary.
// It has no database, provider transport, or execution dependency.
package pmadapter

const (
	Version      = 1
	MaxBytes     = 1 << 20
	MaxTextBytes = 16 << 10
	MaxDepth     = 16
)

type Project struct {
	Provider string `json:"provider"`
	Instance string `json:"instance"`
	Project  string `json:"project"`
}

type Manifest struct {
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	Provider         string   `json:"provider"`
	ContractVersions []int    `json:"contract_versions"`
	Capabilities     []string `json:"capabilities"`
	ResourceTypes    []string `json:"resource_types"`
	MappingProfile   string   `json:"mapping_profile"`
	MappingVersion   string   `json:"mapping_version"`
}

type Reference struct {
	Project    Project `json:"project"`
	Type       string  `json:"type"`
	ID         string  `json:"id"`
	URL        string  `json:"url,omitempty"`
	Revision   string  `json:"revision"`
	ObservedAt string  `json:"observed_at"`
}

type Resource struct {
	Ref     Reference      `json:"ref"`
	Deleted bool           `json:"deleted"`
	Fields  map[string]any `json:"fields"`
}

type Relationship struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type Snapshot struct {
	Complete      bool           `json:"complete"`
	Cursor        string         `json:"cursor,omitempty"`
	Resources     []Resource     `json:"resources"`
	Relationships []Relationship `json:"relationships"`
}

// Mapping is an adapter interpretation, never a reviewed local mutation.
type Mapping struct {
	Field     string `json:"field"`
	Value     string `json:"value"`
	Authority string `json:"authority"`
	Lossy     bool   `json:"lossy"`
}

type Proposal struct {
	ID             string    `json:"id"`
	Source         string    `json:"source"`
	SourceRevision string    `json:"source_revision"`
	Mappings       []Mapping `json:"mappings"`
}

type Packet struct {
	SchemaVersion int        `json:"schema_version"`
	Manifest      Manifest   `json:"manifest"`
	Project       Project    `json:"project"`
	Snapshot      Snapshot   `json:"snapshot"`
	Proposals     []Proposal `json:"proposals"`
}

type Diagnostic struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

type Report struct {
	SchemaVersion int          `json:"schema_version"`
	State         string       `json:"state"`
	ContentSHA256 string       `json:"content_sha256"`
	Packet        any          `json:"packet"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
	Authority     string       `json:"authority"`
}
