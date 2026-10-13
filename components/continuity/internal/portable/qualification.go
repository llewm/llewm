package portable

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	MaxQualificationObservations = 10000
	MaxQualificationCandidates   = 100
	DefaultQualificationLimit    = 20
	maxQualificationTextBytes    = 1024
	maxQualificationMeasurement  = (1<<53 - 1) / MaxQualificationObservations
	maxCandidateDimensions       = 8
	maxRepresentativeRefs        = 3
)

type DiscoveryObservation struct {
	ID                         string    `json:"id"`
	LineageID                  string    `json:"lineage_id"`
	ProjectID                  string    `json:"project_id"`
	TaskID                     string    `json:"task_id,omitempty"`
	SessionID                  string    `json:"session_id"`
	Client                     string    `json:"client"`
	InvocationID               string    `json:"invocation_id,omitempty"`
	OperationFamily            string    `json:"operation_family"`
	StableReference            string    `json:"stable_reference"`
	SourceRevision             string    `json:"source_revision,omitempty"`
	CandidateCount             int       `json:"candidate_count"`
	ReturnedCount              int       `json:"returned_count"`
	BodiesLoaded               int       `json:"bodies_loaded"`
	ContentBytes               int       `json:"content_bytes"`
	RenderedBytes              int       `json:"rendered_bytes"`
	CompactDerivativeAvailable bool      `json:"compact_derivative_available"`
	Outcome                    string    `json:"outcome"`
	OccurredAt                 time.Time `json:"occurred_at"`
	ReceivedAt                 time.Time `json:"received_at"`
	Authority                  string    `json:"authority"`
	Provenance                 string    `json:"provenance"`
}

type DimensionSummary struct {
	Values    []string `json:"values"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated"`
}

type DiscoveryCandidate struct {
	ID                         string           `json:"id"`
	LineageID                  string           `json:"lineage_id"`
	ProjectID                  string           `json:"project_id"`
	OperationFamily            string           `json:"operation_family"`
	StableReference            string           `json:"stable_reference"`
	SourceRevision             string           `json:"source_revision"`
	ObservationCount           int              `json:"observation_count"`
	Sessions                   DimensionSummary `json:"sessions"`
	Tasks                      DimensionSummary `json:"tasks"`
	Clients                    DimensionSummary `json:"clients"`
	Invocations                DimensionSummary `json:"invocations"`
	Outcomes                   DimensionSummary `json:"outcomes"`
	InputAuthorities           DimensionSummary `json:"input_authorities"`
	Provenances                DimensionSummary `json:"provenances"`
	CandidateCount             int              `json:"candidate_count"`
	ReturnedCount              int              `json:"returned_count"`
	BodiesLoaded               int              `json:"bodies_loaded"`
	ContentBytes               int              `json:"content_bytes"`
	RenderedBytes              int              `json:"rendered_bytes"`
	CompactDerivativeSeen      bool             `json:"compact_derivative_seen"`
	FirstOccurredAt            time.Time        `json:"first_occurred_at"`
	LastOccurredAt             time.Time        `json:"last_occurred_at"`
	RepresentativeObservations []string         `json:"representative_observations"`
	Necessity                  string           `json:"necessity"`
	Avoidability               string           `json:"avoidability"`
	Authority                  string           `json:"authority"`
}

type DiscoveryReport struct {
	ObservationsScanned int                  `json:"observations_scanned"`
	ObservationsGrouped int                  `json:"observations_grouped"`
	CandidateCount      int                  `json:"candidate_count"`
	CandidatesReturned  int                  `json:"candidates_returned"`
	Candidates          []DiscoveryCandidate `json:"candidates"`
	Limit               int                  `json:"limit"`
	Truncated           bool                 `json:"truncated"`
	Authority           string               `json:"authority"`
	Disclaimer          string               `json:"disclaimer"`
}

