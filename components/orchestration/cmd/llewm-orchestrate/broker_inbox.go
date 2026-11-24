package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
	"github.com/llewm/llewm/components/privatestorage"
)

const localBrokerDir = ".llewm/orchestration/local"
const localBrokerEndpoint = "http://127.0.0.1:9468"

func loadBrokerCursor(path string, cursor *hookCursor) error {
	f, err := privatestorage.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = privatestorage.CheckFile(f); err != nil {
		return err
	}
	var saved hookCursor
	if err = transport.Decode(io.LimitReader(f, 4097), &saved); err != nil {
		return errors.New("invalid broker cursor")
	}
	if saved.Endpoint != cursor.Endpoint || saved.SessionID != cursor.SessionID || saved.TaskID != cursor.TaskID {
		return errors.New("cursor belongs to a different endpoint/session/task")
	}
	*cursor = saved
	return nil
}

// The hook supplies the effective task and broker identity. Its notification
// offset must never skip messages the agent has not yet inspected.
func brokerInbox(ctx context.Context, dir, endpoint, session string, out io.Writer) error {
	hook := hookCursor{Endpoint: endpoint, SessionID: session}
	if err := loadBrokerCursor(filepath.Join(dir, "hook-cursors", "codex", hookCursorName(session, "")), &hook); err != nil {
		return fmt.Errorf("load Codex project hook cursor: %w", err)
	}
	if hook.BrokerID == "" {
		return errors.New("hook cursor has no broker identity")
	}
	token, err := readPrivateToken(filepath.Join(dir, "engineer.token"))
	if err != nil {
		return err
	}
	inboxDir := filepath.Join(dir, "inbox-cursors", "codex")
	if err = privatestorage.MkdirAll(inboxDir); err != nil {
		return err
	}
	if err = privatestorage.CheckDir(inboxDir); err != nil {
		return err
	}
	// Prefix task IDs to distinguish a task literally named "project".
	scope := ""
	if hook.InboxTaskID != "" {
		scope = "task-" + hook.InboxTaskID
	}
	path := filepath.Join(inboxDir, hookCursorName(session, scope))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock, err := privatestorage.Lock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	cursor := hookCursor{Endpoint: endpoint, SessionID: session, TaskID: hook.InboxTaskID, BrokerID: hook.BrokerID}
	if err = loadBrokerCursor(path, &cursor); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if cursor.BrokerID != hook.BrokerID {
		return errors.New("inbox cursor belongs to a different broker")
	}
	for {
		q := coordination.BrokerQuery{SessionID: session, TaskID: cursor.TaskID, BrokerID: cursor.BrokerID, After: cursor.After}
		body, err := json.Marshal(q)
		if err != nil {
			return err
		}
		data, err := transport.Request(ctx, endpoint, token, "POST", "/v1/broker/pull", body, nil)
		if err != nil {
			return err
		}
		var feed coordination.BrokerFeed
		if err = json.Unmarshal(data, &feed); err != nil {
			return err
		}
		if feed.Broker.ID != cursor.BrokerID || feed.NextAfter < cursor.After || (len(feed.Events) > 0 && feed.NextAfter == cursor.After) {
			return errors.New("invalid broker inbox response")
		}
		for _, event := range feed.Events {
			if event.Command.Action != "state" && event.Command.SessionID != session {
				if err = json.NewEncoder(out).Encode(event); err != nil {
					return err
				}
			}
		}
		if feed.NextAfter == cursor.After {
			return nil
		}
		cursor.After = feed.NextAfter
		if err = saveHookCursor(path, cursor); err != nil {
			return err
		}
		if len(feed.Events) < 100 {
			return nil
		}
	}
}
