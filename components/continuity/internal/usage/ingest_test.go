package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

const claudeLines = `{"type":"user","sessionId":"s1","cwd":"ROOT","timestamp":"2026-10-03T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","sessionId":"s1","cwd":"ROOT","timestamp":"2026-10-03T10:00:01Z","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"secret"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":16000,"cache_read_input_tokens":21000,"output_tokens":200,"output_tokens_details":{"thinking_tokens":20}}}}
{"type":"assistant","sessionId":"s1","cwd":"ROOT","timestamp":"2026-10-03T10:00:01Z","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"mcp__continuity__continuity_work","input":{"q":"private"}}],"usage":{"input_tokens":2,"cache_creation_input_tokens":16000,"cache_read_input_tokens":21000,"output_tokens":200,"output_tokens_details":{"thinking_tokens":20}}}}
{"type":"user","sessionId":"s1","cwd":"ROOT","timestamp":"2026-10-03T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"0123456789"}]}]}}
`

const codexLines = `{"timestamp":"2026-10-03T11:00:00Z","type":"session_meta","payload":{"id":"c1","cwd":"ROOT"}}
{"timestamp":"2026-10-03T11:00:01Z","type":"turn_context","payload":{"model":"gpt-6-luna","cwd":"ROOT"}}
{"timestamp":"2026-10-03T11:00:02Z","type":"response_item","payload":{"type":"function_call","call_id":"call_1","name":"exec","arguments":"{\"cmd\":\"ls\"}"}}
{"timestamp":"2026-10-03T11:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"{\"output\":\"abcde\",\"metadata\":{\"exit_code\":0}}"}}
{"timestamp":"2026-10-03T11:00:04Z","type":"token_usage_record","payload":{"response_id":"resp_1","usage":{"input_tokens":1000,"cached_input_tokens":800,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":30}}}
`

