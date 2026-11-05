// Advisor Panel: the overlay behind /advisor. It shows the session's latest
// review — its verdict, the work it inspected, and every finding with the
// evidence behind it — and turns one finding into an action: fix, dismiss,
// defer, follow up, re-run the review, close.
//
// The panel owns no state but its own view: it renders a snapshot the app
// hands it and emits one message per action, exactly like the Execution Panel.
// The app owns the review store, the transcript and the prompt, so a dismissal
// is recorded in one place and a re-run can never overwrite the review it
// supersedes.
package input

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/app/kit"
)

// AdvisorAction names one action on the panel.
type AdvisorAction string

const (
	AdvisorActionFix      AdvisorAction = "fix"
	AdvisorActionDismiss  AdvisorAction = "dismiss"
	AdvisorActionDefer    AdvisorAction = "defer"
	AdvisorActionFollowUp AdvisorAction = "follow-up"
	AdvisorActionRerun    AdvisorAction = "rerun"
	AdvisorActionClose    AdvisorAction = "close"
)

// AdvisorActionMsg asks the app to apply one panel action. Dismiss carries the
// reason the person typed; the panel refuses to emit one without it.
type AdvisorActionMsg struct {
	Action    AdvisorAction
	ReviewID  string
	FindingID string
	Note      string
}

// AdvisorView is one snapshot of the review layer: the session's history, the
// live status, and how the merge policy reads the latest merge review. The
// panel renders it and never reaches back into the app for more.
type AdvisorView struct {
	State   advisor.State
	Running bool
	Err     string
	// Merge is the recommendation for a merge-kind review, resolved by the app
	// from the session's policy. Empty for every other kind of review.
	Merge advisor.Recommendation
}

// AdvisorPanel is the findings overlay.
type AdvisorPanel struct {
	active        bool
	width, height int
	view          AdvisorView

	// review is the review on display. It starts as the latest one and keeps
	// its identity across refreshes, so a decision does not scroll the panel
	// to a different review.
	review    advisor.Review
	hasReview bool

	// Filters. Empty means "all" for each, so the zero panel shows everything.
	severity advisor.Severity
	status   advisor.FindingStatus
	file     string

	cursor int
	detail bool

	dismissing   bool
	reason       textarea.Model
	statusLine   string
	cachedBody   string
	cacheBodySet bool
	cachedView   string
	cacheViewSet bool
}

// NewAdvisorPanel builds an inactive panel.
func NewAdvisorPanel() AdvisorPanel { return AdvisorPanel{} }

// Enter opens the panel on a snapshot of the session's review history.
func (p *AdvisorPanel) Enter(view AdvisorView, width, height int) {
	p.view = view
	p.active = true
	p.detail = false
	p.dismissing = false
	p.statusLine = ""
	p.severity, p.status, p.file = "", "", ""
	p.cursor = 0
	p.hasReview = false
	p.adopt(view.State)
	p.Resize(width, height)
	p.invalidate()
}

// Refresh replaces the snapshot of an open panel without losing the review the
// person is reading, their filters, or the finding they selected.
func (p *AdvisorPanel) Refresh(view AdvisorView) {
	if !p.active {
		return
	}
	p.view = view
	p.adopt(view.State)
	p.invalidate()
}

// Close dismisses the panel. Nothing it showed is changed.
func (p *AdvisorPanel) Close() {
	p.active = false
	p.detail = false
	p.dismissing = false
	p.statusLine = ""
	p.invalidate()
}

// IsActive implements overlayPanel.
func (p *AdvisorPanel) IsActive() bool { return p.active }

// Resize implements resizableOverlay.
func (p *AdvisorPanel) Resize(width, height int) {
	p.width, p.height = max(1, width), max(1, height)
	if p.dismissing {
		p.reason.SetWidth(max(20, p.innerWidth()-6))
		p.reason.SetHeight(3)
	}
	p.invalidate()
}

