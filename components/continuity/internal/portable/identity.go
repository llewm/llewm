package portable

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

var (
	lineagePattern  = regexp.MustCompile(`^lineage:[0-9a-f]{32}$`)
	instancePattern = regexp.MustCompile(`^instance:[0-9a-f]{32}$`)
)

type Identity struct {
	ProjectID         string    `json:"project_id"`
	LineageID         string    `json:"lineage_id"`
	InstanceID        string    `json:"instance_id"`
	AssociationSource string    `json:"association_source"`
	Authority         string    `json:"authority"`
	AssociatedAt      time.Time `json:"associated_at"`
	SchemaVersion     int       `json:"schema_version"`
}

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s Service) Get(ctx context.Context, projectID string) (*Identity, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	var value Identity
	var associated string
	err := s.DB.QueryRowContext(ctx, `SELECT project_id,lineage_id,instance_id,association_source,authority,associated_at,schema_version FROM project_memory_identities WHERE project_id=?`, projectID).Scan(
		&value.ProjectID, &value.LineageID, &value.InstanceID, &value.AssociationSource,
		&value.Authority, &associated, &value.SchemaVersion,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value.AssociatedAt, err = time.Parse(time.RFC3339Nano, associated)
	if err != nil {
		return nil, fmt.Errorf("decode portable identity time: %w", err)
	}
	return &value, nil
}

// Initialize creates one immutable local identity. An empty lineage generates a
// new lineage; a supplied lineage is an explicit adoption. Repeating the same
// request is idempotent, while attempting to change lineages fails visibly.
func (s Service) Initialize(ctx context.Context, projectID, lineageID string) (Identity, error) {
	projectID = strings.TrimSpace(projectID)
	lineageID = strings.TrimSpace(lineageID)
	if projectID == "" {
		return Identity{}, fmt.Errorf("project ID is required")
	}
	requestedLineage := lineageID
	source := "explicit_adoption"
	if lineageID == "" {
		var err error
		lineageID, err = randomID("lineage:")
		if err != nil {
			return Identity{}, err
		}
		source = "generated"
	} else if !lineagePattern.MatchString(lineageID) {
		return Identity{}, fmt.Errorf("lineage ID must match lineage:<32 lowercase hex characters>")
	}
	instanceID, err := randomID("instance:")
	if err != nil {
		return Identity{}, err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	value := Identity{
		ProjectID: projectID, LineageID: lineageID, InstanceID: instanceID,
		AssociationSource: source, Authority: "caller_asserted", AssociatedAt: now,
		SchemaVersion: SchemaVersion,
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO project_memory_identities(project_id,lineage_id,instance_id,association_source,authority,associated_at,schema_version) VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id) DO NOTHING`,
		value.ProjectID, value.LineageID, value.InstanceID, value.AssociationSource,
		value.Authority, value.AssociatedAt.Format(time.RFC3339Nano), value.SchemaVersion,
	)
	if err != nil {
		return Identity{}, err
	}
	stored, err := s.Get(ctx, projectID)
	if err != nil {
		return Identity{}, err
	}
	if stored == nil {
		return Identity{}, fmt.Errorf("portable identity was not stored")
	}
	if requestedLineage != "" && stored.LineageID != requestedLineage {
		return Identity{}, fmt.Errorf("project is already associated with lineage %s", stored.LineageID)
	}
	return *stored, nil
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate portable identity: %w", err)
	}
	return prefix + hex.EncodeToString(value), nil
}
