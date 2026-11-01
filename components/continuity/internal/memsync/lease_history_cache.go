package memsync

import (
	"context"
	"encoding/json"

	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// A receipt retains raw immutable objects, never trusted decoded events. It is
// disposable device-local status, excluded from portable history. One bounded
// receipt per ref replaces the previous tip; no append-only object cache grows
// with renewals. Missing, damaged, old, oversized or unwritable status only costs
// Git reads. Ref freshness still comes from the locked fetch/listing and writes
// still compare-and-swap the exact listed tip.
type leaseHistoryReceipt struct {
	Version int      `json:"version"`
	Tip     string   `json:"tip"`
	Commits [][]byte `json:"commits"`
}

func (r leaseRefs) historyKey(ref string) string {
	body, _ := json.Marshal([]string{"lease-history-v1", r.clone, r.remote, r.prefix, ref})
	return "lease-history:" + recordsync.Hash(string(body))
}

func (r leaseRefs) cachedEvents(ctx context.Context, transport *objectTransport, ref, tip string) ([]lease.RemoteEvent, error) {
	key := r.historyKey(ref)
	if r.archive != nil {
		var receipt leaseHistoryReceipt
		if err := r.archive.Status(key, &receipt); err == nil && receipt.Version == 1 && receipt.Tip == tip && len(receipt.Commits) > 0 && len(receipt.Commits) <= leaseHistory {
			if events, err := readLeaseHistory(ctx, transport, tip, receipt.Commits, nil); err == nil && r.ref(events[0].ScopeKind, events[0].ScopeID) == ref {
				syncwork.RecordWork(ctx, "lease_history_cache_hits", 1)
				return events, nil
			}
		}
	}
	var bodies [][]byte
	events, err := readLeaseHistory(ctx, transport, tip, nil, &bodies)
	if err != nil {
		return nil, err
	}
	if len(events) > 0 && r.ref(events[0].ScopeKind, events[0].ScopeID) == ref && r.archive != nil {
		// SaveStatus enforces its 64 KiB serialized bound. Avoid allocating a
		// second large representation for histories that cannot fit it.
		size := 0
		for _, body := range bodies {
			size += len(body)
		}
		if size <= 40<<10 {
			_ = r.archive.SaveStatus(key, leaseHistoryReceipt{Version: 1, Tip: tip, Commits: bodies})
		}
	}
	return events, nil
}
