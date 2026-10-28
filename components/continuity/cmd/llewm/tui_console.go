package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/mattn/go-runewidth"
)

type terminalConsoleState struct {
	project     projectView
	checkpoints checkpoint.ListPage
	cursors     []string
	health      readiness.Report
	sync        historyStatus
	syncError   string
	releases    release.View
	indexes     map[string]int
	loaded      map[string]bool
	errors      map[string]string
}

func (v *terminalView) isConsoleView() bool {
	switch v.view {
	case "project", "checkpoints", "health", "releases":
		return true
	}
	return false
}

func (v *terminalView) ensureConsole(ctx context.Context) error {
	if v.console.loaded == nil {
		v.console.loaded = map[string]bool{}
	}
	if v.console.indexes == nil {
		v.console.indexes = map[string]int{}
	}
	if v.console.errors == nil {
		v.console.errors = map[string]string{}
	}
	if v.console.loaded[v.view] {
		return nil
	}
	var err error
	switch v.view {
	case "project":
		v.console.project, err = v.source.Project(ctx)
	case "checkpoints":
		v.console.checkpoints, err = v.source.Checkpoints(ctx, "", "")
		v.console.cursors = nil
	case "health":
		v.console.health, err = v.source.Health(ctx)
		syncStatus, syncErr := v.source.Sync(ctx)
		v.console.sync = syncStatus
		v.console.syncError = ""
		if syncErr != nil {
			v.console.syncError = syncErr.Error()
		}
	case "releases":
		v.console.releases, err = v.source.Releases(ctx)
	default:
		return nil
	}
	v.console.loaded[v.view] = true
	delete(v.console.errors, v.view)
	if err != nil {
		v.console.errors[v.view] = err.Error()
	}
	return nil
}

func (v *terminalView) consoleSelection() (*int, int, bool) {
	if l := v.level(); l != nil {
		switch l.kind {
		case "checkpoints":
			return &l.index, len(l.checkpoints.Checkpoints), true
		case "checkpoint", "release", "evidence":
			return &l.index, len(l.links), true
		case "report":
			return &l.index, 1, true
		}
		return nil, 0, false
	}
	if !v.isConsoleView() {
		return nil, 0, false
	}
	// A pointer into a map cannot be used; reuse an explicit top-view index.
	count := 1
	switch v.view {
	case "checkpoints":
		count = len(v.console.checkpoints.Checkpoints)
	case "releases":
		count = len(v.console.releases.Releases)
	}
	return &v.consoleIndex, count, true
}

