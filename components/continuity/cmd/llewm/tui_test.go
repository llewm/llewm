package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/deliverystatus"
	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/timeline"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/workstate"
	"github.com/mattn/go-runewidth"
)

type fakeTerminalSource struct {
	showError          string
	workError          string
	project            projectView
	checkpointPages    map[string]checkpoint.ListPage
	checkpointRecords  map[string]*checkpoint.Checkpoint
	health             readiness.Report
	releases           release.View
	report             *workitem.BugReport
	delivery           *deliverystatus.Snapshot
	failure            string
	checkpointRequests []checkpoint.ListRequest
	work               workstate.List
	timeline           timeline.Page
	decisions          decision.ListPage
	records            map[string]decision.Record
	plans              []trackplan.Plan
	ideas              []workitem.Summary
	topics             []topic.Summary
	hits               []history.Hit
	searches           []history.SearchOptions
	current            *checkpoint.Checkpoint
}

func (f *fakeTerminalSource) Current(context.Context) (*checkpoint.Checkpoint, error) {
	return f.current, nil
}

func (f *fakeTerminalSource) Ideas(_ context.Context, status string) (workitem.ListPage, error) {
	page := workitem.ListPage{}
	for _, i := range f.ideas {
		if status == "" || i.Status == status || status == "open" && !workitem.IsTerminal(i.Status) {
			page.Items = append(page.Items, i)
		}
	}
	page.Total = len(page.Items)
	return page, nil
}

func (f *fakeTerminalSource) Topics(context.Context) ([]topic.Summary, error) { return f.topics, nil }

// HistorySearch filters fake hits by kind and pages them; it records each request.
func (f *fakeTerminalSource) HistorySearch(_ context.Context, options history.SearchOptions) (history.SearchPage, error) {
	f.searches = append(f.searches, options)
	var matched []history.Hit
	for _, hit := range f.hits {
		if len(options.Kinds) == 0 || hit.Kind == options.Kinds[0] {
			matched = append(matched, hit)
		}
	}
	page := history.SearchPage{Candidates: len(matched)}
	end := min(options.Offset+options.Limit, len(matched))
	if options.Offset < end {
		page.Hits = matched[options.Offset:end]
	}
	return page, nil
}

func (f *fakeTerminalSource) HistoryShow(_ context.Context, refs []string) (history.ShowPage, error) {
	if f.showError != "" {
		return history.ShowPage{}, errors.New(f.showError)
	}
	page := history.ShowPage{}
	for _, ref := range refs {
		page.Items = append(page.Items, history.Item{Ref: ref, Family: "fake", Title: "Title of " + ref, Text: "Full text of " + ref})
	}
	return page, nil
}

func (f *fakeTerminalSource) Work(context.Context, string) (workstate.List, error) {
	if f.workError != "" {
		return workstate.List{}, errors.New(f.workError)
	}
	return f.work, nil
}

func (f *fakeTerminalSource) Timeline(context.Context, string, string) (timeline.Page, error) {
	return f.timeline, nil
}

func (f *fakeTerminalSource) Decisions(context.Context, int) (decision.ListPage, error) {
	return f.decisions, nil
}

func (f *fakeTerminalSource) Decision(_ context.Context, id string) (decision.Record, error) {
	return f.records[id], nil
}

func (f *fakeTerminalSource) Backlinks(context.Context, string, string, string) (decision.BacklinkPage, error) {
	return decision.BacklinkPage{IncludedFamilies: []string{"decision"}}, nil
}

func (f *fakeTerminalSource) Plans(_ context.Context, nodeID, taskID string) (trackplan.PlanListPage, error) {
	latest := map[string]trackplan.Plan{}
	var order []string
	for _, plan := range f.plans {
		if (nodeID == "" || plan.RoadmapNodeID == nodeID) && (taskID == "" || plan.TaskID == taskID) {
			if _, ok := latest[plan.ID]; !ok {
				order = append(order, plan.ID)
			}
			if plan.Revision > latest[plan.ID].Revision {
				latest[plan.ID] = plan
			}
		}
	}
	page := trackplan.PlanListPage{Plans: []trackplan.PlanSummary{}}
	for _, id := range order {
		plan := latest[id]
		page.Plans = append(page.Plans, trackplan.PlanSummary{ID: plan.ID, Revision: plan.Revision, RoadmapNodeID: plan.RoadmapNodeID, TaskID: plan.TaskID, Title: plan.Title, Status: plan.Status, ItemCount: len(plan.Items)})
	}
	page.Total = len(page.Plans)
	return page, nil
}

func (f *fakeTerminalSource) Plan(_ context.Context, id string, revision int) (*trackplan.Plan, error) {
	var found *trackplan.Plan
	for i := range f.plans {
		plan := &f.plans[i]
		if plan.ID == id && (plan.Revision == revision || revision == 0 && (found == nil || plan.Revision > found.Revision)) {
			found = plan
		}
	}
	if found == nil {
		return nil, errors.New("plan not found")
	}
	return found, nil
}

