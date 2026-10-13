package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/topic"
)

// Decisions and topics travel as the curated "ledger" families. A decision carries
// its ordered links in one envelope because links are written with the decision
// and never change. record_sha256 is project-scoped, so it is recomputed for the
// destination instead of being copied.

type decisionLinkPayload struct {
	Kind           string `json:"kind"`
	TargetKind     string `json:"target_kind"`
	TargetID       string `json:"target_id"`
	TargetRevision string `json:"target_revision"`
}

type decisionPayload struct {
	Title                    string                `json:"title"`
	Rationale                string                `json:"rationale"`
	Disposition              string                `json:"disposition"`
	Actor                    string                `json:"actor"`
	ActorType                string                `json:"actor_type"`
	ReviewSource             string                `json:"review_source"`
	SupersedesID             string                `json:"supersedes_id,omitempty"`
	Links                    []decisionLinkPayload `json:"links"`
	OperationalSchemaVersion int                   `json:"operational_schema_version"`
}

type topicRevisionPayload struct {
	Title                    string `json:"title"`
	Summary                  string `json:"summary"`
	Status                   string `json:"status"`
	Creator                  string `json:"creator"`
	Model                    string `json:"model,omitempty"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type topicAliasPayload struct {
	TopicID string `json:"topic_id"`
	Creator string `json:"creator"`
}

type topicLinkPayload struct {
	TopicID        string `json:"topic_id"`
	TargetKind     string `json:"target_kind"`
	TargetID       string `json:"target_id"`
	TargetRevision string `json:"target_revision"`
	Status         string `json:"status"`
	Creator        string `json:"creator"`
	Model          string `json:"model,omitempty"`
	AcceptedFrom   string `json:"accepted_from,omitempty"`
}

type portableTopicAlias struct {
	Alias, TopicID, Creator string
	RecordedAt              time.Time
}

type portableLedgerRecords struct {
	Decisions      map[string]decision.Record
	TopicRevisions map[string]topic.Topic
	TopicAliases   map[string]portableTopicAlias
	TopicLinks     map[string]topic.Link
}

func newPortableLedgerRecords() portableLedgerRecords {
	return portableLedgerRecords{
		Decisions: map[string]decision.Record{}, TopicRevisions: map[string]topic.Topic{},
		TopicAliases: map[string]portableTopicAlias{}, TopicLinks: map[string]topic.Link{},
	}
}

var decisionLinkKinds = []string{"subject", "supports", "opposes", "resulted_in", "supersedes"}
var decisionTargetKinds = []string{"task", "checkpoint", "roadmap_node", "track_plan", "work_item", "narrative", "commit", "decision"}
var topicTargetKinds = []string{"roadmap_node", "narrative", "narrative_claim", "decision", "work_item", "track_plan", "task", "checkpoint", "commit", "file"}

func loadLedgerSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	text := func(value string) string { return portableReviewText(value, projectID, projectRoot) }
	var specs []EnvelopeSpec
	rows, err := source.QueryContext(ctx, `SELECT id,title,rationale,disposition,actor,actor_type,authority,review_source,COALESCE(supersedes_id,''),recorded_at,schema_version FROM decision_records WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	var decisions []decision.Record
	for rows.Next() {
		var value decision.Record
		var recorded string
		if err := rows.Scan(&value.ID, &value.Title, &value.Rationale, &value.Disposition, &value.Actor, &value.ActorType, &value.Authority, &value.ReviewSource, &value.SupersedesID, &recorded, &value.SchemaVersion); err != nil {
			rows.Close()
			return nil, err
		}
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			rows.Close()
			return nil, err
		}
		decisions = append(decisions, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, value := range decisions {
		links, err := source.QueryContext(ctx, `SELECT kind,target_kind,target_id,target_revision FROM decision_links WHERE project_id=? AND decision_id=? ORDER BY ordinal`, projectID, value.ID)
		if err != nil {
			return nil, err
		}
		payload := decisionPayload{Title: text(value.Title), Rationale: text(value.Rationale), Disposition: value.Disposition, Actor: text(value.Actor), ActorType: value.ActorType, ReviewSource: text(value.ReviewSource), SupersedesID: value.SupersedesID, Links: []decisionLinkPayload{}, OperationalSchemaVersion: value.SchemaVersion}
		for links.Next() {
			var link decisionLinkPayload
			if err := links.Scan(&link.Kind, &link.TargetKind, &link.TargetID, &link.TargetRevision); err != nil {
				links.Close()
				return nil, err
			}
			link.TargetID, link.TargetRevision = text(link.TargetID), text(link.TargetRevision)
			payload.Links = append(payload.Links, link)
		}
		if err := links.Close(); err != nil {
			return nil, err
		}
		spec, err := ledgerSpec(lineageID, "decision", value.ID, 0, value.RecordedAt, value.Authority, envelopeIndex{Title: payload.Title, Status: payload.Disposition}, decisionRefs(payload), payload)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}

	rows, err = source.QueryContext(ctx, `SELECT id,revision,title,summary,status,creator,COALESCE(model,''),authority,recorded_at,schema_version FROM topic_revisions WHERE project_id=? ORDER BY id,revision`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, title, summary, status, creator, model, authority, recorded string
		var revision, schema int
		if err := rows.Scan(&id, &revision, &title, &summary, &status, &creator, &model, &authority, &recorded, &schema); err != nil {
			rows.Close()
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			rows.Close()
			return nil, err
		}
		payload := topicRevisionPayload{Title: text(title), Summary: text(summary), Status: status, Creator: text(creator), Model: text(model), OperationalSchemaVersion: schema}
		spec, err := ledgerSpec(lineageID, "topic_revision", id, revision, at, authority, envelopeIndex{Title: payload.Title, Status: payload.Status}, nil, payload)
		if err != nil {
			rows.Close()
			return nil, err
		}
		specs = append(specs, spec)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = source.QueryContext(ctx, `SELECT alias,topic_id,creator,recorded_at FROM topic_aliases WHERE project_id=? ORDER BY alias`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var alias, topicID, creator, recorded string
		if err := rows.Scan(&alias, &topicID, &creator, &recorded); err != nil {
			rows.Close()
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			rows.Close()
			return nil, err
		}
		payload := topicAliasPayload{TopicID: topicID, Creator: text(creator)}
		spec, err := ledgerSpec(lineageID, "topic_alias", alias, 0, at, "caller_asserted", envelopeIndex{Title: alias}, []Reference{{Kind: "topic", ID: topicID}}, payload)
		if err != nil {
			rows.Close()
			return nil, err
		}
		specs = append(specs, spec)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = source.QueryContext(ctx, `SELECT id,topic_id,target_kind,target_id,target_revision,status,creator,COALESCE(model,''),authority,COALESCE(accepted_from,''),recorded_at FROM topic_links WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, authority, recorded string
		var payload topicLinkPayload
		if err := rows.Scan(&id, &payload.TopicID, &payload.TargetKind, &payload.TargetID, &payload.TargetRevision, &payload.Status, &payload.Creator, &payload.Model, &authority, &payload.AcceptedFrom, &recorded); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			return nil, err
		}
		payload.TargetID, payload.TargetRevision, payload.Creator, payload.Model = text(payload.TargetID), text(payload.TargetRevision), text(payload.Creator), text(payload.Model)
		spec, err := ledgerSpec(lineageID, "topic_link", id, 0, at, authority, envelopeIndex{Title: payload.TopicID, Status: payload.Status}, topicLinkRefs(payload), payload)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, rows.Err()
}

func ledgerSpec(lineageID, recordType, id string, revision int, recordedAt time.Time, authority string, index envelopeIndex, refs []Reference, payload any) (EnvelopeSpec, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	indexBody, err := json.Marshal(index)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	if refs == nil {
		refs = []Reference{}
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: recordType, RecordSchemaVersion: 1, RecordID: id, RecordRevision: revision, RecordedAt: recordedAt, Authority: authority, Index: indexBody, Refs: refs, Payload: body}
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	records := newPortableLedgerRecords()
	if _, err := decodeLedgerEnvelope(&records, encoded.Envelope); err != nil {
		return EnvelopeSpec{}, err
	}
	return spec, nil
}

func decisionRefs(payload decisionPayload) []Reference {
	refs := []Reference{}
	if payload.SupersedesID != "" {
		refs = append(refs, Reference{Kind: "decision", ID: payload.SupersedesID})
	}
	return refs
}

func topicLinkRefs(payload topicLinkPayload) []Reference {
	refs := []Reference{{Kind: "topic", ID: payload.TopicID}}
	if payload.AcceptedFrom != "" {
		refs = append(refs, Reference{Kind: "topic_link", ID: payload.AcceptedFrom})
	}
	return refs
}

// decodeLedgerEnvelope decodes a decision or topic envelope into records. It
// reports false for other record types.
func decodeLedgerEnvelope(records *portableLedgerRecords, envelope Envelope) (bool, error) {
	recorded, timeErr := parsePortableTime(envelope.RecordedAt)
	invalid := func() error { return fmt.Errorf("%s %s is invalid", envelope.RecordType, envelope.RecordID) }
	mismatch := func() error {
		return fmt.Errorf("%s %s metadata does not match payload", envelope.RecordType, envelope.RecordID)
	}
	switch envelope.RecordType {
	case "decision":
		var payload decisionPayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode decision %s: %w", envelope.RecordID, err)
		}
		if timeErr != nil || envelope.RecordRevision != 0 || !oneOfValue(envelope.Authority, "caller_asserted", "user_reported") || !portableIncomingIdentifier(envelope.RecordID) ||
			!validReviewText(payload.Title, true) || !validReviewText(payload.Rationale, true) || !oneOfValue(payload.Disposition, "accepted", "deferred", "rejected", "superseded") ||
			!validReviewText(payload.Actor, true) || payload.ActorType != "human" || !validReviewText(payload.ReviewSource, envelope.Authority == "user_reported") ||
			payload.SupersedesID != "" && (!portableIncomingIdentifier(payload.SupersedesID) || payload.SupersedesID == envelope.RecordID) || len(payload.Links) > 16 || payload.OperationalSchemaVersion != 1 {
			return true, invalid()
		}
		value := decision.Record{ID: envelope.RecordID, Title: payload.Title, Rationale: payload.Rationale, Disposition: payload.Disposition, Actor: payload.Actor, ActorType: payload.ActorType, Authority: envelope.Authority, ReviewSource: payload.ReviewSource, SupersedesID: payload.SupersedesID, Links: []decision.Link{}, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
		for _, link := range payload.Links {
			if !oneOfValue(link.Kind, decisionLinkKinds...) || !oneOfValue(link.TargetKind, decisionTargetKinds...) || !portableIncomingIdentifier(link.TargetID) || link.TargetRevision != "" && !portableIncomingIdentifier(link.TargetRevision) {
				return true, invalid()
			}
			value.Links = append(value.Links, decision.Link{Kind: link.Kind, TargetKind: link.TargetKind, TargetID: link.TargetID, TargetRevision: link.TargetRevision, Authority: "caller_asserted"})
		}
		if !sameReferences(envelope.Refs, decisionRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Title, Status: payload.Disposition}) {
			return true, mismatch()
		}
		records.Decisions[envelope.RecordID] = value
	case "topic_revision":
		var payload topicRevisionPayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode topic revision %s: %w", envelope.RecordID, err)
		}
		if timeErr != nil || envelope.RecordRevision < 1 || !oneOfValue(envelope.Authority, "caller_asserted", "user_reported", "model_suggested") || !portableIncomingIdentifier(envelope.RecordID) ||
			!validReviewText(payload.Title, true) || !validReviewText(payload.Summary, false) || !oneOfValue(payload.Status, "proposed", "accepted", "retired") ||
			!validReviewText(payload.Creator, true) || !validReviewText(payload.Model, false) || payload.OperationalSchemaVersion != 1 {
			return true, invalid()
		}
		if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Title, Status: payload.Status}) {
			return true, mismatch()
		}
		records.TopicRevisions[fmt.Sprintf("%s@%d", envelope.RecordID, envelope.RecordRevision)] = topic.Topic{ID: envelope.RecordID, Revision: envelope.RecordRevision, Title: payload.Title, Summary: payload.Summary, Status: payload.Status, Creator: payload.Creator, Model: payload.Model, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
	case "topic_alias":
		var payload topicAliasPayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode topic alias %s: %w", envelope.RecordID, err)
		}
		if timeErr != nil || envelope.RecordRevision != 0 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || !portableIncomingIdentifier(payload.TopicID) || !validReviewText(payload.Creator, true) {
			return true, invalid()
		}
		if !sameReferences(envelope.Refs, []Reference{{Kind: "topic", ID: payload.TopicID}}) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: envelope.RecordID}) {
			return true, mismatch()
		}
		records.TopicAliases[envelope.RecordID] = portableTopicAlias{Alias: envelope.RecordID, TopicID: payload.TopicID, Creator: payload.Creator, RecordedAt: recorded}
	case "topic_link":
		var payload topicLinkPayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode topic link %s: %w", envelope.RecordID, err)
		}
		if timeErr != nil || envelope.RecordRevision != 0 || !oneOfValue(envelope.Authority, "caller_asserted", "user_reported", "model_suggested") || !portableIncomingIdentifier(envelope.RecordID) ||
			!portableIncomingIdentifier(payload.TopicID) || !oneOfValue(payload.TargetKind, topicTargetKinds...) || !portableIncomingIdentifier(payload.TargetID) ||
			payload.TargetRevision != "" && !portableIncomingIdentifier(payload.TargetRevision) || !oneOfValue(payload.Status, "proposed", "accepted", "retired") ||
			!validReviewText(payload.Creator, true) || !validReviewText(payload.Model, false) || payload.AcceptedFrom != "" && (!portableIncomingIdentifier(payload.AcceptedFrom) || payload.AcceptedFrom == envelope.RecordID) {
			return true, invalid()
		}
		if !sameReferences(envelope.Refs, topicLinkRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.TopicID, Status: payload.Status}) {
			return true, mismatch()
		}
		records.TopicLinks[envelope.RecordID] = topic.Link{ID: envelope.RecordID, TopicID: payload.TopicID, TargetKind: payload.TargetKind, TargetID: payload.TargetID, TargetRevision: payload.TargetRevision, Status: payload.Status, Creator: payload.Creator, Model: payload.Model, Authority: envelope.Authority, AcceptedFrom: payload.AcceptedFrom, RecordedAt: recorded}
	default:
		return false, nil
	}
	return true, nil
}

// validateLedgerDependencies checks that superseded decisions, aliased topics,
// linked topics, and accepted-from links exist in the bundle or the project.
func validateLedgerDependencies(ctx context.Context, tx *sql.Tx, projectID string, values portableLedgerRecords, appendKeys map[string]struct{}) error {
	topicBundled := func(id string) bool {
		for _, revision := range values.TopicRevisions {
			if revision.ID == id {
				return true
			}
		}
		return false
	}
	require := func(bundled bool, query string, args ...any) (bool, error) {
		if bundled {
			return true, nil
		}
		return rowExists(ctx, tx, query, args...)
	}
	for id, value := range values.Decisions {
		if _, ok := appendKeys[identityKey("decision", id, 0)]; !ok || value.SupersedesID == "" {
			continue
		}
		_, bundled := values.Decisions[value.SupersedesID]
		if ok, err := require(bundled, `SELECT 1 FROM decision_records WHERE project_id=? AND id=?`, projectID, value.SupersedesID); err != nil || !ok {
			return firstError(err, fmt.Errorf("decision %s supersedes a missing decision", id))
		}
	}
	for alias, value := range values.TopicAliases {
		if _, ok := appendKeys[identityKey("topic_alias", alias, 0)]; !ok {
			continue
		}
		if ok, err := require(topicBundled(value.TopicID), `SELECT 1 FROM topic_revisions WHERE project_id=? AND id=?`, projectID, value.TopicID); err != nil || !ok {
			return firstError(err, fmt.Errorf("topic alias %s names a missing topic", alias))
		}
	}
	for id, value := range values.TopicLinks {
		if _, ok := appendKeys[identityKey("topic_link", id, 0)]; !ok {
			continue
		}
		if ok, err := require(topicBundled(value.TopicID), `SELECT 1 FROM topic_revisions WHERE project_id=? AND id=?`, projectID, value.TopicID); err != nil || !ok {
			return firstError(err, fmt.Errorf("topic link %s names a missing topic", id))
		}
		if value.AcceptedFrom != "" {
			_, bundled := values.TopicLinks[value.AcceptedFrom]
			if ok, err := require(bundled, `SELECT 1 FROM topic_links WHERE project_id=? AND id=?`, projectID, value.AcceptedFrom); err != nil || !ok {
				return firstError(err, fmt.Errorf("topic link %s accepts a missing proposal", id))
			}
		}
	}
	return nil
}

func firstError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// appendLedgerRecords inserts decisions (predecessors first), topic revisions,
// aliases, and links (proposals before acceptances).
func appendLedgerRecords(ctx context.Context, tx *sql.Tx, projectID string, values portableLedgerRecords, appendKeys map[string]struct{}) error {
	decisions := make([]decision.Record, 0, len(values.Decisions))
	for _, value := range values.Decisions {
		decisions = append(decisions, value)
	}
	sort.Slice(decisions, func(i, j int) bool {
		if !decisions[i].RecordedAt.Equal(decisions[j].RecordedAt) {
			return decisions[i].RecordedAt.Before(decisions[j].RecordedAt)
		}
		return decisions[i].ID < decisions[j].ID
	})
	for _, value := range decisions {
		if _, ok := appendKeys[identityKey("decision", value.ID, 0)]; !ok {
			continue
		}
		value.ProjectID = projectID
		hash, err := decision.SemanticHash(value)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decision_records(id,project_id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,supersedes_id,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?)`, value.ID, projectID, hash, value.Title, value.Rationale, value.Disposition, value.Actor, value.ActorType, value.Authority, value.ReviewSource, value.SupersedesID, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion); err != nil {
			return fmt.Errorf("append decision %s: %w", value.ID, err)
		}
		for ordinal, link := range value.Links {
			if _, err := tx.ExecContext(ctx, `INSERT INTO decision_links(project_id,decision_id,ordinal,kind,target_kind,target_id,target_revision,authority) VALUES(?,?,?,?,?,?,?,?)`, projectID, value.ID, ordinal, link.Kind, link.TargetKind, link.TargetID, link.TargetRevision, link.Authority); err != nil {
				return fmt.Errorf("append decision link %s/%d: %w", value.ID, ordinal, err)
			}
		}
	}
	revisions := make([]topic.Topic, 0, len(values.TopicRevisions))
	for _, value := range values.TopicRevisions {
		revisions = append(revisions, value)
	}
	sort.Slice(revisions, func(i, j int) bool {
		if revisions[i].ID != revisions[j].ID {
			return revisions[i].ID < revisions[j].ID
		}
		return revisions[i].Revision < revisions[j].Revision
	})
	for _, value := range revisions {
		if _, ok := appendKeys[identityKey("topic_revision", value.ID, value.Revision)]; !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_revisions(project_id,id,revision,title,summary,status,creator,model,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),?,?,?)`, projectID, value.ID, value.Revision, value.Title, value.Summary, value.Status, value.Creator, value.Model, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion); err != nil {
			return fmt.Errorf("append topic %s@%d: %w", value.ID, value.Revision, err)
		}
	}
	aliases := make([]string, 0, len(values.TopicAliases))
	for alias := range values.TopicAliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		if _, ok := appendKeys[identityKey("topic_alias", alias, 0)]; !ok {
			continue
		}
		value := values.TopicAliases[alias]
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_aliases(project_id,alias,topic_id,creator,recorded_at) VALUES(?,?,?,?,?)`, projectID, alias, value.TopicID, value.Creator, value.RecordedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("append topic alias %s: %w", alias, err)
		}
	}
	links := make([]topic.Link, 0, len(values.TopicLinks))
	for _, value := range values.TopicLinks {
		links = append(links, value)
	}
	sort.Slice(links, func(i, j int) bool {
		// A proposal is recorded before the link that accepts it.
		if (links[i].AcceptedFrom == "") != (links[j].AcceptedFrom == "") {
			return links[i].AcceptedFrom == ""
		}
		if !links[i].RecordedAt.Equal(links[j].RecordedAt) {
			return links[i].RecordedAt.Before(links[j].RecordedAt)
		}
		return links[i].ID < links[j].ID
	})
	for _, value := range links {
		if _, ok := appendKeys[identityKey("topic_link", value.ID, 0)]; !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_links(id,project_id,topic_id,target_kind,target_id,target_revision,status,creator,model,authority,accepted_from,recorded_at) VALUES(?,?,?,?,?,?,?,?,NULLIF(?,''),?,NULLIF(?,''),?)`, value.ID, projectID, value.TopicID, value.TargetKind, value.TargetID, value.TargetRevision, value.Status, value.Creator, value.Model, value.Authority, value.AcceptedFrom, value.RecordedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("append topic link %s: %w", value.ID, err)
		}
	}
	return nil
}
