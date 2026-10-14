package trackplan

import (
	"fmt"
	"reflect"
	"sort"
)

// Policy governs planning state only. It never authorizes execution or grants
// permission to change the policy itself.
type Policy struct {
	Mode     string          `toml:"mode,omitempty" json:"mode,omitempty"`
	Progress string          `toml:"progress,omitempty" json:"progress,omitempty"`
	Allow    []string        `toml:"allow,omitempty" json:"allow,omitempty"`
	Tracks   map[string]Rule `toml:"tracks,omitempty" json:"tracks,omitempty"`
}
type Rule struct {
	Mode     string   `toml:"mode,omitempty" json:"mode"`
	Progress string   `toml:"progress,omitempty" json:"progress"`
	Allow    []string `toml:"allow,omitempty" json:"allow,omitempty"`
}
type Change struct {
	ComparisonRevision int      `json:"comparison_revision"`
	Kind               string   `json:"kind"`
	ParentRevision     int      `json:"parent_revision"`
	BaselineRevision   int      `json:"baseline_revision"`
	Categories         []string `json:"categories,omitempty"`
	Policy             Rule     `json:"policy"`
}

var changeKinds = []string{"create_plan", "add_items", "remove_items", "reorder_items", "dependencies", "scope", "acceptance_criteria"}