// BuildDiscoveryReport groups only unchanged stable references observed across
// distinct sessions. It intentionally makes no necessity or avoidability judgment.
func BuildDiscoveryReport(observations []DiscoveryObservation, limit int) (DiscoveryReport, error) {
	if len(observations) > MaxQualificationObservations {
		return DiscoveryReport{}, fmt.Errorf("observations exceed %d", MaxQualificationObservations)
	}
	if limit == 0 {
		limit = DefaultQualificationLimit
	}
	if limit < 1 || limit > MaxQualificationCandidates {
		return DiscoveryReport{}, fmt.Errorf("limit must be between 1 and %d", MaxQualificationCandidates)
	}
	ordered := append([]DiscoveryObservation(nil), observations...)
	for index := range ordered {
		ordered[index] = sanitizeDiscoveryObservation(ordered[index])
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].OccurredAt.Equal(ordered[j].OccurredAt) {
			return ordered[i].OccurredAt.Before(ordered[j].OccurredAt)
		}
		return lessUTF16(ordered[i].ID, ordered[j].ID)
	})
	seen := make(map[string]struct{}, len(ordered))
	groups := make(map[string]*discoveryAccumulator)
	grouped := 0
	for _, observation := range ordered {
		if err := validateDiscoveryObservation(observation); err != nil {
			return DiscoveryReport{}, err
		}
		if _, duplicate := seen[observation.ID]; duplicate {
			return DiscoveryReport{}, fmt.Errorf("duplicate discovery observation %q", observation.ID)
		}
		seen[observation.ID] = struct{}{}
		if observation.SourceRevision == "" {
			continue
		}
		grouped++
		key := discoveryKey(observation)
		group := groups[key]
		if group == nil {
			group = newDiscoveryAccumulator(observation)
			groups[key] = group
		}
		group.add(observation)
	}
	candidates := make([]DiscoveryCandidate, 0, len(groups))
	for _, group := range groups {
		if len(group.sessions) < 2 {
			continue
		}
		candidates = append(candidates, group.candidate())
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ContentBytes != candidates[j].ContentBytes {
			return candidates[i].ContentBytes > candidates[j].ContentBytes
		}
		if candidates[i].ObservationCount != candidates[j].ObservationCount {
			return candidates[i].ObservationCount > candidates[j].ObservationCount
		}
		return lessUTF16(candidates[i].ID, candidates[j].ID)
	})
	total := len(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return DiscoveryReport{
		ObservationsScanned: len(observations), ObservationsGrouped: grouped,
		CandidateCount: total, CandidatesReturned: len(candidates), Candidates: candidates,
		Limit: limit, Truncated: total > len(candidates), Authority: "deterministic_derived",
		Disclaimer: "Repeated unchanged discovery is a review candidate; necessity and avoidability remain unknown until attributed review.",
	}, nil
}

func sanitizeDiscoveryObservation(value DiscoveryObservation) DiscoveryObservation {
	for _, field := range []*string{&value.ID, &value.LineageID, &value.ProjectID, &value.TaskID, &value.SessionID, &value.Client, &value.InvocationID, &value.OperationFamily, &value.StableReference, &value.SourceRevision, &value.Outcome, &value.Authority, &value.Provenance} {
		*field = redact.String(*field)
	}
	return value
}

type discoveryAccumulator struct {
	lineageID, projectID, operation, reference, revision string
	observations, candidates, returned, bodies           int
	contentBytes, renderedBytes                          int
	derivative                                           bool
	first, last                                          time.Time
	sessions, tasks, clients, invocations                map[string]struct{}
	outcomes, authorities, provenances                   map[string]struct{}
	representatives                                      []string
}

func newDiscoveryAccumulator(value DiscoveryObservation) *discoveryAccumulator {
	return &discoveryAccumulator{
		lineageID: value.LineageID, projectID: value.ProjectID, operation: value.OperationFamily,
		reference: value.StableReference, revision: value.SourceRevision,
		sessions: make(map[string]struct{}), tasks: make(map[string]struct{}),
		clients: make(map[string]struct{}), invocations: make(map[string]struct{}),
		outcomes: make(map[string]struct{}), authorities: make(map[string]struct{}), provenances: make(map[string]struct{}),
	}
}

