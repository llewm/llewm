package usage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// FinishedAfter is how long a session must be idle, when its end was not
// observed, before its summary is exported.
const FinishedAfter = 6 * time.Hour

// ModelUsage is one model's exact totals within a session.
type ModelUsage struct {
	Model          string `json:"model"`
	Requests       int64  `json:"requests"`
	Input          int64  `json:"input_tokens"`
	CacheWrite     int64  `json:"cache_write_tokens"`
	CacheWriteLong int64  `json:"cache_write_long_tokens"`
	CacheRead      int64  `json:"cache_read_tokens"`
	Output         int64  `json:"output_tokens"`
	Reasoning      int64  `json:"reasoning_tokens"`
}

// ToolUsage is one tool's calls and result sizes within a session.
type ToolUsage struct {
	Tool        string `json:"tool"`
	Calls       int64  `json:"calls"`
	Errors      int64  `json:"errors"`
	ResultBytes int64  `json:"result_bytes"`
	MaxBytes    int64  `json:"max_result_bytes"`
}

// Summary is one finished session's usage, as it travels between machines.
type Summary struct {
	ID            string       `json:"id"`
	SchemaVersion int          `json:"schema_version"`
	Client        string       `json:"client"`
	SessionID     string       `json:"session_id"`
	InstanceID    string       `json:"instance_id"`
	FirstAt       time.Time    `json:"first_at"`
	LastAt        time.Time    `json:"last_at"`
	TaskID        string       `json:"task_id,omitempty"`
	Overhead      int64        `json:"overhead_tokens"`
	Models        []ModelUsage `json:"models"`
	Tools         []ToolUsage  `json:"tools"`
}

// SummaryID names a summary by its content, so equal summaries share an ID
// and any change yields a new one.
func SummaryID(value Summary) string {
	value.ID = ""
	var body []byte
	if value.SchemaVersion == 1 {
		// Schema 1 IDs predate the explicit version field. Preserve their exact
		// JSON shape so already-synced summaries remain verifiable.
		legacy := struct {
			ID         string       `json:"id"`
			Client     string       `json:"client"`
			SessionID  string       `json:"session_id"`
			InstanceID string       `json:"instance_id"`
			FirstAt    time.Time    `json:"first_at"`
			LastAt     time.Time    `json:"last_at"`
			TaskID     string       `json:"task_id,omitempty"`
			Overhead   int64        `json:"overhead_tokens"`
			Models     []ModelUsage `json:"models"`
			Tools      []ToolUsage  `json:"tools"`
		}{value.ID, value.Client, value.SessionID, value.InstanceID, value.FirstAt, value.LastAt, value.TaskID, value.Overhead, value.Models, value.Tools}
		body, _ = json.Marshal(legacy)
	} else {
		body, _ = json.Marshal(value)
	}
	sum := sha256.Sum256(body)
	return value.Client + ":" + value.SessionID + ":" + hex.EncodeToString(sum[:8])
}

