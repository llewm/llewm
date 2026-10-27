package recordsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
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
	Hash    string
	Bytes   []byte
	Objects []portable.EncodedEnvelope
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
	for i, line := range lines {
		if i > 0 && bytes.Compare(lines[i-1], line) >= 0 {
			return Block{}, fmt.Errorf("block envelopes are not strictly ordered")
		}
		object, err := portable.DecodeTransportEnvelope(line)
		if err != nil {
			return Block{}, fmt.Errorf("invalid block envelope: %w", err)
		}
		if object.Envelope.LineageID != lineage {
			return Block{}, fmt.Errorf("block lineage mismatch")
		}
		result.Objects = append(result.Objects, object)
	}
	return result, nil
}
