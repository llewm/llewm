package memsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

const bareStorageReady = "continuity-storage-ready"

func readCloneReceipt(p cloneStoragePaths, remote string) (cloneStorageReceipt, bool, error) {
	root, err := recordsync.OpenRoot(filepath.Dir(p.Root))
	if err != nil {
		return cloneStorageReceipt{}, false, err
	}
	defer root.Close()
	body, err := recordsync.ReadFile(root, filepath.Base(p.Receipt), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return cloneStorageReceipt{}, false, nil
	}
	if err != nil {
		return cloneStorageReceipt{}, false, err
	}
	r, err := decodeCloneStorage(body, p, remote)
	return r, err == nil, err
}

func cloneMigrationLock(p cloneStoragePaths) (*os.File, error) {
	file, err := tryLockFile(filepath.Join(filepath.Dir(p.Root), ".llewm-storage-migrate-"+recordsync.Hash(p.Root)+".lock"))
	if err != nil {
		return nil, fmt.Errorf("another storage operation is active: %w", err)
	}
	return file, nil
}

func workingCloneAt(path string) bool {
	root, err := recordsync.OpenRoot(path)
	if err != nil {
		return false
	}
	defer root.Close()
	gitRoot, err := root.OpenRoot(".git")
	if err != nil {
		return false
	}
	gitRoot.Close()
	return true
}

func originalClonePath(p cloneStoragePaths) (string, error) {
	if workingCloneAt(p.Root) {
		if _, err := os.Lstat(p.Retained); !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("retained clone destination already exists or cannot be inspected")
		}
		return p.Root, nil
	}
	if workingCloneAt(p.Retained) {
		return p.Retained, nil
	}
	return "", fmt.Errorf("original working clone is unavailable; do not initialize replacement history")
}

func cloneReceiptAtRoot(root *os.Root, name string, expected []byte) error {
	actual, err := recordsync.ReadFile(root, name, len(expected))
	if err != nil || !bytes.Equal(actual, expected) {
		return fmt.Errorf("bare storage ownership or completion marker is missing or changed")
	}
	return nil
}

func completedCloneStorage(ctx context.Context, p cloneStoragePaths, r cloneStorageReceipt, remote string, access Access) (bool, error) {
	parent, err := recordsync.OpenRoot(filepath.Dir(p.Root))
	if err != nil {
		return false, err
	}
	defer parent.Close()
	marker, err := recordsync.ReadFile(parent, filepath.Base(p.Receipt)+".done", len(r.done()))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !bytes.Equal(marker, r.done()) {
		return false, fmt.Errorf("bare storage completion receipt differs")
	}
	root, err := recordsync.OpenRoot(p.Root)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if err := checkStandaloneBareStore(root); err != nil {
		return false, err
	}
	if err := cloneReceiptAtRoot(root, bareStorageOwner, r.sealed()); err != nil {
		return false, err
	}
	repo := access.git(p.Root)
	bare, err := repo.run(ctx, "rev-parse", "--is-bare-repository")
	if err != nil || bare != "true" {
		return false, fmt.Errorf("completed memory clone is no longer bare")
	}
	if err := verifyRemote(ctx, repo, remote); err != nil {
		return false, err
	}
	return true, nil
}

