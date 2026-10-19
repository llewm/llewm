package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

type Item struct {
	Ref        string `json:"ref"`
	Family     string `json:"family"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	RecordedAt string `json:"recorded_at"`
	Text       string `json:"text"`
	Truncated  bool   `json:"truncated"`
	Error      string `json:"error,omitempty"`
}

type ShowPage struct {
	SchemaVersion int    `json:"schema_version"`
	Items         []Item `json:"items"`
	MaxBytes      int    `json:"max_bytes"`
	ContentBytes  int    `json:"content_bytes"`
	Truncated     bool   `json:"truncated"`
	Authority     string `json:"authority"`
	Disclaimer    string `json:"disclaimer"`
}

// Show expands exact references in order within one byte budget. An unknown
// reference is reported per item instead of failing the whole read.
func (s Service) Show(ctx context.Context, projectID string, refs []string, maxBytes int) (ShowPage, error) {
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxBytes < 256 || maxBytes > MaxMaxBytes {
		return ShowPage{}, fmt.Errorf("max bytes must be between 256 and %d", MaxMaxBytes)
	}
	if len(refs) == 0 || len(refs) > MaxShowRefs {
		return ShowPage{}, fmt.Errorf("provide between 1 and %d references", MaxShowRefs)
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ShowPage{}, err
	}
	defer tx.Rollback()
	page := ShowPage{SchemaVersion: SchemaVersion, Items: []Item{}, MaxBytes: maxBytes, Authority: "mixed", Disclaimer: Disclaimer}
	for _, ref := range refs {
		item, err := load(ctx, tx, projectID, strings.TrimSpace(ref))
		if err != nil {
			item = Item{Ref: ref, Error: err.Error()}
		}
		if item.Error == "" {
			state, successors, truncated, err := correction(ctx, tx, projectID, ref)
			if err != nil {
				return ShowPage{}, err
			}
			if state == "superseded" {
				prefix := "State: superseded"
				if len(successors) > 0 {
					prefix += "\nSuccessors: " + strings.Join(successors, ", ")
				}
				if truncated {
					prefix += "\n[successors truncated]"
				}
				item.Text = prefix + "\n\n" + item.Text
			} else if item.Family == "narrative" || item.Family == "narrative_claim" || item.Family == "decision" {
				item.Text = "State: current\n\n" + item.Text
			}
		}
		remaining := maxBytes - page.ContentBytes
		if remaining <= 0 {
			page.Truncated = true
			break
		}
		if item.Text, item.Truncated = truncateBytes(item.Text, remaining); item.Truncated {
			page.Truncated = true
		}
		page.ContentBytes += len(item.Text)
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func load(ctx context.Context, db queryer, projectID, ref string) (Item, error) {
	family, id, found := strings.Cut(ref, ":")
	if !found || id == "" {
		return Item{}, errors.New("reference must look like family:id")
	}
	item := Item{Ref: ref}
	switch family {
	case "narrative":
		return loadNarrative(ctx, db, projectID, ref, id)
	case "decision":
		item.Family = "decision"
		var rationale, actor, authority, review string
		var successor sql.NullString
		err := db.QueryRowContext(ctx, `SELECT title,disposition,recorded_at,rationale,actor,authority,review_source,`+decision.SupersededBy("decision_records")+` FROM decision_records WHERE project_id=? AND id=?`, projectID, id).Scan(&item.Title, &item.Kind, &item.RecordedAt, &rationale, &actor, &authority, &review, &successor)
		if err != nil {
			return Item{}, notFound(err, ref)
		}
		var links []string
		if err = collect(ctx, db, `SELECT kind||' '||target_kind||':'||target_id||CASE WHEN target_revision='' THEN '' ELSE '@'||target_revision END FROM decision_links WHERE project_id=? AND decision_id=? ORDER BY ordinal`, []any{projectID, id}, "", func(value, _ string) { links = append(links, value) }); err != nil {
			return Item{}, err
		}
		item.Text = fmt.Sprintf("Actor: %s (%s, review %s)\n%s", actor, authority, review, rationale)
		if len(links) > 0 {
			item.Text += "\nLinks: " + strings.Join(links, "; ")
		}
		if successor.Valid {
			item.Text += "\nSuperseded by: " + successor.String
		}
	case "work_item":
		item.Family = "work_item"
		if err := db.QueryRowContext(ctx, `SELECT title,recorded_at,body,kind FROM work_items WHERE project_id=? AND id=?`, projectID, id).Scan(&item.Title, &item.RecordedAt, &item.Text, &item.Kind); err != nil {
			return Item{}, notFound(err, ref)
		}
		var status, reason, targets string
		err := db.QueryRowContext(ctx, `SELECT status,reason,target_refs_json FROM work_item_dispositions WHERE project_id=? AND item_id=? ORDER BY recorded_at DESC,id DESC LIMIT 1`, projectID, id).Scan(&status, &reason, &targets)
		if err != nil && err != sql.ErrNoRows {
			return Item{}, err
		}
		if err == nil {
			item.Text = "Status: " + status + "\nReason: " + reason + "\nFindings/targets: " + targets + "\n\n" + item.Text
			if status == "answered" {
				var refs []string
				if err := json.Unmarshal([]byte(targets), &refs); err != nil {
					return Item{}, err
				}
				if err := workitem.ValidateAnswerTargets(ctx, db, projectID, refs); err != nil {
					item.Text = "Finding resolution: unverified (" + err.Error() + ")\n" + item.Text
				}
			}
		}
	case "checkpoint":
		item.Family = "checkpoint"
		var summary, next, payload string
		if err := db.QueryRowContext(ctx, `SELECT task_title,status,created_at,summary,next_action,payload_json FROM checkpoints WHERE project_id=? AND id=?`, projectID, id).Scan(&item.Title, &item.Kind, &item.RecordedAt, &summary, &next, &payload); err != nil {
			return Item{}, notFound(err, ref)
		}
		item.Text = summary + checkpointLists(payload) + "\nNext: " + next
	case "procedure":
		item.Family = "procedure"
		procedureID, revision, _ := strings.Cut(id, "@")
		var purpose, steps, effects string
		query := `SELECT title,kind,recorded_at,purpose,steps_json,declared_effects_json FROM reviewed_procedures WHERE project_id=? AND procedure_id=? AND (?='' OR CAST(revision AS TEXT)=?) ORDER BY revision DESC LIMIT 1`
		if err := db.QueryRowContext(ctx, query, projectID, procedureID, revision, revision).Scan(&item.Title, &item.Kind, &item.RecordedAt, &purpose, &steps, &effects); err != nil {
			return Item{}, notFound(err, ref)
		}
		item.Text = procedureText(purpose, steps, effects)
	case "resource":
		item.Family = "resource"
		if resourceID, noteID, isNote := strings.Cut(id, "#"); isNote {
			item.Kind = "note"
			var authority, observed string
			if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT r.title FROM resources r WHERE r.project_id=n.project_id AND r.id=n.resource_id ORDER BY r.revision DESC LIMIT 1),n.resource_id),n.recorded_at,n.text,n.authority,n.observed_at FROM resource_notes n WHERE n.project_id=? AND n.resource_id=? AND n.id=?`, projectID, resourceID, noteID).Scan(&item.Title, &item.RecordedAt, &item.Text, &authority, &observed); err != nil {
				return Item{}, notFound(err, ref)
			}
			item.Text = fmt.Sprintf("Observed %s (%s): %s", observed, authority, item.Text)
			break
		}
		resourceID, revision, _ := strings.Cut(id, "@")
		var summary, attributes string
		query := `SELECT title,kind,recorded_at,summary,attributes_json FROM resources WHERE project_id=? AND id=? AND (?='' OR CAST(revision AS TEXT)=?) ORDER BY revision DESC LIMIT 1`
		if err := db.QueryRowContext(ctx, query, projectID, resourceID, revision, revision).Scan(&item.Title, &item.Kind, &item.RecordedAt, &summary, &attributes); err != nil {
			return Item{}, notFound(err, ref)
		}
		item.Text = resourceText(summary, attributes)
	default:
		return Item{}, fmt.Errorf("unsupported reference family %q", family)
	}
	return item, nil
}

