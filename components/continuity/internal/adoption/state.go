// Package adoption keeps a device-local, reversible project adoption receipt.
// It never rewrites project prose or client configuration.
package adoption

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const FileName = "adoption.json"
const MaxFileBytes = 1 << 20

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Exists bool   `json:"exists"`
	// Text is redacted and is evidence only, never a byte-for-byte restore source.
	Text string `json:"redacted_text,omitempty"`
}
type Transition struct {
	State          string    `json:"state"`
	At             time.Time `json:"at"`
	Review         string    `json:"review,omitempty"`
	Handback       string    `json:"handback,omitempty"`
	HandbackSHA256 string    `json:"handback_sha256,omitempty"`
	Verification   string    `json:"verification,omitempty"`
}
type State struct {
	Version     int          `json:"version"`
	Baseline    []File       `json:"baseline"`
	History     []Transition `json:"history"`
	ReviewAfter time.Time    `json:"review_after"`
}

func (s *State) Phase() string {
	if s == nil || len(s.History) == 0 {
		return "unmanaged"
	}
	return s.History[len(s.History)-1].State
}
func Load(dir string) (*State, error) {
	path := filepath.Join(dir, FileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return nil, fmt.Errorf("invalid adoption receipt %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err = json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Version != 1 || len(s.History) == 0 {
		return nil, errors.New("invalid adoption receipt version/history")
	}
	for _, h := range s.History {
		if h.State != "trial" && h.State != "adopted" && h.State != "exited" {
			return nil, errors.New("invalid adoption phase")
		}
	}
	return &s, nil
}
func CheckActive(dir string) error {
	s, err := Load(dir)
	if err != nil {
		return err
	}
	if s.Phase() == "exited" {
		return errors.New("Continuity is disconnected for this project; use the reviewed prose handback. State is retained; do not reinitialize or remove it to resume")
	}
	return nil
}
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// SafePath rejects symlink traversal, non-regular files and state/Git paths.
func SafePath(root, path string) (string, error) {
	path = filepath.Clean(path)
	if !filepath.IsLocal(path) || path == "." {
		return "", fmt.Errorf("expected project-relative file: %s", path)
	}
	parts := strings.Split(path, string(filepath.Separator))
	if parts[0] == ".git" || parts[0] == ".continuity" || parts[0] == ".llewm" {
		return "", fmt.Errorf("prose must be outside tool state: %s", path)
	}
	return regularPath(root, path)
}
func regularPath(root, path string) (string, error) {
	p := root
	for i, part := range strings.Split(path, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink refused: %s", p)
		}
		if i < len(strings.Split(path, string(filepath.Separator)))-1 && !info.IsDir() {
			return "", fmt.Errorf("not a directory: %s", p)
		}
	}
	return p, nil
}
func Snapshot(root string, paths []string, text bool) ([]File, error) {
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	out := []File{}
	last := ""
	for _, path := range paths {
		if path == last {
			continue
		}
		last = path
		p, err := SafePath(root, path)
		if err != nil {
			return nil, err
		}
		f := File{Path: filepath.ToSlash(filepath.Clean(path))}
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			out = append(out, f)
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
			return nil, fmt.Errorf("file is not regular or exceeds 1 MiB: %s", path)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f.Exists = true
		f.SHA256 = Digest(b)
		if text {
			f.Text = redact.String(string(b))
		}
		out = append(out, f)
	}
	return out, nil
}
func Save(dir string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > MaxFileBytes {
		return errors.New("adoption receipt exceeds 1 MiB")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".adoption.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("adoption transition already active (inspect retained lock after a crash): %w", err)
	}
	lock.Close()
	defer os.Remove(lock.Name())
	previous, err := Load(dir)
	if err != nil {
		return err
	}
	if previous == nil {
		if len(s.History) != 1 || s.Phase() != "trial" {
			return errors.New("first transition must start a trial")
		}
	} else {
		if len(s.History) != len(previous.History)+1 || !reflect.DeepEqual(s.History[:len(previous.History)], previous.History) || !reflect.DeepEqual(s.Baseline, previous.Baseline) || !s.ReviewAfter.Equal(previous.ReviewAfter) {
			return errors.New("stale or modified adoption baseline; reload trial status")
		}
		if previous.Phase() == "exited" || s.Phase() == "trial" {
			return errors.New("invalid adoption transition")
		}
	}
	path := filepath.Join(dir, FileName)
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return errors.New("adoption receipt is not regular")
	}
	f, err := os.CreateTemp(dir, "adoption-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func Start(dir, root string, paths []string, reviewAfter time.Time) error {
	s, err := Load(dir)
	if err != nil {
		return err
	}
	if s != nil {
		return errors.New("project already has an adoption receipt; inspect trial status")
	}
	files, err := Snapshot(root, paths, true)
	if err != nil {
		return err
	}
	return Save(dir, State{Version: 1, Baseline: files, ReviewAfter: reviewAfter, History: []Transition{{State: "trial", At: time.Now().UTC()}}})
}
func Current(root string, s *State) ([]File, error) {
	paths := []string{}
	for _, f := range s.Baseline {
		paths = append(paths, f.Path)
	}
	return Snapshot(root, paths, true)
}
