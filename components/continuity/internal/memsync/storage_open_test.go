package memsync

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBareStorageOpenRequiresCompletedCutover(t *testing.T) {
	ctx, old, remote, access, _ := cloneStorageFixture(t)
	p, err := defaultCloneStoragePaths(old.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openBareRepository(ctx, p.Root, remote, access); err == nil {
		t.Fatal("normal opening accepted working-tree storage")
	}
	if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Root, p.Retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Stage, p.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := openBareRepository(ctx, p.Root, remote, access); err == nil {
		t.Fatal("normal opening accepted switched but incomplete storage")
	}
	if err := completeBareClone(ctx, p, remote, access); err != nil {
		t.Fatal(err)
	}
	if _, err := openBareRepository(ctx, p.Root, remote, access); err != nil {
		t.Fatalf("completed cutover refused: %v", err)
	}
	for _, name := range []string{filepath.Join(p.Root, bareStorageOwner), p.Receipt, p.Receipt + ".done"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if _, err := openBareRepository(ctx, p.Root, remote, access); err == nil {
			t.Fatalf("normal opening accepted missing %s", filepath.Base(name))
		}
		if err := os.WriteFile(name, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBareStorageOpenNativeAndParentDiscovery(t *testing.T) {
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	remote := bareRemote(t)
	if _, err := transport.repo.run(ctx, "remote", "add", "origin", remote); err != nil {
		t.Fatal(err)
	}
	if _, err := openBareRepository(ctx, transport.repo.Dir, remote, Access{NoPrompt: true}); err != nil {
		t.Fatalf("native bare clone refused: %v", err)
	}
	child := filepath.Join(transport.repo.Dir, "unrelated-child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := openBareRepository(ctx, child, remote, Access{NoPrompt: true}); err == nil {
		t.Fatal("opening discovered a parent repository")
	}
	p, err := defaultCloneStoragePaths(transport.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Receipt, []byte("damaged preparation"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openBareRepository(ctx, transport.repo.Dir, remote, Access{NoPrompt: true}); err == nil {
		t.Fatal("native opening inferred away a damaged cutover")
	}
}

func TestNativeBareCloneDoesNotShareObjectFilesWithLocalRemote(t *testing.T) {
	remote := bareRemote(t)
	source := newMachine(t, "native-source", remote)
	source.decide(t, "retained", "Independent durable objects")
	source.sync(t, OutcomeSynced)
	peer := newMachine(t, "native-copy", remote)
	compared := 0
	err := filepath.WalkDir(filepath.Join(remote, "objects"), func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(remote, name)
		if err != nil {
			return err
		}
		original, err := os.Stat(name)
		if err != nil {
			return err
		}
		copy, err := os.Stat(filepath.Join(peer.syncer.Clone, relative))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		compared++
		if os.SameFile(original, copy) {
			t.Fatalf("native clone shares object inode with local remote: %s", relative)
		}
		return nil
	})
	if err != nil || compared == 0 {
		t.Fatalf("object independence qualification: %d %v", compared, err)
	}
}

func TestBareStorageOpenPreservesWhitespaceInPhysicalPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support this Unix path spelling")
	}
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	remote := bareRemote(t)
	if _, err := transport.repo.run(ctx, "remote", "add", "origin", remote); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(filepath.Dir(transport.repo.Dir), "clone with trailing whitespace \n")
	if err := os.Rename(transport.repo.Dir, name); err != nil {
		t.Fatal(err)
	}
	if _, err := openBareRepository(ctx, name, remote, Access{NoPrompt: true}); err != nil {
		t.Fatalf("physical path bytes were normalized: %v", err)
	}
}
