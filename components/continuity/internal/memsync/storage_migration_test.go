package memsync

import (
	"bytes"
	"compress/zlib"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func cloneStorageFixture(t testing.TB) (context.Context, cloneStoragePaths, string, Access, cloneSnapshot) {
	t.Helper()
	ctx := context.Background()
	remote := bareRemote(t)
	m := newMachine(t, "bare-storage", remote)
	m.decide(t, "retained", "Keep immutable history")
	m.sync(t, OutcomeSynced)
	// Simulate the previous storage backend in a disposable working clone.
	// The current Init/Sync route is bare and must never manufacture one.
	legacy := filepath.Join(filepath.Dir(m.syncer.Clone), "legacy-clone")
	if _, err := (git{Dir: filepath.Dir(legacy)}).run(ctx, "clone", "--quiet", remote, legacy); err != nil {
		t.Fatal(err)
	}
	m.syncer.Clone = legacy
	repo := git{Dir: legacy, Identity: []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}}
	before, err := repo.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repo.run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "update-ref", "refs/continuity/leases/fixture/task", before); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "symbolic-ref", "refs/continuity/aliases/inner", "refs/remotes/origin/"+branch); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "symbolic-ref", "refs/continuity/aliases/outer", "refs/continuity/aliases/inner"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "commit", "--quiet", "--allow-empty", "-m", "fixture unpublished history"); err != nil {
		t.Fatal(err)
	}
	if err := configureHelper(ctx, repo, "!fixture-secret-value"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotClone(ctx, repo, remote, true)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(m.syncer.Clone)
	paths := cloneStoragePaths{Root: m.syncer.Clone, Stage: filepath.Join(parent, "bare-stage"), Retained: filepath.Join(parent, "retained-clone"), Receipt: filepath.Join(parent, "storage.json")}
	return ctx, paths, remote, Access{NoPrompt: true}, snapshot
}

func TestBareCloneStorageRetainsUnpublishedRefsSymbolicsAndPrivateConfig(t *testing.T) {
	t.Parallel()
	ctx, paths, remote, access, before := cloneStorageFixture(t)
	receipt, err := prepareBareClone(ctx, paths, remote, access)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(receipt.sealed(), []byte("fixture-secret-value")) {
		t.Fatal("private configuration escaped into receipt")
	}
	still, err := snapshotClone(ctx, access.git(paths.Root), remote, true)
	if err != nil || !sourceMatchesReceipt(still, receipt) {
		t.Fatalf("preparation changed original: %+v %v", still, err)
	}
	if err := completeBareClone(ctx, paths, remote, access); err != nil {
		t.Fatal(err)
	}
	after, err := snapshotClone(ctx, access.git(paths.Root), remote, false)
	if err != nil || after.refsHash() != before.refsHash() || !equivalentPrivateConfig(before.Settings, after.Settings) {
		t.Fatalf("bare conversion lost refs/config: %+v %v", after, err)
	}
	link, err := access.git(paths.Root).run(ctx, "symbolic-ref", "--no-recurse", "refs/continuity/aliases/outer")
	if err != nil || link != "refs/continuity/aliases/inner" {
		t.Fatalf("symbolic alias chain collapsed: %q %v", link, err)
	}
	retained, err := snapshotClone(ctx, access.git(paths.Retained), remote, true)
	if err != nil || !sourceMatchesReceipt(retained, receipt) {
		t.Fatalf("original not retained exactly: %+v %v", retained, err)
	}
	for _, name := range []string{"index", "objects/info/alternates", "v4"} {
		if _, err := os.Lstat(filepath.Join(paths.Root, name)); !os.IsNotExist(err) {
			t.Fatalf("bare clone has worktree/index/alternate dependency: %s %v", name, err)
		}
	}
	if again, err := prepareBareClone(ctx, paths, remote, access); err != nil || again != receipt {
		t.Fatalf("preparation not idempotent: %+v %v", again, err)
	}
	if err := completeBareClone(ctx, paths, remote, access); err != nil {
		t.Fatalf("completion not idempotent: %v", err)
	}
	// A completed migration remains completed after legitimate later commits.
	repo := access.git(paths.Root)
	repo.Durable = true
	repo.Identity = []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}
	old, err := repo.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	next, err := repo.run(ctx, "commit-tree", tree, "-p", old, "-m", "fixture after activation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "update-ref", before.Head, next, old); err != nil {
		t.Fatal(err)
	}
	if err := completeBareClone(ctx, paths, remote, access); err != nil {
		t.Fatalf("later valid history invalidated completion: %v", err)
	}
}