func newFakeTerminal(t *testing.T) (*terminalView, *fakeTerminalSource) {
	t.Helper()
	source := &fakeTerminalSource{records: map[string]decision.Record{}, checkpointRecords: map[string]*checkpoint.Checkpoint{"c1": {ID: "c1", TaskID: "task-one", Task: "Checkpoint one"}}}
	for i := 0; i < 12; i++ {
		source.work.Items = append(source.work.Items, roadmap.TrackInventoryItem{Node: &roadmap.NodeRef{ID: "node-" + string(rune('a'+i)), Title: "Track title", Status: "accepted"}})
		source.timeline.Entries = append(source.timeline.Entries, timeline.Entry{Kind: "checkpoint", Summary: "event", RecordedAt: "2026-09-26T08:43:24Z"})
	}
	source.work.InventoryTotal = 12
	source.decisions.Decisions = []decision.Summary{{ID: "d1", Title: "Choose local state", Disposition: "accepted"}}
	source.decisions.Total = 1
	source.records["d1"] = decision.Record{ID: "d1", Title: "Choose local state", Disposition: "accepted", Rationale: strings.Repeat("rationale words wrap ", 12) + "END"}
	items := []trackplan.Item{
		{ID: "child-b", Position: 2, Title: "Child B", Status: "pending", ParentID: "root", DependsOn: []string{"child-a", "root"}},
		{ID: "root", Position: 1, Title: "Root", Status: "active"},
		{ID: "child-a", Position: 1, Title: "Child A", Status: "completed", ParentID: "root", Detail: "Item detail text", EvidenceRefs: []string{"commit:abc"}},
		{ID: "old", Position: 3, Title: "Old", Status: "superseded"},
	}
	source.plans = []trackplan.Plan{
		{ID: "plan-a", Revision: 1, RoadmapNodeID: "node-a", Title: "Plan A", Status: "proposed", Items: items[:1]},
		{ID: "plan-a", Revision: 2, RoadmapNodeID: "node-a", Title: "Plan A", Status: "accepted", Summary: "Plan A summary", Items: items},
		{ID: "plan-b1", Revision: 1, RoadmapNodeID: "node-b", Title: "Plan B1", Status: "completed"},
		{ID: "plan-b2", Revision: 1, RoadmapNodeID: "node-b", Title: "Plan B2", Status: "accepted", Items: items[1:3]},
	}
	source.ideas = []workitem.Summary{{ID: "tui-ideas", Kind: "idea", Title: "Show ideas", Status: "pending"}, {ID: "done", Kind: "idea", Title: "Done idea", Status: "integrated"}}
	source.topics = []topic.Summary{{Topic: topic.Topic{ID: "reference-cards", Title: "Reference cards", Status: "accepted"}, Aliases: []string{"cards"}, AcceptedLinks: 3}}
	for i := 0; i < 60; i++ {
		kind := "outcome"
		if i%3 == 0 {
			kind = "decision"
		}
		source.hits = append(source.hits, history.Hit{Ref: fmt.Sprintf("narrative:n%02d#%d", i, i%3), Kind: kind, Title: fmt.Sprintf("Note %02d", i), Snippet: "snippet", RecordedAt: "2026-09-15T03:55:00Z", Derivations: []string{"explicit_link"}})
	}
	source.records["d1"] = decision.Record{ID: "d1", Title: "Choose local state", Disposition: "accepted", Rationale: strings.Repeat("rationale words wrap ", 12) + "END", Links: []decision.Link{{Kind: "subject", TargetKind: "roadmap_node", TargetID: "node-a", TargetRevision: "1"}, {Kind: "resulted_in", TargetKind: "checkpoint", TargetID: "c1"}}}
	view := newTerminalView(source)
	if err := view.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return view, source
}

func press(t *testing.T, v *terminalView, keys ...any) error {
	t.Helper()
	var err error
	for _, key := range keys {
		switch k := key.(type) {
		case rune:
			err = v.key(context.Background(), tcell.NewEventKey(tcell.KeyRune, k, tcell.ModNone))
		case tcell.Key:
			err = v.key(context.Background(), tcell.NewEventKey(k, 0, tcell.ModNone))
		}
	}
	return err
}

func simulationScreen(t *testing.T, width, height int) tcell.SimulationScreen {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(screen.Fini)
	screen.SetSize(width, height)
	return screen
}

func screenText(screen tcell.SimulationScreen) []string {
	cells, width, height := screen.GetContents()
	lines := make([]string, height)
	for y := 0; y < height; y++ {
		var b strings.Builder
		for x := 0; x < width; x++ {
			runes := cells[y*width+x].Runes
			if len(runes) == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteString(string(runes))
		}
		lines[y] = strings.TrimRight(b.String(), " ")
	}
	return lines
}

