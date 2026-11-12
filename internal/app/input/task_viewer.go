package input

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/task"
)

// TaskViewItem is a display-only snapshot. Output is recent bounded output, not
// a promise of a complete historical log. Status preserves the backend lifecycle.
type TaskViewItem struct {
	ID, AgentName, Description, Subject, Progress, Output, Error string
	OutputFile                                                   string
	Recorded                                                     bool // Persisted entry without a live worker.
	Status                                                       task.TaskStatus
	AwaitingApproval, ApprovalQueued, OutputTruncated            bool
	CanContinue                                                  bool
	Attempt                                                      int
	SessionID                                                    string
	Attempts                                                     string // Concise immutable run history, not the child conversation.
	// What the row needs to answer "which worker, how far, on what" without
	// opening the inspector. Zero values simply omit the cell.
	Model     string
	Mode      string
	Activity  string // Latest structured activity of a running worker.
	Steps     int
	Tokens    int
	StartedAt time.Time
	EndedAt   time.Time
}
type TaskStopMsg struct {
	TaskID  string
	ViewID  uint64
	Attempt int
}
type TaskContinueMsg struct {
	TaskID  string
	ViewID  uint64
	Attempt int
}

type TaskReviewApprovalMsg struct {
	TaskID string
	ViewID uint64
}

// TaskTranscriptMsg asks the app to open a worker's own conversation. The
// viewer only names the session; reading it is the app's job.
type TaskTranscriptMsg struct {
	TaskID    string
	SessionID string
	ViewID    uint64
}

// TaskSteerMsg carries a line the user typed straight into a running worker.
// It is ordinary inbox mail, not a new execution.
type TaskSteerMsg struct {
	TaskID  string
	Message string
	ViewID  uint64
	Attempt int
}
type TaskViewerClosedMsg struct{ ViewID uint64 }

// TaskViewer never mutates tasks. The caller owns all lifecycle operations.
type TaskViewer struct {
	active, detail, confirm, confirmStop bool
	historyExpanded                      bool
	width, height                        int
	body                                 viewport.Model
	rowOffsets                           map[string]int
	items                                []TaskViewItem
	selected                             string
	archived                             map[string]TaskViewItem
	stopID                               string
	stopAttempt                          int
	viewID                               uint64
	pendingAction                        bool
	steer                                bool
	steerInput                           textinput.Model
}

func (v *TaskViewer) IsActive() bool      { return v.active }
func (v *TaskViewer) ViewID() uint64      { return v.viewID }
func (v *TaskViewer) ActionPending() bool { return v.pendingAction }
func (v *TaskViewer) Show(items []TaskViewItem, width, height int) {
	*v = TaskViewer{active: true, viewID: v.viewID + 1, body: viewport.New(), archived: make(map[string]TaskViewItem)}
	v.body.FillHeight = true
	v.Resize(width, height)
	v.Refresh(items)
}
func (v *TaskViewer) Resize(width, height int) {
	v.width, v.height = max(1, width), max(1, height)
	v.body.SetWidth(max(1, v.width-1))
	v.rebuild()
}
func (v *TaskViewer) current() (TaskViewItem, bool) {
	if item, ok := v.live(v.selected); ok {
		return item, true
	}
	item, ok := v.archived[v.selected]
	return item, ok
}
func (v *TaskViewer) live(id string) (TaskViewItem, bool) {
	for _, item := range v.items {
		if item.ID == id {
			return item, true
		}
	}
	return TaskViewItem{}, false
}
func (v *TaskViewer) Refresh(items []TaskViewItem) {
	if v.archived == nil {
		v.archived = make(map[string]TaskViewItem)
	}
	v.items = nil
	seen := make(map[string]bool)
	for _, item := range items {
		if item.ID != "" && !seen[item.ID] {
			v.items = append(v.items, item)
			seen[item.ID] = true
			v.archived[item.ID] = item
		}
	}
	if v.selected == "" && len(v.items) > 0 {
		v.selected = v.items[0].ID
	}
	if v.confirm {
		item, ok := v.live(v.stopID)
		if !ok || item.Status != task.StatusRunning || item.Attempt != v.stopAttempt {
			v.confirm = false
			v.confirmStop = false
			v.stopID = ""
		}
	}
	v.rebuild()
}
func taskViewStatus(item TaskViewItem) string {
	status := "unknown"
	if item.Status == task.StatusRunning && item.AwaitingApproval {
		if item.ApprovalQueued {
			status = "awaiting approval (queued request)"
		} else {
			status = "awaiting approval (current request)"
		}
	} else if item.Status != "" {
		status = string(item.Status)
	}
	return status
}
func taskViewSubject(item TaskViewItem) string {
	subject := item.Subject
	if subject == "" {
		subject = item.Description
	}
	owner := strings.TrimSpace(item.AgentName)
	if owner != "" && !strings.EqualFold(owner, "agent") && !strings.EqualFold(owner, "bash") && !strings.EqualFold(owner, subject) && !strings.HasPrefix(subject, owner+": ") {
		return owner + ": " + subject
	}
	return subject
}

