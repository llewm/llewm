package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/timeline"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/workstate"
)

// terminalSource is the bounded, read-only service surface used by the terminal view.
// It never creates a project, changes selection, or interprets a link as authority.
type terminalSource interface {
	Project(context.Context) (projectView, error)
	Checkpoints(context.Context, string, string) (checkpoint.ListPage, error)
	Checkpoint(context.Context, string) (*checkpoint.Checkpoint, error)
	Health(context.Context) (readiness.Report, error)
	Releases(context.Context) (release.View, error)
	Report(context.Context, string) (*workitem.BugReport, error)
	Work(ctx context.Context, view string) (workstate.List, error)
	Timeline(ctx context.Context, kind, cursor string) (timeline.Page, error)
	Decisions(ctx context.Context, offset int) (decision.ListPage, error)
	Decision(ctx context.Context, id string) (decision.Record, error)
	Backlinks(ctx context.Context, kind, id, revision string) (decision.BacklinkPage, error)
	Plans(ctx context.Context, nodeID, taskID string) (trackplan.PlanListPage, error)
	Plan(ctx context.Context, id string, revision int) (*trackplan.Plan, error)
	Ideas(ctx context.Context, status string) (workitem.ListPage, error)
	Topics(ctx context.Context) ([]topic.Summary, error)
	// Current returns the project's latest checkpoint, the one bootstrap resumes.
	Current(ctx context.Context) (*checkpoint.Checkpoint, error)
	HistorySearch(ctx context.Context, options history.SearchOptions) (history.SearchPage, error)
	HistoryShow(ctx context.Context, refs []string) (history.ShowPage, error)
}

const terminalPageSize = 20

type appTerminalSource struct {
	app      *app.App
	checkout project.Checkout
}

func (s appTerminalSource) Current(ctx context.Context) (*checkpoint.Checkpoint, error) {
	return s.app.Checkpoints.Latest(ctx, s.app.ProjectID)
}

func (s appTerminalSource) Work(ctx context.Context, view string) (workstate.List, error) {
	return s.app.Work.List(ctx, s.app.ProjectID, view, 100)
}

func (s appTerminalSource) Timeline(ctx context.Context, kind, cursor string) (timeline.Page, error) {
	return s.app.Timeline.List(ctx, s.app.ProjectID, timeline.Options{Kind: kind, Limit: terminalPageSize, Cursor: cursor})
}

func (s appTerminalSource) Decisions(ctx context.Context, offset int) (decision.ListPage, error) {
	return s.app.Decisions.List(ctx, s.app.ProjectID, decision.ListOptions{Limit: terminalPageSize, Offset: offset})
}

func (s appTerminalSource) Decision(ctx context.Context, id string) (decision.Record, error) {
	return s.app.Decisions.Show(ctx, s.app.ProjectID, id, 32<<10)
}

func (s appTerminalSource) Backlinks(ctx context.Context, kind, id, revision string) (decision.BacklinkPage, error) {
	return s.app.Decisions.Backlinks(ctx, s.app.ProjectID, kind, id, revision, terminalPageSize, 0)
}

func (s appTerminalSource) Plans(ctx context.Context, nodeID, taskID string) (trackplan.PlanListPage, error) {
	return s.app.TrackPlans.ListPlansPage(ctx, s.app.ProjectID, trackplan.PlanListOptions{RoadmapNodeID: nodeID, TaskID: taskID, Limit: 100})
}

func (s appTerminalSource) Plan(ctx context.Context, id string, revision int) (*trackplan.Plan, error) {
	return s.app.TrackPlans.Plan(ctx, s.app.ProjectID, id, revision)
}

var errTerminalQuit = errors.New("quit")

var terminalTopViews = []string{"tracks", "timeline", "decisions", "ideas", "history", "project", "checkpoints", "health", "releases"}
var terminalViewTitles = map[string]string{"tracks": "Tracks", "timeline": "Timeline", "decisions": "Decisions", "ideas": "Work items", "history": "History", "project": "Project", "checkpoints": "Checkpoints", "health": "Health", "releases": "Releases"}
var terminalFilters = []string{"open", "current", "active", "next", "blocked", "stale", "all", "everything", "actions"}
var terminalKinds = []string{"", "checkpoint", "roadmap_revision", "work_item", "task_transition", "activity_event"}

// terminalLevel is one drilled-in screen above a top-level view.
type terminalLevel struct {
	// kind is "plans" (a track's plans), "plan" (one plan revision's items),
	// "history" (search hits), "record" (one saved record), or "links" (a decision's links).
	checkpoint  *checkpoint.Checkpoint
	checkpoints checkpoint.ListPage
	cursors     []string
	release     *release.Record
	body        []string
	kind        string
	track       string
	plans       []trackplan.PlanSummary
	plan        *trackplan.Plan
	latest      int
	items       []planRow
	index       int

	linked                        string
	topic, query, kindFilter, ref string
	offset, candidates            int
	hits                          []history.Hit
	links                         []terminalLink
}

type planRow struct {
	item  trackplan.Item
	depth int
}

type planProgress struct{ done, total int }

type terminalView struct {
	consoleIndex int
	console      terminalConsoleState
	source       terminalSource
	work         workstate.List
	// current is the latest checkpoint and currentTrack the Tracks row it belongs
	// to, or -1 when that task is not in the listed tracks.
	current      *checkpoint.Checkpoint
	currentTrack int
	// currentPlacement says where the current task lives when the filter
	// hides it: roadmap:NODE, or "unbound" for a task with no roadmap node.
	currentPlacement string
	now              func() time.Time
	timeline         timeline.Page
	decisions        decision.ListPage
	progress         map[string]planProgress
	stack            []*terminalLevel
	ideas            workitem.ListPage
	topics           []topic.Summary

	ideaFilter            string
	ideaIndex, topicIndex int
	input                 string
	inputActive           bool

	view, filter, kind                   string
	workIndex, eventIndex, decisionIndex int
	timelineCursors                      []string
	decisionOffset                       int

	details      []string
	detailScroll int
	detailRows   int
	expanded     bool
	help         bool
	helpScroll   int
	status       string
}

