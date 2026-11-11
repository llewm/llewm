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
	Endpoint    string `json:"endpoint"`
	SessionID   string `json:"session_id"`
	TaskID      string `json:"task_id"`
	InboxTaskID string `json:"inbox_task_id,omitempty"`
	BrokerID    string `json:"broker_id"`
	After       int64  `json:"after"`
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
	// All hooks, including SessionEnd, serialize within the request budget.
	// The OS releases the lock on exit/crash; never unlink its shared inode.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	lockFile, err := privatestorage.Lock(ctx, cursorPath+".lock")
	if err != nil {
		return fmt.Errorf("cursor lock unavailable: %w", err)
	}
	defer lockFile.Close()
	cursor := hookCursor{Endpoint: endpoint, SessionID: event.SessionID, TaskID: task}
	if f, e := privatestorage.Open(cursorPath); e == nil {
		if err = privatestorage.CheckFile(f); err == nil {
			err = transport.Decode(io.LimitReader(f, 4097), &cursor)
		}
		f.Close()
		if err != nil {
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
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	c := coordination.BrokerCommand{SchemaVersion: 1, RequestID: "hook-" + hex.EncodeToString(random[:]), Action: "state", SessionID: event.SessionID, TaskID: task, KeepTask: task == "", Resume: event.Name == "SessionStart" && state == "active", Agent: agent, State: state, TTLSeconds: 600}
	var receipt coordination.BrokerReceipt
	if err = request("/v1/broker/commands", c, &receipt); err != nil {
		return err
	}
	// SessionEnd emits no hint. Leave unread notification history for a resume.
	if event.Name == "SessionEnd" {
		return nil
	}
	// A static hook follows the task resolved atomically by the state update.
	// Never reuse a cursor across task filters: offline task messages may be older.
	if cursor.InboxTaskID != receipt.TaskID {
		cursor.After = 0
	}
	cursor.InboxTaskID = receipt.TaskID
	var feed coordination.BrokerFeed
	if err = request("/v1/broker/pull", coordination.BrokerQuery{SessionID: event.SessionID, TaskID: receipt.TaskID, BrokerID: cursor.BrokerID, After: cursor.After}, &feed); err != nil {
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
	if count > 0 {
		// Only a bounded hint enters host context. The agent reads actual messages
		// with its own durable cursor and acknowledges after inspection.
		sessionLabel := event.SessionID
		if receipt.TaskID != "" {
			sessionLabel += fmt.Sprintf(" (task %s)", receipt.TaskID)
		}
		hint := fmt.Sprintf("Local agent broker has %d new messages or acknowledgments for session %s. Call broker_pull (or llewm-orchestrate broker-pull) using your own saved inbox cursor; hook notifications do not acknowledge messages. Broker %s.", count, sessionLabel, feed.Broker.ID)
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