func taskViewStopped(item TaskViewItem) bool {
	return item.Status == task.StatusStopped || item.Status == task.StatusKilled
}

func (v *TaskViewer) rebuild() {
	if !v.active || v.confirm {
		return
	}
	previousSelection := v.selected
	if !v.detail {
		v.selectVisible()
	}
	v.body.SetHeight(max(1, v.height-4-strings.Count(v.hint(), "\n")-1-transcriptFrameMargin))
	offset := v.body.YOffset()
	var lines []string
	if v.detail {
		item, ok := v.current()
		if !ok {
			lines = []string{"Task no longer available."}
		} else {
			lines = []string{taskViewSubject(item), "Owner: " + item.AgentName, "ID: " + item.ID, "Status: " + taskViewStatus(item)}
			if item.Model != "" {
				lines = append(lines, "Model: "+item.Model)
			}
			if item.Mode != "" {
				lines = append(lines, "Mode: "+item.Mode)
			}
			if usage := taskViewUsage(item); usage != "" {
				lines = append(lines, "Usage: "+usage)
			}
			if timing := taskViewTiming(item, time.Now()); timing != "" {
				lines = append(lines, timing)
			}
			if item.SessionID != "" {
				lines = append(lines, "Session: "+item.SessionID)
			}
			if item.Attempt > 0 {
				lines = append(lines, fmt.Sprintf("Attempt: %d", item.Attempt))
			}
			if item.Attempts != "" {
				lines = append(lines, "Run history:\n"+item.Attempts)
			}
			if _, live := v.live(item.ID); !live {
				lines = append(lines, "No longer in the live task list; showing last snapshot.")
			}
			if item.Recorded {
				lines = append(lines, "Recorded task; live worker unavailable.")
			}
			if item.OutputFile != "" {
				lines = append(lines, "Output log: "+item.OutputFile)
			}
			if item.Status == task.StatusRunning && item.Progress != "" {
				lines = append(lines, "Progress: "+item.Progress)
			}
			if item.Status == task.StatusRunning && item.Activity != "" && item.Activity != item.Progress {
				lines = append(lines, "Activity: "+item.Activity)
			}
			if item.Error != "" && (!taskViewStopped(item) || !kit.IsRoutineStopReason(item.Error)) {
				label := "Error: "
				if taskViewStopped(item) {
					label = "Reason: "
				}
				lines = append(lines, label+item.Error)
			}
			label := "Recent output"
			if item.OutputTruncated {
				label += " (truncated)"
			}
			output := item.Output
			if taskViewStopped(item) && kit.IsRoutineStopReason(strings.TrimPrefix(strings.TrimSpace(output), "Error: ")) {
				output = ""
			}
			if output == "" {
				output = "No recent output available."
			}
			lines = append(lines, "", label, output)
		}
	} else {
		active, history := v.groupedItems()
		v.rowOffsets = make(map[string]int, len(v.items))
		rowOffset := 0
		addLine := func(line string) {
			lines = append(lines, line)
			rowOffset += strings.Count(ansi.Wrap(line, max(1, v.width-2), " "), "\n") + 1
		}
		now := time.Now()
		addItem := func(item TaskViewItem) {
			v.rowOffsets[item.ID] = rowOffset
			prefix := "  " + kit.CurrentSymbols.LiveDot + " "
			if item.ID == v.selected {
				prefix = "› " + kit.CurrentSymbols.LiveDot + " "
			}
			addLine(prefix + taskViewRow(item, now, max(1, v.width-2-lipgloss.Width(prefix))))
		}
		for _, item := range active {
			addItem(item)
		}
		if len(active) == 0 {
			if len(history) == 0 {
				addLine("No background tasks.")
			} else {
				addLine("No active background tasks.")
			}
		}
		if len(history) > 0 {
			addLine("")
			addLine(v.historySummary(history))
			if v.historyExpanded {
				for _, item := range history {
					addItem(item)
				}
			}
		}
		if v.selected != "" {
			if _, ok := v.live(v.selected); !ok {
				lines = append(lines, "Selected task disappeared. Select a row to continue.")
			}
		}
	}
	v.body.SetContent(ansi.Wrap(strings.Join(lines, "\n"), max(1, v.width-2), " "))
	v.body.SetYOffset(offset)
	// Preserve deliberate page scrolling unless rebuilding changed selection.
	if !v.detail && v.selected != previousSelection {
		v.ensureSelectionVisible()
	}
}

