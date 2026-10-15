// Package standinggrant records standing grants: a person's permission for
// agents to run a named procedure revision on a trigger (after a slice commit)
// without asking each time, until it expires or is revoked. A grant covers the
// procedure and every procedure its steps name as procedure:ID, each at the
// revision current when the grant was made; a newer revision of any of them
// voids the grant, so editing a procedure never widens what was granted.
// Continuity executes nothing: the agent runs the steps. Grants are local to
// this machine.
package standinggrant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	SchemaVersion = 1
	maxIDBytes    = 256
	maxTextBytes  = 4 << 10
	maxCovered    = 16
	// TriggerAfterSliceCommit is the only trigger so far: after a commit that
	// completes a slice and passes its checks.
	TriggerAfterSliceCommit = "after_slice_commit"
	Disclaimer              = "A standing grant is a person's permission recorded on this machine; Continuity executes nothing. Run only the covered procedure revisions, only on the trigger, and stop if the grant is not active. An active grant does not guarantee harness permission; a harness denial does not revoke the grant."
)

// Grant states.
const (
	Active  = "active"
	Expired = "expired"
	Revoked = "revoked"
	// Void means a covered procedure has a newer revision, or its granted
	// revision is no longer accepted.
	Void = "void"
)

// Triggers lists the accepted trigger kinds.
var Triggers = []string{TriggerAfterSliceCommit}

// ErrNotFound means no grant has the requested ID.
var ErrNotFound = errors.New("standing grant not found")

// Covered is one procedure revision a grant covers.
type Covered struct {
	ProcedureID string `json:"procedure_id"`
	Revision    int    `json:"revision"`
	Title       string `json:"title,omitempty"`
	// LatestRevision is set when a newer revision voids the grant.
	LatestRevision int `json:"latest_revision,omitempty"`
}

// Revocation ends a grant.
type Revocation struct {
	Creator    string    `json:"creator"`
	Reason     string    `json:"reason,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Grant is one standing grant with its current state.
type Grant struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"project_id"`
	ProcedureID string            `json:"procedure_id"`
	Revision    int               `json:"procedure_revision"`
	Trigger     string            `json:"trigger"`
	Parameters  map[string]string `json:"parameters"`
	Covers      []Covered         `json:"covers"`
	ExpiresAt   *time.Time        `json:"expires_at,omitempty"`
	Creator     string            `json:"creator"`
	Reason      string            `json:"reason,omitempty"`
	Authority   string            `json:"authority"`
	RecordedAt  time.Time         `json:"recorded_at"`
	Revocation  *Revocation       `json:"revocation,omitempty"`
	State       string            `json:"state"`
	// VoidReason says which covered procedure changed.
	VoidReason string `json:"void_reason,omitempty"`
}

// Ref renders the granted procedure as procedure:ID@REVISION.
func (g Grant) Ref() string { return fmt.Sprintf("procedure:%s@%d", g.ProcedureID, g.Revision) }

// Request creates a grant. Revision zero means the latest accepted revision.
type Request struct {
	ProjectID   string
	ProcedureID string
	Revision    int
	Trigger     string
	Parameters  map[string]string
	ExpiresAt   *time.Time
	Creator     string
	Reason      string
}

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func plain(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("standing grant %s is required", name)
	}
	if len(value) > maxIDBytes || redact.String(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return "", fmt.Errorf("standing grant %s must be a plain value of at most %d bytes", name, maxIDBytes)
	}
	return value, nil
}

var procedureRef = regexp.MustCompile(`procedure:([A-Za-z0-9][A-Za-z0-9._-]*)`)

// NamedProcedures returns the procedures a procedure's steps name as
// procedure:ID, in first-mention order, excluding the procedure itself.
func NamedProcedures(value procedures.Procedure) []string {
	var result []string
	seen := map[string]bool{value.ID: true}
	for _, step := range value.Steps {
		for _, match := range procedureRef.FindAllStringSubmatch(step.Description, -1) {
			id := strings.TrimRight(match[1], ".")
			if !seen[id] {
				seen[id] = true
				result = append(result, id)
			}
		}
	}
	return result
}

