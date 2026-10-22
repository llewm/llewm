package recordsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"sort"
	"time"
)

var ErrSemanticPending = errors.New("semantic history import pending")

type ImportResult struct {
	ImportedRecords   int `json:"imported_records"`
	Collisions        int `json:"collisions"`
	Forks             int `json:"forks"`
	SupersessionForks int `json:"supersession_forks"`
	DeferredRecords   int `json:"deferred_records"`
}

type Importer struct {
	DB        *sql.DB
	ProjectID string
	Now       func() time.Time
}

func (s Importer) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Importer) Import(ctx context.Context, objects []portable.EncodedEnvelope) (ImportResult, error) {
	var result ImportResult
	err := s.importRecords(ctx, objects, &result)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrSemanticPending, err)
	}
	return result, nil
}
func (s Importer) importRecords(ctx context.Context, objects []portable.EncodedEnvelope, result *ImportResult) error {
	versions := map[string][]portable.EnvelopeSpec{}
	stored := map[string][]portable.EncodedEnvelope{}
	var order []string
	for _, e := range objects {
		if portable.RetiredRecordType(e.Envelope.RecordType) {
			continue
		}
		if !portable.SupportedEnvelope(e.Envelope) {
			result.DeferredRecords++
			continue
		}
		spec, err := portable.DecodeMemoryEnvelope(e.Bytes)
		if err != nil {
			return err
		}
		identity := portable.RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)
		if _, seen := versions[identity]; !seen {
			order = append(order, identity)
		}
		versions[identity] = append(versions[identity], spec)
		stored[identity] = append(stored[identity], e)
	}
	sort.Strings(order)
	var specs []portable.EnvelopeSpec
	var forked [][]fork.ObservedHead
	// Recoveries carry their own bounded proof and must be applied before
	// interpreting the historical identities they correct, also on fresh peers.
	var recoveries []portable.EnvelopeSpec
	for _, values := range versions {
		for _, spec := range values {
			if spec.RecordType == "redaction_recovery" {
				recoveries = append(recoveries, spec)
			}
		}
	}
	if len(recoveries) > 0 {
		plan, receipt, err := portable.ImportMemoryRecords(ctx, s.DB, s.ProjectID, recoveries, s.now())
		if err != nil {
			return err
		}
		if !plan.ApplyAllowed {
			return fmt.Errorf("redaction recovery observations collide")
		}
		if receipt != nil {
			result.ImportedRecords += receipt.AppendedRecords
		}
	}
	recovered, err := portable.RecoveredEnvelopes(ctx, s.DB, s.ProjectID)
	if err != nil {
		return err
	}
	for _, identity := range order {
		if len(versions[identity]) > 0 && versions[identity][0].RecordType == "redaction_recovery" {
			continue
		}
		if len(stored[identity]) > 1 {
			heads, err := forkHeads(stored[identity], recovered)
			if err != nil {
				return err
			}
			if len(heads) == 1 {
				spec, err := portable.DecodeMemoryEnvelope(heads[0].Envelope)
				if err != nil {
					return err
				}
				specs = append(specs, spec)
				continue
			}
			if heads == nil {
				result.Collisions++
			} else {
				forked = append(forked, heads)
			}
			continue
		}
		if len(versions[identity]) == 0 {
			continue // exported by this cycle
		}
		specs = append(specs, versions[identity][0])
	}
	var keep, adopt []fork.ObservedHead
	if len(forked) > 0 {
		local, err := portable.LocalEnvelopes(ctx, s.DB, s.ProjectID)
		if err != nil {
			return err
		}
		for _, heads := range forked {
			newHead := false
			for _, head := range heads {
				var count int
				if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fork_heads WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=? AND content_sha256=?`, s.ProjectID, head.RecordType, head.RecordID, head.Revision, head.ContentSHA256).Scan(&count); err != nil {
					return err
				}
				if count == 0 {
					newHead = true
				}
			}
			if newHead {
				result.Forks++
			}
			first := heads[0]
			if held, ok := local[portable.RecordKey(first.RecordType, first.RecordID, first.Revision)]; ok {
				heads = withLocalHead(heads, held)
				keep = append(keep, heads...)
				continue
			}
			// The project has no version yet: take the proposed (newest) one so
			// later revisions have a predecessor, and keep the rest as heads.
			proposed := newestHead(heads)
			heads[proposed].Local = true
			spec, err := portable.DecodeMemoryEnvelope(heads[proposed].Envelope)
			if err != nil {
				return err
			}
			specs = append(specs, spec)
			adopt = append(adopt, heads...)
		}
	}
	if err := fork.RecordHeads(ctx, s.DB, s.ProjectID, keep, s.now()); err != nil {
		return err
	}
	if len(specs) > 0 {
		before, err := supersessionForkRefs(ctx, s.DB, s.ProjectID)
		if err != nil {
			return err
		}
		plan, receipt, err := portable.ImportMemoryRecords(ctx, s.DB, s.ProjectID, specs, s.now())
		if err != nil {
			return err
		}
		if !plan.ApplyAllowed {
			result.Collisions += plan.Counts[portable.PlanCollision]
			if plan.Counts[portable.PlanInvalid]+plan.Counts[portable.PlanUnsupported] > 0 {
				return fmt.Errorf("memory import refused: %d invalid, %d unsupported records", plan.Counts[portable.PlanInvalid], plan.Counts[portable.PlanUnsupported])
			}
		} else {
			if receipt != nil {
				result.ImportedRecords += receipt.AppendedRecords
			}
			// Only now does the project hold the adopted versions.
			if err := fork.RecordHeads(ctx, s.DB, s.ProjectID, adopt, s.now()); err != nil {
				return err
			}
			after, err := supersessionForkRefs(ctx, s.DB, s.ProjectID)
			if err != nil {
				return err
			}
			for ref := range after {
				if !before[ref] {
					result.SupersessionForks++
				}
			}
		}
	}
	return nil
}

func forkHeads(envelopes []portable.EncodedEnvelope, recovered map[string]portable.EncodedEnvelope) ([]fork.ObservedHead, error) {
	var heads []fork.ObservedHead
	var bodies [][]byte
	byContent := map[string]int{}
	for _, encoded := range envelopes {
		if original, ok := recovered[encoded.Envelope.EnvelopeSHA256]; ok {
			encoded = original
		}
		e := encoded.Envelope
		at, err := time.Parse(time.RFC3339Nano, e.RecordedAt)
		if err != nil {
			return nil, err
		}
		head := fork.ObservedHead{RecordType: e.RecordType, RecordID: e.RecordID, Revision: e.RecordRevision, ContentSHA256: e.ContentSHA256, EnvelopeSHA256: e.EnvelopeSHA256, Envelope: encoded.Bytes, RecordedAt: at}
		key := head.ContentSHA256
		if head.Revision == 0 {
			key = head.EnvelopeSHA256
		}
		if index, ok := byContent[key]; ok {
			if head.RecordedAt.After(heads[index].RecordedAt) || head.RecordedAt.Equal(heads[index].RecordedAt) && head.EnvelopeSHA256 > heads[index].EnvelopeSHA256 {
				heads[index] = head
			}
			continue
		}
		byContent[key] = len(heads)
		heads = append(heads, head)
		bodies = append(bodies, encoded.Bytes)
	}
	if len(heads) > 1 && heads[0].Revision == 0 {
		return nil, nil
	}
	if fork.EquivalentRoadmapNodeEnvelopes(bodies) {
		return heads[:1], nil
	}
	return heads, nil
}

// withLocalHead marks the project's version, adding it when the repository
// does not have it (it always should, since export runs first).
func withLocalHead(heads []fork.ObservedHead, held portable.EncodedEnvelope) []fork.ObservedHead {
	for index := range heads {
		if heads[index].ContentSHA256 == held.Envelope.ContentSHA256 {
			heads[index].Local = true
			return heads
		}
	}
	recorded, _ := time.Parse(time.RFC3339Nano, held.Envelope.RecordedAt)
	return append(heads, fork.ObservedHead{RecordType: held.Envelope.RecordType, RecordID: held.Envelope.RecordID, Revision: held.Envelope.RecordRevision, ContentSHA256: held.Envelope.ContentSHA256, EnvelopeSHA256: held.Envelope.EnvelopeSHA256, Envelope: held.Bytes, RecordedAt: recorded, Local: true})
}

// newestHead is the index of the head a fork proposes: the newest, with the
// larger envelope hash breaking ties, as fork.RevisionForks orders them.
func newestHead(heads []fork.ObservedHead) int {
	best := 0
	for index, head := range heads[1:] {
		index++
		if head.RecordedAt.After(heads[best].RecordedAt) || (head.RecordedAt.Equal(heads[best].RecordedAt) && head.ContentSHA256 > heads[best].ContentSHA256) {
			best = index
		}
	}
	return best
}

// supersessionForkRefs names the project's open supersession forks.
func supersessionForkRefs(ctx context.Context, db *sql.DB, projectID string) (map[string]bool, error) {
	forks, err := fork.SupersessionForks(ctx, db, projectID, "")
	if err != nil {
		return nil, err
	}
	refs := make(map[string]bool, len(forks))
	for _, value := range forks {
		refs[value.Ref()] = true
	}
	return refs, nil
}
