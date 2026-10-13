// Package workitem stores ideas, bugs, and tasks as one kind-tagged type with
// append-only dispositions and links. Capturing an item never changes the
// active task, checkpoint, or roadmap state.
package workitem

import "time"

const SchemaVersion = 1

// Kinds lists the supported work-item kinds.
var Kinds = []string{"idea", "bug", "task", "research"}

// Severities lists the supported bug severities.
var Severities = []string{"low", "medium", "high", "critical"}

// Statuses lists the disposition statuses. pending (no disposition), accepted,
// and deferred are open; integrated, discarded, merged, and answered are
// terminal. answered closes a research item and links its findings.
var Statuses = []string{"accepted", "deferred", "integrated", "discarded", "merged", "answered"}

// Relationships lists the typed link relationships from an item to a target.
var Relationships = []string{"derived_from", "follow_up_to", "supports", "depends_on", "alternative_to", "duplicates", "belongs_to_track"}

type Item struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	Kind          string        `json:"kind"`
	Title         string        `json:"title"`
	Body          string        `json:"body"`
	Observed      string        `json:"observed,omitempty"`
	Reproduction  string        `json:"reproduction,omitempty"`
	Environment   string        `json:"environment,omitempty"`
	Severity      string        `json:"severity,omitempty"`
	TaskID        string        `json:"task_id,omitempty"`
	CheckpointID  string        `json:"checkpoint_id,omitempty"`
	RoadmapNodeID string        `json:"roadmap_node_id,omitempty"`
	Creator       string        `json:"creator"`
	Model         string        `json:"model,omitempty"`
	Authority     string        `json:"authority"`
	Tags          []string      `json:"tags"`
	Status        string        `json:"status"`
	RecordedAt    time.Time     `json:"recorded_at"`
	SchemaVersion int           `json:"schema_version"`
	Links         []Link        `json:"links,omitempty"`
	Dispositions  []Disposition `json:"dispositions,omitempty"`
}

type Disposition struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ItemID        string    `json:"item_id"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason"`
	TargetRefs    []string  `json:"target_refs"`
	Creator       string    `json:"creator"`
	Model         string    `json:"model,omitempty"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

type Link struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ItemID        string    `json:"item_id"`
	Relationship  string    `json:"relationship"`
	TargetRef     string    `json:"target_ref"`
	Creator       string    `json:"creator"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

// Capture is one capture request. From names a work_item:<id> this item
// promotes (that item gets an integrated disposition pointing here) or a
// friction:<id> candidate it tracks; either adds a derived_from link.
type Capture struct {
	Item
	From  string `json:"from,omitempty"`
	Links []Link `json:"links,omitempty"`
}

type ListOptions struct {
	Kind, Status, Tag, TaskID, RoadmapNodeID string
	Limit                                    int
}

type Summary struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	Status        string    `json:"status"`
	Severity      string    `json:"severity,omitempty"`
	Tags          []string  `json:"tags"`
	TaskID        string    `json:"task_id,omitempty"`
	RoadmapNodeID string    `json:"roadmap_node_id,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
}

type ListPage struct {
	Items     []Summary `json:"items"`
	Total     int       `json:"total"`
	Limit     int       `json:"limit"`
	Truncated bool      `json:"truncated"`
}

// Counts are open work items by kind, for the bootstrap.
type Counts struct {
	Open   int            `json:"open"`
	ByKind map[string]int `json:"by_kind"`
}

type Audit struct {
	Markdown      string `json:"markdown"`
	PendingCount  int    `json:"pending_count"`
	IncludedCount int    `json:"included_count"`
	RenderedBytes int    `json:"rendered_bytes"`
	Truncated     bool   `json:"truncated"`
	Authority     string `json:"authority"`
	Disclaimer    string `json:"disclaimer"`
}

type ShowResult struct {
	SchemaVersion   int     `json:"schema_version"`
	ID              string  `json:"id"`
	Metadata        Summary `json:"metadata"`
	Item            Item    `json:"item"`
	Markdown        string  `json:"markdown"`
	MaxBytes        int     `json:"max_bytes"`
	RenderedBytes   int     `json:"rendered_bytes"`
	EstimatedTokens int     `json:"estimated_tokens"`
	Truncated       bool    `json:"truncated"`
	Authority       string  `json:"authority"`
	Disclaimer      string  `json:"disclaimer"`
}
