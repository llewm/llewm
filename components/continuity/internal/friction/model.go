package friction

import "time"

const (
	DefaultMaxBytes         = 8192
	MaxRenderBytes          = 32768
	DefaultLimit            = 20
	DefaultObservationLimit = 6
	MaxScannedEvents        = 5000
)

type Options struct {
	MinCount         int
	Limit            int
	ObservationLimit int
	MaxBytes         int
}

type Candidate struct {
	ID                  string        `json:"id"`
	CommandFamily       string        `json:"command_family"`
	FailureSignature    string        `json:"failure_signature"`
	RecoveryShape       string        `json:"recovery_shape"`
	OccurrenceCount     int           `json:"occurrence_count"`
	FirstSeenAt         time.Time     `json:"first_seen_at"`
	LastSeenAt          time.Time     `json:"last_seen_at"`
	Clients             []string      `json:"clients"`
	TaskIDs             []string      `json:"task_ids"`
	ObservationRefs     []string      `json:"observation_refs"`
	DimensionsTruncated bool          `json:"dimensions_truncated"`
	Cause               string        `json:"cause"`
	TrackedBy           []TrackedItem `json:"tracked_by,omitempty"`
}

type Report struct {
	SchemaVersion  int         `json:"schema_version"`
	Candidates     []Candidate `json:"candidates"`
	CandidateCount int         `json:"candidate_count"`
	IncludedCount  int         `json:"included_count"`
	EventsScanned  int         `json:"events_scanned"`
	EventsTotal    int         `json:"events_total"`
	ScanTruncated  bool        `json:"scan_truncated"`
	Markdown       string      `json:"markdown"`
	MaxBytes       int         `json:"max_bytes"`
	RenderedBytes  int         `json:"rendered_bytes"`
	Truncated      bool        `json:"truncated"`
	Authority      string      `json:"authority"`
	Disclaimer     string      `json:"disclaimer"`
}

// TrackedItem is a work item linked derived_from this candidate.
type TrackedItem struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}