func (v *terminalView) consoleDetail(ctx context.Context) (bool, error) {
	if l := v.level(); l != nil {
		switch l.kind {
		case "checkpoints":
			v.details = []string{"Task: " + l.ref, "No checkpoints recorded for this task."}
			if l.index < len(l.checkpoints.Checkpoints) {
				v.details = checkpointSummaryLines(l.checkpoints.Checkpoints[l.index])
			}
			return true, nil
		case "checkpoint", "release", "evidence", "report":
			v.details = append([]string{}, l.body...)
			if l.index < len(l.links) {
				v.details = append(v.details, "", "Selected link: "+l.links[l.index].kind+" "+l.links[l.index].target, "→ opens the link; ← returns here.")
			}
			return true, nil
		}
	}
	if v.isConsoleView() {
		if err := v.ensureConsole(ctx); err != nil {
			return true, err
		}
	}
	if message := v.console.errors[v.view]; message != "" && v.level() == nil {
		v.details = []string{"Unable to load " + terminalViewTitles[v.view] + ".", message, "Press r to retry, or Tab to browse another view."}
		return true, nil
	}
	if !v.isConsoleView() || v.level() != nil {
		return false, nil
	}
	c := &v.console
	switch v.view {
	case "project":
		p := c.project
		v.details = []string{"Project: " + p.Name, "Identity: " + p.ProjectID, "Root: " + p.Root, "Database: " + p.Database, "Checkout: " + p.Checkout.Kind + " " + p.Checkout.Path, "Repository: " + p.Identity.RepositoryID, fmt.Sprintf("Store inside project: %t", p.StoreInside)}
		if p.Workspace != nil {
			v.details = append(v.details, "Workspace: "+p.Workspace.Name+" ("+p.Workspace.Mode+")")
		}
		for _, m := range p.Members {
			v.details = append(v.details, fmt.Sprintf("Member: %s", m.Path))
		}
		for _, r := range p.Federated {
			v.details = append(v.details, "Member: "+r.Member+" · "+r.ProjectID+" · "+r.Root)
		}
		for _, l := range p.Locations {
			v.details = append(v.details, fmt.Sprintf("Location: %s", l.Path))
		}
		v.details = append(v.details, "", "Use --project-root PATH to browse another project.")
	case "checkpoints":
		v.details = []string{"No checkpoints recorded for this project."}
		if v.consoleIndex < len(c.checkpoints.Checkpoints) {
			v.details = checkpointSummaryLines(c.checkpoints.Checkpoints[v.consoleIndex])
		}
	case "health":
		h := c.health
		v.details = []string{"Readiness: " + h.Stage, "Source commit: " + valueOrUnknown(h.Source.GitCommit), "Installed commit: " + valueOrUnknown(h.Installed.Executable.GitCommit), "Executable: " + h.Installed.Executable.Path, "Database: " + h.Database.State + " · " + h.Database.AppliedMigration, "Integrity: " + h.Database.Integrity, "MCP runtime: " + h.Runtime.Reason, "Next: " + h.RecommendedNextAction}
		if c.syncError != "" {
			v.details = append(v.details, "Sync status unavailable: "+c.syncError)
		} else if line := c.sync.Line(v.now()); line != "" {
			v.details = append(v.details, line)
		}
		if h.Source.Dirty != nil {
			v.details = append(v.details, fmt.Sprintf("Source dirty: %t", *h.Source.Dirty))
		}
		for _, gate := range h.UnmetGates {
			v.details = append(v.details, "Unmet: "+gate)
		}
		for _, warning := range h.Warnings {
			v.details = append(v.details, "Warning: "+warning)
		}
		if h.Source.Error != "" {
			v.details = append(v.details, "Source error: "+h.Source.Error)
		}
		if h.Database.Error != "" {
			v.details = append(v.details, "Database error: "+h.Database.Error)
		}
		v.details = append(v.details, "", h.Disclaimer)
	case "releases":
		v.details = []string{"No release records yet.", "Record a reviewed draft with llewm release record."}
		if v.consoleIndex < len(c.releases.Releases) {
			v.details = releaseLines(c.releases.Releases[v.consoleIndex])
		}
		if c.releases.Truncated {
			v.details = append(v.details, "", "Showing the latest 100 revisions; llewm release view history --id ID shows a selected release.")
		}
	}
	return true, nil
}

func checkpointSummaryLines(c checkpoint.Summary) []string {
	predecessor := c.PredecessorCheckpointID
	if predecessor == "" {
		predecessor = "root handoff"
	}
	return []string{"Checkpoint: " + c.ID, "Task: " + valueOrUnknown(c.TaskID), "Title: " + c.Task, "Status: " + c.Status, "Recorded: " + c.CreatedAt.Format(time.RFC3339), "Predecessor: " + predecessor, "Authority: " + c.Authority, "→ opens the handoff and its links. Inspection does not resume work."}
}
func releaseLines(r release.Record) []string {
	lines := []string{fmt.Sprintf("Release: %s@%d · %s", r.ID, r.Revision, r.Version), "Title: " + r.Title, "Status: " + r.Status, "Reviewer: " + valueOrUnknown(r.Reviewer), "Authority: " + r.Authority, "Recorded: " + r.RecordedAt.Format(time.RFC3339)}
	if r.PublishedAt != nil {
		lines = append(lines, "Published: "+r.PublishedAt.Format(time.RFC3339))
	}
	for _, e := range r.Entries {
		lines = append(lines, "", e.Category+": "+e.Wording, fmt.Sprintf("Roadmap: %s@%d", e.NodeID, e.NodeRevision))
		for _, n := range e.NarrativeIDs {
			lines = append(lines, "Narrative: "+n)
		}
	}
	return lines
}

