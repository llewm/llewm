package config

import (
	"github.com/llewm/llewm/components/continuity/internal/adoption"

	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/usage"
)

const FileName = "config.toml"

type Config struct {
	PlanPolicy         trackplan.Policy   `toml:"plan_policy,omitempty"`
	SchemaVersion      int                `toml:"schema_version"`
	Mode               string             `toml:"mode"`
	Project            Project            `toml:"project"`
	Storage            Storage            `toml:"storage"`
	Backups            Backups            `toml:"backups"`
	Privacy            Privacy            `toml:"privacy"`
	Bootstrap          Bootstrap          `toml:"bootstrap"`
	CheckpointReminder CheckpointReminder `toml:"checkpoint_reminder"`
	RoadmapScan        RoadmapScan        `toml:"roadmap_scan"`
	Sync               Sync               `toml:"sync,omitempty"`
	Leases             Leases             `toml:"leases,omitempty"`
	Usage              Usage              `toml:"usage,omitempty"`
	Stats              Stats              `toml:"stats,omitempty"`
	Guidance           Guidance           `toml:"guidance,omitempty"`
}

// Guidance configures Continuity's built-in bootstrap rules.
type Guidance struct {
	Core CoreRules `toml:"core,omitempty"`
}

// CoreRules disables built-in rules by ID, or replaces their text.
type CoreRules struct {
	Disable  []string          `toml:"disable,omitempty"`
	Override map[string]string `toml:"override,omitempty"`
}

// GlobalFileName is the engineer-wide configuration in the global directory.
const GlobalFileName = "config.toml"

// LoadGlobalGuidance reads [guidance] from the engineer-wide configuration,
// or returns none when that file does not exist.
func LoadGlobalGuidance(globalRoot string) (Guidance, error) {
	var global struct {
		Guidance Guidance `toml:"guidance"`
	}
	path := filepath.Join(globalRoot, GlobalFileName)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Guidance{}, nil
	}
	if _, err := toml.DecodeFile(path, &global); err != nil {
		return Guidance{}, err
	}
	return global.Guidance, nil
}

// Usage configures usage statistics. Prices sets per-model rates in US
// dollars per million tokens ([usage.prices."MODEL"] input, cache_write,
// cache_write_long, cache_read, output, source, as_of, and basis), overriding
// the built-in table and pricing models it lacks.
type Usage struct {
	Prices map[string]usage.Price `toml:"prices,omitempty"`
}

// Stats configures `llewm stats code`. Exclude adds path patterns to the
// built-in generated, vendored, and lock file list: a pattern ending in / is a
// directory anywhere in a path; others match the path or its base name.
type Stats struct {
	Exclude []string `toml:"exclude,omitempty"`
}

// Leases is the per-scope fencing policy. A scope matching a Required pattern
// (task:ID, roadmap_node:ID, or track_plan:ID, with * for every ID of a kind)
// refuses checkpoint and plan writes made without its current lease; every
// other scope is advisory and marks such writes unleased.
//
// Remote shares leases across machines through the [sync] memory repository
// (the Coordination tier). Skew is the takeover grace after another
// machine's lease expires, as a Go duration such as "45s"; empty means 30s.
type Leases struct {
	Required []string `toml:"required,omitempty"`
	Remote   bool     `toml:"remote,omitempty"`
	Skew     string   `toml:"skew,omitempty"`
}

// Sync names the memory repository this project's curated history syncs with.
// Clone is the local working copy; empty means the shared default for Remote.
type Sync struct {
	Remote string `toml:"remote,omitempty"`
	Clone  string `toml:"clone,omitempty"`
	// Manual turns off automatic syncs at session start and after checkpoints.
	Manual bool `toml:"manual,omitempty"`
	// SessionTimeoutSeconds bounds how long the session-start sync waits on the
	// remote; 0 means 8 seconds. See SessionTimeout for the upper limit.
	SessionTimeoutSeconds int `toml:"session_timeout_seconds,omitempty"`
	// Askpass is a fallback program Git runs for remote credentials when no
	// credential helper, GIT_ASKPASS, or core.askPass supplies them.
	Askpass string `toml:"askpass,omitempty"`
	// SSHKey is a private key used only for this project's memory syncs over SSH.
	SSHKey string `toml:"ssh_key,omitempty"`
}

