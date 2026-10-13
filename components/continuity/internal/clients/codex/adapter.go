package codex

import (
	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/clients"
)

func Decode(projectID string, body []byte, storeRaw bool) (activity.Event, error) {
	return clients.Decode("codex", projectID, body, storeRaw)
}

func DecodeObservation(projectID string, body []byte, storeRaw bool) (clients.Observation, error) {
	return clients.DecodeObservation("codex", projectID, body, storeRaw)
}
