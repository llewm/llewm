package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// AuthorizedBlocks groups only objects already authorized for the destination.
// It never combines an unauthorized relay object into an otherwise allowed
// block. Blocks are deterministic for the same authorized object set. This is
// a snapshot packer, not an incremental publication protocol: callers must
// track membership before publishing changing snapshots as immutable blocks.
func (a Archive) AuthorizedBlocks(ctx context.Context, endpoint string, relay bool) ([]Block, error) {
	objects, err := a.Objects(ctx, endpoint, relay)
	if err != nil {
		return nil, err
	}
	return packValidatedBlocks(a.Lineage, objects)
}

// packValidatedBlocks accepts only envelopes already decoded and checked for
// canonical bytes and lineage. It avoids decoding the same bytes twice while
// retaining the same deterministic block representation as EncodeBlock.
func packValidatedBlocks(lineage string, objects []portable.EncodedEnvelope) ([]Block, error) {
	sort.Slice(objects, func(i, j int) bool { return bytes.Compare(objects[i].Bytes, objects[j].Bytes) < 0 })
	var result []Block
	for start := 0; start < len(objects); {
		end, size := start, len(blockHeader)
		for end < len(objects) && end-start < MaxBlockRecords && size+len(objects[end].Bytes)+1 <= MaxBlockBytes {
			if objects[end].Envelope.LineageID != lineage || (end > 0 && bytes.Equal(objects[end-1].Bytes, objects[end].Bytes)) {
				return nil, fmt.Errorf("invalid or duplicate validated envelope")
			}
			size += len(objects[end].Bytes) + 1
			end++
		}
		if end == start {
			return nil, fmt.Errorf("one envelope exceeds block bounds")
		}
		var body bytes.Buffer
		body.Grow(size)
		body.WriteString(blockHeader)
		for _, object := range objects[start:end] {
			body.Write(object.Bytes)
			body.WriteByte('\n')
		}
		sum := sha256.Sum256(body.Bytes())
		block := Block{Hash: hex.EncodeToString(sum[:]), Bytes: body.Bytes(), Objects: append([]portable.EncodedEnvelope(nil), objects[start:end]...)}
		result = append(result, block)
		start = end
	}
	return result, nil
}

// ConvertV3Snapshot packs a complete v3 observation inventory into v4 blocks.
// It does not publish or remove anything. The transport must establish that
// the inventory covers its authoritative paths and pass the scan status; an
// incomplete inventory must never be used to retire the old representation.
func ConvertV3Snapshot(lineage string, scan ScanResult) ([]Block, error) {
	if scan.Pending != 0 || scan.Invalid != 0 {
		return nil, fmt.Errorf("v3 conversion requires a complete valid inventory")
	}
	if len(scan.Objects) > portable.MaxRecordCount {
		return nil, fmt.Errorf("v3 conversion exceeds record limit")
	}
	objects := append([]portable.EncodedEnvelope(nil), scan.Objects...)
	seen := make(map[string]bool, len(objects))
	total := 0
	for i, object := range objects {
		validated, err := portable.DecodeTransportEnvelope(object.Bytes)
		if err != nil || validated.Envelope.LineageID != lineage {
			return nil, fmt.Errorf("v3 conversion found an invalid envelope")
		}
		if seen[validated.Envelope.EnvelopeSHA256] {
			return nil, fmt.Errorf("v3 conversion found a duplicate envelope")
		}
		seen[validated.Envelope.EnvelopeSHA256] = true
		objects[i] = validated
		total += len(validated.Bytes)
		if total > portable.MaxRecordsBytes {
			return nil, fmt.Errorf("v3 conversion exceeds byte limit")
		}
	}
	return packValidatedBlocks(lineage, objects)
}

// BlockDirectory is the experimental block namespace. It does not replace
// the current record-per-file transport until both transports are qualified.
func BlockDirectory(lineage string) (string, error) {
	if !lineagePattern.MatchString(lineage) {
		return "", fmt.Errorf("invalid block lineage")
	}
	return filepath.Join("v4", strings.TrimPrefix(lineage, "lineage:"), "blocks"), nil
}