func TestTUIRedirectedTerminalFallback(t *testing.T) {
	cmd := newRootCommand("llewm")
	cmd.SetArgs([]string{"tui"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "llewm work list") || !strings.Contains(err.Error(), "llewm history search") {
		t.Fatalf("expected actionable fallback, got %v", err)
	}
}

func TestTUISwitchesTopLevelViews(t *testing.T) {
	view, _ := newFakeTerminal(t)
	for _, want := range []string{"timeline", "decisions", "ideas", "history", "project", "checkpoints", "health", "releases", "delivery", "tracks"} {
		if err := press(t, view, tcell.KeyTab); err != nil {
			t.Fatal(err)
		}
		if view.view != want {
			t.Fatalf("tab view = %s, want %s", view.view, want)
		}
	}
	if err := press(t, view, tcell.KeyBacktab); err != nil || view.view != "delivery" {
		t.Fatalf("backtab view = %s, %v", view.view, err)
	}
	if err := press(t, view, '2'); err != nil || view.view != "timeline" || view.details[0] != "Kind: checkpoint" {
		t.Fatalf("direct view = %s, detail %v, %v", view.view, view.details, err)
	}
	if err := press(t, view, tcell.KeyEscape); err != nil {
		t.Fatalf("escape must not quit: %v", err)
	}
	if err := press(t, view, 'q'); !errors.Is(err, errTerminalQuit) {
		t.Fatalf("q = %v", err)
	}
}

func TestTUIKeepsSelectedRowsVisible(t *testing.T) {
	view, _ := newFakeTerminal(t)
	screen := simulationScreen(t, 90, 14)
	if err := press(t, view, tcell.KeyEnd); err != nil || view.workIndex != 11 {
		t.Fatalf("end index = %d, %v", view.workIndex, err)
	}
	view.draw(screen)
	lines := screenText(screen)
	if !strings.Contains(strings.Join(lines, "\n"), "node-l") {
		t.Fatalf("selected row not visible:\n%s", strings.Join(lines, "\n"))
	}
	if got := visibleStart(11, 6); got != 6 {
		t.Fatalf("visible start = %d", got)
	}
	_ = press(t, view, tcell.KeyDown)
	if view.workIndex != 11 {
		t.Fatalf("selection overflow: %d", view.workIndex)
	}
	_ = press(t, view, tcell.KeyUp, tcell.KeyHome)
	if view.workIndex != 0 {
		t.Fatalf("home index = %d", view.workIndex)
	}
}

func TestTUIDetailPaneWrapsAtFullWidthAndScrolls(t *testing.T) {
	view, _ := newFakeTerminal(t)
	if err := press(t, view, '3'); err != nil {
		t.Fatal(err)
	}
	screen := simulationScreen(t, 60, 14)
	view.draw(screen)
	text := strings.Join(screenText(screen), "\n")
	if !strings.Contains(text, "── Detail") || !strings.Contains(text, "Rationale: rationale words") {
		t.Fatalf("detail pane missing:\n%s", text)
	}
	if view.detailRows >= 14 || view.detailRows < 2 {
		t.Fatalf("detail rows = %d", view.detailRows)
	}
	_ = press(t, view, tcell.KeyPgDn, tcell.KeyPgDn, tcell.KeyPgDn, tcell.KeyPgDn)
	view.draw(screen)
	wrapped := wrapLines(view.details, 60)
	if view.detailScroll != max(0, len(wrapped)-view.detailRows) {
		t.Fatalf("scroll = %d, wrapped %d, rows %d", view.detailScroll, len(wrapped), view.detailRows)
	}
	_ = press(t, view, 'z')
	view.draw(screen)
	if !view.expanded || view.detailRows != 9 {
		t.Fatalf("expanded = %t rows = %d", view.expanded, view.detailRows)
	}
	_ = press(t, view, tcell.KeyEscape)
	if view.expanded {
		t.Fatal("escape did not collapse detail")
	}
}

func TestTUIWrapLines(t *testing.T) {
	lines := wrapLines([]string{"short", "Rationale: " + strings.Repeat("word ", 10) + strings.Repeat("x", 30)}, 20)
	if lines[0] != "short" {
		t.Fatalf("short line = %q", lines[0])
	}
	for i, line := range lines {
		if runewidth.StringWidth(line) > 20 {
			t.Fatalf("line %d too wide: %q", i, line)
		}
		if i > 1 && !strings.HasPrefix(line, "  ") {
			t.Fatalf("continuation %d not indented: %q", i, line)
		}
	}
	if joined := strings.ReplaceAll(strings.Join(lines[1:], ""), " ", ""); !strings.HasSuffix(joined, strings.Repeat("x", 30)) {
		t.Fatalf("long word lost: %q", joined)
	}
}

func TestTUILayoutRowAlignsColumns(t *testing.T) {
	row := layoutRow(40, []string{"●", "accepted", "P1"}, []int{2, 11, 5}, strings.Repeat("title ", 10), "3 findings")
	if runewidth.StringWidth(row) != 40 || !strings.HasSuffix(row, " 3 findings") || !strings.Contains(row, "…") {
		t.Fatalf("row = %q (%d)", row, runewidth.StringWidth(row))
	}
}

func TestTUIDrillsFromTrackIntoPlanAndBack(t *testing.T) {
	view, _ := newFakeTerminal(t)
	screen := simulationScreen(t, 100, 20)
	view.draw(screen)
	if text := strings.Join(screenText(screen), "\n"); !strings.Contains(text, "plan 1/3") || !strings.Contains(text, "plan 1/2") {
		t.Fatalf("track progress missing:\n%s", text)
	}
	if err := press(t, view, tcell.KeyRight); err != nil {
		t.Fatal(err)
	}
	if level := view.level(); level == nil || level.kind != "history" || level.topic != "node-a" {
		t.Fatalf("-> on a track row opens its history = %+v", level)
	}
	if err := press(t, view, tcell.KeyEscape); err != nil || view.level() != nil {
		t.Fatalf("escape back from history = %+v %v", view.level(), err)
	}
	if err := press(t, view, 'p'); err != nil {
		t.Fatal(err)
	}
	level := view.level()
	if level == nil || level.kind != "plan" || level.plan.Revision != 2 || view.context() != "Tracks › node-a › plan@2" {
		t.Fatalf("single plan drill = %+v, context %q", level, view.context())
	}
	var order []string
	for _, row := range level.items {
		order = append(order, strings.Repeat(">", row.depth)+row.item.ID)
	}
	if strings.Join(order, " ") != "root >child-a >child-b old" {
		t.Fatalf("plan tree order = %v", order)
	}
	_ = press(t, view, tcell.KeyDown)
	if strings.Join(view.details, "\n") == "" || !strings.Contains(strings.Join(view.details, "\n"), "Detail: Item detail text") || !strings.Contains(strings.Join(view.details, "\n"), "Evidence: commit:abc") {
		t.Fatalf("item detail = %v", view.details)
	}
	view.draw(screen)
	if text := strings.Join(screenText(screen), "\n"); !strings.Contains(text, "  Child B") || !strings.Contains(text, "needs root") || strings.Contains(text, "needs child-a") {
		t.Fatalf("plan rows missing:\n%s", text)
	}
	if err := press(t, view, 'f'); err != nil || view.filter != "current" {
		t.Fatalf("filter changed inside plan: %s %v", view.filter, err)
	}
	if err := press(t, view, tcell.KeyEscape); err != nil || view.level() != nil || view.workIndex != 0 {
		t.Fatalf("escape back = %+v %v", view.level(), err)
	}

	_ = press(t, view, tcell.KeyDown, 'p')
	if level := view.level(); level == nil || level.kind != "plans" || len(level.plans) != 2 {
		t.Fatalf("multiple plan drill = %+v", level)
	}
	_ = press(t, view, tcell.KeyDown, tcell.KeyRight)
	if view.context() != "Tracks › node-b › 2 plans › plan@1" || view.level().plan.ID != "plan-b2" {
		t.Fatalf("context = %q", view.context())
	}
	_ = press(t, view, tcell.KeyLeft)
	if view.level() == nil || view.level().kind != "plans" || view.level().index != 1 {
		t.Fatalf("back did not restore plan list selection: %+v", view.level())
	}
	_ = press(t, view, tcell.KeyTab)
	if view.level() != nil || view.view != "timeline" {
		t.Fatalf("view switch kept stack: %+v", view.level())
	}
	_ = press(t, view, '1', tcell.KeyDown, tcell.KeyDown, tcell.KeyRight)
	if level := view.level(); level == nil || level.kind != "history" || level.topic != "node-d" {
		t.Fatalf("no-plan track still drills to its history via -> = %+v", level)
	}
	_ = press(t, view, tcell.KeyEscape)
	if err := press(t, view, 'p'); err != nil || view.level() != nil || !strings.Contains(view.status, "No track plan recorded for node-d") {
		t.Fatalf("p on a no-plan track should not navigate = %+v %q %v", view.level(), view.status, err)
	}
}

func TestTUISpaceAndPOpenTrackPlan(t *testing.T) {
	view, _ := newFakeTerminal(t)

	if err := press(t, view, ' '); err != nil {
		t.Fatal(err)
	}
	if level := view.level(); level == nil || level.kind != "history" || level.topic != "node-a" {
		t.Fatalf("space on a track row opens its history = %+v", level)
	}
	if err := press(t, view, tcell.KeyEscape); err != nil || view.level() != nil {
		t.Fatalf("escape back from history = %+v %v", view.level(), err)
	}

	if err := press(t, view, 'p'); err != nil {
		t.Fatal(err)
	}
	if level := view.level(); level == nil || level.kind != "plan" || level.plan.Revision != 2 {
		t.Fatalf("p opens the plan = %+v", level)
	}
	if err := press(t, view, 'p'); err != nil || view.level() != nil {
		t.Fatalf("p toggles the plan closed = %+v %v", view.level(), err)
	}
}

func TestTUIPlanRowsSurviveParentCycles(t *testing.T) {
	rows := planRows([]trackplan.Item{{ID: "a", ParentID: "b"}, {ID: "b", ParentID: "a"}, {ID: "c", ParentID: "c"}, {ID: "d", ParentID: "missing"}})
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.item.ID)
	}
	if strings.Join(ids, ",") != "c,d,a,b" {
		t.Fatalf("rows = %v", ids)
	}
}

