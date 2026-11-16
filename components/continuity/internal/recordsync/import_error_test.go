package recordsync

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestImportPlanErrorPreservesSafeValidationReason(t *testing.T) {
	plan := portable.ImportPlan{
		Counts: map[portable.PlanDisposition]int{portable.PlanInvalid: 1},
		Items: []portable.ImportPlanItem{
			{Disposition: portable.PlanAppend, Reason: "not a failure"},
			{Disposition: portable.PlanInvalid, Reason: "missing predecessor; token=supersecret"},
		},
	}
	got := importPlanError(plan).Error()
	if !strings.Contains(got, "1 invalid, 0 unsupported records: missing predecessor;") || strings.Contains(got, "supersecret") || strings.Contains(got, "not a failure") {
		t.Fatalf("lost diagnostic or exposed unrelated/secret data: %s", got)
	}
	plan.Items[1].Reason = strings.Repeat("界", 3000)
	got = importPlanError(plan).Error()
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) > 2200 || !strings.HasSuffix(got, "…") {
		t.Fatal("diagnostic is not bounded valid UTF-8")
	}
	plan.Items = nil
	if got := importPlanError(plan).Error(); got != "memory import refused: 1 invalid, 0 unsupported records" {
		t.Fatalf("missing-reason fallback: %s", got)
	}
	plan.Counts = map[portable.PlanDisposition]int{portable.PlanUnsupported: 1}
	plan.Items = []portable.ImportPlanItem{{Disposition: portable.PlanUnsupported, Reason: "future schema"}}
	if got := importPlanError(plan).Error(); !strings.HasSuffix(got, ": future schema") {
		t.Fatalf("unsupported reason lost: %s", got)
	}
}
