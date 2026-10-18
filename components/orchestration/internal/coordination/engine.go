package coordination

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrForbidden = errors.New("permission denied")
	ErrConflict  = errors.New("state changed; refresh the plan")
	ErrNotFound  = errors.New("not found")
	idPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

const AttemptTTL = 30 * time.Minute

func validID(s string) bool { return idPattern.MatchString(s) }

func (p *Plan) definition() *Definition {
	if p.AcceptedRevision == 0 {
		return nil
	}
	return &p.Revisions[p.AcceptedRevision-1].Definition
}

func (p *Plan) item(id string) *Item {
	if d := p.definition(); d != nil {
		for i := range d.Items {
			if d.Items[i].ID == id {
				return &d.Items[i]
			}
		}
	}
	return nil
}

func (p *Plan) assignment(item string) (Assignment, bool) {
	if w := p.Work[item]; w != nil && w.Assignment != nil {
		return *w.Assignment, false
	}
	return p.Assignment, true
}

func (p *Plan) dependenciesReady(item *Item) bool {
	if item.BlockedReason != "" {
		return false
	}
	for _, id := range item.DependsOn {
		if p.Work[id] == nil || p.Work[id].CompletedBy == "" {
			return false
		}
	}
	return true
}

// stale is evaluated at resume, submission, review and read time. Stored receipt
// status alone never determines whether a contribution is still authoritative.
func (p *Plan) stale(a *Attempt, now time.Time) string {
	assignment, inherited := p.assignment(a.ItemID)
	if a.AssignmentEpoch != assignment.Epoch || a.Inherited != inherited {
		owner := assignment.Engineer
		if owner == "" {
			owner = "the open pool"
		}
		return "assignment changed; current owner: " + owner
	}
	if a.Revision != p.AcceptedRevision {
		return "accepted plan revision changed"
	}
	w := p.Work[a.ItemID]
	if w == nil || p.item(a.ItemID) == nil {
		return "item removed from accepted plan"
	}
	if w.CompletedBy != "" {
		return "item already completed"
	}
	if a.Released || w.ActiveAttempt != a.ID || w.Generation != a.Generation {
		return "attempt released or replaced"
	}
	if !now.Before(a.ValidUntil) {
		return "attempt lease expired; claim a new attempt"
	}
	return ""
}

func (p *Plan) view(member Member, now time.Time) View {
	v := View{SchemaVersion: SchemaVersion, Plan: p, Ready: []string{}, Attempts: []AttemptStatus{}, Submissions: []SubmissionStatus{}}
	if d := p.definition(); d != nil && member.Can("work") {
		for _, item := range d.Items {
			w := p.Work[item.ID]
			assignment, _ := p.assignment(item.ID)
			if w.CompletedBy != "" || (assignment.Engineer != "" && assignment.Engineer != member.ID) || !p.dependenciesReady(&item) {
				continue
			}
			if a := p.Attempts[w.ActiveAttempt]; a == nil || p.stale(a, now) != "" {
				v.Ready = append(v.Ready, item.ID)
			}
		}
	}
	for _, a := range p.Attempts {
		if a.Engineer != member.ID {
			continue
		}
		reason := p.stale(a, now)
		if !member.Can("work") {
			reason = "work permission revoked"
		}
		assignment, _ := p.assignment(a.ItemID)
		v.Attempts = append(v.Attempts, AttemptStatus{AttemptID: a.ID, CanResume: reason == "", Reason: reason, CurrentOwner: assignment.Engineer})
	}
	sort.Slice(v.Attempts, func(i, j int) bool { return v.Attempts[i].AttemptID < v.Attempts[j].AttemptID })
	for _, c := range p.Contributions {
		if c.Kind != "result" {
			continue
		}
		status := SubmissionStatus{ID: c.ID, PendingReview: true, StaleReason: p.stale(p.Attempts[c.AttemptID], now)}
		if status.StaleReason == "" {
			status.StaleReason = c.StaleAtReceipt
		}
		for _, r := range p.Reviews {
			if r.SubmissionID == c.ID {
				status.PendingReview = false
				status.Decision = r.Decision
				status.StaleReason = ""
				break
			}
		}
		v.Submissions = append(v.Submissions, status)
	}
	return v
}

