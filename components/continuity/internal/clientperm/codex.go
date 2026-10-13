package clientperm

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const managedStart = "# BEGIN continuity managed permissions sha256="
const managedEnd = "# END continuity managed permissions\n"

// FileChange is a reviewed snapshot, including the original file for recovery.
type FileChange struct {
	Path          string
	Before, After []byte
	Exists        bool
	Mode          os.FileMode
}

// CodexPlan owns only checksum-verified blocks, never arbitrary user settings.
type CodexPlan struct {
	Files     []FileChange
	Preserved []string
}

func managedBlock(body string) string {
	return fmt.Sprintf("%s%x\n%s%s", managedStart, sha256.Sum256([]byte(body)), body, managedEnd)
}

// splitManaged requires an intact block at EOF. Refusing hand-edited blocks
// preserves user restrictions, even when their edit changes an approval to prompt.
func splitManaged(data []byte) (string, error) {
	s := string(data)
	if !strings.Contains(s, managedStart) && !strings.Contains(s, strings.TrimSpace(managedEnd)) {
		return s, nil
	}
	start := strings.Index(s, managedStart)
	if start < 0 || (start > 0 && s[start-1] != '\n') || strings.Count(s, managedStart) != 1 || strings.Count(s, strings.TrimSpace(managedEnd)) != 1 || !strings.HasSuffix(s, managedEnd) {
		return "", fmt.Errorf("managed permission block is malformed or no longer at end of file; reconcile it manually")
	}
	line := strings.IndexByte(s[start:], '\n')
	if line < 0 {
		return "", fmt.Errorf("incomplete managed permission header")
	}
	if start+line+1 > len(s)-len(managedEnd) {
		return "", fmt.Errorf("overlapping managed permission markers")
	}
	body := s[start+line+1 : len(s)-len(managedEnd)]
	if s[start:] != managedBlock(body) {
		return "", fmt.Errorf("managed permissions were edited; preserve your restrictions and reconcile the block manually")
	}
	return s[:start], nil
}

func readChange(path string) (FileChange, error) {
	c := FileChange{Path: path, Mode: 0600}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() {
		return c, fmt.Errorf("%s is not a regular file", path)
	}
	c.Exists, c.Mode = true, info.Mode().Perm()
	c.Before, err = os.ReadFile(path)
	return c, err
}

// PlanCodex derives a non-mutating preview. Existing tool tables are entirely
// user-owned, even if they contain no approval_mode. Retired unmanaged entries
// are left for explicit user review. Managed entries are replaced, not appended.
func PlanCodex(home string, rules Rules) (CodexPlan, error) {
	var plan CodexPlan
	for _, path := range []string{filepath.Join(home, "config.toml"), filepath.Join(home, "rules", "llewm.rules")} {
		c, err := readChange(path)
		if err != nil {
			return plan, err
		}
		base, err := splitManaged(c.Before)
		if err != nil {
			return plan, fmt.Errorf("%s: %w", path, err)
		}
		var body strings.Builder
		if filepath.Ext(path) == ".toml" {
			var config map[string]any
			if _, err := toml.Decode(base, &config); err != nil {
				return plan, fmt.Errorf("%s: %w", path, err)
			}
			tools := nestedMap(config, "plugins", CodexPlugin, "mcp_servers", "continuity", "tools")
			inheritedRestriction := false
			for _, parent := range []map[string]any{config, nestedMap(config, "plugins", CodexPlugin), nestedMap(config, "plugins", CodexPlugin, "mcp_servers", "continuity")} {
				if mode, ok := parent["approval_mode"].(string); ok && mode != "approve" {
					inheritedRestriction = true
				}
			}

			names := append([]string(nil), rules.CodexTools...)
			sort.Strings(names)
			for _, name := range names {
				if _, exists := tools[name]; exists || inheritedRestriction {
					plan.Preserved = append(plan.Preserved, name)
					continue
				}
				fmt.Fprintf(&body, "[plugins.%q.mcp_servers.continuity.tools.%q]\napproval_mode = \"approve\"\n", CodexPlugin, name)
			}
			for name := range tools {
				if !containsName(names, name) {
					plan.Preserved = append(plan.Preserved, name+" (retired/unrecognized; user-owned)")
				}
			}
		} else {
			if strings.TrimSpace(base) != "" {
				return plan, fmt.Errorf("%s contains unmanaged rules; move them to another .rules file before using --write", path)
			}
			for _, rule := range append(append([]string{}, rules.Allow...), rules.Deny...) {
				fmt.Fprintln(&body, rule)
			}
		}
		if base != "" && !strings.HasSuffix(base, "\n") {
			base += "\n"
		}
		c.After = []byte(base + managedBlock(body.String()))
		if filepath.Ext(path) == ".toml" {
			var parsed map[string]any
			if _, err := toml.Decode(string(c.After), &parsed); err != nil {
				return plan, fmt.Errorf("cannot safely merge %s: %w", path, err)
			}
		}
		plan.Files = append(plan.Files, c)
	}
	sort.Strings(plan.Preserved)
	return plan, nil
}

func nestedMap(value map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		value, _ = value[key].(map[string]any)
	}
	return value
}
func containsName(names []string, name string) bool {
	for _, v := range names {
		if v == name {
			return true
		}
	}
	return false
}

// Apply writes backups for every existing changed file before replacing either
// file. Each replacement is atomic. An interrupted process leaves backups; an
// ordinary failure rolls back completed replacements. It refuses stale previews.
func (p CodexPlan) Apply() ([]string, error) {
	return p.apply(os.Rename)
}
func (p CodexPlan) apply(rename func(string, string) error) ([]string, error) {
	var changed []FileChange
	for _, c := range p.Files {
		current, err := readChange(c.Path)
		if err != nil {
			return nil, err
		}
		if current.Exists != c.Exists || current.Mode != c.Mode || !bytes.Equal(current.Before, c.Before) {
			return nil, fmt.Errorf("%s changed since preview; rerun", c.Path)
		}
		if !bytes.Equal(c.Before, c.After) {
			changed = append(changed, c)
		}
	}
	var backups []string
	for _, c := range changed {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0700); err != nil {
			return backups, err
		}
		if c.Exists {
			f, err := os.OpenFile(c.Path+".bak-"+time.Now().UTC().Format("20060102T150405.000000000Z"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, c.Mode)
			if err != nil {
				return backups, err
			}
			backups = append(backups, f.Name())
			_, err = f.Write(c.Before)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				return backups, err
			}
		}
	}
	for i, c := range changed {
		if err := replaceFile(c.Path, c.After, c.Mode, rename); err != nil {
			for j := i - 1; j >= 0; j-- {
				old := changed[j]
				var rollback error
				if old.Exists {
					rollback = replaceFile(old.Path, old.Before, old.Mode, os.Rename)
				} else {
					rollback = os.Remove(old.Path)
				}
				if rollback != nil {
					return backups, fmt.Errorf("write failed: %v; rollback %s failed: %v; restore the listed backups", err, old.Path, rollback)
				}
			}
			return backups, fmt.Errorf("write failed; prior replacements rolled back: %w", err)
		}
	}
	return backups, nil
}
func replaceFile(path string, data []byte, mode os.FileMode, rename func(string, string) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".continuity-permissions-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return rename(f.Name(), path)
}