func tuiCommand() *cobra.Command {
	command := &cobra.Command{Use: "tui", Short: "Browse the complete read-only Continuity console", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
			return errors.New("interactive terminal unavailable; use llewm work list, llewm history search, or llewm history search --linked")
		}
		checkout, err := currentCheckout(cmd)
		if err != nil {
			return err
		}
		return withReadOnlyApp(cmd, func(application *app.App) error {
			screen, err := tcell.NewScreen()
			if err != nil {
				return fmt.Errorf("terminal unsupported: %w; use llewm work list or llewm history search", err)
			}
			if err := screen.Init(); err != nil {
				return fmt.Errorf("terminal initialization failed: %w; use llewm work list or llewm history search", err)
			}
			defer screen.Fini()
			state := newTerminalView(appTerminalSource{app: application, checkout: checkout})
			if err := state.load(cmd.Context()); err != nil {
				return err
			}
			for {
				state.draw(screen)
				event := screen.PollEvent()
				if event == nil {
					return nil
				}
				switch e := event.(type) {
				case *tcell.EventResize:
					screen.Sync()
				case *tcell.EventKey:
					err := state.key(cmd.Context(), e)
					if errors.Is(err, errTerminalQuit) {
						return nil
					}
					if err != nil {
						state.status = err.Error()
					}
				}
			}
		})
	}}
	return command
}

func newTerminalView(source terminalSource) *terminalView {
	return &terminalView{source: source, view: "tracks", filter: "open", currentTrack: -1, now: time.Now}
}

func (v *terminalView) load(ctx context.Context) error {
	v.console.errors = map[string]string{}
	v.console.loaded = map[string]bool{}
	// Failure in one saved-data family must not hide the rest of the console.
	for _, view := range terminalTopViews[:5] {
		var err error
		switch view {
		case "tracks":
			v.work, err = v.source.Work(ctx, v.filter)
			if err == nil {
				v.current, err = v.source.Current(ctx)
			}
			if err == nil {
				err = v.loadProgress(ctx)
			}
			if err == nil {
				err = v.placeCurrent(ctx)
			}
		case "timeline":
			v.timeline, err = v.source.Timeline(ctx, v.kind, "")
		case "decisions":
			v.decisions, err = v.source.Decisions(ctx, 0)
		case "ideas":
			v.ideas, err = v.source.Ideas(ctx, v.ideaFilter)
		case "history":
			v.topics, err = v.source.Topics(ctx)
		}
		if err != nil {
			v.console.errors[view] = err.Error()
		}
	}
	v.workIndex, v.eventIndex, v.decisionIndex, v.decisionOffset, v.ideaIndex, v.topicIndex = 0, 0, 0, 0, 0, 0
	if v.currentTrack >= 0 {
		v.workIndex = v.currentTrack
	}
	v.timelineCursors, v.stack = nil, nil
	return v.detail(ctx)
}

// loadProgress summarizes one plan per track as completed/total non-superseded items,
// preferring an accepted or active plan over other statuses.
func (v *terminalView) loadProgress(ctx context.Context) error {
	v.progress = map[string]planProgress{}
	page, err := v.source.Plans(ctx, "", "")
	if err != nil {
		return err
	}
	chosen := map[string]trackplan.PlanSummary{}
	for _, summary := range page.Plans {
		current, seen := chosen[summary.RoadmapNodeID]
		if !seen || !livePlan(current.Status) && livePlan(summary.Status) {
			chosen[summary.RoadmapNodeID] = summary
		}
	}
	for _, summary := range chosen {
		plan, err := v.source.Plan(ctx, summary.ID, summary.Revision)
		if err != nil {
			return err
		}
		v.progress[summary.RoadmapNodeID] = countProgress(plan.Items)
	}
	return nil
}

func livePlan(status string) bool { return status == "accepted" || status == "active" }

// currentTrackIndex finds the track whose tasks include the latest checkpoint's task.
// placeCurrent finds the current task's row under the filter, and when the
// filter hides it, where it lives instead.
func (v *terminalView) placeCurrent(ctx context.Context) error {
	v.currentTrack, v.currentPlacement = currentTrackIndex(v.work.Items, v.current), ""
	if v.currentTrack >= 0 || v.current == nil || v.current.TaskID == "" {
		return nil
	}
	everything, err := v.source.Work(ctx, "everything")
	if err != nil {
		return err
	}
	if i := currentTrackIndex(everything.Items, v.current); i >= 0 && everything.Items[i].Node != nil {
		v.currentPlacement = "roadmap:" + everything.Items[i].Node.ID
	} else {
		v.currentPlacement = "unbound"
	}
	return nil
}

func currentTrackIndex(items []roadmap.TrackInventoryItem, current *checkpoint.Checkpoint) int {
	if current == nil || current.TaskID == "" {
		return -1
	}
	for i, item := range items {
		if item.SelectedTaskID == current.TaskID {
			return i
		}
		for _, task := range item.Tasks {
			if task.TaskID == current.TaskID {
				return i
			}
		}
	}
	return -1
}

// workingGlyph marks the track being worked on; it is distinct from the ▶ status glyph.
const workingGlyph = "◉"

// working reports whether the latest checkpoint says work is underway right now.
func (v *terminalView) working() bool {
	return v.current != nil && v.current.Status == "in_progress"
}

// currentLine summarizes the latest checkpoint for the Tracks header.
func (v *terminalView) currentLine() string {
	if v.current == nil {
		return ""
	}
	label := "Last worked"
	if v.working() {
		label = workingGlyph + " Now"
	}
	name := "task " + valueOrUnknown(v.current.TaskID)
	if v.currentTrack >= 0 {
		name = trackID(v.work.Items[v.currentTrack].Node, v.work.Items[v.currentTrack].SelectedTaskID)
	} else if v.currentPlacement == "unbound" {
		name += " (no roadmap node; bind with llewm roadmap link --kind task)"
	} else if v.currentPlacement != "" {
		name += " (on " + v.currentPlacement + ", not in this filter)"
	} else if v.current.TaskID != "" {
		name += " (not in this filter)"
	}
	return fmt.Sprintf("%s: %s · %s · checkpoint %s (%s)", label, name, v.current.Status, v.current.CreatedAt.Local().Format("01-02 15:04"), ago(v.now().Sub(v.current.CreatedAt)))
}

