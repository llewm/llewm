package fork

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestRevisionHeadsUseContentIdentityAcrossEnvelopeChanges(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p','/test','test','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	head := func(content, envelope string, local bool) ObservedHead {
		body := []byte(fmt.Sprintf(`{"record_type":"topic_revision","record_id":"sync","record_revision":2,"content_sha256":"%s","index":{}}`, content))
		return ObservedHead{RecordType: "topic_revision", RecordID: "sync", Revision: 2, ContentSHA256: content, EnvelopeSHA256: envelope, Envelope: body, RecordedAt: at, Local: local}
	}
	contentA, contentB := fmt.Sprintf("%064d", 1), fmt.Sprintf("%064d", 2)
	envelopeA1, envelopeA2, envelopeB1, envelopeB2 := fmt.Sprintf("%064d", 11), fmt.Sprintf("%064d", 12), fmt.Sprintf("%064d", 21), fmt.Sprintf("%064d", 22)
	if err := RecordHeads(ctx, db, "p", []ObservedHead{head(contentA, envelopeA1, true), head(contentA, envelopeA2, false)}, at); err != nil {
		t.Fatal(err)
	}
	if forks, err := RevisionForks(ctx, db, "p", ""); err != nil || len(forks) != 0 {
		t.Fatalf("equivalent content forked: %#v %v", forks, err)
	}
	var semantic, observed int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM fork_heads),(SELECT COUNT(*) FROM fork_head_observations)`).Scan(&semantic, &observed); err != nil || semantic != 1 || observed != 2 {
		t.Fatalf("head counts semantic=%d observed=%d: %v", semantic, observed, err)
	}
	if err := RecordHeads(ctx, db, "p", []ObservedHead{head(contentB, envelopeB1, false)}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if forks, err := RevisionForks(ctx, db, "p", ""); err != nil || len(forks) != 1 || len(forks[0].Heads) != 2 {
		t.Fatalf("different content did not fork: %#v %v", forks, err)
	}
	legacy := Resolution{ProjectID: "p", ID: "legacy", Family: FamilyRevision, Subject: "topic_revision:sync@2", AcceptedHead: envelopeA1, RejectedHeads: []string{envelopeB1}, Resolver: "owner", Authority: "user_reported", RecordedAt: at.Add(2 * time.Minute), SchemaVersion: SchemaVersion}
	if err := Insert(ctx, db, legacy); err != nil {
		t.Fatal(err)
	}
	if forks, err := RevisionForks(ctx, db, "p", ""); err != nil || len(forks) != 0 {
		t.Fatalf("legacy resolution did not map to content: %#v %v", forks, err)
	}
	if err := RecordHeads(ctx, db, "p", []ObservedHead{head(contentB, envelopeB2, false)}, at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if forks, err := RevisionForks(ctx, db, "p", ""); err != nil || len(forks) != 0 {
		t.Fatalf("format rewrite reopened rejected content: %#v %v", forks, err)
	}
	var stable sql.NullString
	if err := db.QueryRow(`SELECT content_sha256 FROM fork_head_observations WHERE envelope_sha256=?`, envelopeB2).Scan(&stable); err != nil || !stable.Valid || stable.String != contentB {
		t.Fatalf("new observation mapping = %#v: %v", stable, err)
	}
}
