package sessions

import "time"

type Session struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Client    string     `json:"client"`
	Model     string     `json:"model,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Status    string     `json:"status"`
}
