package validator

import (
	"fmt"
	"regexp"
	"strings"
)

var trailerLine = regexp.MustCompile(`^[ \t]*([A-Za-z0-9][A-Za-z0-9-]*):[ \t]*(.*)$`)

type Diagnostic struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Line       int    `json:"line,omitempty"`
	TrailerKey string `json:"trailer_key,omitempty"`
}

type Result struct {
	Valid           bool         `json:"valid"`
	PolicyVersion   string       `json:"policy_version"`
	PolicyEffect    string       `json:"policy_effect"`
	PolicyHash      string       `json:"policy_hash,omitempty"`
	PolicyAuthority string       `json:"policy_authority,omitempty"`
	RequireGitNote  bool         `json:"require_git_note"`
	Diagnostics     []Diagnostic `json:"diagnostics"`
}

type Trailer struct {
	Key  string
	Line int
}

func ValidateCommitMessage(policy Policy, message []byte) (Result, error) {
	if err := policy.Validate(); err != nil {
		return Result{}, err
	}
	if len(message) > MaxInputBytes {
		return Result{}, fmt.Errorf("commit message exceeds 1 MiB limit")
	}
	result := Result{Valid: true, PolicyVersion: policy.PolicyVersion, PolicyEffect: policy.Effect, PolicyHash: policy.ContentHash, PolicyAuthority: policy.Authority, RequireGitNote: policy.RequireGitNote, Diagnostics: make([]Diagnostic, 0)}
	switch policy.Effect {
	case "deny":
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "action_denied", Message: "policy denies Git commit actions"})
	case "proposal_only":
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "proposal_only", Message: "policy permits a proposal only, not a Git commit action"})
	}
	prohibited := map[string]string{}
	for _, key := range policy.ProhibitedTrailers {
		normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(key), ":"))
		prohibited[normalized] = strings.TrimSuffix(strings.TrimSpace(key), ":")
	}
	for _, trailer := range ParseTrailers(message) {
		if configured, found := prohibited[strings.ToLower(trailer.Key)]; found {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "prohibited_trailer", Message: fmt.Sprintf("Git trailer %q is prohibited", configured), Line: trailer.Line, TrailerKey: trailer.Key})
		}
	}
	result.Valid = len(result.Diagnostics) == 0
	return result, nil
}

func ParseTrailers(message []byte) []Trailer {
	normalized := strings.ReplaceAll(string(message), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	// The default Git commit template appends comment lines. Ignore those lines
	// for trailer-block discovery while retaining original line numbers.
	type sourceLine struct {
		text string
		line int
	}
	visible := make([]sourceLine, 0, len(lines))
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		visible = append(visible, sourceLine{text: line, line: index + 1})
	}
	lines = lines[:0]
	lineNumbers := make([]int, 0, len(visible))
	for _, line := range visible {
		lines = append(lines, line.text)
		lineNumbers = append(lineNumbers, line.line)
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	start := len(lines) - 1
	for start > 0 && strings.TrimSpace(lines[start-1]) != "" {
		start--
	}
	trailers := make([]Trailer, 0)
	for index := start; index < len(lines); index++ {
		match := trailerLine.FindStringSubmatch(lines[index])
		if match != nil {
			trailers = append(trailers, Trailer{Key: match[1], Line: lineNumbers[index]})
			continue
		}
		if (strings.HasPrefix(lines[index], " ") || strings.HasPrefix(lines[index], "\t")) && len(trailers) > 0 {
			continue
		}
		return nil
	}
	return trailers
}