func (group *discoveryAccumulator) add(value DiscoveryObservation) {
	group.observations++
	group.candidates += value.CandidateCount
	group.returned += value.ReturnedCount
	group.bodies += value.BodiesLoaded
	group.contentBytes += value.ContentBytes
	group.renderedBytes += value.RenderedBytes
	group.derivative = group.derivative || value.CompactDerivativeAvailable
	if group.first.IsZero() || value.OccurredAt.Before(group.first) {
		group.first = value.OccurredAt
	}
	if group.last.IsZero() || value.OccurredAt.After(group.last) {
		group.last = value.OccurredAt
	}
	addDimension(group.sessions, value.SessionID)
	addDimension(group.tasks, value.TaskID)
	addDimension(group.clients, value.Client)
	addDimension(group.invocations, value.InvocationID)
	addDimension(group.outcomes, value.Outcome)
	addDimension(group.authorities, value.Authority)
	addDimension(group.provenances, value.Provenance)
	if len(group.representatives) < maxRepresentativeRefs {
		group.representatives = append(group.representatives, value.ID)
	}
}

func (group *discoveryAccumulator) candidate() DiscoveryCandidate {
	key := strings.Join([]string{group.lineageID, group.projectID, group.operation, group.reference, group.revision}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return DiscoveryCandidate{
		ID:        "discovery-friction:" + hex.EncodeToString(sum[:16]),
		LineageID: group.lineageID, ProjectID: group.projectID, OperationFamily: group.operation,
		StableReference: group.reference, SourceRevision: group.revision,
		ObservationCount: group.observations, Sessions: summarizeDimension(group.sessions),
		Tasks: summarizeDimension(group.tasks), Clients: summarizeDimension(group.clients), Invocations: summarizeDimension(group.invocations),
		Outcomes: summarizeDimension(group.outcomes), InputAuthorities: summarizeDimension(group.authorities), Provenances: summarizeDimension(group.provenances),
		CandidateCount: group.candidates, ReturnedCount: group.returned, BodiesLoaded: group.bodies,
		ContentBytes: group.contentBytes, RenderedBytes: group.renderedBytes, CompactDerivativeSeen: group.derivative,
		FirstOccurredAt: group.first, LastOccurredAt: group.last,
		RepresentativeObservations: append([]string{}, group.representatives...),
		Necessity:                  "unknown", Avoidability: "unknown", Authority: "deterministic_derived",
	}
}

func validateDiscoveryObservation(value DiscoveryObservation) error {
	if !lineagePattern.MatchString(value.LineageID) {
		return fmt.Errorf("discovery observation %s has invalid lineage ID", value.ID)
	}
	for name, field := range map[string]string{
		"ID": value.ID, "lineage ID": value.LineageID, "project ID": value.ProjectID,
		"session ID": value.SessionID, "client": value.Client, "operation family": value.OperationFamily,
		"stable reference": value.StableReference, "outcome": value.Outcome,
		"authority": value.Authority, "provenance": value.Provenance,
	} {
		if field == "" || len(field) > maxQualificationTextBytes || strings.IndexByte(field, 0) >= 0 {
			return fmt.Errorf("discovery observation %s has invalid %s", value.ID, name)
		}
	}
	for _, field := range []string{value.TaskID, value.InvocationID, value.SourceRevision} {
		if len(field) > maxQualificationTextBytes || strings.IndexByte(field, 0) >= 0 {
			return fmt.Errorf("discovery observation %s has an invalid optional field", value.ID)
		}
	}
	if !oneOfValue(value.OperationFamily, "file_read", "literal_search", "metadata_list", "exact_show") || !oneOfValue(value.Outcome, "found", "not_found", "truncated", "unavailable", "failed") {
		return fmt.Errorf("discovery observation %s has unsupported operation or outcome", value.ID)
	}
	if value.CandidateCount < 0 || value.ReturnedCount < 0 || value.BodiesLoaded < 0 || value.ContentBytes < 0 || value.RenderedBytes < 0 {
		return fmt.Errorf("discovery observation %s has a negative measurement", value.ID)
	}
	for _, measurement := range []int{value.CandidateCount, value.ReturnedCount, value.BodiesLoaded, value.ContentBytes, value.RenderedBytes} {
		if measurement > maxQualificationMeasurement {
			return fmt.Errorf("discovery observation %s has an oversized measurement", value.ID)
		}
	}
	if value.OccurredAt.IsZero() || value.ReceivedAt.IsZero() || value.OccurredAt.Location() != time.UTC || value.ReceivedAt.Location() != time.UTC || value.ReceivedAt.Before(value.OccurredAt) {
		return fmt.Errorf("discovery observation %s requires timestamps", value.ID)
	}
	return nil
}

func discoveryKey(value DiscoveryObservation) string {
	return strings.Join([]string{value.LineageID, value.ProjectID, value.OperationFamily, value.StableReference, value.SourceRevision}, "\x00")
}

func addDimension(values map[string]struct{}, value string) {
	if value != "" {
		values[value] = struct{}{}
	}
}

func summarizeDimension(values map[string]struct{}) DimensionSummary {
	all := make([]string, 0, len(values))
	for value := range values {
		all = append(all, value)
	}
	sort.Slice(all, func(i, j int) bool { return lessUTF16(all[i], all[j]) })
	total := len(all)
	if len(all) > maxCandidateDimensions {
		all = all[:maxCandidateDimensions]
	}
	return DimensionSummary{Values: all, Total: total, Truncated: total > len(all)}
}

type CommitObservation struct {
	ID          string    `json:"id"`
	CommitID    string    `json:"commit_id"`
	CommittedAt time.Time `json:"committed_at"`
	Authority   string    `json:"authority"`
}

type NarrativePresence struct {
	RecordID string `json:"record_id"`
	CommitID string `json:"commit_id"`
}

type NarrativeWriteAttempt struct {
	ID          string    `json:"id"`
	CommitID    string    `json:"commit_id"`
	Outcome     string    `json:"outcome"`
	EvidenceRef string    `json:"evidence_ref,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
	Authority   string    `json:"authority"`
}

type MissingNarrative struct {
	CommitID        string    `json:"commit_id"`
	CommittedAt     time.Time `json:"committed_at"`
	Status          string    `json:"status"`
	LatestAttempt   string    `json:"latest_attempt,omitempty"`
	AttemptRef      string    `json:"attempt_ref,omitempty"`
	CommitAuthority string    `json:"commit_authority"`
	Necessity       string    `json:"necessity"`
	Authority       string    `json:"authority"`
}

type NarrativeCoverageReport struct {
	CommitsChecked  int                `json:"commits_checked"`
	NarrativesSeen  int                `json:"narratives_seen"`
	MissingCount    int                `json:"missing_count"`
	MissingReturned int                `json:"missing_returned"`
	Missing         []MissingNarrative `json:"missing"`
	Limit           int                `json:"limit"`
	Truncated       bool               `json:"truncated"`
	Authority       string             `json:"authority"`
	Disclaimer      string             `json:"disclaimer"`
}

// BuildNarrativeCoverageReport surfaces successful commits lacking a narrative.
// It reports observed attempt state without inferring why the write is absent.
func BuildNarrativeCoverageReport(commits []CommitObservation, narratives []NarrativePresence, attempts []NarrativeWriteAttempt, limit int) (NarrativeCoverageReport, error) {
	if len(commits) > MaxQualificationObservations || len(narratives) > MaxQualificationObservations || len(attempts) > MaxQualificationObservations {
		return NarrativeCoverageReport{}, fmt.Errorf("coverage fixture exceeds %d records per input", MaxQualificationObservations)
	}
	if limit == 0 {
		limit = DefaultQualificationLimit
	}
	if limit < 1 || limit > MaxQualificationCandidates {
		return NarrativeCoverageReport{}, fmt.Errorf("limit must be between 1 and %d", MaxQualificationCandidates)
	}
	present := make(map[string]struct{}, len(narratives))
	recordIDs := make(map[string]struct{}, len(narratives))
	for _, narrative := range narratives {
		if !validQualificationText(narrative.RecordID) || !portableCommitPattern.MatchString(narrative.CommitID) {
			return NarrativeCoverageReport{}, fmt.Errorf("invalid narrative presence")
		}
		if _, duplicate := recordIDs[narrative.RecordID]; duplicate {
			return NarrativeCoverageReport{}, fmt.Errorf("duplicate narrative record %s", narrative.RecordID)
		}
		recordIDs[narrative.RecordID] = struct{}{}
		present[narrative.CommitID] = struct{}{}
	}
	latest := make(map[string]NarrativeWriteAttempt)
	attemptIDs := make(map[string]struct{}, len(attempts))
	for _, attempt := range attempts {
		attempt.ID = redact.String(attempt.ID)
		attempt.EvidenceRef = redact.String(attempt.EvidenceRef)
		attempt.Authority = redact.String(attempt.Authority)
		if !validQualificationText(attempt.ID) || !portableCommitPattern.MatchString(attempt.CommitID) || !oneOfValue(attempt.Outcome, "succeeded", "failed") || attempt.OccurredAt.IsZero() || attempt.OccurredAt.Location() != time.UTC || !validQualificationText(attempt.Authority) || (attempt.EvidenceRef != "" && !validQualificationText(attempt.EvidenceRef)) {
			return NarrativeCoverageReport{}, fmt.Errorf("invalid narrative write attempt")
		}
		if _, duplicate := attemptIDs[attempt.ID]; duplicate {
			return NarrativeCoverageReport{}, fmt.Errorf("duplicate narrative write attempt %s", attempt.ID)
		}
		attemptIDs[attempt.ID] = struct{}{}
		current, exists := latest[attempt.CommitID]
		if !exists || current.OccurredAt.Before(attempt.OccurredAt) || (current.OccurredAt.Equal(attempt.OccurredAt) && lessUTF16(current.ID, attempt.ID)) {
			latest[attempt.CommitID] = attempt
		}
	}
	ordered := append([]CommitObservation(nil), commits...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CommittedAt.Equal(ordered[j].CommittedAt) {
			return ordered[i].CommittedAt.Before(ordered[j].CommittedAt)
		}
		return lessUTF16(ordered[i].CommitID, ordered[j].CommitID)
	})
	seen := make(map[string]struct{}, len(ordered))
	observationIDs := make(map[string]struct{}, len(ordered))
	missing := make([]MissingNarrative, 0)
	narrativesSeen := 0
	for _, commit := range ordered {
		commit.ID = redact.String(commit.ID)
		commit.Authority = redact.String(commit.Authority)
		if !validQualificationText(commit.ID) || !portableCommitPattern.MatchString(commit.CommitID) || commit.CommittedAt.IsZero() || commit.CommittedAt.Location() != time.UTC || commit.Authority != "machine_observed" {
			return NarrativeCoverageReport{}, fmt.Errorf("invalid commit observation")
		}
		if _, duplicate := observationIDs[commit.ID]; duplicate {
			return NarrativeCoverageReport{}, fmt.Errorf("duplicate commit observation ID %s", commit.ID)
		}
		observationIDs[commit.ID] = struct{}{}
		if _, duplicate := seen[commit.CommitID]; duplicate {
			return NarrativeCoverageReport{}, fmt.Errorf("duplicate commit observation %s", commit.CommitID)
		}
		seen[commit.CommitID] = struct{}{}
		if _, exists := present[commit.CommitID]; exists {
			narrativesSeen++
			continue
		}
		entry := MissingNarrative{CommitID: commit.CommitID, CommittedAt: commit.CommittedAt, Status: "write_not_observed", CommitAuthority: commit.Authority, Necessity: "unknown", Authority: "deterministic_derived"}
		if attempt, exists := latest[commit.CommitID]; exists {
			entry.LatestAttempt, entry.AttemptRef = attempt.ID, attempt.EvidenceRef
			if attempt.Outcome == "failed" {
				entry.Status = "write_failed"
			} else {
				entry.Status = "write_succeeded_record_missing"
			}
		}
		missing = append(missing, entry)
	}
	total := len(missing)
	if len(missing) > limit {
		missing = missing[:limit]
	}
	return NarrativeCoverageReport{
		CommitsChecked: len(commits), NarrativesSeen: narrativesSeen, MissingCount: total, MissingReturned: len(missing), Missing: missing, Limit: limit, Truncated: total > len(missing),
		Authority:  "deterministic_derived",
		Disclaimer: "Missing narrative coverage reports observations only; it does not infer cause, blame, or permission to rewrite history.",
	}, nil
}

func validQualificationText(value string) bool {
	return value != "" && len(value) <= maxQualificationTextBytes && strings.IndexByte(value, 0) < 0
}
