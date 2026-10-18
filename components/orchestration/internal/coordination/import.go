package coordination

import (
	"encoding/json"
	"errors"
	"fmt"
)

// PrepareContinuity consumes the public schema-v1 track-plan show --detail JSON,
// never a database or internal Continuity type. It only prepares a proposal;
// source acceptance is evidence, not a grant to release work at this coordinator.
func PrepareContinuity(body []byte, planID, requestID string) (Command, error) {
	c := Command{SchemaVersion: SchemaVersion, RequestID: requestID, PlanID: planID, Action: "propose"}
	if !validID(planID) || !validID(requestID) {
		return c, errors.New("plan and request-id identifiers required")
	}
	if len(body) > MaxCommandBytes {
		return c, errors.New("source snapshot exceeds 128 KiB")
	}
	var source struct {
		SchemaVersion int    `json:"schema_version"`
		ProjectID     string `json:"project_id"`
		ID            string `json:"id"`
		Revision      int    `json:"revision"`
		Status        string `json:"status"`
		Title         string `json:"title"`
		Summary       string `json:"summary"`
		Items         []struct {
			ID                 string   `json:"id"`
			ParentID           string   `json:"parent_id"`
			Title              string   `json:"title"`
			Detail             string   `json:"detail"`
			AcceptanceCriteria string   `json:"acceptance_criteria"`
			Status             string   `json:"status"`
			ProgressNote       string   `json:"progress_note"`
			DependsOn          []string `json:"depends_on"`
			EvidenceRefs       []string `json:"evidence_refs"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &source); err != nil {
		return c, errors.New("invalid source plan JSON")
	}
	if source.SchemaVersion != 1 || source.ProjectID == "" || source.ID == "" || source.Revision < 1 || (source.Status != "accepted" && source.Status != "active" && source.Status != "completed") {
		return c, errors.New("export a full accepted/active/completed schema-v1 Continuity plan with --detail")
	}
	ref := fmt.Sprintf("continuity:%s/plan:%s@%d", source.ProjectID, source.ID, source.Revision)
	d := &Definition{Title: source.Title, SourceRef: ref, Context: source.Summary}
	for _, item := range source.Items {
		if item.ParentID != "" {
			return c, errors.New("hierarchical source plans require an explicitly reviewed flat execution projection")
		}
		i := Item{ID: item.ID, Title: item.Title, Detail: item.Detail, AcceptanceCriteria: item.AcceptanceCriteria, DependsOn: item.DependsOn, ContextRefs: append([]string{ref + "/item:" + item.ID}, item.EvidenceRefs...)}
		if item.ProgressNote != "" {
			i.Detail += "\nSource progress: " + item.ProgressNote
		}
		switch item.Status {
		case "pending", "ready", "active":
		case "blocked":
			i.BlockedReason = item.ProgressNote
			if i.BlockedReason == "" {
				i.BlockedReason = "Blocked in source plan"
			}
		case "completed":
			if len(item.EvidenceRefs) == 0 {
				return c, errors.New("completed source items need evidence references")
			}
			i.PriorCompletionRefs = append([]string{ref + "/item:" + item.ID}, item.EvidenceRefs...)
		default:
			return c, errors.New("unsupported source item status; prepare a reviewed execution projection")
		}
		d.Items = append(d.Items, i)
	}
	if err := validateDefinition(*d); err != nil {
		return c, err
	}
	c.Definition = d
	c, err := cleanCommand(c)
	if err != nil {
		return c, err
	}
	encoded, _ := json.Marshal(c)
	if len(encoded) > MaxCommandBytes {
		return c, errors.New("prepared command exceeds 128 KiB")
	}
	return c, nil
}
