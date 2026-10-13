package usage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Disclaimer is attached to every report.
const Disclaimer = "Captured token counts are exact values from client session files; incompatible recognized usage records stop ingestion instead of being skipped. Cost is an API-rate-equivalent estimate (subscription plans bill differently), shown only for priced models with the applied rate-card provenance. Tool-result tokens are estimated as bytes / 4. Sessions from other machines come from their synced summaries, which are dated by their first request."

// Dimensions a report can break usage down by.
var Dimensions = []string{"session", "day", "model", "client", "task", "tool", "family"}

// Options select and group a report.
type Options struct {
	By      string
	Since   time.Time
	Until   time.Time
	Client  string
	Session string
	Limit   int
}

// Tokens are exact token totals.
type Tokens struct {
	Requests       int64    `json:"requests"`
	Input          int64    `json:"input_tokens"`
	CacheWrite     int64    `json:"cache_write_tokens"`
	CacheWriteLong int64    `json:"cache_write_long_tokens"`
	CacheRead      int64    `json:"cache_read_tokens"`
	Output         int64    `json:"output_tokens"`
	Reasoning      int64    `json:"reasoning_tokens"`
	EstimatedCost  float64  `json:"estimated_cost_usd"`
	UnpricedTokens int64    `json:"unpriced_tokens"`
	UnpricedModels []string `json:"unpriced_models,omitempty"`
}

// CacheHitRatio is the share of prompt tokens read from cache.
func (t Tokens) CacheHitRatio() float64 {
	prompt := t.Input + t.CacheWrite + t.CacheRead
	if prompt == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(prompt)
}

