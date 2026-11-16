package memsync

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type RedactionProjectionPreview struct {
	SourceSHA256   string            `json:"source_sha256"`
	Projected      portable.Envelope `json:"projected"`
	ReviewRequired bool              `json:"review_required"`
	Applied        bool              `json:"applied"`
}

// PreviewRedactionProjection reads one exact tracked observation under the sync
// lock. Only the source hash and safe projected bytes leave this method. It does
// not import, publish, change operational rows, or retain a derived observation.
func (s Syncer) PreviewRedactionProjection(ctx context.Context, hash string) (RedactionProjectionPreview, error) {
	result := RedactionProjectionPreview{ReviewRequired: true}
	b, err := hex.DecodeString(hash)
	if err != nil || len(b) != 32 || strings.ToLower(hash) != hash {
		return result, fmt.Errorf("source hash must be lowercase SHA256")
	}
	repo, err := s.repository(ctx)
	if err != nil {
		return result, err
	}
	lock, err := lockFile(filepath.Join(s.Clone, "continuity-sync.lock"))
	if err != nil {
		return result, err
	}
	defer lock.Close()
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil {
		return result, err
	}
	if identity == nil {
		return result, fmt.Errorf("project has no portable identity")
	}
	bodies, err := s.trackedBlockObservations(ctx, repo, identity.LineageID, hash)
	if err != nil {
		return result, err
	}
	body, ok := bodies[hash]
	if !ok {
		return result, fmt.Errorf("source observation is not tracked in this lineage")
	}
	projection, err := portable.PreviewRedactionProjection(body)
	if err != nil {
		return result, err
	}
	result.SourceSHA256, result.Projected = projection.SourceSHA256, projection.Projected.Envelope
	return result, nil
}
