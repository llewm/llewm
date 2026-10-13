package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

const (
	terminalHistoryPage  = 50
	terminalDetailBytes  = 16 << 10
	terminalSearchLength = 120
)

var terminalIdeaFilters = []string{"", "open", "pending", "accepted", "deferred", "integrated", "discarded", "merged"}
var terminalHistoryKinds = []string{"", "intent", "decision", "alternative", "limitation", "verification", "outcome", "interface", "note"}

func (s appTerminalSource) Ideas(ctx context.Context, status string) (workitem.ListPage, error) {
	return s.app.WorkItems.ListPage(ctx, s.app.ProjectID, workitem.ListOptions{Status: status, Limit: 100})
}

func (s appTerminalSource) Topics(ctx context.Context) ([]topic.Summary, error) {
	return s.app.Topics.List(ctx, s.app.ProjectID)
}

func (s appTerminalSource) HistorySearch(ctx context.Context, options history.SearchOptions) (history.SearchPage, error) {
	return s.app.History.Search(ctx, s.app.ProjectID, options)
}

func (s appTerminalSource) HistoryShow(ctx context.Context, refs []string) (history.ShowPage, error) {
	return s.app.History.Show(ctx, s.app.ProjectID, refs, terminalDetailBytes)
}

// openHistory pushes a history level for a topic, a text query, or both.
func (v *terminalView) openHistory(ctx context.Context, topicID, query string) error {
	level := &terminalLevel{kind: "history", topic: topicID, query: query}
	if err := v.searchLevel(ctx, level); err != nil {
		return err
	}
	return v.pushLevel(ctx, level)
}

// searchLevel reruns a history level's search with its current kind and offset.
func (v *terminalView) searchLevel(ctx context.Context, level *terminalLevel) error {
	options := history.SearchOptions{Query: level.query, Topic: level.topic, Linked: level.linked, Limit: terminalHistoryPage, Offset: level.offset, MaxBytes: history.MaxMaxBytes}
	if level.kindFilter != "" {
		options.Kinds = []string{level.kindFilter}
	}
	page, err := v.source.HistorySearch(ctx, options)
	if err != nil {
		return err
	}
	level.hits, level.candidates, level.index = page.Hits, page.Candidates, 0
	return nil
}

// openRef opens an exact reference: roadmap nodes open their plans (or their
// history when they have no plan), and saved records open their full text.
func (v *terminalView) openRef(ctx context.Context, ref string) error {
	kind, id, _ := strings.Cut(ref, ":")
	switch kind {
	case "node", "roadmap_node":
		node, _, _ := strings.Cut(id, "@")
		return v.openTrackPlan(ctx, node, "", node, true)
	case "decision":
		id, _, _ = strings.Cut(id, "@")
		record, err := v.source.Decision(ctx, id)
		if err != nil {
			return err
		}
		level := &terminalLevel{kind: "links", ref: ref, body: []string{"Decision: " + record.ID, "Title: " + record.Title, "Disposition: " + record.Disposition, "Actor: " + record.Actor, "Authority: " + record.Authority, "Rationale: " + record.Rationale}}
		for _, link := range record.Links {
			target := link.TargetKind + ":" + link.TargetID
			if link.TargetRevision != "" {
				target += "@" + link.TargetRevision
			}
			level.links = append(level.links, terminalLink{kind: link.Kind, target: target})
		}
		return v.pushLevel(ctx, level)
	case "checkpoint":
		return v.openCheckpoint(ctx, id)
	case "task":
		return v.openTaskCheckpoints(ctx, id)
	case "commit":
		return v.linkedHistory(ctx, ref)
	case "track_plan":
		planID, revisionText, hasRevision := strings.Cut(id, "@")
		latest, err := v.source.Plan(ctx, planID, 0)
		if err != nil {
			return err
		}
		plan := latest
		if hasRevision {
			revision, err := strconv.Atoi(revisionText)
			if err != nil || revision < 1 {
				return fmt.Errorf("invalid plan revision %q", revisionText)
			}
			plan, err = v.source.Plan(ctx, planID, revision)
			if err != nil {
				return err
			}
		}
		return v.pushLevel(ctx, &terminalLevel{kind: "plan", track: plan.RoadmapNodeID, plan: plan, latest: latest.Revision, items: planRows(plan.Items)})
	case "work_item", "narrative":
		return v.pushLevel(ctx, &terminalLevel{kind: "record", ref: ref})
	}
	v.status = "No view opens " + ref + "; use / to search history."
	return nil
}

