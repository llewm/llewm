package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
)

// AutomaticConfig is engineer-wide and never inferred from the active project.
// Fetch is a separate opt-in because it contacts the selected configured remote.
type AutomaticConfig struct {
	Enabled    bool   `json:"enabled"`
	SourceRoot string `json:"source_root"`
	Fetch      bool   `json:"fetch"`
	Remote     string `json:"remote"`
	Branch     string `json:"branch"`
	Interval   string `json:"interval"`
	Timeout    string `json:"timeout"`
}

func DefaultAutomaticConfig() AutomaticConfig {
	return AutomaticConfig{Remote: "origin", Branch: "main", Interval: "24h", Timeout: "3s"}
}

func (c AutomaticConfig) Validate() error {
	interval, err := time.ParseDuration(c.Interval)
	if err != nil || interval < time.Minute || interval > 30*24*time.Hour {
		return fmt.Errorf("update interval must be between 1m and 720h")
	}
	timeout, err := time.ParseDuration(c.Timeout)
	if err != nil || timeout < 100*time.Millisecond || timeout > 10*time.Second {
		return fmt.Errorf("update timeout must be between 100ms and 10s")
	}
	if c.Enabled && !filepath.IsAbs(c.SourceRoot) {
		return fmt.Errorf("automatic updates require an absolute source-root")
	}
	if c.Fetch && (!remotePattern.MatchString(c.Remote) || c.Branch == "" || c.Branch[0] == '-') {
		return fmt.Errorf("fetch requires a configured remote name and branch")
	}
	return nil
}

func LoadAutomaticConfig(dir string) (AutomaticConfig, error) {
	c := DefaultAutomaticConfig()
	b, err := os.ReadFile(filepath.Join(dir, "update-check.json"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func SaveAutomaticConfig(dir string, c AutomaticConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return writeAutomaticJSON(dir, "update-check.json", c)
}

// SaveAutomaticConfigIfAbsent applies onboarding defaults without replacing an
// engineer's existing settings, including an explicit opt-out.
func SaveAutomaticConfigIfAbsent(dir string, c AutomaticConfig) (bool, error) {
	if err := c.Validate(); err != nil {
		return false, err
	}
	return publishAutomaticJSON(dir, "update-check.json", c, true)
}

type AutomaticResult struct {
	Unconfigured bool            `json:"unconfigured,omitempty"`
	CheckedAt    time.Time       `json:"checked_at"`
	Config       AutomaticConfig `json:"config"`
	Installed    buildinfo.Info  `json:"installed"`
	Report       *Report         `json:"report,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// Automatic checks at most once per interval in sequential sessions. Failures
// are cached too, preventing offline sessions from retrying every startup.
// The cache is local bookkeeping, never evidence of installation or delivery.
func Automatic(ctx context.Context, dir string, installed buildinfo.Info, now time.Time) (AutomaticResult, error) {
	c, err := LoadAutomaticConfig(dir)
	if err != nil {
		return AutomaticResult{}, err
	}
	if !c.Enabled {
		_, err := os.Stat(filepath.Join(dir, "update-check.json"))
		return AutomaticResult{Unconfigured: os.IsNotExist(err)}, nil
	}
	interval, _ := time.ParseDuration(c.Interval)
	var cached AutomaticResult
	if b, err := os.ReadFile(filepath.Join(dir, "update-check-cache.json")); err == nil && json.Unmarshal(b, &cached) == nil && cached.Config == c && cached.Installed == installed && !cached.CheckedAt.IsZero() && !now.Before(cached.CheckedAt) && now.Sub(cached.CheckedAt) < interval && (cached.Report != nil || cached.Error != "") {
		return cached, nil
	}
	timeout, _ := time.ParseDuration(c.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, checkErr := source(ctx, installed, c.SourceRoot, c.Remote, c.Branch, c.Fetch, false)
	result := AutomaticResult{CheckedAt: now.UTC(), Config: c, Installed: installed}
	if checkErr != nil {
		// Keep raw Git errors, paths and remote details out of automatic context.
		result.Error = "check failed; run llewm update check with the configured source for details"
	} else {
		result.Report = &r
	}
	if err := writeAutomaticJSON(dir, "update-check-cache.json", result); err != nil {
		return result, err
	}
	return result, nil
}

func (r AutomaticResult) Line() string {
	if r.Unconfigured {
		return "Llewm update checks are off; enable with llewm update configure --enabled --source-root /absolute/path/to/llewm (add --fetch for upstream checks)."
	}
	if r.CheckedAt.IsZero() {
		return ""
	}
	state := "unavailable"
	if r.Report != nil {
		state = r.Report.State
	}
	return fmt.Sprintf("Llewm update check: %s (checked %s; source comparison; automatic application disabled).", state, r.CheckedAt.Format(time.RFC3339))
}

func writeAutomaticJSON(dir, name string, value any) error {
	_, err := publishAutomaticJSON(dir, name, value, false)
	return err
}

func publishAutomaticJSON(dir, name string, value any, onlyIfAbsent bool) (bool, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return false, err
	}
	f, err := os.CreateTemp(dir, ".update-check-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	destination := filepath.Join(dir, name)
	if onlyIfAbsent {
		// Link publishes the complete file atomically without overwriting a
		// configuration another onboarding process may just have created.
		if err := os.Link(f.Name(), destination); err != nil {
			if os.IsExist(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	err = os.Rename(f.Name(), destination)
	return err == nil, err
}
