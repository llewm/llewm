// Package deliverystatus reads the reporting adapter's versioned metadata
// snapshot. It never opens its configuration/outbox or invokes delivery.
package deliverystatus

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const MaxBytes = 1 << 20
const MaxReports = 1000

type Receipt struct {
	Issue      int       `json:"issue"`
	URL        string    `json:"url"`
	Outcome    string    `json:"outcome"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Summary struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	ReportID  string    `json:"report_id"`
	SourceRef string    `json:"source_ref"`
	Target    string    `json:"target"`
	Status    string    `json:"status"`
	QueuedAt  time.Time `json:"queued_at"`
	Receipt   *Receipt  `json:"receipt,omitempty"`
}

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	ObservedAt    time.Time `json:"observed_at"`
	Authority     string    `json:"authority"`
	Reports       []Summary `json:"reports"`
}

// Read scopes a supplied snapshot to this project. Empty path means no snapshot
// was supplied, not that all reports are delivered or that the outbox is empty.
func Read(path, projectID string) (*Snapshot, error) {
	if path == "" {
		return nil, nil
	}
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("delivery snapshot must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBytes {
		return nil, fmt.Errorf("delivery snapshot exceeds %d bytes", MaxBytes)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return nil, fmt.Errorf("decode delivery snapshot: %w", err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.ObservedAt.IsZero() || snapshot.Authority != "machine_observed" {
		return nil, fmt.Errorf("unsupported delivery snapshot version or missing observation provenance")
	}
	if len(snapshot.Reports) > MaxReports {
		return nil, fmt.Errorf("delivery snapshot exceeds %d reports", MaxReports)
	}
	reports := []Summary{}
	for _, r := range snapshot.Reports {
		if r.ProjectID == "" || r.ID == "" || r.ReportID == "" || r.SourceRef == "" || r.QueuedAt.IsZero() || (r.Status != "pending" && r.Status != "delivered") {
			return nil, fmt.Errorf("invalid delivery summary")
		}
		if r.Status == "delivered" && (r.Receipt == nil || r.Receipt.RecordedAt.IsZero() || r.Receipt.URL == "" || r.Receipt.Issue < 1) {
			return nil, fmt.Errorf("delivered summary lacks a receipt")
		}
		if r.ProjectID != projectID {
			continue
		}
		r.ID = redact.String(r.ID)
		r.ReportID = redact.String(r.ReportID)
		r.SourceRef = redact.String(r.SourceRef)
		r.Target = redact.String(r.Target)
		if r.Receipt != nil {
			r.Receipt.URL = redact.String(r.Receipt.URL)
			r.Receipt.Outcome = redact.String(r.Receipt.Outcome)
		}
		reports = append(reports, r)
	}
	snapshot.Reports = reports
	return &snapshot, nil
}
