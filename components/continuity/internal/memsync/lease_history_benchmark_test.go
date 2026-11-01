package memsync

import (
	"context"
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Compare the former per-scope reader, a cold shared batch, and unchanged-tip
// receipts on the same bounded fixture, excluding setup and network latency.
func BenchmarkLeaseHistoryRead(b *testing.B) {
	for _, mode := range []string{"per-scope", "batch-miss", "unchanged"} {
		b.Run(mode, func(b *testing.B) {
			refs, writer, event := leaseHistoryFixture(b)
			tips := make([]string, 8)
			refNames := make([]string, 8)
			for scope := range tips {
				event.ScopeID = fmt.Sprintf("scope-%d", scope)
				event.GrantID = fmt.Sprintf("grant-%d", scope)
				refNames[scope] = refs.ref(event.ScopeKind, event.ScopeID)
				for i := 1; i <= leaseHistory; i++ {
					event.Sequence, event.Action = i, "renew"
					if i == 1 {
						event.Action = "acquire"
					}
					var parents []string
					if tips[scope] != "" {
						parents = []string{tips[scope]}
					}
					tips[scope] = leaseHistoryCommit(b, writer, event, parents...)
				}
				if _, err := refs.cachedEvents(context.Background(), writer, refNames[scope], tips[scope]); err != nil {
					b.Fatal(err)
				}
			}
			if mode == "batch-miss" {
				refs.archive = nil
			}
			processes := int64(0)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx, metrics := syncwork.WithWorkMetrics(context.Background())
				batch := &gitCommitBatch{repo: refs.repo}
				transport := &objectTransport{repo: refs.repo, commitBatch: batch}
				for scope, tip := range tips {
					var err error
					if mode == "per-scope" {
						local := &objectTransport{repo: refs.repo}
						_, err = readLeaseHistory(ctx, local, tip, nil, nil)
						processes += int64(local.trees.gitReads)
					} else {
						_, err = refs.cachedEvents(ctx, transport, refNames[scope], tip)
					}
					if err != nil {
						batch.Close()
						b.Fatal(err)
					}
				}
				batch.Close()
				counts := metrics.Counts()
				if mode == "per-scope" {
					processes += counts["lease_history_git_reads"]
				} else {
					processes += counts["lease_commit_batch_processes"] + int64(transport.trees.gitReads)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(processes)/float64(b.N), "Git-processes/op")
		})
	}
}
