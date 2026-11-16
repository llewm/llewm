package authorship

import (
	"strconv"
	"strings"
	"time"
)

// RevalidationWindow bounds how long before an observation the commit-creating
// reflog entry may be. A hook observes at the end of the tool call that made the
// commit, so a genuine attribution is seconds to minutes after creation; a later
// observation saw history some other session, a person, or another host created.
const RevalidationWindow = 15 * time.Minute

// revalidationSkew tolerates reflog timestamps truncated to whole seconds.
const revalidationSkew = 2 * time.Second

// withinWindow reports whether a creation at created can be what an
// observation at observed saw a tool call make.
func withinWindow(created, observed time.Time) bool {
	return !created.After(observed.Add(revalidationSkew)) && observed.Sub(created) <= RevalidationWindow
}

// reflogTime parses the time from a reflog selector printed with --date=unix,
// such as HEAD@{1762900000}.
func reflogTime(selector string) (time.Time, bool) {
	open, end := strings.LastIndex(selector, "@{"), strings.LastIndex(selector, "}")
	if open < 0 || end < open {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(selector[open+2:end], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}