func (v *terminalView) consoleRows(width int) ([]string, bool) {
	var rows listRows
	rows.widths = []int{2, 12}
	l := v.level()
	if l != nil {
		switch l.kind {
		case "checkpoints":
			return checkpointRows(l.checkpoints, width), true
		case "checkpoint", "release", "evidence":
			for _, link := range l.links {
				rows.add(link.target, "", "→", link.kind)
			}
		case "report":
			rows.add(l.ref, "", "•", l.kind)
		default:
			return nil, false
		}
	} else {
		if !v.isConsoleView() {
			return nil, false
		}
		if v.console.errors[v.view] != "" {
			return nil, true
		}
		c := &v.console
		switch v.view {
		case "project":
			rows.add(c.project.Name, c.project.ProjectID, "•", "local")
		case "health":
			rows.add(c.health.RecommendedNextAction, "", "•", c.health.Stage)
			if c.syncError != "" {
				rows.add("Sync status unavailable: "+c.syncError, "", "•", "error")
			} else if line := c.sync.Line(v.now()); line != "" {
				rows.add(line, "", "•", "sync")
			}
		case "checkpoints":
			return checkpointRows(c.checkpoints, width), true
		case "releases":
			for _, r := range c.releases.Releases {
				rows.add(r.Version+" · "+r.Title, fmt.Sprintf("%s@%d", r.ID, r.Revision), statusGlyph(r.Status), r.Status)
			}
		}
	}
	return rows.layout(width), true
}
func checkpointRows(page checkpoint.ListPage, width int) []string {
	rows := listRows{widths: []int{2, 12, 12}}
	for _, c := range page.Checkpoints {
		rows.add(c.Task, c.TaskID, statusGlyph(c.Status), c.Status, shortTime(c.CreatedAt.Format(time.RFC3339)))
	}
	return rows.layout(width)
}

func (v *terminalView) openCheckpoint(ctx context.Context, id string) error {
	c, err := v.source.Checkpoint(ctx, id)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("checkpoint %s is unavailable in this project", id)
	}
	page, err := v.source.HistoryShow(ctx, []string{"checkpoint:" + id})
	if err != nil {
		return err
	}
	l := &terminalLevel{kind: "checkpoint", ref: "checkpoint:" + id, checkpoint: c}
	for _, i := range page.Items {
		if i.Error != "" {
			l.body = append(l.body, i.Error)
		} else {
			l.body = append(l.body, strings.Split(i.Text, "\n")...)
			if i.Truncated {
				l.body = append(l.body, "… truncated; llewm checkpoint show "+id)
			}
		}
	}
	if c.PredecessorCheckpointID != "" {
		l.links = append(l.links, terminalLink{"predecessor", "checkpoint:" + c.PredecessorCheckpointID})
	}
	if c.TaskID != "" {
		l.links = append(l.links, terminalLink{"task", "task:" + c.TaskID})
	}
	for _, r := range c.EvidenceRefs {
		l.links = append(l.links, terminalLink{"evidence", r})
	}
	l.body = append(l.body, "", "Caller-supplied handoff. Inspection does not resume work or authorize its next action.")
	return v.pushLevel(ctx, l)
}
func (v *terminalView) openTaskCheckpoints(ctx context.Context, taskID string) error {
	page, err := v.source.Checkpoints(ctx, taskID, "")
	if err != nil {
		return err
	}
	return v.pushLevel(ctx, &terminalLevel{kind: "checkpoints", ref: taskID, checkpoints: page})
}
func (v *terminalView) openSelectedCheckpoints(ctx context.Context) error {
	if v.level() != nil {
		v.status = "Return to Tracks to open a task's checkpoint chain."
		return nil
	}
	if v.workIndex >= len(v.work.Items) {
		return nil
	}
	item := v.work.Items[v.workIndex]
	if len(item.Tasks) > 1 {
		l := &terminalLevel{kind: "evidence", ref: trackID(item.Node, item.SelectedTaskID), body: []string{"Tasks bound to this track; → opens their checkpoints."}}
		for _, t := range item.Tasks {
			l.links = append(l.links, terminalLink{"task", "task:" + t.TaskID})
		}
		return v.pushLevel(ctx, l)
	}
	if item.SelectedTaskID == "" && len(item.Tasks) == 0 {
		v.status = "No task is bound to this track."
		return nil
	}
	taskID := item.SelectedTaskID
	if taskID == "" {
		taskID = item.Tasks[0].TaskID
	}
	return v.openTaskCheckpoints(ctx, taskID)
}
func (v *terminalView) openSelectedReport(ctx context.Context) error {
	ref := ""
	if l := v.level(); l != nil && l.kind == "record" {
		ref = strings.TrimPrefix(l.ref, "work_item:")
	} else if v.level() == nil && v.ideaIndex < len(v.ideas.Items) {
		ref = v.ideas.Items[v.ideaIndex].ID
	}
	if ref == "" {
		return nil
	}
	report, err := v.source.Report(ctx, ref)
	if err != nil {
		return err
	}
	l := &terminalLevel{kind: "report", ref: "work_item:" + ref, body: strings.Split(report.Markdown(), "\n")}
	return v.pushLevel(ctx, l)
}
func (v *terminalView) consoleDrill(ctx context.Context) (bool, error) {
	if l := v.level(); l != nil {
		switch l.kind {
		case "checkpoint", "release", "evidence":
			if l.index < len(l.links) {
				return true, v.openRef(ctx, l.links[l.index].target)
			}
			return true, nil
		case "checkpoints":
			if l.index < len(l.checkpoints.Checkpoints) {
				return true, v.openCheckpoint(ctx, l.checkpoints.Checkpoints[l.index].ID)
			}
			return true, nil
		case "report":
			v.expanded = true
			return true, nil
		}
		if l.kind == "plan" && l.index < len(l.items) {
			item := l.items[l.index].item
			links := []terminalLink{}
			for _, r := range item.EvidenceRefs {
				links = append(links, terminalLink{"evidence", r})
			}
			for _, id := range item.WorkItemIDs {
				links = append(links, terminalLink{"work_item", "work_item:" + id})
			}
			if len(links) > 0 {
				return true, v.pushLevel(ctx, &terminalLevel{kind: "evidence", ref: item.ID, links: links, body: []string{item.Title, item.Detail}})
			}
		}
		return false, nil
	}
	switch v.view {
	case "checkpoints":
		if v.consoleIndex < len(v.console.checkpoints.Checkpoints) {
			return true, v.openCheckpoint(ctx, v.console.checkpoints.Checkpoints[v.consoleIndex].ID)
		}
	case "releases":
		if v.consoleIndex < len(v.console.releases.Releases) {
			r := v.console.releases.Releases[v.consoleIndex]
			l := &terminalLevel{kind: "release", ref: fmt.Sprintf("%s@%d", r.ID, r.Revision), release: &r, body: releaseLines(r)}
			for _, e := range r.Entries {
				l.links = append(l.links, terminalLink{"roadmap", fmt.Sprintf("roadmap_node:%s@%d", e.NodeID, e.NodeRevision)})
				for _, n := range e.NarrativeIDs {
					l.links = append(l.links, terminalLink{"narrative", "narrative:" + n})
				}
			}
			return true, v.pushLevel(ctx, l)
		}
	case "project", "health":
		v.expanded = true
		return true, nil
	default:
		return false, nil
	}
	return true, nil
}

