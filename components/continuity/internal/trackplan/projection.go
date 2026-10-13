package trackplan

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	CurrentPlanProjectionPath = "components/continuity/docs/current-plan.md"
	planProjectionRenderer    = "continuity-current-plan-markdown"
	planProjectionVersion     = 1
	maxPlanProjectionBytes    = 4 << 20
	maxProjectedPlans         = 100
)

var planProjectionRendererHash = planHash([]byte("continuity-current-plan-markdown:v1:latest-accepted-active-plans+exact-revision-items+snapshot-and-content-hashes"))

type CurrentPlanProjection struct {
	SchemaVersion   int    `json:"schema_version"`
	ProjectID       string `json:"project_id"`
	Path            string `json:"path"`
	SourceHash      string `json:"source_hash"`
	RendererName    string `json:"renderer_name"`
	RendererVersion int    `json:"renderer_version"`
	RendererHash    string `json:"renderer_hash"`
	ContentHash     string `json:"content_hash"`
	Markdown        string `json:"markdown"`
}

type CurrentPlanProjectionCheck struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	Path          string `json:"path"`
	State         string `json:"state"`
	ExpectedHash  string `json:"expected_hash"`
	ActualHash    string `json:"actual_hash,omitempty"`
	SourceHash    string `json:"source_hash"`
	RendererHash  string `json:"renderer_hash"`
	Disclaimer    string `json:"disclaimer"`
}

type planSnapshot struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	Plans         []Plan `json:"plans"`
}

// RenderCurrentPlan returns deterministic Markdown over the latest accepted or
// active revision of each track plan. It reads one SQLite snapshot and writes no
// files or records.
func (service Service) RenderCurrentPlan(ctx context.Context, projectID string) (CurrentPlanProjection, error) {
	if strings.TrimSpace(projectID) == "" {
		return CurrentPlanProjection{}, fmt.Errorf("project ID is required")
	}
	if strings.TrimSpace(service.Root) == "" {
		return CurrentPlanProjection{}, fmt.Errorf("project root is required")
	}
	tx, err := service.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CurrentPlanProjection{}, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `WITH latest AS (
		SELECT id,MAX(revision) revision FROM track_plan_revisions WHERE project_id=? GROUP BY id
	) SELECT p.id,p.revision,p.roadmap_node_id,COALESCE(p.task_id,''),p.title,p.status,
		(SELECT COUNT(*) FROM track_plan_items i WHERE i.project_id=p.project_id AND i.plan_id=p.id AND i.plan_revision=p.revision),
		p.recorded_at
	FROM track_plan_revisions p JOIN latest l ON l.id=p.id AND l.revision=p.revision
	WHERE p.project_id=? AND p.status IN ('accepted','active')
	ORDER BY p.roadmap_node_id,p.id LIMIT ?`, projectID, projectID, maxProjectedPlans+1)
	if err != nil {
		return CurrentPlanProjection{}, err
	}
	summaries := make([]PlanSummary, 0)
	for rows.Next() {
		var value PlanSummary
		var recorded string
		if err := rows.Scan(&value.ID, &value.Revision, &value.RoadmapNodeID, &value.TaskID, &value.Title, &value.Status, &value.ItemCount, &recorded); err != nil {
			rows.Close()
			return CurrentPlanProjection{}, err
		}
		value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			rows.Close()
			return CurrentPlanProjection{}, err
		}
		summaries = append(summaries, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CurrentPlanProjection{}, err
	}
	if err := rows.Close(); err != nil {
		return CurrentPlanProjection{}, err
	}
	if len(summaries) > maxProjectedPlans {
		return CurrentPlanProjection{}, fmt.Errorf("current plan projection exceeds %d latest accepted or active plans", maxProjectedPlans)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].RoadmapNodeID != summaries[j].RoadmapNodeID {
			return summaries[i].RoadmapNodeID < summaries[j].RoadmapNodeID
		}
		return summaries[i].ID < summaries[j].ID
	})
	plans := make([]Plan, 0, len(summaries))
	for _, summary := range summaries {
		plan, err := getPlan(ctx, tx, projectID, summary.ID, summary.Revision)
		if err != nil {
			return CurrentPlanProjection{}, err
		}
		if plan == nil {
			return CurrentPlanProjection{}, fmt.Errorf("track plan %s@%d disappeared from read snapshot", summary.ID, summary.Revision)
		}
		plans = append(plans, *plan)
	}
	if err := tx.Commit(); err != nil {
		return CurrentPlanProjection{}, err
	}

	snapshot := planSnapshot{SchemaVersion: SchemaVersion, ProjectID: projectID, Plans: plans}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return CurrentPlanProjection{}, err
	}
	if len(encoded) > maxPlanProjectionBytes {
		return CurrentPlanProjection{}, fmt.Errorf("current plan snapshot exceeds 4 MiB limit")
	}
	sourceHash := planHash(encoded)
	body := renderCurrentPlans(plans)
	if len([]byte(body)) > maxPlanProjectionBytes {
		return CurrentPlanProjection{}, fmt.Errorf("current plan projection exceeds 4 MiB limit")
	}
	contentHash := planHash([]byte(body))
	markdown := fmt.Sprintf("<!-- Generated by %s v%d; DO NOT EDIT. -->\n\n# Current plan\n\n- Project: `%s`\n- Source snapshot SHA-256: `%s`\n- Renderer: `%s` v%d (`%s`)\n- Content SHA-256: `%s`\n\n%s", planProjectionRenderer, planProjectionVersion, planMarkdownText(projectID), sourceHash, planProjectionRenderer, planProjectionVersion, planProjectionRendererHash, contentHash, body)
	if len([]byte(markdown)) > maxPlanProjectionBytes {
		return CurrentPlanProjection{}, fmt.Errorf("current plan projection exceeds 4 MiB limit")
	}
	return CurrentPlanProjection{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Path: CurrentPlanProjectionPath,
		SourceHash: sourceHash, RendererName: planProjectionRenderer, RendererVersion: planProjectionVersion,
		RendererHash: planProjectionRendererHash, ContentHash: contentHash, Markdown: markdown,
	}, nil
}

