package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	RecoverySchemaVersion = 1
	maxRecoveryBytes      = 64 << 10
)

// RecoveryEnvelope is a device-local, bounded checkpoint write-ahead record.
// It exists only to carry one refused checkpoint across a required restart.
type RecoveryEnvelope struct {
	SchemaVersion   int                  `json:"schema_version"`
	ID              string               `json:"id"`
	ProjectID       string               `json:"project_id"`
	SourceWriterAPI int                  `json:"source_writer_api"`
	SourceMCPAPI    int                  `json:"source_mcp_api"`
	CreatedAt       time.Time            `json:"created_at"`
	Candidate       Checkpoint           `json:"candidate"`
	RequestKey      string               `json:"request_key,omitempty"`
	Reconciliation  ActionReconciliation `json:"reconciliation"`
	LeaseClaim      lease.Claim          `json:"lease_claim,omitempty"`
	Digest          string               `json:"digest"`
}

// RecoverySummary is safe metadata for listings and bootstrap notices.
type RecoverySummary struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Task      string    `json:"task"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// RecoveryStore persists checkpoint-only recovery envelopes outside SQLite.
type RecoveryStore struct {
	StateDir  string
	ProjectID string
}

func (s RecoveryStore) directory() string {
	sum := sha256.Sum256([]byte(s.ProjectID))
	return filepath.Join(s.StateDir, "checkpoint-recovery", hex.EncodeToString(sum[:12]))
}

// Stage validates and redacts a refused checkpoint before writing it atomically.
func (s RecoveryStore) Stage(candidate Checkpoint, reconciliation ActionReconciliation, writerAPI, mcpAPI int) (RecoveryEnvelope, error) {
	if strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.StateDir) == "" {
		return RecoveryEnvelope{}, errors.New("checkpoint recovery requires a project and state directory")
	}
	candidate.ProjectID = s.ProjectID
	requestKey := candidate.RequestKey
	claim := candidate.Fence.Claim
	prepared, err := Prepare(candidate)
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	prepared.RequestKey = ""
	prepared.Fence = lease.Fence{}
	prepared.CodeHeads = nil
	prepared.BasedOn = ""
	reconciliation, err = prepareActionReconciliation(reconciliation)
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	claim.Holder = redact.String(strings.TrimSpace(claim.Holder))
	claim.SessionID = redact.String(strings.TrimSpace(claim.SessionID))
	envelope := RecoveryEnvelope{
		SchemaVersion: RecoverySchemaVersion, ID: activity.NewID(), ProjectID: s.ProjectID,
		SourceWriterAPI: writerAPI, SourceMCPAPI: mcpAPI, CreatedAt: time.Now().UTC(),
		Candidate: prepared, RequestKey: strings.TrimSpace(requestKey), Reconciliation: reconciliation, LeaseClaim: claim,
	}
	envelope.Digest, err = recoveryDigest(envelope)
	if err != nil {
		return RecoveryEnvelope{}, err
	}

	values, err := s.List()
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	if len(values) >= 128 {
		return RecoveryEnvelope{}, errors.New("checkpoint recovery holds 128 pending envelopes; apply or discard one before staging another")
	}
	if envelope.RequestKey != "" {
		for _, summary := range values {
			existing, loadErr := s.Get(summary.ID)
			if loadErr != nil {
				return RecoveryEnvelope{}, loadErr
			}
			if existing.RequestKey != envelope.RequestKey {
				continue
			}
			if sameRecoveryRequest(existing, envelope) {
				return existing, nil
			}
			return RecoveryEnvelope{}, fmt.Errorf("checkpoint recovery request key %q already has different staged content", envelope.RequestKey)
		}
	}
	body, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	if len(body)+1 > maxRecoveryBytes {
		return RecoveryEnvelope{}, errors.New("checkpoint recovery envelope exceeds 64 KiB limit")
	}
	dir := s.directory()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return RecoveryEnvelope{}, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return RecoveryEnvelope{}, err
	}
	temporary, err := os.CreateTemp(dir, ".stage-*")
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return RecoveryEnvelope{}, err
	}
	if _, err := temporary.Write(append(body, '\n')); err != nil {
		temporary.Close()
		return RecoveryEnvelope{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return RecoveryEnvelope{}, err
	}
	if err := temporary.Close(); err != nil {
		return RecoveryEnvelope{}, err
	}
	if err := os.Rename(temporaryName, s.path(envelope.ID)); err != nil {
		return RecoveryEnvelope{}, err
	}
	removeTemporary = false
	if err := syncRecoveryDirectory(dir); err != nil {
		return RecoveryEnvelope{}, err
	}
	return envelope, nil
}

func (s RecoveryStore) path(id string) string {
	return filepath.Join(s.directory(), id+".json")
}

func validRecoveryID(id string) bool {
	if len(id) < 8 || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if r != '-' && r != '_' && r != '.' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// Get reads and verifies one envelope without modifying it.
func (s RecoveryStore) Get(id string) (RecoveryEnvelope, error) {
	if !validRecoveryID(id) {
		return RecoveryEnvelope{}, fmt.Errorf("invalid checkpoint recovery ID %q", id)
	}
	file, err := os.Open(s.path(id))
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxRecoveryBytes+1))
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	if len(body) > maxRecoveryBytes {
		return RecoveryEnvelope{}, errors.New("checkpoint recovery envelope exceeds 64 KiB limit")
	}
	var envelope RecoveryEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("decode checkpoint recovery %q: %w", id, err)
	}
	if envelope.SchemaVersion != RecoverySchemaVersion || envelope.ID != id || envelope.ProjectID != s.ProjectID {
		return RecoveryEnvelope{}, fmt.Errorf("checkpoint recovery %q has incompatible identity or schema", id)
	}
	digest, err := recoveryDigest(envelope)
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	if digest != envelope.Digest {
		return RecoveryEnvelope{}, fmt.Errorf("checkpoint recovery %q failed its integrity check", id)
	}
	envelope.Candidate.RequestKey = envelope.RequestKey
	envelope.Candidate.Fence = lease.Fence{Claim: envelope.LeaseClaim}
	return envelope, nil
}

// List returns bounded metadata for every pending envelope.
func (s RecoveryStore) List() ([]RecoverySummary, error) {
	entries, err := os.ReadDir(s.directory())
	if errors.Is(err, os.ErrNotExist) {
		return []RecoverySummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	values := make([]RecoverySummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		envelope, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		values = append(values, RecoverySummary{ID: id, TaskID: envelope.Candidate.TaskID, Task: envelope.Candidate.Task, Status: envelope.Candidate.Status, CreatedAt: envelope.CreatedAt})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values, nil
}

// Remove discards a recovery envelope after a confirmed save or explicit request.
func (s RecoveryStore) Remove(id string) error {
	if !validRecoveryID(id) {
		return fmt.Errorf("invalid checkpoint recovery ID %q", id)
	}
	if err := os.Remove(s.path(id)); err != nil {
		return err
	}
	return syncRecoveryDirectory(s.directory())
}

func syncRecoveryDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func recoveryDigest(envelope RecoveryEnvelope) (string, error) {
	envelope.Digest = ""
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func sameRecoveryRequest(left, right RecoveryEnvelope) bool {
	left.ID, right.ID = "", ""
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	left.Candidate.ID, right.Candidate.ID = "", ""
	left.Candidate.CreatedAt, right.Candidate.CreatedAt = time.Time{}, time.Time{}
	left.Candidate.RequestKey, right.Candidate.RequestKey = "", ""
	left.Candidate.Fence, right.Candidate.Fence = lease.Fence{}, lease.Fence{}
	left.Digest, right.Digest = "", ""
	l, lerr := json.Marshal(left)
	r, rerr := json.Marshal(right)
	return lerr == nil && rerr == nil && string(l) == string(r)
}