func blockPath(lineage, hash string) (string, error) {
	directory, err := BlockDirectory(lineage)
	if err != nil {
		return "", err
	}
	if !hex64.MatchString(hash) {
		return "", fmt.Errorf("invalid block hash")
	}
	return filepath.Join(directory, hash+".block"), nil
}

// PublishBlock uses the same exclusive, durable publication primitive as
// individual envelopes. A block's content determines its immutable path.
func PublishBlock(root *os.Root, lineage string, block Block) (bool, error) {
	path, err := blockPath(lineage, block.Hash)
	if err != nil {
		return false, err
	}
	if _, err := DecodeBlock(lineage, block.Hash, block.Bytes); err != nil {
		return false, err
	}
	return WriteFile(root, path, block.Bytes, false)
}

// ReadBlock validates one exact content-addressed path. Git callers should
// pass tracked paths, rather than treating untracked working-tree files as
// received history.
func ReadBlock(root *os.Root, lineage, path string) (Block, error) {
	name := filepath.Base(path)
	hash := strings.TrimSuffix(name, ".block")
	expected, err := blockPath(lineage, hash)
	if err != nil || name != hash+".block" || path != expected {
		return Block{}, fmt.Errorf("invalid block path")
	}
	body, err := ReadFile(root, path, MaxBlockBytes)
	if err != nil {
		return Block{}, err
	}
	return DecodeBlock(lineage, hash, body)
}

type BlockScanResult struct {
	Blocks  []Block
	Pending int
	Invalid int
	// CatalogReused reports local membership receipt reuse, not semantic reuse.
	CatalogReused bool
	// Only a successful catalog read can supply publication membership. Keep
	// this private and tied to the open transport root, not caller-editable data.
	publicationBase *manifestPublicationBase
}

// ScanBlocks reads only complete, validated blocks in the selected lineage.
// Partial files and provider conflict copies are reported, never imported.
func ScanBlocks(ctx context.Context, root *os.Root, lineage string) (BlockScanResult, error) {
	return scanBlocks(ctx, root, lineage, func(path string) (Block, error) { return ReadBlock(root, lineage, path) })
}

// ScanIndexedBlocks uses a validated disposable inventory for repeated reads.
// Index corruption is surfaced to the coordinator; incomplete files remain
// pending and never become imported observations.
func (s Index) ScanBlocks(ctx context.Context, root *os.Root) (BlockScanResult, error) {
	return scanBlocks(ctx, root, s.Lineage, func(path string) (Block, error) { return s.ReadBlock(ctx, root, path) })
}

func scanBlocks(ctx context.Context, root *os.Root, lineage string, read func(string) (Block, error)) (BlockScanResult, error) {
	var result BlockScanResult
	base, err := BlockDirectory(lineage)
	if err != nil {
		return result, err
	}
	info, err := root.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, fmt.Errorf("block directory must be a real directory")
	}
	directory, err := openRead(root, base)
	if err != nil {
		return result, err
	}
	defer directory.Close()
	entries, totalBytes, totalRecords := 0, 0, 0
	for {
		values, readErr := directory.ReadDir(256)
		for _, entry := range values {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			entries++
			if entries > MaxScanEntries {
				return result, fmt.Errorf("block scan entry limit exceeded")
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".llewm-stage-") {
				continue
			}
			hash := strings.TrimSuffix(name, ".block")
			if name != hash+".block" || !hex64.MatchString(hash) {
				result.Invalid++
				continue
			}
			block, err := read(filepath.Join(base, name))
			if err != nil {
				if errors.Is(err, ErrIndexRebuild) {
					return result, err
				}
				result.Pending++
				continue
			}
			totalBytes += len(block.Bytes) - len(blockHeader)
			totalRecords += len(block.Objects)
			if totalBytes > portable.MaxRecordsBytes || totalRecords > portable.MaxRecordCount {
				return result, fmt.Errorf("block scan records exceed bounds")
			}
			result.Blocks = append(result.Blocks, block)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, readErr
		}
	}
	sort.Slice(result.Blocks, func(i, j int) bool { return result.Blocks[i].Hash < result.Blocks[j].Hash })
	return result, nil
}