// CheckCurrentPlanProjection compares the exact rendered bytes with one
// project-relative file. It never creates or modifies that file.
func (service Service) CheckCurrentPlanProjection(ctx context.Context, projectID, relative string) (CurrentPlanProjectionCheck, error) {
	if relative == "" {
		relative = CurrentPlanProjectionPath
	}
	path, err := service.safeProjectionPath(relative)
	if err != nil {
		return CurrentPlanProjectionCheck{}, err
	}
	projection, err := service.RenderCurrentPlan(ctx, projectID)
	if err != nil {
		return CurrentPlanProjectionCheck{}, err
	}
	result := CurrentPlanProjectionCheck{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Path: filepath.ToSlash(filepath.Clean(relative)),
		State: "missing", ExpectedHash: planHash([]byte(projection.Markdown)), SourceHash: projection.SourceHash,
		RendererHash: projection.RendererHash,
		Disclaimer:   "Check is read-only and byte-exact. Missing or drifted output is reported without writing, repairing, or changing plan authority.",
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return CurrentPlanProjectionCheck{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxPlanProjectionBytes {
		return CurrentPlanProjectionCheck{}, fmt.Errorf("projection path must be a non-symlink regular file no larger than 4 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return CurrentPlanProjectionCheck{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return CurrentPlanProjectionCheck{}, err
	}
	if !os.SameFile(info, opened) {
		return CurrentPlanProjectionCheck{}, fmt.Errorf("projection file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPlanProjectionBytes+1))
	if err != nil {
		return CurrentPlanProjectionCheck{}, err
	}
	if len(data) > maxPlanProjectionBytes {
		return CurrentPlanProjectionCheck{}, fmt.Errorf("projection file exceeds 4 MiB limit")
	}
	result.ActualHash = planHash(data)
	result.State = "drifted"
	if string(data) == projection.Markdown {
		result.State = "current"
	}
	return result, nil
}

func (service Service) safeProjectionPath(relative string) (string, error) {
	relative = filepath.ToSlash(filepath.Clean(strings.TrimSpace(relative)))
	if relative == "" || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("projection path must be project-relative")
	}
	root, err := filepath.Abs(service.Root)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("project root must be a non-symlink directory")
	}
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	parent := root
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		info, statErr := os.Lstat(parent)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				// The destination cannot exist below a missing ancestor. Return
				// its intended path so the caller reports a missing projection.
				return filepath.Join(root, filepath.FromSlash(relative)), nil
			}
			return "", fmt.Errorf("projection parent must already exist: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("projection parent must be a non-symlink directory")
		}
	}
	return filepath.Join(root, filepath.FromSlash(relative)), nil
}

func renderCurrentPlans(plans []Plan) string {
	var out strings.Builder
	if len(plans) == 0 {
		out.WriteString("_No latest accepted or active track plans._\n\n")
		out.WriteString("Authority: latest caller-reviewed accepted or active track-plan revisions. This projection is read-only and does not select, activate, revise, or execute work.\n")
		return out.String()
	}
	for _, plan := range plans {
		fmt.Fprintf(&out, "## plan:%s@%d — %s\n\n", planMarkdownText(plan.ID), plan.Revision, planMarkdownText(plan.Title))
		fmt.Fprintf(&out, "- Status: `%s`\n- Roadmap: `roadmap:%s`\n- Task: `%s`\n- Creator: %s\n- Authority: `%s`\n- Recorded: `%s`\n", planMarkdownText(plan.Status), planMarkdownText(plan.RoadmapNodeID), planMarkdownText(plan.TaskID), planMarkdownText(plan.Creator), planMarkdownText(plan.Authority), plan.RecordedAt.UTC().Format(time.RFC3339Nano))
		if plan.Model != "" {
			fmt.Fprintf(&out, "- Model: %s\n", planMarkdownText(plan.Model))
		}
		out.WriteByte('\n')
		fmt.Fprintf(&out, "%s\n\n", planMarkdownText(plan.Summary))
		if len(plan.WorkItemIDs) > 0 {
			fmt.Fprintf(&out, "Work items: %s\n\n", planReferences("work_item", plan.WorkItemIDs))
		}
		for _, item := range plan.Items {
			fmt.Fprintf(&out, "- item:%s (position %d, status `%s`): %s\n", planMarkdownText(item.ID), item.Position, planMarkdownText(item.Status), planMarkdownText(item.Title))
			if item.Detail != "" {
				fmt.Fprintf(&out, "  Detail: %s\n", planMarkdownText(item.Detail))
			}
			if item.ParentID != "" {
				fmt.Fprintf(&out, "  Parent: `item:%s`\n", planMarkdownText(item.ParentID))
			}
			if len(item.DependsOn) > 0 {
				fmt.Fprintf(&out, "  Depends on: %s\n", planReferences("item", item.DependsOn))
			}
			if len(item.EvidenceRefs) > 0 {
				refs := make([]string, len(item.EvidenceRefs))
				for index, ref := range item.EvidenceRefs {
					refs[index] = planMarkdownText(ref)
				}
				fmt.Fprintf(&out, "  Evidence: %s\n", strings.Join(refs, ", "))
			}
			if len(item.WorkItemIDs) > 0 {
				fmt.Fprintf(&out, "  Work items: %s\n", planReferences("work_item", item.WorkItemIDs))
			}
		}
		out.WriteByte('\n')
	}
	out.WriteString("Authority: latest caller-reviewed accepted or active track-plan revisions. This projection is read-only and does not select, activate, revise, or execute work.\n")
	return out.String()
}

func planReferences(kind string, values []string) string {
	refs := make([]string, len(values))
	for index, value := range values {
		refs[index] = "`" + kind + ":" + planMarkdownText(value) + "`"
	}
	return strings.Join(refs, ", ")
}

func planMarkdownText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return strings.ReplaceAll(value, "`", "\\`")
}

func planHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