// HandleKeypress implements overlayPanel.
func (p *AdvisorPanel) HandleKeypress(msg tea.KeyMsg) tea.Cmd {
	if !p.active {
		return nil
	}
	if p.dismissing {
		return p.handleDismissKey(msg)
	}
	switch msg.String() {
	case "esc", "q":
		if p.detail {
			p.detail = false
			p.invalidate()
			return nil
		}
		p.Close()
		return nil
	case "up", "k":
		if p.detail {
			p.scrollDetail(-1)
			return nil
		}
		p.moveCursor(-1)
		return nil
	case "down", "j":
		if p.detail {
			p.scrollDetail(1)
			return nil
		}
		p.moveCursor(1)
		return nil
	case "enter":
		if _, ok := p.selected(); ok && !p.detail {
			p.detail = true
			p.invalidate()
		}
		return nil
	case "s":
		p.severity = nextSeverity(p.severity)
		p.cursor = 0
		p.invalidate()
		return nil
	case "t":
		p.status = nextFindingStatus(p.status)
		p.cursor = 0
		p.invalidate()
		return nil
	case "f":
		p.file = p.nextFile(p.file)
		p.cursor = 0
		p.invalidate()
		return nil
	case "x", "d", "e", "u", "r":
		return p.actionFor(msg.String())
	}
	return nil
}

// HandlePaste implements pasteHandler so a pasted dismissal reason lands in the
// panel's editor instead of the prompt textarea behind it.
func (p *AdvisorPanel) HandlePaste(content string) tea.Cmd {
	if !p.active || !p.dismissing {
		return nil
	}
	p.reason.InsertString(strings.ReplaceAll(content, "\r\n", "\n"))
	p.invalidate()
	return nil
}

// Actions returns the labels of the actions available now, for tests and hints.
func (p *AdvisorPanel) Actions() []string {
	actions := p.actions()
	labels := make([]string, len(actions))
	for i, action := range actions {
		labels[i] = action.label
	}
	return labels
}

// ── Actions ─────────────────────────────────────────────────────────────

type advisorButton struct {
	action AdvisorAction
	key    string
	label  string
}

// actions lists what the person can do in the current state. A finding-scoped
// action only appears while a finding is selected, so the panel can never offer
// a decision about nothing.
func (p *AdvisorPanel) actions() []advisorButton {
	if _, ok := p.selected(); !ok {
		return []advisorButton{
			{AdvisorActionRerun, "r", "Re-run review"},
			{AdvisorActionClose, "esc", "Close"},
		}
	}
	return []advisorButton{
		{AdvisorActionFix, "x", "Fix"},
		{AdvisorActionDismiss, "d", "Dismiss"},
		{AdvisorActionDefer, "e", "Defer"},
		{AdvisorActionFollowUp, "u", "Follow up"},
		{AdvisorActionRerun, "r", "Re-run review"},
		{AdvisorActionClose, "esc", "Close"},
	}
}

// actionFor turns a key into the message for the action it names. The review
// and finding ids travel with it, so the app never has to guess which row the
// person meant.
func (p *AdvisorPanel) actionFor(key string) tea.Cmd {
	for _, button := range p.actions() {
		if button.key != key {
			continue
		}
		switch button.action {
		case AdvisorActionClose:
			p.Close()
			return nil
		case AdvisorActionDismiss:
			p.beginDismiss()
			return nil
		}
		reviewID, findingID := p.review.ID, ""
		if finding, ok := p.selected(); ok {
			findingID = finding.ID
		}
		action := button.action
		return func() tea.Msg {
			return AdvisorActionMsg{Action: action, ReviewID: reviewID, FindingID: findingID}
		}
	}
	return nil
}

// beginDismiss opens the reason editor. A dismissal is a recorded decision, so
// the panel asks for the reason before it emits anything.
func (p *AdvisorPanel) beginDismiss() {
	if _, ok := p.selected(); !ok {
		return
	}
	p.reason = newChromelessTextarea()
	p.reason.SetWidth(max(20, p.innerWidth()-6))
	p.reason.SetHeight(3)
	p.reason.SetValue("")
	p.reason.Focus()
	p.dismissing = true
	p.setStatus("A reason is required: the dismissal is recorded with it.")
}

