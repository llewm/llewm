//go:build !windows

package gitenv

// platformArgs are Git options every Continuity Git command needs on this
// platform; none are required outside Windows.
var platformArgs []string
