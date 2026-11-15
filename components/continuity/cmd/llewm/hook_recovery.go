package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/clients"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/diagnostic"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Recovery markers contain only hashes and timestamps, never event payloads.
// They are an optional bounded cache, not continuity state or write authority.
func hookRecovery(cmd *cobra.Command, root, client string, body []byte, auditOnly bool, err error) error {
	if err == nil {
		return nil
	}
	detail := diagnostic.From(err)
	if !detail.Advisory {
		return err
	}
	if auditOnly {
		return nil
	}
	var event clients.HookEnvelope
	if json.Unmarshal(body, &event) != nil {
		return err
	}
	name := event.HookEvent
	if name == "" {
		name = event.EventName
	}
	if !strings.EqualFold(name, "SessionStart") && !strings.EqualFold(name, "PostToolUse") {
		return nil
	}
	if strings.EqualFold(name, "SessionStart") {
		name = "SessionStart"
	} else {
		name = "PostToolUse"
	}
	digest := sha256.Sum256([]byte(client + "\x00" + event.SessionID + "\x00" + detail.Code + "\x00" + detail.Message))
	key := hex.EncodeToString(digest[:])
	path := filepath.Join(config.StateDir(root), "hook-recovery.json")
	markers := map[string]int64{}
	now := time.Now().Unix()
	if stat, e := os.Stat(path); e == nil && stat.Size() <= 16384 {
		if data, e := os.ReadFile(path); e == nil {
			_ = json.Unmarshal(data, &markers)
		}
	}
	if markers == nil {
		markers = map[string]int64{}
	}
	for k, stamp := range markers {
		if stamp > now || now-stamp > 86400 {
			delete(markers, k)
		}
	}
	if event.SessionID != "" && markers[key] != 0 {
		return nil
	}
	if len(markers) >= 16 {
		markers = map[string]int64{}
	}
	markers[key] = now
	// Do not create a project directory merely to suppress a warning. If the
	// optional marker cannot be written, recovery still reaches the client.
	if data, e := json.Marshal(markers); e == nil {
		_ = os.WriteFile(path, data, 0600)
	}
	return writeJSON(cmd.OutOrStdout(), map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": name, "additionalContext": "Continuity advisory (" + detail.Code + "): " + detail.Message + " " + detail.Recovery}, "diagnostic": detail})
}
