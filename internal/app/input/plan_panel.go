// Execution Panel: the overlay the agent opens when a plan is ready, when a
// step blocks, and when a plan finishes. It is the whole control surface for
// Plan Mode — every action here is a button, so the flow never needs a typed
// command. The panel renders a snapshot of plan.State and emits one message per
// action; the app owns the store and the agent.
package input

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/plan"
)

// PlanAction names one button on the panel.
type PlanAction string

const (
	PlanActionExecuteNew   PlanAction = "execute-new"
	PlanActionKeepExisting PlanAction = "keep-existing"
	PlanActionEdit         PlanAction = "edit"
	PlanActionCancel       PlanAction = "cancel"
	PlanActionPause        PlanAction = "pause"
	PlanActionResume       PlanAction = "resume"
	PlanActionSkipStep     PlanAction = "skip-step"
	PlanActionRetryStep    PlanAction = "retry-step"
	PlanActionRestartStep  PlanAction = "restart-step"
	PlanActionContinue     PlanAction = "continue"
	PlanActionClose        PlanAction = "close"
)

// PlanContext is the conversation the executor starts from. Planning is
// exploratory — long, full of dead ends — and none of that history is what a
// step needs, so the person decides how much of it carries over when the plan
// is approved.
type PlanContext string

const (
	// PlanContextKeep keeps the planning conversation: the executor sees
	// everything the planner did.
	PlanContextKeep PlanContext = "keep"
	// PlanContextCompact summarizes the planning conversation first.
	PlanContextCompact PlanContext = "compact"
	// PlanContextFresh starts a new session that carries only the plan.
	PlanContextFresh PlanContext = "fresh"
)

// planContexts lists the choices in cycle order.
var planContexts = []PlanContext{PlanContextKeep, PlanContextCompact, PlanContextFresh}

// PlanContextDescription explains one choice in the panel's own words. The
// panel is the only place the mode is set, so this is the whole explanation.
func PlanContextDescription(c PlanContext) string {
	switch c {
	case PlanContextCompact:
		return "summarize this conversation first"
	case PlanContextFresh:
		return "start a new session from the plan"
	}
	return "planning history carries over"
}

// PlanActionMsg asks the app to apply one panel action to the plan store.
type PlanActionMsg struct {
	Action PlanAction
	// Context is the conversation the first step starts from; only Execute new
	// plan reads it.
	Context PlanContext
}

// PlanEditSavedMsg carries an edited plan back to the app, which persists it
// in the session state. The panel merges the edit so the plan keeps its
// identity, step IDs, and progress journal.
type PlanEditSavedMsg struct {
	Plan   plan.Plan
	Status string // what the app did with it; shown under the plan
}

// PlanPanel is the Execution Panel overlay.
type PlanPanel struct {
	active        bool
	width, height int
	state         plan.State
	cursor        int
	status        string
	// context is where execution starts from. It survives a close, so the
	// choice a person made once is what the next plan uses too.
	context PlanContext

	editing bool
	editor  textarea.Model
	draft   plan.Plan // the plan being edited; its identity survives the edit

	// Bubble Tea re-renders the panel on every spinner tick and window event,
	// so the body is memoized. It depends only on the snapshot, the cursor,
	// the geometry, and the status line, and every one of those marks the
	// cache dirty.
	cachedBody    string
	cachedBodySet bool
	cachedView    string
	cachedViewSet bool
}

// invalidate drops the memoized render. Called from anything the render reads.
func (p *PlanPanel) invalidate() {
	p.cachedBodySet = false
	p.cachedViewSet = false
}

// NewPlanPanel builds an inactive panel.
func NewPlanPanel() PlanPanel {
	return PlanPanel{editor: newChromelessTextarea()}
}

