package proseimport

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

func writeSource(t *testing.T, root, path, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotPagesPreserveAllTextAndOriginalIdentity(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmtNewline(newline), func(t *testing.T) {
			root := t.TempDir()
			// Each source exceeds the old response and source limits.
			body := strings.Repeat("Roadmap café 🙂"+newline, 20000) + "# Final section" + newline
			paths := []string{"ROADMAP.md", "PLAN.md", "EMPTY.md"}
			originals := []string{body, body + "Last item", ""}
			for i, path := range paths {
				writeSource(t, root, path, originals[i])
			}
			got := map[string]string{}
			var cursor string
			for pages := 0; ; pages++ {
				if pages > 100 {
					t.Fatal("pagination made no progress")
				}
				page, err := Snapshot(root, paths, 48<<10, cursor)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Sources) != len(paths) {
					t.Fatalf("incomplete manifest: %+v", page.Sources)
				}
				size := 0
				for _, doc := range page.Documents {
					if !utf8.ValidString(doc.Text) || len(doc.Text) > MaxChunkBytes || doc.Offset != len(got[doc.Path]) || doc.End != doc.Offset+len(doc.Text) {
						t.Fatalf("invalid chunk boundaries: %s %d..%d", doc.Path, doc.Offset, doc.End)
					}
					if strings.HasSuffix(doc.Text, "\r") {
						t.Fatal("split a CRLF pair")
					}
					got[doc.Path] += doc.Text
					size += len(doc.Text)
				}
				if size > 48<<10 || size == 0 && !page.Complete {
					t.Fatalf("page size %d, complete %v", size, page.Complete)
				}
				if page.Complete {
					if page.NextCursor != "" || pages == 0 {
						t.Fatal("unexpected completion")
					}
					break
				}
				cursor = page.NextCursor
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != len(paths) {
				t.Fatalf("snapshot created scratch files: %v %v", entries, err)
			}
			for i, path := range paths {
				b, err := os.ReadFile(filepath.Join(root, path))
				if err != nil || string(b) != originals[i] || got[path] != originals[i] {
					t.Fatalf("changed or omitted bytes for %s: %v", path, err)
				}
			}
		})
	}
}

func fmtNewline(s string) string {
	if s == "\r\n" {
		return "CRLF"
	}
	return "LF"
}

func TestSnapshotRedactsBeforeChunkingAndAllowsSmallBudgets(t *testing.T) {
	root := t.TempDir()
	body := "🙂🙂\r\ntoken =\n\"very-private-value\"\n" + strings.Repeat("x", 200) + "password=another-private-value\n"
	writeSource(t, root, "ROADMAP.md", body)
	for _, budget := range []int{4, 7, 32, MaxTotalBytes} {
		var got, cursor string
		for pages := 0; ; pages++ {
			if pages > len(body) {
				t.Fatal("pagination made no progress")
			}
			page, err := Snapshot(root, []string{"ROADMAP.md"}, budget, cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, doc := range page.Documents {
				got += doc.Text
				if doc.SHA256 != digest([]byte(body)) || !utf8.ValidString(doc.Text) {
					t.Fatal("lost original identity or invalid UTF-8")
				}
			}
			if page.Complete {
				break
			}
			cursor = page.NextCursor
		}
		if got != redact.String(body) || strings.Contains(got, "private-value") {
			t.Fatalf("redaction changed across chunks: %q", got)
		}
	}
}

func TestSnapshotCursorRejectsChangedSourcesAndSelection(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "A.md", strings.Repeat("A", 100))
	writeSource(t, root, "B.md", "B")
	page, err := Snapshot(root, []string{"A.md", "B.md"}, 8, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{{"B.md", "A.md"}, {"A.md"}} {
		if _, err := Snapshot(root, paths, 8, page.NextCursor); err == nil {
			t.Fatal("accepted changed selection")
		}
	}
	writeSource(t, root, "B.md", "changed unseen source")
	if _, err := Snapshot(root, []string{"A.md", "B.md"}, 8, page.NextCursor); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("accepted changed source: %v", err)
	}
	for _, cursor := range []string{"invalid!", strings.Repeat("x", 1025), base64.RawURLEncoding.EncodeToString([]byte(`{"index":-1}`))} {
		if _, err := Snapshot(root, []string{"A.md"}, 8, cursor); err == nil {
			t.Fatal("accepted invalid cursor")
		}
	}
}

func TestSnapshotRejectsInvalidSources(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "A.md", "test")
	writeSource(t, root, "nul.md", "a\x00b")
	writeSource(t, root, "invalid.md", "\xff")
	if err := os.Mkdir(filepath.Join(root, "directory.md"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{{"A.md", "./A.md"}, {"nul.md"}, {"invalid.md"}, {"directory.md"}, {"../outside.md"}, {".llewm/private.md"}, {"A.go"}} {
		if _, err := Snapshot(root, paths, MaxTotalBytes, ""); err == nil {
			t.Fatalf("accepted %v", paths)
		}
	}
}

func TestSnapshotReplayIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "A.md", strings.Repeat("section\n", 100))
	first, err := Snapshot(root, []string{"A.md"}, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := Snapshot(root, []string{"A.md"}, 20, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Snapshot(root, []string{"A.md"}, 20, first.NextCursor)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("same cursor did not replay the same page")
	}
	// Deliberately forged offsets are still bounded and UTF-8 checked.
	raw, _ := base64.RawURLEncoding.DecodeString(first.NextCursor)
	var cursor snapshotCursor
	json.Unmarshal(raw, &cursor)
	cursor.Offset = 10000
	raw, _ = json.Marshal(cursor)
	if _, err := Snapshot(root, []string{"A.md"}, 20, base64.RawURLEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("accepted out-of-range offset")
	}
}
