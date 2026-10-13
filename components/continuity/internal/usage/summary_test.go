package usage

import (
	"testing"
	"time"
)

func TestSummaryIDPreservesSchemaOneHash(t *testing.T) {
	value := Summary{
		SchemaVersion: 1,
		Client:        "codex",
		SessionID:     "session",
		InstanceID:    "instance",
		FirstAt:       time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC),
		LastAt:        time.Date(2026, 10, 5, 8, 1, 0, 0, time.UTC),
		TaskID:        "task",
		Overhead:      42,
		Models:        []ModelUsage{},
		Tools:         []ToolUsage{},
	}
	if got, want := SummaryID(value), "codex:session:6cac2bb28631c916"; got != want {
		t.Fatalf("schema-1 summary ID = %q, want %q", got, want)
	}
}