func (v *terminalView) checkpointPage(ctx context.Context, delta int) (bool, error) {
	var page *checkpoint.ListPage
	var cursors *[]string
	var index *int
	taskID := ""
	if l := v.level(); l != nil {
		if l.kind != "checkpoints" {
			return false, nil
		}
		page, cursors, index, taskID = &l.checkpoints, &l.cursors, &l.index, l.ref
	} else if v.view == "checkpoints" {
		page, cursors, index = &v.console.checkpoints, &v.console.cursors, &v.consoleIndex
	} else {
		return false, nil
	}
	nextCursors := append([]string{}, (*cursors)...)
	cursor := ""
	if delta > 0 {
		if page.NextCursor == "" {
			return true, nil
		}
		cursor = page.NextCursor
		nextCursors = append(nextCursors, cursor)
	} else {
		if len(nextCursors) == 0 {
			return true, nil
		}
		nextCursors = nextCursors[:len(nextCursors)-1]
		if len(nextCursors) > 0 {
			cursor = nextCursors[len(nextCursors)-1]
		}
	}
	next, err := v.source.Checkpoints(ctx, taskID, cursor)
	if err != nil {
		return true, err
	}
	*page, *cursors, *index = next, nextCursors, 0
	return true, v.detail(ctx)
}

func (v *terminalView) consoleHint() (string, bool) {
	common := "Tab view · / search · PgUp/PgDn detail · z expand · ? help · q quit"
	if l := v.level(); l != nil {
		switch l.kind {
		case "checkpoints":
			return "→ open · n/p page · ← back · " + common, true
		case "checkpoint", "release", "evidence":
			return "→ link · ← back · " + common, true
		case "report":
			return "← back · " + common, true
		}
		return "", false
	}
	if !v.isConsoleView() {
		return "", false
	}
	if v.view == "checkpoints" {
		return "→ open · n/p page · " + common, true
	}
	return "→ open · r refresh · " + common, true
}
func (v *terminalView) consoleContext() string {
	switch v.view {
	case "checkpoints":
		more := ""
		if v.console.checkpoints.NextCursor != "" {
			more = " · more (n)"
		}
		return fmt.Sprintf("page %d · %d checkpoints%s", len(v.console.cursors)+1, len(v.console.checkpoints.Checkpoints), more)
	case "releases":
		return fmt.Sprintf("%d of %d revisions", len(v.console.releases.Releases), v.console.releases.Total)
	case "project":
		return v.console.project.Name + " · " + v.console.project.ProjectID
	case "health":
		return v.console.health.Stage + " · local observations"
	}
	return ""
}

