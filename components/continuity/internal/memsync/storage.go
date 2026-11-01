package memsync

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

type CloneStoragePaths struct {
	Clone    string `json:"clone"`
	Stage    string `json:"stage"`
	Retained string `json:"retained"`
	Receipt  string `json:"receipt"`
}

// StoragePaths returns stable, private sibling destinations without creating
// them or changing the clone. Preparation never overwrites unowned paths.
func StoragePaths(clone string) (CloneStoragePaths, error) {
	p, err := defaultCloneStoragePaths(clone)
	return CloneStoragePaths{Clone: p.Root, Stage: p.Stage, Retained: p.Retained, Receipt: p.Receipt}, err
}

type CloneStoragePreparation struct {
	Paths         CloneStoragePaths `json:"paths"`
	ObjectFormat  string            `json:"object_format"`
	References    int               `json:"references"`
	ReceiptSHA256 string            `json:"receipt_sha256"`
}

type CloneStorageInspection struct {
	Paths         CloneStoragePaths `json:"paths"`
	State         string            `json:"state"`
	ObjectFormat  string            `json:"object_format"`
	References    int               `json:"references"`
	RefsSHA256    string            `json:"refs_sha256"`
	ReceiptSHA256 string            `json:"receipt_sha256,omitempty"`
}

// InspectStorage is a read-only preflight, not a stopped-writer proof. The
// preparation API independently verifies its source again before staging.
func InspectStorage(ctx context.Context, clone, remote string, access Access) (CloneStorageInspection, error) {
	var report CloneStorageInspection
	p, err := defaultCloneStoragePaths(clone)
	if err != nil {
		return report, err
	}
	report.Paths = CloneStoragePaths{Clone: p.Root, Stage: p.Stage, Retained: p.Retained, Receipt: p.Receipt}
	access.NoPrompt = true
	r, found, err := readCloneReceipt(p, remote)
	if err != nil {
		return report, err
	}
	if repo, err := openBareRepository(ctx, clone, remote, access); err == nil {
		snapshot, err := snapshotClone(ctx, repo, remote, false)
		if err != nil {
			return CloneStorageInspection{}, err
		}
		if err := fsckClone(ctx, repo); err != nil {
			return CloneStorageInspection{}, err
		}
		report.State, report.ObjectFormat, report.References, report.RefsSHA256 = "bare", snapshot.ObjectFormat, len(snapshot.Refs), snapshot.refsHash()
		if found {
			report.ReceiptSHA256 = recordsync.Hash(string(r.sealed()))
		}
		return report, nil
	}
	original, err := originalClonePath(p)
	if err != nil {
		return CloneStorageInspection{}, err
	}
	repo := access.git(original)
	if original == p.Retained {
		repo.WorkTree = original
	}
	snapshot, err := snapshotClone(ctx, repo, remote, true)
	if err != nil {
		return CloneStorageInspection{}, err
	}
	if err := durableGitAvailable(ctx, repo); err != nil {
		return CloneStorageInspection{}, err
	}
	if err := fsckClone(ctx, repo); err != nil {
		return CloneStorageInspection{}, err
	}
	if err := verifyLocalCloneSource(ctx, repo, original); err != nil {
		return CloneStorageInspection{}, err
	}
	report.State, report.ObjectFormat, report.References, report.RefsSHA256 = "working", snapshot.ObjectFormat, len(snapshot.Refs), snapshot.refsHash()
	if found {
		if !sourceMatchesReceipt(snapshot, r) {
			return CloneStorageInspection{}, fmt.Errorf("original clone changed after storage preparation")
		}
		report.State, report.ReceiptSHA256 = "preparing", recordsync.Hash(string(r.sealed()))
		if original == p.Retained {
			report.State = "switch_pending"
		}
	}
	return report, nil
}

// PrepareStorageMigration retains the working source unchanged and resumes its
// verified owned stage. No remote/network operation is part of preparation.
func PrepareStorageMigration(ctx context.Context, clone, remote string, access Access, writersStopped bool) (CloneStoragePreparation, error) {
	var report CloneStoragePreparation
	if !writersStopped {
		return report, fmt.Errorf("storage migration requires stopped writers")
	}
	p, err := defaultCloneStoragePaths(clone)
	if err != nil {
		return report, err
	}
	r, err := prepareBareClone(ctx, p, remote, access)
	if err != nil {
		return report, err
	}
	report.Paths = CloneStoragePaths{Clone: p.Root, Stage: p.Stage, Retained: p.Retained, Receipt: p.Receipt}
	report.ObjectFormat, report.References = r.ObjectFormat, r.RefCount
	report.ReceiptSHA256 = recordsync.Hash(string(r.sealed()))
	return report, nil
}

// CompleteStorageMigration resumes retirement/switch/completion and verifies
// the normal open gate. Originals and preparation metadata remain retained.
func CompleteStorageMigration(ctx context.Context, clone, remote string, access Access, writersStopped bool) error {
	if !writersStopped {
		return fmt.Errorf("storage migration requires stopped writers")
	}
	p, err := defaultCloneStoragePaths(clone)
	if err != nil {
		return err
	}
	if err := completeBareClone(ctx, p, remote, access); err != nil {
		return err
	}
	_, err = openBareRepository(ctx, clone, remote, access)
	return err
}

type CloneStorageAudit struct {
	Clone           string `json:"clone"`
	ObjectFormat    string `json:"object_format"`
	References      int    `json:"references"`
	HeadRef         string `json:"head_ref"`
	HeadOID         string `json:"head_oid,omitempty"`
	LineageID       string `json:"lineage_id,omitempty"`
	Blocks          int    `json:"blocks"`
	UniqueEnvelopes int    `json:"unique_envelopes"`
	FullObjectCheck bool   `json:"full_object_check"`
}

// AuditStorage checks all Git objects and refs independently of tree caches,
// then validates the selected lineage's current wire blocks. This is a full
// transport audit, not a peer receipt or semantic-import certification.
func AuditStorage(ctx context.Context, clone, remote, lineage string, access Access) (CloneStorageAudit, error) {
	var report CloneStorageAudit
	if _, err := recordsync.BlockDirectory(lineage); err != nil {
		return report, err
	}
	repo, err := openBareRepository(ctx, clone, remote, access)
	if err != nil {
		return report, err
	}
	lock, err := lockFile(filepath.Join(clone, "continuity-sync.lock"))
	if err != nil {
		return report, err
	}
	defer lock.Close()
	if err := fsckClone(ctx, repo); err != nil {
		return report, err
	}
	snapshot, err := snapshotClone(ctx, repo, remote, false)
	if err != nil {
		return report, err
	}
	transport := &objectTransport{repo: repo}
	branch, err := transport.currentBranch(ctx)
	if err != nil {
		return report, err
	}
	report.Clone, report.ObjectFormat, report.References = repo.Dir, snapshot.ObjectFormat, len(snapshot.Refs)
	report.HeadRef, report.HeadOID, report.LineageID = branch.Ref, branch.OID, lineage
	if branch.OID != "" {
		commit, err := transport.readCommit(ctx, branch.OID)
		if err != nil {
			return CloneStorageAudit{}, err
		}
		blocks, err := transport.blockDelta(ctx, "", commit.Tree, lineage)
		if err != nil {
			return CloneStorageAudit{}, err
		}
		seen := map[string]bool{}
		for _, block := range blocks {
			for _, object := range block.Objects {
				seen[object.Envelope.EnvelopeSHA256] = true
			}
		}
		report.Blocks, report.UniqueEnvelopes = len(blocks), len(seen)
	}
	report.FullObjectCheck = true
	return report, nil
}