func TestBareCloneStorageResumesNamespaceBoundaries(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"prepared", "retired", "switched"} {
		t.Run(phase, func(t *testing.T) {
			ctx, p, remote, access, before := cloneStorageFixture(t)
			if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
				t.Fatal(err)
			}
			if phase == "retired" || phase == "switched" {
				if err := os.Rename(p.Root, p.Retained); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "switched" {
				if err := os.Rename(p.Stage, p.Root); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
				t.Fatalf("prepare resume: %v", err)
			}
			if err := completeBareClone(ctx, p, remote, access); err != nil {
				t.Fatal(err)
			}
			after, err := snapshotClone(ctx, access.git(p.Root), remote, false)
			if err != nil || after.refsHash() != before.refsHash() {
				t.Fatalf("namespace recovery lost refs: %v", err)
			}
		})
	}
}

func TestBareCloneStorageResumesPrivateConfigRewrite(t *testing.T) {
	t.Parallel()
	ctx, p, remote, access, before := cloneStorageFixture(t)
	if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
		t.Fatal(err)
	}
	stage, err := recordsync.OpenRoot(p.Stage)
	if err != nil {
		t.Fatal(err)
	}
	_, err = recordsync.WriteFile(stage, "config", before.Config, true)
	stage.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := completeBareClone(ctx, p, remote, access); err == nil {
		t.Fatal("incomplete private config activated")
	}
	if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
		t.Fatal(err)
	}
	if err := completeBareClone(ctx, p, remote, access); err != nil {
		t.Fatal(err)
	}
}

func TestBareCloneStorageRetiresExplicitWorktreeWithoutChangingOriginalConfig(t *testing.T) {
	t.Parallel()
	ctx, p, remote, access, _ := cloneStorageFixture(t)
	if _, err := access.git(p.Root).run(ctx, "config", "--local", "core.worktree", p.Root); err != nil {
		t.Fatal(err)
	}
	before, err := snapshotClone(ctx, access.git(p.Root), remote, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
		t.Fatal(err)
	}
	stage, err := recordsync.OpenRoot(p.Stage)
	if err != nil {
		t.Fatal(err)
	}
	_, err = recordsync.WriteFile(stage, "config", before.Config, true)
	stage.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
		t.Fatalf("config rewrite resume: %v", err)
	}
	if err := os.Rename(p.Root, p.Retained); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
		t.Fatalf("retired worktree resume: %v", err)
	}
	if err := completeBareClone(ctx, p, remote, access); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(p.Retained, ".git", "config"))
	if err != nil || !bytes.Equal(config, before.Config) {
		t.Fatalf("retired private config changed: %v", err)
	}
}

