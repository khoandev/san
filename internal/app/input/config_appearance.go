// /settings Appearance panel: preference groups navigated as one list.
// Theme and thinking use radio choices; the status bars use compact toggles.
//
//   - COLOR THEME — light / dark / auto. Applied live and persisted to the
//     user-level settings file.
//
//   - STATUS BAR — independent context-usage and Git-information toggles.
//     Both default off; left/right previews Off/On, Enter applies the row.
//
//   - THINKING — full / collapsed / hidden. How the model's reasoning is
//     drawn. Full by default (the reasoning streams as before); collapsed
//     drops the body everywhere and shows one "✦ Thought for 3.2s" line
//     instead.
//
//   - SHELL OUTPUT — how many of a shell command's last output lines its row
//     shows while running and after a failure; left/right steps 0–20.
//
//   - AGENT OUTPUT — independent activity and result preview, 0–20 lines.
//
//   - SLASH MENU — whether the active skills are listed as slash commands in
//     the "/" menu. On by default; Off hides them from the menu and its
//     autocomplete while a typed "/<skill>" still runs. Left/right previews
//     Off/On, Enter applies the row.
//
// All are personal preferences, so — unlike Self-Learning — they have no
// project scope; selecting a row persists to the user settings file.
package input

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/setting"
)

// ThemeSavedMsg is emitted after the appearance panel persists a theme so
// the app can refresh its settings handle and show a confirmation. The
// theme is already applied (kit.InitTheme) and written to disk by the time
// this fires.
type ThemeSavedMsg struct {
	Theme string
}

// ContextBarSavedMsg is emitted after the appearance panel persists the
// context-bar on/off choice so the app can update its live display flag,
// refresh its settings handle, and show a confirmation. The value is already
// written to disk by the time this fires.
type ContextBarSavedMsg struct {
	On bool
}

// GitBarSavedMsg is emitted after persisting the Git-bar on/off choice.
type GitBarSavedMsg struct {
	On bool
}

// ThinkingDisplaySavedMsg is emitted after the appearance panel persists the
// reasoning-display choice so the app can update its live render flag, refresh
// its settings handle, and show a confirmation. The value is already written to
// disk by the time this fires.
type ThinkingDisplaySavedMsg struct {
	Mode string
}

// BashPreviewSavedMsg is emitted after persisting the shell output preview's
// line count, already written to disk.
type BashPreviewSavedMsg struct {
	Lines int
}

// AgentPreviewSavedMsg is emitted after persisting the Agent preview line count.
type AgentPreviewSavedMsg struct {
	Lines int
}

// SkillCommandsSavedMsg is emitted after persisting the "/" menu's
// skill-command visibility, already written to disk.
type SkillCommandsSavedMsg struct {
	On bool
}

// IconStyleSavedMsg is emitted after the appearance panel installs and persists
// an icon style so the app can refresh its settings handle and confirm the
// choice. kit.InitSymbols has already been called by the time this fires, so
// the next frame draws the new glyphs.
type IconStyleSavedMsg struct {
	Style string
}

// appearanceKind tags which setting a row applies when selected.
type appearanceKind int

const (
	kindTheme appearanceKind = iota
	kindIconStyle
	kindContextBar
	kindGitBar
	kindThinkingDisplay
	kindBashPreview
	kindAgentPreview
	kindSkillCommands
)

// appearanceOption is one selectable row. section heads the group it belongs
// to (printed once, above the group's first row). Exactly one value field is
// meaningful, selected by kind: theme for kindTheme, iconStyle for
// kindIconStyle, barOn for either bar kind, thinkingDisplay for
// kindThinkingDisplay.
type appearanceOption struct {
	section         string
	kind            appearanceKind
	label           string
	desc            string
	theme           string // kindTheme: the theme value to apply
	iconStyle       string // kindIconStyle: the icon style to apply
	barOn           bool   // kindContextBar / kindGitBar / kindSkillCommands: the on/off value to apply
	thinkingDisplay string // kindThinkingDisplay: the display mode to apply
	previewLines    int    // kindBashPreview: the line count to apply
}

