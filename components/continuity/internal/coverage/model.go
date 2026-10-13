// Package coverage records, per requirement and client or platform, whether
// the requirement is met, how, and on what evidence. Requirements are authored
// in a Markdown file in the repository; cells are append-only observations
// that reference a requirement by ID, name the machine store that made them,
// and travel through History sync. A requirement with no cell
// is unknown, and a cell whose requirement is gone is orphaned.
package coverage

import "time"

const (
	SchemaVersion = 1

	// DefaultRequirementsPath is relative to the project root.
	DefaultRequirementsPath = "docs/requirements.md"

	// Disclaimer is attached to every read.
	Disclaimer = "Coverage cells are caller-reported observations, not proof that a client delivers the requirement. Verification names how strongly each was checked."
)

// Statuses lists the statuses a cell can record. A requirement without a cell
// is reported as unknown; unknown is never recorded.
var Statuses = []string{"unsupported", "partial", "supported", "failing"}

// Verifications lists verification levels from weakest to strongest.
// inferred: reasoned from documentation or code; reported: the client or a
// person said so; observed: seen working in a real session; tested: an
// automated check exercised it.
var Verifications = []string{"inferred", "reported", "observed", "tested"}

// Requirement is one marked section of the requirements file.
type Requirement struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Scope []string `json:"scope,omitempty"`
	Body  string   `json:"body"`
}

// Cell is one append-only observation for a requirement on a client and
// platform. The latest cell per (requirement, client, platform) is current.
type Cell struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	RequirementID  string   `json:"requirement_id"`
	Client         string   `json:"client"`
	Platform       string   `json:"platform"`
	ClientVersion  string   `json:"client_version,omitempty"`
	Status         string   `json:"status"`
	Verification   string   `json:"verification"`
	Implementation string   `json:"implementation,omitempty"`
	Refs           []string `json:"refs"`
	Creator        string   `json:"creator"`
	Model          string   `json:"model,omitempty"`
	Authority      string   `json:"authority"`
	// InstanceID names the store (one project on one machine) that observed
	// the cell; cells sync, so the matrix can mix machines.
	InstanceID    string    `json:"instance_id,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

// Row is one requirement with its current cells.
type Row struct {
	Requirement
	Status string `json:"status"` // worst current cell status, or unknown
	Cells  []Cell `json:"cells"`
}

// Report is the matrix: requirements as rows, current cells within them.
type Report struct {
	SchemaVersion    int    `json:"schema_version"`
	RequirementsPath string `json:"requirements_path"`
	// InstanceID is this store's instance, so a reader can tell cells observed
	// here from cells synced from other machines.
	InstanceID string   `json:"instance_id,omitempty"`
	Rows       []Row    `json:"rows"`
	Orphaned   []Cell   `json:"orphaned"`
	Problems   []string `json:"problems"`
	Returned   int      `json:"returned"`
	Total      int      `json:"total"`
	Truncated  bool     `json:"truncated"`
	Authority  string   `json:"authority"`
	Disclaimer string   `json:"disclaimer"`
}

// Stale is a current cell whose verified client version differs from the
// version most recently observed for that client.
type Stale struct {
	Cell      Cell   `json:"cell"`
	Installed string `json:"installed_version"`
}
