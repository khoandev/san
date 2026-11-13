package conv

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/todo"
)

// maxVisibleItems caps the rows the strip spends. The window follows the step
// being worked on, so the list stays the size of one glance instead of growing
// with the plan.
const maxVisibleItems = 5

// TrackerListParams holds the parameters for rendering a tracker list.
type TrackerListParams struct {
	Items        []*todo.Item
	StreamActive bool
	Width        int
	// Frame advances the active row's breath. Zero draws the mark still, which
	// is what a caller that renders once (a print, a test) wants.
	Frame    int
	Blockers func(itemID string) []string
	// CompletionShown keeps a terminal worker visible until its notice is shown.
	// Nil treats completions as already announced.
	CompletionShown func(taskID string) bool
	// Executing reports whether the executor behind an item is running right
	// now. An in_progress item only reads as running when Executing says so:
	// the status field records intent and outlives the process that wrote it,
	// so it cannot by itself claim work is happening.
	Executing func(t *todo.Item) bool
	// AgentColors maps an agent's name to its display color. An item owned by a
	// background agent (Owner set) is tinted with that color, matching the
	// agent's launch line in the flow. Plain user todos have no owner and keep
	// the status palette.
	AgentColors map[string]string
	// WorkerStatus provides display-only live activity and output-log health.
	// It answers with a Brief rather than a TaskInfo on purpose: this runs
	// twice per visible row on every frame, and a TaskInfo carries the
	// worker's retained output (up to 512 KiB) that a row never draws. The
	// compiler elides that copy today because the value does not escape, but
	// that is an inlining decision rather than a promise; asking for a Brief
	// makes the cheap read structural instead of incidental.
	AwaitingApproval map[string]bool
	WorkerStatus     func(taskID string) task.Brief

	// PlanTitle names the plan whose steps are projected into Items. Empty
	// means no plan owns rows, and the strip is a plain todo list.
	PlanTitle string
	// PlanStatusNote is an optional attention note about the plan as a whole
	// ("2 blocked steps"). It is drawn in the warning tone when width allows.
	PlanStatusNote string
	// Paused and Awaiting report the executor's own flags, so the heading can
	// say why the plan is not moving.
	Paused   bool
	Awaiting bool
}