// DefaultMemoryClone returns the shared working copy for remote under the
// user's data directory, so every project using one remote shares one clone.
func DefaultMemoryClone(remote string) (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	sum := sha256.Sum256([]byte(remote))
	// The repository name follows the last "/" or, for scp-like remotes, ":".
	trimmed := strings.TrimRight(remote, "/")
	name := strings.TrimSuffix(trimmed[strings.LastIndexAny(trimmed, "/:")+1:], ".git")
	name = unsafeCloneName.ReplaceAllString(name, "-")
	if name == "" || name == "." || name == ".." {
		name = "memory"
	}
	basename := name + "-" + hex.EncodeToString(sum[:4])
	legacy := filepath.Join(base, "llmem", "memory", basename)
	if _, err := os.Stat(legacy); err == nil {
		return "", fmt.Errorf("existing memory clone at %s: retain it with explicit [sync] clone configuration before upgrading; relocation must be performed through Continuity with all writers stopped", legacy)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect previous memory clone: %w", err)
	}
	return filepath.Join(base, "llewm", "memory", basename), nil
}

var unsafeCloneName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// MemoryClone is the configured clone path, or the default for the remote, or
// "" when sync is not configured.
func (s Sync) MemoryClone() (string, error) {
	if s.Remote == "" {
		return "", nil
	}
	if s.Clone != "" {
		return s.Clone, nil
	}
	return DefaultMemoryClone(s.Remote)
}

// Automatic reports whether syncs run on their own.
func (s Sync) Automatic() bool { return s.Remote != "" && !s.Manual }

// MaxSessionTimeout caps the session-start sync well inside the plugins' 15 s
// SessionStart hook limit, so the bootstrap is still built after a slow sync.
const MaxSessionTimeout = 10 * time.Second

// DefaultSessionTimeout leaves room for a fetch over a slow link. An offline
// remote costs only the connection probe, not this whole budget.
const DefaultSessionTimeout = 8 * time.Second

// SessionTimeout is how long the session-start sync waits on the remote:
// DefaultSessionTimeout unless set, at most MaxSessionTimeout.
func (s Sync) SessionTimeout() time.Duration {
	timeout := time.Duration(s.SessionTimeoutSeconds) * time.Second
	if timeout <= 0 {
		return DefaultSessionTimeout
	}
	return min(timeout, MaxSessionTimeout)
}

type Project struct {
	Name string `toml:"name"`
	Root string `toml:"root,omitempty"`
}

type Storage struct {
	Database string `toml:"database"`
}

type Backups struct {
	Dir      string   `toml:"dir"`
	KeepLast int      `toml:"keep_last"`
	KeepDays int      `toml:"keep_days"`
	Pinned   []string `toml:"pinned,omitempty"`
}

type Privacy struct {
	StoreRawPayloads bool `toml:"store_raw_payloads"`
	StoreRawOutput   bool `toml:"store_raw_output"`
}

type Bootstrap struct {
	MaxBytes int `toml:"max_bytes"`
}

type CheckpointReminder struct {
	RemindAfterSeconds int64 `toml:"remind_after_seconds"`
	RemindAfterEvents  int   `toml:"remind_after_events"`
}

type RoadmapScan struct {
	OnSessionStart bool `toml:"on_session_start"`
}

func Default(root string) Config {
	return Config{
		SchemaVersion: 1,
		Mode:          "full",
		Project:       Project{Name: filepath.Base(root), Root: root},
		Storage:       Storage{Database: filepath.Join(StateDir(root), "continuity.db")},
		Backups:       Backups{Dir: filepath.Join(StateDir(root), "backups"), KeepLast: 3, KeepDays: 30},
		Bootstrap:     Bootstrap{MaxBytes: 16_384},
	}
}

func StateDir(root string) string {
	if value := os.Getenv("CONTINUITY_HOME"); value != "" {
		return value
	}
	return filepath.Join(root, ".llewm", "continuity")
}