func (v *TaskViewer) ensureSelectionVisible() {
	row, ok := v.rowOffsets[v.selected]
	if !ok {
		return
	}
	if row < v.body.YOffset() {
		v.body.SetYOffset(row)
	} else if row >= v.body.YOffset()+v.body.Height() {
		v.body.SetYOffset(row - v.body.Height() + 1)
	}
}
func (v *TaskViewer) close() { *v = TaskViewer{viewID: v.viewID} }

// Hide invalidates pending actions and releases the session's display snapshots.
func (v *TaskViewer) Hide() { *v = TaskViewer{viewID: v.viewID + 1} }

// Suspend acknowledges an action while preserving selection and detail scroll.
func (v *TaskViewer) Suspend() {
	v.active, v.confirm, v.confirmStop, v.pendingAction = false, false, false, false
	v.stopID = ""
}

// Resume returns from a root-owned action to the same task, not a row index.
func (v *TaskViewer) Resume(items []TaskViewItem, width, height int) {
	v.viewID++
	v.active, v.pendingAction = true, false
	v.Resize(width, height)
	v.Refresh(items)
}
func (v *TaskViewer) HandleKeypress(key tea.KeyMsg) tea.Cmd {
	if !v.active {
		return nil
	}
	if v.steer {
		return v.handleSteerKey(key)
	}
	k := key.String()
	// Repeated activation keys must never confirm a destructive action.
	if key.Key().IsRepeat && (k == "enter" || k == "s" || k == "r" || k == "c" || k == "t" || k == "m") {
		return nil
	}
	if v.confirm {
		switch k {
		case "esc", "q":
			v.confirm = false
			v.confirmStop = false
		case "left", "right", "tab":
			v.confirmStop = !v.confirmStop
		case "enter":
			id := v.stopID
			item, ok := v.live(id)
			if v.confirmStop && ok && !item.Recorded && item.Status == task.StatusRunning && v.selected == id && item.Attempt == v.stopAttempt {
				viewID, attempt := v.viewID, v.stopAttempt
				v.Suspend()
				v.pendingAction = true
				return func() tea.Msg { return TaskStopMsg{TaskID: id, ViewID: viewID, Attempt: attempt} }
			}
			v.confirm = false
			v.confirmStop = false
		}
		v.rebuild()
		return nil
	}
	switch k {
	case "esc", "q", "ctrl+c":
		if v.detail {
			v.detail = false
			v.body.GotoTop()
			v.rebuild()
		} else {
			viewID := v.viewID
			v.close()
			return func() tea.Msg { return TaskViewerClosedMsg{ViewID: viewID} }
		}
	case "enter":
		if _, ok := v.current(); ok {
			v.detail = true
			v.body.GotoTop()
			v.rebuild()
		}
	case "h":
		if !v.detail && !key.Key().IsRepeat {
			v.historyExpanded = !v.historyExpanded
			v.body.GotoTop()
			v.rebuild()
		}
	case "s":
		if item, ok := v.live(v.selected); ok && !item.Recorded && item.Status == task.StatusRunning {
			v.confirm = true
			v.confirmStop = false
			v.stopID, v.stopAttempt = item.ID, item.Attempt
			v.rebuild()
		}
	case "c":
		if item, ok := v.live(v.selected); ok && !item.Recorded && item.CanContinue && (item.Status == task.StatusFailed || item.Status == task.StatusCompleted) {
			id, viewID, attempt := item.ID, v.viewID, item.Attempt
			v.Suspend()
			v.pendingAction = true
			return func() tea.Msg { return TaskContinueMsg{TaskID: id, ViewID: viewID, Attempt: attempt} }
		}
	case "r":
		if item, ok := v.live(v.selected); ok && !item.Recorded && item.Status == task.StatusRunning && item.AwaitingApproval {
			id, viewID := item.ID, v.viewID
			v.Suspend()
			v.pendingAction = true
			return func() tea.Msg { return TaskReviewApprovalMsg{TaskID: id, ViewID: viewID} }
		}
	case "t":
		// Read the worker's own conversation, not a summary of it. A worker
		// without a durable session has nothing to open.
		if item, ok := v.current(); ok && item.SessionID != "" {
			id, sessionID, viewID := item.ID, item.SessionID, v.viewID
			return func() tea.Msg {
				return TaskTranscriptMsg{TaskID: id, SessionID: sessionID, ViewID: viewID}
			}
		}
	case "m":
		// Steer a live worker with one line of ordinary owner input.
		if item, ok := v.live(v.selected); ok && !item.Recorded && item.Status == task.StatusRunning {
			v.steer = true
			v.steerInput = textinput.New()
			v.steerInput.Placeholder = "message to " + taskViewSubject(item)
			v.steerInput.Focus()
		}
	case "up", "k", "down", "j":
		if v.detail {
			if k == "up" || k == "k" {
				v.body.ScrollUp(1)
			} else {
				v.body.ScrollDown(1)
			}
			break
		}
		items := v.visibleItems()
		index := -1
		for i, item := range items {
			if item.ID == v.selected {
				index = i
			}
		}
		if k == "up" || k == "k" {
			index = max(0, index-1)
		} else {
			index = min(len(items)-1, index+1)
		}
		if index >= 0 && index < len(items) {
			v.selected = items[index].ID
			v.rebuild()
			v.ensureSelectionVisible()
		}
	case "pgup":
		v.body.PageUp()
	case "pgdown":
		v.body.PageDown()
	case "home":
		v.body.GotoTop()
	case "end":
		v.body.GotoBottom()
	}
	return nil
}

