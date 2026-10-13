package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
)

// Cross-machine leases (opt-in with config.toml [leases] remote). Each scope's
// grants are a chain of events on the memory remote, written with an atomic
// compare-and-swap, so two machines cannot both be granted a scope. The device
// tables mirror the remote: every remote operation first copies the remote's
// events in, decides against the mirror, writes the next event to the remote,
// and only then records it locally. When the remote cannot be reached, an
// advisory scope gets a local-only lease that a later sync publishes, and a
// required scope refuses.

// RemoteEvent is one grant event as the remote stores it: the grant's identity
// and the state the event leaves it in.
type RemoteEvent struct {
	GrantID       string    `json:"grant_id"`
	ScopeKind     string    `json:"scope_kind"`
	ScopeID       string    `json:"scope_id"`
	Token         int64     `json:"token"`
	Holder        string    `json:"holder"`
	SessionID     string    `json:"session_id,omitempty"`
	InstanceID    string    `json:"instance_id"`
	AcquiredAt    time.Time `json:"acquired_at"`
	Sequence      int       `json:"sequence"`
	Action        string    `json:"action"`
	ExpiresAt     time.Time `json:"expires_at"`
	MarginMS      int64     `json:"margin_ms"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
	// Version is the remote's identifier for this event (a commit); it is not
	// part of the stored event.
	Version string `json:"-"`
}

// lease is the grant as this event leaves it, at now.
func (e RemoteEvent) lease(projectID string, now time.Time) Lease {
	value := Lease{ID: e.GrantID, ProjectID: projectID, ScopeKind: e.ScopeKind, ScopeID: e.ScopeID, Token: e.Token, Holder: e.Holder, SessionID: e.SessionID, InstanceID: e.InstanceID,
		AcquiredAt: e.AcquiredAt, ExpiresAt: e.ExpiresAt, ValidUntil: e.ExpiresAt.Add(-time.Duration(e.MarginMS) * time.Millisecond), Renewals: e.Sequence - 1, State: Held, OnRemote: true}
	switch {
	case e.Action == "release":
		at := e.RecordedAt
		value.ReleasedAt, value.Renewals, value.State = &at, e.Sequence-2, Released
	case !now.Before(e.ExpiresAt):
		value.State = Expired
	}
	return value
}

// Scope renders the event's scope as KIND:ID.
func (e RemoteEvent) Scope() string { return e.ScopeKind + ":" + e.ScopeID }

// Validate checks an event read from or written to the remote.
func (e RemoteEvent) Validate() error {
	if !oneOf(e.ScopeKind, ScopeKinds...) || e.ScopeID == "" || e.GrantID == "" || e.Holder == "" || e.Token < 1 || e.Sequence < 1 || e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("remote lease event for %s is incomplete", e.Scope())
	}
	for _, value := range []string{e.ScopeID, e.GrantID, e.Holder, e.SessionID, e.InstanceID} {
		if _, err := plain("field", value, false); err != nil {
			return fmt.Errorf("remote lease event for %s: %w", e.Scope(), err)
		}
	}
	if !oneOf(e.Action, "acquire", "renew", "release") || (e.Action == "acquire") != (e.Sequence == 1) {
		return fmt.Errorf("remote lease event for %s has action %q at sequence %d", e.Scope(), e.Action, e.Sequence)
	}
	if e.AcquiredAt.IsZero() || e.ExpiresAt.IsZero() || e.RecordedAt.IsZero() || e.MarginMS < 0 {
		return fmt.Errorf("remote lease event for %s needs its times", e.Scope())
	}
	return nil
}

// RemoteScope is one scope on the remote: its current version and its recent
// events, newest first.
type RemoteScope struct {
	Version string
	Events  []RemoteEvent
}

// Remote stores lease events on the memory remote.
type Remote interface {
	// Scopes reads every scope of this project from the remote, keyed KIND:ID.
	Scopes(ctx context.Context) (map[string]RemoteScope, error)
	// Write makes event the scope's next state if the scope is still at
	// expected ("" when it has no events yet) and returns the new version.
	Write(ctx context.Context, expected string, event RemoteEvent) (string, error)
}

var (
	// ErrRemoteMoved means the scope changed on the remote since it was read.
	ErrRemoteMoved = errors.New("the remote lease changed since it was read")
	// ErrRemoteUnreachable means the memory remote could not be reached.
	ErrRemoteUnreachable = errors.New("the memory remote could not be reached")
)

// remoteAttempts bounds how often an operation rereads a scope that moved.
const remoteAttempts = 3

func (l Lease) event(sequence int, action string, marginMS int64, at time.Time) RemoteEvent {
	return RemoteEvent{GrantID: l.ID, ScopeKind: l.ScopeKind, ScopeID: l.ScopeID, Token: l.Token, Holder: l.Holder, SessionID: l.SessionID, InstanceID: l.InstanceID,
		AcquiredAt: l.AcquiredAt.UTC(), Sequence: sequence, Action: action, ExpiresAt: l.ExpiresAt.UTC(), MarginMS: marginMS, RecordedAt: at.UTC(), SchemaVersion: SchemaVersion}
}

// Refresh copies the remote's lease events into the device tables and returns
// notes on events it could not mirror.
func (s Service) Refresh(ctx context.Context, projectID string) ([]string, error) {
	scopes, err := s.Remote.Scopes(ctx)
	if err != nil {
		return nil, err
	}
	return s.mirror(ctx, projectID, scopes)
}

// mirror records the remote's events locally, oldest first. A remote grant
// whose token is already used here by a different grant (two machines granted
// it while apart) is skipped and noted.
func (s Service) mirror(ctx context.Context, projectID string, scopes map[string]RemoteScope) ([]string, error) {
	var notes []string
	keys := make([]string, 0, len(scopes))
	for key := range scopes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	err := s.write(ctx, func(tx *sql.Tx) error {
		for _, key := range keys {
			events := scopes[key].Events
			for index := len(events) - 1; index >= 0; index-- {
				note, err := mirrorEvent(ctx, tx, projectID, events[index], s.now())
				if err != nil {
					return err
				}
				if note != "" {
					notes = append(notes, note)
				}
			}
		}
		return nil
	})
	return notes, err
}

func mirrorEvent(ctx context.Context, tx *sql.Tx, projectID string, event RemoteEvent, now time.Time) (string, error) {
	if err := event.Validate(); err != nil {
		return err.Error(), nil
	}
	var grantID string
	var maxSequence int
	err := tx.QueryRowContext(ctx, `SELECT g.id, COALESCE((SELECT MAX(sequence) FROM lease_events WHERE grant_id=g.id),0) FROM lease_grants g WHERE g.project_id=? AND g.scope_kind=? AND g.scope_id=? AND g.token=?`, projectID, event.ScopeKind, event.ScopeID, event.Token).Scan(&grantID, &maxSequence)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO lease_grants(id,project_id,scope_kind,scope_id,token,holder,session_id,instance_id,acquired_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			event.GrantID, projectID, event.ScopeKind, event.ScopeID, event.Token, event.Holder, event.SessionID, event.InstanceID, event.AcquiredAt.UTC().Format(time.RFC3339Nano), SchemaVersion); err != nil {
			return "", err
		}
		grantID = event.GrantID
	case err != nil:
		return "", err
	case grantID != event.GrantID:
		return fmt.Sprintf("%s token %d was granted both here and on the remote (to %s on %s) while apart; the remote grant was not mirrored", event.Scope(), event.Token, event.Holder, event.InstanceID), nil
	}
	if event.Sequence > maxSequence {
		if err := insertEvent(ctx, tx, grantID, event.Sequence, event.Action, event.ExpiresAt, time.Duration(event.MarginMS)*time.Millisecond, event.RecordedAt); err != nil {
			return "", err
		}
	}
	return "", markRemote(ctx, tx, grantID, event.Sequence, event.Version, now)
}

func markRemote(ctx context.Context, tx *sql.Tx, grantID string, sequence int, version string, now time.Time) error {
	if version == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO lease_remote_events(grant_id,sequence,version,observed_at) VALUES(?,?,?,?)`, grantID, sequence, version, now.UTC().Format(time.RFC3339Nano))
	return err
}

