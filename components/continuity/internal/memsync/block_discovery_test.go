package memsync

import (
	"context"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestObjectTreeDiscoveryReusesIdentitiesAndFailsClosedAtBounds(t *testing.T) {
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	files := map[string]blockTreeEntry{
		"selected/a/first":              objectBlob(t, transport, "first", "first", 40),
		"selected/b/space and\nnewline": objectBlob(t, transport, "space and\nnewline", "second", 40),
	}
	root := fixturePathTree(t, transport, files, 40)
	first, err := transport.changedLeaves(ctx, "", root, "selected", 100)
	if err != nil || len(first) != 2 {
		t.Fatalf("initial discovery: %d %v", len(first), err)
	}
	entries := transport.trees.entries
	if _, err := transport.changedLeaves(ctx, "", root, "selected", 100); err != nil || entries != transport.trees.entries {
		t.Fatalf("immutable tree was rediscovered: %v", err)
	}
	files["selected/a/late"] = objectBlob(t, transport, "late", "late", 40)
	next := fixturePathTree(t, transport, files, 40)
	transport.trees.entries = recordsync.MaxScanEntries
	if _, err := transport.changedLeaves(ctx, root, next, "selected", 100); err == nil {
		t.Fatal("exhausted discovery accepted an incomplete delta")
	}
	fresh := &objectTransport{repo: transport.repo}
	if complete, err := fresh.changedLeaves(ctx, "", next, "selected", 100); err != nil || len(complete) != 3 {
		t.Fatalf("fresh bounded recovery failed: %d %v", len(complete), err)
	}
}