func overlay(a, b Rule) Rule {
	if b.Mode != "" {
		a.Mode = b.Mode
	}
	if b.Progress != "" {
		a.Progress = b.Progress
	}
	if b.Allow != nil {
		a.Allow = append([]string{}, b.Allow...)
	}
	return a
}
func (p Policy) For(node string) Rule {
	r := overlay(Rule{Mode: "review", Progress: "auto"}, Rule{p.Mode, p.Progress, p.Allow})
	return overlay(r, p.Tracks[node])
}
func (p Policy) Validate() error {
	for _, r := range append([]Rule{p.For("")}, func() []Rule {
		var a []Rule
		for node := range p.Tracks {
			a = append(a, p.For(node))
		}
		return a
	}()...) {
		if !oneOf(r.Mode, "review", "bounded", "agent") || !oneOf(r.Progress, "auto", "review") {
			return fmt.Errorf("plan policy mode must be review, bounded or agent; progress must be auto or review")
		}
		seen := map[string]bool{}
		for _, k := range r.Allow {
			if !oneOf(k, changeKinds...) || seen[k] {
				return fmt.Errorf("invalid or duplicate delegated plan change %q", k)
			}
			seen[k] = true
		}
	}
	return nil
}
func MergePolicy(global, project Policy) Policy {
	r := overlay(Rule{global.Mode, global.Progress, global.Allow}, Rule{project.Mode, project.Progress, project.Allow})
	p := Policy{Mode: r.Mode, Progress: r.Progress, Allow: r.Allow, Tracks: map[string]Rule{}}
	nodes := map[string]bool{}
	for node := range global.Tracks {
		nodes[node] = true
	}
	for node := range project.Tracks {
		nodes[node] = true
	}
	for node := range nodes {
		selected := overlay(Rule{global.Mode, global.Progress, global.Allow}, global.Tracks[node])
		selected = overlay(selected, Rule{project.Mode, project.Progress, project.Allow})
		p.Tracks[node] = overlay(selected, project.Tracks[node])
	}
	return p
}
func sameStrings(a, b []string) bool {
	a = append([]string{}, a...)
	b = append([]string{}, b...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}
func categories(base *Plan, next Plan) []string {
	if base == nil {
		return []string{"create_plan"}
	}
	set := map[string]bool{}
	if next.Status == "superseded" && base.Status != "superseded" {
		set["remove_items"] = true
	}
	if base.Status == "superseded" && next.Status != "superseded" {
		set["add_items"] = true
	}
	if base.Title != next.Title || base.RoadmapNodeID != next.RoadmapNodeID || base.TaskID != next.TaskID || !sameStrings(base.WorkItemIDs, next.WorkItemIDs) {
		set["scope"] = true
	}
	old := map[string]Item{}
	for _, item := range base.Items {
		old[item.ID] = item
	}
	for _, item := range next.Items {
		previous, ok := old[item.ID]
		if !ok {
			set["add_items"] = true
			continue
		}
		delete(old, item.ID)
		if previous.Title != item.Title || previous.Detail != item.Detail || !sameStrings(previous.WorkItemIDs, item.WorkItemIDs) {
			set["scope"] = true
		}
		if previous.AcceptanceCriteria != item.AcceptanceCriteria {
			set["acceptance_criteria"] = true
		}
		if previous.Position != item.Position {
			set["reorder_items"] = true
		}
		if previous.ParentID != item.ParentID || !sameStrings(previous.DependsOn, item.DependsOn) {
			set["dependencies"] = true
		}
		if item.Status == "superseded" && previous.Status != "superseded" {
			set["remove_items"] = true
		}
		if previous.Status == "superseded" && item.Status != "superseded" {
			set["add_items"] = true
		}
		for _, ref := range previous.EvidenceRefs {
			found := false
			for _, v := range item.EvidenceRefs {
				if v == ref {
					found = true
				}
			}
			if !found {
				set["scope"] = true
			}
		}
	}
	if len(old) > 0 {
		set["remove_items"] = true
	}
	keys := []string{}
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func permits(rule Rule, changes []string) bool {
	if rule.Mode == "agent" {
		return true
	}
	if rule.Mode != "bounded" {
		return false
	}
	for _, change := range changes {
		if !oneOf(change, rule.Allow...) {
			return false
		}
	}
	return true
}

// ValidateChange also verifies incoming portable records against their parent.
// Imported policy snapshots are historical provenance, not local grants.
func ValidateChange(value Plan, base *Plan) error {
	c := value.Change
	if c == nil {
		return nil
	}
	if value.Model != "" {
		if !oneOf(c.Kind, "progress", "delegated", "proposal") || (c.Kind == "proposal" && value.Status != "proposed") {
			return fmt.Errorf("model revision has invalid review disposition")
		}
	} else if oneOf(c.Kind, "progress", "delegated", "proposal") {
		return fmt.Errorf("model provenance is required")
	}
	if value.Status == "completed" {
		for _, item := range value.Items {
			if !oneOf(item.Status, "completed", "superseded") {
				return fmt.Errorf("completed plan has unfinished item %s", item.ID)
			}
		}
	}
	if c.ParentRevision != value.Revision-1 || c.BaselineRevision < 0 || c.BaselineRevision > c.ParentRevision || !oneOf(c.Kind, "proposal", "progress", "delegated", "review", "revision") {
		return fmt.Errorf("invalid plan change ancestry")
	}
	comparison := 0
	if base != nil {
		comparison = base.Revision
	}
	if c.ComparisonRevision != comparison || comparison > c.ParentRevision {
		return fmt.Errorf("invalid plan comparison baseline")
	}
	baseline := 0
	if base != nil {
		if base.Model == "" && oneOf(base.Status, "accepted", "active", "completed") {
			baseline = base.Revision
		} else if base.Model != "" && base.Change != nil {
			baseline = base.Change.BaselineRevision
		}
	}
	if c.BaselineRevision != baseline {
		return fmt.Errorf("plan approved baseline provenance does not match")
	}
	if err := (Policy{Mode: c.Policy.Mode, Progress: c.Policy.Progress, Allow: c.Policy.Allow}).Validate(); err != nil {
		return err
	}
	observed := categories(base, value)
	seenCategories := map[string]bool{}
	for _, kind := range c.Categories {
		if !oneOf(kind, changeKinds...) || seenCategories[kind] {
			return fmt.Errorf("unknown plan change category")
		}
		seenCategories[kind] = true
	}
	for _, kind := range observed {
		if !oneOf(kind, c.Categories...) {
			return fmt.Errorf("plan change categories omit a structural change")
		}
	}
	if c.Kind == "progress" {
		if value.Model == "" || c.Kind != "progress" || len(c.Categories) != 0 || len(observed) != 0 || c.Policy.Progress != "auto" || base == nil || !oneOf(base.Status, "accepted", "active", "completed") {
			return fmt.Errorf("invalid autonomous progress")
		}
	} else if c.Kind == "delegated" {
		if value.Model == "" || c.Kind != "delegated" || !permits(c.Policy, c.Categories) {
			return fmt.Errorf("invalid delegated plan revision")
		}
	}
	if c.Kind == "progress" || c.Kind == "delegated" {
		for _, item := range value.Items {
			previous, exists := Item{}, false
			if base != nil {
				previous, exists = base.Item(item.ID)
			}
			if item.Status == "completed" && (!exists || previous.Status != "completed") && (item.ProgressNote == "" || len(item.EvidenceRefs) == 0) {
				return fmt.Errorf("completing item %s requires a progress note and evidence", item.ID)
			}
		}
	}
	return nil
}
