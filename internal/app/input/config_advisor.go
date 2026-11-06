// /settings Advisor panel: the advisor's whole configuration — on or off, and
// which model reviews. There is nothing else to decide.
//
// The advisor reads the conversation after each turn and, when it has something
// concrete to say, drops one short note into it. Every extra knob (triggers,
// block policy, bounds, display) made the feature look like a system that has to
// be configured before it can be switched on, so those are gone with the
// machinery they drove.
//
// The panel edits the effective config and saves it whole: the block is
// replaced at the chosen level rather than merged into it (see
// setting.UpdateAdvisorAt). The on/off row writes an explicit pointer, so an
// explicit "off" round-trips through settings.json instead of reading as
// "unset, therefore off anyway".
package input

import (
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/setting"
)

// AdvisorScope names where an Advisor panel save goes. It mirrors setting.Scope;
// turning the advisor on for this session alone is what /advisor does instead.
type AdvisorScope string

const (
	AdvisorScopeUser    AdvisorScope = "user"
	AdvisorScopeProject AdvisorScope = "project"
)

// AdvisorConfigSavedMsg reports a saved Advisor config. The panel has already
// written the file; the app owns the live session config and the rewiring that
// follows from it.
type AdvisorConfigSavedMsg struct {
	Scope  AdvisorScope
	Config setting.AdvisorSettings
}

// advisorPanel is the /settings Advisor tab: a configForm over the two
// setting.AdvisorSettings fields, plus the model sub-view that picks the second.
type advisorPanel struct {
	form configForm[setting.AdvisorSettings]

	// models lists the connected "vendor/model" refs the model picker offers.
	// The app injects it; a panel without one shows the empty list.
	models func() []string

	// picking is the model sub-view: while it is open the panel owns the keys
	// and the shell routes esc to it instead of closing the popup.
	picking bool
	pick    advisorModelPick
}

func newAdvisorPanel(settings *setting.Settings) *advisorPanel {
	p := &advisorPanel{}
	p.form = configForm[setting.AdvisorSettings]{
		workspace: func() (string, *setting.Settings) { return "", settings },
		rowsFn:    func() []configRow[setting.AdvisorSettings] { return advisorRows },
		scopes:    []configScope{scopeUser, scopeProject},
		seed:      func(data *setting.Data) setting.AdvisorSettings { return data.Advisor.Clone() },
		equal:     func(a, b setting.AdvisorSettings) bool { return a.Equal(b) },
		validate:  func(a setting.AdvisorSettings) error { return a.Validate() },
		persist: func(a setting.AdvisorSettings, scope configScope) error {
			return setting.UpdateAdvisorAt(a, setting.Scope(scope))
		},
		saved: func(a setting.AdvisorSettings, scope configScope) tea.Msg {
			return AdvisorConfigSavedMsg{Scope: AdvisorScope(scope), Config: a.Clone()}
		},
	}
	return p
}

func (p *advisorPanel) Title() string { return "advisor" }

// Enter seeds the working copy from the settings file the panel was built over.
func (p *advisorPanel) Enter() {
	p.picking = false
	p.form.Enter()
}

func (p *advisorPanel) Dirty() bool { return p.form.Dirty() }

func (p *advisorPanel) HintLine() string {
	if p.picking {
		return p.pick.hint()
	}
	return p.form.HintLine()
}

// Modal reports the model sub-view: while it is open the panel drives esc
// itself, so it backs out of the picker instead of closing /settings.
func (p *advisorPanel) Modal() bool { return p.picking }

// Breadcrumb names the sub-view the shell shows in the header lockup.
func (p *advisorPanel) Breadcrumb() string {
	if p.picking {
		return p.pick.crumb()
	}
	return ""
}

func (p *advisorPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if p.picking {
		closed, picked, ref := p.pick.handleKey(msg)
		if picked {
			p.form.snap.Model = ref
		}
		if picked || closed {
			p.picking = false
		}
		return nil, false
	}
	cmd, done, activated := p.form.HandleKey(msg)
	if activated == advisorModelEntry {
		p.openModelPick()
	}
	return cmd, done
}

func (p *advisorPanel) Render(width, height int) string {
	if p.picking {
		return p.pick.render(width, p.form.snap.Model)
	}
	p.form.visible = max(height-1, 0)
	return p.form.Render(width)
}

// openModelPick opens the model sub-view on the connected providers.
func (p *advisorPanel) openModelPick() {
	var refs []string
	if p.models != nil {
		refs = p.models()
	}
	p.pick.begin(refs, p.form.snap.Model)
	p.picking = true
}

// SetModelSource wires the picker's list of connected "vendor/model" refs.
func (p *advisorPanel) SetModelSource(fn func() []string) { p.models = fn }

// ── Rows ────────────────────────────────────────────────────────────────

// advisorModelEntry is the rowEntry id that opens the model picker.
const advisorModelEntry = "model"

// advisorRows is the whole panel: the switch, the model, and the save control.
var advisorRows = []configRow[setting.AdvisorSettings]{
	{kind: rowSubHeader, label: "Review"},
	{
		kind:       rowBool,
		label:      "Advisor",
		note:       "one note per turn, when it has something to say",
		indent:     2,
		boolGetter: func(s *setting.AdvisorSettings) bool { return s.Enabled() },
		toggle: func(s *setting.AdvisorSettings) {
			on := !s.Enabled()
			s.Enable = &on
		},
	},
	{
		kind:    rowEntry,
		label:   "Model",
		entryID: advisorModelEntry,
		indent:  2,
		desc:    "who reviews",
		summary: func(s *setting.AdvisorSettings) string { return advisorModelLabel(s) },
	},
	{kind: rowSpacer},
	{kind: rowSave, label: "Save"},
}
