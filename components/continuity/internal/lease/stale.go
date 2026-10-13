package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// A stale write is a checkpoint or plan revision stamped with a token of its
// scope after a newer grant of that scope existed: its writer had lost the
// lease, usually on another machine before the two synced. Detection is a read
// over the stamps and grants this machine has (both sync), so it works
// whichever arrives first. Resolving one records whether the write stands or
// yields to the newer lease; either way the record stays in history.

// StaleSubjectPrefix starts the fork reference of a stale write:
// stale:RECORD_KIND:RECORD_ID.
const StaleSubjectPrefix = "stale:"

// Accepted values of a stale-write resolution.
const (
	AcceptWrite = "write"
	AcceptLease = "lease"
)

// StaleWrite is a write made under a superseded token, and the first newer
// grant that existed when it was written.
type StaleWrite struct {
	Stamp Stamp `json:"stamp"`
	Newer Lease `json:"newer"`
}

// Ref is the write's fork reference.
func (w StaleWrite) Ref() string { return StaleSubject(w.Stamp.RecordKind, w.Stamp.RecordID) }

// StaleSubject renders a stale write's fork reference.
func StaleSubject(recordKind, recordID string) string {
	return StaleSubjectPrefix + recordKind + ":" + recordID
}

// ParseStaleSubject splits stale:RECORD_KIND:RECORD_ID.
func ParseStaleSubject(value string) (string, string, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(value), StaleSubjectPrefix)
	kind, id, found := strings.Cut(rest, ":")
	if !ok || !found || !oneOf(kind, StampCheckpoint, StampPlan) || strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("stale write %q must be stale:checkpoint:ID or stale:track_plan_revision:PLAN@REVISION", value)
	}
	return kind, id, nil
}

// StaleWrites returns the unresolved stale writes, newest first; subject, when
// set, keeps only that one.
func StaleWrites(ctx context.Context, db queryer, projectID, subject string) ([]StaleWrite, error) {
	stamps, err := Stamps(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	resolved, err := resolvedStale(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	grants := map[string][]Lease{}
	result := []StaleWrite{}
	now := time.Now().UTC()
	for _, stamp := range stamps {
		ref := StaleSubject(stamp.RecordKind, stamp.RecordID)
		if stamp.Token == 0 || resolved[ref] || (subject != "" && subject != ref) {
			continue
		}
		scope := stamp.Scope()
		if _, loaded := grants[scope]; !loaded {
			history, err := scopeGrants(ctx, db, projectID, stamp.ScopeKind, stamp.ScopeID, now)
			if err != nil {
				return nil, err
			}
			grants[scope] = history
		}
		for _, grant := range grants[scope] {
			if grant.Token > stamp.Token && grant.AcquiredAt.Before(stamp.RecordedAt) {
				result = append(result, StaleWrite{Stamp: stamp, Newer: grant})
				break
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Stamp.RecordedAt.After(result[j].Stamp.RecordedAt) })
	return result, nil
}

// scopeGrants returns a scope's grants, lowest token first.
func scopeGrants(ctx context.Context, db queryer, projectID, scopeKind, scopeID string, now time.Time) ([]Lease, error) {
	rows, err := db.QueryContext(ctx, leaseSelect+` WHERE g.project_id=? AND g.scope_kind=? AND g.scope_id=? ORDER BY g.token`, projectID, scopeKind, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Lease
	for rows.Next() {
		value, err := scan(rows, projectID, now)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// StaleResolution settles one stale write.
type StaleResolution struct {
	ProjectID     string    `json:"project_id,omitempty"`
	ID            string    `json:"id"`
	RecordKind    string    `json:"record_kind"`
	RecordID      string    `json:"record_id"`
	Accepted      string    `json:"accepted"`
	Resolver      string    `json:"resolver"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

// Ref is the resolved write's fork reference.
func (r StaleResolution) Ref() string { return StaleSubject(r.RecordKind, r.RecordID) }

// ValidateStaleResolution checks a resolution and returns it normalized.
func ValidateStaleResolution(value StaleResolution) (StaleResolution, error) {
	for name, field := range map[string]string{"ID": value.ID, "record ID": value.RecordID, "resolver": value.Resolver} {
		if strings.TrimSpace(field) == "" || strings.TrimSpace(field) != field || len(field) > maxIDBytes {
			return StaleResolution{}, fmt.Errorf("stale write resolution needs a trimmed %s of at most %d bytes", name, maxIDBytes)
		}
	}
	if !oneOf(value.RecordKind, StampCheckpoint, StampPlan) {
		return StaleResolution{}, fmt.Errorf("unsupported stale write kind %q", value.RecordKind)
	}
	if !oneOf(value.Accepted, AcceptWrite, AcceptLease) {
		return StaleResolution{}, fmt.Errorf("a stale write resolution accepts %q (the write stands) or %q (it yields to the newer lease), not %q", AcceptWrite, AcceptLease, value.Accepted)
	}
	if !oneOf(value.Authority, "user_reported", "caller_asserted") {
		return StaleResolution{}, errors.New("stale write resolution authority must be user_reported or caller_asserted")
	}
	if value.SchemaVersion != StampSchemaVersion || value.RecordedAt.IsZero() {
		return StaleResolution{}, errors.New("stale write resolution needs schema 1 and a time")
	}
	value.RecordedAt = value.RecordedAt.UTC()
	return value, nil
}

// InsertStaleResolution appends a validated resolution.
func InsertStaleResolution(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, value StaleResolution) error {
	value, err := ValidateStaleResolution(value)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO stale_write_resolutions(project_id,id,record_kind,record_id,accepted,resolver,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?)`,
		value.ProjectID, value.ID, value.RecordKind, value.RecordID, value.Accepted, value.Resolver, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
	return err
}

// StaleResolutions returns every stale-write resolution in recorded order.
func StaleResolutions(ctx context.Context, db queryer, projectID string) ([]StaleResolution, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,record_kind,record_id,accepted,resolver,authority,recorded_at,schema_version FROM stale_write_resolutions WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StaleResolution
	for rows.Next() {
		value := StaleResolution{ProjectID: projectID}
		var recorded string
		if err := rows.Scan(&value.ID, &value.RecordKind, &value.RecordID, &value.Accepted, &value.Resolver, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			return nil, err
		}
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func resolvedStale(ctx context.Context, db queryer, projectID string) (map[string]bool, error) {
	values, err := StaleResolutions(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	resolved := map[string]bool{}
	for _, value := range values {
		resolved[value.Ref()] = true
	}
	return resolved, nil
}

// StaleNotice counts unresolved stale writes for the bootstrap, or "".
func StaleNotice(writes []StaleWrite) string {
	switch len(writes) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("%s was written under token %d of %s after token %d was granted to %s (llewm fork show %s).", writes[0].Ref(), writes[0].Stamp.Token, writes[0].Stamp.Scope(), writes[0].Newer.Token, writes[0].Newer.HolderLabel(), writes[0].Ref())
	}
	return fmt.Sprintf("%d writes were made under a superseded lease token (llewm fork list).", len(writes))
}