// ValidateSummary checks a summary read from another machine.
func ValidateSummary(value Summary) error {
	if (value.SchemaVersion != 1 && value.SchemaVersion != 2) || value.Client == "" || value.SessionID == "" || value.InstanceID == "" || value.FirstAt.IsZero() || value.LastAt.Before(value.FirstAt) || value.Overhead < 0 {
		return fmt.Errorf("usage summary %s is incomplete", value.ID)
	}
	if value.ID != SummaryID(value) {
		return fmt.Errorf("usage summary %s does not match its content", value.ID)
	}
	for _, model := range value.Models {
		if model.Requests < 0 || model.Input < 0 || model.CacheWrite < model.CacheWriteLong || model.CacheWriteLong < 0 || model.CacheRead < 0 || model.Output < 0 || model.Reasoning < 0 {
			return fmt.Errorf("usage summary %s has negative counts", value.ID)
		}
	}
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// LocalSummaries derives a summary for each finished session ingested on
// this machine: ended, or idle longer than FinishedAfter at now.
func LocalSummaries(ctx context.Context, db queryer, projectID string, now time.Time) ([]Summary, error) {
	var instance string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT instance_id FROM project_memory_identities WHERE project_id=?),'')`, projectID).Scan(&instance); err != nil {
		return nil, err
	}
	if instance == "" {
		return nil, nil
	}
	tasks, err := Reporter{DB: nil}.sessionTasksFrom(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT u.client,u.session_id,MIN(u.occurred_at),MAX(u.occurred_at),COALESCE(MAX(s.status),'') FROM usage_requests u LEFT JOIN sessions s ON s.id=u.session_id AND s.project_id=u.project_id WHERE u.project_id=? GROUP BY u.client,u.session_id ORDER BY u.client,u.session_id`, projectID)
	if err != nil {
		return nil, err
	}
	var summaries []Summary
	for rows.Next() {
		value := Summary{SchemaVersion: 2}
		var first, last, status string
		if err := rows.Scan(&value.Client, &value.SessionID, &first, &last, &status); err != nil {
			rows.Close()
			return nil, err
		}
		value.FirstAt, value.LastAt = parseTime(first), parseTime(last)
		if status != "ended" && now.Sub(value.LastAt) < FinishedAfter {
			continue
		}
		value.InstanceID, value.TaskID = instance, tasks[value.SessionID]
		summaries = append(summaries, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range summaries {
		if err := fillSummary(ctx, db, projectID, &summaries[index]); err != nil {
			return nil, err
		}
		summaries[index].ID = SummaryID(summaries[index])
	}
	return summaries, nil
}

func fillSummary(ctx context.Context, db queryer, projectID string, value *Summary) error {
	rows, err := db.QueryContext(ctx, `SELECT model,COUNT(*),SUM(input_tokens),SUM(cache_write_tokens),SUM(cache_write_long_tokens),SUM(cache_read_tokens),SUM(output_tokens),SUM(reasoning_tokens) FROM usage_requests WHERE project_id=? AND client=? AND session_id=? GROUP BY model ORDER BY model`, projectID, value.Client, value.SessionID)
	if err != nil {
		return err
	}
	value.Models = []ModelUsage{}
	for rows.Next() {
		var model ModelUsage
		if err := rows.Scan(&model.Model, &model.Requests, &model.Input, &model.CacheWrite, &model.CacheWriteLong, &model.CacheRead, &model.Output, &model.Reasoning); err != nil {
			rows.Close()
			return err
		}
		value.Models = append(value.Models, model)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.QueryContext(ctx, `SELECT c.tool,COUNT(*),COALESCE(SUM(t.is_error),0),COALESCE(SUM(t.result_bytes),0),COALESCE(MAX(t.result_bytes),0) FROM usage_tool_calls c LEFT JOIN usage_tool_results t ON t.project_id=c.project_id AND t.client=c.client AND t.call_id=c.call_id WHERE c.project_id=? AND c.client=? AND c.session_id=? GROUP BY c.tool ORDER BY c.tool`, projectID, value.Client, value.SessionID)
	if err != nil {
		return err
	}
	value.Tools = []ToolUsage{}
	for rows.Next() {
		var tool ToolUsage
		if err := rows.Scan(&tool.Tool, &tool.Calls, &tool.Errors, &tool.ResultBytes, &tool.MaxBytes); err != nil {
			rows.Close()
			return err
		}
		value.Tools = append(value.Tools, tool)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return db.QueryRowContext(ctx, `SELECT COALESCE((SELECT input_tokens+cache_write_tokens+cache_read_tokens FROM usage_requests WHERE project_id=? AND client=? AND session_id=? AND sidechain=0 ORDER BY occurred_at,request_id LIMIT 1),0)`, projectID, value.Client, value.SessionID).Scan(&value.Overhead)
}

// StoredSummaries returns the summaries imported from the memory repository.
func StoredSummaries(ctx context.Context, db queryer, projectID string) ([]Summary, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,schema_version,client,session_id,instance_id,first_at,last_at,task_id,overhead_tokens,models_json,tools_json FROM usage_session_summaries WHERE project_id=? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Summary
	for rows.Next() {
		var value Summary
		var first, last, models, tools string
		if err := rows.Scan(&value.ID, &value.SchemaVersion, &value.Client, &value.SessionID, &value.InstanceID, &first, &last, &value.TaskID, &value.Overhead, &models, &tools); err != nil {
			return nil, err
		}
		value.FirstAt, value.LastAt = parseTime(first), parseTime(last)
		if err := json.Unmarshal([]byte(models), &value.Models); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tools), &value.Tools); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// InsertSummary stores an imported summary.
func InsertSummary(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectID string, value Summary) error {
	models, err := json.Marshal(value.Models)
	if err != nil {
		return err
	}
	tools, err := json.Marshal(value.Tools)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO usage_session_summaries(project_id,id,client,session_id,instance_id,first_at,last_at,task_id,overhead_tokens,models_json,tools_json,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, value.ID, value.Client, value.SessionID, value.InstanceID, value.FirstAt.UTC().Format(time.RFC3339Nano), value.LastAt.UTC().Format(time.RFC3339Nano), value.TaskID, value.Overhead, string(models), string(tools), value.SchemaVersion)
	return err
}

// remoteSummaries returns the newest imported summary of each session run
// on another machine.
func remoteSummaries(ctx context.Context, db queryer, projectID string) ([]Summary, error) {
	var instance string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT instance_id FROM project_memory_identities WHERE project_id=?),'')`, projectID).Scan(&instance); err != nil {
		return nil, err
	}
	stored, err := StoredSummaries(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	newest := map[string]Summary{}
	for _, value := range stored {
		if value.InstanceID == instance {
			continue // this machine's raw usage is authoritative here
		}
		key := value.Client + ":" + value.SessionID
		if previous, ok := newest[key]; !ok || value.LastAt.After(previous.LastAt) || value.LastAt.Equal(previous.LastAt) && (requests(value) > requests(previous) || requests(value) == requests(previous) && value.SchemaVersion > previous.SchemaVersion) {
			newest[key] = value
		}
	}
	result := make([]Summary, 0, len(newest))
	for _, value := range newest {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func requests(value Summary) int64 {
	var total int64
	for _, model := range value.Models {
		total += model.Requests
	}
	return total
}
