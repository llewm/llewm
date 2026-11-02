package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// BlockVersion identifies the encoding and validation rules for immutable
// history blocks. A changed encoding must use a new version.
const BlockVersion = 1

const blockHeader = "llewm-history-block-v1\n"
const MaxBlockBytes = 1 << 20
const MaxBlockRecords = 512

// Block is a content-addressed, deterministic collection of wire envelopes.
// The hash identifies the entire encoded block, including its version.
type Block struct {
	Hash      string
	Bytes     []byte
	Objects   []portable.EncodedEnvelope
	validated *sealedBlockContents
	// decoded certifies immutable wire bytes only. It must never stand in for
	// validated, whose producers have also checked a database snapshot.
	decoded *sealedBlockContents
}

// A private copy prevents a caller from mutating exported Block fields and
// turning an unvalidated envelope into a trusted validation result.
type sealedBlockContents struct {
	lineage string
	objects []portable.EncodedEnvelope
}

func copyEnvelope(e portable.EncodedEnvelope) portable.EncodedEnvelope {
	copy := e
	copy.Bytes = append([]byte(nil), e.Bytes...)
	copy.Envelope.Index = append([]byte(nil), e.Envelope.Index...)
	copy.Envelope.Payload = append([]byte(nil), e.Envelope.Payload...)
	copy.Envelope.Refs = append([]portable.Reference(nil), e.Envelope.Refs...)
	return copy
}

func sealBlockContents(lineage string, objects []portable.EncodedEnvelope) *sealedBlockContents {
	sealed := &sealedBlockContents{lineage: lineage, objects: make([]portable.EncodedEnvelope, len(objects))}
	for i, object := range objects {
		sealed.objects[i] = copyEnvelope(object)
	}
	return sealed
}

func (b Block) matchesValidatedContents(lineage string) bool {
	return matchesSealedContents(lineage, b.Bytes, b.validated)
}

func matchesSealedContents(lineage string, body []byte, sealed *sealedBlockContents) bool {
	if sealed == nil || sealed.lineage != lineage || !bytes.HasPrefix(body, []byte(blockHeader)) {
		return false
	}
	rest := body[len(blockHeader):]
	for _, object := range sealed.objects {
		if !bytes.HasPrefix(rest, object.Bytes) || len(rest) <= len(object.Bytes) || rest[len(object.Bytes)] != '\n' {
			return false
		}
		rest = rest[len(object.Bytes)+1:]
	}
	return len(rest) == 0
}

// decodeObservedBlock reuses only pure byte validation. Database inventory,
// provenance, dependency and semantic checks remain the caller's responsibility.
// Exported parsed objects are untrusted, even when the wire receipt matches.
func decodeObservedBlock(ctx context.Context, lineage string, input Block) (Block, error) {
	sealed := input.decoded
	if len(input.Bytes) <= MaxBlockBytes && sealed != nil && len(sealed.objects) > 0 && len(sealed.objects) <= MaxBlockRecords && matchesSealedContents(lineage, input.Bytes, sealed) {
		sum := sha256.Sum256(input.Bytes)
		hash := hex.EncodeToString(sum[:])
		if input.Hash == "" || input.Hash == hash {
			objects := make([]portable.EncodedEnvelope, len(sealed.objects))
			for i, object := range sealed.objects {
				objects[i] = copyEnvelope(object)
			}
			syncwork.RecordWork(ctx, "block_wire_rechecks_reused", 1)
			return Block{Hash: hash, Bytes: bytes.Clone(input.Bytes), Objects: objects, decoded: sealed}, nil
		}
	}
	syncwork.RecordWork(ctx, "block_wire_rechecks_decoded", 1)
	return DecodeBlock(lineage, input.Hash, input.Bytes)
}