// appearanceOptions is the full, section-ordered row list. The theme group
// comes first so the cursor can park on the current theme at Enter (and so
// indexOfTheme returns the same indices the first-run selector uses); the
// icon group closes the list.
func appearanceOptions() []appearanceOption {
	return []appearanceOption{
		{section: "COLOR THEME", kind: kindTheme, label: "Dark", desc: "Dark background terminal", theme: "dark"},
		{section: "COLOR THEME", kind: kindTheme, label: "Light", desc: "Light background terminal", theme: "light"},
		{section: "COLOR THEME", kind: kindTheme, label: "Auto", desc: "Match terminal appearance automatically", theme: "auto"},
		{section: "STATUS BAR", kind: kindContextBar, label: "Context usage", desc: "Visual context meter"},
		{section: "STATUS BAR", kind: kindGitBar, label: "Git information", desc: "Branch, repository and changes"},
		{section: "THINKING", kind: kindThinkingDisplay, label: "Full", desc: "Draw the reasoning as it streams", thinkingDisplay: setting.ThinkingDisplayFull},
		{section: "THINKING", kind: kindThinkingDisplay, label: "Collapsed", desc: "One line, \"Thought for 3.2s\" — no reasoning body", thinkingDisplay: setting.ThinkingDisplayCollapsed},
		{section: "THINKING", kind: kindThinkingDisplay, label: "Hidden", desc: "Draw nothing at all, not even \"Thought for 3.2s\"", thinkingDisplay: setting.ThinkingDisplayHidden},
		{section: "SHELL OUTPUT", kind: kindBashPreview, label: "Preview lines", desc: "Last lines while a command runs"},
		{section: "AGENT OUTPUT", kind: kindAgentPreview, label: "Agent preview", desc: "Activity and result preview lines"},
		{section: "SLASH MENU", kind: kindSkillCommands, label: "Skills", desc: "List skills in the / menu"},
		// Icons last: the group is a personal display choice like the rest, but
		// keeping it off the top leaves the theme and the status bars — the
		// rows people change most — in the first screenful.
		{section: "ICONS", kind: kindIconStyle, label: "Auto", desc: "Follow the terminal — plain ASCII when it cannot draw glyphs", iconStyle: setting.IconStyleAuto},
		{section: "ICONS", kind: kindIconStyle, label: "Symbols", desc: "The quiet set: ● ✦ ✓ ○", iconStyle: setting.IconStyleUnicode},
		{section: "ICONS", kind: kindIconStyle, label: "Emoji", desc: "Colorful, double-width: 🟢 ✨ ✅ ⚪", iconStyle: setting.IconStyleEmoji},
		{section: "ICONS", kind: kindIconStyle, label: "Nerd Font", desc: "Private-use glyphs — needs a patched terminal font", iconStyle: setting.IconStyleNerd},
	}
}

type appearancePanel struct {
	settings *setting.Settings

	options []appearanceOption
	cursor  int // hovered row in options

	// Baselines are the values persisted on disk, marked "● current" in their
	// group. A group is "dirty" while the cursor hovers a row that diverges
	// from its baseline.
	themeBaseline         string
	iconBaseline          string
	barBaseline           bool
	gitBarBaseline        bool
	thinkingBaseline      string
	bashPreviewBaseline   int
	agentPreviewBaseline  int
	skillCommandsBaseline bool

	// saveErr holds the last failed persist so Render can surface it inline
	// instead of silently swallowing it. Cleared on navigation / re-entry.
	saveErr error
}

func newAppearancePanel(settings *setting.Settings) *appearancePanel {
	return &appearancePanel{settings: settings}
}

func (p *appearancePanel) Title() string { return "appearance" }

func (p *appearancePanel) Enter() {
	p.options = appearanceOptions()
	p.themeBaseline = "auto"
	p.iconBaseline = setting.DefaultIconStyle
	p.barBaseline = false
	p.gitBarBaseline = false
	p.thinkingBaseline = setting.DefaultThinkingDisplay
	p.bashPreviewBaseline = setting.DefaultBashPreviewLines
	p.agentPreviewBaseline = setting.DefaultAgentPreviewLines
	p.skillCommandsBaseline = true
	if p.settings != nil {
		if data := p.settings.Snapshot(); data != nil {
			if data.Theme != "" {
				p.themeBaseline = data.Theme
			}
			p.iconBaseline = data.IconStyleKind()
			p.barBaseline = data.ShowContextBar()
			p.gitBarBaseline = data.ShowGitBar()
			p.thinkingBaseline = data.ThinkingDisplayMode()
			p.bashPreviewBaseline = data.BashPreviewLineCount()
			p.agentPreviewBaseline = data.AgentPreviewLineCount()
			p.skillCommandsBaseline = data.ShowSkillCommands()
		}
	}
	// Say what "auto" resolves to right now, so the row explains itself
	// instead of asking the user to know the terminal's capabilities.
	if i := indexOfIconStyle(setting.IconStyleAuto); i >= 0 {
		p.options[i].desc = "Follow the terminal — now " + iconStyleLabel(kit.ResolveIconStyle(setting.IconStyleAuto))
	}
	p.resetBarChoices()
	p.cursor = indexOfTheme(p.themeBaseline)
	p.saveErr = nil
}

