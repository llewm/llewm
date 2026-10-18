// Package gitprotection inspects Git exclusion of local project state without
// editing ignore rules or the index.
package gitprotection

import (
	"bytes"
	"context"
	"errors"
	"os/exec"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

type Report struct {
	Ignored      bool   `json:"ignored"`
	TrackedFiles int    `json:"tracked_files"`
	Error        string `json:"error,omitempty"`
}

func Inspect(ctx context.Context, root string) Report {
	var r Report
	// --no-index separates exclusion rules from already-tracked state. Checking
	// the directory requires whole-footprint protection, not just database files.
	err := gitenv.Command(ctx, "-C", root, "check-ignore", "--no-index", "-q", "--", ".llewm/").Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		r.Ignored = true
	case errors.As(err, &exit) && exit.ExitCode() == 1:
	default:
		r.Error = "could not inspect .llewm/ Git exclusion"
	}
	tracked, err := gitenv.Command(ctx, "-C", root, "ls-files", "-z", "--", ".llewm").Output()
	if err != nil {
		r.Error = "could not inspect tracked .llewm/ state"
	} else {
		r.TrackedFiles = bytes.Count(tracked, []byte{0})
	}
	return r
}

func (r Report) Warnings() []string {
	var warnings []string
	if r.Error != "" {
		warnings = append(warnings, r.Error)
	} else if !r.Ignored {
		warnings = append(warnings, "ignore the whole .llewm/ directory: add /.llewm/ to the root .gitignore (shared) or Git info/exclude (local); see project-layout.md#keep-local-state-out-of-git")
	}
	if r.TrackedFiles > 0 {
		warnings = append(warnings, ".llewm/ contains tracked files; ignore rules do not untrack them; review separately before changing the index or published history")
	}
	return warnings
}