func TestTUIStepsPlanRevisionsAndShowsHelp(t *testing.T) {
	view, _ := newFakeTerminal(t)
	if err := press(t, view, ']'); err != nil || !strings.Contains(view.status, "inside an open plan") {
		t.Fatalf("revision outside plan = %q %v", view.status, err)
	}
	_ = press(t, view, 'p', tcell.KeyEnd)
	if view.level().plan.Revision != 2 || view.level().index != 3 {
		t.Fatalf("opened plan = %+v", view.level())
	}
	_ = press(t, view, ']')
	if view.level().plan.Revision != 2 || !strings.Contains(view.status, "from 1 to 2") {
		t.Fatalf("stepped past latest: %d %q", view.level().plan.Revision, view.status)
	}
	_ = press(t, view, '[')
	level := view.level()
	if level.plan.Revision != 1 || len(level.items) != 1 || level.index != 0 || view.context() != "Tracks › node-a › plan@1" {
		t.Fatalf("previous revision = %+v context %q", level, view.context())
	}
	if !strings.Contains(view.details[0], "plan-a@1 (latest 2)") {
		t.Fatalf("revision header = %q", view.details[0])
	}
	_ = press(t, view, ']')
	if view.level().plan.Revision != 2 {
		t.Fatalf("next revision = %d", view.level().plan.Revision)
	}

	screen := simulationScreen(t, 80, 24)
	_ = press(t, view, '?')
	view.draw(screen)
	if text := strings.Join(screenText(screen), "\n"); !view.help || !strings.Contains(text, "previous / next plan revision") {
		t.Fatalf("help not shown:\n%s", text)
	}
	_ = press(t, view, tcell.KeyLeft)
	if view.help || view.level() == nil {
		t.Fatalf("closing help should not also go back: help=%t level=%+v", view.help, view.level())
	}
	_ = press(t, view, '?')
	if err := press(t, view, 'q'); !errors.Is(err, errTerminalQuit) {
		t.Fatalf("q inside help = %v", err)
	}
}