// ago renders an elapsed duration compactly.
func ago(elapsed time.Duration) string {
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
}

func countProgress(items []trackplan.Item) planProgress {
	var p planProgress
	for _, item := range items {
		if item.Status == "superseded" {
			continue
		}
		p.total++
		if item.Status == "completed" {
			p.done++
		}
	}
	return p
}

func (v *terminalView) level() *terminalLevel {
	if len(v.stack) == 0 {
		return nil
	}
	return v.stack[len(v.stack)-1]
}

func (v *terminalView) key(ctx context.Context, e *tcell.EventKey) error {
	v.status = ""
	r := rune(0)
	if e.Key() == tcell.KeyRune {
		r = e.Rune()
	}
	if v.help && e.Key() != tcell.KeyCtrlC && r != 'q' {
		switch e.Key() {
		case tcell.KeyPgDn:
			v.helpScroll += max(1, v.detailRows)
			return nil
		case tcell.KeyPgUp:
			v.helpScroll = max(0, v.helpScroll-max(1, v.detailRows))
			return nil
		case tcell.KeyDown:
			v.helpScroll++
			return nil
		case tcell.KeyUp:
			v.helpScroll = max(0, v.helpScroll-1)
			return nil
		}
		v.help = false
		return nil
	}
	if v.inputActive && e.Key() == tcell.KeyCtrlC {
		return errTerminalQuit
	}
	if handled, err := v.historyKey(ctx, e, r); handled {
		return err
	}
	switch {
	case e.Key() == tcell.KeyCtrlC || r == 'q':
		return errTerminalQuit
	case r == '?':
		v.help, v.helpScroll = true, 0
		return nil
	case r == '[' || r == ']':
		delta := -1
		if r == ']' {
			delta = 1
		}
		return v.stepRevision(ctx, delta)
	case e.Key() == tcell.KeyTab:
		return v.switchView(ctx, 1)
	case e.Key() == tcell.KeyBacktab:
		return v.switchView(ctx, -1)
	case r >= '1' && r <= '9':
		v.changeView(terminalTopViews[r-'1'])
	case e.Key() == tcell.KeyEscape && v.expanded:
		v.expanded = false
		return nil
	case e.Key() == tcell.KeyEscape || e.Key() == tcell.KeyLeft || e.Key() == tcell.KeyBackspace || e.Key() == tcell.KeyBackspace2 || r == 'h':
		if len(v.stack) == 0 {
			return nil
		}
		v.stack = v.stack[:len(v.stack)-1]
	case e.Key() == tcell.KeyRight || r == 'l' || r == ' ':
		return v.drill(ctx)
	case r == 'c' && v.view == "tracks":
		return v.openSelectedCheckpoints(ctx)
	case r == 'b' && v.view == "ideas":
		return v.openSelectedReport(ctx)
	case r == 'p' && v.view == "tracks" && (v.level() == nil || v.level().kind == "plan" || v.level().kind == "plans"):
		return v.toggleSelectedTrackPlan(ctx)
	case e.Key() == tcell.KeyEnter || r == 'z':
		v.expanded = !v.expanded
		return nil
	case e.Key() == tcell.KeyPgDn:
		v.detailScroll += max(1, v.detailRows-1)
		return nil
	case e.Key() == tcell.KeyPgUp:
		v.detailScroll = max(0, v.detailScroll-max(1, v.detailRows-1))
		return nil
	case e.Key() == tcell.KeyUp:
		if v.expanded {
			v.detailScroll = max(0, v.detailScroll-1)
			return nil
		}
		v.move(-1)
	case e.Key() == tcell.KeyDown:
		if v.expanded {
			v.detailScroll++
			return nil
		}
		v.move(1)
	case e.Key() == tcell.KeyHome:
		v.move(-1 << 30)
	case e.Key() == tcell.KeyEnd:
		v.move(1 << 30)
	case len(v.stack) > 0 && v.level().kind != "checkpoints" && (r == 'f' || r == 'k' || r == 'n' || r == 'p'):
		return nil
	case r == 'f' && v.view == "tracks":
		filter := cycle(terminalFilters, v.filter)
		list, err := v.source.Work(ctx, filter)
		if err != nil {
			return err
		}
		v.work, v.workIndex, v.filter = list, 0, filter
		delete(v.console.errors, "tracks")
		if err := v.placeCurrent(ctx); err != nil {
			return err
		}
	case r == 'k' && v.view == "timeline":
		kind := cycle(terminalKinds, v.kind)
		page, err := v.source.Timeline(ctx, kind, "")
		if err != nil {
			return err
		}
		v.timeline, v.eventIndex, v.timelineCursors, v.kind = page, 0, nil, kind
		delete(v.console.errors, "timeline")
	case r == 'n':
		return v.next(ctx)
	case r == 'p':
		return v.previous(ctx)
	case r == 'r':
		return v.refresh(ctx)
	default:
		return nil
	}
	return v.detail(ctx)
}

func (v *terminalView) switchView(ctx context.Context, delta int) error {
	for i, name := range terminalTopViews {
		if name == v.view {
			v.changeView(terminalTopViews[(i+delta+len(terminalTopViews))%len(terminalTopViews)])
			break
		}
	}
	return v.detail(ctx)
}