// Enter opens the panel on a snapshot of the session's plan state.
func (p *PlanPanel) Enter(state plan.State, width, height int) {
	p.state = state.Clone()
	p.active = true
	p.editing = false
	p.status = ""
	p.cursor = 0
	if p.context == "" {
		p.context = PlanContextKeep
	}
	p.Resize(width, height)
	p.invalidate()
}

// Refresh updates the snapshot of an open panel without stealing the cursor.
// Called after every action so the panel shows the state the store now holds.
func (p *PlanPanel) Refresh(state plan.State) {
	if !p.active {
		return
	}
	p.state = state.Clone()
	if n := len(p.actions()); p.cursor >= n {
		p.cursor = max(0, n-1)
	}
	p.invalidate()
}

// Close dismisses the panel. The plan and its progress are untouched.
func (p *PlanPanel) Close() {
	p.active = false
	p.editing = false
	p.status = ""
	p.invalidate()
}

// IsActive implements overlayPanel.
func (p *PlanPanel) IsActive() bool { return p.active }

// Resize implements resizableOverlay.
func (p *PlanPanel) Resize(width, height int) {
	p.width, p.height = max(1, width), max(1, height)
	if p.editing {
		p.editor.SetWidth(max(20, p.innerWidth()))
		p.editor.SetHeight(max(6, p.height-14))
	}
	p.invalidate()
}

// HandleKeypress implements overlayPanel.
func (p *PlanPanel) HandleKeypress(msg tea.KeyMsg) tea.Cmd {
	if !p.active {
		return nil
	}
	if p.editing {
		return p.handleEditorKey(msg)
	}
	actions := p.actions()
	switch msg.String() {
	case "esc", "q":
		p.Close()
		return nil
	case "up", "k", "left", "h":
		if len(actions) > 0 {
			p.cursor = (p.cursor - 1 + len(actions)) % len(actions)
			p.invalidate()
		}
		return nil
	case "down", "j", "right", "l":
		if len(actions) > 0 {
			p.cursor = (p.cursor + 1) % len(actions)
			p.invalidate()
		}
		return nil
	case "e":
		if p.canEdit() {
			p.openEditor()
		}
		return nil
	case "c":
		if p.offersExecute() {
			p.cycleContext()
		}
		return nil
	case "enter", "space":
		if len(actions) == 0 {
			return nil
		}
		action := actions[p.cursor].Action
		if action == PlanActionEdit {
			p.openEditor()
			return nil
		}
		return func() tea.Msg { return PlanActionMsg{Action: action, Context: p.context} }
	}
	return nil
}

// offersExecute reports whether the panel is about to run a plan, which is the
// only state where the conversation context is a decision to make.
func (p *PlanPanel) offersExecute() bool {
	for _, action := range p.actions() {
		if action.Action == PlanActionExecuteNew {
			return true
		}
	}
	return false
}

// cycleContext steps the exec context to its next choice, wrapping at the end.
func (p *PlanPanel) cycleContext() {
	at := 0
	for i, c := range planContexts {
		if c == p.context {
			at = i
			break
		}
	}
	p.context = planContexts[(at+1)%len(planContexts)]
	p.invalidate()
}

// Context returns the exec context the panel would send with Execute new plan.
func (p *PlanPanel) Context() PlanContext { return p.context }

// HandlePaste implements pasteHandler so a pasted plan body lands in the
// editor instead of leaking into the prompt textarea behind the panel.
func (p *PlanPanel) HandlePaste(content string) tea.Cmd {
	if !p.active || !p.editing {
		return nil
	}
	p.editor.InsertString(strings.ReplaceAll(content, "\r\n", "\n"))
	return nil
}

// Actions returns the labels of the current buttons, for tests and hints.
func (p *PlanPanel) Actions() []string {
	actions := p.actions()
	labels := make([]string, len(actions))
	for i, a := range actions {
		labels[i] = a.label
	}
	return labels
}

// ── Actions ─────────────────────────────────────────────────────────────

type planButton struct {
	Action PlanAction
	label  string
}