// recordRemote stores an event this machine just wrote to the remote: a new
// grant (sequence 1) or the next event of a mirrored one.
func (s Service) recordRemote(ctx context.Context, projectID string, event RemoteEvent) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if event.Sequence == 1 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO lease_grants(id,project_id,scope_kind,scope_id,token,holder,session_id,instance_id,acquired_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				event.GrantID, projectID, event.ScopeKind, event.ScopeID, event.Token, event.Holder, event.SessionID, event.InstanceID, event.AcquiredAt.UTC().Format(time.RFC3339Nano), SchemaVersion); err != nil {
				return err
			}
		}
		if err := insertEvent(ctx, tx, event.GrantID, event.Sequence, event.Action, event.ExpiresAt, time.Duration(event.MarginMS)*time.Millisecond, event.RecordedAt); err != nil {
			return err
		}
		return markRemote(ctx, tx, event.GrantID, event.Sequence, event.Version, s.now())
	})
}

// read loads the scope's latest grant and this machine's instance.
func (s Service) read(ctx context.Context, request Request) (*Lease, string, error) {
	instance, err := instanceID(ctx, s.DB, request.ProjectID)
	if err != nil {
		return nil, "", err
	}
	existing, err := current(ctx, s.DB, request.ProjectID, request.ScopeKind, request.ScopeID, s.now())
	return existing, instance, err
}

