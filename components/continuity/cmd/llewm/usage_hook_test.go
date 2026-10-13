package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStopIngestsCodexUsageIdempotently(t *testing.T) {
	t.Run("full", func(t *testing.T) { testStopUsage(t, false) })
	t.Run("audit-only", func(t *testing.T) { testStopUsage(t, true) })
}

func testStopUsage(t *testing.T, auditOnly bool) {
	root := testProject(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	runCLI(t, "", "init")
	dir := filepath.Join(home, "sessions", "2026", "10", "04")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("{\"timestamp\":\"2026-10-04T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"stop-usage\",\"cwd\":%q}}\n", root) +
		`{"timestamp":"2026-10-04T00:00:01Z","type":"turn_context","payload":{"model":"fixture-model"}}` + "\n" +
		`{"timestamp":"2026-10-04T00:00:02Z","type":"token_usage_record","payload":{"response_id":"stop-response","usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-10-04T00-00-00-stop-usage.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"stop-usage","hook_event_name":"Stop"}`
	args := []string{"hook", "codex"}
	if auditOnly {
		args = append(args, "--audit-only")
	}
	if out := runCLI(t, payload, args...); auditOnly && out != "" {
		t.Fatalf("audit Stop emitted %q", out)
	}
	first := runCLI(t, "", "usage", "report", "--by", "model", "--output", "json")
	if !strings.Contains(first, "fixture-model") {
		t.Fatalf("Stop did not ingest: %s", first)
	}
	runCLI(t, payload, args...)
	second := runCLI(t, "", "usage", "report", "--by", "model", "--output", "json")
	if first != second {
		t.Fatalf("repeat Stop changed totals:\n%s\n%s", first, second)
	}
}