// drill opens a plan from a track's plan list, the track's history (from a
// Tracks row, same as every other view's row), or the record a history hit,
// link, or timeline/decision/idea/topic row refers to. On a plan item it has
// nothing further to open, so it only points at the detail pane already
// showing that row's information.
func (v *terminalView) drill(ctx context.Context) error {
	if handled, err := v.consoleDrill(ctx); handled {
		return err
	}
	if handled, err := v.drillHistory(ctx, v.level()); handled {
		return err
	}
	if level := v.level(); level != nil {
		if level.kind == "plans" && level.index < len(level.plans) {
			return v.openPlan(ctx, level.track, level.plans[level.index])
		}
		v.status = "Plan item detail is shown below."
		return nil
	}
	return nil
}

// toggleSelectedTrackPlan opens the selected Tracks-row track's plan (bound
// to 'p'), independently of the generic drill key which always opens history
// instead. Pressing 'p' again while that plan is showing returns to the
// Tracks list.
func (v *terminalView) toggleSelectedTrackPlan(ctx context.Context) error {
	if v.view != "tracks" {
		return nil
	}
	if level := v.level(); level != nil {
		if level.kind == "plan" || level.kind == "plans" {
			v.stack = nil
			return v.detail(ctx)
		}
		v.status = "Plan detail is shown below."
		return nil
	}
	if v.workIndex >= len(v.work.Items) {
		return nil
	}
	item := v.work.Items[v.workIndex]
	nodeID, taskID := "", item.SelectedTaskID
	if item.Node != nil {
		nodeID, taskID = item.Node.ID, ""
	}
	return v.openTrackPlan(ctx, nodeID, taskID, trackID(item.Node, item.SelectedTaskID), false)
}

func (v *terminalView) openPlan(ctx context.Context, track string, summary trackplan.PlanSummary) error {
	plan, err := v.source.Plan(ctx, summary.ID, summary.Revision)
	if err != nil {
		return err
	}
	return v.pushLevel(ctx, &terminalLevel{kind: "plan", track: track, plan: plan, latest: summary.Revision, items: planRows(plan.Items)})
}

// stepRevision shows an adjacent revision of the open plan, bounded by 1 and the latest.
func (v *terminalView) stepRevision(ctx context.Context, delta int) error {
	level := v.level()
	if level == nil || level.kind != "plan" {
		v.status = "Revisions step inside an open plan."
		return nil
	}
	revision := level.plan.Revision + delta
	if revision < 1 || revision > level.latest {
		v.status = fmt.Sprintf("Plan revisions run from 1 to %d.", level.latest)
		return nil
	}
	plan, err := v.source.Plan(ctx, level.plan.ID, revision)
	if err != nil {
		return err
	}
	level.plan, level.items = plan, planRows(plan.Items)
	level.index = min(level.index, max(0, len(level.items)-1))
	return v.detail(ctx)
}

// planRows orders items as a depth-first tree by parent, then position.
// Unknown or self parents become roots, and items caught in a parent cycle are appended last.
func planRows(items []trackplan.Item) []planRow {
	known := map[string]bool{}
	for _, item := range items {
		known[item.ID] = true
	}
	children := map[string][]trackplan.Item{}
	for _, item := range items {
		parent := item.ParentID
		if !known[parent] || parent == item.ID {
			parent = ""
		}
		children[parent] = append(children[parent], item)
	}
	for _, list := range children {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Position != list[j].Position {
				return list[i].Position < list[j].Position
			}
			return list[i].ID < list[j].ID
		})
	}
	var rows []planRow
	visited := map[string]bool{}
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, item := range children[parent] {
			if visited[item.ID] {
				continue
			}
			visited[item.ID] = true
			rows = append(rows, planRow{item: item, depth: depth})
			walk(item.ID, depth+1)
		}
	}
	walk("", 0)
	for _, item := range items {
		if !visited[item.ID] {
			visited[item.ID] = true
			rows = append(rows, planRow{item: item})
		}
	}
	return rows
}

func cycle(values []string, current string) string {
	for i, value := range values {
		if value == current {
			return values[(i+1)%len(values)]
		}
	}
	return values[0]
}

func (v *terminalView) selection() (*int, int) {
	if index, count, ok := v.consoleSelection(); ok {
		return index, count
	}
	if level := v.level(); level != nil {
		switch level.kind {
		case "plans":
			return &level.index, len(level.plans)
		case "history":
			return &level.index, len(level.hits)
		case "record":
			return &level.index, 1
		case "links":
			return &level.index, len(level.links)
		}
		return &level.index, len(level.items)
	}
	switch v.view {
	case "ideas":
		return &v.ideaIndex, len(v.ideas.Items)
	case "history":
		return &v.topicIndex, len(v.topics)
	case "timeline":
		return &v.eventIndex, len(v.timeline.Entries)
	case "decisions":
		return &v.decisionIndex, len(v.decisions.Decisions)
	}
	return &v.workIndex, len(v.work.Items)
}

func (v *terminalView) move(delta int) {
	index, count := v.selection()
	if count == 0 {
		return
	}
	*index = min(max(*index+delta, 0), count-1)
}

func (v *terminalView) next(ctx context.Context) error {
	if handled, err := v.checkpointPage(ctx, 1); handled {
		return err
	}
	switch v.view {
	case "timeline":
		if v.timeline.NextCursor == "" {
			return nil
		}
		cursor := v.timeline.NextCursor
		page, err := v.source.Timeline(ctx, v.kind, cursor)
		if err != nil {
			return err
		}
		v.timelineCursors = append(v.timelineCursors, cursor)
		v.timeline, v.eventIndex = page, 0
	case "decisions":
		if !v.decisions.Truncated {
			return nil
		}
		offset := v.decisionOffset + terminalPageSize
		page, err := v.source.Decisions(ctx, offset)
		if err != nil {
			return err
		}
		v.decisions, v.decisionOffset, v.decisionIndex = page, offset, 0
	default:
		return nil
	}
	return v.detail(ctx)
}

