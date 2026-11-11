package conv

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/todo"
)

// maxVisibleItems caps task rows; overflow is summarized in one extra row.
const maxVisibleItems = 8

// trackerProgressCells is the width of the heading's progress bar: five cells
// read at a glance and still leave room for a plan title on a narrow strip.
const trackerProgressCells = 5

// TrackerListParams holds the parameters for rendering a tracker list.
type TrackerListParams struct {
	Items        []*todo.Item
	StreamActive bool
	Width        int
	Blockers     func(itemID string) []string
	// CompletionShown keeps a terminal worker visible until its notice is shown.
	// Nil treats completions as already announced.
	CompletionShown func(taskID string) bool
	// Executing reports whether the executor behind an item is running right
	// now. An in_progress item only animates when Executing says so: the
	// status field records intent and outlives the process that wrote it, so
	// it cannot by itself justify a moving pixel.
	Executing func(t *todo.Item) bool
	// Frame is the shared frame-tick counter (see FrameClock.Frame) that
	// breathes the in-progress item's live dot.
	Frame int
	// AgentColors maps an agent's name to its display color. An item owned by a
	// background agent (Owner set) is tinted with that color, matching the
	// agent's launch line in the flow. Plain user todos have no owner and keep
	// the status palette.
	AgentColors map[string]string
	// WorkerStatus provides display-only live activity and output-log health.
	AwaitingApproval map[string]bool
	WorkerStatus     func(taskID string) task.TaskInfo

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
	history := 0
	for _, t := range params.Items {
		id := todo.BackgroundTaskID(t)
		if id != "" && t.Status == todo.StatusCompleted && (params.CompletionShown == nil || params.CompletionShown(id)) {
			if phaseOf(t, false) == itemAborted {
				history++
			}
			continue
		}
		items = append(items, t)
	}
	todosDone, todos := TodoProgress(params.Items)
	planDone, planTotal := planTodoCounts(items)

	// Completed work may rest when idle — but a plan's rows are the record of
	// what just ran, so they stay struck through until the plan is replaced or
	// the session leaves Execute Mode (see syncPlanTodos). That is also why the
	// list is the only progress view: a finished step is a struck row here, not
	// a counter somewhere else.
	if !params.StreamActive && planTotal == 0 && todosDone == len(items) {
		items = nil
	}
	if len(items) == 0 && history == 0 {
		return ""
	}

	var sb strings.Builder
	headerStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	if len(items) > 0 {
		done, total := todosDone, todos
		if planTotal > 0 {
			done, total = planDone, planTotal
		}
		sb.WriteString(renderTrackerHeader(params, done, total, planTotal > 0) + "\n")
	}

	visible, hidden := visibleTrackerItems(items)
	idWidth := itemIDWidth(visible)
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
		line := renderItem(t, phase, max(1, params.Width-lipgloss.Width(suffix)), idWidth, params.Blockers, params.Frame, params.AgentColors)
		sb.WriteString(kit.TruncateText(strings.TrimSuffix(line, "\n")+suffix, max(1, params.Width)) + "\n")
	}

	// Current identities lead; past work is a single quiet secondary entry.
	// Share the overflow row rather than spending any of the eight task slots.
	folded := strings.TrimSuffix(renderFoldedLine(hidden), "\n")
	if history > 0 {
		label := kit.Plural(history, "past task")
		if len(items) == 0 {
			label = "Todos · " + label
		}
		prefix := "  "
		if folded != "" && params.Width > lipgloss.Width(label)+5 {
			prefix = kit.TruncateText(folded, params.Width-lipgloss.Width(label)-3) + " · "
		}
		folded = prefix + mutedStyle.Render(label)
	}
	if folded != "" {
		sb.WriteString(kit.TruncateText(folded, max(1, params.Width)) + "\n")
	}
	return sb.String()
}

// renderTrackerHeader is the strip's one heading: the plan's identity and its
// progress, or "Todos" for a plain list. Optional segments are added left to
// right only while they fit, so a narrow terminal loses the bar and notes
// before it loses the name of the work.
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
		counts := mutedStyle.Render(fmt.Sprintf("%d/%d done", done, total))
		if next := line + "  " + counts; fits(next) {
			line = next
			if bar := ProgressBar(done, total); fits(line + "  " + bar) {
				line += "  " + bar
			}
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
// running: an attention note, then the executor's own flags.
func trackerNotes(params TrackerListParams) []string {
	var notes []string
	if note := strings.Join(strings.Fields(params.PlanStatusNote), " "); note != "" {
		notes = append(notes, lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning).Render(note))
	}
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	if params.Paused {
		notes = append(notes, mutedStyle.Render("paused"))
	}
	if params.Awaiting {
		notes = append(notes, mutedStyle.Render("waiting for you"))
	}
	return notes
}

// ProgressBar draws done/total as a five-cell bar. It is a heading's second
// read: the counts say exactly, the bar says at a glance. Both headings that
// carry counts (the strip above the composer and the Execution Panel's step
// list) draw this one bar, so progress reads the same in both places.
func ProgressBar(done, total int) string {
	filled := 0
	if total > 0 {
		filled = min(max(done*trackerProgressCells/total, 0), trackerProgressCells)
	}
	filledStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
	emptyStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDisabled)
	return filledStyle.Render(strings.Repeat("▰", filled)) + emptyStyle.Render(strings.Repeat("▱", trackerProgressCells-filled))
}

