package proseimport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

type SnapshotPage struct {
	Sources    []Source   `json:"sources"`
	Documents  []Document `json:"documents"`
	Complete   bool       `json:"complete"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type snapshotCursor struct {
	Selection string `json:"selection"`
	Index     int    `json:"index"`
	Offset    int    `json:"offset"`
}

// readDocument redacts the whole source before splitting so chunk boundaries
// cannot expose a partial credential. Only one source is buffered at a time.
func readDocument(root, path string) (Document, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt", ".rst":
	default:
		return Document{}, fmt.Errorf("source %q must be Markdown, text or reStructuredText", path)
	}
	full, err := adoption.SafePath(root, path)
	if err != nil {
		return Document{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return Document{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSourceBytes {
		return Document{}, fmt.Errorf("source %q must be a regular file within the %d-byte validation limit", path, MaxSourceBytes)
	}
	f, err := os.Open(full)
	if err != nil {
		return Document{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
	if err != nil {
		return Document{}, err
	}
	if len(b) > MaxSourceBytes {
		return Document{}, fmt.Errorf("source %q exceeds the %d-byte validation limit", path, MaxSourceBytes)
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return Document{}, fmt.Errorf("source %q is not UTF-8 prose", path)
	}
	text := redact.String(string(b))
	return Document{Source: Source{Path: path, SHA256: digest(b)}, Text: text, End: len(text)}, nil
}

// Snapshot returns a bounded page of disposable in-memory chunks. Every chunk
// retains the original path/hash; offsets address redacted UTF-8 bytes. A cursor
// pins the ordered source selection and hashes, refusing changes between pages.
func Snapshot(root string, paths []string, budget int, cursor string) (SnapshotPage, error) {
	if len(paths) == 0 || len(paths) > 16 || budget < utf8.UTFMax || budget > MaxTotalBytes {
		return SnapshotPage{}, fmt.Errorf("select 1..16 prose files and a byte budget within 4..%d", MaxTotalBytes)
	}
	var position snapshotCursor
	if cursor != "" {
		if len(cursor) > 1024 {
			return SnapshotPage{}, fmt.Errorf("invalid source cursor")
		}
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(b, &position) != nil || !hashPattern.MatchString(position.Selection) || position.Index < 0 || position.Index >= len(paths) || position.Offset < 0 {
			return SnapshotPage{}, fmt.Errorf("invalid source cursor")
		}
	}
	page := SnapshotPage{Documents: []Document{}}
	seen := map[string]bool{}
	next := position
	for i, path := range paths {
		path = filepath.ToSlash(filepath.Clean(path))
		if seen[path] {
			return SnapshotPage{}, fmt.Errorf("duplicate source %q", path)
		}
		seen[path] = true
		doc, err := readDocument(root, path)
		if err != nil {
			return SnapshotPage{}, err
		}
		page.Sources = append(page.Sources, doc.Source)
		if i != next.Index {
			continue
		}
		if next.Offset > len(doc.Text) || next.Offset < len(doc.Text) && !utf8.RuneStart(doc.Text[next.Offset]) {
			return SnapshotPage{}, fmt.Errorf("invalid source cursor offset")
		}
		for next.Offset < len(doc.Text) {
			end := chunkEnd(doc.Text, next.Offset, min(budget, MaxChunkBytes))
			if end == next.Offset {
				break
			}
			// Copy the small chunk so the page does not retain the full source buffer.
			page.Documents = append(page.Documents, Document{Source: doc.Source, Text: strings.Clone(doc.Text[next.Offset:end]), Offset: next.Offset, End: end})
			budget -= end - next.Offset
			next.Offset = end
		}
		if next.Offset == len(doc.Text) {
			if len(doc.Text) == 0 {
				page.Documents = append(page.Documents, doc)
			}
			next.Index++
			next.Offset = 0
		}
	}
	b, _ := json.Marshal(page.Sources)
	selection := digest(b)
	if cursor != "" && position.Selection != selection {
		return SnapshotPage{}, fmt.Errorf("source selection or contents changed; restart the snapshot without --cursor and rebuild the import")
	}
	page.Complete = next.Index == len(paths)
	if !page.Complete {
		next.Selection = selection
		b, _ = json.Marshal(next)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}

// Prefer paragraph/line boundaries, but keep progress on long lines. Never split
// a UTF-8 code point or the CRLF pair used by Windows checkouts.
func chunkEnd(text string, start, budget int) int {
	end := min(start+budget, len(text))
	if end == len(text) {
		return end
	}
	for end > start && !utf8.RuneStart(text[end]) {
		end--
	}
	if end > start && text[end-1] == '\r' && text[end] == '\n' {
		end--
	}
	part := text[start:end]
	if boundary := strings.LastIndex(part, "\n\n"); boundary >= len(part)/2 {
		return start + boundary + 2
	}
	if boundary := strings.LastIndexByte(part, '\n'); boundary >= len(part)/2 {
		return start + boundary + 1
	}
	return end
}