// loadNarrative expands one claim (narrative:ID#N) or every claim of a narrative.
func loadNarrative(ctx context.Context, db queryer, projectID, ref, id string) (Item, error) {
	narrativeID, ordinalText, oneClaim := strings.Cut(id, "#")
	item := Item{Ref: ref, Family: "narrative_claim"}
	var trigger, sourceJSON string
	if err := db.QueryRowContext(ctx, `SELECT title,trigger_kind,COALESCE(occurred_from,recorded_at),COALESCE(json_extract(payload_json,'$.source'),'null') FROM work_narratives WHERE project_id=? AND id=?`, projectID, narrativeID).Scan(&item.Title, &trigger, &item.RecordedAt, &sourceJSON); err != nil {
		return Item{}, notFound(err, ref)
	}
	query := `SELECT ordinal,kind,label,text,authority FROM work_narrative_claims WHERE project_id=? AND narrative_id=? ORDER BY ordinal`
	args := []any{projectID, narrativeID}
	if oneClaim {
		ordinal, err := strconv.Atoi(ordinalText)
		if err != nil || ordinal < 0 {
			return Item{}, fmt.Errorf("claim reference %q needs a non-negative ordinal", ref)
		}
		query = `SELECT ordinal,kind,label,text,authority FROM work_narrative_claims WHERE project_id=? AND narrative_id=? AND ordinal=?`
		args = append(args, ordinal)
	} else {
		item.Family, item.Kind = "narrative", trigger
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return Item{}, err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var ordinal int
		var kind, label, text, authority string
		if err = rows.Scan(&ordinal, &kind, &label, &text, &authority); err != nil {
			return Item{}, err
		}
		citations, err := citationText(ctx, db, projectID, narrativeID, ordinal)
		if err != nil {
			return Item{}, err
		}
		text += citations
		heading := kind
		if label != "" {
			heading += " — " + label
		}
		if oneClaim {
			item.Kind = kind
			parts = append(parts, fmt.Sprintf("[%s, %s]\n%s", heading, authority, text))
		} else {
			parts = append(parts, fmt.Sprintf("#%d [%s, %s]\n%s", ordinal, heading, authority, text))
		}
	}
	if err = rows.Err(); err != nil {
		return Item{}, err
	}
	if len(parts) == 0 {
		return Item{}, fmt.Errorf("%s not found", ref)
	}
	var anchors []string
	if err = collect(ctx, db, `SELECT anchor_kind||':'||target||CASE WHEN COALESCE(target_revision,'')='' THEN '' ELSE '@'||target_revision END
	 ||CASE WHEN COALESCE(content_sha256,'')='' THEN '' ELSE ' sha256='||content_sha256 END
	 ||CASE WHEN COALESCE(region_locator,'')='' THEN '' ELSE ' region='||region_locator END
	 ||' authority='||authority
	 FROM work_narrative_anchors WHERE project_id=? AND narrative_id=? ORDER BY ordinal`, []any{projectID, narrativeID}, "", func(value, _ string) { anchors = append(anchors, value) }); err != nil {
		return Item{}, err
	}
	var source *worknarrative.Source
	if err := json.Unmarshal([]byte(sourceJSON), &source); err != nil {
		return Item{}, err
	}
	item.Text = worknarrative.RenderSource(source) + strings.Join(parts, "\n\n")
	if len(anchors) > 0 {
		item.Text += "\n\nAnchors: " + strings.Join(anchors, ", ")
	}
	return item, nil
}

