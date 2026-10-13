package codestats

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

// Attribution maps full commit hashes to the record keys of one grouping:
// task and node from the task and roadmap_node anchors of narratives that
// anchor the commit, plus for node the roadmap node of a plan item citing it;
// plan-item from plan items whose evidence cites commit:HASH (a prefix of at
// least 7 characters matches). plans are the latest revision of each plan.
func Attribution(ctx context.Context, db *sql.DB, projectID, by string, plans []trackplan.Plan, commits []string) (map[string][]string, error) {
	keys := map[string]map[string]bool{}
	add := func(commit, key string) {
		if keys[commit] == nil {
			keys[commit] = map[string]bool{}
		}
		keys[commit][key] = true
	}
	if by == "task" || by == "node" {
		kind := "task"
		if by == "node" {
			kind = "roadmap_node"
		}
		rows, err := db.QueryContext(ctx, `SELECT c.target, a.target FROM work_narrative_anchors c
			JOIN work_narrative_anchors a ON a.project_id=c.project_id AND a.narrative_id=c.narrative_id
			WHERE c.project_id=? AND c.anchor_kind='commit' AND a.anchor_kind=?`, projectID, kind)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var commit, target string
			if err := rows.Scan(&commit, &target); err != nil {
				rows.Close()
				return nil, err
			}
			add(commit, target)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if by == "plan-item" || by == "node" {
		resolve := prefixResolver(commits)
		for _, plan := range plans {
			for _, item := range plan.Items {
				for _, ref := range item.EvidenceRefs {
					hash, ok := strings.CutPrefix(ref, "commit:")
					if !ok {
						continue
					}
					for _, commit := range resolve(hash) {
						if by == "node" {
							add(commit, plan.RoadmapNodeID)
						} else {
							add(commit, plan.ID+"/"+item.ID+" "+item.Title)
						}
					}
				}
			}
		}
	}
	result := make(map[string][]string, len(keys))
	for commit, set := range keys {
		for key := range set {
			result[commit] = append(result[commit], key)
		}
		sort.Strings(result[commit])
	}
	return result, nil
}

// prefixResolver matches a full or abbreviated hash (7 characters or more)
// against the collected commits.
func prefixResolver(commits []string) func(string) []string {
	sorted := append([]string{}, commits...)
	sort.Strings(sorted)
	return func(hash string) []string {
		hash = strings.ToLower(strings.TrimSpace(hash))
		if len(hash) < 7 {
			return nil
		}
		var matches []string
		for i := sort.SearchStrings(sorted, hash); i < len(sorted) && strings.HasPrefix(sorted[i], hash); i++ {
			if len(matches) == 0 || matches[len(matches)-1] != sorted[i] {
				matches = append(matches, sorted[i])
			}
		}
		return matches
	}
}