// actions is the one place that decides which buttons are legal right now, so
// the panel can never offer an action the store would reject.
func (p *PlanPanel) actions() []planButton {
	active, hasActive := p.activePlan()
	draft, hasDraft := p.draftPlan()
	conflict := hasDraft && hasActive && draft.ID != active.ID && !active.Terminal()

	if conflict {
		return []planButton{
			{PlanActionExecuteNew, "Execute new plan"},
			{PlanActionKeepExisting, "Keep existing context"},
			{PlanActionEdit, "Edit plan"},
			{PlanActionCancel, "Cancel"},
			{PlanActionClose, "Close"},
		}
	}
	if hasDraft {
		actions := []planButton{
			{PlanActionExecuteNew, "Execute new plan"},
			{PlanActionEdit, "Edit plan"},
			{PlanActionCancel, "Cancel"},
			{PlanActionClose, "Close"},
		}
		return actions
	}
	if !hasActive {
		return []planButton{{PlanActionClose, "Close"}}
	}
	if active.Terminal() {
		return []planButton{{PlanActionClose, "Close"}}
	}
	step, hasStep := active.CurrentStep()
	switch {
	case active.AwaitingStepChoice:
		return []planButton{
			{PlanActionContinue, "Continue step"},
			{PlanActionRestartStep, "Restart step"},
			{PlanActionSkipStep, "Skip step"},
			{PlanActionCancel, "Cancel"},
			{PlanActionClose, "Close"},
		}
	case hasStep && step.Status == plan.StepBlocked:
		return []planButton{
			{PlanActionRetryStep, "Retry step"},
			{PlanActionRestartStep, "Restart step"},
			{PlanActionSkipStep, "Skip step"},
			{PlanActionCancel, "Cancel"},
			{PlanActionClose, "Close"},
		}
	case active.Paused:
		return []planButton{
			{PlanActionResume, "Resume"},
			{PlanActionSkipStep, "Skip step"},
			{PlanActionCancel, "Cancel"},
			{PlanActionClose, "Close"},
		}
	case active.Status == plan.StatusPendingApproval && p.state.DraftID == "":
		return []planButton{
			{PlanActionExecuteNew, "Execute new plan"},
			{PlanActionEdit, "Edit plan"},
			{PlanActionCancel, "Cancel"},
			{PlanActionClose, "Close"},
		}
	default:
		return []planButton{
			{PlanActionPause, "Pause"},
			{PlanActionSkipStep, "Skip step"},
			{PlanActionCancel, "Cancel"},
			{PlanActionClose, "Close"},
		}
	}
}

func (p *PlanPanel) activePlan() (plan.Plan, bool) {
	return planByID(p.state.Plans, p.state.ActiveID)
}

func (p *PlanPanel) draftPlan() (plan.Plan, bool) {
	return planByID(p.state.Plans, p.state.DraftID)
}

func planByID(plans []plan.Plan, id string) (plan.Plan, bool) {
	if id == "" {
		return plan.Plan{}, false
	}
	for i := range plans {
		if plans[i].ID == id {
			return plans[i], true
		}
	}
	return plan.Plan{}, false
}

// focused is the plan the panel describes: the draft while a decision is
// pending, otherwise the active plan.
func (p *PlanPanel) focused() (plan.Plan, bool) {
	if draft, ok := p.draftPlan(); ok {
		return draft, true
	}
	return p.activePlan()
}

// canEdit reports whether the focused plan is still pre-execution.
func (p *PlanPanel) canEdit() bool {
	focused, ok := p.focused()
	if !ok {
		return false
	}
	switch focused.Status {
	case "", plan.StatusDraft, plan.StatusPendingApproval:
		return true
	}
	return false
}

func (p *PlanPanel) openEditor() {
	focused, ok := p.focused()
	if !ok {
		return
	}
	p.draft = focused
	p.editor.SetWidth(max(20, p.innerWidth()))
	p.editor.SetHeight(max(6, p.height-14))
	p.editor.SetValue(plan.FormatText(focused))
	p.editor.CursorStart()
	p.editor.Focus()
	p.editing = true
	p.setStatus("")
}

