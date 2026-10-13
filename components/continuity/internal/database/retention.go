package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type BackupRetentionOptions struct {
	Dir      string
	KeepLast int
	KeepDays int
	Pinned   []string
	Now      time.Time
}

type BackupRetentionEntry struct {
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
	Action     string    `json:"action"`
	Reason     string    `json:"reason"`
}

type BackupRetentionPlan struct {
	Directory       string                 `json:"directory"`
	KeepLast        int                    `json:"keep_last"`
	KeepDays        int                    `json:"keep_days"`
	Pinned          []string               `json:"pinned"`
	Entries         []BackupRetentionEntry `json:"entries"`
	PruneCandidates []string               `json:"prune_candidates"`
	ReclaimBytes    int64                  `json:"reclaim_bytes"`
	DryRun          bool                   `json:"dry_run"`
}

type BackupPruneReceipt struct {
	Directory      string              `json:"directory"`
	AppliedAt      time.Time           `json:"applied_at"`
	Plan           BackupRetentionPlan `json:"plan"`
	Removed        []string            `json:"removed"`
	ReclaimedBytes int64               `json:"reclaimed_bytes"`
	Applied        bool                `json:"applied"`
}

// PlanBackupRetention inventories backup files and reports what an explicit
// prune would remove. It never deletes files.
func PlanBackupRetention(options BackupRetentionOptions) (BackupRetentionPlan, error) {
	plan := BackupRetentionPlan{
		Directory: options.Dir,
		KeepLast:  options.KeepLast,
		KeepDays:  options.KeepDays,
		Pinned:    append([]string(nil), options.Pinned...),
		Entries:   []BackupRetentionEntry{},
		DryRun:    true,
	}
	if strings.TrimSpace(options.Dir) == "" {
		return plan, errors.New("backup directory is required")
	}
	if options.KeepLast < 0 || options.KeepDays < 0 {
		return plan, errors.New("backup retention values cannot be negative")
	}
	absDir, err := filepath.Abs(options.Dir)
	if err != nil {
		return plan, err
	}
	plan.Directory = absDir
	entries, err := os.ReadDir(absDir)
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	type candidate struct {
		path    string
		name    string
		size    int64
		modTime time.Time
		pinned  bool
	}
	managed := []candidate{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		path := filepath.Join(absDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return plan, fmt.Errorf("inspect backup %s: %w", path, err)
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			plan.Entries = append(plan.Entries, BackupRetentionEntry{Path: path, SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC(), Action: "keep", Reason: "non_regular"})
			continue
		}
		if !strings.HasPrefix(entry.Name(), "continuity-") {
			plan.Entries = append(plan.Entries, BackupRetentionEntry{Path: path, SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC(), Action: "keep", Reason: "manual_name"})
			continue
		}
		managed = append(managed, candidate{path: path, name: entry.Name(), size: info.Size(), modTime: info.ModTime().UTC(), pinned: backupPinned(absDir, path, entry.Name(), options.Pinned)})
	}
	slices.SortFunc(managed, func(a, b candidate) int {
		if order := b.modTime.Compare(a.modTime); order != 0 {
			return order
		}
		return strings.Compare(a.path, b.path)
	})
	cutoff := now.UTC().Add(-time.Duration(options.KeepDays) * 24 * time.Hour)
	unpinnedRank := 0
	for _, backup := range managed {
		action, reason := "prune", "expired"
		switch {
		case backup.pinned:
			action, reason = "keep", "pinned"
		default:
			unpinnedRank++
			if options.KeepLast > 0 && unpinnedRank <= options.KeepLast {
				action, reason = "keep", "keep_last"
			} else if options.KeepDays > 0 && !backup.modTime.Before(cutoff) {
				action, reason = "keep", "keep_days"
			}
		}
		plan.Entries = append(plan.Entries, BackupRetentionEntry{Path: backup.path, SizeBytes: backup.size, ModifiedAt: backup.modTime, Action: action, Reason: reason})
		if action == "prune" {
			plan.PruneCandidates = append(plan.PruneCandidates, backup.path)
			plan.ReclaimBytes += backup.size
		}
	}
	return plan, nil
}

// ApplyBackupRetention removes the exact candidates in a previously computed
// plan after revalidating containment, type, size, and modification time. The
// returned receipt includes any removals completed before an error.
func ApplyBackupRetention(plan BackupRetentionPlan, now time.Time) (BackupPruneReceipt, error) {
	receipt := BackupPruneReceipt{Directory: plan.Directory, Plan: plan, Removed: []string{}}
	if !plan.DryRun {
		return receipt, errors.New("backup prune requires a dry-run plan")
	}
	absDir, err := filepath.Abs(plan.Directory)
	if err != nil {
		return receipt, err
	}
	receipt.Directory = absDir
	type verifiedCandidate struct {
		entry BackupRetentionEntry
		path  string
	}
	verified := make([]verifiedCandidate, 0, len(plan.PruneCandidates))
	for _, candidate := range plan.PruneCandidates {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			return receipt, err
		}
		rel, err := filepath.Rel(absDir, absPath)
		if err != nil || !filepath.IsLocal(rel) || rel == "." {
			return receipt, fmt.Errorf("prune candidate escapes backup directory: %s", candidate)
		}
		entry, ok := retentionEntry(plan.Entries, absPath)
		if !ok || entry.Action != "prune" {
			return receipt, fmt.Errorf("prune candidate is absent from the reviewed plan: %s", candidate)
		}
		if err := validatePruneCandidate(absPath, entry); err != nil {
			return receipt, err
		}
		verified = append(verified, verifiedCandidate{entry: entry, path: absPath})
	}
	if now.IsZero() {
		now = time.Now()
	}
	receipt.AppliedAt = now.UTC()
	for _, candidate := range verified {
		if err := validatePruneCandidate(candidate.path, candidate.entry); err != nil {
			return receipt, err
		}
		if err := os.Remove(candidate.path); err != nil {
			return receipt, fmt.Errorf("remove backup %s: %w", candidate.path, err)
		}
		receipt.Removed = append(receipt.Removed, candidate.path)
		receipt.ReclaimedBytes += candidate.entry.SizeBytes
	}
	receipt.Applied = true
	return receipt, nil
}

func retentionEntry(entries []BackupRetentionEntry, path string) (BackupRetentionEntry, bool) {
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == filepath.Clean(path) {
			return entry, true
		}
	}
	return BackupRetentionEntry{}, false
}

func validatePruneCandidate(path string, expected BackupRetentionEntry) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("revalidate backup %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("backup changed type after planning: %s", path)
	}
	if info.Size() != expected.SizeBytes || !info.ModTime().UTC().Equal(expected.ModifiedAt.UTC()) {
		return fmt.Errorf("backup changed after planning: %s", path)
	}
	return nil
}

func backupPinned(dir, path, name string, pins []string) bool {
	for _, pin := range pins {
		pin = filepath.Clean(strings.TrimSpace(pin))
		if pin == "." {
			continue
		}
		if pin == name || filepath.Clean(pin) == filepath.Clean(path) || (!filepath.IsAbs(pin) && filepath.Join(dir, pin) == filepath.Clean(path)) {
			return true
		}
	}
	return false
}
