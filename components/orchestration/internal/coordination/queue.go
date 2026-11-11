package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// JobSpec is reviewed routing and scope, not an OS sandbox or permission grant.
// Manual items without a job spec never enter the client work queue.
type JobSpec struct {
	OS               string   `json:"os"`
	Arch             string   `json:"arch"`
	Capabilities     []string `json:"capabilities"`
	Priority         int      `json:"priority"` // 0 first, 100 last
	Repository       string   `json:"repository"`
	BaseRevision     string   `json:"base_revision"`
	AllowedChanges   []string `json:"allowed_changes"`
	ApprovalRequired []string `json:"approval_required"`
}

// WorkerProfile reports capabilities; authentication still determines authority.
type WorkerProfile struct {
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Capabilities []string `json:"capabilities"`
}

type QueueRequest struct {
	Worker  WorkerProfile `json:"worker"`
	PlanIDs []string      `json:"plan_ids"`
	Limit   int           `json:"limit"`
}

type QueuedJob struct {
	PlanID    string `json:"plan_id"`
	Version   int    `json:"version"`
	Revision  int    `json:"revision"`
	SourceRef string `json:"source_ref"`
	Position  int    `json:"position"`
	Item      Item   `json:"item"`
}

type Queue struct {
	SchemaVersion int         `json:"schema_version"`
	Jobs          []QueuedJob `json:"jobs"`
	Truncated     bool        `json:"truncated"`
}

func ValidateWorkerProfile(w WorkerProfile) error {
	if !validID(w.OS) || !validID(w.Arch) || len(w.Capabilities) > 32 {
		return errors.New("worker needs OS/architecture and at most 32 capabilities")
	}
	seen := map[string]bool{}
	for _, cap := range w.Capabilities {
		if !validID(cap) || seen[cap] {
			return errors.New("worker capabilities must be unique identifiers")
		}
		seen[cap] = true
	}
	return nil
}

func validateJob(j JobSpec) error {
	if err := ValidateWorkerProfile(WorkerProfile{OS: j.OS, Arch: j.Arch, Capabilities: j.Capabilities}); err != nil {
		return err
	}
	if j.Priority < 0 || j.Priority > 100 || strings.TrimSpace(j.Repository) == "" || strings.TrimSpace(j.BaseRevision) == "" || len(j.AllowedChanges) == 0 || len(j.AllowedChanges) > 32 || len(j.ApprovalRequired) > 32 {
		return errors.New("job needs priority 0..100, repository, baseline and bounded allowed changes")
	}
	for _, list := range [][]string{j.AllowedChanges, j.ApprovalRequired} {
		for _, value := range list {
			if strings.TrimSpace(value) == "" {
				return errors.New("job scope entries must be nonempty")
			}
		}
	}
	return nil
}

func (j JobSpec) matches(w WorkerProfile) bool {
	if j.OS != w.OS || j.Arch != w.Arch {
		return false
	}
	for _, required := range j.Capabilities {
		found := false
		for _, have := range w.Capabilities {
			if required == have {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// A result awaiting human review is not fresh work, even after its attempt
// expires. Explicit rejection permits another attempt; adoption completes it.
func (p *Plan) pendingResult(itemID string) bool {
	for _, c := range p.Contributions {
		a := p.Attempts[c.AttemptID]
		if c.Kind != "result" || c.StaleAtReceipt != "" || a == nil || a.ItemID != itemID {
			continue
		}
		assignment, inherited := p.assignment(itemID)
		w := p.Work[itemID]
		if w == nil || a.Revision != p.AcceptedRevision || a.Generation != w.Generation || a.AssignmentEpoch != assignment.Epoch || a.Inherited != inherited {
			continue
		}
		reviewed := false
		for _, r := range p.Reviews {
			if r.SubmissionID == c.ID {
				reviewed = true
				break
			}
		}
		if !reviewed {
			return true
		}
	}
	return false
}

// Queue reads one authenticated snapshot over an explicit client plan allowlist.
// Claim is a separate CAS operation; discovery never reserves or executes work.
func (s *Store) Queue(ctx context.Context, token string, q QueueRequest) (Queue, error) {
	result := Queue{SchemaVersion: SchemaVersion, Jobs: []QueuedJob{}}
	if err := ValidateWorkerProfile(q.Worker); err != nil {
		return result, err
	}
	if len(q.PlanIDs) == 0 || len(q.PlanIDs) > 32 || q.Limit < 1 || q.Limit > 128 {
		return result, errors.New("queue requires 1..32 allowed plans and limit 1..128")
	}
	seen := map[string]bool{}
	for _, id := range q.PlanIDs {
		if !validID(id) || seen[id] {
			return result, errors.New("queue plan IDs must be unique identifiers")
		}
		seen[id] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	member, err := authenticate(ctx, tx, token)
	if err != nil {
		return result, err
	}
	if !member.Can("work") {
		return result, ErrForbidden
	}
	for _, id := range q.PlanIDs {
		p, err := loadPlan(ctx, tx, id)
		if err != nil {
			return result, err
		}
		view := p.view(member, s.now())
		for _, itemID := range view.Ready {
			item := p.item(itemID)
			if item.Job == nil || !item.Job.matches(q.Worker) {
				continue
			}
			position := 0
			for i, v := range p.definition().Items {
				if v.ID == item.ID {
					position = i + 1
					break
				}
			}
			result.Jobs = append(result.Jobs, QueuedJob{PlanID: p.ID, Version: p.Version, Revision: p.AcceptedRevision, SourceRef: p.definition().SourceRef, Position: position, Item: *item})
		}
	}
	sort.Slice(result.Jobs, func(i, j int) bool {
		a, b := result.Jobs[i], result.Jobs[j]
		if a.Item.Job.Priority != b.Item.Job.Priority {
			return a.Item.Job.Priority < b.Item.Job.Priority
		}
		if a.PlanID != b.PlanID {
			return a.PlanID < b.PlanID
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.Item.ID < b.Item.ID
	})
	if len(result.Jobs) > q.Limit {
		result.Jobs = result.Jobs[:q.Limit]
		result.Truncated = true
	}
	// Keep the response within the existing protocol limit, even with large
	// reviewed contexts. A single job always fits the command-size bound.
	for len(result.Jobs) > 0 {
		body, err := json.Marshal(result)
		if err != nil {
			return Queue{}, err
		}
		if len(body) <= MaxCommandBytes {
			break
		}
		if len(result.Jobs) == 1 {
			return Queue{}, errors.New("queued job exceeds response bound; split its reviewed context")
		}
		result.Jobs = result.Jobs[:len(result.Jobs)-1]
		result.Truncated = true
	}
	return result, nil
}