// EncodeBlock validates envelopes before publication. Sorting by canonical
// bytes makes the result independent of input order while preserving competing
// envelopes for the same record identity.
func EncodeBlock(lineage string, objects []portable.EncodedEnvelope) (Block, error) {
	if len(objects) == 0 || len(objects) > MaxBlockRecords {
		return Block{}, fmt.Errorf("invalid block record count")
	}
	lines := make([][]byte, 0, len(objects))
	for _, object := range objects {
		decoded, err := portable.DecodeTransportEnvelope(object.Bytes)
		if err != nil {
			return Block{}, fmt.Errorf("invalid block envelope: %w", err)
		}
		if decoded.Envelope.LineageID != lineage {
			return Block{}, fmt.Errorf("block lineage mismatch")
		}
		lines = append(lines, decoded.Bytes)
	}
	sort.Slice(lines, func(i, j int) bool { return bytes.Compare(lines[i], lines[j]) < 0 })
	var body bytes.Buffer
	body.WriteString(blockHeader)
	for i, line := range lines {
		if i > 0 && bytes.Equal(lines[i-1], line) {
			return Block{}, fmt.Errorf("duplicate block envelope")
		}
		if body.Len()+len(line)+1 > MaxBlockBytes {
			return Block{}, fmt.Errorf("block exceeds size limit")
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	return DecodeBlock(lineage, "", body.Bytes())
}

// DecodeBlock accepts only complete canonical blocks. expectedHash, when
// provided, binds the bytes to their content-addressed location.
// Its private receipt retains pure wire validation, never database authority.
func DecodeBlock(lineage, expectedHash string, body []byte) (Block, error) {
	if len(body) > MaxBlockBytes || !bytes.HasPrefix(body, []byte(blockHeader)) || len(body) <= len(blockHeader) || body[len(body)-1] != '\n' {
		return Block{}, fmt.Errorf("invalid or incomplete history block")
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if expectedHash != "" && expectedHash != hash {
		return Block{}, fmt.Errorf("history block hash mismatch")
	}
	lines := bytes.Split(body[len(blockHeader):len(body)-1], []byte{'\n'})
	if len(lines) == 0 || len(lines) > MaxBlockRecords {
		return Block{}, fmt.Errorf("invalid block record count")
	}
	result := Block{Hash: hash, Bytes: append([]byte(nil), body...)}
	var err error
	result.Objects, err = decodeBlockEnvelopes(lineage, lines, min(4, runtime.GOMAXPROCS(0)))
	if err != nil {
		return Block{}, err
	}
	result.decoded = sealBlockContents(lineage, result.Objects)
	return result, nil
}

// Lines are independent immutable inputs. Keep output and errors in wire order
// even when validation runs concurrently. Small blocks stay sequential; a block
// remains bounded by one MiB and 512 records, with no persistent worker or cache.
func decodeBlockEnvelopes(lineage string, lines [][]byte, workers int) ([]portable.EncodedEnvelope, error) {
	objects := make([]portable.EncodedEnvelope, len(lines))
	decode := func(i int) error {
		if i > 0 && bytes.Compare(lines[i-1], lines[i]) >= 0 {
			return fmt.Errorf("block envelopes are not strictly ordered")
		}
		object, err := portable.DecodeTransportEnvelope(lines[i])
		if err != nil {
			return fmt.Errorf("invalid block envelope: %w", err)
		}
		if object.Envelope.LineageID != lineage {
			return fmt.Errorf("block lineage mismatch")
		}
		objects[i] = object
		return nil
	}
	workers = min(workers, 4)
	if len(lines) < 32 || workers < 2 {
		for i := range lines {
			if err := decode(i); err != nil {
				return nil, err
			}
		}
		return objects, nil
	}
	jobs := make(chan int, len(lines))
	for i := range lines {
		jobs <- i
	}
	close(jobs)
	errors := make([]error, len(lines))
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for i := range jobs {
				errors[i] = decode(i)
			}
		}()
	}
	group.Wait()
	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}
	return objects, nil
}
