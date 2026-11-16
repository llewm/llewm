package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPInitializesWithoutAnAvailableProject(t *testing.T) {
	for _, name := range []string{"outside_git", "uninitialized_git", "explicit_outside_git", "missing_explicit_root", "pending_migration"} {
		t.Run(name, func(t *testing.T) {
			root := testProject(t)
			cwd, explicit := root, ""
			if strings.Contains(name, "outside_git") || name == "missing_explicit_root" {
				cwd = t.TempDir()
			}
			var before []byte
			dbPath := filepath.Join(root, ".llewm", "continuity", "continuity.db")
			if name == "pending_migration" {
				runCLI(t, "", "init")
				db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=?`, latestSchema); err != nil {
					db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				before, err = os.ReadFile(dbPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			if name == "explicit_outside_git" {
				explicit = cwd
			} else if name == "missing_explicit_root" {
				explicit = filepath.Join(cwd, "missing")
			}
			ctx, cancel := context.WithTimeout(context.Background(), mcpIntegrationTimeout)
			defer cancel()
			child := mcpTestCommand(ctx)
			child.Dir = cwd
			child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+explicit)
			client := mcp.NewClient(&mcp.Implementation{Name: "unavailable-project", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			list, err := session.ListTools(ctx, nil)
			if err != nil || len(list.Tools) != expectedMCPToolCount {
				t.Fatalf("catalog: %+v %v", list, err)
			}
			for _, tool := range list.Tools {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{}})
				if err != nil {
					t.Fatalf("%s protocol error: %v", tool.Name, err)
				}
				body, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if tool.Name == "continuity_info" {
					if result.IsError || !strings.Contains(string(body), `"project_status":"unavailable"`) || !strings.Contains(string(body), "project_error") {
						t.Fatalf("info: %s", body)
					}
					if name == "pending_migration" && !strings.Contains(string(body), "migration_required") {
						t.Fatalf("missing migration diagnosis: %s", body)
					}
				} else if !result.IsError || !strings.Contains(string(body), "Continuity project unavailable") {
					t.Fatalf("unguarded %s: %s", tool.Name, body)
				}
			}
			if name == "pending_migration" {
				after, err := os.ReadFile(dbPath)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("MCP changed unavailable store: %v", err)
				}
				return
			}
			for _, base := range []string{root, cwd} {
				if _, err := os.Stat(filepath.Join(base, ".llewm")); !os.IsNotExist(err) {
					t.Fatalf("MCP created project state at %s: %v", base, err)
				}
			}
		})
	}
}
