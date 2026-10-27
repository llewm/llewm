package memsync

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// previousEnvelopes validates the entire old partition before removing any
// old observation. Re-encoding the observations themselves preserves
// remote-only records and rejected heads which an operational export omits.
func previousEnvelopes(clone, lineage string) ([]portable.EncodedEnvelope, error) {
	root := filepath.Join(clone, portable.FormerMemoryLineageDir(lineage))
	var records []portable.EncodedEnvelope
	bytes := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("format conversion refuses symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !strings.HasSuffix(path, ".json") || info.Size() > portable.MaxPayloadBytes+portable.MaxIndexBytes+(64<<10) {
			return fmt.Errorf("format conversion refuses unexpected file: %s", path)
		}
		bytes += int(info.Size())
		if bytes > portable.MaxRecordsBytes || len(records) >= portable.MaxRecordCount {
			return fmt.Errorf("format conversion exceeds its record/byte budget; nothing removed")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(clone, path)
		if err != nil {
			return err
		}
		converted, err := portable.ConvertPreviousMemoryFile(relative, body)
		if err != nil {
			return fmt.Errorf("convert %s: %w", relative, err)
		}
		if converted.Envelope.LineageID != lineage {
			return fmt.Errorf("format conversion found another lineage in %s", relative)
		}
		records = append(records, converted)
		return nil
	})
	return records, err
}

// retireFormerFormat makes one normal, fast-forwardable history commit. The
// original v2 bytes remain in Git history and the database observation ledger.
func retireFormerFormat(ctx context.Context, repo git, clone string, identity portable.Identity, convert bool, result *Result) error {
	ancient := filepath.Join(clone, "v1", strings.TrimPrefix(identity.LineageID, "lineage:"))
	if _, err := os.Lstat(ancient); err == nil {
		return fmt.Errorf("format-1 records require the original format-2 conversion before upgrading; nothing removed")
	} else if !os.IsNotExist(err) {
		return err
	}
	former := portable.FormerMemoryLineageDir(identity.LineageID)
	if _, err := os.Lstat(filepath.Join(clone, former)); os.IsNotExist(err) {
		if convert {
			result.Converted = true
		}
		return nil
	} else if err != nil {
		return err
	}
	if !convert {
		return fmt.Errorf("the memory repository holds format-2 observations for this lineage; retire all older writers, then run `llewm sync convert --writers-stopped`; no history is discarded")
	}
	records, err := previousEnvelopes(clone, identity.LineageID)
	if err != nil {
		return err
	}
	written, err := writeNew(clone, records)
	if err != nil {
		return err
	}
	if _, err := repo.run(ctx, "rm", "-r", "-q", "--", former); err != nil {
		return err
	}
	if err := commit(ctx, repo, portable.MemoryLineageDir(identity.LineageID), len(written), fmt.Sprintf("continuity: %s convert to format 3, +%d observations", identity.InstanceID, len(written))); err != nil {
		return err
	}
	result.ExportedRecords += len(written)
	result.Converted = true
	return nil
}
