package project

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// RegistryFileName is the machine-local repository routing index. It contains
// locations and identifiers only; project records remain in their own stores.
const RegistryFileName = "registry.db"

// RegistryEntry maps one repository fingerprint to its local project and store.
type RegistryEntry struct {
	Fingerprint  string `json:"fingerprint"`
	RepositoryID string `json:"repository_id"`
	ProjectID    string `json:"project_id"`
	LineageID    string `json:"lineage_id,omitempty"`
	Root         string `json:"root"`
	Store        string `json:"store"`
	GroupRoot    string `json:"group_root,omitempty"`
	GroupName    string `json:"group_name,omitempty"`
	Member       string `json:"member,omitempty"`
	ObservedAt   string `json:"observed_at"`
	// Missing reports that Root no longer exists on this machine. It is computed
	// at read time; reopening the member from its new location updates the route.
	Missing bool `json:"missing,omitempty"`
}

// ResolveRegistryReference resolves an explicit member, repository, project,
// lineage, or fingerprint among the workspace's current members. It never
// searches project bodies, refuses ambiguous identifiers, and refuses a route
// whose checkout no longer exists.
func ResolveRegistryReference(ctx context.Context, globalRoot string, workspace Workspace, reference string) (RegistryEntry, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return RegistryEntry{}, fmt.Errorf("reference is required")
	}
	groupRoot := workspace.Root
	entries, err := WorkspaceRoutes(ctx, globalRoot, workspace)
	if err != nil {
		return RegistryEntry{}, err
	}
	var matches []RegistryEntry
	for _, entry := range entries {
		if reference == entry.Member || reference == entry.RepositoryID || reference == entry.ProjectID || reference == entry.LineageID || reference == entry.Fingerprint {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 0 {
		return RegistryEntry{}, fmt.Errorf("reference %q is not registered in federated workspace %s", reference, groupRoot)
	}
	if len(matches) > 1 {
		return RegistryEntry{}, fmt.Errorf("reference %q is ambiguous in federated workspace %s", reference, groupRoot)
	}
	if matches[0].Missing {
		return RegistryEntry{}, fmt.Errorf("member %s was registered at %s, which no longer exists; open it from its current location to update its route", matches[0].Member, matches[0].Root)
	}
	return matches[0], nil
}

// WorkspaceRoutes returns the registered routes for the workspace's current
// manifest members, marking routes whose checkout no longer exists. Routes for
// members removed from the manifest are left out.
func WorkspaceRoutes(ctx context.Context, globalRoot string, workspace Workspace) ([]RegistryEntry, error) {
	entries, err := RegistryGroup(ctx, globalRoot, workspace.Root)
	if err != nil {
		return nil, err
	}
	members := make(map[string]bool, len(workspace.Members))
	for _, member := range workspace.Members {
		members[member.Path] = true
	}
	current := entries[:0]
	for _, entry := range entries {
		if !members[entry.Member] {
			continue
		}
		entry.Missing = !exists(entry.Root)
		current = append(current, entry)
	}
	return current, nil
}

// ObserveRegistry updates the rebuildable machine-local routing index.
func ObserveRegistry(ctx context.Context, globalRoot string, entry RegistryEntry) error {
	if entry.Fingerprint == "" || entry.RepositoryID == "" || entry.ProjectID == "" || entry.Root == "" || entry.Store == "" {
		return fmt.Errorf("registry observation requires fingerprint, repository, project, root, and store")
	}
	db, err := openRegistry(globalRoot)
	if err != nil {
		return err
	}
	defer db.Close()
	entry.Root, entry.GroupRoot = canonicalPath(entry.Root), canonicalPath(entry.GroupRoot)
	if entry.ObservedAt == "" {
		entry.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO repository_routes(
        fingerprint,repository_id,project_id,lineage_id,root_path,store_path,group_root,group_name,member_path,observed_at
    ) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repository_id) DO UPDATE SET
        fingerprint=excluded.fingerprint, project_id=excluded.project_id, lineage_id=excluded.lineage_id,
        root_path=excluded.root_path, store_path=excluded.store_path, group_root=excluded.group_root,
        group_name=excluded.group_name, member_path=excluded.member_path, observed_at=excluded.observed_at`,
		entry.Fingerprint, entry.RepositoryID, entry.ProjectID, nullEmpty(entry.LineageID), entry.Root, entry.Store,
		nullEmpty(entry.GroupRoot), nullEmpty(entry.GroupName), nullEmpty(entry.Member), entry.ObservedAt)
	return err
}

// RegistryGroup returns every recorded route for a federated workspace folder,
// including members since removed from its manifest; see WorkspaceRoutes.
func RegistryGroup(ctx context.Context, globalRoot, groupRoot string) ([]RegistryEntry, error) {
	groupRoot = canonicalPath(groupRoot)
	db, err := openRegistry(globalRoot)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT fingerprint,repository_id,project_id,COALESCE(lineage_id,''),root_path,store_path,
        COALESCE(group_root,''),COALESCE(group_name,''),COALESCE(member_path,''),observed_at
        FROM repository_routes WHERE group_root=? ORDER BY member_path`, groupRoot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []RegistryEntry
	for rows.Next() {
		var entry RegistryEntry
		if err := rows.Scan(&entry.Fingerprint, &entry.RepositoryID, &entry.ProjectID, &entry.LineageID, &entry.Root, &entry.Store,
			&entry.GroupRoot, &entry.GroupName, &entry.Member, &entry.ObservedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func openRegistry(globalRoot string) (*sql.DB, error) {
	if err := os.MkdirAll(globalRoot, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(globalRoot, RegistryFileName)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS repository_routes (
        repository_id TEXT PRIMARY KEY,
        fingerprint TEXT NOT NULL,
        project_id TEXT NOT NULL,
        lineage_id TEXT,
        root_path TEXT NOT NULL,
        store_path TEXT NOT NULL,
        group_root TEXT,
        group_name TEXT,
        member_path TEXT,
        observed_at TEXT NOT NULL
    ); CREATE INDEX IF NOT EXISTS repository_routes_fingerprint ON repository_routes(fingerprint);
    CREATE INDEX IF NOT EXISTS repository_routes_group ON repository_routes(group_root,member_path);`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// canonicalPath resolves symlinks so a folder reached through a link and its
// physical path share one route. Paths that cannot be resolved stay cleaned.
func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func nullEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