func TestIngestReadsUsageOnceAndResumes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(dir, "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := filepath.Join(dir, "repo")
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "repo")
	if err != nil {
		t.Fatal(err)
	}
	claudeHome, codexHome := filepath.Join(dir, "claude"), filepath.Join(dir, "codex")
	claudeDir := filepath.Join(claudeHome, "projects", nonAlnum.ReplaceAllString(root, "-"))
	codexDir := filepath.Join(codexHome, "sessions", "2026", "10", "03")
	for _, path := range []string{claudeDir, codexDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "ROOT", root)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	claudeFile := filepath.Join(claudeDir, "s1.jsonl")
	write(claudeFile, claudeLines+`{"type":"assistant","partial`) // a final line still being written
	write(filepath.Join(codexDir, "rollout-2026-10-03T11-00-00-c1.jsonl"), codexLines)
	write(filepath.Join(codexDir, "rollout-2026-10-03T12-00-00-other.jsonl"), strings.ReplaceAll(codexLines, `"cwd":"ROOT"`, `"cwd":"/elsewhere"`))

	ingester := Ingester{DB: db, ProjectID: projectID, Roots: []string{root}, ClaudeHome: claudeHome, CodexHome: codexHome}
	result, err := ingester.All(ctx)
	if err != nil || result != (Result{Files: 2, UsageRecords: 3, Requests: 2, Calls: 2, Results: 2}) {
		t.Fatalf("first ingest: %+v %v", result, err)
	}
	var input, write5, read, output, reasoning int64
	if err := db.QueryRow(`SELECT input_tokens,cache_write_tokens,cache_read_tokens,output_tokens,reasoning_tokens FROM usage_requests WHERE request_id='resp_1'`).Scan(&input, &write5, &read, &output, &reasoning); err != nil || input != 200 || read != 800 || output != 50 || reasoning != 30 {
		t.Fatalf("codex request normalized: %d %d %d %d %v", input, read, output, reasoning, err)
	}
	var tool string
	var bytes int64
	if err := db.QueryRow(`SELECT c.tool,r.result_bytes FROM usage_tool_calls c JOIN usage_tool_results r USING(project_id,client,call_id) WHERE c.call_id='toolu_1'`).Scan(&tool, &bytes); err != nil || tool != "mcp__continuity__continuity_work" || bytes != 10 {
		t.Fatalf("claude tool result: %s %d %v", tool, bytes, err)
	}
	if err := db.QueryRow(`SELECT r.result_bytes FROM usage_tool_results r WHERE call_id='call_1'`).Scan(&bytes); err != nil || bytes != 5 {
		t.Fatalf("codex tool result: %d %v", bytes, err)
	}

	// Nothing new: nothing is read again. Then the partial line completes.
	if again, err := ingester.All(ctx); err != nil || again != (Result{}) {
		t.Fatalf("second ingest: %+v %v", again, err)
	}
	write(claudeFile, claudeLines+`{"type":"assistant","sessionId":"s1","cwd":"ROOT","timestamp":"2026-10-03T10:01:00Z","message":{"id":"msg_2","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":37000,"output_tokens":10}}}`+"\n")
	if resumed, err := ingester.Session(ctx, ClaudeCode, "s1"); err != nil || resumed != (Result{Files: 1, UsageRecords: 1, Requests: 1}) {
		t.Fatalf("resumed ingest: %+v %v", resumed, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT group_concat(tool) FROM usage_tool_calls`).Scan(&stored); err != nil || strings.Contains(stored, "private") {
		t.Fatalf("content leaked: %s %v", stored, err)
	}
}

func TestReportEstimatesCostAndMeasuresTools(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(dir, "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(dir, "repo"), "repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_write_long_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,'claude-code','s1','r1','claude-opus-5-5','2026-10-03T10:00:00Z',1000000,1000000,1000000,1000000,1000000,0,0)`,
		`INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_write_long_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,'codex','c1','r2','gpt-6-luna','2026-10-03T11:00:00Z',10,0,0,90,5,0,0)`,
		`INSERT INTO usage_tool_calls(project_id,client,session_id,call_id,tool,occurred_at) VALUES(?,'claude-code','s1','t1','mcp__continuity__continuity_work','2026-10-03T10:00:00Z')`,
		`INSERT INTO usage_tool_results(project_id,client,session_id,call_id,result_bytes,is_error,occurred_at) VALUES(?,'claude-code','s1','t1',4000,0,'2026-10-03T10:00:01Z')`,
		`INSERT INTO usage_tool_calls(project_id,client,session_id,call_id,tool,occurred_at) VALUES(?,'claude-code','s1','t2','Bash','2026-10-03T10:00:02Z')`,
	} {
		if _, err := db.Exec(statement, projectID); err != nil {
			t.Fatal(err)
		}
	}
	reporter := Reporter{DB: db, ProjectID: projectID}
	report, err := reporter.Report(ctx, Options{By: "model"})
	if err != nil {
		t.Fatal(err)
	}
	// Opus 5.5: $4 input + $8 long cache write + $0.20 cache read + $20 output.
	if got := report.Total.EstimatedCost; got < 32.19 || got > 32.21 || report.Total.UnpricedTokens != 105 || len(report.Total.UnpricedModels) != 1 {
		t.Fatalf("cost: %+v", report.Total)
	}
	if len(report.Overhead) != 2 || report.Overhead[0].Client != "claude-code" || report.Overhead[0].Median != 3000000 {
		t.Fatalf("overhead: %+v", report.Overhead)
	}
	priced, err := Reporter{DB: db, ProjectID: projectID, Prices: Prices{Overrides: map[string]Price{"gpt-6-luna": {Input: 1, CacheRead: 0.1, Output: 10, Source: "official test rate", AsOf: "2026-10-05", Basis: "standard API"}}}}.Report(ctx, Options{By: "client"})
	if err != nil || priced.Total.UnpricedTokens != 0 || len(priced.Rows) != 2 || len(priced.Prices) != 2 || priced.Prices[1].Model != "gpt-6-luna" || priced.Prices[1].Source != "official test rate" {
		t.Fatalf("override: %+v %v", priced, err)
	}
	families, err := reporter.Report(ctx, Options{By: "family"})
	if err != nil || len(families.Tools) != 2 || families.Tools[0].Key != "mcp" || families.Tools[0].ResultBytes != 4000 || families.Tools[0].EstimatedTokens != 1000 || families.Tools[1].Key != "cli" {
		t.Fatalf("families: %+v %v", families.Tools, err)
	}
	if _, err := db.Exec(`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at,task_id) VALUES('cp1',?,'Build','in_progress','s','n','{}','2026-10-03T10:00:00Z','build')`, projectID); err != nil {
		t.Fatal(err)
	}
	tasks, err := reporter.Report(ctx, Options{By: "task"})
	if err != nil || len(tasks.Rows) != 2 || tasks.Rows[0].Key != "build" || tasks.Rows[1].Key != "(unattributed)" {
		t.Fatalf("tasks: %+v %v", tasks.Rows, err)
	}
	if _, err := reporter.Report(ctx, Options{By: "weather"}); err == nil {
		t.Fatal("unknown dimension accepted")
	}
}