// RenderTrackerList renders the checklist above the input area as the session's
// one and only progress list: a plan's projected steps and the session's
// tracked work share the glyphs, colors and strike through ChecklistState.
// Terminal workers leave after their notice is shown; abnormal history is
// summarized. A finished plan's rows stay struck through until the plan is
// replaced. There is no separate plan-progress row: the plan's steps are rows
// here, so progress has exactly one presentation.
func RenderTrackerList(params TrackerListParams) string {
	if len(params.Items) == 0 {
		return ""
	}

	items := make([]*todo.Item, 0, len(params.Items))
	for _, t := range params.Items {
		id := todo.BackgroundTaskID(t)
		// A worker that finished normally leaves once its notice is shown: the
		// notice is its record. One that failed, was stopped or was orphaned
		// keeps a row — dropping it would hide the only sign something broke.
		if id != "" && t.Status == todo.StatusCompleted &&
			(params.CompletionShown == nil || params.CompletionShown(id)) &&
			phaseOf(t, false) != itemAborted {
			continue
		}
		items = append(items, t)
	}
	todosDone, todos := TodoProgress(items)
	planDone, planTotal := planTodoCounts(items)

	items = orderTrackerItems(items)
	ordinals, numbered := trackerOrdinals(items)
	tagWidth := 0
	if numbered > 0 {
		tagWidth = len(strconv.Itoa(numbered)) + 1
	}

	// Completed work may rest when idle — but a plan's rows are the record of
	// what just ran, so they stay struck through until the plan is replaced or
	// the session leaves Execute Mode (see syncPlanTodos).
	if !params.StreamActive && planTotal == 0 && todosDone == len(items) {
		items = nil
	}
	if len(items) == 0 {
		return ""
	}

	var sb strings.Builder
	headerStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	done, total := todosDone, todos
	if planTotal > 0 {
		done, total = planDone, planTotal
	}
	sb.WriteString(renderTrackerHeader(params, done, total, planTotal > 0) + "\n")

	visible := windowTrackerItems(items)
	planHeading := planTotal > 0
	lastPlan := true
	// Query executor liveness only for visible rows; counts use recorded status.
	for _, t := range visible {
		isPlan := todo.PlanID(t) != ""
		if planHeading && !isPlan && lastPlan {
			// The plan heading names the first group; the session's own work
			// gets its own quiet heading rather than reading as plan steps.
			sb.WriteString(kit.TruncateText("  "+headerStyle.Render("Todos"), max(1, params.Width)) + "\n")
		}
		lastPlan = isPlan
		phase := phaseOf(t, params.Executing != nil && params.Executing(t))
		waiting := t.Status == todo.StatusInProgress && todo.BackgroundTaskID(t) != "" && params.AwaitingApproval[todo.BackgroundTaskID(t)]
		if waiting && params.WorkerStatus != nil {
			waiting = params.WorkerStatus(todo.BackgroundTaskID(t)).Status == task.StatusRunning
		}
		if waiting {
			phase = itemApproval
		}
		progress := ""
		if phase == itemAborted {
			progress = todo.BackgroundError(t)
		}
		if id := todo.BackgroundTaskID(t); id != "" && params.WorkerStatus != nil {
			info := params.WorkerStatus(id)
			if phase == itemAborted {
				// A final reason outranks stale activity (e.g. the last Read call).
				if info.Error != "" {
					progress = info.Error
				}
				if info.Status != task.StatusFailed && kit.IsRoutineStopReason(progress) {
					progress = ""
				}
			} else if phase != itemFinished {
				progress = info.Progress
				if progress == "" {
					// Structured callers may supply only LiveView.
					progress, _, _ = strings.Cut(info.LiveView, "\n")
				}
			}
			if info.OutputError != "" {
				if phase == itemAborted && progress != "" {
					progress += " · output log unavailable"
				} else {
					progress = "output log unavailable · " + progress
				}
			}
		}
		progress = strings.Join(strings.Fields(progress), " ")
		// Keep one row per worker. Activity must not double tracker height and
		// push sibling identities out of a short terminal.
		suffix := ""
		if progress != "" {
			suffix = mutedStyle.Render(kit.TruncateText(" · "+progress, max(1, params.Width/2)))
		}
		num := itemNumber{width: tagWidth}
		if ordinal, ok := ordinals[t.ID]; ok {
			num.tag = strconv.Itoa(ordinal) + "."
		}
		line := renderItem(t, phase, max(1, params.Width-lipgloss.Width(suffix)), num, params.Blockers, params.AgentColors, ordinals, params.Frame)
		sb.WriteString(kit.TruncateText(strings.TrimSuffix(line, "\n")+suffix, max(1, params.Width)) + "\n")
	}
	return sb.String()
}

// renderTrackerHeader is the strip's one heading: the plan's identity and its
// counts, or "Todos" for a plain list. Optional segments are added left to
// right only while they fit, so a narrow terminal loses the notes before it
// loses the name of the work. The counts are the whole progress read: a bar
// beside them repeated what the numbers already said.
func renderTrackerHeader(params TrackerListParams, done, total int, plan bool) string {
	width := max(1, params.Width)
	label := "Todos"
	if plan {
		label = "Plan"
		if title := strings.Join(strings.Fields(params.PlanTitle), " "); title != "" {
			label = "Plan · " + title
		}
	}
	headerStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	fits := func(candidate string) bool { return lipgloss.Width(candidate) <= width }

	line := "  " + headerStyle.Render(label)
	if total > 0 {
		mark := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success).Render(kit.CurrentSymbols.TaskDone)
		counts := mark + " " + mutedStyle.Render(fmt.Sprintf("%d/%d done", done, total))
		if next := line + "  " + counts; fits(next) {
			line = next
		}
	}
	for _, note := range trackerNotes(params) {
		if next := line + " · " + note; fits(next) {
			line = next
		}
	}
	return kit.TruncateText(line, width)
}

