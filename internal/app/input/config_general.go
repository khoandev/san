// /settings General panel: background auto-update and the local Sleev proxy.
//
//   - AUTO UPDATE — on / off. On (the default) lets an installer-managed
//     binary install new releases in the background at startup; the new
//     version runs on the next launch. Off stays on the current version.
//
//   - SLEEV PROXY — on / off. On routes every provider's requests through the
//     local Sleev gateway, carrying that provider's own credential and naming
//     its real address in the sleev-base-url header; off calls each provider's
//     own endpoint. The decision is written to the user's settings.json through
//     the setting package, so it survives a restart and no checkout can state it.
//
//   - GATEWAY URL — where the proxy listens. Empty means the default,
//     http://127.0.0.1:17321. Enter opens the editor; Enter saves, Esc cancels.
//
//   - HARNESS — the client the gateway is told it is forwarding for, sent as the
//     sleev-harness header; it is the harness whose providers must be enabled in
//     Sleev for the traffic to be optimized. Empty means the default, "pi". The
//     row shares the address editor: same keys, same save.
//
// The proxy is a routing switch, not a provider: nothing is connected by it and
// no model list is fetched from it.
package input

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/setting"
)

// AutoUpdateSavedMsg is emitted after the general panel persists the
// auto-update switch so the app can refresh its settings handle and confirm.
type AutoUpdateSavedMsg struct {
	On bool
}

// SleevProxySavedMsg is emitted after the general panel persists the proxy's
// switch, address or harness, so the app can re-read the settings, rebuild the
// provider it infers through and let the change apply to the next turn.
type SleevProxySavedMsg struct {
	Enabled bool
	BaseURL string
}

// generalOptionKind is what a row does: pick the auto-update value, flip the
// proxy, or edit one of the two values it carries.
type generalOptionKind int

const (
	kindAutoUpdate generalOptionKind = iota
	kindSleevToggle
	kindSleevURL
	kindSleevHarness
)

// valueEditor edits one text row — the gateway's address or the harness it is
// told it serves. Both are a single stored string edited the same way, so they
// share one editor rather than one each.
type valueEditor struct {
	kind   generalOptionKind // the row the editor was opened on
	active bool
	input  textinput.Model
	err    string
}

type generalOption struct {
	section string
	kind    generalOptionKind
	label   string
	desc    string
	on      bool
}

// generalOptions builds the row list for the gateway's current address and
// harness.
func generalOptions(baseURL, harness string) []generalOption {
	return []generalOption{
		{section: "AUTO UPDATE", kind: kindAutoUpdate, label: "On", desc: "install new releases in the background; they run on next launch", on: true},
		{section: "AUTO UPDATE", kind: kindAutoUpdate, label: "Off", desc: "stay on this version (san update to upgrade by hand)"},
		{section: "SLEEV PROXY", kind: kindSleevToggle, label: "Route through proxy", desc: "every provider's requests take the gateway, with its own key"},
		{section: "SLEEV PROXY", kind: kindSleevURL, label: "Gateway URL", desc: baseURL},
		{section: "SLEEV PROXY", kind: kindSleevHarness, label: "Harness", desc: harness},
	}
}

type generalPanel struct {
	settings *setting.Settings

	options []generalOption
	cursor  int

	baseline      bool   // the auto-update value on disk, marked "● current"
	sleevBaseline bool   // the proxy's on/off value on disk
	baseURL       string // the proxy's address on disk
	harness       string // the harness the gateway is told it serves

	editor valueEditor

	saveErr error
}

func newGeneralPanel(settings *setting.Settings) *generalPanel {
	return &generalPanel{settings: settings}
}

func (p *generalPanel) Title() string { return "general" }

func (p *generalPanel) Enter() {
	p.baseline = p.settings == nil || p.settings.AutoUpdate()
	p.saveErr = nil
	p.loadSleev()
	p.options = generalOptions(p.baseURL, p.harness)
	p.resetSleevPreview()
	p.cursor = 0
	if !p.baseline {
		p.cursor = 1
	}
	p.editor = valueEditor{}
}

// loadSleev reads the proxy's setting from the settings handle the panel was
// opened with — the same handle Reload refreshes after a save, so re-opening
// this panel shows what was written. A missing handle is the same state as a
// setting that was never stated: the defaults stand.
func (p *generalPanel) loadSleev() {
	cfg := setting.SleevSettings{}.Normalized()
	if p.settings != nil {
		cfg = p.settings.Sleev()
	}
	p.sleevBaseline = cfg.IsEnabled()
	p.baseURL = cfg.BaseURL
	p.harness = cfg.Harness
}

// resetSleevPreview drops an un-applied toggle preview, the way leaving a radio
// choice does: Enter always applies only the focused row.
func (p *generalPanel) resetSleevPreview() {
	for i := range p.options {
		if p.options[i].kind == kindSleevToggle {
			p.options[i].on = p.sleevBaseline
		}
	}
}

func (p *generalPanel) Dirty() bool {
	opt := p.options[p.cursor]
	switch opt.kind {
	case kindAutoUpdate:
		return opt.on != p.baseline
	case kindSleevToggle:
		return opt.on != p.sleevBaseline
	default:
		return p.editor.active
	}
}

// Modal reports that the editor owns the keys, so the shell leaves Esc and the
// arrows to the panel instead of dismissing the popup.
func (p *generalPanel) Modal() bool { return p.editor.active }