func TestCodexParserMarksSubagentsErrorsAndUsageDrift(t *testing.T) {
	var batch Batch
	parser := &codexParser{}
	for _, line := range []string{
		`{"timestamp":"2026-10-05T08:00:00Z","type":"session_meta","payload":{"id":"child","session_id":"parent","cwd":"/repo","source":{"subagent":{"other":"guardian"}}}}`,
		`{"timestamp":"2026-10-05T08:00:01Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-10-05T08:00:02Z","type":"token_usage_record","payload":{"response_id":"ok","usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":20,"reasoning_output_tokens":5}}}`,
		`{"timestamp":"2026-10-05T08:00:03Z","type":"token_usage_record","payload":{"response_id":"bad","usage":{"input_tokens":10,"cached_input_tokens":20,"output_tokens":1}}}`,
		`{"timestamp":"2026-10-05T08:00:04Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call","output":"{\"output\":\"failed text\",\"metadata\":{\"exit_code\":1}}"}}`,
	} {
		parser.line([]byte(line), &batch)
	}
	if batch.SessionID != "parent" || len(batch.Requests) != 1 || batch.Requests[0].SessionID != "parent" || !batch.Requests[0].Sidechain {
		t.Fatalf("subagent request: %+v", batch)
	}
	if batch.UsageRecords != 2 || batch.ParseIssues != 1 {
		t.Fatalf("usage diagnostics: %+v", batch)
	}
	if len(batch.Results) != 1 || !batch.Results[0].IsError || batch.Results[0].ResultBytes != int64(len("failed text")) {
		t.Fatalf("error result: %+v", batch.Results)
	}
}

func TestIngestStopsBeforeAdvancingOnUsageDrift(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(dir, "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := filepath.Join(dir, "repo")
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "repo")
	if err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(dir, "codex")
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "10", "05")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(`{"timestamp":"2026-10-05T08:00:00Z","type":"session_meta","payload":{"id":"drift","cwd":"ROOT"}}
{"timestamp":"2026-10-05T08:00:01Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}
{"timestamp":"2026-10-05T08:00:02Z","type":"token_usage_record","payload":{"response_id":"bad","usage":{"input_tokens":10,"cached_input_tokens":20,"output_tokens":1}}}
`, "ROOT", root)
	if err := os.WriteFile(filepath.Join(sessionDir, "rollout-2026-10-05T08-00-00-drift.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = (Ingester{DB: db, ProjectID: projectID, Roots: []string{root}, CodexHome: codexHome}).Session(ctx, Codex, "drift")
	if err == nil || !strings.Contains(err.Error(), "incompatible usage record") {
		t.Fatalf("drift error: %v", err)
	}
	var sources int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_sources`).Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("cursor advanced: sources=%d err=%v", sources, err)
	}
}