// checkpointLists renders the structured completed and attempt lists when present.
func checkpointLists(payload string) string {
	var value struct {
		Completed []string `json:"completed"`
		Attempts  []struct {
			Description string `json:"description"`
			Outcome     string `json:"outcome"`
		} `json:"attempts"`
	}
	if json.Unmarshal([]byte(payload), &value) != nil {
		return ""
	}
	var out strings.Builder
	if len(value.Completed) > 0 {
		out.WriteString("\nCompleted:")
		for _, entry := range value.Completed {
			out.WriteString("\n- " + entry)
		}
	}
	if len(value.Attempts) > 0 {
		out.WriteString("\nAttempts:")
		for _, attempt := range value.Attempts {
			out.WriteString("\n- " + attempt.Description + ": " + attempt.Outcome)
		}
	}
	return out.String()
}

func notFound(err error, ref string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s not found", ref)
	}
	return err
}

// procedureText renders a procedure's purpose, numbered steps with their inert
// argv templates, and declared effects.
func procedureText(purpose, stepsJSON, effectsJSON string) string {
	var steps []struct {
		Description  string   `json:"description"`
		ArgvTemplate []string `json:"argv_template"`
	}
	var effects []string
	_ = json.Unmarshal([]byte(stepsJSON), &steps)
	_ = json.Unmarshal([]byte(effectsJSON), &effects)
	var out strings.Builder
	out.WriteString(purpose)
	for i, step := range steps {
		fmt.Fprintf(&out, "\n%d. %s", i+1, step.Description)
		if len(step.ArgvTemplate) > 0 {
			fmt.Fprintf(&out, ": %s", strings.Join(step.ArgvTemplate, " "))
		}
	}
	if len(effects) > 0 {
		out.WriteString("\nEffects: " + strings.Join(effects, "; "))
	}
	return out.String()
}

// resourceText renders a resource summary and its attributes in key order.
func resourceText(summary, attributesJSON string) string {
	var attributes map[string]string
	_ = json.Unmarshal([]byte(attributesJSON), &attributes)
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	out.WriteString(summary)
	for _, key := range keys {
		fmt.Fprintf(&out, "\n%s: %s", key, attributes[key])
	}
	return strings.TrimPrefix(out.String(), "\n")
}
