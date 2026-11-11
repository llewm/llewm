package memsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// PublicationReceipt describes delivery of one immutable committed snapshot.
// It does not cover later database writes or certify another machine's import.
type PublicationReceipt struct {
	State        string `json:"state"`
	SnapshotHead string `json:"snapshot_head,omitempty"`
	RemoteHead   string `json:"remote_head,omitempty"`
	Branch       string `json:"branch,omitempty"`
	LineageID    string `json:"lineage_id"`
	SourceCommit string `json:"source_commit"`
	SourceDirty  bool   `json:"source_dirty"`
	ObservedAt   string `json:"observed_at"`
	PeerReceipt  string `json:"peer_receipt"`
}

func (s Syncer) publicationArchive(ctx context.Context) (recordsync.Archive, error) {
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil {
		return recordsync.Archive{}, err
	}
	if identity == nil {
		return recordsync.Archive{}, fmt.Errorf("project portable identity is not initialized")
	}
	return recordsync.Archive{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID}, nil
}

func (s Syncer) publicationReceiptKey(lineage string) string {
	build := buildinfo.Current()
	body, _ := json.Marshal([]any{"git-delivery-receipt-v1", build.GitCommit, build.BuildDirty, s.ProjectID, lineage, s.Remote, filepath.Clean(s.Clone), s.Relay})
	return recordsync.Hash(string(body))
}

// PublicationStatus returns the last recorded observation, without contacting
// the remote. A missing, damaged or differently bound cache is not evidence.
func (s Syncer) PublicationStatus(ctx context.Context) (*PublicationReceipt, error) {
	a, err := s.publicationArchive(ctx)
	if err != nil {
		return nil, err
	}
	if build := buildinfo.Current(); build.BuildDirty || !validGitOID(build.GitCommit) {
		return nil, nil
	}
	var receipt PublicationReceipt
	if err := a.Status(s.publicationReceiptKey(a.Lineage), &receipt); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if receipt.SourceDirty || receipt.SourceCommit != buildinfo.Current().GitCommit || receipt.LineageID != a.Lineage ||
		(receipt.State != "snapshot_published" && receipt.State != "unconfirmed") ||
		receipt.PeerReceipt != "unconfirmed" || (receipt.SnapshotHead != "" && !validGitOID(receipt.SnapshotHead)) ||
		(receipt.RemoteHead != "" && !validGitOID(receipt.RemoteHead)) {
		return nil, fmt.Errorf("invalid publication receipt binding")
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.ObservedAt); err != nil {
		return nil, fmt.Errorf("invalid publication receipt time")
	}
	if receipt.State == "snapshot_published" && (receipt.SnapshotHead == "" || receipt.RemoteHead == "" || receipt.Branch == "") {
		return nil, fmt.Errorf("incomplete publication receipt")
	}
	return &receipt, nil
}

func (s Syncer) recordPublication(ctx context.Context, repo git, lineage, snapshot, branch string, delivered bool) (*PublicationReceipt, error) {
	build := buildinfo.Current()
	r := &PublicationReceipt{State: "unconfirmed", SnapshotHead: snapshot, Branch: branch, LineageID: lineage,
		SourceCommit: build.GitCommit, SourceDirty: build.BuildDirty, ObservedAt: s.now().Format(time.RFC3339Nano), PeerReceipt: "unconfirmed"}
	if delivered && snapshot != "" {
		remote, err := repo.run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
		if err != nil || !validGitOID(remote) {
			return r, fmt.Errorf("publication requires an observed remote tip")
		}
		if _, err := repo.run(ctx, "merge-base", "--is-ancestor", snapshot, remote); err != nil {
			return r, fmt.Errorf("observed remote does not contain the committed snapshot")
		}
		r.State, r.RemoteHead = "snapshot_published", remote
	}
	a := recordsync.Archive{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage}
	if err := a.SaveStatus(s.publicationReceiptKey(lineage), r); err != nil {
		return r, err
	}
	return r, nil
}

type RecordPublication struct {
	Ref          string   `json:"ref"`
	State        string   `json:"state"`
	EnvelopeHash []string `json:"envelope_sha256"`
}