// trackerNotes is what the heading says about a plan that is not simply
// running: an attention note, then the executor's own flags. The blocked and
// paused notes carry the checklist's own marks so the heading and the rows
// below it name a state with the same glyph.
func trackerNotes(params TrackerListParams) []string {
	var notes []string
	if note := strings.Join(strings.Fields(params.PlanStatusNote), " "); note != "" {
		mark := kit.CurrentSymbols.TaskBlocked + " "
		notes = append(notes, lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning).Render(mark+note))
	}
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	if params.Paused {
		notes = append(notes, mutedStyle.Render(kit.CurrentSymbols.TaskStalled+" paused"))
	}
	if params.Awaiting {
		notes = append(notes, mutedStyle.Render("waiting for you"))
	}
	return notes
}

// windowTrackerItems returns the rows the strip shows: the current row and the
// two nearest rows on each side, clamped to the list. A step 1..7 plan therefore
// reads 1..5 while step 1 or 2 runs and 3..7 while step 6 runs, and the heading's
// counts account for everything outside the window.
//
// With nothing recorded in progress the first unfinished row is the current
// one, so the window sits on the next step rather than on a block of history. A
// fully finished list keeps its tail: that is the record of what just ran.
func windowTrackerItems(items []*todo.Item) []*todo.Item {
	if len(items) <= maxVisibleItems {
		return items
	}
	focus := -1
	for i, t := range items {
		if t.Status == todo.StatusInProgress {
			focus = i
			break
		}
	}
	if focus < 0 {
		focus = len(items) - 1
		for i, t := range items {
			if phaseOf(t, false) != itemFinished {
				focus = i
				break
			}
		}
	}
	start := focus - maxVisibleItems/2
	start = max(0, min(start, len(items)-maxVisibleItems))
	return items[start : start+maxVisibleItems]
}

// itemPhase is how an item reads to the user. It collapses the recorded status,
// how a finished item ended, and whether an executor is actually running it
// into one exhaustive set, so each render branch answers to exactly one phase.
type itemPhase int

const itemApproval itemPhase = -1

const (
	itemWaiting  itemPhase = iota // nothing has started it
	itemRunning                   // in progress, with a live executor
	itemStalled                   // marked in progress, but nothing is executing it
	itemBlocked                   // a plan step waiting on the person
	itemAborted                   // ended without finishing its work
	itemFinished                  // ended having done its work
)

// TodoProgress counts the session's own list — a plan's projected steps
// included, since plan steps are todos — leaving background work out, and how
// many of those finished. A caller that only wants one of the two groups asks
// planTodoCounts instead.
func TodoProgress(items []*todo.Item) (done, total int) {
	for _, t := range items {
		if todo.BackgroundTaskID(t) == "" {
			total++
			if phaseOf(t, false) == itemFinished {
				done++
			}
		}
	}
	return done, total
}

// planTodoCounts counts the plan-projected rows and how many finished. The join
// is the plan ID in item metadata, which is what keeps a projected step
// separable from the model's own todos.
func planTodoCounts(items []*todo.Item) (done, total int) {
	for _, t := range items {
		if todo.PlanID(t) == "" {
			continue
		}
		total++
		if phaseOf(t, false) == itemFinished {
			done++
		}
	}
	return done, total
}

