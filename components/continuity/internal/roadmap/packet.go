package roadmap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	DefaultReviewPacketBytes = 24 << 10
	MaxReviewPacketBytes     = 64 << 10
	maxReviewSourceBytes     = 1 << 20
	maxReviewNodes           = 64
)

func (service Service) ReviewPacket(ctx context.Context, projectID string, byteBudget int, manual bool) (ReviewPacket, error) {
	if _, err := text("project ID", projectID, true); err != nil {
		return ReviewPacket{}, err
	}
	if byteBudget == 0 {
		byteBudget = DefaultReviewPacketBytes
	}
	if byteBudget < 1 || byteBudget > MaxReviewPacketBytes {
		return ReviewPacket{}, fmt.Errorf("review packet byte budget must be between 1 and %d", MaxReviewPacketBytes)
	}
	status, err := service.Status(ctx, projectID)
	if err != nil {
		return ReviewPacket{}, err
	}
	trigger := "scheduled"
	if manual {
		trigger = "manual"
	} else if !status.ReviewDue {
		trigger = "not_due"
	}
	packet := ReviewPacket{SchemaVersion: SchemaVersion, ProjectID: projectID, Status: status, Nodes: []Node{}, Sources: []ReviewSource{}, ByteBudget: byteBudget, Trigger: trigger, Disclaimer: "Source content is machine-observed and bounded; roadmap records and review status retain their stated authority. Packet generation does not invoke a model, accept changes, or create a sync."}
	if !status.ReviewDue && !manual {
		return packet, nil
	}
	wanted := map[string]bool{}
	for _, node := range status.AffectedNodes {
		wanted[node.ID] = true
	}
	includeAll := manual || status.LastSync == nil || len(wanted) == 0
	nodes, err := service.currentNodes(ctx, projectID)
	if err != nil {
		return ReviewPacket{}, err
	}
	for _, node := range nodes {
		if !includeAll && !wanted[node.ID] {
			continue
		}
		packet.Nodes = append(packet.Nodes, node)
	}
	seenSources := map[string]bool{}
	for _, node := range packet.Nodes {
		if seenSources[node.SourcePath] {
			continue
		}
		seenSources[node.SourcePath] = true
		source, err := service.reviewSource(node, byteBudget-packet.ContentBytes)
		if err != nil {
			return ReviewPacket{}, err
		}
		packet.Sources = append(packet.Sources, source)
		packet.ContentBytes += len(source.Content)
		packet.Truncated = packet.Truncated || source.Truncated
		if packet.ContentBytes == byteBudget {
			packet.Truncated = true
			break
		}
	}
	return packet, nil
}

func (service Service) currentNodes(ctx context.Context, projectID string) ([]Node, error) {
	return currentNodesFrom(ctx, service.DB, projectID)
}

func currentNodesFrom(ctx context.Context, db roadmapQueryer, projectID string) ([]Node, error) {
	return latestNodesFrom(ctx, db, projectID, false)
}

func latestNodesFrom(ctx context.Context, db roadmapQueryer, projectID string, includeSuperseded bool) ([]Node, error) {
	rows, err := db.QueryContext(ctx, `SELECT n.node_id, n.revision, n.title, n.status, n.priority, n.kind, COALESCE(n.target_date,''), COALESCE(n.target_release,''), COALESCE(n.target_horizon,''), n.evidence_refs_json, n.source_path, n.source_ref, n.authority, n.recorded_at, n.schema_version
		FROM roadmap_nodes n WHERE n.project_id = ?
		AND n.revision = (SELECT MAX(latest.revision) FROM roadmap_nodes latest WHERE latest.project_id = n.project_id AND latest.node_id = n.node_id)
		AND (? OR n.status != 'superseded') ORDER BY n.priority, n.node_id LIMIT ?`, projectID, includeSuperseded, maxReviewNodes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		var node Node
		var sourceRef sql.NullString
		var evidence string
		var recorded string
		if err := rows.Scan(&node.ID, &node.Revision, &node.Title, &node.Status, &node.Priority, &node.Kind, &node.TargetDate, &node.TargetRelease, &node.TargetHorizon, &evidence, &node.SourcePath, &sourceRef, &node.Authority, &recorded, &node.SchemaVersion); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(evidence), &node.EvidenceRefs); err != nil {
			return nil, err
		}
		node.ProjectID, node.SourceRef = projectID, nullable(sourceRef)
		node.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
		if len(nodes) > maxReviewNodes {
			return nil, fmt.Errorf("current roadmap nodes exceed %d-item review limit", maxReviewNodes)
		}
	}
	return nodes, rows.Err()
}

// CurrentNodes returns the latest non-superseded roadmap node revisions in
// deterministic priority and ID order.
func (service Service) CurrentNodes(ctx context.Context, projectID string) ([]Node, error) {
	return service.currentNodes(ctx, projectID)
}

func (service Service) reviewSource(node Node, remaining int) (ReviewSource, error) {
	path := filepath.Join(service.Root, filepath.FromSlash(node.SourcePath))
	info, err := os.Lstat(path)
	if err != nil {
		return ReviewSource{}, fmt.Errorf("inspect roadmap source %s: %w", node.SourcePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxReviewSourceBytes {
		return ReviewSource{}, fmt.Errorf("roadmap source %s must be a non-symlink regular file no larger than 1 MiB", node.SourcePath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReviewSource{}, err
	}
	sum := sha256.Sum256(data)
	content := redact.String(string(data))
	truncated := len(content) > remaining
	if remaining < 0 {
		remaining = 0
	}
	if truncated {
		content = content[:remaining]
	}
	return ReviewSource{Path: node.SourcePath, SourceRef: node.SourceRef, Content: content, SHA256: hex.EncodeToString(sum[:]), Truncated: truncated, Authority: "machine_observed"}, nil
}