type PublicationCheck struct {
	Remote      string              `json:"remote"`
	Branch      string              `json:"branch,omitempty"`
	RemoteHead  string              `json:"remote_head,omitempty"`
	LineageID   string              `json:"lineage_id,omitempty"`
	ObservedAt  string              `json:"observed_at,omitempty"`
	PeerReceipt string              `json:"peer_receipt"`
	Records     []RecordPublication `json:"records"`
}

// CheckPublication fetches the configured remote and verifies exact immutable
// record identities in its observed branch. It neither publishes nor imports.
// An optional envelope hash selects exact bytes rather than any version of an ID.
func (s Syncer) CheckPublication(ctx context.Context, refs []string, envelopeHash string) (PublicationCheck, error) {
	s.NoPrompt = true
	result := PublicationCheck{Remote: redact.String(s.Remote), PeerReceipt: "unconfirmed"}
	if len(refs) == 0 || len(refs) > 16 || (envelopeHash != "" && (len(refs) != 1 || !validDigest(envelopeHash))) {
		return result, fmt.Errorf("provide 1..16 checkpoint/narrative refs; an envelope hash requires one ref")
	}
	types, ids := make([]string, len(refs)), make([]string, len(refs))
	seen := map[string]bool{}
	for i, ref := range refs {
		kind, id, ok := strings.Cut(ref, ":")
		if !ok || (kind != "checkpoint" && kind != "narrative") || id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") || seen[ref] {
			return result, fmt.Errorf("unique checkpoint:ID or narrative:ID references required")
		}
		seen[ref], types[i], ids[i] = true, kind, id
		if kind == "narrative" {
			types[i] = "work_narrative"
		}
		result.Records = append(result.Records, RecordPublication{Ref: ref, State: "unconfirmed", EnvelopeHash: []string{}})
	}
	a, err := s.publicationArchive(ctx)
	if err != nil {
		return result, err
	}
	result.LineageID = a.Lineage
	repo, err := s.repository(ctx)
	if err != nil {
		return result, err
	}
	lock, err := lockFile(filepath.Join(s.Clone, "continuity-sync.lock"))
	if err != nil {
		return result, err
	}
	defer lock.Close()
	if err := probe(ctx, repo, s.Remote, time.Time{}); err != nil {
		return result, err
	}
	if err := s.fetch(ctx, repo, plan{}); err != nil {
		return result, err
	}
	local, err := repo.run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return result, err
	}
	result.Branch = remoteBranch(ctx, repo, local)
	var blocks []recordsync.Block
	if remoteExists(ctx, repo, result.Branch) {
		head, err := repo.run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+result.Branch)
		if err != nil || !validGitOID(head) {
			return result, fmt.Errorf("invalid observed remote tip")
		}
		transport := s.objectReader(repo)
		commit, err := transport.readCommit(ctx, head)
		if err != nil {
			return result, err
		}
		blocks, err = transport.blockDelta(ctx, "", commit.Tree, a.Lineage)
		if err != nil {
			return result, err
		}
		result.RemoteHead = head
	}
	for i := range result.Records {
		matches := map[string]bool{}
		for _, block := range blocks {
			for _, object := range block.Objects {
				e := object.Envelope
				if e.RecordType == types[i] && e.RecordID == ids[i] && (envelopeHash == "" || e.EnvelopeSHA256 == envelopeHash) {
					matches[e.EnvelopeSHA256] = true
				}
			}
		}
		if len(matches) > 8 {
			return result, fmt.Errorf("record has too many versions; select an exact envelope hash")
		}
		for hash := range matches {
			result.Records[i].EnvelopeHash = append(result.Records[i].EnvelopeHash, hash)
		}
		sort.Strings(result.Records[i].EnvelopeHash)
	}
	// Only complete byte/layout validation can produce a publication claim.
	for i := range result.Records {
		result.Records[i].State = "not_in_observed_remote"
		if len(result.Records[i].EnvelopeHash) > 0 {
			result.Records[i].State = "published"
		}
	}
	result.ObservedAt = s.now().Format(time.RFC3339Nano)
	if body, err := json.Marshal(result); err != nil || len(body) > 16<<10 {
		return PublicationCheck{}, fmt.Errorf("publication result exceeds bounds; check fewer references")
	}
	return result, nil
}
