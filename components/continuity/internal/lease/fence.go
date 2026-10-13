package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StampSchemaVersion is the lease_stamps row version.
const StampSchemaVersion = 1

// Record kinds a write can be stamped on.
const (
	StampCheckpoint = "checkpoint"
	StampPlan       = "track_plan_revision"
)

// Stamp states.
const (
	// Leased: the writer presented the scope's current, unexpired grant.
	Leased = "leased"
	// Unleased: the writer presented no lease.
	Unleased = "unleased"
	// Lapsed: the writer presented a token that is no longer the scope's
	// current, unexpired grant held by that writer; it lost its lease.
	Lapsed = "lapsed"
)

// ErrLeaseRequired means a required scope refused a write because the writer
// did not present its current lease.
var ErrLeaseRequired = errors.New("lease required")

// Claim is the lease a writer says it holds: who, and under which token.
// The zero value presents no lease.
type Claim struct {
	Holder    string `json:"holder,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Token     int64  `json:"token,omitempty"`
}

// Empty reports whether no lease is presented.
func (c Claim) Empty() bool { return c == Claim{} }

// Fence is how one write relates to the lease on its scope: the writer's claim
// and whether the scope's policy requires a current lease.
type Fence struct {
	Claim    Claim
	Required bool
}

// Stamp records which lease a checkpoint or plan revision was written under.
type Stamp struct {
	RecordKind string `json:"record_kind"`
	RecordID   string `json:"record_id"`
	ScopeKind  string `json:"scope_kind"`
	ScopeID    string `json:"scope_id"`
	State      string `json:"state"`
	Token      int64  `json:"token,omitempty"`
	Holder     string `json:"holder,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	// InstanceID names the store (one project on one machine) that wrote the
	// record; stamps sync so other machines can flag stale writes.
	InstanceID string    `json:"instance_id,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Scope renders the stamped scope as KIND:ID.
func (s Stamp) Scope() string { return s.ScopeKind + ":" + s.ScopeID }

// Note describes the stamp for a human, or "" when the write was leased.
func (s Stamp) Note() string {
	switch s.State {
	case Unleased:
		return fmt.Sprintf("written without a lease on %s (advisory scope)", s.Scope())
	case Lapsed:
		return fmt.Sprintf("written under token %d of %s, which is no longer its current lease (advisory scope)", s.Token, s.Scope())
	}
	return ""
}

// Stamp checks the writer's claim against the scope's current lease and records
// the stamp for recordID in tx. A claim counts when it names the scope's latest
// grant, that grant is unexpired and unreleased, and the holder matches. When it
// does not, a required scope refuses the write with ErrLeaseRequired; an
// advisory scope records the write as unleased (no claim) or lapsed (a claim
// that no longer holds). The caller owns tx and should roll it back on error.
func (f Fence) Stamp(ctx context.Context, tx *sql.Tx, projectID, recordKind, recordID, scopeKind, scopeID string, now time.Time) (Stamp, error) {
	claim := f.Claim
	var err error
	if claim.Holder, err = plain("holder", claim.Holder, !claim.Empty()); err != nil {
		return Stamp{}, err
	}
	if claim.SessionID, err = plain("session ID", claim.SessionID, false); err != nil {
		return Stamp{}, err
	}
	if !claim.Empty() && claim.Token < 1 {
		return Stamp{}, errors.New("lease claim needs the token of the held grant")
	}
	scope := scopeKind + ":" + scopeID
	existing, err := current(ctx, tx, projectID, scopeKind, scopeID, now)
	if err != nil {
		return Stamp{}, err
	}
	instance, err := instanceID(ctx, tx, projectID)
	if err != nil {
		return Stamp{}, err
	}
	stamp := Stamp{RecordKind: recordKind, RecordID: recordID, ScopeKind: scopeKind, ScopeID: scopeID, State: Leased, Token: claim.Token, Holder: claim.Holder, SessionID: claim.SessionID, InstanceID: instance, RecordedAt: now}
	var problem string
	switch {
	case claim.Empty():
		stamp.State, problem = Unleased, "no lease was presented"
		if existing != nil && existing.State == Held {
			problem += fmt.Sprintf("; %s is leased by %s (token %d)", scope, holderOn(*existing, instance), existing.Token)
		}
	case existing == nil:
		stamp.State, problem = Lapsed, fmt.Sprintf("%s has never been leased", scope)
	case existing.Token != claim.Token:
		stamp.State, problem = Lapsed, fmt.Sprintf("%s is at token %d, not %d", scope, existing.Token, claim.Token)
	case existing.Holder != claim.Holder || existing.SessionID != claim.SessionID || existing.InstanceID != instance:
		stamp.State, problem = Lapsed, fmt.Sprintf("token %d on %s belongs to %s", claim.Token, scope, holderOn(*existing, instance))
	case existing.State == Released:
		stamp.State, problem = Lapsed, fmt.Sprintf("token %d on %s was released", claim.Token, scope)
	case existing.State == Expired:
		stamp.State, problem = Lapsed, fmt.Sprintf("token %d on %s expired at %s", claim.Token, scope, existing.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if stamp.State != Leased && f.Required {
		return Stamp{}, fmt.Errorf("%w: %s requires a current lease and %s; acquire it (llewm lease acquire %s) and write under its token", ErrLeaseRequired, scope, problem, scope)
	}
	if stamp.State == Unleased {
		stamp.Token, stamp.Holder, stamp.SessionID = 0, "", ""
	}
	if err := InsertStamp(ctx, tx, projectID, stamp); err != nil {
		return Stamp{}, err
	}
	return stamp, nil
}

// InsertStamp appends a stamp as given; import uses it for stamps written on
// other machines.
func InsertStamp(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectID string, stamp Stamp) error {
	var token any
	if stamp.Token > 0 {
		token = stamp.Token
	}
	_, err := db.ExecContext(ctx, `INSERT INTO lease_stamps(project_id,record_kind,record_id,scope_kind,scope_id,state,token,holder,session_id,instance_id,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?)`,
		projectID, stamp.RecordKind, stamp.RecordID, stamp.ScopeKind, stamp.ScopeID, stamp.State, token, stamp.Holder, stamp.SessionID, stamp.InstanceID, stamp.RecordedAt.UTC().Format(time.RFC3339Nano), StampSchemaVersion)
	return err
}

const stampColumns = `record_kind,record_id,scope_kind,scope_id,state,token,holder,session_id,COALESCE(instance_id,''),recorded_at`

func scanStamp(row interface{ Scan(...any) error }) (Stamp, error) {
	var value Stamp
	var token sql.NullInt64
	var recorded string
	if err := row.Scan(&value.RecordKind, &value.RecordID, &value.ScopeKind, &value.ScopeID, &value.State, &token, &value.Holder, &value.SessionID, &value.InstanceID, &recorded); err != nil {
		return Stamp{}, err
	}
	value.Token = token.Int64
	var err error
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	return value, err
}

// Stamps returns every stamp of a project in recorded order, for export.
func Stamps(ctx context.Context, db queryer, projectID string) ([]Stamp, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+stampColumns+` FROM lease_stamps WHERE project_id=? ORDER BY recorded_at,record_kind,record_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Stamp
	for rows.Next() {
		value, err := scanStamp(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// ValidateStamp checks a stamp read from another machine.
func ValidateStamp(value Stamp) error {
	if !oneOf(value.RecordKind, StampCheckpoint, StampPlan) || !oneOf(value.ScopeKind, ScopeKinds...) || !oneOf(value.State, Leased, Unleased, Lapsed) {
		return fmt.Errorf("lease stamp %s:%s has an unsupported kind, scope, or state", value.RecordKind, value.RecordID)
	}
	if (value.State == Unleased) != (value.Token == 0) || value.Token < 0 || value.RecordedAt.IsZero() {
		return fmt.Errorf("lease stamp %s:%s has an inconsistent token", value.RecordKind, value.RecordID)
	}
	for _, field := range []string{value.RecordID, value.ScopeID, value.Holder, value.SessionID, value.InstanceID} {
		if _, err := plain("field", field, false); err != nil {
			return fmt.Errorf("lease stamp %s:%s: %w", value.RecordKind, value.RecordID, err)
		}
	}
	if value.RecordID == "" || value.ScopeID == "" {
		return fmt.Errorf("lease stamp needs a record and scope ID")
	}
	return nil
}

// GetStamp returns the stamp of a record, or nil when it was written before
// stamps existed or its stamp has not synced here.
func GetStamp(ctx context.Context, db queryer, projectID, recordKind, recordID string) (*Stamp, error) {
	value, err := scanStamp(db.QueryRowContext(ctx, `SELECT `+stampColumns+` FROM lease_stamps WHERE project_id=? AND record_kind=? AND record_id=?`, projectID, recordKind, recordID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// Policy says which scopes require a current lease for checkpoint and plan
// writes. Patterns are KIND:ID or KIND:* ; every other scope is advisory.
type Policy struct{ required []string }

// ParsePolicy validates the configured required-scope patterns.
func ParsePolicy(patterns []string) (Policy, error) {
	policy := Policy{}
	for _, pattern := range patterns {
		kind, id, ok := strings.Cut(strings.TrimSpace(pattern), ":")
		if !ok || !oneOf(kind, ScopeKinds...) || strings.TrimSpace(id) == "" {
			return Policy{}, fmt.Errorf("lease policy pattern %q must be task:ID, roadmap_node:ID, or track_plan:ID, with * for every ID of a kind", pattern)
		}
		policy.required = append(policy.required, kind+":"+strings.TrimSpace(id))
	}
	return policy, nil
}

// Required reports whether the scope requires a current lease.
func (p Policy) Required(scopeKind, scopeID string) bool {
	for _, pattern := range p.required {
		if pattern == scopeKind+":"+scopeID || pattern == scopeKind+":*" {
			return true
		}
	}
	return false
}