// Create records a grant after checking that the procedure revision is
// accepted, that every procedure its steps name has an accepted revision, and
// that the parameters are ones the procedures declare.
func (s Service) Create(ctx context.Context, request Request) (Grant, error) {
	var err error
	if request.ProcedureID, err = plain("procedure ID", strings.TrimPrefix(strings.TrimSpace(request.ProcedureID), "procedure:"), true); err != nil {
		return Grant{}, err
	}
	if request.Creator, err = plain("creator", request.Creator, true); err != nil {
		return Grant{}, err
	}
	if request.Trigger == "" {
		request.Trigger = TriggerAfterSliceCommit
	}
	if !contains(Triggers, request.Trigger) {
		return Grant{}, fmt.Errorf("standing grant trigger must be one of %s, not %q", strings.Join(Triggers, ", "), request.Trigger)
	}
	request.Reason = strings.TrimSpace(redact.String(request.Reason))
	if len(request.Reason) > maxTextBytes {
		return Grant{}, fmt.Errorf("standing grant reason exceeds %d bytes", maxTextBytes)
	}
	now := s.now()
	if request.ExpiresAt != nil && !request.ExpiresAt.After(now) {
		return Grant{}, fmt.Errorf("standing grant expiry must be in the future")
	}
	store := procedures.Service{DB: s.DB}
	root, err := store.Get(ctx, request.ProjectID, request.ProcedureID, request.Revision)
	if err != nil {
		return Grant{}, err
	}
	if root == nil || root.Status != "accepted" {
		return Grant{}, fmt.Errorf("procedure:%s has no accepted revision%s", request.ProcedureID, revisionSuffix(request.Revision))
	}
	if latest, err := store.Latest(ctx, request.ProjectID, root.ID); err != nil {
		return Grant{}, err
	} else if latest.Revision != root.Revision {
		return Grant{}, fmt.Errorf("procedure:%s@%d is not its latest revision (%d); a grant on it would be void at once", root.ID, root.Revision, latest.Revision)
	}
	grant := Grant{ID: activity.NewID(), ProjectID: request.ProjectID, ProcedureID: root.ID, Revision: root.Revision, Trigger: request.Trigger, Parameters: map[string]string{}, ExpiresAt: request.ExpiresAt, Creator: request.Creator, Reason: request.Reason, Authority: "user_reported", RecordedAt: now, State: Active}
	grant.Covers = []Covered{{ProcedureID: root.ID, Revision: root.Revision, Title: root.Title}}
	declared := map[string]bool{}
	for _, parameter := range root.Parameters {
		declared[parameter.Name] = true
	}
	for _, id := range NamedProcedures(*root) {
		if len(grant.Covers) >= maxCovered {
			return Grant{}, fmt.Errorf("procedure:%s names more than %d procedures", root.ID, maxCovered-1)
		}
		named, err := store.Get(ctx, request.ProjectID, id, 0)
		if err != nil {
			return Grant{}, err
		}
		if named == nil {
			return Grant{}, fmt.Errorf("procedure:%s names procedure:%s, which has no accepted revision", root.ID, id)
		}
		for _, parameter := range named.Parameters {
			declared[parameter.Name] = true
		}
		grant.Covers = append(grant.Covers, Covered{ProcedureID: named.ID, Revision: named.Revision, Title: named.Title})
	}
	for name, value := range request.Parameters {
		if !declared[name] {
			return Grant{}, fmt.Errorf("parameter %q is not declared by the covered procedures", name)
		}
		if value, err = plain("parameter "+name, value, true); err != nil {
			return Grant{}, err
		}
		grant.Parameters[name] = value
	}
	parameters, err := json.Marshal(grant.Parameters)
	if err != nil {
		return Grant{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO standing_grants(id,project_id,procedure_id,procedure_revision,trigger_kind,parameters_json,expires_at,creator,reason,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		grant.ID, grant.ProjectID, grant.ProcedureID, grant.Revision, grant.Trigger, string(parameters), formatTime(grant.ExpiresAt), grant.Creator, grant.Reason, grant.Authority, now.Format(time.RFC3339Nano), SchemaVersion); err != nil {
		return Grant{}, err
	}
	for position, covered := range grant.Covers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO standing_grant_procedures(grant_id,position,project_id,procedure_id,procedure_revision) VALUES(?,?,?,?,?)`,
			grant.ID, position, grant.ProjectID, covered.ProcedureID, covered.Revision); err != nil {
			return Grant{}, err
		}
	}
	return grant, tx.Commit()
}

// Revoke ends a grant. Revoking one that is already revoked is refused.
func (s Service) Revoke(ctx context.Context, projectID, id, creator, reason string) (Grant, error) {
	var err error
	if creator, err = plain("creator", creator, true); err != nil {
		return Grant{}, err
	}
	reason = strings.TrimSpace(redact.String(reason))
	if len(reason) > maxTextBytes {
		return Grant{}, fmt.Errorf("standing grant reason exceeds %d bytes", maxTextBytes)
	}
	grant, err := s.Get(ctx, projectID, id)
	if err != nil {
		return Grant{}, err
	}
	if grant.Revocation != nil {
		return Grant{}, fmt.Errorf("standing grant %s was already revoked", grant.ID)
	}
	now := s.now()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO standing_grant_revocations(grant_id,creator,reason,recorded_at,schema_version) VALUES(?,?,?,?,?)`, grant.ID, creator, reason, now.Format(time.RFC3339Nano), SchemaVersion); err != nil {
		return Grant{}, err
	}
	grant.Revocation = &Revocation{Creator: creator, Reason: reason, RecordedAt: now}
	grant.State, grant.VoidReason = Revoked, ""
	return grant, nil
}

const grantSelect = `SELECT g.id,g.procedure_id,g.procedure_revision,g.trigger_kind,g.parameters_json,g.expires_at,g.creator,g.reason,g.authority,g.recorded_at,COALESCE(r.creator,''),COALESCE(r.reason,''),COALESCE(r.recorded_at,'')
FROM standing_grants g LEFT JOIN standing_grant_revocations r ON r.grant_id=g.id`

// Get returns one grant with its state.
func (s Service) Get(ctx context.Context, projectID, id string) (Grant, error) {
	grants, err := s.load(ctx, projectID, ` WHERE g.project_id=? AND g.id=?`, projectID, strings.TrimSpace(id))
	if err != nil {
		return Grant{}, err
	}
	if len(grants) == 0 {
		return Grant{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return grants[0], nil
}

// List returns active grants newest first, or every grant when all is set.
func (s Service) List(ctx context.Context, projectID string, all bool) ([]Grant, error) {
	grants, err := s.load(ctx, projectID, ` WHERE g.project_id=? ORDER BY g.recorded_at DESC, g.id`, projectID)
	if err != nil || all {
		return grants, err
	}
	result := []Grant{}
	for _, grant := range grants {
		if grant.State == Active {
			result = append(result, grant)
		}
	}
	return result, nil
}

// ForProcedure returns the active grants that cover a procedure.
func (s Service) ForProcedure(ctx context.Context, projectID, procedureID string) ([]Grant, error) {
	grants, err := s.List(ctx, projectID, false)
	if err != nil {
		return nil, err
	}
	result := []Grant{}
	for _, grant := range grants {
		for _, covered := range grant.Covers {
			if covered.ProcedureID == procedureID {
				result = append(result, grant)
				break
			}
		}
	}
	return result, nil
}

func (s Service) load(ctx context.Context, projectID, where string, args ...any) ([]Grant, error) {
	rows, err := s.DB.QueryContext(ctx, grantSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []Grant
	for rows.Next() {
		grant := Grant{ProjectID: projectID}
		var parameters, expires, recorded, revokedBy, revokedReason, revokedAt string
		if err := rows.Scan(&grant.ID, &grant.ProcedureID, &grant.Revision, &grant.Trigger, &parameters, &expires, &grant.Creator, &grant.Reason, &grant.Authority, &recorded, &revokedBy, &revokedReason, &revokedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(parameters), &grant.Parameters); err != nil {
			return nil, err
		}
		if grant.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, err
		}
		if expires != "" {
			at, err := time.Parse(time.RFC3339Nano, expires)
			if err != nil {
				return nil, err
			}
			grant.ExpiresAt = &at
		}
		if revokedAt != "" {
			at, err := time.Parse(time.RFC3339Nano, revokedAt)
			if err != nil {
				return nil, err
			}
			grant.Revocation = &Revocation{Creator: revokedBy, Reason: revokedReason, RecordedAt: at}
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	now := s.now()
	store := procedures.Service{DB: s.DB}
	for i := range grants {
		if err := s.covers(ctx, &grants[i]); err != nil {
			return nil, err
		}
		grants[i].State = Active
		switch {
		case grants[i].Revocation != nil:
			grants[i].State = Revoked
		case grants[i].ExpiresAt != nil && !now.Before(*grants[i].ExpiresAt):
			grants[i].State = Expired
		}
		for j, covered := range grants[i].Covers {
			latest, err := store.Latest(ctx, projectID, covered.ProcedureID)
			if err != nil {
				return nil, err
			}
			if latest == nil {
				continue
			}
			grants[i].Covers[j].Title = latest.Title
			reason := ""
			switch {
			case latest.Revision != covered.Revision:
				grants[i].Covers[j].LatestRevision = latest.Revision
				reason = fmt.Sprintf("procedure:%s is now at revision %d", covered.ProcedureID, latest.Revision)
			case latest.Status != "accepted":
				reason = fmt.Sprintf("procedure:%s@%d is %s", covered.ProcedureID, covered.Revision, latest.Status)
			}
			if reason != "" && grants[i].VoidReason == "" {
				grants[i].VoidReason = reason
				if grants[i].State == Active {
					grants[i].State = Void
				}
			}
		}
	}
	return grants, nil
}

func (s Service) covers(ctx context.Context, grant *Grant) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT procedure_id,procedure_revision FROM standing_grant_procedures WHERE grant_id=? ORDER BY position`, grant.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	grant.Covers = []Covered{}
	for rows.Next() {
		var covered Covered
		if err := rows.Scan(&covered.ProcedureID, &covered.Revision); err != nil {
			return err
		}
		grant.Covers = append(grant.Covers, covered)
	}
	return rows.Err()
}

// Notice is the bootstrap line: active grants with their trigger and expiry,
// and a count of void ones (a covered procedure changed) so the person can
// renew them. It returns "" when there are none of either.
func Notice(ctx context.Context, db *sql.DB, projectID string, now time.Time) (string, error) {
	grants, err := (Service{DB: db, Now: func() time.Time { return now }}).List(ctx, projectID, true)
	if err != nil {
		return "", err
	}
	var active []string
	void := 0
	for _, grant := range grants {
		switch grant.State {
		case Active:
			entry := grant.Ref() + " " + strings.ReplaceAll(grant.Trigger, "_", " ")
			if grant.ExpiresAt != nil {
				entry += " until " + grant.ExpiresAt.UTC().Format("2006-01-02")
			}
			if len(grant.Covers) > 1 {
				names := make([]string, 0, len(grant.Covers)-1)
				for _, covered := range grant.Covers[1:] {
					names = append(names, fmt.Sprintf("%s@%d", covered.ProcedureID, covered.Revision))
				}
				entry += " (covers " + strings.Join(names, ", ") + ")"
			}
			active = append(active, entry+", by "+grant.Creator)
		case Void:
			void++
		}
	}
	sort.Strings(active)
	if len(active) > 3 {
		active = append(active[:3], fmt.Sprintf("%d more", len(active)-3))
	}
	parts := []string{}
	if len(active) > 0 {
		parts = append(parts, strings.Join(active, "; ")+". Run these without asking when the trigger occurs. First read standing-grant show GRANT_ID --commands and preserve bound parameters and steps. Include grant ID, procedure revision, trigger, exact target and effect in any escalation. Harness permission is separate: record a denial without invalidating the grant; never route around it.")
	}
	if void > 0 {
		parts = append(parts, fmt.Sprintf("%d void because a covered procedure changed. Proactively report any affected planned work; inspect llewm standing-grant list --all and the current procedures, prepare exact repairs for review, then request renewal only if no active grant covers that work. Continue unaffected work; never silently skip an expected installation or renew a grant yourself.", void))
	}
	return strings.Join(parts, " "), nil
}

func revisionSuffix(revision int) string {
	if revision > 0 {
		return fmt.Sprintf(" %d", revision)
	}
	return ""
}

func formatTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