// setStatus writes the line under the plan and drops the memoized render.
func (p *PlanPanel) setStatus(status string) {
	p.status = status
	p.invalidate()
}

func (p *PlanPanel) handleEditorKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.editing = false
		p.editor.Blur()
		p.invalidate()
		return nil
	case "ctrl+s":
		edited, err := plan.ParseText(p.editor.Value())
		if err != nil {
			p.setStatus("Not saved: " + err.Error())
			return nil
		}
		merged := plan.MergeEdit(p.draft, edited)
		p.editing = false
		p.editor.Blur()
		p.invalidate()
		return func() tea.Msg { return PlanEditSavedMsg{Plan: merged} }
	}
	var cmd tea.Cmd
	p.editor, cmd = p.editor.Update(msg)
	return cmd
}

// ── Render ──────────────────────────────────────────────────────────────

// Docked reports whether the panel renders as a card above the composer
// instead of taking the alternate screen. It is the panel's default: a plan is
// reviewed against the conversation that produced it, and a full screen hides
// exactly that. Editing is the exception — the plan's text is read and
// rewritten there, so the editor keeps the whole screen.
func (p *PlanPanel) Docked() bool { return p.active && !p.editing }

// DrawsOwnFrame reports that the docked card carries its own rounded border, so
// the docked layout must not also draw the full-width separator above it — two
// rules one row apart is exactly the decoration this panel does not need.
func (p *PlanPanel) DrawsOwnFrame() bool { return true }

// Render implements overlayPanel.
func (p *PlanPanel) Render() string {
	if !p.active {
		return ""
	}
	if p.editing {
		return panelFrame(p.width, p.height, p.innerWidth(),
			p.header("Edit plan"), p.editor.View(),
			kit.HintLine(keycap("ctrl+s")+" save", keycap("esc")+" discard"))
	}
	if p.cachedViewSet {
		return p.cachedView
	}
	p.cachedView = p.card()
	p.cachedViewSet = true
	return p.cachedView
}

// card renders the docked panel: the same header, body and hints the full
// screen version draws, framed in the card and placed where it stands — no
// vertical centering and no terminal-height box, so the conversation above it
// stays on screen.
func (p *PlanPanel) card() string {
	w := p.innerWidth()
	rule := kit.Rule(w)
	col := lipgloss.NewStyle().Width(w).Render(
		p.header("") + "\n" + rule + "\n\n" + p.body() + "\n\n" + p.hint())
	return apCardStyle.Render(col)
}

// cardChrome is every card row that is not content: the border and its
// padding (4), the header and its rule (2), the blank row under the rule (1),
// the blank row before the hint (1), and the hint itself (1).
const cardChrome = 9

// dockBudget bounds the whole card: at most three fifths of the terminal, and
// never past the rows the dock separator leaves, so the conversation the plan
// was written about keeps most of the screen. A short terminal still gets the
// chrome plus a row or two — a card that cannot show its buttons is not a
// smaller card, it is a broken one.
func (p *PlanPanel) dockBudget() int {
	return max(min(12, p.height-2), min(p.height*3/5, p.height-4))
}

// bodyRows is the row budget for the card's content.
func (p *PlanPanel) bodyRows() int {
	return max(1, p.dockBudget()-cardChrome)
}

// innerWidth is the content width of the card. The rounded border and its
// padding add six columns around it, so this keeps the whole card at
// min(width-4, 96) and leaves the frame's margins alone.
func (p *PlanPanel) innerWidth() int {
	return max(20, min(p.width-10, 90))
}

func (p *PlanPanel) header(sub string) string {
	left := apTitleGlyphStyle.Render(kit.CurrentSymbols.ThinkingStar+" ") + apTitleStyle.Render("Execution Panel")
	if sub != "" {
		left += apBreadcrumbDimStyle.Render("  ›  ") + apBreadcrumbSubStyle.Render(sub)
	}
	return left
}

