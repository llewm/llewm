package release

import "time"

const SchemaVersion = 1

type Entry struct {
	Position     int      `json:"position"`
	Category     string   `json:"category"`
	Wording      string   `json:"wording"`
	NodeID       string   `json:"node_id"`
	NodeRevision int      `json:"node_revision"`
	NarrativeIDs []string `json:"narrative_ids"`
}

type Record struct {
	ProjectID     string     `json:"project_id"`
	ID            string     `json:"id"`
	Revision      int        `json:"revision"`
	Version       string     `json:"version"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	Reviewer      string     `json:"reviewer,omitempty"`
	PublishedAt   *time.Time `json:"published_at,omitempty"`
	ContentSHA256 string     `json:"content_sha256"`
	Entries       []Entry    `json:"entries"`
	Authority     string     `json:"authority"`
	RecordedAt    time.Time  `json:"recorded_at"`
	SchemaVersion int        `json:"schema_version"`
}

type View struct {
	SchemaVersion int      `json:"schema_version"`
	ProjectID     string   `json:"project_id"`
	Releases      []Record `json:"releases"`
	Total         int      `json:"total"`
	Limit         int      `json:"limit"`
	Truncated     bool     `json:"truncated"`
	Authority     string   `json:"authority"`
	Disclaimer    string   `json:"disclaimer"`
}

type Projection struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	Markdown      string `json:"markdown"`
	SHA256        string `json:"sha256"`
	Authority     string `json:"authority"`
	Draft         bool   `json:"draft"`
	Disclaimer    string `json:"disclaimer"`
}

type ProjectionCheck struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	Path          string `json:"path"`
	State         string `json:"state"`
	ExpectedHash  string `json:"expected_hash"`
	ActualHash    string `json:"actual_hash,omitempty"`
	Authority     string `json:"authority"`
	Disclaimer    string `json:"disclaimer"`
}