func (v *terminalView) previous(ctx context.Context) error {
	if handled, err := v.checkpointPage(ctx, -1); handled {
		return err
	}
	switch v.view {
	case "timeline":
		if len(v.timelineCursors) == 0 {
			return nil
		}
		v.timelineCursors = v.timelineCursors[:len(v.timelineCursors)-1]
		cursor := ""
		if len(v.timelineCursors) > 0 {
			cursor = v.timelineCursors[len(v.timelineCursors)-1]
		}
		page, err := v.source.Timeline(ctx, v.kind, cursor)
		if err != nil {
			return err
		}
		v.timeline, v.eventIndex = page, 0
	case "decisions":
		if v.decisionOffset == 0 {
			return nil
		}
		offset := max(0, v.decisionOffset-terminalPageSize)
		page, err := v.source.Decisions(ctx, offset)
		if err != nil {
			return err
		}
		v.decisions, v.decisionOffset, v.decisionIndex = page, offset, 0
	default:
		return nil
	}
	return v.detail(ctx)
}

func (v *terminalView) detail(ctx context.Context) error {
	v.detailScroll = 0
	v.details = []string{"Nothing selected.", "Links are claims, not authority."}
	kind, id, revision := "", "", ""
	if handled, err := v.consoleDetail(ctx); handled {
		return err
	}
	if handled, err := v.historyDetail(ctx, v.level()); handled {
		return err
	}
	if level := v.level(); level != nil {
		kind, id, revision = v.levelDetail(level)
		if kind == "" {
			return nil
		}
		return v.appendBacklinks(ctx, kind, id, revision)
	}
	switch v.view {
	case "tracks":
		if v.workIndex >= len(v.work.Items) {
			break
		}
		item := v.work.Items[v.workIndex]
		v.details = nil
		if item.Node != nil {
			kind, id = "roadmap_node", item.Node.ID
			v.details = append(v.details, "Node: "+id, "Title: "+item.Node.Title, fmt.Sprintf("Status: %s  Priority: %d", item.Node.Status, item.Node.Priority))
		} else if item.SelectedTaskID != "" {
			kind, id = "task", item.SelectedTaskID
		}
		if v.workIndex == v.currentTrack && v.current != nil {
			state := "Last worked"
			if v.working() {
				state = "Working now"
			}
			v.details = append(v.details, fmt.Sprintf("%s: checkpoint %s (%s), %s", state, v.current.CreatedAt.Local().Format("01-02 15:04"), ago(v.now().Sub(v.current.CreatedAt)), v.current.Status), "Next: "+v.current.NextAction)
		} else if item.LatestCheckpoint != nil {
			cp := item.LatestCheckpoint
			v.details = append(v.details, fmt.Sprintf("Checkpoint: %s (%s), %s", cp.CreatedAt.Local().Format("01-02 15:04"), ago(v.now().Sub(cp.CreatedAt)), cp.Status), "Next: "+cp.NextAction)
		}
		v.details = append(v.details, "Binding: "+item.BindingState, "Task: "+valueOrUnknown(item.SelectedTaskID), fmt.Sprintf("Unresolved actions: %d  Pending work items: %d", item.UnresolvedActionCount, item.PendingWorkItems))
		if p, ok := v.progress[trackID(item.Node, "")]; ok && item.Node != nil {
			v.details = append(v.details, fmt.Sprintf("Plan: %d of %d items completed (p to open)", p.done, p.total))
		}
		if len(item.Anomalies) > 0 {
			v.details = append(v.details, "Findings: "+strings.Join(item.Anomalies, ", "))
		}
	case "timeline":
		if v.eventIndex >= len(v.timeline.Entries) {
			break
		}
		e := v.timeline.Entries[v.eventIndex]
		v.details = []string{"Kind: " + e.Kind, "Summary: " + e.Summary, "Source: " + e.SourceRef, "Subject: " + e.SubjectRef, "Recorded: " + e.RecordedAt, "Occurred: " + valueOrUnknown(e.OccurredAt), "Actor: " + valueOrUnknown(e.Actor), "Producer: " + valueOrUnknown(e.Producer), "Authority: " + e.Authority}
		if e.RoadmapNodeID != "" {
			kind, id = "roadmap_node", e.RoadmapNodeID
		} else if e.TaskID != "" {
			kind, id = "task", e.TaskID
		}
	case "decisions":
		if v.decisionIndex >= len(v.decisions.Decisions) {
			break
		}
		summary := v.decisions.Decisions[v.decisionIndex]
		record, err := v.source.Decision(ctx, summary.ID)
		if err != nil {
			return err
		}
		kind, id = "decision", summary.ID
		v.details = []string{"Decision: " + id, "Title: " + record.Title, "Disposition: " + record.Disposition, "Actor: " + record.Actor, "Authority: " + record.Authority, "Rationale: " + record.Rationale}
		for _, link := range record.Links {
			v.details = append(v.details, "Link: "+link.Kind+" "+link.TargetKind+":"+link.TargetID+" "+link.TargetRevision)
		}
	}
	if kind == "" {
		return nil
	}
	return v.appendBacklinks(ctx, kind, id, revision)
}

func (v *terminalView) levelDetail(level *terminalLevel) (kind, id, revision string) {
	if level.kind == "plans" {
		if level.index >= len(level.plans) {
			return "", "", ""
		}
		p := level.plans[level.index]
		v.details = []string{"Plan: " + p.ID + "@" + fmt.Sprint(p.Revision), "Title: " + p.Title, "Status: " + p.Status, "Roadmap: " + valueOrUnknown(p.RoadmapNodeID), "Task: " + valueOrUnknown(p.TaskID), fmt.Sprintf("Items: %d  Ready: %d", p.ItemCount, p.ReadyCount), "Recorded: " + p.RecordedAt.Format("2006-01-02T15:04:05Z07:00"), "→ opens this plan"}
		return "track_plan", p.ID, fmt.Sprint(p.Revision)
	}
	plan := level.plan
	header := fmt.Sprintf("Plan: %s@%d (latest %d) · %s · %s by %s", plan.ID, plan.Revision, level.latest, plan.Status, plan.Authority, plan.Creator)
	v.details = []string{header}
	if level.index < len(level.items) {
		item := level.items[level.index].item
		v.details = append(v.details, "Item: "+item.ID, "Title: "+item.Title, "Status: "+item.Status)
		if item.ParentID != "" {
			v.details = append(v.details, "Parent: "+item.ParentID)
		}
		if len(item.DependsOn) > 0 {
			v.details = append(v.details, "Depends on: "+strings.Join(item.DependsOn, ", "))
		}
		if item.Detail != "" {
			v.details = append(v.details, "Detail: "+item.Detail)
		}
		for _, ref := range item.EvidenceRefs {
			v.details = append(v.details, "Evidence: "+ref)
		}
		if len(item.WorkItemIDs) > 0 {
			v.details = append(v.details, "Work items: "+strings.Join(item.WorkItemIDs, ", "))
		}
	}
	v.details = append(v.details, "", "Plan summary: "+plan.Summary)
	return "track_plan", plan.ID, fmt.Sprint(plan.Revision)
}