// handleSteerKey owns the keyboard while the one-line steer input is open:
// every other key belongs to the text field, and Enter hands the line to the
// app, which routes it through the same inbox the SendMessage tool uses.
func (v *TaskViewer) handleSteerKey(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc":
		v.steer, v.steerInput = false, textinput.Model{}
		return nil
	case "enter":
		text := strings.TrimSpace(v.steerInput.Value())
		item, ok := v.live(v.selected)
		if text == "" || !ok || item.Recorded || item.Status != task.StatusRunning {
			return nil
		}
		id, attempt, viewID := item.ID, item.Attempt, v.viewID
		v.steer, v.steerInput = false, textinput.Model{}
		v.Suspend()
		v.pendingAction = true
		return func() tea.Msg {
			return TaskSteerMsg{TaskID: id, Message: text, ViewID: viewID, Attempt: attempt}
		}
	}
	var cmd tea.Cmd
	v.steerInput, cmd = v.steerInput.Update(key)
	return cmd
}

// HandlePaste implements pasteHandler while the steer input is open; without it
// the app drops the paste rather than let it leak into the prompt behind.
func (v *TaskViewer) HandlePaste(content string) tea.Cmd {
	if !v.steer || content == "" {
		return nil
	}
	v.steerInput.SetValue(v.steerInput.Value() + strings.ReplaceAll(content, "\n", " "))
	return nil
}

func (v *TaskViewer) hint() string {
	if v.steer {
		return "enter send · esc cancel"
	}
	hint := "↑/↓ select · Enter details\ns stop · r approval · Esc close"
	if _, history := v.groupedItems(); !v.detail && len(history) > 0 {
		hint = "↑/↓ select · Enter · h history\ns stop · r approval · Esc close"
	}
	if v.detail {
		hint = "↑/↓ scroll · PgUp/PgDown\ns stop · r approval · Esc back"
	}
	// Only advertise the row's own keys where the width can hold them: a hint
	// that wraps mid-word is worse than an unlisted key.
	if v.width >= 56 {
		if item, ok := v.current(); ok && item.SessionID != "" {
			hint = strings.Replace(hint, "s stop", "t transcript · s stop", 1)
		}
		if item, ok := v.live(v.selected); ok && !item.Recorded && item.Status == task.StatusRunning {
			hint = strings.Replace(hint, "s stop", "m steer · s stop", 1)
		}
	}
	if item, ok := v.live(v.selected); ok && !item.Recorded && item.CanContinue && (item.Status == task.StatusFailed || item.Status == task.StatusCompleted) {
		label := "c ask main to continue"
		if v.width < 36 {
			label = "c continue"
		}
		hint = strings.ReplaceAll(hint, "s stop · r approval", label)
	}
	if item, ok := v.current(); ok && item.Recorded {
		hint = strings.ReplaceAll(hint, "s stop · r approval · ", "")
	}
	if v.confirm {
		hint = "←/→ choose · Enter confirm\nEsc back"
	}
	return ansi.Wrap(hint, max(1, v.width-1), " ")
}

