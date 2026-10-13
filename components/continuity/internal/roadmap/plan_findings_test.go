package roadmap

import (
	"strings"
	"testing"
)

func TestPlanFindings(t *testing.T) {
	cases := []struct {
		name   string
		status string
		bound  bool
		plans  []planState
		want   string
	}{
		{"legacy node without binding or plan", "completed", false, nil, ""},
		{"bound node without plan", "active", true, nil, "missing_track_plan"},
		{"only superseded plans", "accepted", true, []planState{{"superseded", 0}}, "missing_track_plan"},
		{"bound node with proposal only", "active", true, []planState{{"proposed", 2}}, "track_plan_unreviewed"},
		{"reviewed plan in progress", "active", true, []planState{{"accepted", 1}}, ""},
		{"completed node with open plan", "completed", true, []planState{{"accepted", 1}}, "track_plan_behind"},
		{"completed node with finished proposal", "completed", true, []planState{{"proposed", 0}}, "track_plan_unreviewed,track_plan_behind"},
		{"completed node and plan", "completed", true, []planState{{"completed", 0}, {"superseded", 3}}, ""},
		{"finished plan on open node", "accepted", true, []planState{{"completed", 0}}, "track_plan_behind"},
		{"completed legacy node bound without a plan", "completed", true, nil, ""},
		{"accepted plan on a deferred node", "deferred", false, []planState{{"accepted", 2}}, "work_on_inactive_node"},
		{"deferred node with a finished plan", "deferred", false, []planState{{"completed", 0}}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(planFindings(c.status, c.bound, c.plans), ","); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
