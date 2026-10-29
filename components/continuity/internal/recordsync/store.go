// Package recordsync owns immutable transport records and shared import semantics.
// Transport storage is distinct from the operational SQLite database.
package recordsync

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

const MaxObjectBytes = portable.MaxPayloadBytes + portable.MaxIndexBytes + (64 << 10)
const MaxScanEntries = portable.MaxRecordCount * 5

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var lineagePattern = regexp.MustCompile(`^lineage:[0-9a-f]{32}$`)
var recordTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func NewID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

// OpenRoot refuses a symlinked root. All accesses below it use os.Root so a
// racing remote symlink cannot redirect file I/O outside the selected tree.
func OpenRoot(directory string) (*os.Root, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("transport root must be a real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	handle, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	actual, statErr := handle.Stat()
	handle.Close()
	if statErr != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, fmt.Errorf("transport root changed while opening")
	}
	return root, nil
}

func ReadFile(root *os.Root, path string, limit int) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, fmt.Errorf("transport file must be regular and bounded")
	}
	f, err := openRead(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, fmt.Errorf("transport file changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("transport file exceeds bounds")
	}
	return body, nil
}

// WriteFile durably stages bytes before publication. Immutable publication uses
// an exclusive hard link and never replaces an existing file. A filesystem that
// cannot provide this operation fails closed instead of weakening publication.
func WriteFile(root *os.Root, path string, body []byte, replace bool) (bool, error) {
	return writeFile(root, path, body, replace, nil)
}

