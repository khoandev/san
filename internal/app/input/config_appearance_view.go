package input

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// settingBody records positions while generating rows, so windowing does not
// need to reconstruct section spacing or identify errors by their offsets.
type settingBody struct {
	lines      []string
	errors     []string
	sectionRow int
	cursorRow  int
}

// Render windows the flat Appearance list around the hovered section. Adding
// preferences must not push the settings card past a normal 24-row terminal.
// Navigation still traverses every option; failed saves remain pinned in view.
func (p *appearancePanel) Render(width, height int) string {
	return renderSettingBody(p.renderBody(width), width, height)
}

// renderSettingBody clips a settings panel's body to the card and truncates every
// line to its width, so a long value never wraps into a second row. Shared by
// the tabs that draw a flat preference list.
func renderSettingBody(body settingBody, width, height int) string {
	budget := height
	if budget > 0 && len(body.errors) > 0 {
		budget = max(1, budget-len(body.errors))
	}
	lines := windowSettingLines(body.lines, body.sectionRow, body.cursorRow, budget)
	lines = append(lines, body.errors...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(0, width), "…")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// windowSettingLines keeps the hovered section and the hovered row in view: the
// window starts at the section heading and slides down when the cursor would
// fall past its end. A failed save keeps its own rows, so it is counted out of
// the body's budget rather than pushing the list off the card.
func windowSettingLines(lines []string, sectionRow, cursorRow, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	start := min(sectionRow, max(0, len(lines)-height))
	if cursorRow >= start+height {
		start = cursorRow - height + 1
	}
	return lines[start:min(len(lines), start+height)]
}

func (p *appearancePanel) renderBody(width int) settingBody {
	body := settingBody{lines: make([]string, 0, len(p.options)*2)}
	previous := ""
	sectionRow := 0
	for i, opt := range p.options {
		if opt.section != previous {
			if previous != "" {
				body.lines = append(body.lines, "")
			}
			sectionRow = len(body.lines)
			body.lines = append(body.lines, renderAppearanceSection(opt.section, width))
			previous = opt.section
		}
		if i == p.cursor {
			body.sectionRow = sectionRow
			body.cursorRow = len(body.lines)
		}
		switch {
		case isAppearanceToggle(opt):
			body.lines = append(body.lines, renderBarToggle(opt, i == p.cursor, width))
		case opt.kind == kindBashPreview || opt.kind == kindAgentPreview:
			body.lines = append(body.lines, renderPreviewStepper(opt, i == p.cursor, width))
		default:
			body.lines = append(body.lines, renderRadioRow(opt.label, opt.desc, i == p.cursor, p.isCurrent(opt)))
		}
	}
	if p.saveErr != nil {
		body.errors = []string{"", appearanceErrorStyle.Render("⚠ couldn't save: " + p.saveErr.Error())}
	}
	return body
}
