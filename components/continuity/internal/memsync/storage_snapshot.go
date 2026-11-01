package memsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const maxPrivateGitConfig = 256 << 10
const bareStorageOwner = "continuity-storage-owner"

type cloneStoragePaths struct{ Root, Stage, Retained, Receipt string }

type cloneStorageReceipt struct {
	Version                                             int
	PathsSHA256, RemoteSHA256, RefsSHA256, ConfigSHA256 string
	StageName, RetainedName, ObjectFormat               string
	RefCount                                            int
}

type cloneStorageSeal struct {
	Receipt  cloneStorageReceipt
	Checksum string
}

func receiptForClone(p cloneStoragePaths, remote string, s cloneSnapshot) cloneStorageReceipt {
	return cloneStorageReceipt{Version: 1, PathsSHA256: p.hash(), RemoteSHA256: recordsync.Hash(remote), RefsSHA256: s.refsHash(), ConfigSHA256: recordsync.Hash(string(s.Config)), StageName: filepath.Base(p.Stage), RetainedName: filepath.Base(p.Retained), ObjectFormat: s.ObjectFormat, RefCount: len(s.Refs)}
}

func (r cloneStorageReceipt) bytes() []byte { body, _ := json.Marshal(r); return body }
func (r cloneStorageReceipt) sealed() []byte {
	body, _ := json.Marshal(cloneStorageSeal{Receipt: r, Checksum: recordsync.Hash(string(r.bytes()))})
	return body
}
func (r cloneStorageReceipt) done() []byte {
	return []byte("llewm-bare-storage-v1\n" + recordsync.Hash(string(r.sealed())) + "\n")
}

func (p cloneStoragePaths) normalized() (cloneStoragePaths, error) {
	seen := map[string]bool{}
	for _, value := range []*string{&p.Root, &p.Stage, &p.Retained, &p.Receipt} {
		if strings.TrimSpace(*value) == "" || strings.ContainsRune(*value, 0) {
			return p, fmt.Errorf("explicit clone storage paths required")
		}
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return p, err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return p, err
		}
		name := filepath.Base(absolute)
		if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return p, fmt.Errorf("invalid clone storage path")
		}
		*value = filepath.Join(parent, name)
		if seen[*value] {
			return p, fmt.Errorf("clone storage paths must be distinct")
		}
		seen[*value] = true
	}
	for _, value := range []string{p.Stage, p.Retained, p.Receipt} {
		if filepath.Dir(value) != filepath.Dir(p.Root) {
			return p, fmt.Errorf("clone storage paths must share one parent")
		}
	}
	return p, nil
}
func (p cloneStoragePaths) hash() string {
	return recordsync.Hash("clone-storage-paths-v1\x00" + p.Root + "\x00" + p.Stage + "\x00" + p.Retained + "\x00" + p.Receipt)
}

