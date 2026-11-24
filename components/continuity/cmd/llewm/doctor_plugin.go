package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/clientinstall"
	"github.com/llewm/llewm/components/continuity/internal/clientperm"
)

// A project-scoped Claude permission setup identifies an intended client.
// With just one detected client, its user-wide rules also identify the client.
func doctorClient(requested, directory string) string {
	if requested != "" {
		return requested
	}
	homes, err := clientperm.DefaultHomes()
	if err != nil {
		return ""
	}
	statuses := clientperm.Detect(homes, directory, nil)
	for _, status := range statuses {
		if status.Client != clientperm.ClaudeCode {
			continue
		}
		if status.OK() && (len(statuses) == 1 || os.Getenv("CLAUDECODE") == "1") {
			return status.Client
		}
		for _, path := range status.Configured {
			if filepath.Dir(path) == filepath.Join(directory, ".claude") {
				return status.Client
			}
		}
	}
	return ""
}

func checkDoctorPlugin(ctx context.Context, application *app.App, client, directory string, recovery bool) (string, string, error) {
	program := client
	if client == "claude-code" {
		program = "claude"
	}
	path, err := exec.LookPath(program)
	if err != nil {
		return "unknown", "unchecked", err
	}
	service := clientinstall.Service{Client: client, ProjectPath: directory, Run: clientinstall.NativeRunnerAt(path, directory)}
	state, err := service.ProjectPlugin(ctx)
	if err != nil {
		return state, "unchecked", err
	}
	if state != "enabled" {
		return state, "unchecked", fmt.Errorf("%s plugin is %s for this checkout. Review the marketplace owner and run llewm install --client %s --dry-run for user-scope setup", client, state, client)
	}
	if recovery {
		var delivered bool
		err = application.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM bootstrap_deliveries WHERE project_id=? AND client=?)", application.ProjectID, client).Scan(&delivered)
		if err != nil {
			return state, "unchecked", err
		}
		if !delivered {
			return state, "missing", fmt.Errorf("no retained SessionStart bootstrap delivery for %s in this project. Open a fresh client session here and verify recovery", client)
		}
		return state, "observed", nil
	}
	return state, "not_required", nil
}