// offline handles an unreachable remote: a required scope refuses, and an
// advisory one runs the operation locally and warns that it is local-only.
func (s Service) offline(ctx context.Context, request Request, action string, cause error) (Result, error) {
	scope := request.ScopeKind + ":" + request.ScopeID
	if s.Policy.Required(request.ScopeKind, request.ScopeID) {
		return Result{}, fmt.Errorf("%w: %s requires a lease and its %s needs the memory remote, which could not be reached (%v)", ErrLeaseRequired, scope, action, cause)
	}
	var result Result
	var err error
	switch action {
	case "acquire":
		result, err = s.acquireLocal(ctx, request)
	case "renew":
		result.Lease, err = s.renewLocal(ctx, request)
	default:
		result.Lease, err = s.releaseLocal(ctx, request)
	}
	if err != nil {
		return Result{}, err
	}
	result.Warning = fmt.Sprintf("the memory remote could not be reached, so this %s of %s is local-only: other machines do not see it until a sync publishes it (%v)", action, scope, cause)
	return result, nil
}

func (s Service) acquireRemote(ctx context.Context, request Request) (Result, error) {
	for attempt := 0; attempt < remoteAttempts; attempt++ {
		started := time.Now()
		scopes, err := s.Remote.Scopes(ctx)
		if errors.Is(err, ErrRemoteUnreachable) {
			return s.offline(ctx, request, "acquire", err)
		} else if err != nil {
			return Result{}, err
		}
		if _, err := s.mirror(ctx, request.ProjectID, scopes); err != nil {
			return Result{}, err
		}
		existing, instance, err := s.read(ctx, request)
		if err != nil {
			return Result{}, err
		}
		now := s.now()
		var result Result
		var event RemoteEvent
		switch {
		case existing != nil && existing.State == Held && sameHolder(*existing, request, instance):
			result = Result{Lease: *existing, Renewed: true}
			result.Lease.ExpiresAt = now.Add(request.TTL)
			event = result.Lease.event(existing.Renewals+2, "renew", 0, now)
		case blocks(existing, instance, now, s.skew()):
			return Result{}, &ConflictError{Current: *existing, Instance: instance}
		default:
			token, err := s.nextToken(ctx, request, scopes)
			if err != nil {
				return Result{}, err
			}
			result.Lease = Lease{ID: activity.NewID(), ProjectID: request.ProjectID, ScopeKind: request.ScopeKind, ScopeID: request.ScopeID, Token: token, Holder: request.Holder, SessionID: request.SessionID, InstanceID: instance, AcquiredAt: now, ExpiresAt: now.Add(request.TTL)}
			event = result.Lease.event(1, "acquire", 0, now)
		}
		if err := s.publish(ctx, request, scopes, &event, &result.Lease, started); errors.Is(err, ErrRemoteMoved) {
			continue
		} else if errors.Is(err, ErrRemoteUnreachable) {
			return s.offline(ctx, request, "acquire", err)
		} else if err != nil {
			return Result{}, err
		}
		return result, nil
	}
	return Result{}, fmt.Errorf("%s kept changing on the memory remote; try again", request.ScopeKind+":"+request.ScopeID)
}