// visibleTrackerItems keeps the newest items in each priority group, then draws
// them in their original order. Work that is moving, or that is waiting on the
// person, keeps its rows before history does.
func visibleTrackerItems(items []*todo.Item) ([]*todo.Item, [itemFinished + 1]int) {
	var hidden [itemFinished + 1]int
	if len(items) <= maxVisibleItems {
		return items, hidden
	}
	for _, t := range items {
		hidden[windowPhase(t)]++
	}
	// Every phase must be allocated here: a phase left out keeps its whole
	// count in hidden, which hides each of its rows. Running was missing once,
	// so a list with more than eight rows drew none of its live work.
	remaining := maxVisibleItems
	for _, phase := range []itemPhase{itemRunning, itemBlocked, itemStalled, itemAborted, itemWaiting, itemFinished} {
		shown := min(hidden[phase], remaining)
		hidden[phase] -= shown
		remaining -= shown
	}

	skip := hidden
	visible := make([]*todo.Item, 0, maxVisibleItems)
	for _, t := range items {
		phase := windowPhase(t)
		if skip[phase] > 0 {
			skip[phase]--
			continue
		}
		visible = append(visible, t)
	}
	return visible, hidden
}

// windowPhase ranks a row for the visible window. An item recorded in progress
// ranks as running even when no executor is live right now: liveness would have
// to be asked of every hidden row, and work that says it is in progress keeps
// its row ahead of blocked and pending work either way.
func windowPhase(t *todo.Item) itemPhase {
	phase := phaseOf(t, false)
	if phase == itemStalled {
		return itemRunning
	}
	return phase
}

func renderFoldedLine(hidden [itemFinished + 1]int) string {
	var parts []string
	// Running and stalled rows share one summary: windowing classifies by the
	// recorded status, and both read as work that is not finished.
	if n := hidden[itemRunning] + hidden[itemStalled]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d more in progress", n))
	}
	if n := hidden[itemBlocked]; n > 0 {
		parts = append(parts, kit.Plural(n, "blocked step"))
	}
	if n := hidden[itemAborted]; n > 0 {
		// The combined group also contains deliberate stops; only an
		// individual failure row, not this mixed count, warrants error red.
		parts = append(parts, fmt.Sprintf("%d failed/stopped", n))
	}
	if n := hidden[itemWaiting]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d more pending", n))
	}
	if n := hidden[itemFinished]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", n))
	}
	if len(parts) == 0 {
		return ""
	}
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	return "  " + mutedStyle.Render("…  "+strings.Join(parts, ", ")) + "\n"
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

// renderItem draws one checklist row. States with a status of their own
// (finished, skipped, active, blocked, pending) share the checklist primitive;
// approval, abort and stall keep the marks their phase has always drawn, since
// their meaning is narrower than any one state.
func renderItem(t *todo.Item, phase itemPhase, width, idWidth int, blockers func(string) []string, frame int, agentColors map[string]string) string {
	indent := "  "
	mutedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)

	tag, tagWidth := "", 0
	if todo.BackgroundTaskID(t) == "" {
		tag, tagWidth = "#"+t.ID, idWidth
	}
	idTag := ""
	if tag != "" {
		idTag = fmt.Sprintf("%-*s", idWidth, tag)
	}

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
			Frame:    frame,
			Indent:   indent,
			Tag:      tag,
			TagWidth: tagWidth,
			Text:     text,
			Detail:   detail,
			Width:    width,
			Tint:     tint,
		}) + "\n"
	}

	switch phase {
	case itemApproval:
		return renderItemLine(indent, mutedStyle.Render(kit.CurrentSymbols.LiveDot), idTag, activeText(t), mutedStyle.Render("[awaiting approval]"), width)
	case itemAborted:
		abortedStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
		if detail := todo.BackgroundStatusDetail(t); detail == "stopped" || detail == "killed" || detail == todo.StatusDetailInterrupted {
			abortedStyle = mutedStyle
		}
		detail := mutedStyle.Render("[" + todo.BackgroundStatusDetail(t) + "]")
		return renderItemLine(indent, abortedStyle.Render(kit.CurrentSymbols.LiveDot), idTag, t.Subject, detail, width)

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
		return renderItemLine(indent, mutedStyle.Render("◌"), idTag, activeText(t), mutedStyle.Render("[stalled]"), width)

	case itemRunning:
		// Breathe on the shared frame tick (a true ~360ms clock; see FrameClock)
		// rather than the wall clock, which only sampled on redraws and so
		// flickered irregularly.
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
	prefix := indent + icon + "  "
	if id != "" {
		prefix += id + "  "
	}
	suffix := ""
	if detail != "" {
		suffix = "  " + detail
	}
	textWidth := width - lipgloss.Width(prefix+suffix)
	if textWidth <= 0 {
		subject = ""
	} else {
		subject = kit.TruncateText(subject, textWidth)
	}
	return kit.TruncateText(prefix+subject+suffix, max(1, width)) + "\n"
}

func itemIDWidth(items []*todo.Item) int {
	width := 0
	for _, t := range items {
		if todo.BackgroundTaskID(t) == "" {
			width = max(width, len(t.ID)+1)
		}
	}
	return width
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
