package trackplan

import (
	"time"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/lease"
)

const SchemaVersion = 1

type Plan struct {
	BaseRevision  int       `json:"base_revision,omitempty"`
	Change        *Change   `json:"change,omitempty"`
	ProjectID     string    `json:"project_id"`
	ID            string    `json:"id"`
	Revision      int       `json:"revision"`
	RoadmapNodeID string    `json:"roadmap_node_id"`
	TaskID        string    `json:"task_id,omitempty"`
	Title         string    `json:"title"`
	Summary       string    `json:"summary"`
	Status        string    `json:"status"`
	Creator       string    `json:"creator"`
	Model         string    `json:"model,omitempty"`
	Authority     string    `json:"authority"`
	WorkItemIDs   []string  `json:"work_item_ids"`
	Items         []Item    `json:"items"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
	// Fence is the lease the writer presents for the plan; a required scope
	// refuses the revision without it. Lease is the stamp the revision got.
	Fence lease.Fence  `json:"-"`
	Lease *lease.Stamp `json:"lease,omitempty"`
}

type Item struct {
	ProgressNote       string   `json:"progress_note,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	ID                 string   `json:"id"`
	Position           int      `json:"position"`
	Title              string   `json:"title"`
	Detail             string   `json:"detail"`
	Status             string   `json:"status"`
	ParentID           string   `json:"parent_id,omitempty"`
	DependsOn          []string `json:"depends_on"`
	EvidenceRefs       []string `json:"evidence_refs"`
	WorkItemIDs        []string `json:"work_item_ids"`
}

type PlanListOptions struct {
	Status, RoadmapNodeID, TaskID string
	Limit                         int
}

type PlanHistoryOptions struct {
	Limit  int
	Offset int
}

type PlanRevisionSummary struct {
	ID            string    `json:"id"`
	Revision      int       `json:"revision"`
	RoadmapNodeID string    `json:"roadmap_node_id"`
	TaskID        string    `json:"task_id,omitempty"`
	Title         string    `json:"title"`
	Status        string    `json:"status"`
	ItemCount     int       `json:"item_count"`
	RecordedAt    time.Time `json:"recorded_at"`
}

type PlanHistoryPage struct {
	Revisions []PlanRevisionSummary
	Total     int
	Limit     int
	Offset    int
	HasMore   bool
}

type PlanSummary struct {
	EffectiveRevision int       `json:"effective_revision"`
	EffectiveStatus   string    `json:"effective_status"`
	ID                string    `json:"id"`
	Revision          int       `json:"revision"`
	RoadmapNodeID     string    `json:"roadmap_node_id"`
	TaskID            string    `json:"task_id,omitempty"`
	Title             string    `json:"title"`
	Status            string    `json:"status"`
	ItemCount         int       `json:"item_count"`
	ReadyCount        int       `json:"ready_count"`
	RecordedAt        time.Time `json:"recorded_at"`
}

type PlanListPage struct {
	Plans     []PlanSummary
	Total     int
	Limit     int
	Truncated bool
}

type ReadyItem struct {
	PlanID        string   `json:"plan_id"`
	PlanRevision  int      `json:"plan_revision"`
	RoadmapNodeID string   `json:"roadmap_node_id"`
	TaskID        string   `json:"task_id,omitempty"`
	Item          Item     `json:"item"`
	Reasons       []string `json:"reasons"`
}

// Outline is a plan revision without item detail, evidence, or work items: what
// most plan reads need. Plan reads return it unless detail is requested.
type Outline struct {
	Change           *Change       `json:"change,omitempty"`
	ID               string        `json:"id"`
	Revision         int           `json:"revision"`
	RoadmapNodeID    string        `json:"roadmap_node_id"`
	TaskID           string        `json:"task_id,omitempty"`
	Title            string        `json:"title"`
	Summary          string        `json:"summary"`
	SummaryTruncated bool          `json:"summary_truncated,omitempty"`
	Status           string        `json:"status"`
	Creator          string        `json:"creator"`
	Model            string        `json:"model,omitempty"`
	Authority        string        `json:"authority"`
	RecordedAt       time.Time     `json:"recorded_at"`
	Items            []ItemOutline `json:"items"`
}

type ItemOutline struct {
	ID        string   `json:"id"`
	Position  int      `json:"position"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	ParentID  string   `json:"parent_id,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// OutlineSummaryBytes bounds the plan summary in an outline; summaries that
// accumulate revision notes can outgrow every item title together.
const OutlineSummaryBytes = 512

func (p Plan) Outline() Outline {
	summary, truncated := truncateBytes(p.Summary, OutlineSummaryBytes)
	outline := Outline{Change: p.Change, ID: p.ID, Revision: p.Revision, RoadmapNodeID: p.RoadmapNodeID, TaskID: p.TaskID, Title: p.Title, Summary: summary, SummaryTruncated: truncated, Status: p.Status, Creator: p.Creator, Model: p.Model, Authority: p.Authority, RecordedAt: p.RecordedAt, Items: make([]ItemOutline, 0, len(p.Items))}
	for _, item := range p.Items {
		outline.Items = append(outline.Items, ItemOutline{ID: item.ID, Position: item.Position, Title: item.Title, Status: item.Status, ParentID: item.ParentID, DependsOn: item.DependsOn})
	}
	return outline
}

func (p Plan) RevisionSummary() PlanRevisionSummary {
	return PlanRevisionSummary{ID: p.ID, Revision: p.Revision, RoadmapNodeID: p.RoadmapNodeID, TaskID: p.TaskID, Title: p.Title, Status: p.Status, ItemCount: len(p.Items), RecordedAt: p.RecordedAt}
}

// Item returns the plan's item with the given ID.
func (p Plan) Item(id string) (Item, bool) {
	for _, item := range p.Items {
		if item.ID == id {
			return item, true
		}
	}
	return Item{}, false
}

func truncateBytes(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…", true
}