// body renders the card content once per snapshot; Render reuses it until
// something the content depends on changes.
func (p *PlanPanel) body() string {
	if p.cachedBodySet {
		return p.cachedBody
	}
	p.cachedBody = p.renderBody()
	p.cachedBodySet = true
	return p.cachedBody
}

func (p *PlanPanel) renderBody() string {
	var b strings.Builder
	focused, ok := p.focused()
	if !ok {
		b.WriteString(apSummaryStyle.Render("No plan in this session."))
		return b.String()
	}
	active, hasActive := p.activePlan()
	conflict := p.state.DraftID != "" && hasActive && active.ID != focused.ID && !active.Terminal()

	p.titleBlock(&b, focused)
	if conflict {
		b.WriteString("\n")
		b.WriteString(planWarnStyle.Render(fmt.Sprintf("A new plan is ready while %q is still unfinished. Choose one:", active.Title)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(p.steps(focused))
	p.currentStep(&b, focused)

	// Risks, decisions and open questions are what a person reads *before*
	// approving. Once the executor is running they are history; the steps and
	// the current step are the only things worth the rows.
	if p.canEdit() {
		p.list(&b, "Open questions", focused.OpenQuestions)
		p.list(&b, "Risks", focused.Risks)
		decisions := make([]string, 0, len(focused.Decisions))
		for _, d := range focused.Decisions {
			decisions = append(decisions, d.Text)
		}
		p.list(&b, "Decisions", decisions)
	}

	b.WriteString("\n")
	b.WriteString(p.contextLine())
	b.WriteString(p.buttons())
	if p.status != "" {
		b.WriteString("\n\n")
		b.WriteString(apSummaryStyle.Render(p.status))
	}
	return p.clip(b.String())
}

func (p *PlanPanel) field(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	const width = 11
	fmt.Fprintf(b, "%s %s\n", planLabelStyle.Render(apPad(label, width)), value)
}

// titleBlock is the plan's identity: the title in the panel's title style with
// its status right-aligned on the same row, and the goal muted underneath. A
// label column in front of the title only pushed the one line a person always
// reads two columns to the right, so it is gone.
func (p *PlanPanel) titleBlock(b *strings.Builder, pl plan.Plan) {
	status := statusLine(pl)
	title := apTitleStyle.Render(pl.Title)
	if lipgloss.Width(title)+lipgloss.Width(status)+2 > p.innerWidth() {
		b.WriteString(title + "\n")
		b.WriteString(apSummaryStyle.Render(status) + "\n")
	} else {
		gap := max(p.innerWidth()-lipgloss.Width(title)-lipgloss.Width(status)-1, 1)
		b.WriteString(title + strings.Repeat(" ", gap) + apSummaryStyle.Render(status) + "\n")
	}
	if goal := strings.TrimSpace(pl.Goal); goal != "" {
		b.WriteString(apSummaryStyle.Render(kit.TruncateText(goal, p.innerWidth())) + "\n")
	}
}

// currentStep describes the step in flight: what it is, what proves it, and
// what the report said. Files and evidence collapse to one line each — the
// panel is a control surface, and the full record lives in the transcript.
func (p *PlanPanel) currentStep(b *strings.Builder, pl plan.Plan) {
	step, ok := pl.CurrentStep()
	if !ok {
		return
	}
	b.WriteString("\n")
	b.WriteString(planSectionStyle.Render("NOW") + "\n")
	b.WriteString("  " + apTitleStyle.Render(step.Title) + "\n")
	line := func(label, value string, style lipgloss.Style) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fmt.Fprintf(b, "  %s %s\n", planLabelStyle.Render(apPad(label, 9)), style.Render(value))
	}
	line("objective", kit.TruncateText(step.Objective, p.innerWidth()-12), apSummaryStyle)
	for i, criterion := range step.Verification {
		if i == 0 {
			line("verify", kit.TruncateText(criterion, p.innerWidth()-12), apSummaryStyle)
			continue
		}
		b.WriteString("  " + strings.Repeat(" ", 10) + kit.TruncateText(criterion, p.innerWidth()-12) + "\n")
	}
	if step.BlockReason != "" {
		line("blocked", kit.TruncateText(step.BlockReason, p.innerWidth()-12), planWarnStyle)
	}
	line("result", kit.TruncateText(step.Result, p.innerWidth()-12), apSummaryStyle)
	if len(step.Files) > 0 {
		line("files", kit.TruncateText(strings.Join(step.Files, ", "), p.innerWidth()-12), apSummaryStyle)
	}
	if len(step.Evidence) > 0 {
		shown := kit.TruncateText(step.Evidence[0], p.innerWidth()-20)
		if extra := len(step.Evidence) - 1; extra > 0 {
			shown += fmt.Sprintf(" (+%d more)", extra)
		}
		line("evidence", shown, apSummaryStyle)
	}
}

// contextLine names the conversation the first step starts from. It only
// appears while Execute new plan is on offer: once execution started the
// choice is spent. The explanation is dropped, not wrapped, when the card is
// too narrow for it — the choice itself is the control, and a sentence broken
// across two rows under it reads as noise.
func (p *PlanPanel) contextLine() string {
	if !p.offersExecute() {
		return ""
	}
	value := planLabelStyle.Render(apPad("Context", 11))
	choice := planAccentStyle.Render("‹ " + string(p.context) + " ›")
	line := value + " " + choice
	desc := PlanContextDescription(p.context)
	if lipgloss.Width(line)+2+lipgloss.Width(desc) <= p.innerWidth() {
		line += "  " + apSummaryStyle.Render(desc)
	}
	return line + "\n\n"
}

func (p *PlanPanel) list(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n", planSectionStyle.Render(strings.ToUpper(label)))
	for _, item := range items {
		fmt.Fprintf(b, "  %s %s\n", planBulletStyle.Render("·"),
			kit.TruncateText(item, max(20, p.innerWidth()-6)))
	}
}

func (p *PlanPanel) steps(pl plan.Plan) string {
	if len(pl.Steps) == 0 {
		return apSummaryStyle.Render("No steps yet.") + "\n"
	}
	var b strings.Builder
	completed, handled, blocked, total := pl.Counts()
	head := fmt.Sprintf("STEPS  %d/%d done", completed, total)
	if handled > completed {
		head += fmt.Sprintf(" · %d handled", handled)
	}
	if blocked > 0 {
		head += fmt.Sprintf(" · %d blocked", blocked)
	}
	b.WriteString(planSectionStyle.Render(head))
	b.WriteString("\n")
	for i, step := range pl.Steps {
		if i >= p.stepRows() {
			fmt.Fprintf(&b, "  %s\n", apSummaryStyle.Render(fmt.Sprintf("… %d more", len(pl.Steps)-i)))
			break
		}
		current := i == pl.StepIndex && !pl.Terminal()
		icon, style := stepMark(step, current)
		line := fmt.Sprintf("  %s %d. %s", icon, i+1, step.Title)
		if current {
			line = style.Render(strings.TrimRight(line, " "))
		} else {
			line = style.Render(line)
		}
		if step.Status == plan.StepBlocked && step.BlockReason != "" {
			line += apSummaryStyle.Render(" — " + kit.TruncateText(step.BlockReason, max(16, p.innerWidth()-lipgloss.Width(line)-6)))
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// stepRows bounds the step list so the buttons never scroll off the card: the
// title and its goal, the STEPS heading, the marker line and the "…" the clip
// may add all need room, and a plan with more steps than fit finishes its list
// with "… N more" rather than losing the tail silently.
func (p *PlanPanel) stepRows() int {
	return max(2, min(p.bodyRows()-10, 8))
}

func stepMark(step plan.Step, current bool) (string, lipgloss.Style) {
	if current {
		switch step.Status {
		case plan.StepBlocked:
			return "!", planWarnStyle
		case plan.StepInProgress:
			return kit.CurrentSymbols.Cursor, planAccentStyle
		}
		return kit.CurrentSymbols.Cursor, planAccentStyle
	}
	switch step.Status {
	case plan.StepCompleted:
		return kit.CurrentSymbols.Success, planDoneStyle
	case plan.StepSkipped:
		return "-", apSummaryStyle
	case plan.StepBlocked:
		return "!", planWarnStyle
	case plan.StepInProgress:
		return kit.CurrentSymbols.Cursor, planAccentStyle
	default:
		return "·", apSummaryStyle
	}
}

func (p *PlanPanel) buttons() string {
	actions := p.actions()
	parts := make([]string, 0, len(actions))
	for i, action := range actions {
		label := action.label
		if i == p.cursor {
			parts = append(parts, planButtonActiveStyle.Render(" "+label+" "))
			continue
		}
		parts = append(parts, planButtonStyle.Render(" "+label+" "))
	}
	return strings.Join(parts, "  ")
}

// hint is the key legend under the card. It is one row by contract — the card
// budget counts it — so on a narrow card the words go before the keys do, and
// the meanings are what a person loses, never the keys themselves.
func (p *PlanPanel) hint() string {
	keys := []string{keycap("←→"), keycap("enter"), keycap("e")}
	meanings := []string{" choose", " confirm", " edit"}
	if p.offersExecute() {
		keys = append(keys, keycap("c"))
		meanings = append(meanings, " context")
	}
	keys = append(keys, keycap("esc"))
	meanings = append(meanings, " close")

	labelled := make([]string, len(keys))
	for i := range keys {
		labelled[i] = keys[i] + meanings[i]
	}
	if line := kit.HintLine(labelled...); lipgloss.Width(line) <= p.innerWidth() {
		return line
	}
	return kit.TruncateText(kit.HintLine(keys...), p.innerWidth())
}

// clip bounds the card body so a long plan never pushes the buttons off
// screen. The tail is what a person must be able to press, so the buttons are
// rendered last and the clip keeps the last rows. The elision marker takes a
// row of its own, so the kept prefix gives one up to make room for it: the
// body never exceeds the budget, chrome and hint included in the card.
func (p *PlanPanel) clip(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	limit := p.bodyRows()
	if len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	tail := min(len(lines), 3) // the blank line, the buttons, and any status
	if limit <= tail+1 {
		return strings.Join(lines[len(lines)-limit:], "\n")
	}
	kept := append([]string{}, lines[:limit-tail-1]...)
	kept = append(kept, apSummaryStyle.Render("…"))
	kept = append(kept, lines[len(lines)-tail:]...)
	return strings.Join(kept, "\n")
}

func statusLine(pl plan.Plan) string {
	status := string(pl.Status)
	if status == "" {
		status = string(plan.StatusDraft)
	}
	completed, handled, blocked, total := pl.Counts()
	line := fmt.Sprintf("%s · %d/%d done", status, completed, total)
	if handled > completed {
		line += fmt.Sprintf(" (%d handled)", handled)
	}
	if blocked > 0 {
		line += fmt.Sprintf(" · %d blocked", blocked)
	}
	if pl.Paused {
		line += " · paused"
	}
	if pl.AwaitingStepChoice {
		line += " · waiting for your call on the current step"
	}
	return line
}

var (
	planLabelStyle        = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Bold(true)
	planSectionStyle      = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Bold(true)
	planBulletStyle       = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	planAccentStyle       = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Accent).Bold(true)
	planDoneStyle         = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
	planWarnStyle         = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
	planButtonStyle       = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	planButtonActiveStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Accent).Bold(true).Reverse(true)
)
