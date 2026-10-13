package portable

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func discoveryObservation(id, session, reference, revision string, occurred time.Time) DiscoveryObservation {
	return DiscoveryObservation{
		ID: id, LineageID: "lineage:55555555555555555555555555555555", ProjectID: "project-fixture",
		TaskID: "portable-memory", SessionID: session, Client: "codex", InvocationID: "invocation-" + session,
		OperationFamily: "exact_show", StableReference: reference, SourceRevision: revision,
		CandidateCount: 1, ReturnedCount: 1, BodiesLoaded: 1, ContentBytes: 100, RenderedBytes: 140,
		CompactDerivativeAvailable: true, Outcome: "found", OccurredAt: occurred, ReceivedAt: occurred.Add(time.Second),
		Authority: "client_claim", Provenance: "fixture",
	}
}

func TestDiscoveryReportGroupsOnlyUnchangedCrossSessionReadsDeterministically(t *testing.T) {
	base := time.Date(2026, 9, 18, 19, 0, 0, 0, time.UTC)
	observations := []DiscoveryObservation{
		discoveryObservation("observation-2", "session-2", "evidence:stable", "sha256:one", base.Add(time.Minute)),
		discoveryObservation("observation-1", "session-1", "evidence:stable", "sha256:one", base),
		discoveryObservation("observation-3", "session-1", "evidence:stable", "sha256:one", base.Add(2*time.Minute)),
		discoveryObservation("changed-1", "session-3", "evidence:stable", "sha256:two", base.Add(3*time.Minute)),
		discoveryObservation("unknown-1", "session-4", "docs:file", "", base.Add(4*time.Minute)),
	}
	observations[1].ContentBytes = 200
	first, err := BuildDiscoveryReport(observations, 10)
	if err != nil {
		t.Fatal(err)
	}
	if first.ObservationsScanned != 5 || first.ObservationsGrouped != 4 || first.CandidateCount != 1 || first.CandidatesReturned != 1 || first.Truncated {
		t.Fatalf("unexpected discovery report: %#v", first)
	}
	candidate := first.Candidates[0]
	if candidate.ObservationCount != 3 || candidate.Sessions.Total != 2 || candidate.ContentBytes != 400 || candidate.RenderedBytes != 420 || candidate.Necessity != "unknown" || candidate.Avoidability != "unknown" || !candidate.CompactDerivativeSeen {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
	if !slices.Equal(candidate.RepresentativeObservations, []string{"observation-1", "observation-2", "observation-3"}) {
		t.Fatalf("representatives not stable: %#v", candidate.RepresentativeObservations)
	}
	reversed := append([]DiscoveryObservation(nil), observations...)
	slices.Reverse(reversed)
	second, err := BuildDiscoveryReport(reversed, 10)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("report changed with input order:\n%s\n%s", firstJSON, secondJSON)
	}
}

func TestDiscoveryReportBoundsCandidatesAndRejectsUnsafeObservations(t *testing.T) {
	base := time.Date(2026, 9, 18, 20, 0, 0, 0, time.UTC)
	observations := []DiscoveryObservation{
		discoveryObservation("a-1", "session-1", "evidence:a", "rev", base),
		discoveryObservation("a-2", "session-2", "evidence:a", "rev", base.Add(time.Minute)),
		discoveryObservation("b-1", "session-1", "evidence:b", "rev", base.Add(2*time.Minute)),
		discoveryObservation("b-2", "session-2", "evidence:b", "rev", base.Add(3*time.Minute)),
	}
	observations[0].ContentBytes, observations[1].ContentBytes = 500, 500
	report, err := BuildDiscoveryReport(observations, 1)
	if err != nil || report.CandidateCount != 2 || report.CandidatesReturned != 1 || !report.Truncated || report.Candidates[0].StableReference != "evidence:a" {
		t.Fatalf("candidate bound failed: %#v err=%v", report, err)
	}
	unsafe := discoveryObservation("unsafe", "session", "evidence:x", "rev", base)
	unsafe.StableReference = "bad\x00reference"
	if _, err := BuildDiscoveryReport([]DiscoveryObservation{unsafe}, 1); err == nil {
		t.Fatal("NUL-bearing reference was accepted")
	}
	if _, err := BuildDiscoveryReport(observations, MaxQualificationCandidates+1); err == nil {
		t.Fatal("oversized candidate limit was accepted")
	}
}

