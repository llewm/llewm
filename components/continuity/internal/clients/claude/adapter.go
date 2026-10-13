package claude

import (
	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/clients"
)

func Decode(projectID string, body []byte, storeRaw bool) (activity.Event, error) {
	return clients.Decode("claude-code", projectID, body, storeRaw)
}

func DecodeObservation(projectID string, body []byte, storeRaw bool) (clients.Observation, error) {
	return clients.DecodeObservation("claude-code", projectID, body, storeRaw)
}
