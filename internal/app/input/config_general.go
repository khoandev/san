// /settings General panel: background auto-update.
//
//   - AUTO UPDATE — on / off. On (the default) lets an installer-managed
//     binary install new releases in the background at startup; the new
//     version runs on the next launch. Off stays on the current version.
package input

import (
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/setting"
)

// AutoUpdateSavedMsg is emitted after the general panel persists the
// auto-update switch so the app can refresh its settings handle and confirm.
type AutoUpdateSavedMsg struct {
	On bool
}

type generalOption struct {
	section string
	label   string
	desc    string
	on      bool
}

// generalOptions builds the two auto-update choices.
func generalOptions() []generalOption {
	return []generalOption{
		{section: "AUTO UPDATE", label: "On", desc: "install new releases in the background; they run on next launch", on: true},
		{section: "AUTO UPDATE", label: "Off", desc: "stay on this version (san update to upgrade by hand)"},
	}
}

type generalPanel struct {
	settings *setting.Settings

	options []generalOption
	cursor  int

	baseline bool // the auto-update value on disk, marked "• current"

	saveErr error
}

func newGeneralPanel(settings *setting.Settings) *generalPanel {
	return &generalPanel{settings: settings}
}

func (p *generalPanel) Title() string { return "general" }

func (p *generalPanel) Enter() {
	p.baseline = p.settings == nil || p.settings.AutoUpdate()
	p.saveErr = nil
	p.options = generalOptions()
	p.cursor = 0
	if !p.baseline {
		p.cursor = 1
	}
}

func (p *generalPanel) Dirty() bool {
	return p.options[p.cursor].on != p.baseline
}

func (p *generalPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		p.saveErr = nil
	case "down", "j":
		if p.cursor < len(p.options)-1 {
			p.cursor++
		}
		p.saveErr = nil
	case "enter", " ":
		return p.apply(p.options[p.cursor])
	}
	return nil, false
}

// apply persists the hovered row and returns the confirmation command. On
// failure it keeps the popup open and surfaces the error, leaving the stored
// value as it was.
func (p *generalPanel) apply(opt generalOption) (tea.Cmd, bool) {
	if err := setting.SaveAutoUpdate(opt.on); err != nil {
		p.saveErr = err
		return nil, false
	}
	p.baseline = opt.on
	return func() tea.Msg { return AutoUpdateSavedMsg{On: opt.on} }, true
}

func (p *generalPanel) HintLine() string {
	return keycap("↑↓") + " move  " + keycap("enter") + " apply"
}

func (p *generalPanel) Render(width, height int) string {
	body := settingBody{}
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
		body.lines = append(body.lines, renderRadioRow(opt.label, opt.desc, i == p.cursor, opt.on == p.baseline))
	}

	if p.saveErr != nil {
		body.errors = append(body.errors, appearanceErrorStyle.Render("⚠ couldn't save: "+p.saveErr.Error()))
	}
	return renderSettingBody(body, width, height)
}