func (t *Tokens) add(other Tokens) {
	t.Requests += other.Requests
	t.Input += other.Input
	t.CacheWrite += other.CacheWrite
	t.CacheWriteLong += other.CacheWriteLong
	t.CacheRead += other.CacheRead
	t.Output += other.Output
	t.Reasoning += other.Reasoning
	t.EstimatedCost += other.EstimatedCost
	t.UnpricedTokens += other.UnpricedTokens
	for _, model := range other.UnpricedModels {
		if !contains(t.UnpricedModels, model) {
			t.UnpricedModels = append(t.UnpricedModels, model)
		}
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// Row is one group of a token report.
type Row struct {
	Key string `json:"key"`
	Tokens
	CacheHitRatio float64 `json:"cache_hit_ratio"`
	Sessions      int     `json:"sessions,omitempty"`
}

// ToolRow is one group of a tool report.
type ToolRow struct {
	Key             string `json:"key"`
	Family          string `json:"family"`
	Calls           int64  `json:"calls"`
	Errors          int64  `json:"errors"`
	ResultBytes     int64  `json:"result_bytes"`
	MaxResultBytes  int64  `json:"max_result_bytes"`
	EstimatedTokens int64  `json:"estimated_result_tokens"`
}

// Overhead is the prompt every session starts with (system prompt, tool
// definitions, instructions, bootstrap), from each session's first request.
type Overhead struct {
	Client   string `json:"client"`
	Sessions int    `json:"sessions"`
	Average  int64  `json:"average_tokens"`
	Median   int64  `json:"median_tokens"`
	Max      int64  `json:"max_tokens"`
}

// Report is a usage summary over a period.
type Report struct {
	By       string     `json:"by"`
	Since    *time.Time `json:"since,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
	Total    Row        `json:"total"`
	Rows     []Row      `json:"rows,omitempty"`
	Tools    []ToolRow  `json:"tools,omitempty"`
	Overhead []Overhead `json:"overhead"`
	// Prices are the exact rate cards applied to priced model rows.
	Prices    []AppliedPrice `json:"prices,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	// RemoteSessions counts sessions from other machines, included through
	// their synced summaries.
	RemoteSessions int    `json:"remote_sessions"`
	Disclaimer     string `json:"disclaimer"`
}

// Family classifies a tool by how it reaches the model: an MCP server tool,
// a skill, a shell command, or a built-in tool of the client.
func Family(tool string) string {
	switch {
	case strings.HasPrefix(tool, "mcp__"):
		return "mcp"
	case tool == "Skill":
		return "skill"
	case tool == "Bash" || tool == "exec" || tool == "exec_command" || tool == "shell" || tool == "local_shell":
		return "cli"
	}
	return "builtin"
}

type Reporter struct {
	DB        *sql.DB
	ProjectID string
	Prices    Prices
}

func (o Options) filter(alias string) (string, []any) {
	var clauses []string
	var args []any
	if !o.Since.IsZero() {
		clauses = append(clauses, alias+".occurred_at >= ?")
		args = append(args, o.Since.UTC().Format(time.RFC3339Nano))
	}
	if !o.Until.IsZero() {
		clauses = append(clauses, alias+".occurred_at < ?")
		args = append(args, o.Until.UTC().Format(time.RFC3339Nano))
	}
	if o.Client != "" {
		clauses = append(clauses, alias+".client = ?")
		args = append(args, o.Client)
	}
	if o.Session != "" {
		clauses = append(clauses, alias+".session_id = ?")
		args = append(args, o.Session)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

// Report builds the summary.
func (r Reporter) Report(ctx context.Context, options Options) (Report, error) {
	if options.Session != "" && options.Client != ClaudeCode && options.Client != Codex {
		return Report{}, fmt.Errorf("session filter needs client claude-code or codex")
	}
	if options.By == "" {
		options.By = "model"
	}
	if !contains(Dimensions, options.By) {
		return Report{}, fmt.Errorf("usage report groups by %s, not %q", strings.Join(Dimensions, ", "), options.By)
	}
	if options.Limit <= 0 || options.Limit > 500 {
		options.Limit = 30
	}
	report := Report{By: options.By, Disclaimer: Disclaimer, Overhead: []Overhead{}}
	remote, err := r.remote(ctx, options)
	if err != nil {
		return Report{}, err
	}
	report.RemoteSessions = len(remote)
	if !options.Since.IsZero() {
		since := options.Since.UTC()
		report.Since = &since
	}
	if !options.Until.IsZero() {
		until := options.Until.UTC()
		report.Until = &until
	}
	groups, sessions, prices, err := r.tokens(ctx, options, remote)
	if err != nil {
		return Report{}, err
	}
	for key, tokens := range groups {
		report.Total.Tokens.add(tokens)
		if options.By != "tool" && options.By != "family" {
			report.Rows = append(report.Rows, Row{Key: key, Tokens: tokens, CacheHitRatio: tokens.CacheHitRatio(), Sessions: len(sessions[key])})
		}
	}
	report.Total.Key, report.Total.CacheHitRatio = "total", report.Total.Tokens.CacheHitRatio()
	sort.Strings(report.Total.UnpricedModels)
	for _, price := range prices {
		report.Prices = append(report.Prices, price)
	}
	sort.Slice(report.Prices, func(i, j int) bool { return report.Prices[i].Model < report.Prices[j].Model })
	if options.By == "day" || options.By == "session" {
		sort.Slice(report.Rows, func(i, j int) bool { return report.Rows[i].Key > report.Rows[j].Key })
	} else {
		sort.Slice(report.Rows, func(i, j int) bool {
			a, b := report.Rows[i], report.Rows[j]
			if a.EstimatedCost != b.EstimatedCost {
				return a.EstimatedCost > b.EstimatedCost
			}
			return a.Input+a.CacheWrite+a.CacheRead+a.Output > b.Input+b.CacheWrite+b.CacheRead+b.Output
		})
	}
	if len(report.Rows) > options.Limit {
		report.Rows, report.Truncated = report.Rows[:options.Limit], true
	}
	if options.By == "tool" || options.By == "family" {
		if report.Tools, err = r.tools(ctx, options, remote); err != nil {
			return Report{}, err
		}
		if len(report.Tools) > options.Limit {
			report.Tools, report.Truncated = report.Tools[:options.Limit], true
		}
	}
	if report.Overhead, err = r.overhead(ctx, options, remote); err != nil {
		return Report{}, err
	}
	return report, nil
}

// tokens sums requests per group key and estimates cost per model.
// remote returns other machines' session summaries within the options.
func (r Reporter) remote(ctx context.Context, options Options) ([]Summary, error) {
	all, err := remoteSummaries(ctx, r.DB, r.ProjectID)
	if err != nil {
		return nil, err
	}
	var result []Summary
	for _, value := range all {
		if (!options.Since.IsZero() && value.FirstAt.Before(options.Since)) || (!options.Until.IsZero() && !value.FirstAt.Before(options.Until)) || (options.Client != "" && value.Client != options.Client) || (options.Session != "" && value.SessionID != options.Session) {
			continue
		}
		result = append(result, value)
	}
	return result, nil
}

func (r Reporter) tokens(ctx context.Context, options Options, remote []Summary) (map[string]Tokens, map[string]map[string]bool, map[string]AppliedPrice, error) {
	where, args := options.filter("u")
	rows, err := r.DB.QueryContext(ctx, `SELECT u.client,u.session_id,u.model,substr(u.occurred_at,1,10),COUNT(*),SUM(u.input_tokens),SUM(u.cache_write_tokens),SUM(u.cache_write_long_tokens),SUM(u.cache_read_tokens),SUM(u.output_tokens),SUM(u.reasoning_tokens)
		FROM usage_requests u WHERE u.project_id=?`+where+` GROUP BY 1,2,3,4`, append([]any{r.ProjectID}, args...)...)
	if err != nil {
		return nil, nil, nil, err
	}
	type part struct {
		client, session, model, day string
		tokens                      Tokens
	}
	var parts []part
	applied := map[string]AppliedPrice{}
	for rows.Next() {
		var p part
		if err := rows.Scan(&p.client, &p.session, &p.model, &p.day, &p.tokens.Requests, &p.tokens.Input, &p.tokens.CacheWrite, &p.tokens.CacheWriteLong, &p.tokens.CacheRead, &p.tokens.Output, &p.tokens.Reasoning); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if price, ok := r.Prices.Resolve(p.model); ok {
			p.tokens.EstimatedCost = price.Cost(p.tokens.Input, p.tokens.CacheWrite, p.tokens.CacheWriteLong, p.tokens.CacheRead, p.tokens.Output)
			applied[p.model] = price
		} else {
			p.tokens.UnpricedTokens = p.tokens.Input + p.tokens.CacheWrite + p.tokens.CacheRead + p.tokens.Output
			p.tokens.UnpricedModels = []string{p.model}
		}
		parts = append(parts, p)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	remoteTasks := map[string]string{}
	for _, value := range remote {
		remoteTasks[value.SessionID] = value.TaskID
		for _, model := range value.Models {
			p := part{client: value.Client, session: value.SessionID, model: model.Model, day: value.FirstAt.Format("2006-01-02"),
				tokens: Tokens{Requests: model.Requests, Input: model.Input, CacheWrite: model.CacheWrite, CacheWriteLong: model.CacheWriteLong, CacheRead: model.CacheRead, Output: model.Output, Reasoning: model.Reasoning}}
			if price, ok := r.Prices.Resolve(p.model); ok {
				p.tokens.EstimatedCost = price.Cost(p.tokens.Input, p.tokens.CacheWrite, p.tokens.CacheWriteLong, p.tokens.CacheRead, p.tokens.Output)
				applied[p.model] = price
			} else {
				p.tokens.UnpricedTokens = p.tokens.Input + p.tokens.CacheWrite + p.tokens.CacheRead + p.tokens.Output
				p.tokens.UnpricedModels = []string{p.model}
			}
			parts = append(parts, p)
		}
	}
	var tasks map[string]string
	if options.By == "task" {
		if tasks, err = r.sessionTasks(ctx); err != nil {
			return nil, nil, nil, err
		}
		for session, task := range remoteTasks {
			tasks[session] = task
		}
	}
	groups := map[string]Tokens{}
	sessions := map[string]map[string]bool{}
	for _, p := range parts {
		key := ""
		switch options.By {
		case "session":
			key = p.client + ":" + p.session
		case "day":
			key = p.day
		case "model":
			key = p.model
		case "client":
			key = p.client
		case "task":
			key = tasks[p.session]
			if key == "" {
				key = "(unattributed)"
			}
		default:
			key = "all"
		}
		total := groups[key]
		total.add(p.tokens)
		groups[key] = total
		if sessions[key] == nil {
			sessions[key] = map[string]bool{}
		}
		sessions[key][p.session] = true
	}
	return groups, sessions, applied, nil
}

// sessionTasks attributes each session to a task: the task of a checkpoint
// recorded with the session's ID, else of the last checkpoint written while
// the session was making requests (attributed by time).
func (r Reporter) sessionTasks(ctx context.Context) (map[string]string, error) {
	return r.sessionTasksFrom(ctx, r.DB, r.ProjectID)
}

func (Reporter) sessionTasksFrom(ctx context.Context, db queryer, projectID string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `WITH windows AS (
			SELECT project_id, session_id, MIN(occurred_at) AS first_at, MAX(occurred_at) AS last_at
			FROM usage_requests WHERE project_id=? GROUP BY project_id, session_id)
		SELECT w.session_id, COALESCE(
			(SELECT c.task_id FROM checkpoints c WHERE c.project_id=w.project_id AND c.session_id=w.session_id AND c.task_id IS NOT NULL ORDER BY c.created_at DESC LIMIT 1),
			(SELECT c.task_id FROM checkpoints c WHERE c.project_id=w.project_id AND c.task_id IS NOT NULL AND c.created_at>=w.first_at AND c.created_at<=w.last_at ORDER BY c.created_at DESC LIMIT 1),
			'')
		FROM windows w`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := map[string]string{}
	for rows.Next() {
		var session, task string
		if err := rows.Scan(&session, &task); err != nil {
			return nil, err
		}
		tasks[session] = task
	}
	return tasks, rows.Err()
}

func (r Reporter) tools(ctx context.Context, options Options, remote []Summary) ([]ToolRow, error) {
	where, args := options.filter("c")
	rows, err := r.DB.QueryContext(ctx, `SELECT c.tool,COUNT(*),COALESCE(SUM(t.is_error),0),COALESCE(SUM(t.result_bytes),0),COALESCE(MAX(t.result_bytes),0)
		FROM usage_tool_calls c LEFT JOIN usage_tool_results t ON t.project_id=c.project_id AND t.client=c.client AND t.call_id=c.call_id
		WHERE c.project_id=?`+where+` GROUP BY c.tool`, append([]any{r.ProjectID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]*ToolRow{}
	for rows.Next() {
		var tool string
		var row ToolRow
		if err := rows.Scan(&tool, &row.Calls, &row.Errors, &row.ResultBytes, &row.MaxResultBytes); err != nil {
			return nil, err
		}
		key, family := tool, Family(tool)
		if options.By == "family" {
			key = family
		}
		existing := byKey[key]
		if existing == nil {
			existing = &ToolRow{Key: key, Family: family}
			byKey[key] = existing
		}
		existing.Calls += row.Calls
		existing.Errors += row.Errors
		existing.ResultBytes += row.ResultBytes
		existing.MaxResultBytes = max(existing.MaxResultBytes, row.MaxResultBytes)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, value := range remote {
		for _, tool := range value.Tools {
			key, family := tool.Tool, Family(tool.Tool)
			if options.By == "family" {
				key = family
			}
			existing := byKey[key]
			if existing == nil {
				existing = &ToolRow{Key: key, Family: family}
				byKey[key] = existing
			}
			existing.Calls += tool.Calls
			existing.Errors += tool.Errors
			existing.ResultBytes += tool.ResultBytes
			existing.MaxResultBytes = max(existing.MaxResultBytes, tool.MaxBytes)
		}
	}
	result := make([]ToolRow, 0, len(byKey))
	for _, row := range byKey {
		row.EstimatedTokens = row.ResultBytes / 4
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ResultBytes > result[j].ResultBytes })
	return result, rows.Err()
}

// overhead measures each session's first main-thread request prompt.
func (r Reporter) overhead(ctx context.Context, options Options, remote []Summary) ([]Overhead, error) {
	where, args := options.filter("u")
	rows, err := r.DB.QueryContext(ctx, `SELECT u.client,u.input_tokens+u.cache_write_tokens+u.cache_read_tokens FROM usage_requests u
		WHERE u.project_id=? AND u.sidechain=0`+where+` AND u.occurred_at=(SELECT MIN(f.occurred_at) FROM usage_requests f WHERE f.project_id=u.project_id AND f.client=u.client AND f.session_id=u.session_id AND f.sidechain=0)
		GROUP BY u.client,u.session_id`, append([]any{r.ProjectID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string][]int64{}
	for rows.Next() {
		var client string
		var prompt int64
		if err := rows.Scan(&client, &prompt); err != nil {
			return nil, err
		}
		values[client] = append(values[client], prompt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, value := range remote {
		if value.Overhead > 0 {
			values[value.Client] = append(values[value.Client], value.Overhead)
		}
	}
	result := []Overhead{}
	for client, prompts := range values {
		sort.Slice(prompts, func(i, j int) bool { return prompts[i] < prompts[j] })
		var sum int64
		for _, value := range prompts {
			sum += value
		}
		result = append(result, Overhead{Client: client, Sessions: len(prompts), Average: sum / int64(len(prompts)), Median: prompts[len(prompts)/2], Max: prompts[len(prompts)-1]})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Client < result[j].Client })
	return result, rows.Err()
}