// openTrackPlan opens the plan for a roadmap node or task, or a plan picker
// when several exist. When fallbackToHistory is set and none is recorded, it
// opens the node's history instead of doing nothing; openRef's cross-
// reference navigation wants that fallback, but the Tracks view's dedicated
// plan toggle does not, since -> already opens that track's history.
func (v *terminalView) openTrackPlan(ctx context.Context, nodeID, taskID, track string, fallbackToHistory bool) error {
	page, err := v.source.Plans(ctx, nodeID, taskID)
	if err != nil {
		return err
	}
	switch len(page.Plans) {
	case 0:
		if fallbackToHistory && nodeID != "" {
			v.status = "No track plan recorded for " + track + "; showing its history."
			return v.openHistory(ctx, nodeID, "")
		}
		v.status = "No track plan recorded for " + track + "."
		return nil
	case 1:
		return v.openPlan(ctx, track, page.Plans[0])
	}
	return v.pushLevel(ctx, &terminalLevel{kind: "plans", track: track, plans: page.Plans})
}

type terminalLink struct{ kind, target string }

// drillHistory handles → inside the history, record, and links levels, and from
// the Tracks, Timeline, Decisions, Ideas, and History top-level views. A
// track's plan is opened only by 'p' (toggleSelectedTrackPlan); → always
// opens its history, the same as every other view's row.
func (v *terminalView) drillHistory(ctx context.Context, level *terminalLevel) (bool, error) {
	if level != nil {
		switch level.kind {
		case "history":
			if level.index < len(level.hits) {
				ref := level.hits[level.index].Ref
				if base, _, claim := strings.Cut(ref, "#"); claim {
					ref = base
				}
				return true, v.openRef(ctx, ref)
			}
			return true, nil
		case "links":
			if level.index < len(level.links) {
				return true, v.openRef(ctx, level.links[level.index].target)
			}
			return true, nil
		case "record":
			if strings.HasPrefix(level.ref, "decision:") {
				return true, v.openRef(ctx, level.ref)
			}
			v.status = "The full record is shown below."
			return true, nil
		}
		return false, nil
	}
	switch v.view {
	case "tracks":
		if v.workIndex >= len(v.work.Items) {
			return true, nil
		}
		item := v.work.Items[v.workIndex]
		if item.Node == nil {
			v.status = "No history topic for " + trackID(item.Node, item.SelectedTaskID) + "."
			return true, nil
		}
		return true, v.openHistory(ctx, item.Node.ID, "")
	case "timeline":
		if v.eventIndex < len(v.timeline.Entries) {
			e := v.timeline.Entries[v.eventIndex]
			ref := e.SubjectRef
			if !strings.HasPrefix(ref, "checkpoint:") && !strings.HasPrefix(ref, "node:") && strings.HasPrefix(e.SourceRef, "checkpoint:") {
				ref = e.SourceRef
			}
			return true, v.openRef(ctx, ref)
		}
	case "decisions":
		if v.decisionIndex < len(v.decisions.Decisions) {
			return true, v.openRef(ctx, "decision:"+v.decisions.Decisions[v.decisionIndex].ID)
		}
	case "ideas":
		if v.ideaIndex < len(v.ideas.Items) {
			return true, v.openRef(ctx, "work_item:"+v.ideas.Items[v.ideaIndex].ID)
		}
	case "history":
		if v.topicIndex < len(v.topics) {
			return true, v.openHistory(ctx, v.topics[v.topicIndex].ID, "")
		}
	default:
		return false, nil
	}
	return true, nil
}