func writeFile(root *os.Root, path string, body []byte, replace bool, beforeCreate func()) (bool, error) {
	if !filepath.IsLocal(path) {
		return false, fmt.Errorf("unsafe transport path")
	}
	parent := filepath.Dir(path)
	if !replace {
		existing, err := ReadFile(root, path, len(body))
		if err == nil {
			if !bytes.Equal(existing, body) {
				return false, fmt.Errorf("immutable transport file differs")
			}
			return false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	// Existing immutable files need no directory mutation. Keep the bounded,
	// rooted byte comparison above, including errors for damaged files.
	if beforeCreate != nil {
		beforeCreate()
	}
	if err := root.MkdirAll(parent, 0700); err != nil {
		return false, err
	}
	id, err := NewID()
	if err != nil {
		return false, err
	}
	temporary := filepath.Join(parent, ".llewm-stage-"+id)
	f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	defer root.Remove(temporary)
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if replace {
		err = root.Rename(temporary, path)
	} else {
		err = root.Link(temporary, path)
	}
	if errors.Is(err, os.ErrExist) && !replace {
		existing, readErr := ReadFile(root, path, len(body))
		if readErr != nil {
			return false, readErr
		}
		if !bytes.Equal(existing, body) {
			return false, fmt.Errorf("concurrent immutable file differs")
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	directory, err := root.Open(parent)
	if err != nil {
		return true, err
	}
	defer directory.Close()
	return true, directory.Sync()
}

func ReadObject(root *os.Root, path, lineage string) (portable.EncodedEnvelope, error) {
	body, err := ReadFile(root, path, MaxObjectBytes)
	if err != nil {
		return portable.EncodedEnvelope{}, err
	}
	e, err := portable.DecodeTransportEnvelope(body)
	if err != nil {
		return e, err
	}
	if e.Envelope.LineageID != lineage || filepath.Clean(path) != portable.MemoryRecordPath(e.Envelope) {
		return e, fmt.Errorf("transport object has wrong lineage or path")
	}
	return e, nil
}

type ScanResult struct {
	Objects []portable.EncodedEnvelope
	Pending int
	Invalid int
}

// Scan ignores unrelated provider metadata but reports every noncanonical entry
// inside the selected lineage record tree. Other lineage namespaces are never scanned. Partial/missing/corrupt files never reach the importer.
// No mtime or shared cursor is used. The caller decides whether a scan is usable.
func Scan(ctx context.Context, root *os.Root, lineage string) (ScanResult, error) {
	return scanWithKnown(ctx, root, lineage, nil)
}

func scanWithKnown(ctx context.Context, root *os.Root, lineage string, known map[string]portable.EncodedEnvelope) (ScanResult, error) {
	var result ScanResult
	if !lineagePattern.MatchString(lineage) {
		return result, fmt.Errorf("invalid scan lineage")
	}
	base := portable.MemoryLineageDir(lineage)
	if _, err := root.Lstat(base); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	count, total := 0, 0
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(path)
		if err != nil {
			result.Pending++
			return nil
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			result.Invalid++
			return nil
		}
		d, err := openRead(root, path)
		if err != nil {
			result.Pending++
			return nil
		}
		defer d.Close()
		for {
			entries, readErr := d.ReadDir(256)
			for _, entry := range entries {
				count++
				if count > MaxScanEntries {
					return fmt.Errorf("transport scan entry limit exceeded")
				}
				name := entry.Name()
				if strings.HasPrefix(name, ".llewm-stage-") {
					continue
				}
				child := filepath.Join(path, name)
				if depth < 2 {
					valid := recordTypePattern.MatchString(name)
					if depth == 1 {
						valid = hex64.MatchString(name)
					}
					if !valid || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
						result.Invalid++
						continue
					}
					if err := walk(child, depth+1); err != nil {
						return err
					}
					continue
				}
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(name, ".json") || !hex64.MatchString(strings.TrimSuffix(name, ".json")) {
					result.Invalid++
					continue
				}
				var e portable.EncodedEnvelope
				var err error
				if cached, ok := known[child]; ok {
					body, readErr := ReadFile(root, child, MaxObjectBytes)
					if readErr == nil && bytes.Equal(body, portable.MemoryFileBytes(cached)) {
						e = copyEnvelope(cached)
					} else {
						e, err = ReadObject(root, child, lineage)
					}
				} else {
					e, err = ReadObject(root, child, lineage)
				}
				if err != nil {
					result.Pending++
					continue
				}
				total += len(e.Bytes)
				if len(result.Objects) >= portable.MaxRecordCount || total > portable.MaxRecordsBytes {
					return fmt.Errorf("transport record count/byte limit exceeded")
				}
				result.Objects = append(result.Objects, e)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		return nil
	}
	err := walk(base, 0)
	sort.Slice(result.Objects, func(i, j int) bool {
		return portable.MemoryRecordPath(result.Objects[i].Envelope) < portable.MemoryRecordPath(result.Objects[j].Envelope)
	})
	return result, err
}

type Archive struct {
	Directory string
	Lineage   string
}

func OpenArchive(directory, lineage string) (Archive, error) {
	// Only a local archive is created automatically. Endpoint roots never are.
	if !lineagePattern.MatchString(lineage) {
		return Archive{}, fmt.Errorf("invalid archive lineage")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return Archive{}, err
	}
	root, err := OpenRoot(directory)
	if err != nil {
		return Archive{}, err
	}
	defer root.Close()
	_, err = WriteFile(root, "lineage", []byte(lineage+"\n"), false)
	return Archive{directory, lineage}, err
}
func (a Archive) Put(e portable.EncodedEnvelope, origin string) (bool, error) {
	validated, err := portable.DecodeTransportEnvelope(e.Bytes)
	if err != nil {
		return false, err
	}
	if validated.Envelope.LineageID != a.Lineage {
		return false, fmt.Errorf("archive lineage mismatch")
	}
	return a.putValidated(validated, origin)
}

// putValidated is used only after the caller has decoded and checked the
// envelope's exact bytes and lineage, including through DecodeBlock.
func (a Archive) putValidated(validated portable.EncodedEnvelope, origin string) (bool, error) {
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return false, err
	}
	defer root.Close()
	return putValidatedRoot(root, validated, origin)
}

func putValidatedRoot(root *os.Root, validated portable.EncodedEnvelope, origin string) (bool, error) {
	return putValidatedRootObserved(root, validated, origin, nil)
}

func putValidatedRootObserved(root *os.Root, validated portable.EncodedEnvelope, origin string, beforeCreate func()) (bool, error) {
	// Record provenance first, so a crash cannot turn a received object into a
	// locally originated object during the next operational export.
	marker := filepath.Join("origins", validated.Envelope.EnvelopeSHA256, Hash(origin))
	if _, err := writeFile(root, marker, []byte("observed\n"), false, beforeCreate); err != nil {
		return false, err
	}
	return writeFile(root, portable.MemoryRecordPath(validated.Envelope), portable.MemoryFileBytes(validated), false, beforeCreate)
}
func (a Archive) CaptureLocal(envelopes []portable.EncodedEnvelope) error {
	return a.captureLocal(envelopes, nil)
}

func (a Archive) captureLocal(envelopes []portable.EncodedEnvelope, beforeCreate func()) error {
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, e := range envelopes {
		_, err := root.Lstat(filepath.Join("origins", e.Envelope.EnvelopeSHA256))
		if errors.Is(err, os.ErrNotExist) {
			validated, err := portable.DecodeTransportEnvelope(e.Bytes)
			if err != nil {
				return err
			}
			if validated.Envelope.LineageID != a.Lineage {
				return fmt.Errorf("archive lineage mismatch")
			}
			if _, err := putValidatedRootObserved(root, validated, "local", beforeCreate); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if _, err := writeFile(root, portable.MemoryRecordPath(e.Envelope), portable.MemoryFileBytes(e), false, beforeCreate); err != nil {
				return err
			}
		}
	}
	return nil
}
func (a Archive) Objects(ctx context.Context, endpoint string, relay bool) ([]portable.EncodedEnvelope, error) {
	allowed, _, err := a.objects(ctx, endpoint, relay)
	return allowed, err
}

// objects returns both destination-authorized objects and the complete
// validated snapshot. Both views come from the same archive scan.
func (a Archive) objects(ctx context.Context, endpoint string, relay bool) ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
	return a.objectsWithBlocks(ctx, endpoint, relay, nil)
}

func (a Archive) objectsWithBlocks(ctx context.Context, endpoint string, relay bool, blocks []Block) ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	known := make(map[string]portable.EncodedEnvelope)
	knownCount, knownBytes := 0, 0
	for _, block := range blocks {
		if block.validated == nil || block.validated.lineage != a.Lineage {
			continue
		}
		for _, object := range block.validated.objects {
			knownCount++
			knownBytes += len(object.Bytes)
			if knownCount > portable.MaxRecordCount || knownBytes > portable.MaxRecordsBytes {
				return nil, nil, fmt.Errorf("validated archive hints exceed bounds")
			}
			known[portable.MemoryRecordPath(object.Envelope)] = object
		}
	}
	scan, err := scanWithKnown(ctx, root, a.Lineage, known)
	if err != nil {
		return nil, nil, err
	}
	if scan.Pending+scan.Invalid > 0 {
		return nil, nil, fmt.Errorf("local archive contains incomplete or invalid records")
	}
	if relay {
		return scan.Objects, scan.Objects, nil
	}
	var result []portable.EncodedEnvelope
	for _, e := range scan.Objects {
		allowed := false
		for _, origin := range []string{"local", endpoint} {
			if _, err := root.Lstat(filepath.Join("origins", e.Envelope.EnvelopeSHA256, Hash(origin))); err == nil {
				allowed = true
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, nil, err
			}
		}
		if allowed {
			result = append(result, e)
		}
	}
	return result, scan.Objects, nil
}
func (a Archive) SaveStatus(endpoint string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > 64<<10 {
		return fmt.Errorf("local endpoint status exceeds bounds")
	}
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	_, err = WriteFile(root, filepath.Join("status", Hash(endpoint)+".json"), body, true)
	return err
}
func (a Archive) Status(endpoint string, value any) error {
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	body, err := ReadFile(root, filepath.Join("status", Hash(endpoint)+".json"), 64<<10)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, value)
}
