package coordination

import (
	"context"
	"errors"
	"time"
)

var ErrProjectFull = errors.New("project event limit reached")
var ErrProjectBusy = errors.New("project wait limit reached")

func ValidBrokerID(id string) bool { return safeID(id) }

type ProjectMember struct {
	ID        string     `json:"id"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ConfigureProject applies immutable serving policy before the store is published.
func (s *Store) ConfigureProject(members []ProjectMember, maxEvents int64, maxWaits int) error {
	if s.brokerScope != "shared" || maxEvents <= 0 || maxWaits <= 0 || maxWaits > 1024 || members == nil {
		return errors.New("invalid shared project limits or missing members")
	}
	allowed := make(map[string]time.Time, len(members))
	for _, member := range members {
		if !safeID(member.ID) {
			return errors.New("invalid project member")
		}
		if _, ok := allowed[member.ID]; ok {
			return errors.New("duplicate project member")
		}
		var expiry time.Time
		if member.ExpiresAt != nil {
			expiry = *member.ExpiresAt
			if expiry.IsZero() {
				return errors.New("invalid membership expiry")
			}
		}
		allowed[member.ID] = expiry
	}
	s.projectMembers = allowed
	s.maxEvents = maxEvents
	s.waitSlots = make(chan struct{}, maxWaits)
	return nil
}

func (s *Store) brokerMember(ctx context.Context, q queryer, token string) (Member, error) {
	m, err := authenticate(ctx, q, token)
	if err != nil {
		return Member{}, err
	}
	if s.projectMembers != nil {
		expiry, ok := s.projectMembers[m.ID]
		if !ok || (!expiry.IsZero() && !s.now().Before(expiry)) {
			return Member{}, ErrForbidden
		}
	}
	return m, nil
}
