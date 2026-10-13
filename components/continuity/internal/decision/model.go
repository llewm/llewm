package decision

import (
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision/decisionhash"
)

const SchemaVersion = 1

type Link = decisionhash.Link

type Record struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Title         string    `json:"title"`
	Rationale     string    `json:"rationale"`
	Disposition   string    `json:"disposition"`
	Actor         string    `json:"actor"`
	ActorType     string    `json:"actor_type"`
	Authority     string    `json:"authority"`
	ReviewSource  string    `json:"review_source,omitempty"`
	SupersedesID  string    `json:"supersedes_id,omitempty"`
	Links         []Link    `json:"links"`
	RecordedAt    time.Time `json:"recorded_at"`
	RecordSHA256  string    `json:"record_sha256"`
	SchemaVersion int       `json:"schema_version"`
	Replay        bool      `json:"replay,omitempty"`
	SupersededBy  string    `json:"superseded_by,omitempty"`
}

type Summary struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Disposition   string    `json:"disposition"`
	Actor         string    `json:"actor"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	LinkCount     int       `json:"link_count"`
	SupersedesID  string    `json:"supersedes_id,omitempty"`
	SupersededBy  string    `json:"superseded_by,omitempty"`
	SchemaVersion int       `json:"schema_version"`
}

type ListOptions struct {
	Disposition    string
	TargetKind     string
	TargetID       string
	TargetRevision string
	Limit          int
	Offset         int
}

type ListPage struct {
	SchemaVersion int       `json:"schema_version"`
	Decisions     []Summary `json:"decisions"`
	Total         int       `json:"total"`
	Limit         int       `json:"limit"`
	Offset        int       `json:"offset"`
	Truncated     bool      `json:"truncated"`
	BodiesLoaded  int       `json:"bodies_loaded"`
	Authority     string    `json:"authority"`
	Disclaimer    string    `json:"disclaimer"`
}

type Backlink struct {
	SourceKind       string    `json:"source_kind"`
	SourceID         string    `json:"source_id"`
	SourceRevision   string    `json:"source_revision,omitempty"`
	LinkKind         string    `json:"link_kind"`
	LinkStatus       string    `json:"link_status,omitempty"`
	TargetKind       string    `json:"target_kind"`
	TargetID         string    `json:"target_id"`
	TargetRevision   string    `json:"target_revision,omitempty"`
	Authority        string    `json:"authority"`
	TargetResolution string    `json:"target_resolution"`
	RecordedAt       time.Time `json:"recorded_at"`
}

type BacklinkPage struct {
	SchemaVersion    int        `json:"schema_version"`
	IncludedFamilies []string   `json:"included_families"`
	Backlinks        []Backlink `json:"backlinks"`
	Total            int        `json:"total"`
	Limit            int        `json:"limit"`
	Offset           int        `json:"offset"`
	Truncated        bool       `json:"truncated"`
	BodiesLoaded     int        `json:"bodies_loaded"`
	Authority        string     `json:"authority"`
	Disclaimer       string     `json:"disclaimer"`
}
