// Package projectpath defines project-relative slash paths whose meaning does
// not depend on the host operating system.
//
// Records carry these paths between peers, so every host must normalize and
// admit them identically. Host path/filepath rules differ: on Windows,
// filepath.Clean keeps or adds a "./" prefix when the first element contains a
// colon (so "https:/x" is not read as a volume), and filepath.IsAbs disagrees
// about "/x" and "C:/x". Only the slash-separated path package is used here.
package projectpath

import (
	"path"
	"path/filepath"
	"strings"
)

// Normalize converts host input into the canonical stored form: host
// separators become slashes and the result is cleaned with slash semantics.
// It reports false when the result is not a valid new project-relative path.
func Normalize(value string) (string, bool) {
	value = path.Clean(filepath.ToSlash(value))
	return value, Valid(value)
}

// Valid reports whether value is a canonical project-relative path that new
// records may store: non-empty, slash-cleaned, not the project root, inside
// the project, and without a colon in its first element. The colon rule
// refuses URLs and Windows volume or drive-relative forms, which do not name
// the same file on every host.
func Valid(value string) bool {
	if !insideProject(value) || value == "." || path.Clean(value) != value {
		return false
	}
	first, _, _ := strings.Cut(value, "/")
	return !strings.Contains(first, ":")
}

// Admitted reports whether a path retained in a received record is admissible.
// It accepts every form earlier writers could store on any supported host:
// slash-cleaned paths (including the project root "." and a colon in the first
// element), plus the Windows form "./" followed by a cleaned path whose first
// element has a colon. The decision is identical on every host.
func Admitted(value string) bool {
	if !insideProject(value) {
		return false
	}
	if path.Clean(value) == value {
		return true
	}
	rest, ok := strings.CutPrefix(value, "./")
	if !ok || !insideProject(rest) || path.Clean(rest) != rest {
		return false
	}
	first, _, _ := strings.Cut(rest, "/")
	return strings.Contains(first, ":")
}

func insideProject(value string) bool {
	return value != "" && value != ".." && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "../") && strings.IndexByte(value, 0) < 0
}