func TestDiscoveryReportRedactsBeforeDerivedOutput(t *testing.T) {
	base := time.Date(2026, 9, 18, 20, 30, 0, 0, time.UTC)
	observations := []DiscoveryObservation{
		discoveryObservation("secret-1", "session-1", "token=supersecret", "rev", base),
		discoveryObservation("secret-2", "session-2", "token=supersecret", "rev", base.Add(time.Minute)),
	}
	report, err := BuildDiscoveryReport(observations, 10)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "supersecret") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("derived report leaked seeded secret: %s", encoded)
	}
}

func TestNarrativeCoverageReportsFailedUnobservedAndInconsistentWrites(t *testing.T) {
	base := time.Date(2026, 9, 18, 21, 0, 0, 0, time.UTC)
	commits := []CommitObservation{
		{ID: "commit-a", CommitID: strings.Repeat("a", 40), CommittedAt: base, Authority: "machine_observed"},
		{ID: "commit-b", CommitID: strings.Repeat("b", 40), CommittedAt: base.Add(time.Minute), Authority: "machine_observed"},
		{ID: "commit-c", CommitID: strings.Repeat("c", 40), CommittedAt: base.Add(2 * time.Minute), Authority: "machine_observed"},
		{ID: "commit-d", CommitID: strings.Repeat("d", 40), CommittedAt: base.Add(3 * time.Minute), Authority: "machine_observed"},
	}
	narratives := []NarrativePresence{{RecordID: "narrative-a", CommitID: commits[0].CommitID}}
	attempts := []NarrativeWriteAttempt{
		{ID: "attempt-b", CommitID: commits[1].CommitID, Outcome: "failed", EvidenceRef: "event:write-b", OccurredAt: base.Add(90 * time.Second), Authority: "machine_observed"},
		{ID: "attempt-d", CommitID: commits[3].CommitID, Outcome: "succeeded", EvidenceRef: "event:write-d", OccurredAt: base.Add(4 * time.Minute), Authority: "machine_observed"},
	}
	report, err := BuildNarrativeCoverageReport(commits, narratives, attempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	if report.CommitsChecked != 4 || report.NarrativesSeen != 1 || report.MissingCount != 3 || report.MissingReturned != 3 || report.Truncated {
		t.Fatalf("unexpected coverage totals: %#v", report)
	}
	want := []string{"write_failed", "write_not_observed", "write_succeeded_record_missing"}
	for index, status := range want {
		if report.Missing[index].Status != status || report.Missing[index].Necessity != "unknown" {
			t.Fatalf("missing narrative %d mismatch: %#v", index, report.Missing[index])
		}
	}
	if report.Missing[0].AttemptRef != "event:write-b" || report.Missing[1].LatestAttempt != "" || report.Missing[2].LatestAttempt != "attempt-d" {
		t.Fatalf("attempt provenance mismatch: %#v", report.Missing)
	}
	bounded, err := BuildNarrativeCoverageReport(commits, narratives, attempts, 2)
	if err != nil || bounded.MissingCount != 3 || bounded.MissingReturned != 2 || !bounded.Truncated {
		t.Fatalf("coverage bound failed: %#v err=%v", bounded, err)
	}
}

func TestNarrativeCoverageRejectsDuplicateOrInvalidCommitObservations(t *testing.T) {
	base := time.Date(2026, 9, 18, 22, 0, 0, 0, time.UTC)
	commit := CommitObservation{ID: "commit", CommitID: strings.Repeat("e", 40), CommittedAt: base, Authority: "machine_observed"}
	if _, err := BuildNarrativeCoverageReport([]CommitObservation{commit, commit}, nil, nil, 10); err == nil {
		t.Fatal("duplicate commit observation was accepted")
	}
	commit.CommitID = "not-a-commit"
	if _, err := BuildNarrativeCoverageReport([]CommitObservation{commit}, nil, nil, 10); err == nil {
		t.Fatal("invalid commit observation was accepted")
	}
	commit.CommitID = strings.Repeat("e", 40)
	commit.Authority = "caller_asserted"
	if _, err := BuildNarrativeCoverageReport([]CommitObservation{commit}, nil, nil, 10); err == nil {
		t.Fatal("non-machine commit observation was accepted")
	}
}