func TestTUIIdeasAndTopicHistoryWithPagingAndKinds(t *testing.T) {
	view, source := newFakeTerminal(t)
	_ = press(t, view, '4')
	if view.view != "ideas" || !strings.Contains(strings.Join(view.details, "\n"), "Full text of work_item:tui-ideas") || !strings.Contains(view.context(), "Work items · filter all · 2 of 2") {
		t.Fatalf("ideas view = %q %v", view.context(), view.details)
	}
	_ = press(t, view, 'f')
	if view.ideaFilter != "open" || len(view.ideas.Items) != 1 {
		t.Fatalf("idea filter = %q %d", view.ideaFilter, len(view.ideas.Items))
	}
	_ = press(t, view, tcell.KeyRight)
	if level := view.level(); level == nil || level.kind != "record" || level.ref != "work_item:tui-ideas" {
		t.Fatalf("idea open = %+v", level)
	}
	_ = press(t, view, '5')
	if view.level() != nil || !strings.Contains(strings.Join(view.details, "\n"), "Aliases: cards") {
		t.Fatalf("topics view = %v", view.details)
	}
	_ = press(t, view, tcell.KeyRight)
	level := view.level()
	if level == nil || level.kind != "history" || level.topic != "reference-cards" || len(level.hits) != 50 || level.candidates != 60 {
		t.Fatalf("topic history = %+v", level)
	}
	if view.context() != "History › topic reference-cards · 1–50 of 60" || !strings.Contains(strings.Join(view.details, "\n"), "Via: explicit_link") {
		t.Fatalf("history context = %q details %v", view.context(), view.details)
	}
	_ = press(t, view, 'n')
	if level.offset != 50 || len(level.hits) != 10 || level.hits[0].Title != "Note 50" {
		t.Fatalf("next page = %d %d", level.offset, len(level.hits))
	}
	_ = press(t, view, 'n')
	if level.offset != 50 {
		t.Fatalf("paged past the end: %d", level.offset)
	}
	_ = press(t, view, 'k', 'k')
	if level.kindFilter != "decision" || level.offset != 0 || level.candidates != 20 || !strings.Contains(view.context(), "kind decision") {
		t.Fatalf("kind filter = %q %d %d %q", level.kindFilter, level.offset, level.candidates, view.context())
	}
	if got := source.searches[len(source.searches)-1]; got.Topic != "reference-cards" || got.Kinds[0] != "decision" {
		t.Fatalf("search options = %+v", got)
	}
	_ = press(t, view, tcell.KeyRight)
	if record := view.level(); record.kind != "record" || record.ref != "narrative:n00" {
		t.Fatalf("claim opens its narrative: %+v", record)
	}
}