func (v *TaskViewer) Render() string {
	if !v.active {
		return ""
	}
	rule := kit.DimStyle().Render(strings.Repeat("─", max(1, v.width-1)))
	if v.confirm {
		choice := "[Back]   Stop task"
		if v.confirmStop {
			choice = "Back   [Stop task]"
		}
		return strings.Join([]string{rule, ansi.Wrap("Stop task "+v.stopID+"?", max(1, v.width-1), " "), "Ends this task; no resume.", "", choice, rule, kit.HintLine(v.hint())}, "\n")
	}
	active, _ := v.groupedItems()
	title := kit.TruncateText(fmt.Sprintf("Tasks · %d active", len(active)), max(1, v.width-1))
	hintLine := kit.HintLine(v.hint())
	if v.steer {
		title = "Steer worker"
		hintLine = v.steerInput.View()
	}
	return strings.Join([]string{rule, kit.SelectorTitleStyle().Render(title), "", v.body.View(), rule, hintLine}, "\n")
}

// ── Row content ─────────────────────────────────────────────────────────────

// taskViewStage names the lifecycle in the roster's vocabulary rather than the
// backend's. "completed" is what the task record says; a finished worker whose
// session is still resumable is idle, which is the state a reader acts on.
// The inspector keeps the exact backend word.
func taskViewStage(item TaskViewItem) string {
	switch item.Status {
	case task.StatusRunning:
		if item.AwaitingApproval {
			return "awaiting approval"
		}
		return "running"
	case task.StatusCompleted, task.StatusFailed:
		if item.CanContinue {
			return "idle"
		}
	case task.StatusStopped, task.StatusKilled:
		return "stopped"
	}
	if item.Status == "" {
		return "unknown"
	}
	return string(item.Status)
}

// taskViewCells is the right-hand scan column: how far, on what, how much. Cells
// are dropped from the end while the row is too narrow, so the stage never
// disappears and the subject keeps a readable stub.
func taskViewCells(item TaskViewItem, now time.Time, width int) string {
	cells := []string{taskViewStage(item)}
	if age := taskViewAge(item, now); age != "" {
		cells = append(cells, age)
	}
	if item.Tokens > 0 {
		cells = append(cells, kit.FormatTokenCount(item.Tokens)+" tok")
	}
	if item.Steps > 0 {
		cells = append(cells, fmt.Sprintf("%d steps", item.Steps))
	}
	if item.Model != "" && width >= 72 {
		cells = append(cells, item.Model)
	}
	budget := max(8, width-14)
	for len(cells) > 1 && lipgloss.Width(strings.Join(cells, " · ")) > budget {
		cells = cells[:len(cells)-1]
	}
	return strings.Join(cells, " · ")
}

func taskViewRow(item TaskViewItem, now time.Time, width int) string {
	subject := taskViewSubject(item)
	meta := taskViewCells(item, now, width)
	room := width - lipgloss.Width(meta) - 2
	if meta == "" || room < 12 {
		return kit.TruncateText(subject, width)
	}
	subject = kit.TruncateText(subject, room)
	gap := max(1, width-lipgloss.Width(subject)-lipgloss.Width(meta))
	return subject + strings.Repeat(" ", gap) + meta
}

// taskViewAge measures how long the row has been the state it reports: a
// running worker ages from its start, a settled one from its end.
func taskViewAge(item TaskViewItem, now time.Time) string {
	if item.StartedAt.IsZero() {
		return ""
	}
	until := now
	if !item.EndedAt.IsZero() {
		until = item.EndedAt
	}
	if until.Before(item.StartedAt) {
		return ""
	}
	return taskViewSpan(until.Sub(item.StartedAt))
}

func taskViewSpan(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func taskViewUsage(item TaskViewItem) string {
	var parts []string
	if item.Steps > 0 {
		parts = append(parts, fmt.Sprintf("%d steps", item.Steps))
	}
	if item.Tokens > 0 {
		parts = append(parts, kit.FormatTokenCount(item.Tokens)+" tokens")
	}
	return strings.Join(parts, " · ")
}

// taskViewTiming says how long the work took and, once it settled, how long ago
// that was — the two facts a roster's age column folds together.
func taskViewTiming(item TaskViewItem, now time.Time) string {
	if item.StartedAt.IsZero() {
		return ""
	}
	if item.EndedAt.IsZero() {
		return "Elapsed: " + taskViewSpan(now.Sub(item.StartedAt))
	}
	timing := "Took: " + taskViewSpan(item.EndedAt.Sub(item.StartedAt))
	if ago := now.Sub(item.EndedAt); ago >= time.Second {
		timing += " · ended " + taskViewSpan(ago) + " ago"
	}
	return timing
}
