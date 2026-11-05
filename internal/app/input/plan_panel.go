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

// PlanActionMsg asks the app to apply one panel action to the plan store.
type PlanActionMsg struct{ Action PlanAction }

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

	editing bool
	editor  textarea.Model
	draft   plan.Plan // the plan being edited; its identity survives the edit

	// The panel is a fullscreen overlay: Bubble Tea re-renders it on every
	// spinner tick and window event, so the body is memoized. It depends only
	// on the snapshot, the cursor, the geometry, and the status line, and
	// every one of those marks the cache dirty.
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
	case "enter", "space":
		if len(actions) == 0 {
			return nil
		}
		action := actions[p.cursor].Action
		if action == PlanActionEdit {
			p.openEditor()
			return nil
		}
		return func() tea.Msg { return PlanActionMsg{Action: action} }
	}
	return nil
}

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
	p.cachedView = panelFrame(p.width, p.height, p.innerWidth(), p.header(""), p.body(), p.hint())
	p.cachedViewSet = true
	return p.cachedView
}

func (p *PlanPanel) innerWidth() int {
	return max(40, min(p.width-12, 96))
}

func (p *PlanPanel) header(sub string) string {
	left := apTitleGlyphStyle.Render("✦ ") + apTitleStyle.Render("Execution Panel")
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

	p.field(&b, "Plan", focused.Title)
	if focused.Goal != "" {
		p.field(&b, "Goal", focused.Goal)
	}
	p.field(&b, "Status", statusLine(focused))
	if conflict {
		b.WriteString(planWarnStyle.Render(fmt.Sprintf("A new plan is ready while %q is still unfinished. Choose one:", active.Title)))
		b.WriteString("\n\n")
	}
	b.WriteString("\n")
	b.WriteString(p.steps(focused))

	if step, ok := focused.CurrentStep(); ok {
		b.WriteString("\n")
		p.field(&b, "Now", step.Title)
		if step.Objective != "" {
			p.field(&b, "Objective", step.Objective)
		}
		for i, criterion := range step.Verification {
			label := "Verify"
			if i > 0 {
				label = ""
			}
			p.field(&b, label, criterion)
		}
		if step.BlockReason != "" {
			p.field(&b, "Blocked", planWarnStyle.Render(step.BlockReason))
		}
		if step.Result != "" {
			p.field(&b, "Result", kit.TruncateText(step.Result, p.innerWidth()-12))
		}
		if len(step.Files) > 0 {
			p.field(&b, "Files", kit.TruncateText(strings.Join(step.Files, ", "), p.innerWidth()-12))
		}
		for i, evidence := range step.Evidence {
			label := "Evidence"
			if i > 0 {
				label = ""
			}
			p.field(&b, label, kit.TruncateText(evidence, p.innerWidth()-12))
		}
	}
	p.refused(&b, focused)
	p.list(&b, "Open questions", focused.OpenQuestions)
	p.list(&b, "Risks", focused.Risks)
	decisions := make([]string, 0, len(focused.Decisions))
	for _, d := range focused.Decisions {
		decisions = append(decisions, d.Text)
	}
	p.list(&b, "Decisions", decisions)

	b.WriteString("\n")
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

// refused renders completions an independent review refused. The step keeps its
// completion — the work was done — and this section is where the refusal reads:
// the severity, the review that found it, and where its findings live.
func (p *PlanPanel) refused(b *strings.Builder, pl plan.Plan) {
	refused := refusedSteps(pl)
	if len(refused) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n", planSectionStyle.Render("NEEDS REVIEW"))
	for _, step := range refused {
		line := fmt.Sprintf("  %s %s %s", planWarnStyle.Render("!"),
			planWarnStyle.Render(step.NeedsReview),
			kit.TruncateText(step.Title, max(16, p.innerWidth()-24)))
		if step.AdvisorReviewID != "" {
			line += apSummaryStyle.Render(" · " + step.AdvisorReviewID)
		}
		line += apSummaryStyle.Render(" · open /advisor")
		b.WriteString(line + "\n")
	}
}

// refusedSteps returns the completed steps whose result a review refused, oldest
// first.
func refusedSteps(pl plan.Plan) []plan.Step {
	var out []plan.Step
	for _, step := range pl.Steps {
		if step.NeedsReview != "" {
			out = append(out, step)
		}
	}
	return out
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
	if refused := len(refusedSteps(pl)); refused > 0 {
		head += fmt.Sprintf(" · %d needs review", refused)
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
		if step.NeedsReview != "" {
			line += planWarnStyle.Render(" — needs review " + step.NeedsReview)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// stepRows bounds the step list so the buttons never scroll off the card.
func (p *PlanPanel) stepRows() int {
	return max(3, p.height-24)
}

func stepMark(step plan.Step, current bool) (string, lipgloss.Style) {
	// A refused completion is not a clean one: it gets the warning mark whether
	// or not it is the step in focus.
	if step.NeedsReview != "" {
		return "!", planWarnStyle
	}
	if current {
		switch step.Status {
		case plan.StepBlocked:
			return "!", planWarnStyle
		case plan.StepInProgress:
			return "▸", planAccentStyle
		}
		return "▸", planAccentStyle
	}
	switch step.Status {
	case plan.StepCompleted:
		return "✓", planDoneStyle
	case plan.StepSkipped:
		return "-", apSummaryStyle
	case plan.StepBlocked:
		return "!", planWarnStyle
	case plan.StepInProgress:
		return "▸", planAccentStyle
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

func (p *PlanPanel) hint() string {
	return kit.HintLine(keycap("←→")+" choose", keycap("enter")+" confirm", keycap("e")+" edit", keycap("esc")+" close")
}

// clip bounds the card body to the terminal so a long plan never pushes the
// buttons off screen. The tail is what a person must be able to press, so the
// buttons are rendered last and the clip keeps the last rows.
func (p *PlanPanel) clip(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	limit := max(6, p.height-12)
	if len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	tail := min(len(lines), 3) // the blank line, the buttons, and any status
	kept := append([]string{}, lines[:limit-tail]...)
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
	if refused := refusedSteps(pl); len(refused) > 0 {
		line += " · needs review " + refused[0].NeedsReview
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
