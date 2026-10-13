package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "readonly.db")
	created, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES ('p','/p','p','2026-01-01')`); err == nil {
		t.Fatal("read-only database accepted a write")
	}
}
