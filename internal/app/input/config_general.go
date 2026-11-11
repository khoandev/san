// /settings General panel: background auto-update and the local Sleev proxy.
//
//   - AUTO UPDATE — on / off. On (the default) lets an installer-managed
//     binary install new releases in the background at startup; the new
//     version runs on the next launch. Off stays on the current version.
//
//   - SLEEV PROXY — on / off. On routes every provider's requests through the
//     local Sleev gateway, carrying that provider's own credential and naming
//     its real address in the sleev-base-url header; off calls each provider's
//     own endpoint. The decision is written to providers.json, beside the
//     providers it affects, not to the settings file.
//
//   - GATEWAY URL — where the proxy listens. Empty means the default,
//     http://127.0.0.1:17321. Enter opens the editor; Enter saves, Esc cancels.
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
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// AutoUpdateSavedMsg is emitted after the general panel persists the
// auto-update switch so the app can refresh its settings handle and confirm.
type AutoUpdateSavedMsg struct {
	On bool
}

// SleevProxySavedMsg is emitted after the general panel persists the proxy's
// switch or address, so the running session can rebuild the provider it infers
// through and the change applies to the next turn.
type SleevProxySavedMsg struct {
	Enabled bool
	BaseURL string
}

// generalOptionKind is what a row does: pick the auto-update value, flip the
// proxy, or edit the proxy's address.
type generalOptionKind int

const (
	kindAutoUpdate generalOptionKind = iota
	kindSleevToggle
	kindSleevURL
)

type generalOption struct {
	section string
	kind    generalOptionKind
	label   string
	desc    string
	on      bool
}

// generalOptions builds the row list for the current gateway address.
func generalOptions(baseURL string) []generalOption {
	return []generalOption{
		{section: "AUTO UPDATE", kind: kindAutoUpdate, label: "On", desc: "install new releases in the background; they run on next launch", on: true},
		{section: "AUTO UPDATE", kind: kindAutoUpdate, label: "Off", desc: "stay on this version (san update to upgrade by hand)"},
		{section: "SLEEV PROXY", kind: kindSleevToggle, label: "Route through proxy", desc: "every provider's requests take the gateway, with its own key"},
		{section: "SLEEV PROXY", kind: kindSleevURL, label: "Gateway URL", desc: baseURL},
	}
}

type generalPanel struct {
	settings *setting.Settings

	options []generalOption
	cursor  int

	baseline      bool   // the auto-update value on disk, marked "● current"
	sleevBaseline bool   // the proxy's on/off value on disk
	baseURL       string // the proxy's address on disk

	urlEditing bool
	urlInput   textinput.Model
	urlErr     string

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
	p.options = generalOptions(p.baseURL)
	p.resetSleevPreview()
	p.cursor = 0
	if !p.baseline {
		p.cursor = 1
	}
	p.urlEditing = false
	p.urlErr = ""
}

// loadSleev reads the proxy's stored setting. A store San cannot open is the
// same state as one that was never configured: the defaults stand, and the save
// that follows reports the error.
func (p *generalPanel) loadSleev() {
	cfg := llm.SleevConfig{}.Normalized()
	if store, err := llm.NewStore(); err != nil {
		p.saveErr = err
	} else if stored := store.Sleev(); stored != nil {
		cfg = stored.Normalized()
	}
	p.sleevBaseline = cfg.Enabled
	p.baseURL = cfg.BaseURL
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
		return p.urlEditing
	}
}

// Modal reports that the address editor owns the keys, so the shell leaves Esc
// and the arrows to the panel instead of dismissing the popup.
func (p *generalPanel) Modal() bool { return p.urlEditing }

func (p *generalPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if p.urlEditing {
		return p.handleURLEditorKey(msg)
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
		if err := p.saveSleev(opt.on, p.baseURL); err != nil {
			p.saveErr = err
			return nil, false
		}
		p.sleevBaseline = opt.on
		return func() tea.Msg { return SleevProxySavedMsg{Enabled: opt.on, BaseURL: p.baseURL} }, true
	case kindSleevURL:
		p.startURLEditor()
	}
	return nil, false
}

func (p *generalPanel) startURLEditor() {
	input := textinput.New()
	input.Placeholder = llm.DefaultSleevBaseURL
	input.SetValue(p.baseURL)
	input.CharLimit = 256
	input.SetWidth(44)
	input.Focus()

	p.urlInput = input
	p.urlEditing = true
	p.urlErr = ""
}

func (p *generalPanel) handleURLEditorKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		p.urlEditing = false
		p.urlErr = ""
		return nil, false
	case "enter":
		baseURL, err := normalizeSleevURL(p.urlInput.Value())
		if err != nil {
			p.urlErr = err.Error()
			return nil, false
		}
		if err := p.saveSleev(p.sleevBaseline, baseURL); err != nil {
			p.urlErr = err.Error()
			return nil, false
		}
		p.baseURL = baseURL
		for i := range p.options {
			if p.options[i].kind == kindSleevURL {
				p.options[i].desc = baseURL
			}
		}
		p.urlEditing = false
		p.urlErr = ""
		return func() tea.Msg { return SleevProxySavedMsg{Enabled: p.sleevBaseline, BaseURL: baseURL} }, true
	}

	var cmd tea.Cmd
	p.urlInput, cmd = p.urlInput.Update(msg)
	return cmd, false
}

// saveSleev writes the proxy's setting to the provider store, keeping the
// harness name stored beside it — it has no row of its own and always travels
// with a save.
func (p *generalPanel) saveSleev(enabled bool, baseURL string) error {
	store, err := llm.NewStore()
	if err != nil {
		return err
	}
	cfg := llm.SleevConfig{Enabled: enabled, BaseURL: baseURL}
	if stored := store.Sleev(); stored != nil {
		cfg.Harness = stored.Harness
	}
	return store.SetSleev(cfg)
}

// normalizeSleevURL validates the gateway address, filling in the local default
// when the field is left empty.
func normalizeSleevURL(value string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(value), "/")
	if baseURL == "" {
		return llm.DefaultSleevBaseURL, nil
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return "", fmt.Errorf("gateway URL must start with https:// or http://")
	}
	return baseURL, nil
}

func (p *generalPanel) HintLine() string {
	if p.urlEditing {
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
		case kindSleevURL:
			body.lines = append(body.lines, p.renderURLRow(opt, i == p.cursor, width))
		default:
			body.lines = append(body.lines, renderRadioRow(opt.label, opt.desc, i == p.cursor, opt.on == p.baseline))
		}
	}

	if p.urlErr != "" {
		body.errors = append(body.errors, appearanceErrorStyle.Render("⚠ "+p.urlErr))
	}
	if p.saveErr != nil {
		body.errors = append(body.errors, appearanceErrorStyle.Render("⚠ couldn't save: "+p.saveErr.Error()))
	}
	return renderSettingBody(body, width, height)
}

// renderURLRow draws the address row: the stored value normally, the live
// editor while it has focus.
func (p *generalPanel) renderURLRow(opt generalOption, hovered bool, width int) string {
	if p.urlEditing {
		head, _ := appearanceRowHead(opt.label+":", true)
		return head + p.urlInput.View()
	}
	head, _ := appearanceRowHead(opt.label, hovered)
	return withDesc(head+kit.DimStyle().Render(opt.desc), "enter to edit", width)
}
