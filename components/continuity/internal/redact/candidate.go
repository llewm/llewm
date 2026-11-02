package redact

import "unicode/utf8"

// candidateStart finds the first prefix needed by any replacement rule. Search
// delimiters instead of asking the regexp engine to try every prose position.
// Replacement, capture and rule ordering still belong to the original regexps.
// Keep this recognizer and its differential fixtures aligned with patterns.
func candidateStart(value string) int {
	for i := 0; i < len(value); i++ {
		if value[i] != ':' && value[i] != '=' {
			continue
		}
		end := i
		for end > 0 && regexSpace(value[end-1]) {
			end--
		}
		if end > 0 && (value[end-1] == '"' || value[end-1] == '\'') {
			end--
		}
		for _, key := range [...]string{"apikey", "api_key", "api-key", "token", "password", "secret"} {
			if start := foldedSuffix(value[:end], key); start >= 0 {
				return start
			}
		}
		if value[i] != ':' {
			continue
		}
		if start := foldedSuffix(value[:i], "authorization"); start >= 0 {
			end := i + 1
			for end < len(value) && regexSpace(value[end]) {
				end++
			}
			// Bearer has only ASCII simple-fold equivalents.
			if end+6 < len(value) && foldedSuffix(value[end:end+6], "bearer") == 0 && regexSpace(value[end+6]) {
				return start
			}
		}
		if i+2 >= len(value) || value[i+1] != '/' || value[i+2] != '/' {
			continue
		}
		start := foldedSuffix(value[:i], "http")
		if start < 0 {
			start = foldedSuffix(value[:i], "https")
		}
		if start < 0 {
			continue
		}
		end = i + 3
		for end < len(value) && value[end] != ':' && value[end] != '/' && !regexSpace(value[end]) {
			end++
		}
		if end > i+3 && end < len(value) && value[end] == ':' {
			return start
		}
	}
	return -1
}

// Go regexp's ASCII Perl \s class deliberately excludes vertical tab and
// Unicode spaces. Matching that same class preserves the existing boundary.
func regexSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// All keywords are ASCII. Unicode SimpleFold adds only long s and Kelvin sign
// to the case-insensitive equivalents of their letters. Decode backwards so
// their different UTF-8 widths cannot shift a candidate's returned byte offset.
func foldedSuffix(value, keyword string) int {
	end := len(value)
	for i := len(keyword) - 1; i >= 0; i-- {
		if end == 0 {
			return -1
		}
		r, size := utf8.DecodeLastRuneInString(value[:end])
		end -= size
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		} else if r == 'ſ' {
			r = 's'
		} else if r == 'K' {
			r = 'k'
		}
		if r != rune(keyword[i]) {
			return -1
		}
	}
	return end
}