// phaseOf classifies an item. executing answers whether its executor is running;
// it only distinguishes itemRunning from itemStalled, since an item that already
// reached a terminal status has no worker left to ask about.
func phaseOf(t *todo.Item, executing bool) itemPhase {
	// A projected plan step carries the plan's own status, which has states the
	// item statuses cannot express. Blocked is unfinished work waiting on the
	// person, not a fresh pending row; completed still outranks stale metadata.
	if t.Status != todo.StatusCompleted && todo.PlanStepStatus(t) == string(plan.StepBlocked) {
		return itemBlocked
	}
	switch t.Status {
	case todo.StatusCompleted:
		if todo.EndedAbnormally(t) {
			return itemAborted
		}
		return itemFinished
	case todo.StatusInProgress:
		if executing {
			return itemRunning
		}
		return itemStalled
	default:
		return itemWaiting
	}
}

// activeText prefers the item's active phrasing ("Auditing deps") over its
// subject ("Audit deps") while it is the one being worked on.
func activeText(t *todo.Item) string {
	text := t.ActiveForm
	if text == "" {
		text = t.Subject
	}
	return text
}

// renderItem draws one checklist row. Every phase draws from the checklist's
// own mark family; frame advances the breath of the running row only.
func renderItem(t *todo.Item, phase itemPhase, width int, num itemNumber, blockers func(string) []string, agentColors map[string]string, ordinals map[string]int, frame int) string {
	indent := "  "
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	idTag := num.padded()

	// A row owned by a background agent wears that agent's color (icon + text),
	// mirroring its launch line in the flow; a plain todo keeps the status
	// palette. Aborted rows stay error-red and stalled rows stay muted — those
	// states carry more meaning than whose agent produced them.
	agentFg, owned := agentForeground(t, agentColors)
	tint := kit.AdaptiveColor{}
	if owned {
		tint = agentFg
	}

	checklist := func(state ChecklistState, text, detail string) string {
		return RenderChecklistRow(ChecklistRow{
			State:    state,
			Indent:   indent,
			Tag:      num.tag,
			TagWidth: num.width,
			Text:     text,
			Detail:   detail,
			Width:    width,
			Tint:     tint,
			Frame:    frame,
			Animate:  true,
		}) + "\n"
	}

	switch phase {
	case itemApproval:
		return renderItemLine(indent, mutedStyle.Render(kit.CurrentSymbols.TaskActive), idTag, activeText(t), mutedStyle.Render("[awaiting approval]"), width)
	case itemAborted:
		abortedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
		if detail := todo.BackgroundStatusDetail(t); detail == "stopped" || detail == "killed" || detail == todo.StatusDetailInterrupted {
			abortedStyle = mutedStyle
		}
		detail := mutedStyle.Render("[" + todo.BackgroundStatusDetail(t) + "]")
		return renderItemLine(indent, abortedStyle.Render(kit.CurrentSymbols.TaskFailed), idTag, t.Subject, detail, width)

	case itemFinished:
		state := ChecklistDone
		if todo.PlanStepStatus(t) == string(plan.StepSkipped) {
			state = ChecklistSkipped
		}
		detail := ""
		if todo.BackgroundTaskID(t) != "" {
			detail = "[completed]"
		}
		return checklist(state, t.Subject, detail)

	case itemBlocked:
		// The plan store, not the todo list, holds why the step stopped.
		detail := ""
		if reason := strings.TrimSpace(todo.PlanStepBlockReason(t)); reason != "" {
			detail = "· " + reason
		}
		return checklist(ChecklistBlocked, t.Subject, detail)

	case itemStalled:
		// Nothing is executing this item, so draw it at rest. Reaching here
		// means the status outlived its executor within a live session — the
		// model marked an item in_progress and moved on without closing it.
		// Animating would claim work that isn't happening.
		return renderItemLine(indent, mutedStyle.Render(kit.CurrentSymbols.TaskStalled), idTag, activeText(t), mutedStyle.Render("[stalled]"), width)

	case itemRunning:
		// The active row is the one live row in the strip: its mark breathes so
		// a long step visibly still has work arriving, while every settled row
		// around it holds still.
		detail := ""
		if elapsed := formatElapsedTime(t.StatusChangedAt); elapsed != "" {
			detail = "· " + elapsed
		}
		return checklist(ChecklistActive, activeText(t), detail)

	default:
		detail := ""
		if blockers != nil {
			if bl := blockers(t.ID); len(bl) > 0 {
				blockerRefs := make([]string, len(bl))
				for i, b := range bl {
					// A blocker is usually a row in this same list, so name it
					// by the number a reader can look for. Work that is not on
					// the list keeps its id.
					if ordinal, ok := ordinals[b]; ok {
						blockerRefs[i] = strconv.Itoa(ordinal)
						continue
					}
					blockerRefs[i] = "#" + b
				}
				detail = "← " + strings.Join(blockerRefs, ", ")
			}
		}
		return checklist(ChecklistPending, t.Subject, detail)
	}
}

