package recordsync

import (
	"bytes"
	"context"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// capturePublication accounts for only the files CaptureLocal may add. Existing
// files must keep their entire metadata identity. Parent directories may change
// timestamps/size, but must keep their device, inode and mode. This does not
// authorize endpoint publication or acknowledge any database changes.
type capturePublication struct {
	before      map[string]archiveMetadata
	additions   map[string][]byte
	directories map[string]bool
}

func (a Archive) prepareCapturePublication(ctx context.Context, state string, objects []portable.EncodedEnvelope) *capturePublication {
	stamp, before, err := directoryChangeSnapshot(ctx, a.Directory, true)
	if err != nil || stamp != state {
		return nil
	}
	p := &capturePublication{before: before, additions: map[string][]byte{}, directories: map[string]bool{}}
	add := func(path string, body []byte) {
		if _, exists := before[path]; exists {
			return
		}
		p.additions[path] = body
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			p.directories[parent] = true
			if parent == "." {
				break
			}
		}
	}
	for _, object := range objects {
		path := portable.MemoryRecordPath(object.Envelope)
		if _, exists := before[path]; !exists {
			add(path, portable.MemoryFileBytes(object))
		}
		origin := filepath.Join("origins", object.Envelope.EnvelopeSHA256)
		if _, exists := before[origin]; !exists {
			add(filepath.Join(origin, Hash("local")), []byte("observed\n"))
		}
	}
	return p
}

// completedStamp declines optimization on any unaccounted mutation, including
// concurrent valid foreign arrivals. A crash leaves the previous durable cursor
// untouched; recovery takes the normal full path. New files are byte-validated
// against the export and stat-checked after reading, not merely accepted by name.
func (p *capturePublication) completedStamp(ctx context.Context, a Archive) string {
	if p == nil {
		return ""
	}
	stamp, after, err := directoryChangeSnapshot(ctx, a.Directory, true)
	if err != nil {
		return ""
	}
	for path, old := range p.before {
		current, exists := after[path]
		if !exists {
			return ""
		}
		if old == current {
			continue
		}
		if !old.directory || !current.directory || !p.directories[path] || old.identity[0] != current.identity[0] || old.identity[1] != current.identity[1] || old.identity[2] != current.identity[2] {
			return ""
		}
	}
	for path, current := range after {
		if _, exists := p.before[path]; exists {
			continue
		}
		if current.directory {
			if !p.directories[path] {
				return ""
			}
		} else if _, exists := p.additions[path]; !exists {
			return ""
		}
	}
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return ""
	}
	defer root.Close()
	for path, expected := range p.additions {
		current, exists := after[path]
		if !exists || current.directory {
			return ""
		}
		body, err := ReadFile(root, path, len(expected))
		if err != nil || !bytes.Equal(body, expected) {
			return ""
		}
		info, err := root.Lstat(path)
		if err != nil {
			return ""
		}
		identity, ok := fileIdentity(info)
		if !ok || identity != current.identity {
			return ""
		}
	}
	return stamp
}
