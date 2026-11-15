package redact

import (
	"regexp"
	"strings"
)

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|token|password|secret)["']?\s*[=:]\s*["'])[^"'\r\n]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret)\s*[=:]\s*)[^\s"']+`),
}

type replacement struct {
	pattern *regexp.Regexp
	with    string
}

// Keep the historical assignment rules above; command forms also occur in
// narrative text, diagnostics and metadata, so they belong in this shared path.
// Quotes are consumed as a whole to cover values containing spaces or escapes.
const shellValue = `(?:"(?:\\.|[^"\\\r\n])*"|'[^'\r\n]*'|[^\s"';|&<>]+)`

var commandPatterns = []replacement{
	// Preserve the historical Bearer marker spelling for canonical records;
	// otherwise remove the whole header value, whatever the scheme.
	{regexp.MustCompile(`(?i)(authorization:\s*(?:bearer[ \t]+)?)[^\r\n"']+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)((?:--(?:api[_-]?key|token|password|secret)|-u|--user)(?:\s+|=))` + shellValue), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(\baws_(?:secret_access_key|access_key_id|session_token)(?:\s+|\s*=\s*))` + shellValue), `${1}[REDACTED]`},
	{regexp.MustCompile(`(^|[^A-Za-z0-9_-])(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|AKIA[A-Z0-9]{16}|xox[bp]-[A-Za-z0-9-]{10,})\b`), `${1}[REDACTED]`},
}

var userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/"'@]+@`)

func redactUserinfo(value string) string {
	return userinfoPattern.ReplaceAllStringFunc(value, func(match string) string {
		scheme, user, _ := strings.Cut(match, "://")
		// SSH usernames select a remote account and are part of supported Git
		// destinations. A password, including an encoded separator, is not.
		if (strings.EqualFold(scheme, "ssh") || strings.EqualFold(scheme, "git+ssh")) && !strings.Contains(user, ":") && !strings.Contains(strings.ToLower(user), "%3a") {
			return match
		}
		return scheme + "://[REDACTED]@"
	})
}

func replaceSecrets(value string) string {
	value = redactUserinfo(value)
	// Process URL userinfo before the legacy rule, which historically consumed @.
	// All command replacements are idempotent, including already-redacted URLs.
	for _, rule := range commandPatterns {
		value = rule.pattern.ReplaceAllString(value, rule.with)
	}
	for _, pattern := range patterns {
		value = pattern.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	return value
}

// The rules are fixed; Replacer supports concurrent calls and retains no input.
var metadataKeySeparators = strings.NewReplacer("_", "", "-", "")

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
			normal := strings.ToLower(metadataKeySeparators.Replace(key))
			switch normal {
			case "stdin", "env", "environment", "credentials", "authorization", "apikey", "token", "password", "secret", "accesstoken", "refreshtoken", "privatekey", "awssecretaccesskey", "awsaccesskeyid", "awssessiontoken":
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
	start := candidateStart(value)
	if start < 0 {
		return value
	}
	// Preserve the preceding quote or token boundary for replacement rules.
	if start > 0 {
		start--
	}
	suffix := value[start:]
	suffix = replaceSecrets(suffix)
	if suffix == value[start:] {
		return value
	}
	return value[:start] + suffix
}
