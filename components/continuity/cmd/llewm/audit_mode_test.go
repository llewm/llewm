package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/app"
)

func TestAuditOnlyHooksAreSilentAndDoNotPersistPayloadsOrContinuityState(t *testing.T) {
	for _, client := range []string{"codex", "claude-code"} {
		t.Run(client, func(t *testing.T) {
			root := testProject(t)
			runCLI(t, "", "init")
			path := filepath.Join(root, ".llewm/continuity", "config.toml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.ReplaceAll(string(data), "store_raw_payloads = false", "store_raw_payloads = true"))
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("\n[sync]\nremote='https://invalid.invalid/should-not-sync'\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			runCLI(t, "", "mode", "audit-only")
			old := startBackgroundUsage
			startBackgroundUsage = func(string, string) error { t.Fatal("audit-only launched global usage scan"); return nil }
			defer func() { startBackgroundUsage = old }()
			for _, name := range []string{"SessionStart", "PostToolUse", "UserPromptSubmit", "PreCompact", "Stop", "SessionEnd"} {
				body := fmt.Sprintf(`{"session_id":"audit-session","hook_event_name":%q,"tool_input":{"command":"private-input"},"prompt":"private-prompt","tool_response":"private-output"}`, name)
				if out := runCLI(t, body, "hook", client); out != "" {
					t.Fatalf("%s leaked output: %q", name, out)
				}
			}
			a, err := app.Open(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			for _, table := range []string{"actor_invocations", "presence_events", "bootstrap_deliveries", "checkpoints"} {
				var count int
				if err := a.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count %d: %v", table, count, err)
				}
			}
			var count int
			var payload string
			if err := a.DB.QueryRow("SELECT COUNT(*),group_concat(payload_json) FROM events").Scan(&count, &payload); err != nil || count != 4 || strings.Contains(payload, "private-") || !strings.Contains(payload, "audit-only") {
				t.Fatalf("events: %d %s %v", count, payload, err)
			}
			var status string
			if err := a.DB.QueryRow("SELECT status FROM sessions WHERE id='audit-session'").Scan(&status); err != nil || status != "ended" {
				t.Fatalf("session: %s %v", status, err)
			}
		})
	}
}

func TestAuditFlagOverridesFullModeWithoutChangingProject(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	if out := runCLI(t, `{"session_id":"s","hook_event_name":"SessionStart"}`, "hook", "codex", "--audit-only"); out != "" {
		t.Fatal(out)
	}
	if out := runCLI(t, "", "mode"); out != "full\n" {
		t.Fatal(out)
	}
	for _, client := range []string{"codex", "claude-code"} {
		out := runCLI(t, "", "client", "audit-hooks", "--client", client)
		if !strings.Contains(out, "--audit-only") || strings.Contains(out, "additionalContext") || strings.Contains(out, "PostToolUse") || strings.Contains(out, "mcp") {
			t.Fatal(out)
		}
	}
}

func TestAuditModeRefusesMCPStartup(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "mode", "audit-only")
	cmd := newRootCommand("llewm")
	cmd.SetArgs([]string{"mcp"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "audit-only mode does not serve MCP") {
		t.Fatalf("MCP startup: %v", err)
	}
}
