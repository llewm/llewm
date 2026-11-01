package memsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// defaultCloneStoragePaths is stable across interruption and contains no
// credential values. The original and all recovery metadata remain siblings;
// an existing unowned destination must never be overwritten to restart work.
func defaultCloneStoragePaths(clone string) (cloneStoragePaths, error) {
	absolute, err := filepath.Abs(clone)
	if err != nil {
		return cloneStoragePaths{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return cloneStoragePaths{}, err
	}
	root := filepath.Join(parent, filepath.Base(absolute))
	id := recordsync.Hash(root)
	return (cloneStoragePaths{
		Root: root, Stage: filepath.Join(parent, ".llewm-bare-stage-"+id),
		Retained: filepath.Join(parent, ".llewm-working-retained-"+id),
		Receipt:  filepath.Join(parent, ".llewm-bare-storage-"+id+".json"),
	}).normalized()
}

func checkStandaloneBareStore(root *os.Root) error {
	for _, name := range []string{"commondir", "shallow", "info/grafts", "objects/info/alternates", "objects/info/http-alternates"} {
		if _, err := root.Lstat(filepath.FromSlash(name)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("bare history storage contains an unsupported dependency or ancestry override: %s", name)
		}
	}
	return nil
}

// openBareRepository gates normal use on a completed migration, when the clone
// has a migration owner. It never converts storage or infers a missing source
// to be empty. Native bare clones have no cutover receipt or owner marker.
func openBareRepository(ctx context.Context, clone, remote string, access Access) (git, error) {
	var empty git
	p, err := defaultCloneStoragePaths(clone)
	if err != nil {
		return empty, err
	}
	root, err := recordsync.OpenRoot(p.Root)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	// Check the selected directory itself before invoking Git, preventing
	// repository discovery from walking into an unrelated parent repository.
	if _, err := recordsync.ReadFile(root, "HEAD", 4096); err != nil {
		return empty, fmt.Errorf("bare history storage is required; use the supported storage migration after stopping old writers")
	}
	objects, err := root.OpenRoot("objects")
	if err != nil {
		return empty, fmt.Errorf("bare history object store is missing: %w", err)
	}
	objects.Close()
	if err := checkStandaloneBareStore(root); err != nil {
		return empty, err
	}
	repo := access.git(p.Root)
	repo.Durable = true
	bare, err := repo.run(ctx, "rev-parse", "--is-bare-repository")
	if err != nil || bare != "true" {
		return empty, fmt.Errorf("selected memory repository is not bare")
	}
	gitDir, err := readGitBytes(ctx, repo, 32<<10, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return empty, err
	}
	actual, actualErr := os.Stat(string(bytes.TrimSuffix(gitDir, []byte{'\n'})))
	selected, selectedErr := os.Stat(p.Root)
	if actualErr != nil || selectedErr != nil || !os.SameFile(actual, selected) {
		return empty, fmt.Errorf("Git resolved a different memory object store")
	}
	if err := verifyRemote(ctx, repo, remote); err != nil {
		return empty, err
	}
	if err := durableGitAvailable(ctx, repo); err != nil {
		return empty, err
	}
	r, found, err := readCloneReceipt(p, remote)
	if err != nil {
		return empty, err
	}
	_, ownerErr := recordsync.ReadFile(root, bareStorageOwner, 4096)
	if errors.Is(ownerErr, os.ErrNotExist) && !found {
		return repo, nil
	}
	if ownerErr != nil || !found {
		return empty, fmt.Errorf("bare history storage has incomplete or damaged cutover ownership")
	}
	complete, err := completedCloneStorage(ctx, p, r, remote, access)
	if err != nil {
		return empty, err
	}
	if !complete {
		return empty, fmt.Errorf("bare history storage cutover is incomplete; resume the supported migration")
	}
	return repo, nil
}
