package memsync

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type RedactionRecoveryResult struct {
	RecoveryID string                  `json:"recovery_id"`
	Original   portable.Envelope       `json:"original"`
	Derived    portable.Envelope       `json:"derived"`
	Plan       portable.ImportPlan     `json:"plan"`
	Receipt    *portable.ImportReceipt `json:"receipt,omitempty"`
	Applied    bool                    `json:"applied"`
}

// RepairRedaction previews or appends a proof-backed correction locally. It
// reads only tracked, canonical observations in this lineage under the sync
// lock. Ordinary sync later transports the correction; no memory Git mutation
// or network operation occurs here.
func (s Syncer) RepairRedaction(ctx context.Context, originalHash, derivedHash, root, projectID, creator, reason, expectedID string, apply bool) (RedactionRecoveryResult, error) {
	var result RedactionRecoveryResult
	for _, hash := range []string{originalHash, derivedHash} {
		b, err := hex.DecodeString(hash)
		if err != nil || len(b) != 32 || strings.ToLower(hash) != hash {
			return result, fmt.Errorf("observation hashes must be lowercase SHA256")
		}
	}
	repo, err := s.repository(ctx)
	if err != nil {
		return result, err
	}
	lock, err := lockFile(filepath.Join(s.Clone, ".git", "continuity-sync.lock"))
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
	lineageDir := portable.MemoryLineageDir(identity.LineageID)
	listing, err := repo.run(ctx, "ls-files", "--", lineageDir)
	if err != nil {
		return result, err
	}
	bodies := map[string][]byte{}
	for _, relative := range strings.Fields(listing) {
		name := strings.TrimSuffix(filepath.Base(relative), ".json")
		if name != originalHash && name != derivedHash {
			continue
		}
		body, err := os.ReadFile(filepath.Join(s.Clone, filepath.FromSlash(relative)))
		if err != nil {
			return result, err
		}
		spec, err := portable.DecodeMemoryFile(relative, body)
		if err != nil {
			return result, err
		}
		encoded, err := portable.EncodeEnvelope(spec)
		if err != nil {
			return result, err
		}
		bodies[name] = encoded.Bytes
	}
	if len(bodies) != 2 {
		return result, fmt.Errorf("both different observations must already be tracked in this lineage")
	}
	spec, err := portable.RedactionRecoverySpec(portable.RedactionRecovery{Original: bodies[originalHash], Derived: bodies[derivedHash], ProjectRoot: root, ProjectID: projectID, Creator: creator, Reason: reason}, s.now())
	if err != nil {
		return result, err
	}
	result.RecoveryID = spec.RecordID
	if apply && expectedID != spec.RecordID {
		return result, fmt.Errorf("apply requires the exact previewed --expect-recovery %s", spec.RecordID)
	}
	// Retries use the first observation's timestamp; they append no new proof.
	var stored string
	err = s.DB.QueryRowContext(ctx, `SELECT envelope_json FROM redaction_recoveries WHERE project_id=? AND id=?`, s.ProjectID, spec.RecordID).Scan(&stored)
	if err == nil {
		spec, err = portable.DecodeMemoryEnvelope([]byte(stored))
	}
	if err != nil && err != sql.ErrNoRows {
		return result, err
	}
	var schema string
	if err := s.DB.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&schema); err != nil {
		return result, err
	}
	bundle, err := portable.BuildBundle(portable.BundleSpec{ExportID: "repair:" + spec.RecordID, LineageID: identity.LineageID, SourceInstanceID: identity.InstanceID, CreatedAt: spec.RecordedAt, SourceDatabaseSchema: schema, SelectionProfile: "curated-v1", RedactionProfile: portable.DefaultRedactionProfile(), Omissions: portable.CuratedV1Omissions()}, []portable.EnvelopeSpec{spec})
	if err != nil {
		return result, err
	}
	originalSpec, _ := portable.DecodeMemoryEnvelope(bodies[originalHash])
	original, _ := portable.EncodeEnvelope(originalSpec)
	result.Original = original.Envelope
	derivedSpec, _ := portable.DecodeMemoryEnvelope(bodies[derivedHash])
	derived, _ := portable.EncodeEnvelope(derivedSpec)
	result.Derived = derived.Envelope
	result.Plan, err = portable.PlanOperationalImport(ctx, s.DB, s.ProjectID, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil {
		return result, err
	}
	if !result.Plan.ApplyAllowed {
		return result, fmt.Errorf("recovery plan refused")
	}
	if !apply || result.Plan.Counts[portable.PlanAppend] == 0 {
		return result, nil
	}
	applied, err := portable.ApplyOperationalImport(ctx, s.DB, s.ProjectID, bundle.ManifestBytes, bundle.RecordsBytes, s.now())
	result.Plan, result.Receipt = applied.Plan, applied.Receipt
	result.Applied = err == nil
	return result, err
}
