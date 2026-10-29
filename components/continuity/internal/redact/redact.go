package redact

import (
	"regexp"
	"strings"
)

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|token|password|secret)["']?\s*[=:]\s*["'])[^"'\r\n]+`),
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"']+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret)\s*[=:]\s*)[^\s"']+`),
	regexp.MustCompile(`(?i)(https?://[^:/\s]+:)[^@\s]+@`),
}

// Every match above requires one of these prefixes. Checking them together
// avoids four complete replacement scans for ordinary historical prose. Keep
// this case-folded regexp aligned with patterns, including Unicode folding;
// a positive result always runs the original replacements in their order.
var candidate = regexp.MustCompile(`(?i)(?:(?:api[_-]?key|token|password|secret)["']?\s*[=:]|authorization:\s*bearer\s+|https?://[^:/\s]+:)`)

// Value sanitizes decoded JSON structurally so nested metadata cannot bypass
// string redaction. Entire raw-input/environment fields are never retained.
func Value(value any) any {
	switch v := value.(type) {
	case string:
		return String(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Value(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			cleanKey := String(key)
			normal := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normal {
			case "stdin", "env", "environment", "credentials", "authorization", "apikey", "token", "password", "secret", "accesstoken", "refreshtoken", "privatekey":
				out[cleanKey] = "[REDACTED]"
			default:
				out[cleanKey] = Value(item)
			}
		}
		return out
	default:
		return value
	}
}

func String(value string) string {
	if !strings.ContainsAny(value, ":=") {
		return value
	}
	match := candidate.FindStringIndex(value)
	if match == nil {
		return value
	}
	start := match[0]
	// The first replacement can include the quote immediately before its
	// keyword. No rule can begin earlier than this first necessary prefix.
	if start > 0 && (value[start-1] == '"' || value[start-1] == '\'') {
		start--
	}
	suffix := value[start:]
	for _, pattern := range patterns {
		suffix = pattern.ReplaceAllString(suffix, `${1}[REDACTED]`)
	}
	if suffix == value[start:] {
		return value
	}
	return value[:start] + suffix
}
