package recordsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// Independent sequential codec from before bounded parallel validation.
func referenceDecodeBlock(lineage, expectedHash string, body []byte) (Block, error) {
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
