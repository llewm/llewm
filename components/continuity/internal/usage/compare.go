package usage

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// RunRef selects one complete local session, including attributed subagent usage.
type RunRef struct {
	Client  string `json:"client"`
	Session string `json:"session_id"`
}

func ParseRunRef(value string) (RunRef, error) {
	client, session, ok := strings.Cut(value, ":")
	if !ok || (client != ClaudeCode && client != Codex) || session == "" || strings.ContainsAny(session, " /\\*?[:\t\n\r") {
		return RunRef{}, fmt.Errorf("run %q must be claude-code:SESSION or codex:SESSION", value)
	}
	return RunRef{client, session}, nil
}

type Run struct {
	Ref                  RunRef         `json:"ref"`
	Tokens               Tokens         `json:"tokens"`
	Models               []Row          `json:"models"`
	Prices               []AppliedPrice `json:"prices,omitempty"`
	Tools                []ToolRow      `json:"tools"`
	FirstAt              string         `json:"first_request_at"`
	LastAt               string         `json:"last_request_at"`
	RequestWindowSeconds float64        `json:"request_window_seconds"`
	StartupPrompt        *int64         `json:"startup_prompt_tokens"`
	ObservedModes        []string       `json:"observed_hook_modes"`
	Sources              int            `json:"matched_source_files"`
	PendingBytes         int64          `json:"pending_source_bytes"`
	UnavailableSources   int            `json:"unavailable_source_files"`
	ChangedSources       int            `json:"changed_source_files"`
}

// Metric differences are candidate minus baseline. Negative means a reduction.
type Metric struct {
	Name          string   `json:"name"`
	Baseline      float64  `json:"baseline"`
	Candidate     float64  `json:"candidate"`
	Delta         float64  `json:"delta"`
	ChangePercent *float64 `json:"change_percent"`
}

type Comparison struct {
	SchemaVersion int      `json:"schema_version"`
	Baseline      Run      `json:"baseline"`
	Candidate     Run      `json:"candidate"`
	Metrics       []Metric `json:"metrics"`
	Warnings      []string `json:"warnings"`
	Disclaimer    string   `json:"disclaimer"`
}