func (p *AdvisorPanel) handleDismissKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.dismissing = false
		p.reason.Blur()
		p.setStatus("")
		return nil
	case "ctrl+s":
		note := strings.TrimSpace(p.reason.Value())
		finding, ok := p.selected()
		if !ok {
			p.dismissing = false
			p.reason.Blur()
			p.setStatus("The finding is gone; nothing was dismissed.")
			return nil
		}
		if note == "" {
			p.setStatus("A reason is required to dismiss a finding.")
			return nil
		}
		p.dismissing = false
		p.reason.Blur()
		p.setStatus("")
		reviewID, findingID := p.review.ID, finding.ID
		return func() tea.Msg {
			return AdvisorActionMsg{Action: AdvisorActionDismiss, ReviewID: reviewID, FindingID: findingID, Note: note}
		}
	}
	var cmd tea.Cmd
	p.reason, cmd = p.reason.Update(msg)
	p.invalidate()
	return cmd
}

// ── Findings ────────────────────────────────────────────────────────────

// adopt picks the review to show: the one already displayed while it is still
// in the history, the latest otherwise. It never invents a review.
func (p *AdvisorPanel) adopt(state advisor.State) {
	latest, ok := state.Latest()
	if !ok {
		p.review, p.hasReview = advisor.Review{}, false
		p.cursor = 0
		return
	}
	if p.hasReview {
		for _, review := range state.Reviews {
			if review.ID == p.review.ID {
				p.review, p.hasReview = review.Clone(), true
				p.clampCursor()
				return
			}
		}
	}
	p.review, p.hasReview = latest.Clone(), true
	if p.file != "" && !containsString(p.files(), p.file) {
		p.file = ""
	}
	p.clampCursor()
}

// visible is the review's findings that pass every filter, in the review's own
// order (severity, file, line, title).
func (p *AdvisorPanel) visible() []advisor.Finding {
	if !p.hasReview {
		return nil
	}
	out := make([]advisor.Finding, 0, len(p.review.Findings))
	for _, finding := range p.review.Findings {
		if p.severity != "" && finding.Severity != p.severity {
			continue
		}
		if p.status != "" && finding.Status != p.status {
			continue
		}
		if p.file != "" && finding.File != p.file {
			continue
		}
		out = append(out, finding)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Severity != b.Severity {
			return a.Severity.Less(b.Severity)
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Title < b.Title
	})
	return out
}

func (p *AdvisorPanel) selected() (advisor.Finding, bool) {
	findings := p.visible()
	if len(findings) == 0 {
		return advisor.Finding{}, false
	}
	p.cursor = min(max(p.cursor, 0), len(findings)-1)
	return findings[p.cursor], true
}

func (p *AdvisorPanel) moveCursor(delta int) {
	findings := p.visible()
	if len(findings) == 0 {
		p.cursor = 0
		return
	}
	p.cursor = (p.cursor + delta + len(findings)) % len(findings)
	p.invalidate()
}

