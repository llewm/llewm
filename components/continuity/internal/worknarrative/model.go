package worknarrative

import "time"

const (
	SchemaVersion      = 1
	RecordType         = "work_narrative"
	DefaultListLimit   = 20
	MaxListLimit       = 100
	DefaultRenderBytes = 16 << 10
	MaxRenderBytes     = 32 << 10
	MaxRecordBytes     = 32 << 10
	MaxClaims          = 64
	MaxAnchors         = 64
)

// MaxClaimLabelBytes bounds the optional original section heading kept with a claim.
const MaxClaimLabelBytes = 128

type Claim struct {
	Kind string `json:"kind"`
	// Label preserves an original section heading. It is omitted when empty so
	// semantic hashes of earlier records are unchanged.
	Label     string `json:"label,omitempty"`
	Text      string `json:"text"`
	Authority string `json:"authority"`
}

type Anchor struct {
	Kind           string `json:"kind"`
	Target         string `json:"target"`
	TargetRevision string `json:"target_revision,omitempty"`
	ContentSHA256  string `json:"content_sha256,omitempty"`
	RegionLocator  string `json:"region_locator,omitempty"`
	Authority      string `json:"authority"`
	Drift          string `json:"drift,omitempty"`
}

type Record struct {
	ID                  string     `json:"id"`
	ProjectID           string     `json:"project_id"`
	RecordType          string     `json:"record_type"`
	Title               string     `json:"title"`
	Trigger             string     `json:"trigger"`
	OccurredFrom        *time.Time `json:"occurred_from,omitempty"`
	OccurredTo          *time.Time `json:"occurred_to,omitempty"`
	Claims              []Claim    `json:"claims"`
	Anchors             []Anchor   `json:"anchors"`
	Creator             string     `json:"creator"`
	CreatorInvocationID string     `json:"creator_invocation_id,omitempty"`
	SemanticSHA256      string     `json:"semantic_sha256"`
	SupersedesID        string     `json:"supersedes_id,omitempty"`
	State               string     `json:"state"`
	RecordedAt          time.Time  `json:"recorded_at"`
	SchemaVersion       int        `json:"schema_version"`
}

type Metadata struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	RecordType          string    `json:"record_type"`
	Title               string    `json:"title"`
	Trigger             string    `json:"trigger"`
	Creator             string    `json:"creator"`
	CreatorInvocationID string    `json:"creator_invocation_id,omitempty"`
	SemanticSHA256      string    `json:"semantic_sha256"`
	SupersedesID        string    `json:"supersedes_id,omitempty"`
	State               string    `json:"state"`
	RecordedAt          time.Time `json:"recorded_at"`
	SchemaVersion       int       `json:"schema_version"`
	Anchors             []Anchor  `json:"anchors"`
}

type ListOptions struct {
	RecordType string `json:"record_type,omitempty"`
	Trigger    string `json:"trigger,omitempty"`
	AnchorKind string `json:"anchor_kind,omitempty"`
	Anchor     string `json:"anchor,omitempty"`
	State      string `json:"state,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
}

type Measurement struct {
	ScopeRecords     int `json:"scope_records"`
	CandidateRecords int `json:"candidate_records"`
	RecordsReturned  int `json:"records_returned"`
	RecordsLoaded    int `json:"records_loaded"`
	MetadataBytes    int `json:"metadata_bytes"`
	ContentBytes     int `json:"content_bytes"`
	RenderedBytes    int `json:"rendered_bytes"`
	ExpansionDepth   int `json:"expansion_depth"`
}

type ListResult struct {
	SchemaVersion int         `json:"schema_version"`
	Records       []Metadata  `json:"records"`
	NextCursor    string      `json:"next_cursor,omitempty"`
	Measurement   Measurement `json:"measurement"`
	Authority     string      `json:"authority"`
	Disclaimer    string      `json:"disclaimer"`
}

type ShowResult struct {
	SchemaVersion int         `json:"schema_version"`
	ID            string      `json:"id"`
	Record        Record      `json:"record"`
	Markdown      string      `json:"markdown"`
	MaxBytes      int         `json:"max_bytes"`
	Truncated     bool        `json:"truncated"`
	Measurement   Measurement `json:"measurement"`
	Authority     string      `json:"authority"`
	Disclaimer    string      `json:"disclaimer"`
}
