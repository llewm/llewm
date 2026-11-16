package memsync

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
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

// RedactionReviewRequest selects tracked observations to review: listed source
// hashes, or with All every tracked observation that current admission rejects
// and a redaction projection repairs. Without Apply nothing is written.
type RedactionReviewRequest struct {
	Sources  []string
	All      bool
	Apply    bool
	Reviewer string
	Reason   string
}

type RedactionReviewCandidate struct {
	SourceSHA256    string `json:"source_sha256"`
	ProjectedSHA256 string `json:"projected_sha256"`
	RecordType      string `json:"record_type"`
	RecordID        string `json:"record_id"`
	RecordRevision  int    `json:"record_revision"`
}

// RedactionReviewResult lists candidates by hash and safe projected identity
// only. Other records that fail alone are counted, never described, because
// their content may hold a credential. Unsupported counts families projection
// never rewrites, including records that decode only with their dependencies;
// Unrepairable counts supported families a projection does not fix.
type RedactionReviewResult struct {
	Candidates   []RedactionReviewCandidate `json:"candidates"`
	Unsupported  int                        `json:"unsupported"`
	Unrepairable int                        `json:"unrepairable"`
	Applied      bool                       `json:"applied"`
}

// ReviewRedactionProjections records host-local reviews of tracked sources.
// It reads committed transport bytes under the sync lock and never imports,
// publishes, or changes operational rows; the next sync admits reviewed records.
func (s Syncer) ReviewRedactionProjections(ctx context.Context, request RedactionReviewRequest) (RedactionReviewResult, error) {
	result := RedactionReviewResult{Candidates: []RedactionReviewCandidate{}}
	if request.All == (len(request.Sources) > 0) {
		return result, fmt.Errorf("select either --all or one or more --source hashes")
	}
	if request.Apply && (strings.TrimSpace(request.Reviewer) == "" || strings.TrimSpace(request.Reason) == "") {
		return result, fmt.Errorf("applying a redaction review needs --reviewer and --reason")
	}
	for _, hash := range request.Sources {
		if b, err := hex.DecodeString(hash); err != nil || len(b) != 32 || strings.ToLower(hash) != hash {
			return result, fmt.Errorf("source hash must be lowercase SHA256")
		}
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
	var bodies map[string][]byte
	if request.All {
		bodies, err = s.readTrackedObservations(ctx, repo, identity.LineageID, func(string) bool { return true })
	} else {
		bodies, err = s.trackedBlockObservations(ctx, repo, identity.LineageID, request.Sources...)
	}
	if err != nil {
		return result, err
	}
	hashes := make([]string, 0, len(bodies))
	for hash := range bodies {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	sources := map[string][]byte{}
	for _, hash := range hashes {
		projection, err := portable.PreviewRedactionProjection(bodies[hash])
		if err != nil && !request.All {
			return result, err
		}
		switch {
		case errors.Is(err, portable.ErrSourceAdmissible):
			continue
		case errors.Is(err, portable.ErrProjectionUnsupported):
			result.Unsupported++
			continue
		case err != nil:
			result.Unrepairable++
			continue
		}
		e := projection.Projected.Envelope
		result.Candidates = append(result.Candidates, RedactionReviewCandidate{SourceSHA256: projection.SourceSHA256, ProjectedSHA256: e.EnvelopeSHA256, RecordType: e.RecordType, RecordID: e.RecordID, RecordRevision: e.RecordRevision})
		sources[hash] = bodies[hash]
	}
	if !request.Apply || len(result.Candidates) == 0 {
		return result, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	at := s.now()
	for _, candidate := range result.Candidates {
		if _, err := portable.ReviewRedactionProjection(ctx, tx, s.ProjectID, sources[candidate.SourceSHA256], request.Reviewer, request.Reason, at); err != nil {
			return result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}