func TestCloneStorageLockRemainsHeldAcrossRetirement(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	retained := filepath.Join(parent, "retained")
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := tryLockFile(filepath.Join(source, ".git", "continuity-sync.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = os.Rename(source, retained)
	if !parentRenameKeepsOpenFiles {
		// Windows: a writer holding any file inside the clone fences the
		// rename itself, which retireOriginalClone relies on after releasing
		// its own lock.
		if err == nil {
			t.Fatal("rename succeeded while a file inside the clone was open")
		}
		if _, statErr := os.Stat(filepath.Join(source, ".git")); statErr != nil {
			t.Fatalf("refused retirement changed the source: %v", statErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("held migration lock blocked retirement: %v", err)
	}
	if other, err := tryLockFile(filepath.Join(retained, ".git", "continuity-sync.lock")); err == nil {
		other.Close()
		t.Fatal("retirement released legacy lock")
	}
}

func TestRetireOriginalCloneVerifiesWhereLockCannotBeHeld(t *testing.T) {
	t.Parallel()
	ctx, p, remote, access, source := cloneStorageFixture(t)
	r := receiptForClone(p, remote, source)
	parent, err := recordsync.OpenRoot(filepath.Dir(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	retire := func() error {
		lock, err := tryLockFile(filepath.Join(p.Root, ".git", "continuity-sync.lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		return retireOriginalClone(ctx, parent, p, lock, r, remote, access)
	}
	// A clone that changed after preparation is restored where the lock had
	// to be released; POSIX keeps the lock held, so nothing can change it.
	repo := git{Dir: p.Root, Identity: []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}}
	if _, err := repo.run(ctx, "commit", "--quiet", "--allow-empty", "-m", "written after preparation"); err != nil {
		t.Fatal(err)
	}
	err = retire()
	if parentRenameKeepsOpenFiles {
		if err != nil {
			t.Fatalf("retire with lock held: %v", err)
		}
		if err := os.Rename(p.Retained, p.Root); err != nil {
			t.Fatal(err)
		}
	} else {
		if err == nil || !strings.Contains(err.Error(), "changed while it was retired") {
			t.Fatalf("changed clone was retired: %v", err)
		}
		if !workingCloneAt(p.Root) {
			t.Fatal("changed clone was not restored to its original path")
		}
		if _, err := os.Lstat(p.Retained); !os.IsNotExist(err) {
			t.Fatalf("retained path left behind: %v", err)
		}
	}
	if _, err := repo.run(ctx, "reset", "--quiet", "--hard", "HEAD~1"); err != nil {
		t.Fatal(err)
	}
	if err := retire(); err != nil {
		t.Fatalf("retire unchanged clone: %v", err)
	}
	if !workingCloneAt(p.Retained) || workingCloneAt(p.Root) {
		t.Fatal("unchanged clone was not retired")
	}
}

func TestBareCloneStorageResumesEarlyOwnedStages(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"receipt-only", "empty-stage", "partial-owner", "owner-only", "initialized"} {
		t.Run(phase, func(t *testing.T) {
			ctx, p, remote, access, source := cloneStorageFixture(t)
			var err error
			p, err = p.normalized()
			if err != nil {
				t.Fatal(err)
			}
			r := receiptForClone(p, remote, source)
			parent, err := recordsync.OpenRoot(filepath.Dir(p.Root))
			if err != nil {
				t.Fatal(err)
			}
			_, err = recordsync.WriteFile(parent, filepath.Base(p.Receipt), r.sealed(), false)
			parent.Close()
			if err != nil {
				t.Fatal(err)
			}
			if phase != "receipt-only" {
				if err := os.Mkdir(p.Stage, 0700); err != nil {
					t.Fatal(err)
				}
			}
			temporary := ".llewm-stage-" + strings.Repeat("b", 32)
			if phase == "partial-owner" {
				if err := os.WriteFile(filepath.Join(p.Stage, temporary), r.sealed()[:len(r.sealed())/2], 0600); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "owner-only" || phase == "initialized" {
				stage, err := recordsync.OpenRoot(p.Stage)
				if err != nil {
					t.Fatal(err)
				}
				_, err = recordsync.WriteFile(stage, bareStorageOwner, r.sealed(), false)
				stage.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == "initialized" {
				repo := access.git(p.Stage)
				repo.Durable = true
				if _, err := repo.run(ctx, "init", "--quiet", "--bare", "--object-format="+source.ObjectFormat, "."); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
				t.Fatalf("early stage resume: %v", err)
			}
			if phase == "partial-owner" {
				if _, err := os.Stat(filepath.Join(p.Stage, temporary)); !os.IsNotExist(err) {
					t.Fatalf("owned metadata scratch retained: %v", err)
				}
			}
			if err := completeBareClone(ctx, p, remote, access); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBareCloneStorageRefusesDirtyOrActiveSourceWithoutReceipt(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"dirty", "active", "includes", "extra-push-url", "redirected-source", "dangling-symbolic-ref", "ref-lock"} {
		t.Run(mode, func(t *testing.T) {
			ctx, p, remote, access, before := cloneStorageFixture(t)
			repo := access.git(p.Root)
			switch mode {
			case "dirty":
				if err := os.WriteFile(filepath.Join(p.Root, "pending-work"), []byte("retain me"), 0600); err != nil {
					t.Fatal(err)
				}
			case "active":
				lock, err := lockFile(filepath.Join(p.Root, ".git", "continuity-sync.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "includes":
				if _, err := repo.run(ctx, "config", "--local", "include.path", "missing-private-config"); err != nil {
					t.Fatal(err)
				}
			case "dangling-symbolic-ref":
				if _, err := repo.run(ctx, "symbolic-ref", "refs/continuity/dangling", "refs/heads/missing"); err != nil {
					t.Fatal(err)
				}
			case "ref-lock":
				if err := os.WriteFile(filepath.Join(p.Root, ".git", "refs", "abandoned.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "extra-push-url":
				if _, err := repo.run(ctx, "config", "--local", "--add", "remote.origin.pushurl", remote); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.run(ctx, "config", "--local", "--add", "remote.origin.pushurl", "https://example.invalid/unapproved"); err != nil {
					t.Fatal(err)
				}
			case "redirected-source":
				other := bareRemote(t)
				// Redirect the exact source path the migration verifies; on macOS
				// p.Root reaches it through the /var symlink, which Git would not match.
				source, err := p.normalized()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.run(ctx, "config", "--local", "url."+other+".insteadOf", source.Root); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := prepareBareClone(ctx, p, remote, access); err == nil {
				t.Fatal("unsafe source accepted")
			}
			if _, err := os.Lstat(p.Receipt); !os.IsNotExist(err) {
				t.Fatalf("unsafe source persisted preparation: %v", err)
			}
			if _, err := os.Lstat(p.Stage); !os.IsNotExist(err) {
				t.Fatalf("unsafe source created a stage: %v", err)
			}
			refs, err := readGitBytes(ctx, repo, maxCachedTreeBytes, "for-each-ref", "--format=%(objectname)")
			if err != nil || len(refs) == 0 || before.refsHash() == "" {
				t.Fatalf("source history unavailable after refusal: %v", err)
			}
		})
	}
}

func TestBareCloneStorageRefusesChangedRecoveryInputs(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source-config", "stage-config", "stage-owner", "stage-ref", "stage-object", "stage-alternates", "receipt", "competing-root"} {
		t.Run(mode, func(t *testing.T) {
			ctx, p, remote, access, _ := cloneStorageFixture(t)
			if _, err := prepareBareClone(ctx, p, remote, access); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source-config":
				if _, err := access.git(p.Root).run(ctx, "config", "--local", "--replace-all", "credential.helper", "changed private setting"); err != nil {
					t.Fatal(err)
				}
			case "stage-config":
				if _, err := access.git(p.Stage).run(ctx, "config", "--local", "--replace-all", "credential.helper", "changed private setting"); err != nil {
					t.Fatal(err)
				}
			case "stage-owner":
				if err := os.WriteFile(filepath.Join(p.Stage, bareStorageOwner), []byte("wrong preparation"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stage-alternates":
				if err := os.MkdirAll(filepath.Join(p.Stage, "objects", "info"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(p.Stage, "objects", "info", "alternates"), []byte(filepath.Join(p.Root, ".git", "objects")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stage-ref":
				head, err := access.git(p.Stage).run(ctx, "rev-parse", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := access.git(p.Stage).run(ctx, "update-ref", "refs/continuity/unexpected", head); err != nil {
					t.Fatal(err)
				}
			case "stage-object":
				head, err := access.git(p.Stage).run(ctx, "rev-parse", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				oid := strings.Repeat("a", len(head))
				path := filepath.Join(p.Stage, "objects", oid[:2], oid[2:])
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				var body bytes.Buffer
				writer := zlib.NewWriter(&body)
				if _, err := writer.Write([]byte("blob 8\x00replaced")); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				if err := os.WriteFile(p.Receipt, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "competing-root":
				if err := os.Rename(p.Root, p.Retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p.Root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := completeBareClone(ctx, p, remote, access); err == nil {
				t.Fatal("changed recovery state activated")
			}
			if _, err := os.Stat(p.Stage); err != nil {
				t.Fatalf("refusal lost staged recovery data: %v", err)
			}
			if _, err := os.Stat(p.Receipt + ".done"); !os.IsNotExist(err) {
				t.Fatal("failed cutover published completion")
			}
		})
	}
}
