package workstate

import (
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

const SchemaVersion = 1

type Counts struct {
	CurrentFocus int `json:"current_focus"`
	Active       int `json:"active"`
	Blocked      int `json:"blocked"`
	Resumable    int `json:"resumable"`
	Anomalous    int `json:"anomalous"`
	Ready        int `json:"ready"`
}

type Finding struct {
	Code         string   `json:"code"`
	Severity     string   `json:"severity"`
	Summary      string   `json:"summary"`
	RoadmapNode  string   `json:"roadmap_node_id,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
	CheckpointID string   `json:"checkpoint_id,omitempty"`
	SourceRefs   []string `json:"source_refs"`
}

type Check struct {
	SchemaVersion int       `json:"schema_version"`
	ProjectID     string    `json:"project_id"`
	Scope         string    `json:"scope"`
	State         string    `json:"state"`
	Findings      []Finding `json:"findings"`
	Total         int       `json:"total"`
	Truncated     bool      `json:"truncated"`
	Authority     string    `json:"authority"`
	Disclaimer    string    `json:"disclaimer"`
}

type Summary struct {
	SchemaVersion      int                          `json:"schema_version"`
	ProjectID          string                       `json:"project_id"`
	Checkpoint         *checkpoint.Checkpoint       `json:"checkpoint,omitempty"`
	RecommendedNext    string                       `json:"recommended_next,omitempty"`
	RoutingDecision    string                       `json:"routing_decision"`
	RoadmapFreshness   string                       `json:"roadmap_freshness"`
	RoadmapReviewDue   bool                         `json:"roadmap_review_due"`
	Counts             Counts                       `json:"counts"`
	CurrentWork        []roadmap.TrackInventoryItem `json:"current_work"`
	ReadyItems         []trackplan.ReadyItem        `json:"ready_items"`
	Findings           []Finding                    `json:"findings"`
	InventoryTruncated bool                         `json:"inventory_truncated"`
	FindingsTruncated  bool                         `json:"findings_truncated"`
	Authority          string                       `json:"authority"`
	Disclaimer         string                       `json:"disclaimer"`
}

type List struct {
	SchemaVersion    int                          `json:"schema_version"`
	ProjectID        string                       `json:"project_id"`
	View             string                       `json:"view"`
	Items            []roadmap.TrackInventoryItem `json:"items"`
	ReadyItems       []trackplan.ReadyItem        `json:"ready_items"`
	InventoryTotal   int                          `json:"inventory_total"`
	ReadyTotal       int                          `json:"ready_total"`
	InventoryOmitted int                          `json:"inventory_omitted"`
	ReadyOmitted     int                          `json:"ready_omitted"`
	Truncated        bool                         `json:"truncated"`
	Authority        string                       `json:"authority"`
	Disclaimer       string                       `json:"disclaimer"`
}

type Detail struct {
	SchemaVersion int                        `json:"schema_version"`
	ProjectID     string                     `json:"project_id"`
	Identifier    string                     `json:"identifier"`
	MatchedBy     string                     `json:"matched_by"`
	Item          roadmap.TrackInventoryItem `json:"item"`
	Authority     string                     `json:"authority"`
	Disclaimer    string                     `json:"disclaimer"`
}