func TestTUISearchPromptAndReferenceOpening(t *testing.T) {
	view, source := newFakeTerminal(t)
	screen := simulationScreen(t, 100, 20)
	_ = press(t, view, '/', 'c', 'a', 'r', 'x', tcell.KeyBackspace2, 'd')
	view.draw(screen)
	if !view.inputActive || view.input != "card" || !strings.Contains(strings.Join(screenText(screen), "\n"), "Search history: card") {
		t.Fatalf("prompt = %q active=%t", view.input, view.inputActive)
	}
	if err := press(t, view, 'q'); err != nil || view.input != "cardq" {
		t.Fatalf("q must type into the prompt: %v %q", err, view.input)
	}
	_ = press(t, view, tcell.KeyBackspace2, tcell.KeyEnter)
	level := view.level()
	if level == nil || level.kind != "history" || level.query != "card" || level.topic != "" || view.inputActive {
		t.Fatalf("search level = %+v", level)
	}
	_ = press(t, view, '/', 'x', tcell.KeyEscape)
	if view.inputActive || len(view.stack) != 1 {
		t.Fatalf("escape did not cancel prompt: %+v", view.stack)
	}
	// Searching inside a topic keeps the topic scope.
	_ = press(t, view, '5', tcell.KeyRight, '/', 'x', tcell.KeyEnter)
	if got := source.searches[len(source.searches)-1]; got.Topic != "reference-cards" || got.Query != "x" {
		t.Fatalf("topic search = %+v", got)
	}

	_ = press(t, view, '3', tcell.KeyRight)
	links := view.level()
	if links == nil || links.kind != "links" || len(links.links) != 2 || links.links[0].target != "roadmap_node:node-a@1" {
		t.Fatalf("decision links = %+v", links)
	}
	_ = press(t, view, tcell.KeyRight)
	if plan := view.level(); plan.kind != "plan" || plan.plan.ID != "plan-a" {
		t.Fatalf("roadmap link opens plan: %+v", plan)
	}
	_ = press(t, view, tcell.KeyLeft, tcell.KeyDown, tcell.KeyRight)
	if record := view.level(); record.kind != "checkpoint" || record.ref != "checkpoint:c1" {
		t.Fatalf("checkpoint link opens record: %+v", record)
	}

	source.timeline.Entries[0].SubjectRef = "node:node-c@2"
	_ = press(t, view, '2', tcell.KeyRight)
	if level := view.level(); level == nil || level.kind != "history" || level.topic != "node-c" {
		t.Fatalf("roadmap revision without plans opens node history: %+v", level)
	}
	source.timeline.Entries[0].SubjectRef, source.timeline.Entries[0].SourceRef = "task:t1", "task-transition:x"
	_ = press(t, view, '2', tcell.KeyRight)
	if view.level() == nil || view.level().kind != "checkpoints" || view.level().ref != "t1" {
		t.Fatalf("unsupported reference = %+v %q", view.level(), view.status)
	}
}

func TestTUIOpensOnTheTrackBeingWorkedOn(t *testing.T) {
	view, source := newFakeTerminal(t)
	checkpointAt := time.Date(2026, 9, 27, 9, 44, 0, 0, time.UTC)
	view.now = func() time.Time { return checkpointAt.Add(12 * time.Minute) }
	source.work.Items[2].SelectedTaskID = "task-c"
	source.current = &checkpoint.Checkpoint{ID: "cp", TaskID: "task-c", Status: "in_progress", NextAction: "Start slice 2", CreatedAt: checkpointAt}
	if err := view.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if view.workIndex != 2 || view.currentTrack != 2 {
		t.Fatalf("opened on row %d, current %d", view.workIndex, view.currentTrack)
	}
	if context := view.context(); !strings.HasPrefix(context, "◉ Now: node-c · in_progress · checkpoint ") || !strings.Contains(context, "(12m ago) · Tracks · filter current") {
		t.Fatalf("context = %q", context)
	}
	if row := view.rows(120)[2]; !strings.HasPrefix(row, "◉") || !strings.Contains(row, "working now") {
		t.Fatalf("current row = %q", row)
	}
	if details := strings.Join(view.details, "\n"); !strings.Contains(details, "Working now: checkpoint") || !strings.Contains(details, "Next: Start slice 2") {
		t.Fatalf("details:\n%s", details)
	}

	// With the selection elsewhere the current row stays bold and the header is emphasized.
	_ = press(t, view, tcell.KeyHome)
	screen := simulationScreen(t, 120, 20)
	view.draw(screen)
	cells, width, _ := screen.GetContents()
	if _, _, attrs := cells[1*width].Style.Decompose(); attrs&tcell.AttrBold == 0 || attrs&tcell.AttrDim != 0 {
		t.Fatalf("header is not emphasized: %v", attrs)
	}
	if _, _, attrs := cells[(2+2)*width].Style.Decompose(); attrs&tcell.AttrBold == 0 || attrs&tcell.AttrReverse != 0 {
		t.Fatalf("current row is not bold: %v", attrs)
	}
	if _, _, attrs := cells[(2+1)*width].Style.Decompose(); attrs&tcell.AttrBold != 0 {
		t.Fatalf("other rows are bold: %v", attrs)
	}
}

func TestTUISaysLastWorkedWhenNothingIsInProgress(t *testing.T) {
	view, source := newFakeTerminal(t)
	checkpointAt := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	view.now = func() time.Time { return checkpointAt.Add(50 * time.Hour) }
	source.work.Items[4].SelectedTaskID = "task-e"
	source.current = &checkpoint.Checkpoint{TaskID: "task-e", Status: "completed", CreatedAt: checkpointAt}
	if err := view.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if context := view.context(); !strings.HasPrefix(context, "Last worked: node-e · completed") || !strings.Contains(context, "(2d ago)") {
		t.Fatalf("context = %q", context)
	}
	if row := view.rows(120)[4]; strings.HasPrefix(row, "◉") || !strings.Contains(row, "last worked") {
		t.Fatalf("row = %q", row)
	}
}

