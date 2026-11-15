package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestHookCredentialsStayOutOfStoreAndBackup(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw_%t", raw), func(t *testing.T) {
			root := testProject(t)
			runCLI(t, "", "init")
			if raw {
				path := filepath.Join(root, ".llewm", "continuity", "config.toml")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(body, []byte("store_raw_payloads = false")) {
					t.Fatal("missing privacy config")
				}
				body = bytes.ReplaceAll(body, []byte("store_raw_payloads = false"), []byte("store_raw_payloads = true"))
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			commands := []string{
				`export GITHUB_TOKEN=fixtureCredential`,
				`curl -H "Authorization: Bearer fixtureCredential"`,
				`curl -H "Authorization: token fixtureCredential"`,
				`curl -H "Authorization: Basic fixtureCredential"`,
				`mytool --token fixtureCredential --password 'fixtureCredential with spaces' --secret "fixtureCredential" --api-key fixtureCredential`,
				`git clone https://user:fixtureCredential@example.test/path`,
				`git clone https://fixtureCredential@example.test/path`,
				`curl -u admin:fixtureCredential`,
				`aws configure set aws_secret_access_key fixtureCredential`,
				`echo sk-proj-abcdefghijklmnopqrstuv ghp_abcdefghijklmnopqrstuv gho_abcdefghijklmnopqrstuv github_pat_abcdefghijklmnopqrstuv AKIAABCDEFGHIJKLMNOP xoxb-1234567890-abcdefghijkl`,
			}
			for _, client := range []string{"claude-code", "codex"} {
				body, err := json.Marshal(map[string]any{
					"session_id": "privacy-" + client, "hook_event_name": "PostToolUse", "tool_name": "Bash",
					"tool_input":    map[string]any{"command": strings.Join(commands, "\n")},
					"tool_response": map[string]any{"aws_secret_access_key": "fixtureCredential", "nested": map[string]any{"environment": map[string]any{"SOMETHING": "fixtureCredential"}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				runCLI(t, string(body), "hook", client)
			}
			path := filepath.Join(root, ".llewm", "continuity", "continuity.db")
			backup := filepath.Join(t.TempDir(), "snapshot.db")
			if _, err := database.Backup(context.Background(), path, backup); err != nil {
				t.Fatal(err)
			}
			for _, snapshot := range []string{path, backup} {
				db, err := database.OpenReadOnly(context.Background(), snapshot)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := db.Query(`SELECT payload_json FROM events WHERE event_type='posttooluse'`)
				if err != nil {
					db.Close()
					t.Fatal(err)
				}
				count := 0
				for rows.Next() {
					var payload string
					if err := rows.Scan(&payload); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(payload, "[REDACTED]") || strings.Contains(payload, "client_event") != raw {
						t.Fatalf("unexpected payload %s", payload)
					}
					count++
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				db.Close()
				if count != 2 {
					t.Fatalf("got %d hook rows", count)
				}
				// Scan the entire SQLite file, including pages outside the events
				// query, so metadata and raw-payload paths cannot hide a leak.
				body, err := os.ReadFile(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"fixtureCredential", "abcdefghijklmnopqrstuv", "AKIAABCDEFGHIJKLMNOP", "xoxb-1234567890-abcdefghijkl"} {
					if bytes.Contains(body, []byte(secret)) {
						t.Fatalf("credential %q persisted in %s", secret, snapshot)
					}
				}
			}
		})
	}
}