// nextToken is one more than the highest token of the scope seen here or on
// the remote, so tokens only increase across machines.
func (s Service) nextToken(ctx context.Context, request Request, scopes map[string]RemoteScope) (int64, error) {
	var token int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(token),0) FROM lease_grants WHERE project_id=? AND scope_kind=? AND scope_id=?`, request.ProjectID, request.ScopeKind, request.ScopeID).Scan(&token); err != nil {
		return 0, err
	}
	if remote := scopes[request.ScopeKind+":"+request.ScopeID]; len(remote.Events) > 0 && remote.Events[0].Token > token {
		token = remote.Events[0].Token
	}
	return token + 1, nil
}

// publish writes event to the remote against the version read, then records it
// locally and updates value. The margin covers the whole round trip since
// started.
func (s Service) publish(ctx context.Context, request Request, scopes map[string]RemoteScope, event *RemoteEvent, value *Lease, started time.Time) error {
	var marginValue time.Duration
	if event.Action != "release" {
		// Twice the read so far, allowing for the write that follows.
		marginValue = margin(request.TTL, 2*time.Since(started))
	}
	event.MarginMS = marginValue.Milliseconds()
	version, err := s.Remote.Write(ctx, scopes[request.ScopeKind+":"+request.ScopeID].Version, *event)
	if err != nil {
		return err
	}
	event.Version = version
	if err := s.recordRemote(ctx, request.ProjectID, *event); err != nil {
		return fmt.Errorf("the lease is on the memory remote but recording it here failed: %w", err)
	}
	value.ExpiresAt, value.ValidUntil = event.ExpiresAt, event.ExpiresAt.Add(-marginValue)
	value.Renewals, value.OnRemote, value.State = event.Sequence-1, true, Held
	if event.Action == "release" {
		at := event.RecordedAt
		value.ReleasedAt, value.Renewals, value.State = &at, event.Sequence-2, Released
	}
	return nil
}

// changeRemote renews or releases the caller's lease on the remote.
func (s Service) changeRemote(ctx context.Context, request Request, action string) (Result, error) {
	for attempt := 0; attempt < remoteAttempts; attempt++ {
		started := time.Now()
		scopes, err := s.Remote.Scopes(ctx)
		if errors.Is(err, ErrRemoteUnreachable) {
			return s.offline(ctx, request, action, err)
		} else if err != nil {
			return Result{}, err
		}
		if _, err := s.mirror(ctx, request.ProjectID, scopes); err != nil {
			return Result{}, err
		}
		existing, err := held(ctx, s.DB, request, s.now(), action == "release")
		if err != nil {
			return Result{}, err
		}
		now := s.now()
		value := *existing
		if action == "renew" {
			value.ExpiresAt = now.Add(request.TTL)
		}
		sequence := value.Renewals + 2
		event := value.event(sequence, action, 0, now)
		if err := s.publish(ctx, request, scopes, &event, &value, started); errors.Is(err, ErrRemoteMoved) {
			continue
		} else if errors.Is(err, ErrRemoteUnreachable) {
			return s.offline(ctx, request, action, err)
		} else if err != nil {
			return Result{}, err
		}
		return Result{Lease: value}, nil
	}
	return Result{}, fmt.Errorf("%s kept changing on the memory remote; try again", request.ScopeKind+":"+request.ScopeID)
}

// Reconcile mirrors the remote, then publishes this machine's local-only
// grant events: each scope's latest grant written here whose latest event is
// not on the remote. A grant is published when the remote has no newer token
// for its scope; otherwise it stays local and is noted, since another machine
// was granted the scope meanwhile.
func (s Service) Reconcile(ctx context.Context, projectID string) ([]string, error) {
	scopes, err := s.Remote.Scopes(ctx)
	if err != nil {
		return nil, err
	}
	notes, err := s.mirror(ctx, projectID, scopes)
	if err != nil {
		return notes, err
	}
	instance, err := instanceID(ctx, s.DB, projectID)
	if err != nil {
		return notes, err
	}
	leases, err := list(ctx, s.DB, projectID, true, s.now())
	if err != nil {
		return notes, err
	}
	for _, value := range leases {
		if value.OnRemote || value.InstanceID != instance {
			continue
		}
		remote := scopes[value.Scope()]
		if len(remote.Events) > 0 && remote.Events[0].GrantID != value.ID {
			newest := remote.Events[0].lease(projectID, s.now())
			if newest.Token >= value.Token || blocks(&newest, instance, s.now(), s.skew()) {
				notes = append(notes, fmt.Sprintf("local-only lease %s token %d of %s was not published: the remote has token %d of %s", value.Scope(), value.Token, value.HolderLabel(), newest.Token, holderOn(newest, instance)))
				continue
			}
		}
		event, err := latestEvent(ctx, s.DB, value)
		if err != nil {
			return notes, err
		}
		version, err := s.Remote.Write(ctx, remote.Version, event)
		if errors.Is(err, ErrRemoteMoved) {
			notes = append(notes, fmt.Sprintf("local-only lease %s token %d: the scope changed on the remote while publishing; the next sync tries again", value.Scope(), value.Token))
			continue
		} else if err != nil {
			return notes, err
		}
		if err := s.write(ctx, func(tx *sql.Tx) error { return markRemote(ctx, tx, value.ID, event.Sequence, version, s.now()) }); err != nil {
			return notes, err
		}
	}
	return notes, nil
}

// latestEvent rebuilds a grant's latest event for the remote.
func latestEvent(ctx context.Context, db queryer, value Lease) (RemoteEvent, error) {
	var action, expires, recorded string
	var sequence int
	var marginMS int64
	if err := db.QueryRowContext(ctx, `SELECT sequence,action,expires_at,margin_ms,recorded_at FROM lease_events WHERE grant_id=? ORDER BY sequence DESC LIMIT 1`, value.ID).Scan(&sequence, &action, &expires, &marginMS, &recorded); err != nil {
		return RemoteEvent{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return RemoteEvent{}, err
	}
	at, err := time.Parse(time.RFC3339Nano, recorded)
	if err != nil {
		return RemoteEvent{}, err
	}
	value.ExpiresAt = expiresAt
	return value.event(sequence, action, marginMS, at), nil
}
