package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// GlobalDir is independent of project state and CONTINUITY_HOME.
func GlobalDir() (string, error) {
	if path := os.Getenv("CONTINUITY_GLOBAL_HOME"); path != "" {
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("CONTINUITY_GLOBAL_HOME must be an absolute path")
		}
		return filepath.Clean(path), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	legacy := filepath.Join(base, "llmem", "continuity")
	if _, err := os.Stat(legacy); err == nil {
		return "", fmt.Errorf("existing global guidance at %s: explicitly move it to %s or set CONTINUITY_GLOBAL_HOME to the retained directory before upgrading", legacy, filepath.Join(base, "llewm", "continuity"))
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect previous global guidance: %w", err)
	}
	return filepath.Join(base, "llewm", "continuity"), nil
}

// InitGlobal creates editable sources without replacing existing engineer content.
func InitGlobal() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, file := range []struct{ name, heading string }{
		{"AGENTS.md", "Global rules"},
		{"CONTEXT.md", "Engineer context"},
		{"PROMPTS.md", "Reusable guidance"},
	} {
		f, err := os.OpenFile(filepath.Join(dir, file.name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := fmt.Fprintf(f, "# %s\n", file.heading)
		closeErr := f.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return dir, nil
}