// prepareBareClone requires stopped writers. It takes nonblocking migration and
// legacy sync locks, retains the source unchanged and resumes an owned stage by
// init/fetch. It never deletes a partial stage or copies credential values into
// receipts. Configuration values remain in the private Git config only.
func prepareBareClone(ctx context.Context, paths cloneStoragePaths, remote string, access Access) (cloneStorageReceipt, error) {
	var receipt cloneStorageReceipt
	p, err := paths.normalized()
	if err != nil {
		return receipt, err
	}
	access.NoPrompt = true
	lock, err := cloneMigrationLock(p)
	if err != nil {
		return receipt, err
	}
	defer lock.Close()
	receipt, found, err := readCloneReceipt(p, remote)
	if err != nil {
		return receipt, err
	}
	if found {
		if complete, err := completedCloneStorage(ctx, p, receipt, remote, access); err != nil || complete {
			return receipt, err
		}
	}
	original, err := originalClonePath(p)
	if err != nil {
		return receipt, err
	}
	legacyLock, err := tryLockFile(filepath.Join(original, ".git", "continuity-sync.lock"))
	if err != nil {
		return receipt, fmt.Errorf("a legacy sync writer is still active: %w", err)
	}
	defer legacyLock.Close()
	sourceRepo := access.git(original)
	if original == p.Retained {
		sourceRepo.WorkTree = original
	}
	if err := durableGitAvailable(ctx, sourceRepo); err != nil {
		return receipt, err
	}
	source, err := snapshotClone(ctx, sourceRepo, remote, true)
	if err != nil {
		return receipt, err
	}
	if err := fsckClone(ctx, sourceRepo); err != nil {
		return receipt, err
	}
	if err := verifyLocalCloneSource(ctx, sourceRepo, original); err != nil {
		return receipt, err
	}
	parent, err := recordsync.OpenRoot(filepath.Dir(p.Root))
	if err != nil {
		return receipt, err
	}
	defer parent.Close()
	if !found {
		for _, path := range []string{p.Stage, p.Retained} {
			if _, err := parent.Lstat(filepath.Base(path)); !errors.Is(err, os.ErrNotExist) {
				return receipt, fmt.Errorf("clone migration destination already exists or cannot be inspected")
			}
		}
		receipt = receiptForClone(p, remote, source)
		if _, err := recordsync.WriteFile(parent, filepath.Base(p.Receipt), receipt.sealed(), false); err != nil {
			return receipt, err
		}
	} else if !sourceMatchesReceipt(source, receipt) {
		return receipt, fmt.Errorf("original clone changed after storage preparation")
	}
	// A crash after switching the stage but before its completion marker needs
	// only completion, never another stage or another history clone.
	if original == p.Retained {
		if _, err := parent.Lstat(filepath.Base(p.Root)); err == nil {
			return receipt, validateBareCandidate(ctx, p.Root, receipt, source, remote, access)
		} else if !errors.Is(err, os.ErrNotExist) {
			return receipt, err
		}
	}
	if err := ensureCloneStageOwner(parent, p, receipt); err != nil {
		return receipt, err
	}
	stageRepo := access.git(p.Stage)
	stageRepo.Durable = true
	if err := writeBarePrivateConfig(ctx, stageRepo, source.Config); err != nil {
		return receipt, err
	}
	if _, err := stageRepo.run(ctx, "init", "--quiet", "--bare", "--template=", "--object-format="+source.ObjectFormat, "."); err != nil {
		return receipt, err
	}
	if err := verifyLocalCloneSource(ctx, stageRepo, original); err != nil {
		return receipt, err
	}
	if _, err := stageRepo.run(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", original, "+refs/*:refs/*"); err != nil {
		return receipt, err
	}
	for _, ref := range source.Refs {
		if ref.Symbolic != "" {
			if _, err := stageRepo.run(ctx, "symbolic-ref", ref.Name, ref.Symbolic); err != nil {
				return receipt, err
			}
		}
	}
	if _, err := stageRepo.run(ctx, "symbolic-ref", "HEAD", source.Head); err != nil {
		return receipt, err
	}
	stage, err := recordsync.OpenRoot(p.Stage)
	if err != nil {
		return receipt, err
	}
	defer stage.Close()
	if err := writeBarePrivateConfig(ctx, stageRepo, source.Config); err != nil {
		return receipt, err
	}
	if err := validateBareSnapshot(ctx, p.Stage, receipt, source, remote, access); err != nil {
		return receipt, err
	}
	after, err := snapshotClone(ctx, sourceRepo, remote, true)
	if err != nil || !sourceMatchesReceipt(after, receipt) {
		return receipt, fmt.Errorf("original clone changed while preparing bare storage")
	}
	if _, err := recordsync.WriteFile(stage, bareStorageReady, receipt.done(), false); err != nil {
		return receipt, err
	}
	return receipt, syncCloneParent(parent)
}

func verifyLocalCloneSource(ctx context.Context, repo git, path string) error {
	resolved, err := readGitBytes(ctx, repo, 32<<10, "ls-remote", "--get-url", path)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSuffix(resolved, []byte{'\n'}), []byte(path)) {
		return fmt.Errorf("local clone source is redirected by Git configuration; refusing storage migration")
	}
	return nil
}

func writeBarePrivateConfig(ctx context.Context, repo git, source []byte) error {
	root, err := recordsync.OpenRoot(repo.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := recordsync.WriteFile(root, "config", source, true); err != nil {
		return err
	}
	// Target the private file explicitly, including before HEAD/objects exist
	// and after a crash left core.bare=false or an old absolute worktree route.
	path := filepath.Join(repo.Dir, "config")
	if _, err := repo.run(ctx, "config", "--file", path, "--", "core.bare", "true"); err != nil {
		return err
	}
	if _, err := repo.run(ctx, "config", "--file", path, "--unset-all", "core.worktree"); err != nil && exitCode(err) != 5 {
		return err
	}
	config, err := recordsync.ReadFile(root, "config", maxPrivateGitConfig)
	if err != nil {
		return err
	}
	_, err = recordsync.WriteFile(root, "config", config, true)
	return err
}

func ensureCloneStageOwner(parent *os.Root, p cloneStoragePaths, r cloneStorageReceipt) error {
	if err := parent.Mkdir(filepath.Base(p.Stage), 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	stage, err := parent.OpenRoot(filepath.Base(p.Stage))
	if err != nil {
		return err
	}
	defer stage.Close()
	if body, err := recordsync.ReadFile(stage, bareStorageOwner, len(r.sealed())); err == nil {
		if !bytes.Equal(body, r.sealed()) {
			return fmt.Errorf("staged clone belongs to another preparation")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The only pre-owner files this algorithm creates are atomic-write scratch
	// files. Keep them; never delete ambiguous data to make a stage look empty.
	directory, err := stage.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(65)
	directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if len(entries) > 64 {
		return fmt.Errorf("unowned staged clone contains excessive entries")
	}
	for _, entry := range entries {
		if !validStorageTemporary(entry.Name()) {
			return fmt.Errorf("unowned staged clone contains unrelated data")
		}
		body, err := recordsync.ReadFile(stage, entry.Name(), len(r.sealed()))
		if err != nil || !bytes.HasPrefix(r.sealed(), body) {
			return fmt.Errorf("unowned staged clone contains unrelated data")
		}
	}
	if _, err = recordsync.WriteFile(stage, bareStorageOwner, r.sealed(), false); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := stage.Remove(entry.Name()); err != nil {
			return err
		}
	}
	return syncCloneParent(stage)
}

func validStorageTemporary(name string) bool {
	const prefix = ".llewm-stage-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	for _, c := range strings.TrimPrefix(name, prefix) {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func fsckClone(ctx context.Context, repo git) error {
	_, err := readGitBytes(ctx, repo, maxCachedTreeBytes, "fsck", "--full", "--strict", "--no-reflogs", "--no-dangling")
	return err
}

func validateBareSnapshot(ctx context.Context, path string, r cloneStorageReceipt, source cloneSnapshot, remote string, access Access) error {
	root, err := recordsync.OpenRoot(path)
	if err != nil {
		return err
	}
	err = checkStandaloneBareStore(root)
	root.Close()
	if err != nil {
		return err
	}
	repo := access.git(path)
	staged, err := snapshotClone(ctx, repo, remote, false)
	if err != nil {
		return err
	}
	if staged.refsHash() != r.RefsSHA256 || len(staged.Refs) != r.RefCount || staged.ObjectFormat != r.ObjectFormat || !equivalentPrivateConfig(source.Settings, staged.Settings) {
		return fmt.Errorf("staged bare clone differs from original refs or private configuration")
	}
	return fsckClone(ctx, repo)
}

func validateBareCandidate(ctx context.Context, path string, r cloneStorageReceipt, source cloneSnapshot, remote string, access Access) error {
	root, err := recordsync.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := cloneReceiptAtRoot(root, bareStorageOwner, r.sealed()); err != nil {
		return err
	}
	if err := cloneReceiptAtRoot(root, bareStorageReady, r.done()); err != nil {
		return err
	}
	return validateBareSnapshot(ctx, path, r, source, remote, access)
}

// completeBareClone resumes retirement/switch/completion boundaries. An exact
// prepared stage is required before the source is renamed. Neither originals,
// conflicting paths nor partial stages are overwritten or deleted.
func completeBareClone(ctx context.Context, paths cloneStoragePaths, remote string, access Access) error {
	p, err := paths.normalized()
	if err != nil {
		return err
	}
	access.NoPrompt = true
	lock, err := cloneMigrationLock(p)
	if err != nil {
		return err
	}
	defer lock.Close()
	r, found, err := readCloneReceipt(p, remote)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("verified bare storage preparation required")
	}
	if complete, err := completedCloneStorage(ctx, p, r, remote, access); err != nil || complete {
		return err
	}
	original, err := originalClonePath(p)
	if err != nil {
		return err
	}
	legacyLock, err := tryLockFile(filepath.Join(original, ".git", "continuity-sync.lock"))
	if err != nil {
		return fmt.Errorf("a legacy sync writer is still active: %w", err)
	}
	defer legacyLock.Close()
	sourceRepo := access.git(original)
	if original == p.Retained {
		sourceRepo.WorkTree = original
	}
	source, err := snapshotClone(ctx, sourceRepo, remote, true)
	if err != nil {
		return err
	}
	if !sourceMatchesReceipt(source, r) {
		return fmt.Errorf("original clone changed after preparation")
	}
	parent, err := recordsync.OpenRoot(filepath.Dir(p.Root))
	if err != nil {
		return err
	}
	defer parent.Close()
	rootExists := false
	if _, err := parent.Lstat(filepath.Base(p.Root)); err == nil {
		rootExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if original == p.Root || !rootExists {
		if err := validateBareCandidate(ctx, p.Stage, r, source, remote, access); err != nil {
			return err
		}
		if original == p.Root {
			if err := retireOriginalClone(ctx, parent, p, legacyLock, r, remote, access); err != nil {
				return err
			}
			if err := syncCloneParent(parent); err != nil {
				return err
			}
		}
		if err := parent.Rename(filepath.Base(p.Stage), filepath.Base(p.Root)); err != nil {
			return err
		}
		if err := syncCloneParent(parent); err != nil {
			return err
		}
	} else {
		if _, err := parent.Lstat(filepath.Base(p.Stage)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clone cutover has conflicting source and stage paths")
		}
	}
	if err := validateBareCandidate(ctx, p.Root, r, source, remote, access); err != nil {
		return err
	}
	_, err = recordsync.WriteFile(parent, filepath.Base(p.Receipt)+".done", r.done(), false)
	return err
}

// retireOriginalClone renames the original working clone to its retained path.
// Where the platform cannot rename a directory holding an open file (Windows),
// the legacy lock is released first: the rename itself then fails while any
// legacy writer holds a file inside the clone, and the retained clone is
// verified against the preparation receipt afterwards and moved back if it
// changed in between.
func retireOriginalClone(ctx context.Context, parent *os.Root, p cloneStoragePaths, legacyLock *os.File, r cloneStorageReceipt, remote string, access Access) error {
	released := !parentRenameKeepsOpenFiles
	if released {
		if err := legacyLock.Close(); err != nil {
			return err
		}
	}
	rootName, retainedName := filepath.Base(p.Root), filepath.Base(p.Retained)
	if err := parent.Rename(rootName, retainedName); err != nil {
		if released {
			return fmt.Errorf("retire original clone (a legacy sync writer may still be active): %w", err)
		}
		return err
	}
	if !released {
		return nil
	}
	retainedRepo := access.git(p.Retained)
	retainedRepo.WorkTree = p.Retained
	retained, err := snapshotClone(ctx, retainedRepo, remote, true)
	if err == nil && sourceMatchesReceipt(retained, r) {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("original clone changed while it was retired")
	}
	if restoreErr := parent.Rename(retainedName, rootName); restoreErr != nil {
		return fmt.Errorf("%w; restoring the original clone path also failed: %v", err, restoreErr)
	}
	return err
}
