package input

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/kit"
)

// isAppearanceBar reports whether opt is one of the two status-bar switches.
func isAppearanceBar(opt appearanceOption) bool {
	return opt.kind == kindContextBar || opt.kind == kindGitBar
}

// isAppearanceToggle reports whether opt is a compact Off/On control, drawn in
// the shared toggle layout and driven by ←/→.
func isAppearanceToggle(opt appearanceOption) bool {
	return isAppearanceBar(opt) || opt.kind == kindSkillCommands
}

// resetBarChoices initializes the compact toggles from the saved preferences.
// Leaving a row discards its un-applied preview, just like leaving a radio
// choice: Enter always applies only the focused preference.
func (p *appearancePanel) resetBarChoices() {
	for i := range p.options {
		switch p.options[i].kind {
		case kindContextBar:
			p.options[i].barOn = p.barBaseline
		case kindGitBar:
			p.options[i].barOn = p.gitBarBaseline
		case kindSkillCommands:
			p.options[i].barOn = p.skillCommandsBaseline
		case kindBashPreview:
			p.options[i].previewLines = p.bashPreviewBaseline
		case kindAgentPreview:
			p.options[i].previewLines = p.agentPreviewBaseline
		}
	}
}

// renderBarToggle shares a label column and compact Off / On cells between
// every compact switch (the status bars and the / menu skill listing). Only the selected value has a background, not the row.
// Unfocused controls use a neutral fill; the focused value shares the popup's
// active-tab accent so color marks navigation, not every enabled preference.
func renderBarToggle(opt appearanceOption, hovered bool, width int) string {
	return renderToggleRow(opt.label, opt.desc, opt.barOn, hovered, width)
}

// renderToggleRow draws one Off/On switch in the shared control layout. It is
// what every settings tab's switch looks like, so an on/off choice reads the
// same on the Appearance tab and the Agent tab.
func renderToggleRow(label, desc string, on, hovered bool, width int) string {
	head, selected := appearanceRowHead(label, hovered)
	offStyle, onStyle := appearanceToggleStyle, appearanceToggleStyle
	if on {
		onStyle = selected
	} else {
		offStyle = selected
	}
	return withDesc(head+offStyle.Render("Off")+" "+onStyle.Render("On"), desc, width)
}

// renderPreviewStepper draws the preview-lines row in the toggles' layout: the
// count in one cell between ‹ › arrows, "Off" for 0.
func renderPreviewStepper(opt appearanceOption, hovered bool, width int) string {
	head, selected := appearanceRowHead(opt.label, hovered)
	value := "Off"
	if opt.previewLines > 0 {
		value = strconv.Itoa(opt.previewLines)
	}
	arrow := appearanceDescStyle.Render
	return withDesc(head+arrow("‹ ")+selected.Render(value)+arrow(" ›"), opt.desc, width)
}

// appearanceRowHead is a control row's focus bar and padded label, plus the
// style of its selected value: the active-tab accent while the row has the
// cursor. The focus bar is the same affordance the radio rows and the other
// pickers use, so a row has one signal for "you are here".
func appearanceRowHead(label string, hovered bool) (string, lipgloss.Style) {
	bar, padded, selected := strings.Repeat(" ", kit.Width(kit.FocusBar)+1), appearanceLabelStyle.Render(label), appearanceToggleSelectedStyle
	if hovered {
		bar, padded, selected = kit.FocusBarStyle().Render(kit.FocusBar)+" ", kit.SelectorSelectedLabelStyle().Render(label), appearanceToggleFocusedStyle
	}
	return bar + padded + strings.Repeat(" ", max(18-lipgloss.Width(label), 1)), selected
}

// withDesc appends the row's description when it fits.
func withDesc(row, desc string, width int) string {
	if remaining := width - lipgloss.Width(row) - 2; remaining >= 16 {
		row += "  " + appearanceDescStyle.Render(ansi.Truncate(desc, remaining, "…"))
	}
	return row
}

// Adaptive colors resolve at render time, so these shared styles still track
// live theme changes without rebuilding them for every toggle and frame.
var (
	appearanceToggleStyle         = lipgloss.NewStyle().Width(5).Align(lipgloss.Center).Foreground(kit.CurrentTheme.TextDim)
	appearanceToggleSelectedStyle = appearanceToggleStyle.Background(kit.CurrentTheme.Border).Foreground(kit.CurrentTheme.Text).Bold(true)
	appearanceToggleFocusedStyle  = appearanceToggleSelectedStyle.Background(kit.CurrentTheme.Focus).Foreground(kit.CurrentTheme.Background)
)