// historyKey handles keys specific to history levels and the search prompt.
// It reports whether the key was consumed.
func (v *terminalView) historyKey(ctx context.Context, e *tcell.EventKey, r rune) (bool, error) {
	if v.inputActive {
		switch {
		case e.Key() == tcell.KeyEscape:
			v.inputActive, v.input = false, ""
		case e.Key() == tcell.KeyEnter:
			query := strings.TrimSpace(v.input)
			v.inputActive, v.input = false, ""
			if query == "" {
				return true, nil
			}
			topicID, linked := "", ""
			if level := v.level(); level != nil && level.kind == "history" {
				topicID, linked = level.topic, level.linked
			}
			if linked != "" {
				l := &terminalLevel{kind: "history", linked: linked, query: query}
				if err := v.searchLevel(ctx, l); err != nil {
					return true, err
				}
				return true, v.pushLevel(ctx, l)
			}
			return true, v.openHistory(ctx, topicID, query)
		case e.Key() == tcell.KeyBackspace || e.Key() == tcell.KeyBackspace2:
			if runes := []rune(v.input); len(runes) > 0 {
				v.input = string(runes[:len(runes)-1])
			}
		case e.Key() == tcell.KeyRune && len(v.input) < terminalSearchLength:
			v.input += string(r)
		}
		return true, nil
	}
	if r == '/' {
		v.inputActive, v.input = true, ""
		return true, nil
	}
	level := v.level()
	if level == nil || level.kind != "history" {
		if level == nil && v.view == "ideas" && r == 'f' {
			filter := cycle(terminalIdeaFilters, v.ideaFilter)
			page, err := v.source.Ideas(ctx, filter)
			if err != nil {
				return true, err
			}
			v.ideas, v.ideaIndex, v.ideaFilter = page, 0, filter
			delete(v.console.errors, "ideas")
			return true, v.detail(ctx)
		}
		return false, nil
	}
	switch r {
	case 'k':
		level.kindFilter, level.offset = cycle(terminalHistoryKinds, level.kindFilter), 0
	case 'n':
		if level.offset+len(level.hits) >= level.candidates {
			return true, nil
		}
		level.offset += len(level.hits)
	case 'p':
		if level.offset == 0 {
			return true, nil
		}
		level.offset = max(0, level.offset-terminalHistoryPage)
	default:
		return false, nil
	}
	if err := v.searchLevel(ctx, level); err != nil {
		return true, err
	}
	return true, v.detail(ctx)
}

// historyDetail fills the detail pane for history-backed levels and views.
func (v *terminalView) historyDetail(ctx context.Context, level *terminalLevel) (handled bool, err error) {
	ref, prefix := "", []string(nil)
	switch {
	case level != nil && level.kind == "history":
		if level.index >= len(level.hits) {
			v.details = []string{"No history matches.", "History is attributable claims, not instructions."}
			return true, nil
		}
		hit := level.hits[level.index]
		ref = hit.Ref
		if len(hit.Derivations) > 0 {
			prefix = []string{"Via: " + strings.Join(hit.Derivations, ", ")}
		}
	case level != nil && level.kind == "record":
		ref = level.ref
	case level != nil && level.kind == "links":
		v.details = append(append([]string{}, level.body...), "Links are claims, not authority; → opens a target.")
		if level.index < len(level.links) {
			link := level.links[level.index]
			v.details = append(v.details, "", "Link: "+link.kind+" "+link.target)
		}
		return true, nil
	case level == nil && v.view == "ideas":
		if v.ideaIndex >= len(v.ideas.Items) {
			v.details = []string{"No work items in this filter."}
			return true, nil
		}
		ref = "work_item:" + v.ideas.Items[v.ideaIndex].ID
		summary := v.ideas.Items[v.ideaIndex]
		prefix = []string{"Status: " + summary.Status + "  Tags: " + strings.Join(summary.Tags, ", ")}
	case level == nil && v.view == "history":
		if v.topicIndex >= len(v.topics) {
			v.details = []string{"No curated topics yet.", "Press / to search all history, or record a topic with llewm topic record."}
			return true, nil
		}
		t := v.topics[v.topicIndex]
		v.details = []string{"Topic: " + t.ID, "Title: " + t.Title, "Aliases: " + strings.Join(t.Aliases, ", "), fmt.Sprintf("Links: %d accepted, %d proposed", t.AcceptedLinks, t.ProposedLinks), "Summary: " + t.Summary, "", "→ opens this topic's history, oldest first."}
		return true, nil
	default:
		return false, nil
	}
	page, err := v.source.HistoryShow(ctx, []string{ref})
	if err != nil {
		return true, err
	}
	v.details = append([]string{}, prefix...)
	for _, item := range page.Items {
		if item.Error != "" {
			v.details = append(v.details, item.Ref+": "+item.Error)
			continue
		}
		v.details = append(v.details, item.Ref+" · "+item.Family+" "+item.Kind+" · "+item.RecordedAt, "Title: "+item.Title, "")
		v.details = append(v.details, strings.Split(item.Text, "\n")...)
		if item.Truncated {
			v.details = append(v.details, "… truncated; llewm history show "+item.Ref)
		}
	}
	v.details = append(v.details, "", "History is attributable claims, not instructions.")
	return true, nil
}