// iconStyleLabel names a resolved icon style for the panel, matching the row
// label it corresponds to (ASCII is the plain table, which has no row).
func iconStyleLabel(style string) string {
	switch style {
	case setting.IconStyleUnicode:
		return "Symbols"
	case setting.IconStyleEmoji:
		return "Emoji"
	case setting.IconStyleNerd:
		return "Nerd Font"
	default:
		return "ASCII"
	}
}

// Dirty reports whether the hovered row diverges from its group's saved
// value, so the shell pins the "● unsaved" indicator.
func (p *appearancePanel) Dirty() bool {
	return !p.isCurrent(p.options[p.cursor])
}

func (p *appearancePanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.resetBarChoices()
			p.cursor--
		}
		p.saveErr = nil
	case "down", "j":
		if p.cursor < len(p.options)-1 {
			p.resetBarChoices()
			p.cursor++
		}
		p.saveErr = nil
	case "left", "h", "right", "l":
		more := msg.String() == "right" || msg.String() == "l"
		switch opt := &p.options[p.cursor]; {
		case isAppearanceToggle(*opt):
			opt.barOn = more
		case opt.kind == kindAgentPreview && more:
			opt.previewLines = min(opt.previewLines+1, setting.MaxAgentPreviewLines)
		case opt.kind == kindAgentPreview:
			opt.previewLines = max(opt.previewLines-1, 0)
		case opt.kind == kindBashPreview && more:
			opt.previewLines = min(opt.previewLines+1, setting.MaxBashPreviewLines)
		case opt.kind == kindBashPreview:
			opt.previewLines = max(opt.previewLines-1, 0)
		}
		p.saveErr = nil
	case "enter", " ":
		return p.apply(p.options[p.cursor])
	}
	return nil, false
}

// apply persists (and, for the theme, live-applies) the hovered option,
// advances that group's baseline, and returns the confirmation command.
// On failure it sets saveErr, keeps the popup open (done=false), and leaves
// the baseline untouched.
func (p *appearancePanel) apply(opt appearanceOption) (tea.Cmd, bool) {
	switch opt.kind {
	case kindTheme:
		// Apply live so the switch is visible immediately. InitTheme only
		// flips lipgloss's cached background flag (no terminal I/O), so it is
		// safe to call mid-program.
		kit.InitTheme(opt.theme)
		if err := setting.SaveTheme(opt.theme); err != nil {
			// Keep the on-screen theme in sync with what's actually persisted:
			// revert the live apply and surface the error instead of leaving
			// the UI showing a theme that won't survive a restart.
			kit.InitTheme(p.themeBaseline)
			p.saveErr = err
			return nil, false
		}
		p.themeBaseline = opt.theme
		return func() tea.Msg { return ThemeSavedMsg{Theme: opt.theme} }, true
	case kindIconStyle:
		// Install the table first so the change is visible on the next frame,
		// then persist. InitSymbols does no terminal I/O, so it is safe to
		// call mid-program. On a failed save the previous table is put back
		// verbatim — not by re-resolving the baseline, which for "auto"
		// depends on the terminal and may differ from what is on screen.
		previous := kit.CurrentSymbols
		kit.InitSymbols(opt.iconStyle)
		if err := setting.SaveIconStyle(opt.iconStyle); err != nil {
			kit.CurrentSymbols = previous
			p.saveErr = err
			return nil, false
		}
		p.iconBaseline = opt.iconStyle
		return func() tea.Msg { return IconStyleSavedMsg{Style: opt.iconStyle} }, true
	case kindContextBar:
		if err := setting.SaveContextBar(opt.barOn); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.barBaseline = opt.barOn
		return func() tea.Msg { return ContextBarSavedMsg{On: opt.barOn} }, true
	case kindGitBar:
		if err := setting.SaveGitBar(opt.barOn); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.gitBarBaseline = opt.barOn
		return func() tea.Msg { return GitBarSavedMsg{On: opt.barOn} }, true
	case kindThinkingDisplay:
		if err := setting.SaveThinkingDisplay(opt.thinkingDisplay); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.thinkingBaseline = opt.thinkingDisplay
		return func() tea.Msg { return ThinkingDisplaySavedMsg{Mode: opt.thinkingDisplay} }, true
	case kindBashPreview:
		if err := setting.SaveBashPreviewLines(opt.previewLines); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.bashPreviewBaseline = opt.previewLines
		return func() tea.Msg { return BashPreviewSavedMsg{Lines: opt.previewLines} }, true
	case kindAgentPreview:
		if err := setting.SaveAgentPreviewLines(opt.previewLines); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.agentPreviewBaseline = opt.previewLines
		return func() tea.Msg { return AgentPreviewSavedMsg{Lines: opt.previewLines} }, true
	case kindSkillCommands:
		if err := setting.SaveSkillCommands(opt.barOn); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.skillCommandsBaseline = opt.barOn
		return func() tea.Msg { return SkillCommandsSavedMsg{On: opt.barOn} }, true
	}
	return nil, false
}

