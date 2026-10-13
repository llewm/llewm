package validator

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var trailerKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

const (
	PolicySchemaVersion = 1
	MaxInputBytes       = 1 << 20
)

type Policy struct {
	SchemaVersion      int      `json:"schema_version"`
	ActionType         string   `json:"action_type"`
	PolicyVersion      string   `json:"policy_version"`
	Effect             string   `json:"effect"`
	ProhibitedTrailers []string `json:"prohibited_trailers"`
	RequireGitNote     bool     `json:"require_git_note"`
	ContentHash        string   `json:"content_hash,omitempty"`
	Authority          string   `json:"authority,omitempty"`
}

type policyContent struct {
	SchemaVersion      int      `json:"schema_version"`
	ActionType         string   `json:"action_type"`
	PolicyVersion      string   `json:"policy_version"`
	Effect             string   `json:"effect"`
	ProhibitedTrailers []string `json:"prohibited_trailers"`
	RequireGitNote     bool     `json:"require_git_note"`
}

func DecodePolicy(reader io.Reader) (Policy, error) {
	limited := io.LimitReader(reader, MaxInputBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Policy{}, err
	}
	if len(body) > MaxInputBytes {
		return Policy{}, fmt.Errorf("policy exceeds 1 MiB limit")
	}
	var policy Policy
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode policy: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Policy{}, err
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode policy: multiple JSON values")
		}
		return fmt.Errorf("decode policy: %w", err)
	}
	return nil
}

func (policy Policy) Validate() error {
	if policy.SchemaVersion != PolicySchemaVersion {
		return fmt.Errorf("unsupported policy schema version %d", policy.SchemaVersion)
	}
	if policy.ActionType != "git_commit" {
		return fmt.Errorf("unsupported action type %q", policy.ActionType)
	}
	if strings.TrimSpace(policy.PolicyVersion) == "" {
		return fmt.Errorf("policy_version is required")
	}
	switch policy.Effect {
	case "allow", "deny", "proposal_only":
	default:
		return fmt.Errorf("unsupported policy effect %q", policy.Effect)
	}
	if len(policy.ProhibitedTrailers) > 64 {
		return fmt.Errorf("prohibited_trailers exceeds 64-item limit")
	}
	seen := map[string]bool{}
	for _, trailer := range policy.ProhibitedTrailers {
		key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(trailer), ":"))
		if key == "" {
			return fmt.Errorf("prohibited trailer key must not be blank")
		}
		if !trailerKey.MatchString(key) {
			return fmt.Errorf("invalid prohibited trailer key %q", trailer)
		}
		if seen[key] {
			return fmt.Errorf("duplicate prohibited trailer key %q", trailer)
		}
		seen[key] = true
	}
	if policy.ContentHash != "" {
		content, err := json.Marshal(policyContent{SchemaVersion: policy.SchemaVersion, ActionType: policy.ActionType, PolicyVersion: policy.PolicyVersion, Effect: policy.Effect, ProhibitedTrailers: policy.ProhibitedTrailers, RequireGitNote: policy.RequireGitNote})
		if err != nil {
			return err
		}
		actual := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		if policy.ContentHash != actual {
			return fmt.Errorf("policy content_hash mismatch: got %q, computed %q", policy.ContentHash, actual)
		}
	}
	return nil
}