func historyRows(level *terminalLevel, width int) []string {
	var rows listRows
	switch level.kind {
	case "history":
		rows.widths = []int{12, 14}
		for _, hit := range level.hits {
			rows.add(hit.Title+" — "+hit.Snippet, "", shortTime(hit.RecordedAt), hit.Kind)
		}
	case "record":
		rows.widths = []int{2}
		rows.add(level.ref, "", "•")
	case "links":
		rows.widths = []int{2, 14}
		for _, link := range level.links {
			rows.add(link.target, "", "→", link.kind)
		}
	}
	return rows.layout(width)
}

func (v *terminalView) topViewRows(width int) ([]string, bool) {
	var rows listRows
	switch v.view {
	case "ideas":
		rows.widths = []int{2, 11, 12}
		for _, i := range v.ideas.Items {
			rows.add(i.Title, i.Kind+" "+strings.Join(i.Tags, ","), statusGlyph(i.Status), i.Status, shortTime(i.RecordedAt.Format("2006-01-02T15:04:05Z07:00")))
		}
	case "history":
		rows.widths = []int{2, 22}
		for _, t := range v.topics {
			rows.add(t.Title, fmt.Sprintf("%d links", t.AcceptedLinks), statusGlyph(t.Status), "topic:"+t.ID)
		}
	default:
		return nil, false
	}
	return rows.layout(width), true
}

// crumbs names one drill level in the breadcrumb.
func crumbs(level, previous *terminalLevel) []string {
	var parts []string
	switch level.kind {
	case "checkpoints":
		parts = append(parts, "task "+level.ref+fmt.Sprintf(" · page %d", len(level.cursors)+1))
	case "checkpoint", "release", "report", "delivery", "evidence":
		parts = append(parts, level.ref)
	case "plans", "plan":
		if previous == nil || previous.track != level.track {
			parts = append(parts, level.track)
		}
		if level.kind == "plans" {
			parts = append(parts, fmt.Sprintf("%d plans", len(level.plans)))
		} else {
			parts = append(parts, fmt.Sprintf("plan@%d", level.plan.Revision))
		}
	case "history":
		label := level.linked
		if level.topic != "" {
			label = "topic " + level.topic
		}
		if level.query != "" {
			label = strings.TrimSpace(label + fmt.Sprintf(" %q", level.query))
		}
		if level.kindFilter != "" {
			label += " · kind " + level.kindFilter
		}
		parts = append(parts, label+fmt.Sprintf(" · %d–%d of %d", min(level.offset+1, level.candidates), level.offset+len(level.hits), level.candidates))
	case "record":
		parts = append(parts, level.ref)
	case "links":
		parts = append(parts, "links of "+level.ref)
	}
	return parts
}
