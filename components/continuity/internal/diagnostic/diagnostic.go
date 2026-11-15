// Package diagnostic provides the same bounded recovery facts to CLI, hooks and MCP.
package diagnostic

import (
	"errors"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"strings"
)

type Report struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Recovery string `json:"recovery"`
	Advisory bool   `json:"advisory"`
}

func Bounded(text string) string {
	text = redact.String(text)
	if len(text) > 2048 {
		text = strings.ToValidUTF8(text[:2048], "") + "..."
	}
	return text
}

func From(err error) Report {
	r := Report{Code: "unavailable", Recovery: "Run `llewm doctor`; resolve the reported setup problem explicitly."}
	if err == nil {
		return Report{Code: "available"}
	}
	r.Message = Bounded(err.Error())
	var migration *database.MigrationRequiredError
	var missing *database.UninitializedError
	var writer *database.StaleWriterError
	var rebind *project.RebindRequiredError
	var coded interface{ DiagnosticCode() string }
	switch {
	case errors.As(err, &migration):
		r.Code = "migration_required"
		r.Recovery = "Review `llewm database status`; run the exact backup-gated migration shown in the message."
	case errors.As(err, &missing):
		r.Code = "uninitialized"
		r.Recovery = "Run `llewm init` in this project first."
	case errors.As(err, &writer):
		r.Code = "stale_writer"
		r.Recovery = "Restart the client so a compatible MCP process handles writes."
	case errors.As(err, &rebind):
		r.Code = "rebind_required"
		r.Recovery = "Review the project identity and explicitly run the suggested project rebind command."
	case errors.As(err, &coded):
		r.Code = coded.DiagnosticCode()
		r.Recovery = "Inspect the named instruction sources and bootstrap.max_bytes; retrieve the saved checkpoint explicitly."
	case errors.Is(err, project.ErrNotRegistered):
		r.Code = "unregistered"
		r.Recovery = "Run `llewm init` in this project first."
	default:
		return r
	}
	r.Advisory = true
	return r
}