func (v *terminalView) appendBacklinks(ctx context.Context, kind, id, revision string) error {
	page, err := v.source.Backlinks(ctx, kind, id, revision)
	if err != nil {
		return err
	}
	v.details = append(v.details, "", fmt.Sprintf("Backlinks: %d (%s)", page.Total, strings.Join(page.IncludedFamilies, ", ")))
	for _, link := range page.Backlinks {
		linkStatus := link.LinkStatus
		if linkStatus == "" {
			linkStatus = "n/a"
		}
		v.details = append(v.details, "  "+link.SourceKind+":"+link.SourceID+"@"+valueOrUnknown(link.SourceRevision)+" "+link.LinkKind+" ("+linkStatus+") ["+link.TargetResolution+"]")
	}
	if page.Truncated {
		v.details = append(v.details, "  More backlinks: llewm history search --linked TARGET")
	}
	v.details = append(v.details, "Links are claims, not authority.")
	return nil
}

// rows renders the current list as aligned single-line rows.
func (v *terminalView) rows(width int) []string {
	if rows, ok := v.consoleRows(width); ok {
		return rows
	}
	if level := v.level(); level != nil {
		if level.kind == "plans" || level.kind == "plan" {
			return levelRows(level, width)
		}
		return historyRows(level, width)
	}
	if rows, ok := v.topViewRows(width); ok {
		return rows
	}
	var rows listRows
	switch v.view {
	case "tracks":
		idWidth := 0
		for _, item := range v.work.Items {
			idWidth = max(idWidth, runewidth.StringWidth(trackID(item.Node, item.SelectedTaskID)))
		}
		rows.widths = []int{2, 11, 5, min(idWidth+2, width/3)}
		for i, item := range v.work.Items {
			status, priority, title := item.BindingState, "", ""
			if item.Node != nil {
				status, priority, title = item.Node.Status, fmt.Sprintf("P%d", item.Node.Priority), item.Node.Title
			}
			glyph := statusGlyph(status)
			var right []string
			if i == v.currentTrack {
				if v.working() {
					glyph = workingGlyph
					right = append(right, "working now")
				} else {
					right = append(right, "last worked")
				}
			}
			if p, ok := v.progress[trackID(item.Node, "")]; ok && item.Node != nil {
				right = append(right, fmt.Sprintf("plan %d/%d", p.done, p.total))
			}
			if len(item.Anomalies) > 0 {
				right = append(right, fmt.Sprintf("%d findings", len(item.Anomalies)))
			}
			rows.add(title, strings.Join(right, " · "), glyph, status, priority, trackID(item.Node, item.SelectedTaskID))
		}
	case "timeline":
		rows.widths = []int{12, 18}
		for _, e := range v.timeline.Entries {
			rows.add(e.Summary, "", shortTime(e.RecordedAt), e.Kind)
		}
	case "decisions":
		rows.widths = []int{2, 11, 12}
		for _, d := range v.decisions.Decisions {
			rows.add(d.Title, fmt.Sprintf("%d links", d.LinkCount), statusGlyph(d.Disposition), d.Disposition, shortTime(d.RecordedAt.Format("2006-01-02T15:04:05Z07:00")))
		}
	}
	return rows.layout(width)
}

func levelRows(level *terminalLevel, width int) []string {
	var rows listRows
	if level.kind == "plans" {
		rows.widths = []int{2, 11, 5}
		for _, p := range level.plans {
			rows.add(p.Title, fmt.Sprintf("%d items · %d ready", p.ItemCount, p.ReadyCount), statusGlyph(p.Status), p.Status, fmt.Sprintf("@%d", p.Revision))
		}
		return rows.layout(width)
	}
	rows.widths = []int{2, 11}
	status := map[string]string{}
	for _, row := range level.items {
		status[row.item.ID] = row.item.Status
	}
	for _, row := range level.items {
		// Only unfinished dependencies are listed; the detail pane shows all of them.
		var waiting []string
		for _, dependency := range row.item.DependsOn {
			if status[dependency] != "completed" {
				waiting = append(waiting, dependency)
			}
		}
		right := ""
		if len(waiting) > 0 {
			right = runewidth.Truncate("needs "+strings.Join(waiting, ","), max(10, width/4), "…")
		}
		rows.add(strings.Repeat("  ", row.depth)+row.item.Title, right, statusGlyph(row.item.Status), row.item.Status)
	}
	return rows.layout(width)
}

// listRows collects aligned rows: fixed-width leading columns, a flexible title,
// and a right-hand summary padded to the widest summary in the list.
type listRows struct {
	widths []int
	fixed  [][]string
	flex   []string
	right  []string
}

func (r *listRows) add(flex, right string, fixed ...string) {
	r.fixed, r.flex, r.right = append(r.fixed, fixed), append(r.flex, flex), append(r.right, right)
}

func (r listRows) layout(width int) []string {
	rightWidth := 0
	for _, right := range r.right {
		rightWidth = max(rightWidth, runewidth.StringWidth(right))
	}
	rows := make([]string, len(r.flex))
	for i := range rows {
		rows[i] = layoutRow(width, r.fixed[i], r.widths, r.flex[i], runewidth.FillLeft(r.right[i], rightWidth))
	}
	return rows
}

