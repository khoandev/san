package input

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

// RateLimitRequest is a stopped turn's transient provider failure as the dialog
// shows it: the provider's own wording, and the wait it asked for before the
// next attempt. The caller names the failure; this package only draws it.
type RateLimitRequest struct {
	Title  string        // "Provider rate limit", "Provider overloaded", "Connection lost"
	Detail string        // the provider's error, verbatim
	Hint   time.Duration // the provider's Retry-After; 0 when it gave none
	// TaskID names the paused subagent this failure stopped. Empty means the
	// main turn failed, which is what Restart runs again.
	TaskID string
}

// RateLimitChoiceMsg answers the dialog. Restart asks for the work that failed
// to run again over the conversation as it stands — the main turn, or the paused
// task named by TaskID; the zero value closes it and leaves the work where it
// stopped.
type RateLimitChoiceMsg struct {
	Restart bool
	TaskID  string
}

// RateLimitModel is the docked dialog a provider failure raises: what failed,
// how long the provider asked to wait, and the two things a person can do
// about it. It holds the keyboard while it is up — neither answer is the
// default one, so nothing may be typed past it by accident.
type RateLimitModel struct {
	active  bool
	request RateLimitRequest
	width   int
	height  int
	cursor  int // 0 close, 1 restart
}

// NewRateLimit creates a hidden dialog. It opens on Show.
func NewRateLimit() RateLimitModel { return RateLimitModel{} }

// Show raises the dialog for one provider failure.
func (p *RateLimitModel) Show(req RateLimitRequest, width, height int) {
	p.active = true
	p.request = req
	p.width = width
	p.height = height
	// Close first: Enter is the reflex, and it must not spend an inference on
	// an error the person has not read yet.
	p.cursor = 0
}

// Hide drops the dialog and the failure it was holding.
func (p *RateLimitModel) Hide() {
	p.active = false
	p.request = RateLimitRequest{}
}

// IsActive implements overlayPanel.
func (p *RateLimitModel) IsActive() bool { return p.active }

// Resize keeps the dialog inside a terminal that changed size under it.
func (p *RateLimitModel) Resize(width, height int) {
	p.width, p.height = width, height
}

// HandleKeypress implements overlayPanel: arrows and tab move between the two
// answers, Enter takes the focused one, and esc, c and r take one directly.
func (p *RateLimitModel) HandleKeypress(msg tea.KeyMsg) tea.Cmd {
	if !p.active {
		return nil
	}
	switch msg.String() {
	case "left", "up", "shift+tab":
		p.cursor = 0
		return nil
	case "right", "down", "tab":
		p.cursor = 1
		return nil
	case "enter":
		return p.choose(p.cursor == 1)
	case "esc", "ctrl+c", "c":
		return p.choose(false)
	case "r":
		return p.choose(true)
	}
	return nil
}

// choose answers the dialog once. The dialog closes with the answer, so a
// second keypress cannot answer it again.
func (p *RateLimitModel) choose(restart bool) tea.Cmd {
	taskID := p.request.TaskID
	p.Hide()
	return func() tea.Msg { return RateLimitChoiceMsg{Restart: restart, TaskID: taskID} }
}

// Render implements overlayPanel.
func (p *RateLimitModel) Render() string {
	if !p.active {
		return ""
	}
	contentWidth := max(p.width-2, 40)
	var sb strings.Builder
	sb.WriteString(" ")
	sb.WriteString(waitTitleStyle().Render(kit.CurrentSymbols.Warning + "  " + p.request.Title))
	sb.WriteString("\n\n   ")
	sb.WriteString(approvalQuestionStyle().Render(kit.TruncateText(p.request.Detail, contentWidth-6)))
	sb.WriteString("\n   ")
	sb.WriteString(approvalQuestionStyle().Render(p.waitText()))
	sb.WriteString("\n\n   ")
	sb.WriteString(p.buttons())
	sb.WriteString("\n\n ")
	sb.WriteString(p.hint(contentWidth))
	sb.WriteString("\n")
	sb.WriteString(approvalSeparatorStyle().Render(strings.Repeat("─", contentWidth)))
	return sb.String()
}

// waitText is what the provider said about waiting, in one line. A provider
// that named no wait gets no invented number.
func (p *RateLimitModel) waitText() string {
	if p.request.Hint <= 0 {
		return "The provider named no wait before retrying."
	}
	return "The provider asked to wait " + kit.WaitText(p.request.Hint) + " before retrying."
}

// buttons renders the two answers, the focused one reversed like every other
// button row in the docked panels.
func (p *RateLimitModel) buttons() string {
	restart := "Restart"
	if p.request.TaskID != "" {
		// The answer restarts a paused worker, not the failed turn: say which.
		restart = "Restart task"
	}
	labels := []string{"Close", restart}
	parts := make([]string, len(labels))
	for i, label := range labels {
		if i == p.cursor {
			parts[i] = planButtonActiveStyle.Render(" " + label + " ")
			continue
		}
		parts[i] = planButtonStyle.Render(" " + label + " ")
	}
	return strings.Join(parts, "  ")
}

// hint is the key legend, one row by contract: a narrow terminal loses the
// meanings before it loses the keys.
func (p *RateLimitModel) hint(contentWidth int) string {
	labelled := []string{
		keycap("←→") + " choose",
		keycap("enter") + " confirm",
		keycap("esc") + " close",
		keycap("r") + " restart",
	}
	if line := kit.HintLine(labelled...); lipgloss.Width(line) <= contentWidth {
		return line
	}
	return kit.TruncateText(kit.HintLine(keycap("←→"), keycap("enter"), keycap("esc"), keycap("r")), contentWidth)
}

// waitTitleStyle marks the dialog as a warning, not as another panel: the same
// glyph the retry row uses, in the same colour.
func waitTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning).Bold(true)
}
