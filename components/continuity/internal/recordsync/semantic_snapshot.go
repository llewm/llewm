package recordsync

import (
	"encoding/json"
	"sort"
)

// This package-owned receipt certifies completeness only at its archive stamp.
// It is never a peer receipt. A missing observation stamp still permits the
// reference importer, but cannot establish a reusable semantic projection.
type semanticSnapshotReceipt struct {
	Project, Lineage, Consumer string
	Archive                    archiveStamp
	Observed                   ObservationStamp
	Blocks                     string
}

func semanticSnapshotDigest(blocks []Block) string {
	hashes := make([]string, 0, len(blocks))
	for _, block := range blocks {
		hashes = append(hashes, block.Hash)
	}
	sort.Strings(hashes)
	body, _ := json.Marshal(hashes)
	return Hash(string(body))
}

func (s Index) sealSemanticSnapshot(consumer string, archive archiveStamp, observed ObservationStamp, blocks []Block) *semanticSnapshotReceipt {
	return &semanticSnapshotReceipt{Project: s.ProjectID, Lineage: s.Lineage, Consumer: consumer, Archive: archive, Observed: observed, Blocks: semanticSnapshotDigest(blocks)}
}

func (s Index) validSemanticSnapshot(scope JournalPublicationScope, plan JournalPublicationPlan) bool {
	p := plan.snapshot
	return p != nil && p.Project == s.ProjectID && p.Lineage == s.Lineage && p.Consumer == scope.Consumer && validArchiveStamp(p.Archive) && p.Blocks == semanticSnapshotDigest(plan.Snapshot)
}