func TestTUINamesACurrentTaskOutsideTheFilter(t *testing.T) {
	view, source := newFakeTerminal(t)
	source.current = &checkpoint.Checkpoint{TaskID: "unlisted", Status: "in_progress", CreatedAt: time.Now()}
	if err := view.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if view.workIndex != 0 || view.currentTrack != -1 || !strings.HasPrefix(view.context(), "◉ Now: task unlisted (no roadmap node; bind with llewm roadmap link --kind task) · in_progress · checkpoint ") {
		t.Fatalf("index %d current %d context %q", view.workIndex, view.currentTrack, view.context())
	}
}

func TestTUIAgo(t *testing.T) {
	for elapsed, want := range map[time.Duration]string{30 * time.Second: "just now", 5 * time.Minute: "5m ago", 3 * time.Hour: "3h ago", 72 * time.Hour: "3d ago"} {
		if got := ago(elapsed); got != want {
			t.Fatalf("ago(%s) = %s, want %s", elapsed, got, want)
		}
	}
}

func (f *fakeTerminalSource) Project(context.Context) (projectView, error) { return f.project, nil }
func (f *fakeTerminalSource) Checkpoints(_ context.Context, taskID, cursor string) (checkpoint.ListPage, error) {
	f.checkpointRequests = append(f.checkpointRequests, checkpoint.ListRequest{TaskID: taskID, Cursor: cursor})
	return f.checkpointPages[taskID+":"+cursor], nil
}
func (f *fakeTerminalSource) Checkpoint(_ context.Context, id string) (*checkpoint.Checkpoint, error) {
	return f.checkpointRecords[id], nil
}
func (f *fakeTerminalSource) Health(context.Context) (readiness.Report, error) {
	if f.failure != "" {
		return readiness.Report{}, errors.New(f.failure)
	}
	return f.health, nil
}
func (f *fakeTerminalSource) Releases(context.Context) (release.View, error) { return f.releases, nil }
func (f *fakeTerminalSource) Report(context.Context, string) (*workitem.BugReport, error) {
	if f.report == nil {
		return nil, errors.New("not a bug")
	}
	return f.report, nil
}
func (f *fakeTerminalSource) Delivery(context.Context) (*deliverystatus.Snapshot, error) {
	return f.delivery, nil
}

func TestTUIConsoleCheckpointChainEvidenceAndBack(t *testing.T) {
	v, f := newFakeTerminal(t)
	f.work.Items[0].SelectedTaskID = "task-one"
	f.checkpointPages = map[string]checkpoint.ListPage{
		"task-one:":     {Checkpoints: []checkpoint.Summary{{ID: "c2", TaskID: "task-one", Task: "Second", PredecessorCheckpointID: "c1"}}, NextCursor: "next"},
		"task-one:next": {Checkpoints: []checkpoint.Summary{{ID: "c1", TaskID: "task-one", Task: "First"}}},
	}
	f.checkpointRecords["c2"] = &checkpoint.Checkpoint{ID: "c2", TaskID: "task-one", PredecessorCheckpointID: "c1", EvidenceRefs: []string{"commit:abc"}}
	if err := press(t, v, 'c'); err != nil {
		t.Fatal(err)
	}
	if v.level().kind != "checkpoints" || v.level().ref != "task-one" {
		t.Fatalf("task chain: %+v", v.level())
	}
	if err := press(t, v, 'n'); err != nil {
		t.Fatal(err)
	}
	if v.level().checkpoints.Checkpoints[0].ID != "c1" {
		t.Fatalf("next page: %+v", v.level())
	}
	if err := press(t, v, 'p', tcell.KeyRight); err != nil {
		t.Fatal(err)
	}
	if v.level().kind != "checkpoint" || v.level().checkpoint.ID != "c2" {
		t.Fatalf("handoff: %+v", v.level())
	}
	if err := press(t, v, tcell.KeyRight); err != nil {
		t.Fatal(err)
	}
	if v.level().checkpoint.ID != "c1" {
		t.Fatalf("predecessor: %+v", v.level())
	}
	if err := press(t, v, tcell.KeyLeft, tcell.KeyDown, tcell.KeyDown, tcell.KeyRight); err != nil {
		t.Fatal(err)
	}
	if v.level().linked != "commit:abc" || f.searches[len(f.searches)-1].Linked != "commit:abc" {
		t.Fatalf("commit anchor: %+v", v.level())
	}
	if err := press(t, v, '/', 'x', tcell.KeyEnter); err != nil {
		t.Fatal(err)
	}
	if got := f.searches[len(f.searches)-1]; got.Linked != "commit:abc" || got.Query != "x" {
		t.Fatalf("search lost anchor: %+v", got)
	}
	if err := press(t, v, tcell.KeyLeft, tcell.KeyLeft, tcell.KeyLeft, tcell.KeyLeft); err != nil {
		t.Fatal(err)
	}
	if v.level() != nil || v.workIndex != 0 {
		t.Fatalf("back navigation: %+v", v.stack)
	}
}