func apply(p *Plan, member Member, c Command, now time.Time, newID string) (Receipt, error) {
	r := Receipt{SchemaVersion: SchemaVersion, RequestID: c.RequestID, Actor: member.ID, PlanID: c.PlanID, Status: "recorded"}
	permission := map[string]string{"propose": "plan", "accept-plan": "accept", "assign": "assign", "claim": "work", "resume": "work", "release": "work", "submit": "work", "review": "review"}[c.Action]
	if permission == "" {
		return r, errors.New("unknown action")
	}
	if !member.Can(permission) {
		return r, ErrForbidden
	}
	// Offline contributions and resume/release checks name an immutable attempt.
	// All other changes compare the exact shared plan version read by the caller.
	if c.Action != "submit" && c.Action != "resume" && c.Action != "release" && c.BaseVersion != p.Version {
		return r, ErrConflict
	}
	switch c.Action {
	case "propose":
		if c.Definition == nil {
			return r, errors.New("definition required")
		}
		if err := validateDefinition(*c.Definition); err != nil {
			return r, err
		}
		p.Revisions = append(p.Revisions, Revision{Number: len(p.Revisions) + 1, Author: member.ID, At: now, Definition: *c.Definition})
	case "accept-plan":
		if c.Revision != len(p.Revisions) || c.Revision <= p.AcceptedRevision {
			return r, errors.New("accept the latest unaccepted proposal")
		}
		d := p.Revisions[c.Revision-1].Definition
		// Completed scope cannot silently change meaning or disappear. Rework is
		// a new item referencing the earlier result, retaining its original ID.
		for id, w := range p.Work {
			if w.CompletedBy == "" {
				continue
			}
			var replacement *Item
			for i := range d.Items {
				if d.Items[i].ID == id {
					replacement = &d.Items[i]
				}
			}
			if replacement == nil || !reflect.DeepEqual(p.item(id), replacement) {
				return r, fmt.Errorf("completed item %s must retain its accepted definition", id)
			}
		}
		for _, item := range d.Items {
			if len(item.PriorCompletionRefs) > 0 && p.AcceptedRevision > 0 {
				old := p.item(item.ID)
				if old == nil || !reflect.DeepEqual(old.PriorCompletionRefs, item.PriorCompletionRefs) {
					return r, errors.New("prior completion evidence is only accepted in the initial imported baseline")
				}
			}
			if p.Work[item.ID] == nil {
				p.Work[item.ID] = &Work{}
			}
			if p.AcceptedRevision == 0 && len(item.PriorCompletionRefs) > 0 {
				p.Work[item.ID].CompletedBy = "source:" + item.ID
			}
		}
		p.AcceptedRevision = c.Revision
		p.Acceptances = append(p.Acceptances, Acceptance{Revision: c.Revision, Reviewer: member.ID, At: now})
	case "assign":
		if p.definition() == nil || strings.TrimSpace(c.Reason) == "" {
			return r, errors.New("accepted plan and assignment reason required")
		}
		if c.ItemID == "" {
			p.Assignment = Assignment{Epoch: p.Assignment.Epoch + 1, Engineer: c.Engineer, AssignedBy: member.ID, Reason: c.Reason, At: now}
		} else {
			w := p.Work[c.ItemID]
			if p.item(c.ItemID) == nil || w == nil {
				return r, ErrNotFound
			}
			if w.CompletedBy != "" {
				return r, errors.New("completed work cannot be reassigned")
			}
			// The scope discriminator prevents equal plan/item epochs from
			// accidentally making an inherited attempt authoritative again.
			epoch := 1
			if w.Assignment != nil {
				epoch = w.Assignment.Epoch + 1
			}
			w.Assignment = &Assignment{Epoch: epoch, Engineer: c.Engineer, AssignedBy: member.ID, Reason: c.Reason, At: now}
		}
	case "claim":
		item := p.item(c.ItemID)
		if item == nil {
			return r, ErrNotFound
		}
		if !validID(c.Machine) || !validID(c.Agent) {
			return r, errors.New("machine and agent labels required (identifier syntax)")
		}
		w := p.Work[c.ItemID]
		assignment, inherited := p.assignment(c.ItemID)
		if assignment.Engineer != "" && assignment.Engineer != member.ID {
			return r, ErrForbidden
		}
		if w.CompletedBy != "" || !p.dependenciesReady(item) {
			return r, errors.New("item is completed or dependencies are unfinished")
		}
		if a := p.Attempts[w.ActiveAttempt]; a != nil && p.stale(a, now) == "" {
			return r, fmt.Errorf("%w: item has a live attempt", ErrConflict)
		}
		w.Generation++
		a := &Attempt{ID: newID, ItemID: c.ItemID, Engineer: member.ID, Machine: c.Machine, Agent: c.Agent, Revision: p.AcceptedRevision, AssignmentEpoch: assignment.Epoch, Inherited: inherited, Generation: w.Generation, StartedAt: now, ValidUntil: now.Add(AttemptTTL)}
		p.Attempts[a.ID] = a
		w.ActiveAttempt = a.ID
		r.AttemptID = a.ID
	case "resume", "release":
		a := p.Attempts[c.AttemptID]
		if a == nil {
			return r, ErrNotFound
		}
		if a.Engineer != member.ID {
			return r, ErrForbidden
		}
		r.AttemptID = a.ID
		if reason := p.stale(a, now); reason != "" {
			return r, fmt.Errorf("%w: %s", ErrConflict, reason)
		}
		if c.Action == "resume" {
			a.ValidUntil = now.Add(AttemptTTL)
			r.Status = "resumable"
		} else {
			a.Released = true
			r.Status = "released"
		}
	case "submit":
		a := p.Attempts[c.AttemptID]
		if a == nil {
			return r, ErrNotFound
		}
		if a.Engineer != member.ID {
			return r, ErrForbidden
		}
		if c.Kind != "checkpoint" && c.Kind != "result" {
			return r, errors.New("kind must be checkpoint or result")
		}
		if strings.TrimSpace(c.Context) == "" {
			return r, errors.New("context required")
		}
		if c.Kind == "result" && (len(c.ArtifactRefs) == 0 || len(c.VerificationRefs) == 0) {
			return r, errors.New("results require artifact and verification references")
		}
		if c.Usage != nil && (c.Usage.Provider == "" || c.Usage.Model == "" || c.Usage.InputTokens < 0 || c.Usage.OutputTokens < 0 || (c.Usage.Confidence != "reported" && c.Usage.Confidence != "estimated")) {
			return r, errors.New("usage needs provider, model, nonnegative counts and reported/estimated confidence")
		}
		stale := p.stale(a, now)
		p.Contributions = append(p.Contributions, Contribution{ID: newID, AttemptID: a.ID, Engineer: member.ID, Kind: c.Kind, Context: c.Context, ArtifactRefs: c.ArtifactRefs, VerificationRefs: c.VerificationRefs, Usage: c.Usage, ReceivedAt: now, StaleAtReceipt: stale})
		r.AttemptID = a.ID
		r.SubmissionID = newID
		r.Status = "pending_review"
		if c.Kind == "checkpoint" {
			r.Status = "checkpointed"
		}
		if stale != "" {
			r.Status = "needs_reconciliation"
			r.Notice = stale
		}
	case "review":
		var submission *Contribution
		for i := range p.Contributions {
			if p.Contributions[i].ID == c.SubmissionID {
				submission = &p.Contributions[i]
				break
			}
		}
		if submission == nil {
			return r, ErrNotFound
		}
		if submission.Kind != "result" {
			return r, errors.New("checkpoints are context, not completion submissions")
		}
		for _, review := range p.Reviews {
			if review.SubmissionID == c.SubmissionID {
				return r, errors.New("submission already reviewed")
			}
		}
		if strings.TrimSpace(c.Reason) == "" {
			return r, errors.New("review reason required")
		}
		if c.Decision != "accept" && c.Decision != "adopt" && c.Decision != "reject" {
			return r, errors.New("decision must be accept, adopt or reject")
		}
		a := p.Attempts[submission.AttemptID]
		w := p.Work[a.ItemID]
		if c.Decision != "reject" {
			if p.item(a.ItemID) == nil || w.CompletedBy != "" || !p.dependenciesReady(p.item(a.ItemID)) {
				return r, errors.New("item removed, completed, or dependencies unfinished")
			}
			stale := p.stale(a, now)
			if c.Decision == "accept" && (stale != "" || submission.StaleAtReceipt != "") {
				return r, errors.New("stale submission requires explicit adopt or reject")
			}
			if len(c.VerificationRefs) == 0 {
				return r, errors.New("review must cite verification of the current acceptance criteria and integration")
			}
			w.CompletedBy = submission.ID
		}
		p.Reviews = append(p.Reviews, Review{SubmissionID: submission.ID, Decision: c.Decision, Reviewer: member.ID, Reason: c.Reason, VerificationRefs: c.VerificationRefs, At: now})
		r.SubmissionID = submission.ID
		r.Status = c.Decision
	}
	p.Version++
	r.Version = p.Version
	return r, nil
}