func trackID(node *roadmap.NodeRef, task string) string {
	if node != nil {
		return node.ID
	}
	return task
}

// layoutRow pads fixed columns, truncates the flexible column, and right-aligns a summary.
func layoutRow(width int, fixed []string, widths []int, flex, right string) string {
	var b strings.Builder
	used := 0
	for i, value := range fixed {
		w := min(widths[i], width-used)
		if w <= 0 {
			break
		}
		b.WriteString(runewidth.FillRight(runewidth.Truncate(sanitizeLine(value), w-1, "…"), w))
		used += w
	}
	rightWidth := runewidth.StringWidth(right)
	flexWidth := width - used - rightWidth - 1
	if flexWidth < 8 {
		right, rightWidth, flexWidth = "", 0, width-used
	}
	if flexWidth > 0 {
		b.WriteString(runewidth.FillRight(runewidth.Truncate(sanitizeLine(flex), flexWidth, "…"), flexWidth))
	}
	if strings.TrimSpace(right) != "" {
		b.WriteString(" " + right)
	}
	return b.String()
}

func statusGlyph(status string) string {
	switch status {
	case "completed", "done", "accepted_complete", "delivered":
		return "✓"
	case "active", "in_progress":
		return "▶"
	case "accepted", "ready":
		return "●"
	case "blocked", "rejected":
		return "✗"
	case "deferred", "superseded", "abandoned":
		return "·"
	}
	return "○"
}

// shortTime turns an RFC 3339 timestamp into a compact month-day hour:minute label.
func shortTime(value string) string {
	if len(value) >= 16 && value[4] == '-' && value[10] == 'T' {
		return value[5:10] + " " + value[11:16]
	}
	return value
}

func sanitizeLine(value string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, value)
}

// wrapLines word-wraps logical lines to width, indenting continuation lines.
func wrapLines(lines []string, width int) []string {
	const indent = "  "
	var out []string
	if width <= len(indent) {
		return out
	}
	for _, line := range lines {
		line = sanitizeLine(line)
		if runewidth.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		current := ""
		for _, word := range strings.Fields(line) {
			for {
				prefix := current
				if prefix != "" && prefix != indent {
					prefix += " "
				}
				if runewidth.StringWidth(prefix+word) <= width {
					current = prefix + word
					break
				}
				if current != "" && current != indent {
					out = append(out, current)
					current = indent
					continue
				}
				// A single word wider than the line is split at the available width.
				head := runewidth.Truncate(word, width-runewidth.StringWidth(current), "")
				if head == "" {
					head = string([]rune(word)[:1])
				}
				out = append(out, current+head)
				current, word = indent, strings.TrimPrefix(word, head)
			}
		}
		if strings.TrimSpace(current) != "" {
			out = append(out, current)
		}
	}
	return out
}

func (v *terminalView) hint() string {
	if hint, ok := v.consoleHint(); ok {
		return hint
	}
	common := "Tab view · / search · PgUp/PgDn detail · z expand · ? help · q quit"
	if level := v.level(); level != nil {
		switch level.kind {
		case "plans":
			if v.view == "tracks" {
				return "→ open · p or ← back · " + common
			}
			return "→ open · ← back · " + common
		case "links":
			return "→ open · ← back · " + common
		case "history":
			return "→ open · k kind · n/p page · ← back · " + common
		case "record":
			return "← back · " + common
		}
		if v.view == "tracks" {
			return "p or ← back · [ ] revision · " + common
		}
		return "← back · [ ] revision · " + common
	}
	switch v.view {
	case "tracks":
		return "→ history · p plan · c checkpoints · f filter · " + common
	case "timeline":
		return "k kind · n/p page · " + common
	case "decisions":
		return "→ links · n/p page · " + common
	case "ideas":
		return "→ open · b bug report · f filter · " + common
	case "history":
		return "→ topic history · " + common
	}
	return common
}

func (v *terminalView) context() string {
	if len(v.stack) == 0 && v.isConsoleView() {
		return terminalViewTitles[v.view] + " · " + v.consoleContext()
	}
	if len(v.stack) > 0 {
		parts := []string{terminalViewTitles[v.view]}
		var previous *terminalLevel
		for _, level := range v.stack {
			parts = append(parts, crumbs(level, previous)...)
			previous = level
		}
		return strings.Join(parts, " › ")
	}
	switch v.view {
	case "tracks":
		summary := fmt.Sprintf("Tracks · filter %s · %d of %d", v.filter, len(v.work.Items), v.work.InventoryTotal)
		if line := v.currentLine(); line != "" {
			return line + " · " + summary
		}
		return summary
	case "timeline":
		kind := v.kind
		if kind == "" {
			kind = "default"
		}
		more := ""
		if v.timeline.NextCursor != "" {
			more = " · more (n)"
		}
		return fmt.Sprintf("Timeline · kind %s · page %d%s · cursors are not frozen snapshots", kind, len(v.timelineCursors)+1, more)
	case "decisions":
		return fmt.Sprintf("Decisions · %d–%d of %d", min(v.decisionOffset+1, v.decisions.Total), v.decisionOffset+len(v.decisions.Decisions), v.decisions.Total)
	case "ideas":
		filter := v.ideaFilter
		if filter == "" {
			filter = "all"
		}
		return fmt.Sprintf("Work items · filter %s · %d of %d", filter, len(v.ideas.Items), v.ideas.Total)
	case "history":
		return fmt.Sprintf("History · %d curated topics · / searches all history", len(v.topics))
	}
	return ""
}

// filterLine stays separate from breadcrumbs and status messages, which can
// grow beyond the terminal width or be replaced by search/error feedback.
func (v *terminalView) filterLine() string {
	if level := v.level(); level != nil {
		if level.kind == "history" {
			return " FILTER: kind=" + filterValue(level.kindFilter, "all") + "  [k: change]"
		}
		return ""
	}
	switch v.view {
	case "tracks":
		return " FILTER: " + v.filter + "  [f: change]"
	case "ideas":
		return " FILTER: status=" + filterValue(v.ideaFilter, "all") + "  [f: change]"
	case "timeline":
		return " FILTER: kind=" + filterValue(v.kind, "default") + "  [k: change]"
	}
	return ""
}

func filterValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (v *terminalView) draw(s tcell.Screen) {
	s.Clear()
	width, height := s.Size()
	if width < 60 || height < 12 {
		put(s, 0, 0, width, "Terminal too small (need 60x12); q quits", tcell.StyleDefault)
		s.Show()
		return
	}
	bold := tcell.StyleDefault.Bold(true)
	put(s, 0, 0, width, v.tabLine(width), bold)
	onTracks := v.view == "tracks" && len(v.stack) == 0
	contextStyle := tcell.StyleDefault.Dim(true)
	if onTracks && v.working() {
		contextStyle = tcell.StyleDefault.Bold(true)
	}
	put(s, 0, 1, width, v.context(), contextStyle)

	body := height - 4
	rows := v.rows(width)
	// The list takes up to three fifths of the body and shrinks to leave detail room.
	listRows := min(max(len(rows), 3), body*3/5)
	if v.expanded {
		listRows = 0
	}
	v.detailRows = body - listRows - 1
	index, _ := v.selection()
	start := visibleStart(*index, listRows)
	for i := 0; i < listRows && start+i < len(rows); i++ {
		style := tcell.StyleDefault
		if onTracks && start+i == v.currentTrack {
			style = style.Bold(true)
		}
		if start+i == *index {
			style = style.Reverse(true)
		}
		put(s, 0, i+2, width, runewidth.FillRight(rows[start+i], width), style)
	}
	if listRows > 0 && len(rows) == 0 {
		put(s, 0, 2, width, "No records in this view.", tcell.StyleDefault.Dim(true))
	}

	details := wrapLines(v.details, width)
	v.detailScroll = min(v.detailScroll, max(0, len(details)-v.detailRows))
	separator := "── Detail "
	if len(details) > v.detailRows {
		separator += fmt.Sprintf("%d–%d of %d ", v.detailScroll+1, min(v.detailScroll+v.detailRows, len(details)), len(details))
	}
	sepY := 2 + listRows
	put(s, 0, sepY, width, separator+strings.Repeat("─", max(0, width-runewidth.StringWidth(separator))), tcell.StyleDefault.Dim(true))
	for i := 0; i < v.detailRows && v.detailScroll+i < len(details); i++ {
		put(s, 0, sepY+1+i, width, details[v.detailScroll+i], tcell.StyleDefault)
	}
	if filter := v.filterLine(); filter != "" {
		put(s, 0, height-2, width, runewidth.FillRight(filter, width), bold.Reverse(true))
	}
	status := v.status
	if status == "" {
		status = v.hint()
	}
	if v.inputActive {
		prompt := "Search history: "
		if level := v.level(); level != nil && level.kind == "history" && level.topic != "" {
			prompt = "Search topic " + level.topic + ": "
		}
		status = prompt + v.input + "▏  (Enter search · Esc cancel)"
	}
	put(s, 0, height-1, width, status, tcell.StyleDefault.Dim(v.status == "" && !v.inputActive))
	if v.help {
		v.helpScroll = min(v.helpScroll, max(0, len(terminalHelp)-(height-3)))
		drawHelp(s, width, height, v.helpScroll)
	}
	s.Show()
}

var terminalHelp = []string{
	"Keys (read only)",
	"",
	"Tab / Shift-Tab   next / previous view",
	"1-5               Tracks, Timeline, Decisions, Work items, History",
	"6-9               Project, Checkpoints, Health, Releases",
	"c                 selected track task checkpoints",
	"b                 preview selected bug report (Work items)",
	"↑ ↓ Home End      select",
	"→ l or Space      open the selection: history, record, links, topic, or a chosen plan",
	"p                 toggle the selected track's plan open/closed (Tracks)",
	"◉                 the track being worked on now (Tracks; bold row and header)",
	"/                 search history (within the open topic)",
	"← h Backspace Esc back",
	"[ ]               previous / next plan revision",
	"PgUp PgDn         scroll detail",
	"z or Enter        toggle full-screen detail",
	"f                 cycle track filter (Tracks)",
	"f                 cycle work item status (Work items)",
	"k                 cycle event kind (Timeline) or claim kind (History)",
	"n p               next / previous page (Timeline, Decisions, History, Checkpoints)",
	"r                 refresh from saved state",
	"q or Ctrl-C       quit",
	"",
	"PgUp/PgDn scroll this help; other keys close it.",
}

func drawHelp(s tcell.Screen, width, height, offset int) {
	boxWidth := 4
	for _, line := range terminalHelp {
		boxWidth = max(boxWidth, runewidth.StringWidth(line)+4)
	}
	boxWidth = min(boxWidth, width)
	boxHeight := min(len(terminalHelp)+2, height)
	x, y := (width-boxWidth)/2, (height-boxHeight)/2
	style := tcell.StyleDefault.Reverse(true)
	for row := 0; row < boxHeight; row++ {
		line := ""
		if row > 0 && offset+row-1 < len(terminalHelp) && row < boxHeight-2 {
			line = "  " + terminalHelp[offset+row-1]
		}
		if row == boxHeight-2 {
			line = "  PgUp/PgDn scroll · Esc close"
		}
		put(s, x, y+row, boxWidth, runewidth.FillRight(line, boxWidth), style)
	}
}

func visibleStart(index, rows int) int {
	if rows > 0 && index >= rows {
		return index - rows + 1
	}
	return 0
}

func put(s tcell.Screen, x, y, width int, value string, style tcell.Style) {
	if width <= 0 {
		return
	}
	for _, r := range sanitizeLine(value) {
		cells := runewidth.RuneWidth(r)
		if cells == 0 {
			continue
		}
		if cells > width {
			break
		}
		s.SetContent(x, y, r, nil, style)
		x += cells
		width -= cells
	}
}
