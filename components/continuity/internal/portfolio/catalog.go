// Package portfolio routes independent project histories to explicitly selected
// Git repositories or folder containers. A catalog is local configuration, never portable memory.
package portfolio

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const FileName = "portfolio.toml"
const maxCatalogBytes = 1 << 20

type Catalog struct {
	Version      int           `toml:"version" json:"version"`
	Destinations []Destination `toml:"destinations" json:"destinations"`
	Projects     []Project     `toml:"projects" json:"projects"`
}

type Destination struct {
	Kind      string `toml:"kind" json:"kind"`
	Directory string `toml:"directory,omitempty" json:"directory,omitempty"`
	StoreID   string `toml:"store_id,omitempty" json:"store_id,omitempty"`
	Name      string `toml:"name" json:"name"`
	Remote    string `toml:"remote,omitempty" json:"remote,omitempty"`
	Clone     string `toml:"clone,omitempty" json:"clone,omitempty"`
	SSHKey    string `toml:"ssh_key,omitempty" json:"ssh_key,omitempty"`
}

func (d Destination) MemoryClone() (string, error) {
	if d.Kind != "git" {
		return "", fmt.Errorf("only Git destinations have memory clones")
	}
	if d.Clone != "" {
		return d.Clone, nil
	}
	return config.DefaultMemoryClone(d.Remote)
}

type Project struct {
	Name      string  `toml:"name" json:"name"`
	Root      string  `toml:"root" json:"root"`
	ProjectID string  `toml:"project_id" json:"project_id"`
	Lineage   string  `toml:"lineage" json:"lineage"`
	Routes    []Route `toml:"routes" json:"routes"`
}

type Route struct {
	Destination string `toml:"destination" json:"destination"`
	Relay       bool   `toml:"relay" json:"relay"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var lineagePattern = regexp.MustCompile(`^lineage:[0-9a-f]{32}$`)

func (c Catalog) Validate() error {
	if c.Version != 1 || len(c.Destinations) > 64 || len(c.Projects) > 256 {
		return fmt.Errorf("unsupported or oversized portfolio catalog")
	}
	names, identities := map[string]bool{}, map[string]bool{}
	for _, d := range c.Destinations {
		if !namePattern.MatchString(d.Name) || names[d.Name] {
			return fmt.Errorf("duplicate or invalid portfolio destination")
		}
		var identity string
		switch d.Kind {
		case "git":
			if d.Directory != "" || d.StoreID != "" {
				return fmt.Errorf("Git destination cannot have folder fields")
			}
			if err := ValidateRemote(d.Remote); err != nil {
				return err
			}
			for _, path := range []string{d.Clone, d.SSHKey} {
				if path != "" && (!filepath.IsAbs(path) || path != filepath.Clean(path)) {
					return fmt.Errorf("clone and SSH key paths must be absolute and clean")
				}
			}
			identity = "git:" + d.Remote
		case "folder":
			if d.Remote != "" || d.Clone != "" || d.SSHKey != "" {
				return fmt.Errorf("folder destination cannot have Git fields")
			}
			if !filepath.IsAbs(d.Directory) || d.Directory != filepath.Clean(d.Directory) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(d.StoreID) {
				return fmt.Errorf("folder destination requires an absolute clean directory and exact store ID")
			}
			identity = "folder:" + d.StoreID
		default:
			return fmt.Errorf("destination kind must be git or folder")
		}
		if identities[identity] {
			return fmt.Errorf("duplicate portfolio destination identity")
		}
		names[d.Name], identities[identity] = true, true
	}
	projects, roots, ids, lineages := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range c.Projects {
		if !namePattern.MatchString(p.Name) || !filepath.IsAbs(p.Root) || p.Root != filepath.Clean(p.Root) || !strings.HasPrefix(p.ProjectID, "project-") || !lineagePattern.MatchString(p.Lineage) {
			return fmt.Errorf("invalid portfolio project binding")
		}
		if projects[p.Name] || roots[p.Root] || ids[p.ProjectID] || lineages[p.Lineage] {
			return fmt.Errorf("portfolio projects must have distinct names, roots, project IDs and independent lineages")
		}
		projects[p.Name], roots[p.Root], ids[p.ProjectID], lineages[p.Lineage] = true, true, true, true
		if len(p.Routes) == 0 || len(p.Routes) > 8 {
			return fmt.Errorf("each portfolio project needs one to eight destinations")
		}
		seen := map[string]bool{}
		for _, r := range p.Routes {
			if !names[r.Destination] || seen[r.Destination] {
				return fmt.Errorf("unknown or duplicate project destination")
			}
			seen[r.Destination] = true
		}
	}
	return nil
}

func ValidateRemote(remote string) error {
	if remote == "" || strings.TrimSpace(remote) != remote || strings.ContainsAny(remote, "\r\n\x00") || strings.Contains(remote, "::") || strings.HasPrefix(remote, "-") || redact.String(remote) != remote {
		return fmt.Errorf("remote must be an explicit Git destination without embedded secrets")
	}
	if filepath.IsAbs(remote) {
		return nil
	}
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh") || u.Host == "" || u.Path == "" {
			return fmt.Errorf("use an absolute local path, SSH or HTTP(S) Git remote without query or fragment")
		}
		if u.User != nil {
			if _, password := u.User.Password(); password {
				return fmt.Errorf("remote must not contain a password")
			}
		}
		return nil
	}
	if colon := strings.IndexByte(remote, ':'); colon > 0 && colon < len(remote)-1 && !strings.ContainsAny(remote[:colon], `/\ `) {
		return nil // Git's host:path / user@host:path SSH form.
	}
	return fmt.Errorf("relative Git destinations are not supported")
}

func (c Catalog) Destination(name string) (Destination, bool) {
	for _, d := range c.Destinations {
		if d.Name == name {
			return d, true
		}
	}
	return Destination{}, false
}

// Store serializes local catalog edits and publishes each complete revision
// atomically. A failed validation leaves the prior revision unchanged.
type Store struct{ Directory string }

func DefaultStore() (Store, error) {
	dir, err := config.GlobalDir()
	return Store{Directory: dir}, err
}

func (s Store) Load() (Catalog, error) {
	c := Catalog{Version: 1}
	root, err := recordsync.OpenRoot(s.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	defer root.Close()
	body, err := recordsync.ReadFile(root, FileName, maxCatalogBytes)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c = Catalog{}
	metadata, err := toml.Decode(string(body), &c)
	if err != nil {
		return Catalog{}, fmt.Errorf("invalid portfolio TOML")
	}
	if len(metadata.Undecoded()) != 0 {
		return Catalog{}, fmt.Errorf("unknown portfolio configuration key")
	}
	return c, c.Validate()
}

func (s Store) Update(change func(*Catalog) error) (Catalog, error) {
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return Catalog{}, err
	}
	root, err := recordsync.OpenRoot(s.Directory)
	if err != nil {
		return Catalog{}, err
	}
	defer root.Close()
	lock, err := root.OpenFile("portfolio.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Catalog{}, fmt.Errorf("portfolio catalog is locked: %w", err)
	}
	defer func() { lock.Close(); _ = root.Remove("portfolio.lock") }()
	c, err := s.Load()
	if err != nil {
		return c, err
	}
	if err := change(&c); err != nil {
		return c, err
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	var body bytes.Buffer
	if err := toml.NewEncoder(&body).Encode(c); err != nil {
		return c, err
	}
	if body.Len() > maxCatalogBytes {
		return c, fmt.Errorf("portfolio catalog too large")
	}
	_, err = recordsync.WriteFile(root, FileName, body.Bytes(), true)
	return c, err
}