func validateDefinition(d Definition) error {
	if strings.TrimSpace(d.Title) == "" || len(d.Items) == 0 || len(d.Items) > 256 {
		return errors.New("plan needs title and 1..256 items")
	}
	items := map[string]Item{}
	for _, item := range d.Items {
		if !validID(item.ID) || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.AcceptanceCriteria) == "" {
			return errors.New("items require identifier, title and acceptance criteria")
		}
		if _, ok := items[item.ID]; ok {
			return errors.New("duplicate item identifier")
		}
		items[item.ID] = item
	}
	for _, item := range d.Items {
		if len(item.PriorCompletionRefs) == 0 {
			continue
		}
		if d.SourceRef == "" || item.BlockedReason != "" {
			return errors.New("prior completion requires source_ref and an unblocked item")
		}
		for _, ref := range item.PriorCompletionRefs {
			if strings.TrimSpace(ref) == "" {
				return errors.New("empty prior completion evidence")
			}
		}
		for _, dep := range item.DependsOn {
			if len(items[dep].PriorCompletionRefs) == 0 {
				return errors.New("completed baseline item has an unfinished dependency")
			}
		}
	}
	seen := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		item, ok := items[id]
		if !ok {
			return fmt.Errorf("missing dependency %s", id)
		}
		if seen[id] == 1 {
			return errors.New("dependency cycle")
		}
		if seen[id] == 2 {
			return nil
		}
		seen[id] = 1
		for _, dep := range item.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		seen[id] = 2
		return nil
	}
	for id := range items {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
