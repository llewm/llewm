package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/usage"
)

// auditHook deliberately never decodes tool inputs, raw payloads or attribution.
// Successful hooks write nothing to either output stream.
func auditHook(cmd *cobra.Command, application *app.App, client string, body []byte) error {
	client = strings.ToLower(client)
	if client == "claude" {
		client = usage.ClaudeCode
	}
	if client != usage.ClaudeCode && client != usage.Codex {
		return fmt.Errorf("unsupported client %q", client)
	}
	var envelope struct {
		SessionID string `json:"session_id"`
		HookEvent string `json:"hook_event_name"`
		EventName string `json:"event_name"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	name := envelope.HookEvent
	if name == "" {
		name = envelope.EventName
	}
	name = strings.ToLower(name)
	if name == "" {
		return fmt.Errorf("hook event name is required")
	}
	// Other hooks may still be registered by the full plugin. Ignore them entirely.
	switch name {
	case "sessionstart", "stop", "sessionend", "precompact":
	default:
		return nil
	}
	if envelope.SessionID == "" {
		return fmt.Errorf("audit-only hook needs session_id")
	}
	event := activity.NewEvent(application.ProjectID, client, name, "lifecycle")
	event.SessionID = envelope.SessionID
	event.Payload = map[string]any{"continuity_mode": "audit-only"}
	if err := application.Events.Append(cmd.Context(), event); err != nil {
		return err
	}
	if err := application.Events.ObserveSession(cmd.Context(), application.ProjectID, event.SessionID, client, "", name, event.OccurredAt); err != nil {
		return err
	}
	// No all-session background scan on startup: only this session at end boundaries.
	if name != "sessionstart" {
		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
		defer cancel()
		result, err := usageIngester(ctx, application).Session(ctx, client, event.SessionID)
		if err != nil {
			return err
		}
		if result.ParseIssues > 0 {
			return fmt.Errorf("audit ingestion found %d incompatible usage records; run usage ingest to inspect coverage", result.ParseIssues)
		}
	}
	return nil
}

func modeCommand() *cobra.Command {
	return &cobra.Command{
		Use: "mode [full|audit-only]", Short: "Read or select project hook behavior; audit-only emits no context",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			if len(args) > 0 {
				if _, err := config.SetMode(root, args[0]); err != nil {
					return err
				}
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), cfg.Mode)
			if len(args) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Start a fresh client session. For an uncontaminated baseline disable the full plugin/MCP/skills and use offline usage ingest, or register only client audit-hooks.")
			}
			return nil
		},
	}
}

func auditHooksCommand() *cobra.Command {
	var client string
	command := &cobra.Command{
		Use: "audit-hooks", Short: "Print hook-only settings for silent usage collection; no MCP or skills",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if client != usage.ClaudeCode && client != usage.Codex {
				return fmt.Errorf("client must be claude-code or codex")
			}
			hooks := map[string]any{}
			for _, name := range []string{"SessionStart", "Stop", "SessionEnd", "PreCompact"} {
				hooks[name] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "llewm hook " + client + " --audit-only", "timeout": 5}}}}
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{"hooks": hooks})
		},
	}
	command.Flags().StringVar(&client, "client", usage.ClaudeCode, "claude-code or codex")
	return command
}
