package project

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// RegistryFileName is the machine-local repository routing index. It contains
// locations and identifiers only; project records remain in their own stores.
const RegistryFileName = "registry.db"

const registryVersion = 1

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
	Missing      bool   `json:"missing,omitempty"`
	Availability string `json:"availability,omitempty"`
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
		entry.Availability = PathAvailability(entry.Root)
		entry.Missing = entry.Availability == "missing"
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
	entry.Store = canonicalPath(entry.Store)
	var unchanged int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkout_routes WHERE repository_id=? AND root_path=? AND store_path=?
        AND fingerprint=? AND project_id=? AND COALESCE(lineage_id,'')=? AND COALESCE(group_root,'')=?
        AND COALESCE(group_name,'')=? AND COALESCE(member_path,'')=?
        AND EXISTS(SELECT 1 FROM repository_routes legacy WHERE legacy.repository_id=checkout_routes.repository_id
            AND legacy.root_path=checkout_routes.root_path AND legacy.store_path=checkout_routes.store_path)`, entry.RepositoryID, entry.Root, entry.Store,
		entry.Fingerprint, entry.ProjectID, entry.LineageID, entry.GroupRoot, entry.GroupName, entry.Member).Scan(&unchanged)
	if err != nil || unchanged != 0 {
		return err
	}
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
		FROM checkout_routes WHERE group_root=? AND rowid IN (
            SELECT MAX(rowid) FROM checkout_routes WHERE group_root=? GROUP BY repository_id,COALESCE(member_path,''))
        ORDER BY member_path,repository_id`, groupRoot, groupRoot)
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
	db, err := sql.Open("sqlite", registryDSN(path, false))
	if err != nil {
		return nil, err
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > registryVersion {
		db.Close()
		return nil, fmt.Errorf("registry schema %d is newer than supported %d; use a matching Llewm version", version, registryVersion)
	}
	if version == registryVersion {
		return db, nil
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS repository_routes (
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
    );`); err == nil {
		// Early registries lacked optional workspace fields. Preserve every route
		// while adding them before creating indexes or compatibility triggers.
		var rows *sql.Rows
		rows, err = tx.Query("PRAGMA table_info(repository_routes)")
		columns := map[string]bool{}
		if err == nil {
			for rows.Next() {
				var id, notnull, pk int
				var name, kind string
				var def any
				if err = rows.Scan(&id, &name, &kind, &notnull, &def, &pk); err != nil {
					break
				}
				columns[name] = true
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		for _, column := range []string{"lineage_id", "group_root", "group_name", "member_path"} {
			if err == nil && !columns[column] {
				_, err = tx.Exec("ALTER TABLE repository_routes ADD COLUMN " + column + " TEXT")
			}
		}
	}
	if err == nil {
		_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS repository_routes_fingerprint ON repository_routes(fingerprint);
    CREATE INDEX IF NOT EXISTS repository_routes_group ON repository_routes(group_root,member_path);
    CREATE TABLE checkout_routes (
        repository_id TEXT NOT NULL, fingerprint TEXT NOT NULL, project_id TEXT NOT NULL, lineage_id TEXT,
        root_path TEXT NOT NULL, store_path TEXT NOT NULL, group_root TEXT, group_name TEXT, member_path TEXT,
        observed_at TEXT NOT NULL, PRIMARY KEY(repository_id,root_path,store_path));
    INSERT INTO checkout_routes SELECT repository_id,fingerprint,project_id,lineage_id,root_path,store_path,group_root,group_name,member_path,observed_at FROM repository_routes;
    CREATE TRIGGER registry_insert AFTER INSERT ON repository_routes BEGIN
        DELETE FROM checkout_routes WHERE repository_id=NEW.repository_id AND root_path=NEW.root_path AND store_path=NEW.store_path;
        INSERT INTO checkout_routes VALUES(NEW.repository_id,NEW.fingerprint,NEW.project_id,NEW.lineage_id,NEW.root_path,NEW.store_path,NEW.group_root,NEW.group_name,NEW.member_path,NEW.observed_at); END;
    CREATE TRIGGER registry_update AFTER UPDATE ON repository_routes BEGIN
        DELETE FROM checkout_routes WHERE repository_id=NEW.repository_id AND root_path=NEW.root_path AND store_path=NEW.store_path;
        INSERT INTO checkout_routes VALUES(NEW.repository_id,NEW.fingerprint,NEW.project_id,NEW.lineage_id,NEW.root_path,NEW.store_path,NEW.group_root,NEW.group_name,NEW.member_path,NEW.observed_at); END;
    PRAGMA user_version=1;`)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func registryDSN(path string, readOnly bool) string {
	q := url.Values{"_pragma": []string{"busy_timeout(100)"}}
	if readOnly {
		q.Set("mode", "ro")
	}
	return "file:" + strings.ReplaceAll(strings.ReplaceAll(filepath.ToSlash(path), "?", "%3F"), "#", "%23") + "?" + q.Encode()
}

// RegistryAll is a read-only inventory, including distinct checkouts sharing a
// repository ID or store. Absence is an empty inventory; corruption is reported.
// It neither upgrades old registries nor probes or opens their project stores.
func RegistryAll(ctx context.Context, globalRoot string) ([]RegistryEntry, error) {
	path := filepath.Join(globalRoot, RegistryFileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return []RegistryEntry{}, nil
	} else if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", registryDSN(path, true))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version > registryVersion {
		return nil, fmt.Errorf("registry schema %d is newer than supported %d", version, registryVersion)
	}
	table := "checkout_routes"
	if version == 0 {
		table = "repository_routes"
	}
	columns := map[string]bool{}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, n, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&id, &name, &kind, &n, &def, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	optional := func(name string) string {
		if columns[name] {
			return "COALESCE(" + name + ",'')"
		}
		return "''"
	}
	rows, err = db.QueryContext(ctx, `SELECT fingerprint,repository_id,project_id,`+optional("lineage_id")+`,root_path,store_path,`+optional("group_root")+`,`+optional("group_name")+`,`+optional("member_path")+`,observed_at FROM `+table+` ORDER BY repository_id,root_path,store_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []RegistryEntry{}
	for rows.Next() {
		var e RegistryEntry
		if err := rows.Scan(&e.Fingerprint, &e.RepositoryID, &e.ProjectID, &e.LineageID, &e.Root, &e.Store, &e.GroupRoot, &e.GroupName, &e.Member, &e.ObservedAt); err != nil {
			return nil, err
		}
		e.Availability = PathAvailability(e.Root)
		e.Missing = e.Availability == "missing"
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// PathAvailability never equates an inaccessible or offline volume with absence.
// A missing path is confirmed only when an existing ancestor is reachable.
func PathAvailability(path string) string {
	if !filepath.IsAbs(path) {
		return "unknown"
	}
	_, err := os.Stat(path)
	if err == nil {
		return "present"
	}
	if !os.IsNotExist(err) {
		return "unknown"
	}
	for parent := filepath.Dir(path); parent != path; parent = filepath.Dir(path) {
		path = parent
		if _, err = os.Stat(parent); err == nil {
			return "missing"
		} else if !os.IsNotExist(err) {
			return "unknown"
		}
	}
	return "unknown"
}

type RegistryPrunePlan struct {
	Entries      []RegistryEntry `json:"entries"`
	Candidates   []RegistryEntry `json:"candidates"`
	ReviewSHA256 string          `json:"review_sha256"`
	Applied      bool            `json:"applied"`
}

// PruneRegistry removes only reviewed derived routes whose checkout and store
// are both confirmed absent. It never opens or deletes a project store.
func PruneRegistry(ctx context.Context, globalRoot, review string) (RegistryPrunePlan, error) {
	entries, err := RegistryAll(ctx, globalRoot)
	p := RegistryPrunePlan{Entries: entries, Candidates: []RegistryEntry{}}
	if err != nil {
		return p, err
	}
	for _, e := range entries {
		if e.Availability == "missing" && PathAvailability(e.Store) == "missing" {
			p.Candidates = append(p.Candidates, e)
		}
	}
	body, _ := json.Marshal(p)
	sum := sha256.Sum256(body)
	p.ReviewSHA256 = hex.EncodeToString(sum[:])
	if review == "" {
		return p, nil
	}
	if review != p.ReviewSHA256 {
		return p, fmt.Errorf("registry inventory changed; preview and review again")
	}
	db, err := openRegistry(globalRoot)
	if err != nil {
		return p, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	for _, e := range p.Candidates {
		if PathAvailability(e.Root) != "missing" || PathAvailability(e.Store) != "missing" {
			return p, fmt.Errorf("route availability changed; preview again")
		}
		// Include the reviewed observation timestamp to preserve concurrently
		// refreshed routes. The transaction holds the registry's writer only.
		result, err := tx.ExecContext(ctx, `DELETE FROM checkout_routes WHERE repository_id=? AND root_path=? AND store_path=? AND observed_at=?`, e.RepositoryID, e.Root, e.Store, e.ObservedAt)
		if err != nil {
			return p, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return p, err
		}
		if n != 1 {
			return p, fmt.Errorf("registry route changed; preview again")
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM repository_routes WHERE repository_id=? AND root_path=? AND store_path=? AND observed_at=?`, e.RepositoryID, e.Root, e.Store, e.ObservedAt); err != nil {
			return p, err
		}
	}
	if err = tx.Commit(); err != nil {
		return p, err
	}
	p.Applied = true
	return p, nil
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
