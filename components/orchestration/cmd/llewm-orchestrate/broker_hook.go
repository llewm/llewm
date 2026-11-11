package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/privatestorage"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
)

type hookCursor struct {
	Endpoint  string `json:"endpoint"`
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	BrokerID  string `json:"broker_id"`
	After     int64  `json:"after"`
}

// brokerHook accepts a host envelope but persists only session identity and a
// cursor. It never retains tool input, transcript paths or the raw envelope.
// Its cursor tracks notifications, independently of an agent's inspected inbox.
// Exactly one of cursorPath and cursorDir is set; cursorDir derives a private
// per-session/task file so one static host hook configuration serves every session.
func brokerHook(ctx context.Context, endpoint, tokenPath, agent, task, state, cursorPath, cursorDir string, input io.Reader, out io.Writer) error {
	if err := transport.ValidateLocalEndpoint(endpoint); err != nil {
		return err
	}
	if (cursorPath == "") == (cursorDir == "") {
		return errors.New("exactly one of --cursor-file (unique to this session/task) or --cursor-dir is required")
	}
	body, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return errors.New("hook envelope too large")
	}
	var event struct {
		SessionID string `json:"session_id"`
		Name      string `json:"hook_event_name"`
	}
	if err = json.Unmarshal(body, &event); err != nil {
		return errors.New("invalid hook envelope")
	}
	if cursorDir != "" {
		if event.SessionID == "" {
			return errors.New("hook envelope has no session_id")
		}
		cursorPath = filepath.Join(cursorDir, hookCursorName(event.SessionID, task))
	}
	if event.Name == "SessionEnd" {
		state = "ended"
	}
	switch event.Name {
	case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact", "PostCompact", "Stop", "SessionEnd":
	default:
		return errors.New("unsupported hook event")
	}
	token, err := readPrivateToken(tokenPath)
	if err != nil {
		return err
	}
	if err = privatestorage.MkdirAll(filepath.Dir(cursorPath)); err != nil {
		return err
	}
	if err = privatestorage.CheckDir(filepath.Dir(cursorPath)); err != nil {
		return err
	}
	// An exclusive private lock file is portable and refuses concurrent cursor writers.
	// A crashed hook leaves a visible lock; no automatic stale-lock takeover.
	lock := cursorPath + ".lock"
	lockFile, lockErr := privatestorage.Create(lock)
	if errors.Is(lockErr, os.ErrExist) {
		// Hosts run hooks for parallel tool calls concurrently. The holder
		// publishes the heartbeat and hint; this call is a quiet no-op.
		return nil
	}
	if lockErr != nil {
		return fmt.Errorf("cursor locked or unavailable: %w", lockErr)
	}
	lockFile.Close()
	defer os.Remove(lock)
	cursor := hookCursor{Endpoint: endpoint, SessionID: event.SessionID, TaskID: task}
	if f, e := privatestorage.Open(cursorPath); e == nil {
		defer f.Close()
		if err = privatestorage.CheckFile(f); err != nil {
			return err
		}
		if err = transport.Decode(io.LimitReader(f, 4097), &cursor); err != nil {
			return errors.New("invalid hook cursor")
		}
		if cursor.Endpoint != endpoint || cursor.SessionID != event.SessionID || cursor.TaskID != task {
			return errors.New("cursor belongs to a different endpoint/session/task")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	request := func(path string, v, reply any) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		data, e := transport.Request(ctx, endpoint, token, "POST", path, b, nil)
		if e != nil {
			return errors.New("local broker hook request failed; cursor not advanced")
		}
		return json.Unmarshal(data, reply)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	c := coordination.BrokerCommand{SchemaVersion: 1, RequestID: "hook-" + hex.EncodeToString(random[:]), Action: "state", SessionID: event.SessionID, TaskID: task, Agent: agent, State: state, TTLSeconds: 600}
	var receipt coordination.BrokerReceipt
	if err = request("/v1/broker/commands", c, &receipt); err != nil {
		return err
	}
	var feed coordination.BrokerFeed
	if err = request("/v1/broker/pull", coordination.BrokerQuery{SessionID: event.SessionID, TaskID: task, BrokerID: cursor.BrokerID, After: cursor.After}, &feed); err != nil {
		return err
	}
	count := 0
	for _, e := range feed.Events {
		// Every hook publishes a state heartbeat, so counting other sessions'
		// state would hint on nearly every tool call. Hint only on messages and
		// acknowledgments; state remains visible through broker_pull.
		if e.Command.SessionID != event.SessionID && e.Command.Action != "state" {
			count++
		}
	}
	if count > 0 && event.Name != "SessionEnd" {
		// Only a bounded hint enters host context. The agent reads actual messages
		// with its own durable cursor and acknowledges after inspection.
		hint := fmt.Sprintf("Local agent broker has %d new messages or acknowledgments for session %s. Call broker_pull (or llewm-orchestrate broker-pull) using your own saved inbox cursor; hook notifications do not acknowledge messages. Broker %s.", count, event.SessionID, feed.Broker.ID)
		if err = json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event.Name, "additionalContext": hint}}); err != nil {
			return err
		}
	}
	cursor.BrokerID = feed.Broker.ID
	cursor.After = feed.NextAfter
	return saveHookCursor(cursorPath, cursor)
}

// hookCursorName keeps readable session/task IDs and hashes anything else, so a
// caller-supplied identifier can never name a path outside the cursor directory.
func hookCursorName(session, task string) string {
	if task == "" {
		task = "project"
	}
	part := func(s string) string {
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				sum := sha256.Sum256([]byte(s))
				return "h" + hex.EncodeToString(sum[:12])
			}
		}
		if len(s) > 96 {
			sum := sha256.Sum256([]byte(s))
			return "h" + hex.EncodeToString(sum[:12])
		}
		return s
	}
	return part(session) + "." + part(task) + ".cursor"
}

func saveHookCursor(path string, cursor hookCursor) error {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := path + "." + hex.EncodeToString(random[:]) + ".new"
	f, err := privatestorage.Create(name)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	if err = json.NewEncoder(f).Encode(cursor); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
