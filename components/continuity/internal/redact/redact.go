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
	for _, pattern := range patterns {
		value = pattern.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	return value
}
