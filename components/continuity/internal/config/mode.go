package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// SetMode preserves all configuration keys, including keys added by other versions.
func SetMode(root, mode string) (string, error) {
	if mode != "full" && mode != "audit-only" {
		return "", fmt.Errorf("mode %q must be full or audit-only", mode)
	}
	path, err := WriteDefault(root)
	if err != nil {
		return "", err
	}
	var values map[string]any
	if _, err := toml.DecodeFile(path, &values); err != nil {
		return "", err
	}
	values["mode"] = mode
	f, err := os.CreateTemp(filepath.Dir(path), "config-mode-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err := toml.NewEncoder(f).Encode(values); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(f.Name(), path)
}