func decodeCloneStorage(body []byte, p cloneStoragePaths, remote string) (cloneStorageReceipt, error) {
	var seal cloneStorageSeal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if len(body) > 4096 || decoder.Decode(&seal) != nil {
		return seal.Receipt, fmt.Errorf("invalid bare storage receipt")
	}
	r := seal.Receipt
	if r.Version != 1 || !validDigest(r.PathsSHA256) || !validDigest(r.RemoteSHA256) || !validDigest(r.RefsSHA256) || !validDigest(r.ConfigSHA256) || r.RefCount < 0 || r.RefCount > recordsync.MaxScanEntries || r.PathsSHA256 != p.hash() || r.RemoteSHA256 != recordsync.Hash(remote) || r.StageName != filepath.Base(p.Stage) || r.RetainedName != filepath.Base(p.Retained) || (r.ObjectFormat != "sha1" && r.ObjectFormat != "sha256") || seal.Checksum != recordsync.Hash(string(r.bytes())) || !bytes.Equal(body, r.sealed()) {
		return r, fmt.Errorf("bare storage receipt differs from selected source or destination")
	}
	return r, nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

type cloneRef struct{ Name, OID, Symbolic string }
type cloneConfig struct{ Key, Value string }
type cloneSnapshot struct {
	Head, ObjectFormat string
	Refs               []cloneRef
	Config             []byte
	Settings           []cloneConfig
}

func (s cloneSnapshot) refsHash() string {
	// Git permits non-UTF8 names on Unix. Frame exact bytes rather than JSON
	// encoding, which would replace invalid UTF8 and collapse distinct names.
	var b bytes.Buffer
	b.WriteString("clone-refs-v1\x00" + s.Head + "\x00")
	for _, ref := range s.Refs {
		b.WriteString(ref.Name + "\x00" + ref.OID + "\x00" + ref.Symbolic + "\x00")
	}
	return recordsync.Hash(b.String())
}

func readGitBytes(ctx context.Context, repo git, limit int, args ...string) ([]byte, error) {
	var output = boundedObjectBuffer{limit: limit}
	if err := repo.execOut(ctx, nil, &output, args); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func snapshotClone(ctx context.Context, repo git, remote string, working bool) (cloneSnapshot, error) {
	var snapshot cloneSnapshot
	root, err := recordsync.OpenRoot(repo.Dir)
	if err != nil {
		return snapshot, err
	}
	root.Close()
	bare, err := repo.run(ctx, "rev-parse", "--is-bare-repository")
	if err != nil {
		return snapshot, err
	}
	if bare != fmt.Sprint(!working) {
		return snapshot, fmt.Errorf("unexpected clone storage mode")
	}
	configDir := repo.Dir
	if working {
		configDir = filepath.Join(repo.Dir, ".git")
		gitRoot, err := recordsync.OpenRoot(configDir)
		if err != nil {
			return snapshot, err
		}
		gitRoot.Close()
		top, err := repo.run(ctx, "rev-parse", "--show-toplevel")
		if err != nil {
			return snapshot, err
		}
		actual, actualErr := os.Stat(top)
		selected, selectedErr := os.Stat(repo.Dir)
		if actualErr != nil || selectedErr != nil || !os.SameFile(actual, selected) {
			return snapshot, fmt.Errorf("clone worktree %q differs from selected source %q", redact.String(top), redact.String(repo.Dir))
		}
		dirty, err := readGitBytes(ctx, repo, maxCachedTreeBytes, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
		if err != nil {
			return snapshot, err
		}
		if len(dirty) != 0 {
			return snapshot, fmt.Errorf("memory clone has pending work; preserve it and recover through supported sync before storage migration")
		}
		shallow, err := repo.run(ctx, "rev-parse", "--is-shallow-repository")
		if err != nil || shallow != "false" {
			return snapshot, fmt.Errorf("storage migration requires a complete clone")
		}
	}
	configRoot, err := recordsync.OpenRoot(configDir)
	if err != nil {
		return snapshot, err
	}
	snapshot.Config, err = recordsync.ReadFile(configRoot, "config", maxPrivateGitConfig)
	configRoot.Close()
	if err != nil {
		return snapshot, err
	}
	settings, err := readGitBytes(ctx, repo, maxCachedTreeBytes, "config", "--local", "--null", "--no-includes", "--list")
	if err != nil {
		return snapshot, err
	}
	for _, record := range bytes.Split(settings, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		key, value, ok := bytes.Cut(record, []byte{'\n'})
		if !ok {
			return snapshot, fmt.Errorf("invalid private Git configuration record")
		}
		name := strings.ToLower(string(key))
		if strings.HasPrefix(name, "include.") || strings.HasPrefix(name, "includeif.") || name == "extensions.partialclone" || (strings.HasSuffix(name, ".promisor") && strings.HasPrefix(name, "remote.")) || (name == "extensions.worktreeconfig" && !falseGitBoolean(string(value))) {
			return snapshot, fmt.Errorf("custom included, partial or linked-worktree configuration requires explicit recovery before storage migration")
		}
		snapshot.Settings = append(snapshot.Settings, cloneConfig{Key: string(key), Value: string(value)})
	}
	if err := verifyRemote(ctx, repo, remote); err != nil {
		return snapshot, err
	}
	head, err := readGitBytes(ctx, repo, 4096, "symbolic-ref", "--no-recurse", "HEAD")
	if err != nil {
		return snapshot, fmt.Errorf("storage migration requires an attached history branch: %w", err)
	}
	snapshot.Head = string(bytes.TrimSuffix(head, []byte{'\n'}))
	if !strings.HasPrefix(snapshot.Head, "refs/heads/") {
		return snapshot, fmt.Errorf("invalid memory history branch")
	}
	snapshot.ObjectFormat, err = repo.run(ctx, "rev-parse", "--show-object-format")
	if err != nil || (snapshot.ObjectFormat != "sha1" && snapshot.ObjectFormat != "sha256") {
		return snapshot, fmt.Errorf("unsupported Git object format")
	}
	listing, err := readGitBytes(ctx, repo, maxCachedTreeBytes, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)")
	if err != nil {
		return snapshot, err
	}
	for _, line := range bytes.Split(listing, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 3 || !strings.HasPrefix(string(parts[0]), "refs/") || !validGitOID(string(parts[1])) || len(snapshot.Refs) == recordsync.MaxScanEntries {
			return snapshot, fmt.Errorf("invalid or excessive clone ref inventory")
		}
		if len(parts[2]) != 0 && !strings.HasPrefix(string(parts[2]), "refs/") {
			return snapshot, fmt.Errorf("invalid symbolic clone ref")
		}
		if len(parts[2]) != 0 {
			// Ref-filter may resolve an alias chain. Preserve its immediate
			// link so a later target change retains the original semantics.
			target, err := readGitBytes(ctx, repo, 4096, "symbolic-ref", "--no-recurse", string(parts[0]))
			if err != nil {
				return snapshot, err
			}
			parts[2] = bytes.TrimSuffix(target, []byte{'\n'})
			if !bytes.HasPrefix(parts[2], []byte("refs/")) {
				return snapshot, fmt.Errorf("invalid symbolic clone ref")
			}
		}
		snapshot.Refs = append(snapshot.Refs, cloneRef{Name: string(parts[0]), OID: string(parts[1]), Symbolic: string(parts[2])})
	}
	sort.Slice(snapshot.Refs, func(i, j int) bool { return snapshot.Refs[i].Name < snapshot.Refs[j].Name })
	if err := verifyListedLooseRefs(configDir, snapshot.Refs); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// Ref-filter omits dangling symbolic refs. Refuse these and abandoned ref
// locks rather than silently losing them during the all-refs fetch. Packed
// refs cannot contain symbolic refs; their resolved entries are listed above.
func verifyListedLooseRefs(directory string, refs []cloneRef) error {
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	listed := make(map[string]bool, len(refs))
	for _, ref := range refs {
		listed[ref.Name] = true
	}
	count := 0
	return fs.WalkDir(root.FS(), "refs", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > recordsync.MaxScanEntries {
			return fmt.Errorf("loose ref inventory exceeds bounds")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !listed[path] {
			return fmt.Errorf("unlisted or unsupported loose Git ref requires recovery before storage migration")
		}
		return nil
	})
}

func falseGitBoolean(value string) bool {
	switch strings.ToLower(value) {
	case "false", "no", "off", "0":
		return true
	}
	return false
}

func equivalentPrivateConfig(source, staged []cloneConfig) bool {
	filtered := func(input []cloneConfig) string {
		var entries []cloneConfig
		for _, entry := range input {
			key := strings.ToLower(entry.Key)
			if key != "core.bare" && key != "core.worktree" {
				entries = append(entries, entry)
			}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
		var b bytes.Buffer
		for _, entry := range entries {
			b.WriteString(entry.Key + "\x00" + entry.Value + "\x00")
		}
		return b.String()
	}
	return filtered(source) == filtered(staged)
}

func durableGitAvailable(ctx context.Context, repo git) error {
	listing, err := readGitBytes(ctx, repo, 256<<10, "help", "--config")
	if err != nil {
		return err
	}
	keys := map[string]bool{}
	for _, name := range strings.Fields(string(listing)) {
		keys[strings.ToLower(name)] = true
	}
	if !keys["core.fsync"] || !keys["core.fsyncmethod"] {
		return fmt.Errorf("storage migration requires Git with durable object/reference fsync support")
	}
	return nil
}

func sourceMatchesReceipt(snapshot cloneSnapshot, r cloneStorageReceipt) bool {
	return snapshot.refsHash() == r.RefsSHA256 && recordsync.Hash(string(snapshot.Config)) == r.ConfigSHA256 && snapshot.ObjectFormat == r.ObjectFormat && len(snapshot.Refs) == r.RefCount
}

func syncCloneParent(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
