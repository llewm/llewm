package mcpserver

import (
	"strconv"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

// Receipt is what a write tool returns instead of echoing the record the caller
// just sent: what to cite, what state the write produced, and the record's
// hash. The read tools return whole records.
type Receipt struct {
	Ref        string                    `json:"ref" jsonschema:"the written record as kind:id, with @revision for revisioned records"`
	Status     string                    `json:"status,omitempty" jsonschema:"the record's status, disposition, state, operation, or link relationship"`
	Authority  string                    `json:"authority,omitempty"`
	SHA256     string                    `json:"sha256,omitempty"`
	Commit     string                    `json:"commit,omitempty"`
	RecordedAt time.Time                 `json:"recorded_at,omitzero"`
	Replay     bool                      `json:"replay,omitempty" jsonschema:"the identical record already existed; nothing new was written"`
	Lease      string                    `json:"lease,omitempty" jsonschema:"lease state of the write when the scope is leased or the write is stale"`
	Note       *worknarrative.NoteResult `json:"note,omitempty"`
}

// Receipts is the result of a write tool that can write several records.
type Receipts struct {
	Records []Receipt `json:"records"`
}

func revisionRef(kind, id string, revision int) string {
	return kind + ":" + id + "@" + strconv.Itoa(revision)
}

func leaseState(stamp *lease.Stamp) string {
	if stamp == nil || stamp.State == "unleased" {
		return ""
	}
	return stamp.State
}

func narrativeReceipt(value worknarrative.RecordResult) Receipt {
	receipt := Receipt{Ref: "narrative:" + value.ID, Status: value.State, SHA256: value.SemanticSHA256, RecordedAt: value.RecordedAt, Note: value.Note}
	if value.Note != nil {
		receipt.Commit = value.Note.Commit
	}
	return receipt
}

func decisionReceipt(value decision.Record) Receipt {
	return Receipt{Ref: "decision:" + value.ID, Status: value.Disposition, Authority: value.Authority, SHA256: value.RecordSHA256, RecordedAt: value.RecordedAt, Replay: value.Replay}
}

func planReceipt(value trackplan.Plan) Receipt {
	return Receipt{Ref: revisionRef("plan", value.ID, value.Revision), Status: value.Status, Authority: value.Authority, RecordedAt: value.RecordedAt, Lease: leaseState(value.Lease)}
}

func itemReceipt(value workitem.Item) Receipt {
	return Receipt{Ref: "work_item:" + value.ID, Status: value.Status, Authority: value.Authority, RecordedAt: value.RecordedAt}
}

func dispositionReceipt(value workitem.Disposition) Receipt {
	return Receipt{Ref: "work_item:" + value.ItemID, Status: value.Status, Authority: value.Authority, RecordedAt: value.RecordedAt}
}

func procedureReceipt(value procedures.Procedure) Receipt {
	return Receipt{Ref: revisionRef("procedure", value.ID, value.Revision), Status: value.Status, Authority: value.Authority, RecordedAt: value.RecordedAt}
}

func resourceReceipt(value resource.Resource) Receipt {
	return Receipt{Ref: revisionRef("resource", value.ID, value.Revision), Status: value.Status, Authority: value.Authority, RecordedAt: value.RecordedAt}
}

func resourceNoteReceipt(value resource.Note) Receipt {
	return Receipt{Ref: "resource_note:" + value.ID, Authority: value.Authority, RecordedAt: value.RecordedAt}
}

func roadmapNodeReceipt(value roadmap.Node) Receipt {
	return Receipt{Ref: revisionRef("roadmap_node", value.ID, value.Revision), Status: value.Status, Authority: value.Authority, RecordedAt: value.RecordedAt}
}

func actionReceipt(value checkpoint.Action) Receipt {
	return Receipt{Ref: "action:" + value.ID, Status: value.Status}
}