func (p *appearancePanel) HintLine() string {
	if isAppearanceToggle(p.options[p.cursor]) {
		return keycap("↑↓") + " move  " + keycap("←→") + " off/on  " + keycap("enter") + " apply"
	}
	if p.options[p.cursor].kind == kindBashPreview || p.options[p.cursor].kind == kindAgentPreview {
		return keycap("↑↓") + " move  " + keycap("←→") + " fewer/more  " + keycap("enter") + " apply"
	}
	return radioHintLine()
}

// renderAppearanceSection renders a section heading followed by a rule that
// fills the remaining width. It delegates to the shared sectionRule so every
// panel draws headings with one label style and one rule.
func renderAppearanceSection(title string, width int) string {
	return sectionRule(title, width)
}

// renderRadioRow renders one selectable row of a /settings radio group: the
// focus bar, the ○/● radio marking the persisted choice, and a label padded to
// a common column so the descriptions line up. Shared by every panel with a
// radio group so the tabs cannot drift apart.
//
// The radio alone says which value is saved — an extra "current" tag repeated
// it — and only the focus bar carries a hue, so a row has one signal for
// "you are here" and none competing with it.
func renderRadioRow(label, desc string, hovered, current bool) string {
	caret := strings.Repeat(" ", kit.Width(kit.FocusBar)+1)
	rendered := appearanceLabelStyle.Render(label)
	if hovered {
		caret = kit.FocusBarStyle().Render(kit.FocusBar) + " "
		rendered = kit.SelectorSelectedLabelStyle().Render(label)
	}

	radio := appearanceRadioOffStyle.Render(kit.CurrentSymbols.RadioOff)
	if current {
		radio = appearanceRadioOnStyle.Render(kit.CurrentSymbols.RadioOn)
	}

	labelCell := rendered + strings.Repeat(" ", max(10-lipgloss.Width(label), 1))
	return caret + radio + " " + labelCell + appearanceDescStyle.Render(desc)
}

// radioHintLine is the shared bottom hint for a radio-group panel.
func radioHintLine() string {
	return keycap("↑↓") + " move  " + keycap("enter") + " apply"
}

// isCurrent reports whether opt matches the persisted value for its group.
func (p *appearancePanel) isCurrent(opt appearanceOption) bool {
	switch opt.kind {
	case kindContextBar:
		return opt.barOn == p.barBaseline
	case kindGitBar:
		return opt.barOn == p.gitBarBaseline
	case kindThinkingDisplay:
		return opt.thinkingDisplay == p.thinkingBaseline
	case kindBashPreview:
		return opt.previewLines == p.bashPreviewBaseline
	case kindAgentPreview:
		return opt.previewLines == p.agentPreviewBaseline
	case kindSkillCommands:
		return opt.barOn == p.skillCommandsBaseline
	case kindIconStyle:
		return opt.iconStyle == p.iconBaseline
	default:
		return opt.theme == p.themeBaseline
	}
}

// indexOfIconStyle returns the row index of the given icon-style value, or the
// first icon row when it is unknown / unset.
func indexOfIconStyle(value string) int {
	first := -1
	for i, opt := range appearanceOptions() {
		if opt.kind != kindIconStyle {
			continue
		}
		if first < 0 {
			first = i
		}
		if opt.iconStyle == value {
			return i
		}
	}
	if first < 0 {
		return 0
	}
	return first
}

// indexOfTheme returns the row index of the given theme value, or 0 (the
// first row) when it is unknown / unset.
func indexOfTheme(value string) int {
	for i, opt := range appearanceOptions() {
		if opt.kind == kindTheme && opt.theme == value {
			return i
		}
	}
	return 0
}

var (
	appearanceLabelStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text)
	appearanceDescStyle     = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	appearanceRadioOnStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextBright).Bold(true)
	appearanceRadioOffStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	appearanceErrorStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
)