func TestTUIConsoleNewViewsPreviewReceiptAndErrors(t *testing.T) {
	v, f := newFakeTerminal(t)
	f.project = projectView{ProjectID: "p", Name: "Fixture project", Root: "/fixture"}
	f.health = readiness.Report{Stage: "source_ready", RecommendedNextAction: "Install current build", Runtime: readiness.Runtime{Reason: "MCP process not observed"}}
	f.failure = "fixture health failure"
	if err := press(t, v, '8'); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(v.details, "\n"), f.failure) {
		t.Fatalf("health failure missing: %v", v.details)
	}
	if err := press(t, v, '6'); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(v.details, "\n"), f.project.Name) {
		t.Fatalf("failure hid other view: %v", v.details)
	}
	f.failure = ""
	if err := press(t, v, '8', 'r'); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(v.details, "\n"), "MCP process not observed") {
		t.Fatalf("health retry: %v", v.details)
	}
	f.releases = release.View{Releases: []release.Record{{ID: "pilot", Version: "0.1", Title: "Pilot", Entries: []release.Entry{{NodeID: "node-a", NodeRevision: 1, NarrativeIDs: []string{"n1"}}}}}}
	if err := press(t, v, '9', tcell.KeyRight, tcell.KeyDown, tcell.KeyRight); err != nil {
		t.Fatal(err)
	}
	if v.level().ref != "narrative:n1" {
		t.Fatalf("release evidence: %+v", v.level())
	}
	if err := press(t, v, '4', 'b'); err == nil {
		t.Fatal("non-bug preview succeeded")
	}
	f.ideas[0].Kind = "bug"
	f.report = &workitem.BugReport{Title: "Bug preview", ReportID: "sha256:fixture", Observed: "Observed fact", Disclaimer: "Draft; no submission"}
	if err := press(t, v, 'b'); err != nil {
		t.Fatal(err)
	}
	if v.level().kind != "report" || !strings.Contains(strings.Join(v.details, "\n"), "Observed fact") {
		t.Fatalf("preview: %v", v.details)
	}
	if err := press(t, v, '0'); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(v.details, "\n"), "No delivery snapshot") {
		t.Fatalf("missing snapshot invented status: %v", v.details)
	}
	now := time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC)
	v.now = func() time.Time { return now }
	f.delivery = &deliverystatus.Snapshot{SchemaVersion: 1, ObservedAt: now.Add(-time.Hour), Authority: "machine_observed", Reports: []deliverystatus.Summary{{ReportID: "sha256:fixture", SourceRef: "work_item:bug", Status: "delivered", Receipt: &deliverystatus.Receipt{Issue: 1, URL: "https://forge.invalid/issues/1", Outcome: "created"}}}}
	if err := press(t, v, 'r', tcell.KeyRight); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(v.details, "\n"), "Receipt: issue 1") || !strings.Contains(strings.Join(v.details, "\n"), "1h ago") {
		t.Fatalf("receipt provenance: %v", v.details)
	}
}

func TestTUIConsoleTabsRemainVisibleAtMinimumWidth(t *testing.T) {
	v, _ := newFakeTerminal(t)
	screen := simulationScreen(t, 60, 12)
	for i, name := range terminalTopViews {
		key := rune('1' + i)
		if i == 9 {
			key = '0'
		}
		if err := press(t, v, key); err != nil {
			t.Fatal(err)
		}
		v.draw(screen)
		text := screenText(screen)
		if !strings.Contains(text[0], "["+fmt.Sprintf("%d", (i+1)%10)+" "+terminalViewTitles[name]+"]") {
			t.Fatalf("selected tab hidden at 60 columns: %s", text[0])
		}
		if !strings.Contains(text[11], "Tab view") {
			t.Fatalf("detail overwrote footer: %v", text)
		}
	}
}

func TestTUIConsoleFailedDrillAndInitialSectionStayRecoverable(t *testing.T) {
	v, f := newFakeTerminal(t)
	f.showError = "fixture record unavailable"
	if err := v.openRef(context.Background(), "narrative:unavailable"); err == nil {
		t.Fatal("failed read succeeded")
	}
	if v.level() != nil || v.view != "tracks" {
		t.Fatalf("failed drill changed view: %+v", v.stack)
	}
	f.showError = ""
	f.workError = "fixture tracks unavailable"
	if err := v.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(v.details, "\n"), f.workError) {
		t.Fatalf("section failure hidden: %v", v.details)
	}
	if err := press(t, v, '3'); err != nil || !strings.Contains(strings.Join(v.details, "\n"), "Choose local state") {
		t.Fatalf("failure blocked other view: %v %v", v.details, err)
	}
	f.workError = ""
	if err := press(t, v, '1', 'r'); err != nil {
		t.Fatal(err)
	}
	if v.console.errors["tracks"] != "" {
		t.Fatalf("retry did not clear error: %v", v.console.errors)
	}
	if err := v.openRef(context.Background(), "track_plan:plan-a@1"); err != nil {
		t.Fatal(err)
	}
	if v.level().plan.Revision != 1 || v.level().latest != 2 {
		t.Fatalf("exact reference lost revision: %+v", v.level())
	}
}