func (p *generalPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if p.editor.active {
		return p.handleEditorKey(msg)
	}

	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.resetSleevPreview()
			p.cursor--
		}
		p.saveErr = nil
	case "down", "j":
		if p.cursor < len(p.options)-1 {
			p.resetSleevPreview()
			p.cursor++
		}
		p.saveErr = nil
	case "left", "h", "right", "l":
		// Only the proxy switch previews a value; the radio group and the
		// address row ignore the arrows.
		if opt := &p.options[p.cursor]; opt.kind == kindSleevToggle {
			opt.on = msg.String() == "right" || msg.String() == "l"
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
	switch opt.kind {
	case kindAutoUpdate:
		if err := setting.SaveAutoUpdate(opt.on); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.baseline = opt.on
		return func() tea.Msg { return AutoUpdateSavedMsg{On: opt.on} }, true
	case kindSleevToggle:
		if err := p.saveSleev(opt.on, p.baseURL, p.harness); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.sleevBaseline = opt.on
		return func() tea.Msg { return SleevProxySavedMsg{Enabled: opt.on, BaseURL: p.baseURL} }, true
	case kindSleevURL, kindSleevHarness:
		p.startEditor(opt)
	}
	return nil, false
}

// startEditor opens the shared value editor on the focused row, pre-filled with
// the stored value and advertising the default the empty field means.
func (p *generalPanel) startEditor(opt generalOption) {
	value, placeholder := p.baseURL, setting.DefaultSleevBaseURL
	if opt.kind == kindSleevHarness {
		value, placeholder = p.harness, setting.DefaultSleevHarness
	}

	input := textinput.New()
	input.Placeholder = placeholder
	input.SetValue(value)
	input.CharLimit = 256
	input.SetWidth(44)
	input.Focus()

	p.editor = valueEditor{kind: opt.kind, active: true, input: input}
}

func (p *generalPanel) handleEditorKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		p.editor = valueEditor{}
		return nil, false
	case "enter":
		value, err := normalizeSleevValue(p.editor.kind, p.editor.input.Value())
		if err != nil {
			p.editor.err = err.Error()
			return nil, false
		}
		baseURL, harness := p.baseURL, p.harness
		if p.editor.kind == kindSleevHarness {
			harness = value
		} else {
			baseURL = value
		}
		if err := p.saveSleev(p.sleevBaseline, baseURL, harness); err != nil {
			p.editor.err = err.Error()
			return nil, false
		}
		p.baseURL, p.harness = baseURL, harness
		for i := range p.options {
			if p.options[i].kind == p.editor.kind {
				p.options[i].desc = value
			}
		}
		p.editor = valueEditor{}
		return func() tea.Msg { return SleevProxySavedMsg{Enabled: p.sleevBaseline, BaseURL: baseURL} }, true
	}

	var cmd tea.Cmd
	p.editor.input, cmd = p.editor.input.Update(msg)
	return cmd, false
}

// saveSleev writes all three values to the user's settings file in one go: the
// store states the block wholesale, so a save of one row carries the two it did
// not touch rather than letting them fall back to their defaults.
func (p *generalPanel) saveSleev(enabled bool, baseURL, harness string) error {
	return setting.SaveSleev(enabled, baseURL, harness)
}

// normalizeSleevValue validates the text the editor holds, by the row it was
// opened on.
func normalizeSleevValue(kind generalOptionKind, value string) (string, error) {
	if kind == kindSleevHarness {
		return normalizeSleevHarness(value)
	}
	return normalizeSleevURL(value)
}

// normalizeSleevHarness validates the harness name the gateway is told it
// forwards for. An empty field means the default, which is what the placeholder
// advertises; a name with a space or a slash would build a path the gateway
// cannot route.
func normalizeSleevHarness(value string) (string, error) {
	harness := strings.TrimSpace(value)
	if harness == "" {
		return setting.DefaultSleevHarness, nil
	}
	if strings.ContainsAny(harness, " /\\") {
		return "", fmt.Errorf("harness must be one name, without spaces or slashes")
	}
	return harness, nil
}

// normalizeSleevURL validates the gateway address, filling in the local default
// when the field is left empty.
func normalizeSleevURL(value string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(value), "/")
	if baseURL == "" {
		return setting.DefaultSleevBaseURL, nil
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return "", fmt.Errorf("gateway URL must start with https:// or http://")
	}
	return baseURL, nil
}

func (p *generalPanel) HintLine() string {
	if p.editor.active {
		return keycap("enter") + " save  " + keycap("esc") + " cancel"
	}
	return keycap("↑↓") + " move  " + keycap("←→") + " preview  " + keycap("enter") + " apply"
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
		switch opt.kind {
		case kindSleevToggle:
			body.lines = append(body.lines, renderToggleRow(opt.label, opt.desc, opt.on, i == p.cursor, width))
		case kindSleevURL, kindSleevHarness:
			body.lines = append(body.lines, p.renderValueRow(opt, i == p.cursor, width))
		default:
			body.lines = append(body.lines, renderRadioRow(opt.label, opt.desc, i == p.cursor, opt.on == p.baseline))
		}
	}

	if p.editor.err != "" {
		body.errors = append(body.errors, appearanceErrorStyle.Render("⚠ "+p.editor.err))
	}
	if p.saveErr != nil {
		body.errors = append(body.errors, appearanceErrorStyle.Render("⚠ couldn't save: "+p.saveErr.Error()))
	}
	return renderSettingBody(body, width, height)
}

// renderValueRow draws one edited value — the gateway's address or the harness
// name: the stored value normally, the live editor while it has focus.
func (p *generalPanel) renderValueRow(opt generalOption, hovered bool, width int) string {
	if p.editor.active && p.editor.kind == opt.kind {
		head, _ := appearanceRowHead(opt.label+":", true)
		return head + p.editor.input.View()
	}
	head, _ := appearanceRowHead(opt.label, hovered)
	return withDesc(head+kit.DimStyle().Render(opt.desc), "enter to edit", width)
}