func Load(root string) (Config, error) {
	if os.Getenv("CONTINUITY_HOME") == "" {
		if _, err := os.Lstat(filepath.Join(root, ".continuity")); err == nil {
			return Config{}, fmt.Errorf("legacy .continuity state exists; stop old writers, back up and run llewm project migrate-layout (no automatic fallback or initialization)")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
		if _, err := os.Lstat(filepath.Join(root, ".llewm", "layout.lock")); err == nil {
			return Config{}, fmt.Errorf("layout operation pending; inspect .llewm/layout.lock and use project restore-layout with its backup receipt")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
		if body, err := os.ReadFile(filepath.Join(root, ".llewm", "project.toml")); err == nil && string(body) != "layout_version = 1\n" {
			return Config{}, fmt.Errorf("unknown .llewm project layout")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
	}

	path := filepath.Join(StateDir(root), FileName)
	cfg := Default(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Mode != "full" && cfg.Mode != "audit-only" {
		return Config{}, fmt.Errorf("config mode %q must be full or audit-only", cfg.Mode)
	}
	if !filepath.IsAbs(cfg.Storage.Database) {
		cfg.Storage.Database = filepath.Join(root, cfg.Storage.Database)
	} else if moved := cfg.Project.Root; moved != "" && moved != root && os.Getenv("CONTINUITY_HOME") == "" {
		// A per-checkout config written before a move still names the old root. Keep
		// its store inside the checkout instead of reaching back to the old location.
		if rel, err := filepath.Rel(moved, cfg.Storage.Database); err == nil && filepath.IsLocal(rel) {
			cfg.Storage.Database = filepath.Join(root, rel)
		}
	}
	if !filepath.IsAbs(cfg.Backups.Dir) {
		cfg.Backups.Dir = filepath.Join(root, cfg.Backups.Dir)
	} else if moved := cfg.Project.Root; moved != "" && moved != root && os.Getenv("CONTINUITY_HOME") == "" {
		if rel, err := filepath.Rel(moved, cfg.Backups.Dir); err == nil && filepath.IsLocal(rel) {
			cfg.Backups.Dir = filepath.Join(root, rel)
		}
	}
	cfg.Project.Root = root
	return cfg, nil
}

func WriteDefault(root string) (string, error) {
	if _, err := Load(root); err != nil {
		return "", err
	}

	dir := StateDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return path, toml.NewEncoder(f).Encode(portableDefault(root))
}

// portableDefault writes paths relative to the checkout so the config survives a
// move. A CONTINUITY_HOME store stays absolute because it lives outside the checkout.
func portableDefault(root string) Config {
	cfg := Default(root)
	cfg.Project.Root = ""
	if rel, err := filepath.Rel(root, cfg.Storage.Database); err == nil && filepath.IsLocal(rel) {
		cfg.Storage.Database = rel
	}
	if rel, err := filepath.Rel(root, cfg.Backups.Dir); err == nil && filepath.IsLocal(rel) {
		cfg.Backups.Dir = rel
	}
	return cfg
}

// SetSync records the sync settings in the project's config file, creating the
// file with defaults when absent. Other settings are preserved as written.
func SetSync(root string, value Sync) (string, error) {
	phase, err := adoption.Load(StateDir(root))
	if err != nil {
		return "", err
	}
	if phase.Phase() == "trial" || phase.Phase() == "exited" {
		return "", fmt.Errorf("History sync is unavailable during trial or after exit; adopt explicitly first")
	}

	path, err := WriteDefault(root)
	if err != nil {
		return "", err
	}
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return "", err
	}
	cfg.Sync = value
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := toml.NewEncoder(file).Encode(cfg); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(temporary, path)
}

// LoadPlanPolicy applies project overrides to engineer-wide planning defaults.
func LoadPlanPolicy(globalRoot string, project trackplan.Policy) (trackplan.Policy, error) {
	var global struct {
		Policy trackplan.Policy `toml:"plan_policy"`
	}
	path := filepath.Join(globalRoot, GlobalFileName)
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &global); err != nil {
			return trackplan.Policy{}, err
		}
	} else if !os.IsNotExist(err) {
		return trackplan.Policy{}, err
	}
	if err := global.Policy.Validate(); err != nil {
		return trackplan.Policy{}, err
	}
	if err := project.Validate(); err != nil {
		return trackplan.Policy{}, err
	}
	p := trackplan.MergePolicy(global.Policy, project)
	return p, p.Validate()
}
