package validator

import (
	"strings"
	"testing"
)

func allowPolicy() Policy {
	return Policy{SchemaVersion: 1, ActionType: "git_commit", PolicyVersion: "commit-v1", Effect: "allow", ProhibitedTrailers: []string{"Co-authored-by", "Signed-off-by"}, RequireGitNote: true}
}

func TestValidateCommitMessageRejectsProhibitedTrailers(t *testing.T) {
	message := []byte("Implement validator\n\nDetailed body.\n\nco-AUTHORED-by: Agent <agent@example.test>\n continuation\nSigned-off-by: Person <person@example.test>\n\n# Please enter the commit message.\n")
	result, err := ValidateCommitMessage(allowPolicy(), message)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Diagnostics) != 2 || result.Diagnostics[0].Line != 5 || result.Diagnostics[0].TrailerKey != "co-AUTHORED-by" {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestValidateCommitMessageAcceptsProseAndAllowedTrailers(t *testing.T) {
	for _, message := range []string{
		"Explain Co-authored-by: in ordinary prose.\n",
		"fix: preserve conventional subject\n\nBody without trailers.\n",
		"Implement validator\n\nIssue: 123\n",
		"Implement validator\r\n\r\nReviewed-by: Human\r\n",
	} {
		result, err := ValidateCommitMessage(allowPolicy(), []byte(message))
		if err != nil || !result.Valid {
			t.Fatalf("message rejected: result=%#v err=%v\n%s", result, err, message)
		}
	}
}

func TestPolicyEffectBlocksCommit(t *testing.T) {
	for _, effect := range []string{"deny", "proposal_only"} {
		policy := allowPolicy()
		policy.Effect = effect
		result, err := ValidateCommitMessage(policy, []byte("Valid message\n"))
		if err != nil || result.Valid || len(result.Diagnostics) != 1 {
			t.Fatalf("effect %s: result=%#v err=%v", effect, result, err)
		}
	}
}

func TestDecodePolicyValidatesContract(t *testing.T) {
	valid := `{"schema_version":1,"action_type":"git_commit","policy_version":"v1","effect":"allow","prohibited_trailers":[],"require_git_note":true,"authority":"caller_asserted","extra":"ignored"}`
	policy, err := DecodePolicy(strings.NewReader(valid))
	if err != nil || policy.PolicyVersion != "v1" || !policy.RequireGitNote || policy.ProhibitedTrailers == nil {
		t.Fatalf("decoded policy=%#v err=%v", policy, err)
	}
	for _, body := range []string{
		`{"schema_version":2,"action_type":"git_commit","policy_version":"v1","effect":"allow"}`,
		`{"schema_version":1,"action_type":"deploy","policy_version":"v1","effect":"allow"}`,
		`{"schema_version":1,"action_type":"git_commit","policy_version":"v1","effect":"unknown"}`,
		`{"schema_version":1,"action_type":"git_commit","policy_version":"v1","effect":"allow","prohibited_trailers":["Sign","sign:"]}`,
		`{"schema_version":1,"action_type":"git_commit","policy_version":"v1","effect":"allow","prohibited_trailers":["not a key"]}`,
		valid + valid,
		`{"schema_version":1,"action_type":"git_commit","policy_version":"v1","effect":"allow","prohibited_trailers":[],"content_hash":"sha256:wrong"}`,
	} {
		if _, err := DecodePolicy(strings.NewReader(body)); err == nil {
			t.Fatalf("accepted invalid policy: %s", body)
		}
	}
}

func TestInputLimits(t *testing.T) {
	if _, err := DecodePolicy(strings.NewReader(strings.Repeat("x", MaxInputBytes+1))); err == nil {
		t.Fatal("accepted oversized policy")
	}
	if _, err := ValidateCommitMessage(allowPolicy(), []byte(strings.Repeat("x", MaxInputBytes+1))); err == nil {
		t.Fatal("accepted oversized message")
	}
}