func (r Reporter) localRun(ctx context.Context, ref RunRef) (Run, error) {
	options := Options{By: "model", Client: ref.Client, Session: ref.Session, Limit: 500}
	// Explicit comparisons are local-only: remote summaries have different coverage.
	groups, sessions, prices, err := r.tokens(ctx, options, nil)
	if err != nil {
		return Run{}, err
	}
	run := Run{Ref: ref, Models: []Row{}, ObservedModes: []string{}}
	for _, key := range sortedKeys(groups) {
		t := groups[key]
		run.Tokens.add(t)
		run.Models = append(run.Models, Row{Key: key, Tokens: t, CacheHitRatio: t.CacheHitRatio(), Sessions: len(sessions[key])})
		if p, ok := prices[key]; ok {
			run.Prices = append(run.Prices, p)
		}
	}
	if run.Tokens.Requests == 0 {
		return Run{}, fmt.Errorf("no local usage requests for %s:%s; ingest that session first", ref.Client, ref.Session)
	}
	if run.Tools, err = r.tools(ctx, options, nil); err != nil {
		return Run{}, err
	}
	if err := r.DB.QueryRowContext(ctx, `SELECT MIN(occurred_at),MAX(occurred_at) FROM usage_requests WHERE project_id=? AND client=? AND session_id=?`, r.ProjectID, ref.Client, ref.Session).Scan(&run.FirstAt, &run.LastAt); err != nil {
		return Run{}, err
	}
	first, _ := time.Parse(time.RFC3339Nano, run.FirstAt)
	last, _ := time.Parse(time.RFC3339Nano, run.LastAt)
	run.RequestWindowSeconds = last.Sub(first).Seconds()
	overhead, err := r.overhead(ctx, options, nil)
	if err != nil {
		return Run{}, err
	}
	if len(overhead) > 0 {
		value := overhead[0].Median
		run.StartupPrompt = &value
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT json_extract(payload_json,'$.continuity_mode') FROM events WHERE project_id=? AND client=? AND session_id=? AND json_extract(payload_json,'$.continuity_mode') IS NOT NULL ORDER BY 1`, r.ProjectID, ref.Client, ref.Session)
	if err != nil {
		return Run{}, err
	}
	for rows.Next() {
		var mode string
		if err := rows.Scan(&mode); err != nil {
			rows.Close()
			return Run{}, err
		}
		run.ObservedModes = append(run.ObservedModes, mode)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Run{}, err
	}
	rows, err = r.DB.QueryContext(ctx, `SELECT path,size,offset FROM usage_sources WHERE project_id=? AND client=? AND session_id=? AND matched=1`, r.ProjectID, ref.Client, ref.Session)
	if err != nil {
		return Run{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var size, offset int64
		if err := rows.Scan(&path, &size, &offset); err != nil {
			return Run{}, err
		}
		run.Sources++
		current := size
		if info, err := os.Stat(path); err != nil {
			run.UnavailableSources++
		} else {
			current = info.Size()
			if current != size {
				run.ChangedSources++
			}
		}
		run.PendingBytes += max(0, current-offset)
	}
	return run, rows.Err()
}

func sortedKeys(values map[string]Tokens) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (r Reporter) Compare(ctx context.Context, baseline, candidate RunRef) (Comparison, error) {
	// Validate structured callers as well as CLI selectors.
	for _, ref := range []RunRef{baseline, candidate} {
		if _, err := ParseRunRef(ref.Client + ":" + ref.Session); err != nil {
			return Comparison{}, err
		}
	}
	if baseline == candidate {
		return Comparison{}, fmt.Errorf("baseline and candidate must be different sessions")
	}
	out := Comparison{SchemaVersion: 1, Warnings: []string{}, Disclaimer: "Usage differences describe these recorded sessions, not causal savings or task quality. Token counts are provider-reported; result tokens and API-rate-equivalent costs are estimates. Reasoning is included in output and is not added twice. The request window includes idle time and is not task duration. Hook modes do not prove plugin/MCP/skills were absent. Coverage is limited to recognized local client records; source cursors do not prove complete provider billing. Match task, code revision, client/model/settings and cache conditions, and verify equal outcomes separately."}
	var err error
	if out.Baseline, err = r.localRun(ctx, baseline); err != nil {
		return Comparison{}, err
	}
	if out.Candidate, err = r.localRun(ctx, candidate); err != nil {
		return Comparison{}, err
	}
	add := func(name string, b, c float64) {
		m := Metric{Name: name, Baseline: b, Candidate: c, Delta: c - b}
		if b != 0 {
			p := 100 * (c - b) / b
			m.ChangePercent = &p
		}
		out.Metrics = append(out.Metrics, m)
	}
	b, c := out.Baseline.Tokens, out.Candidate.Tokens
	for _, v := range []struct {
		name string
		b, c int64
	}{
		{"requests", b.Requests, c.Requests}, {"input_tokens", b.Input, c.Input}, {"cache_write_tokens", b.CacheWrite, c.CacheWrite}, {"cache_read_tokens", b.CacheRead, c.CacheRead}, {"output_tokens", b.Output, c.Output}, {"reasoning_tokens_in_output", b.Reasoning, c.Reasoning},
		{"total_tokens", b.Input + b.CacheWrite + b.CacheRead + b.Output, c.Input + c.CacheWrite + c.CacheRead + c.Output},
	} {
		add(v.name, float64(v.b), float64(v.c))
	}
	add("request_window_seconds", out.Baseline.RequestWindowSeconds, out.Candidate.RequestWindowSeconds)
	if out.Baseline.StartupPrompt != nil && out.Candidate.StartupPrompt != nil {
		add("startup_prompt_tokens", float64(*out.Baseline.StartupPrompt), float64(*out.Candidate.StartupPrompt))
	}
	toolTotals := func(run Run) (calls, bytes, errors int64) {
		for _, tool := range run.Tools {
			calls += tool.Calls
			bytes += tool.ResultBytes
			errors += tool.Errors
		}
		return
	}
	bc, bb, be := toolTotals(out.Baseline)
	cc, cb, ce := toolTotals(out.Candidate)
	add("tool_calls", float64(bc), float64(cc))
	add("tool_result_bytes", float64(bb), float64(cb))
	add("reported_tool_errors", float64(be), float64(ce))
	if b.UnpricedTokens == 0 && c.UnpricedTokens == 0 {
		add("estimated_cost_usd", b.EstimatedCost, c.EstimatedCost)
	} else {
		out.Warnings = append(out.Warnings, "Some tokens are unpriced; no total cost comparison is reported.")
	}
	if baseline.Client != candidate.Client {
		out.Warnings = append(out.Warnings, "Clients differ; this is not a controlled same-client comparison.")
	}
	modelKeys := func(run Run) string {
		keys := []string{}
		for _, row := range run.Models {
			keys = append(keys, row.Key)
		}
		return strings.Join(keys, "\n")
	}
	if modelKeys(out.Baseline) != modelKeys(out.Candidate) {
		out.Warnings = append(out.Warnings, "Model sets differ; token and cost differences cannot be attributed to Llewm alone.")
	}
	for _, run := range []Run{out.Baseline, out.Candidate} {
		name := run.Ref.Client + ":" + run.Ref.Session
		if run.Sources == 0 || run.PendingBytes > 0 || run.UnavailableSources > 0 || run.ChangedSources > 0 {
			out.Warnings = append(out.Warnings, name+": source coverage is absent or pending; finish the session and ingest again.")
		}
		if len(run.ObservedModes) != 1 {
			out.Warnings = append(out.Warnings, name+": hook mode is unknown or mixed; offline baseline labels are caller-supplied.")
		}
	}
	return out, nil
}
