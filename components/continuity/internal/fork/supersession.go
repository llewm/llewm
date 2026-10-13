package fork

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// FamilySupersession is a record that machines each superseded before syncing.
const FamilySupersession = "supersession"

// Supersedable record families, named as their references are (decision:ID,
// narrative:ID for work narratives).
var supersedableTables = map[string]string{
	"decision":  "decision_records",
	"narrative": "work_narratives",
}

// SupersessionHead is one current successor chain's tip.
type SupersessionHead struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	RecordedAt time.Time `json:"recorded_at"`
	// Depth is how many supersessions lead to this head from the forked record.
	Depth int `json:"depth"`
}

// SupersessionFork is one record with more than one current successor chain.
// Heads are newest first (ID breaks ties); the first is the proposed head.
type SupersessionFork struct {
	RecordFamily string             `json:"record_family"`
	RecordID     string             `json:"record_id"`
	Title        string             `json:"title"`
	Heads        []SupersessionHead `json:"heads"`
}

// Ref is the fork's reference, as `fork resolve` takes it.
func (f SupersessionFork) Ref() string { return f.RecordFamily + ":" + f.RecordID }

// Proposed is the head a resolution would accept by default.
func (f SupersessionFork) Proposed() SupersessionHead { return f.Heads[0] }

// ParseSupersessionSubject splits FAMILY:ID for a supersedable family.
func ParseSupersessionSubject(value string) (string, string, error) {
	family, id, ok := strings.Cut(strings.TrimSpace(value), ":")
	if _, known := supersedableTables[family]; !ok || !known || id == "" || strings.ContainsAny(id, ":@") {
		return "", "", fmt.Errorf("name a supersession fork as decision:ID or narrative:ID, not %q", value)
	}
	return family, id, nil
}

// SupersessionSubject reports whether value names a supersedable record.
func SupersessionSubject(value string) bool {
	_, _, err := ParseSupersessionSubject(value)
	return err == nil
}

type supersessionNode struct {
	head       SupersessionHead
	supersedes string
	children   []string
}

// SupersessionForks returns every decision and work narrative
// (or only subject, FAMILY:ID, when set) that more than one record superseded
// and whose successor chains still end in more than one head no resolution
// rejected. A fork nested inside another open fork is reported only as part of
// the outer one, whose heads include it.
func SupersessionForks(ctx context.Context, db queryer, projectID, subject string) ([]SupersessionFork, error) {
	families := []string{"decision", "narrative"}
	subjectID := ""
	if subject != "" {
		family, id, err := ParseSupersessionSubject(subject)
		if err != nil {
			return nil, err
		}
		families, subjectID = []string{family}, id
	}
	rejected, err := rejectedSupersessionHeads(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	var forks []SupersessionFork
	for _, family := range families {
		nodes, err := supersessionGraph(ctx, db, projectID, family)
		if err != nil {
			return nil, err
		}
		found := map[string]SupersessionFork{}
		for id, value := range nodes {
			if len(value.children) < 2 {
				continue
			}
			if heads := openHeads(nodes, id, rejected[family]); len(heads) > 1 {
				found[id] = SupersessionFork{RecordFamily: family, RecordID: id, Title: value.head.Title, Heads: heads}
			}
		}
		for id, value := range found {
			if nestedIn(nodes, id, found) || (subjectID != "" && id != subjectID) {
				continue
			}
			forks = append(forks, value)
		}
	}
	sort.Slice(forks, func(i, j int) bool { return forks[i].Ref() < forks[j].Ref() })
	return forks, nil
}

func supersessionGraph(ctx context.Context, db queryer, projectID, family string) (map[string]*supersessionNode, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,title,COALESCE(supersedes_id,''),recorded_at FROM `+supersedableTables[family]+` WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	nodes := map[string]*supersessionNode{}
	for rows.Next() {
		var value supersessionNode
		var recorded string
		if err := rows.Scan(&value.head.ID, &value.head.Title, &value.supersedes, &recorded); err != nil {
			rows.Close()
			return nil, err
		}
		if value.head.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			rows.Close()
			return nil, err
		}
		nodes[value.head.ID] = &value
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for id, value := range nodes {
		if parent, ok := nodes[value.supersedes]; ok {
			parent.children = append(parent.children, id)
		}
	}
	return nodes, nil
}

// openHeads returns the tips below id that no resolution rejected, newest first.
func openHeads(nodes map[string]*supersessionNode, id string, rejected map[string]bool) []SupersessionHead {
	var heads []SupersessionHead
	seen := map[string]bool{id: true}
	var walk func(string, int)
	walk = func(current string, depth int) {
		for _, child := range nodes[current].children {
			if seen[child] {
				continue
			}
			seen[child] = true
			if len(nodes[child].children) == 0 {
				if !rejected[child] {
					head := nodes[child].head
					head.Depth = depth + 1
					heads = append(heads, head)
				}
				continue
			}
			walk(child, depth+1)
		}
	}
	walk(id, 0)
	sort.Slice(heads, func(i, j int) bool {
		if !heads[i].RecordedAt.Equal(heads[j].RecordedAt) {
			return heads[i].RecordedAt.After(heads[j].RecordedAt)
		}
		return heads[i].ID > heads[j].ID
	})
	return heads
}

// nestedIn reports whether an ancestor of id is itself an open fork.
func nestedIn(nodes map[string]*supersessionNode, id string, forks map[string]SupersessionFork) bool {
	seen := map[string]bool{id: true}
	for current := nodes[id].supersedes; current != "" && !seen[current]; {
		seen[current] = true
		if _, ok := forks[current]; ok {
			return true
		}
		parent, ok := nodes[current]
		if !ok {
			return false
		}
		current = parent.supersedes
	}
	return false
}

// rejectedSupersessionHeads maps each family to the record IDs resolutions
// rejected.
func rejectedSupersessionHeads(ctx context.Context, db queryer, projectID string) (map[string]map[string]bool, error) {
	values, err := All(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	rejected := map[string]map[string]bool{}
	for _, value := range values {
		if value.Family != FamilySupersession {
			continue
		}
		family, _, err := ParseSupersessionSubject(value.Subject)
		if err != nil {
			continue
		}
		if rejected[family] == nil {
			rejected[family] = map[string]bool{}
		}
		for _, id := range value.RejectedHeads {
			rejected[family][id] = true
		}
	}
	return rejected, nil
}