func (p *AdvisorPanel) clampCursor() {
	if n := len(p.visible()); p.cursor >= n {
		p.cursor = max(0, n-1)
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// files lists the changed files the review's findings name, sorted, so the
// file filter cycles through a stable order.
func (p *AdvisorPanel) files() []string {
	seen := map[string]bool{}
	var files []string
	for _, finding := range p.review.Findings {
		if finding.File == "" || seen[finding.File] {
			continue
		}
		seen[finding.File] = true
		files = append(files, finding.File)
	}
	sort.Strings(files)
	return files
}

// nextFile cycles the file filter: all files, then each one in turn.
func (p *AdvisorPanel) nextFile(current string) string {
	files := p.files()
	if len(files) == 0 {
		return ""
	}
	if current == "" {
		return files[0]
	}
	for i, file := range files {
		if file == current {
			if i+1 < len(files) {
				return files[i+1]
			}
			return ""
		}
	}
	return ""
}

func nextSeverity(current advisor.Severity) advisor.Severity {
	order := []advisor.Severity{"", advisor.SeverityP0, advisor.SeverityP1, advisor.SeverityP2, advisor.SeverityP3}
	for i, severity := range order {
		if severity == current {
			return order[(i+1)%len(order)]
		}
	}
	return ""
}

func nextFindingStatus(current advisor.FindingStatus) advisor.FindingStatus {
	order := []advisor.FindingStatus{"", advisor.FindingOpen, advisor.FindingAddressed, advisor.FindingDismissed, advisor.FindingDeferred}
	for i, status := range order {
		if status == current {
			return order[(i+1)%len(order)]
		}
	}
	return ""
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// ── Render ──────────────────────────────────────────────────────────────

// Render implements overlayPanel.
func (p *AdvisorPanel) Render() string {
	if !p.active {
		return ""
	}
	if p.dismissing {
		finding, _ := p.selected()
		body := p.renderDismiss(finding)
		return panelFrame(p.width, p.height, p.innerWidth(),
			p.header("dismiss"), body,
			kit.HintLine(keycap("ctrl+s")+" record dismissal", keycap("esc")+" cancel"))
	}
	if p.cacheViewSet {
		return p.cachedView
	}
	p.cachedView = panelFrame(p.width, p.height, p.innerWidth(), p.header(""), p.bodyRender(), p.hint())
	p.cacheViewSet = true
	return p.cachedView
}

func (p *AdvisorPanel) innerWidth() int {
	return max(40, min(p.width-12, 96))
}

func (p *AdvisorPanel) header(sub string) string {
	left := apTitleGlyphStyle.Render("⚑ ") + apTitleStyle.Render("Advisor")
	if p.view.Running {
		left += apBreadcrumbDimStyle.Render("  ›  ") + apBreadcrumbSubStyle.Render("reviewing")
	}
	if sub != "" {
		left += apBreadcrumbDimStyle.Render("  ›  ") + apBreadcrumbSubStyle.Render(sub)
	}
	return left
}

func (p *AdvisorPanel) bodyRender() string {
	if p.cacheBodySet {
		return p.cachedBody
	}
	p.cachedBody = p.renderBody()
	p.cacheBodySet = true
	return p.cachedBody
}

func (p *AdvisorPanel) invalidate() {
	p.cacheBodySet = false
	p.cacheViewSet = false
}

func (p *AdvisorPanel) setStatus(status string) {
	p.statusLine = status
	p.invalidate()
}

func (p *AdvisorPanel) renderBody() string {
	var b strings.Builder
	if !p.hasReview {
		b.WriteString(apSummaryStyle.Render("No reviews in this session yet."))
		b.WriteString("\n\n")
		b.WriteString(apSummaryStyle.Render("/advisor diff reviews the working tree; commit <sha>, task <id>, step <id>, session and file <path> review exactly that."))
		b.WriteString("\n\n")
		b.WriteString(p.buttons())
		return b.String()
	}

	review := p.review
	p.field(&b, "Status", advisorStatusText(review))
	p.field(&b, "Trigger", string(review.Trigger))
	p.field(&b, "Target", advisorTargetText(review.Target))
	if review.ID != "" {
		p.field(&b, "Review", review.ID)
	}
	counts := review.UnresolvedCounts()
	if counts.Total() > 0 || len(review.Findings) > 0 {
		line := advisorCountsText(counts)
		if all := len(review.Findings); all != counts.Total() {
			line += fmt.Sprintf(" · %d resolved", all-counts.Total())
		}
		p.field(&b, "Counts", line)
	}
	if highest := review.Highest(); highest != "" {
		p.field(&b, "Highest", advSeverityStyle(highest).Render(string(highest)))
	}
	if review.Target.Kind == advisor.TargetMerge && p.view.Merge != "" {
		p.field(&b, "Merge", advisorRecommendationStyle(p.view.Merge).Render(p.view.Merge.Label()))
	}
	if review.Error != "" {
		p.field(&b, "Error", advWarnStyle.Render(review.Error))
	}
	if summary := strings.TrimSpace(review.Summary); summary != "" {
		p.field(&b, "Summary", kit.TruncateText(summary, p.innerWidth()-12))
	}
	if p.view.Err != "" && p.view.Err != review.Error {
		p.field(&b, "Last error", advWarnStyle.Render(p.view.Err))
	}

	b.WriteString("\n")
	if p.detail {
		p.renderDetail(&b)
	} else {
		p.renderFindings(&b)
	}
	b.WriteString("\n")
	b.WriteString(p.buttons())
	if p.statusLine != "" {
		b.WriteString("\n\n")
		b.WriteString(apSummaryStyle.Render(p.statusLine))
	}
	return p.clip(b.String())
}

func (p *AdvisorPanel) field(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	const width = 10
	fmt.Fprintf(b, "%s %s\n", advLabelStyle.Render(apPad(label, width)), value)
}

func (p *AdvisorPanel) renderFindings(b *strings.Builder) {
	findings := p.visible()
	head := fmt.Sprintf("FINDINGS  %d of %d", len(findings), len(p.review.Findings))
	b.WriteString(advSectionStyle.Render(head))
	b.WriteString("\n")
	fmt.Fprintf(b, "%s\n", apSummaryStyle.Render(p.filterLine()))
	if len(findings) == 0 {
		message := "No findings match these filters."
		if len(p.review.Findings) == 0 {
			message = "This review found nothing."
		}
		b.WriteString(apSummaryStyle.Render(message))
		b.WriteString("\n")
		return
	}
	rows := max(3, p.height-22)
	for i, finding := range findings {
		if i >= rows {
			fmt.Fprintf(b, "  %s\n", apSummaryStyle.Render(fmt.Sprintf("… %d more", len(findings)-i)))
			break
		}
		b.WriteString(p.findingRow(i, finding))
	}
}

func (p *AdvisorPanel) findingRow(index int, finding advisor.Finding) string {
	cursor := "  "
	if index == p.cursor && !p.detail {
		cursor = advCursorStyle.Render("› ")
	}
	location := finding.File
	if location != "" && finding.Line > 0 {
		location = fmt.Sprintf("%s:%d", finding.File, finding.Line)
	}
	right := string(finding.Status)
	left := fmt.Sprintf("%s %s  %s", advSeverityStyle(finding.Severity).Render(string(finding.Severity)), finding.Title, apSummaryStyle.Render(location))
	room := p.innerWidth() - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if room < 12 {
		left = kit.TruncateText(left, max(12, p.innerWidth()-lipgloss.Width(right)-4))
		room = 0
	}
	line := cursor + left
	if room > 0 {
		line += strings.Repeat(" ", room)
	} else {
		line += "  "
	}
	return line + apSummaryStyle.Render(right) + "\n"
}

func (p *AdvisorPanel) filterLine() string {
	severity, status, file := "all", "all", "all"
	if p.severity != "" {
		severity = string(p.severity)
	}
	if p.status != "" {
		status = string(p.status)
	}
	if p.file != "" {
		file = p.file
	}
	return fmt.Sprintf("severity %s · status %s · file %s", severity, status, file)
}

func (p *AdvisorPanel) renderDetail(b *strings.Builder) {
	finding, ok := p.selected()
	if !ok {
		b.WriteString(apSummaryStyle.Render("The finding is no longer in this review."))
		b.WriteString("\n")
		return
	}
	b.WriteString(advSectionStyle.Render("FINDING"))
	b.WriteString("\n")
	head := fmt.Sprintf("%s  %s", advSeverityStyle(finding.Severity).Render(string(finding.Severity)), finding.Title)
	b.WriteString(head)
	b.WriteString("\n")
	p.field(b, "Status", string(finding.Status))
	location := finding.File
	if location != "" && finding.Line > 0 {
		location = fmt.Sprintf("%s:%d", finding.File, finding.Line)
	}
	p.field(b, "File", location)
	p.field(b, "Detail", kit.TruncateText(finding.Detail, p.innerWidth()-12))
	p.field(b, "Evidence", kit.TruncateText(finding.Evidence, p.innerWidth()-12))
	p.field(b, "Impact", kit.TruncateText(finding.Impact, p.innerWidth()-12))
	p.field(b, "Suggested fix", kit.TruncateText(finding.Suggestion, p.innerWidth()-12))
	if finding.Note != "" {
		p.field(b, "Note", kit.TruncateText(finding.Note, p.innerWidth()-12))
	}
}

func (p *AdvisorPanel) renderDismiss(finding advisor.Finding) string {
	var b strings.Builder
	head := fmt.Sprintf("%s  %s", advSeverityStyle(finding.Severity).Render(string(finding.Severity)), finding.Title)
	b.WriteString(head)
	b.WriteString("\n")
	if finding.File != "" {
		p.field(&b, "File", finding.File)
	}
	b.WriteString("\n")
	b.WriteString(apSummaryStyle.Render("Why is this finding acceptable to ship?"))
	b.WriteString("\n\n")
	b.WriteString(p.reason.View())
	if p.statusLine != "" {
		b.WriteString("\n\n")
		b.WriteString(advWarnStyle.Render(p.statusLine))
	}
	return b.String()
}

func (p *AdvisorPanel) buttons() string {
	actions := p.actions()
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		parts = append(parts, advButtonStyle.Render(" "+action.label+" "))
	}
	return strings.Join(parts, "  ")
}

func (p *AdvisorPanel) hint() string {
	if p.detail {
		return kit.HintLine(keycap("↑↓")+" scroll", keycap("esc")+" back", keycap("x")+" fix", keycap("d")+" dismiss", keycap("r")+" re-run")
	}
	return kit.HintLine(
		keycap("↑↓")+" select", keycap("enter")+" detail",
		keycap("s")+" severity", keycap("t")+" status", keycap("f")+" file",
		keycap("esc")+" close",
	)
}

// clip bounds the card body to the terminal so the buttons never scroll off
// screen. The tail is what the person must be able to press, so the last rows
// always survive.
func (p *AdvisorPanel) clip(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	limit := max(6, p.height-12)
	if len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	tail := 3
	kept := append([]string{}, lines[:limit-tail]...)
	kept = append(kept, apSummaryStyle.Render("…"))
	kept = append(kept, lines[len(lines)-tail:]...)
	return strings.Join(kept, "\n")
}

// scrollDetail is a no-op for now: the detail body is short by construction
// (every long field is truncated to the card). Keeping the key working means
// a longer body can add scrolling without changing the keyboard.
func (p *AdvisorPanel) scrollDetail(int) {}

// ── Text ────────────────────────────────────────────────────────────────

// advisorStatusText is the review's verdict in one line: its status, the work
// it covered and how it ended.
func advisorStatusText(review advisor.Review) string {
	line := string(review.Status)
	if review.Status == "" {
		line = "unknown"
	}
	if !review.FinishedAt.IsZero() {
		line += " · " + review.FinishedAt.Format("15:04")
	}
	if review.Model != "" {
		line += " · " + review.Model
	}
	return line
}

// advisorTargetText names what the review inspected, matching the subcommand
// that can review the same thing again.
func advisorTargetText(target advisor.Target) string {
	kind := string(target.Kind)
	if kind == "" {
		kind = "session"
	}
	label := target.Label
	if label == "" {
		label = target.ID
	}
	switch target.Kind {
	case advisor.TargetFile:
		if label == "" {
			label = target.ID
		}
	case advisor.TargetCommit:
		if label == "" {
			label = target.ID
		}
	}
	if label == "" {
		return kind
	}
	return kind + " · " + label
}

// advisorCountsText renders the per-level tally compactly.
func advisorCountsText(counts advisor.Counts) string {
	var parts []string
	for _, row := range []struct {
		name  string
		count int
	}{
		{"P0", counts.P0}, {"P1", counts.P1}, {"P2", counts.P2}, {"P3", counts.P3},
	} {
		if row.count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", row.name, row.count))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " · ")
}

func advSeverityStyle(severity advisor.Severity) lipgloss.Style {
	switch severity {
	case advisor.SeverityP0:
		return advDangerStyle
	case advisor.SeverityP1:
		return advWarnStyle
	case advisor.SeverityP2:
		return advSectionStyle
	}
	return advLabelStyle
}

func advisorRecommendationStyle(recommendation advisor.Recommendation) lipgloss.Style {
	switch recommendation {
	case advisor.MergeBlocked:
		return advDangerStyle
	case advisor.ReviewRequired:
		return advWarnStyle
	}
	return advSafeStyle
}

var (
	advLabelStyle   = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Bold(true)
	advSectionStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Bold(true)
	advCursorStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Accent).Bold(true)
	advWarnStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
	advDangerStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error).Bold(true)
	advSafeStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
	advButtonStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
)
