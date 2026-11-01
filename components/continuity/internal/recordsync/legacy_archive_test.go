package recordsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func (a legacyArchiveFixture) SaveStatus(endpoint string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	_, err = WriteFile(root, filepath.Join("status", Hash(endpoint)+".json"), body, true)
	return err
}

// Only migration fixtures can write the retired local loose-file format.
type legacyArchiveFixture struct{ Directory, Lineage string }

func openLegacyArchiveFixture(directory, lineage string) (legacyArchiveFixture, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return legacyArchiveFixture{}, err
	}
	root, err := OpenRoot(directory)
	if err != nil {
		return legacyArchiveFixture{}, err
	}
	defer root.Close()
	_, err = WriteFile(root, "lineage", []byte(lineage+"\n"), false)
	return legacyArchiveFixture{Directory: directory, Lineage: lineage}, err
}

func (a legacyArchiveFixture) Put(object portable.EncodedEnvelope, origin string) (bool, error) {
	decoded, err := portable.DecodeTransportEnvelope(object.Bytes)
	if err != nil || decoded.Envelope.LineageID != a.Lineage {
		return false, fmt.Errorf("invalid legacy fixture envelope")
	}
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if _, err := WriteFile(root, filepath.Join("origins", decoded.Envelope.EnvelopeSHA256, Hash(origin)), []byte("observed\n"), false); err != nil {
		return false, err
	}
	return WriteFile(root, portable.MemoryRecordPath(decoded.Envelope), portable.MemoryFileBytes(decoded), false)
}
