package trackplan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	maxTextBytes = 16 << 10
	maxPlanBytes = 256 << 10
)

type Service struct {
	DB   *sql.DB
	Now  func() time.Time
	Root string
}

type planQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (service Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func clean(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > maxTextBytes {
		return "", fmt.Errorf("%s exceeds 16 KiB limit", name)
	}
	return value, nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// normalizeWorkItems strips work_item: prefixes from plan provenance.
func normalizeWorkItems(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.TrimPrefix(strings.TrimSpace(value), "work_item:")
	}
	return result
}

func cleanUnique(values []string, name string) ([]string, error) {
	if len(values) > 64 {
		return nil, fmt.Errorf("%s accepts at most 64 values", name)
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		cleaned, err := clean(name, value, true)
		if err != nil {
			return nil, err
		}
		if seen[cleaned] {
			return nil, fmt.Errorf("duplicate %s %q", name, cleaned)
		}
		seen[cleaned] = true
		result = append(result, cleaned)
	}
	sort.Strings(result)
	return result, nil
}

func (service Service) AppendPlan(ctx context.Context, value Plan) (Plan, error) {
	var err error
	for name, target := range map[string]*string{"project ID": &value.ProjectID, "plan ID": &value.ID, "roadmap node ID": &value.RoadmapNodeID, "task ID": &value.TaskID, "title": &value.Title, "summary": &value.Summary, "status": &value.Status, "creator": &value.Creator, "model": &value.Model} {
		required := name == "project ID" || name == "plan ID" || name == "roadmap node ID" || name == "title" || name == "summary" || name == "status" || name == "creator"
		*target, err = clean(name, *target, required)
		if err != nil {
			return Plan{}, err
		}
	}
	if !oneOf(value.Status, "proposed", "accepted", "active", "completed", "superseded") {
		return Plan{}, fmt.Errorf("unsupported plan status %q", value.Status)
	}
	value.Authority = "caller_asserted"
	if value.Model != "" {
		value.Authority = "model_suggested"
		if value.Status != "proposed" {
			return Plan{}, fmt.Errorf("model-suggested plan revisions must remain proposed until explicit review")
		}
	}
	if len(value.Items) == 0 || len(value.Items) > 100 {
		return Plan{}, fmt.Errorf("plan must contain between 1 and 100 items")
	}
	value.WorkItemIDs, err = cleanUnique(normalizeWorkItems(value.WorkItemIDs), "work item ID")
	if err != nil {
		return Plan{}, err
	}
	if err := validateItems(value.Items); err != nil {
		return Plan{}, err
	}
	for index := range value.Items {
		item := &value.Items[index]
		item.EvidenceRefs, err = cleanUnique(item.EvidenceRefs, "evidence reference")
		if err != nil {
			return Plan{}, err
		}
		item.WorkItemIDs, err = cleanUnique(normalizeWorkItems(item.WorkItemIDs), "work item ID")
		if err != nil {
			return Plan{}, err
		}
	}
	if encoded, err := json.Marshal(value); err != nil {
		return Plan{}, err
	} else if len(encoded) > maxPlanBytes {
		return Plan{}, fmt.Errorf("plan exceeds 256 KiB limit")
	}
	tx, err := service.DB.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	var latest int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM track_plan_revisions WHERE project_id=? AND id=?`, value.ProjectID, value.ID).Scan(&latest); err != nil {
		return Plan{}, err
	}
	if value.Revision == 0 {
		value.Revision = latest + 1
	}
	if value.Revision != latest+1 {
		return Plan{}, fmt.Errorf("plan revision must be the next revision %d", latest+1)
	}
	value.RecordedAt = service.now()
	value.SchemaVersion = SchemaVersion
	if _, err := tx.ExecContext(ctx, `INSERT INTO track_plan_revisions(project_id,id,revision,roadmap_node_id,task_id,title,summary,status,creator,model,authority,recorded_at,schema_version) VALUES(?,?,?,?,NULLIF(?,''),?,?,?,?,NULLIF(?,''),?,?,?)`, value.ProjectID, value.ID, value.Revision, value.RoadmapNodeID, value.TaskID, value.Title, value.Summary, value.Status, value.Creator, value.Model, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion); err != nil {
		return Plan{}, err
	}
	for index := range value.Items {
		item := &value.Items[index]
		evidence, _ := json.Marshal(item.EvidenceRefs)
		workItems, _ := json.Marshal(item.WorkItemIDs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO track_plan_items(project_id,plan_id,plan_revision,id,position,title,detail,status,parent_id,evidence_refs_json,work_item_refs_json) VALUES(?,?,?,?,?,?,?,?,NULLIF(?,''),?,?)`, value.ProjectID, value.ID, value.Revision, item.ID, item.Position, item.Title, item.Detail, item.Status, item.ParentID, string(evidence), string(workItems)); err != nil {
			return Plan{}, err
		}
	}
	for _, item := range value.Items {
		for _, dependency := range item.DependsOn {
			if _, err := tx.ExecContext(ctx, `INSERT INTO track_plan_item_dependencies(project_id,plan_id,plan_revision,item_id,depends_on_item_id) VALUES(?,?,?,?,?)`, value.ProjectID, value.ID, value.Revision, item.ID, dependency); err != nil {
				return Plan{}, err
			}
		}
	}
	for _, itemID := range value.WorkItemIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO track_plan_work_items(project_id,plan_id,plan_revision,item_id) VALUES(?,?,?,?)`, value.ProjectID, value.ID, value.Revision, itemID); err != nil {
			return Plan{}, err
		}
	}
	stamp, err := value.Fence.Stamp(ctx, tx, value.ProjectID, lease.StampPlan, fmt.Sprintf("%s@%d", value.ID, value.Revision), "track_plan", value.ID, value.RecordedAt)
	if err != nil {
		return Plan{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, err
	}
	value.Lease = &stamp
	return value, nil
}

func validateItems(items []Item) error {
	ids := map[string]bool{}
	positions := map[int]bool{}
	for index := range items {
		item := &items[index]
		var err error
		item.ID, err = clean("item ID", item.ID, true)
		if err != nil {
			return err
		}
		item.Title, err = clean("item title", item.Title, true)
		if err != nil {
			return err
		}
		item.Detail, err = clean("item detail", item.Detail, false)
		if err != nil {
			return err
		}
		item.ParentID, err = clean("parent item ID", item.ParentID, false)
		if err != nil {
			return err
		}
		if !oneOf(item.Status, "pending", "ready", "active", "blocked", "completed", "superseded") {
			return fmt.Errorf("unsupported item status %q", item.Status)
		}
		if item.Position < 0 || positions[item.Position] {
			return fmt.Errorf("item positions must be unique and nonnegative")
		}
		if ids[item.ID] {
			return fmt.Errorf("duplicate item ID %q", item.ID)
		}
		positions[item.Position], ids[item.ID] = true, true
	}
	for index := range items {
		item := &items[index]
		var err error
		item.DependsOn, err = cleanUnique(item.DependsOn, "dependency item ID")
		if err != nil {
			return err
		}
		if item.ParentID != "" && (!ids[item.ParentID] || item.ParentID == item.ID) {
			return fmt.Errorf("item %q has invalid parent %q", item.ID, item.ParentID)
		}
		for _, dependency := range item.DependsOn {
			if !ids[dependency] || dependency == item.ID {
				return fmt.Errorf("item %q has invalid dependency %q", item.ID, dependency)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	dependencies := map[string][]string{}
	for _, item := range items {
		dependencies[item.ID] = item.DependsOn
	}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("plan item dependency cycle includes %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range dependencies[id] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	parents := map[string]string{}
	for _, item := range items {
		parents[item.ID] = item.ParentID
	}
	for id := range ids {
		seen := map[string]bool{}
		for current := id; current != ""; current = parents[current] {
			if seen[current] {
				return fmt.Errorf("plan item parent cycle includes %q", current)
			}
			seen[current] = true
		}
	}
	return nil
}

// Accept records an explicit human review of the latest proposed revision by
// appending a copy with the reviewed status (accepted, active, or completed) and
// the reviewer as creator. Items are unchanged; completing requires every item to
// be completed or superseded already. Model identity is cleared because the new
// revision is the reviewer's assertion.
func (service Service) Accept(ctx context.Context, projectID, id, status, reviewer string, fence lease.Fence) (Plan, error) {
	if !oneOf(status, "accepted", "active", "completed") {
		return Plan{}, fmt.Errorf("accepted status must be accepted, active, or completed, not %q", status)
	}
	if strings.TrimSpace(reviewer) == "" {
		return Plan{}, fmt.Errorf("reviewer is required")
	}
	latest, err := service.Plan(ctx, projectID, id, 0)
	if err != nil {
		return Plan{}, err
	}
	if latest == nil {
		return Plan{}, fmt.Errorf("track plan %q not found", id)
	}
	if latest.Status != "proposed" {
		return Plan{}, fmt.Errorf("latest revision %d of plan %s is %s, not proposed", latest.Revision, latest.ID, latest.Status)
	}
	if status == "completed" {
		for _, item := range latest.Items {
			if item.Status != "completed" && item.Status != "superseded" {
				return Plan{}, fmt.Errorf("item %s is %s; record a revision that finishes it before completing the plan", item.ID, item.Status)
			}
		}
	}
	next := *latest
	next.Revision, next.Status, next.Creator, next.Model, next.Authority = 0, status, reviewer, "", ""
	next.RecordedAt, next.Fence, next.Lease = time.Time{}, fence, nil
	return service.AppendPlan(ctx, next)
}

func (service Service) Plan(ctx context.Context, projectID, id string, revision int) (*Plan, error) {
	return getPlan(ctx, service.DB, projectID, id, revision)
}

func getPlan(ctx context.Context, db planQueryer, projectID, id string, revision int) (*Plan, error) {
	id = strings.TrimPrefix(strings.TrimSpace(id), "plan:")
	if revision == 0 {
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM track_plan_revisions WHERE project_id=? AND id=?`, projectID, id).Scan(&revision); err != nil {
			return nil, err
		}
	}
	var value Plan
	var recorded string
	err := db.QueryRowContext(ctx, `SELECT project_id,id,revision,roadmap_node_id,COALESCE(task_id,''),title,summary,status,creator,COALESCE(model,''),authority,recorded_at,schema_version FROM track_plan_revisions WHERE project_id=? AND id=? AND revision=?`, projectID, id, revision).Scan(&value.ProjectID, &value.ID, &value.Revision, &value.RoadmapNodeID, &value.TaskID, &value.Title, &value.Summary, &value.Status, &value.Creator, &value.Model, &value.Authority, &recorded, &value.SchemaVersion)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,position,title,detail,status,COALESCE(parent_id,''),evidence_refs_json,work_item_refs_json FROM track_plan_items WHERE project_id=? AND plan_id=? AND plan_revision=? ORDER BY position,id`, projectID, id, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Item
		var evidence, workItems string
		if err := rows.Scan(&item.ID, &item.Position, &item.Title, &item.Detail, &item.Status, &item.ParentID, &evidence, &workItems); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(evidence), &item.EvidenceRefs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(workItems), &item.WorkItemIDs); err != nil {
			return nil, err
		}
		dependencyRows, err := db.QueryContext(ctx, `SELECT depends_on_item_id FROM track_plan_item_dependencies WHERE project_id=? AND plan_id=? AND plan_revision=? AND item_id=? ORDER BY depends_on_item_id`, projectID, id, revision, item.ID)
		if err != nil {
			return nil, err
		}
		for dependencyRows.Next() {
			var dependency string
			if err := dependencyRows.Scan(&dependency); err != nil {
				dependencyRows.Close()
				return nil, err
			}
			item.DependsOn = append(item.DependsOn, dependency)
		}
		dependencyRows.Close()
		value.Items = append(value.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	itemRows, err := db.QueryContext(ctx, `SELECT item_id FROM track_plan_work_items WHERE project_id=? AND plan_id=? AND plan_revision=? ORDER BY item_id`, projectID, id, revision)
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var itemID string
		if err := itemRows.Scan(&itemID); err != nil {
			return nil, err
		}
		value.WorkItemIDs = append(value.WorkItemIDs, itemID)
	}
	return &value, itemRows.Err()
}

func (service Service) PlanHistory(ctx context.Context, projectID, id string) ([]Plan, error) {
	rows, err := service.DB.QueryContext(ctx, `SELECT revision FROM track_plan_revisions WHERE project_id=? AND id=? ORDER BY revision`, projectID, strings.TrimPrefix(strings.TrimSpace(id), "plan:"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revisions []int
	for rows.Next() {
		var revision int
		if err := rows.Scan(&revision); err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Plan, 0, len(revisions))
	for _, revision := range revisions {
		value, err := service.Plan(ctx, projectID, id, revision)
		if err != nil {
			return nil, err
		}
		result = append(result, *value)
	}
	return result, nil
}

func (service Service) PlanHistoryPage(ctx context.Context, projectID, id string, options PlanHistoryOptions) (PlanHistoryPage, error) {
	if options.Limit == 0 {
		options.Limit = 20
	}
	if options.Limit < 1 || options.Limit > 100 {
		return PlanHistoryPage{}, fmt.Errorf("limit must be between 1 and 100")
	}
	if options.Offset < 0 {
		return PlanHistoryPage{}, fmt.Errorf("offset must be zero or greater")
	}
	id = strings.TrimPrefix(strings.TrimSpace(id), "plan:")
	rows, err := service.DB.QueryContext(ctx, `WITH matching AS (
		SELECT p.id,p.revision,p.roadmap_node_id,COALESCE(p.task_id,'') task_id,p.title,p.status,
			(SELECT COUNT(*) FROM track_plan_items i WHERE i.project_id=p.project_id AND i.plan_id=p.id AND i.plan_revision=p.revision) item_count,
			p.recorded_at
		FROM track_plan_revisions p WHERE p.project_id=? AND p.id=?
	), total AS (SELECT COUNT(*) count FROM matching),
	page AS (SELECT * FROM matching ORDER BY revision LIMIT ? OFFSET ?)
	SELECT page.id,page.revision,page.roadmap_node_id,page.task_id,page.title,page.status,page.item_count,page.recorded_at,total.count
	FROM total LEFT JOIN page ON 1=1 ORDER BY page.revision`, projectID, id, options.Limit, options.Offset)
	if err != nil {
		return PlanHistoryPage{}, err
	}
	defer rows.Close()
	page := PlanHistoryPage{Revisions: []PlanRevisionSummary{}, Limit: options.Limit, Offset: options.Offset}
	for rows.Next() {
		var value PlanRevisionSummary
		var planID, roadmapNodeID, taskID, title, status, recorded sql.NullString
		var revision, itemCount, total sql.NullInt64
		if err := rows.Scan(&planID, &revision, &roadmapNodeID, &taskID, &title, &status, &itemCount, &recorded, &total); err != nil {
			return PlanHistoryPage{}, err
		}
		page.Total = int(total.Int64)
		if !planID.Valid {
			continue
		}
		value = PlanRevisionSummary{ID: planID.String, Revision: int(revision.Int64), RoadmapNodeID: roadmapNodeID.String, TaskID: taskID.String, Title: title.String, Status: status.String, ItemCount: int(itemCount.Int64)}
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded.String)
		if err != nil {
			return PlanHistoryPage{}, err
		}
		page.Revisions = append(page.Revisions, value)
	}
	if err := rows.Err(); err != nil {
		return PlanHistoryPage{}, err
	}
	page.HasMore = options.Offset+len(page.Revisions) < page.Total
	return page, nil
}

func (service Service) ListPlans(ctx context.Context, projectID string, options PlanListOptions) ([]PlanSummary, error) {
	page, err := service.ListPlansPage(ctx, projectID, options)
	if err != nil {
		return nil, err
	}
	return page.Plans, nil
}

func (service Service) ListPlansPage(ctx context.Context, projectID string, options PlanListOptions) (PlanListPage, error) {
	if options.Limit == 0 {
		options.Limit = 20
	}
	if options.Limit < 1 || options.Limit > 100 {
		return PlanListPage{}, fmt.Errorf("limit must be between 1 and 100")
	}
	rows, err := service.DB.QueryContext(ctx, `WITH latest AS (SELECT id,MAX(revision) revision FROM track_plan_revisions WHERE project_id=? GROUP BY id) SELECT p.id,p.revision,p.roadmap_node_id,COALESCE(p.task_id,''),p.title,p.status,(SELECT COUNT(*) FROM track_plan_items i WHERE i.project_id=p.project_id AND i.plan_id=p.id AND i.plan_revision=p.revision),p.recorded_at,COUNT(*) OVER() FROM track_plan_revisions p JOIN latest l ON l.id=p.id AND l.revision=p.revision WHERE p.project_id=? AND (?='' OR p.status=?) AND (?='' OR p.roadmap_node_id=?) AND (?='' OR p.task_id=?) ORDER BY p.roadmap_node_id,p.id LIMIT ?`, projectID, projectID, options.Status, options.Status, options.RoadmapNodeID, options.RoadmapNodeID, options.TaskID, options.TaskID, options.Limit)
	if err != nil {
		return PlanListPage{}, err
	}
	defer rows.Close()
	page := PlanListPage{Plans: []PlanSummary{}, Limit: options.Limit}
	for rows.Next() {
		var value PlanSummary
		var recorded string
		if err := rows.Scan(&value.ID, &value.Revision, &value.RoadmapNodeID, &value.TaskID, &value.Title, &value.Status, &value.ItemCount, &recorded, &page.Total); err != nil {
			return PlanListPage{}, err
		}
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			return PlanListPage{}, err
		}
		if value.Status == "accepted" || value.Status == "active" {
			plan, err := service.Plan(ctx, projectID, value.ID, value.Revision)
			if err != nil {
				return PlanListPage{}, err
			}
			for _, item := range readyItems(*plan) {
				if item.PlanID == plan.ID {
					value.ReadyCount++
				}
			}
		}
		page.Plans = append(page.Plans, value)
	}
	if err = rows.Err(); err != nil {
		return PlanListPage{}, err
	}
	page.Truncated = page.Total > len(page.Plans)
	return page, nil
}

func (service Service) Ready(ctx context.Context, projectID string, options PlanListOptions) ([]ReadyItem, error) {
	options.Status = ""
	plans, err := service.ListPlans(ctx, projectID, options)
	if err != nil {
		return nil, err
	}
	result := []ReadyItem{}
	for _, summary := range plans {
		if summary.Status != "accepted" && summary.Status != "active" {
			continue
		}
		plan, err := service.Plan(ctx, projectID, summary.ID, summary.Revision)
		if err != nil {
			return nil, err
		}
		result = append(result, readyItems(*plan)...)
	}
	return result, nil
}

func readyItems(plan Plan) []ReadyItem {
	statuses := map[string]string{}
	for _, item := range plan.Items {
		statuses[item.ID] = item.Status
	}
	result := []ReadyItem{}
	for _, item := range plan.Items {
		if item.Status != "pending" && item.Status != "ready" {
			continue
		}
		ready := true
		for _, dependency := range item.DependsOn {
			if statuses[dependency] != "completed" && statuses[dependency] != "superseded" {
				ready = false
				break
			}
		}
		if ready {
			result = append(result, ReadyItem{PlanID: plan.ID, PlanRevision: plan.Revision, RoadmapNodeID: plan.RoadmapNodeID, TaskID: plan.TaskID, Item: item, Reasons: []string{"plan status permits work", "all declared dependencies are completed or superseded"}})
		}
	}
	return result
}