// agentForeground returns the owning agent's display color for an item, and
// whether the item is owned by a colored agent at all — a plain user todo has no
// owner, and an owner with no configured color resolves to nothing.
func agentForeground(t *todo.Item, agentColors map[string]string) (kit.AdaptiveColor, bool) {
	if t.Owner == "" || len(agentColors) == 0 {
		return kit.AdaptiveColor{}, false
	}
	color, ok := agentColors[strings.ToLower(t.Owner)]
	if !ok || color == "" {
		return kit.AdaptiveColor{}, false
	}
	return agentColor(color), true
}

func renderItemLine(indent, icon, id, subject, detail string, width int) string {
	// Two columns after the mark, matching the checklist rows, so an icon and
	// the ordinal beside it never read as one glyph.
	prefix := indent + icon + "  "
	if id != "" {
		prefix += id + " "
	}
	suffix := ""
	if detail != "" {
		suffix = " " + detail
	}
	textWidth := width - lipgloss.Width(prefix+suffix)
	if textWidth <= 0 {
		subject = ""
	} else {
		subject = kit.TruncateText(subject, textWidth)
	}
	return kit.TruncateText(prefix+subject+suffix, max(1, width)) + "\n"
}

// orderTrackerItems puts the plan's rows first, in the order the plan gave
// them, then everything else in its recorded order. The ordinal column runs
// through the whole list, so plan steps keep the numbers 1..N the Execution
// Panel shows for the same steps.
func orderTrackerItems(items []*todo.Item) []*todo.Item {
	ordered := make([]*todo.Item, 0, len(items))
	for _, t := range items {
		if todo.PlanID(t) != "" {
			ordered = append(ordered, t)
		}
	}
	for _, t := range items {
		if todo.PlanID(t) == "" {
			ordered = append(ordered, t)
		}
	}
	return ordered
}

// trackerOrdinals numbers the rows that read as steps — the plan's steps and
// the session's own todos — in display order, and reports how many there were.
// Background work is nobody's step, so it keeps the column blank rather than
// taking a number. Numbers are assigned before windowing, so folding a row away
// never renumbers the rows that remain.
func trackerOrdinals(items []*todo.Item) (map[string]int, int) {
	ordinals := make(map[string]int, len(items))
	next := 0
	for _, t := range items {
		if todo.BackgroundTaskID(t) != "" {
			continue
		}
		next++
		ordinals[t.ID] = next
	}
	return ordinals, next
}

// itemNumber is one row's place in the shared ordinal column: the tag it draws
// and the column it must occupy. A background worker keeps the width and draws
// no tag, so every label in the list stays on one column.
type itemNumber struct {
	tag   string
	width int
}

// padded is the tag as it sits in the column. Tags are ASCII ordinals ("12."),
// so rune padding and display width agree.
func (n itemNumber) padded() string {
	if n.width <= 0 {
		return n.tag
	}
	return fmt.Sprintf("%-*s", n.width, n.tag)
}

func formatElapsedTime(since time.Time) string {
	if since.IsZero() {
		return ""
	}
	d := time.Since(since)
	if d < time.Second {
		return ""
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