// tabLine keeps the selected view and nearby destinations visible at 60 columns.
func (v *terminalView) tabLine(width int) string {
	labels := []string{}
	selected := 0
	for i, name := range terminalTopViews {
		label := fmt.Sprintf("%d %s", (i+1)%10, terminalViewTitles[name])
		if name == v.view {
			selected = i
			label = "[" + label + "]"
		}
		labels = append(labels, label)
	}
	start, end := selected, selected+1
	for {
		if end < len(labels) && runewidth.StringWidth("Continuity · read only · "+strings.Join(labels[start:end+1], " · ")) <= width {
			end++
			continue
		}
		if start > 0 && runewidth.StringWidth("Continuity · read only · "+strings.Join(labels[start-1:end], " · ")) <= width {
			start--
			continue
		}
		break
	}
	return "Continuity · read only · " + strings.Join(labels[start:end], " · ")
}

// refresh stays in the selected view and preserves the navigation stack.
func (v *terminalView) refresh(ctx context.Context) error {
	if l := v.level(); l != nil {
		switch l.kind {
		case "history":
			if err := v.searchLevel(ctx, l); err != nil {
				return err
			}
		case "links":
			v.stack = v.stack[:len(v.stack)-1]
			if err := v.openRef(ctx, l.ref); err != nil {
				v.stack = append(v.stack, l)
				return err
			}
		case "report":
			report, err := v.source.Report(ctx, strings.TrimPrefix(l.ref, "work_item:"))
			if err != nil {
				return err
			}
			l.body = strings.Split(report.Markdown(), "\n")
		case "checkpoint":
			v.stack = v.stack[:len(v.stack)-1]
			if err := v.openCheckpoint(ctx, l.checkpoint.ID); err != nil {
				v.stack = append(v.stack, l)
				return err
			}
		case "checkpoints":
			page, err := v.source.Checkpoints(ctx, l.ref, "")
			if err != nil {
				return err
			}
			l.checkpoints, l.cursors, l.index = page, nil, 0
		case "plan":
			plan, err := v.source.Plan(ctx, l.plan.ID, l.plan.Revision)
			if err != nil {
				return err
			}
			l.plan, l.items = plan, planRows(plan.Items)
		}
		return v.detail(ctx)
	}
	if v.isConsoleView() {
		delete(v.console.loaded, v.view)
		v.consoleIndex = 0
		return v.detail(ctx)
	}
	view, wi, ei, di, ii, ti := v.view, v.workIndex, v.eventIndex, v.decisionIndex, v.ideaIndex, v.topicIndex
	if err := v.load(ctx); err != nil {
		return err
	}
	v.view, v.workIndex, v.eventIndex, v.decisionIndex, v.ideaIndex, v.topicIndex = view, wi, ei, di, ii, ti
	index, count := v.selection()
	*index = min(*index, max(0, count-1))
	return v.detail(ctx)
}

// linkedHistory follows an exact commit/record anchor instead of a text guess.
func (v *terminalView) linkedHistory(ctx context.Context, ref string) error {
	l := &terminalLevel{kind: "history", linked: ref}
	if err := v.searchLevel(ctx, l); err != nil {
		return err
	}
	return v.pushLevel(ctx, l)
}

func (v *terminalView) changeView(name string) {
	if v.console.indexes == nil {
		v.console.indexes = map[string]int{}
	}
	if v.isConsoleView() {
		v.console.indexes[v.view] = v.consoleIndex
	}
	v.view, v.stack, v.expanded = name, nil, false
	v.consoleIndex = v.console.indexes[name]
}

// A failed drill must retain the caller's screen and selection for retry.
func (v *terminalView) pushLevel(ctx context.Context, l *terminalLevel) error {
	stack, details, scroll := v.stack, v.details, v.detailScroll
	v.stack = append(v.stack, l)
	if err := v.detail(ctx); err != nil {
		v.stack, v.details, v.detailScroll = stack, details, scroll
		return err
	}
	return nil
}
