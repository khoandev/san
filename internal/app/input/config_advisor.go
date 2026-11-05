// /settings Advisor panel: the global review layer's configuration, reduced to
// the decisions a person actually makes — on/off, the model, and the review
// depth — with the same user / project / session save control the other panels
// use, plus the one scope only the advisor has: this session.
//
// Everything else the advisor supports (triggers, block policy, bounds, display)
// keeps its documented default and stays editable in settings.json. A panel with
// forty knobs reads as a system that has to be configured before it can be
// switched on, which is the opposite of what an opt-in review layer should cost.
//
// The panel edits the effective config and saves it whole: the block is
// replaced at the chosen level rather than merged into it, so a knob turned
// back to its default actually persists there (see setting.UpdateAdvisorAt).
// Every on/off row writes an explicit pointer, which is what makes an explicit
// "off" survive a reload instead of reading as "unset, therefore default".
//
// Session scope writes no file. It hands the config to the app through
// AdvisorConfigSavedMsg: the session's live config and its transcript record are
// the app's to write, and the next session inherits the file, not this pin.
package input

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/setting"
)

// AdvisorScope names where an Advisor panel save goes. It mirrors
// setting.Scope and adds the session, which has no settings file.
type AdvisorScope string

const (
	AdvisorScopeUser    AdvisorScope = "user"
	AdvisorScopeProject AdvisorScope = "project"
	AdvisorScopeSession AdvisorScope = "session"
)

// AdvisorConfigSavedMsg reports a saved Advisor config. The panel has already
// written the user or project file; the app owns the rest — the live session
// config (which is also the seed for the next session's session scope) and, for
// a session save, the transcript record that keeps the pin across a resume.
type AdvisorConfigSavedMsg struct {
	Scope  AdvisorScope
	Config setting.AdvisorSettings
}

// advisorPanel is the /settings Advisor tab. It is a configForm over
// setting.AdvisorSettings plus the session's live config, which the app pushes
// in before the popup opens.
type advisorPanel struct {
	form configForm[setting.AdvisorSettings]
	// live is this session's effective advisor config. It is where the working
	// copy starts when the app supplies it, because that — not the file — is
	// what the session is actually running with.
	live *setting.AdvisorSettings

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
		scopes:    []configScope{scopeUser, scopeProject, scopeSession},
		seed:      func(data *setting.Data) setting.AdvisorSettings { return data.Advisor.Clone() },
		equal:     func(a, b setting.AdvisorSettings) bool { return a.Equal(b) },
		validate:  func(a setting.AdvisorSettings) error { return a.Validate() },
		persist: func(a setting.AdvisorSettings, scope configScope) error {
			if scope == scopeSession {
				return nil // no file: the message carries it
			}
			return setting.UpdateAdvisorAt(a, setting.Scope(scope))
		},
		saved: func(a setting.AdvisorSettings, scope configScope) tea.Msg {
			return AdvisorConfigSavedMsg{Scope: AdvisorScope(scope), Config: a.Clone()}
		},
	}
	return p
}

func (p *advisorPanel) Title() string { return "advisor" }

// Enter seeds the working copy: this session's live config when the app pushed
// one, the settings file otherwise.
func (p *advisorPanel) Enter() {
	p.picking = false
	p.form.Enter()
	if p.live != nil {
		p.form.snap = p.live.Clone()
		p.form.baseline = p.live.Clone()
		p.form.cursor = findEditable(advisorRows, 0, +1, 0)
	}
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
			if ref != p.form.snap.Model.Model {
				// The reasoning rung belongs to the model that was picked
				// before; a new model starts from its own default.
				p.form.snap.Model.ThinkingEffort = ""
			}
			p.form.snap.Model.Model = ref
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
		return p.pick.render(width, p.form.snap.Model.Model)
	}
	note := mutedNote(footerNote, width)
	// The footer's own rows come out of the body's budget, so a wrapped note on
	// a narrow card does not push the form past the popup.
	p.form.visible = max(height-3-strings.Count(note, "\n"), 0)
	body := p.form.Render(width)
	body += "\n" + note
	return body
}

// footerNote is the one line that points past this panel. The knobs it names
// are real and documented; they are just not worth a row each.
const footerNote = "Triggers, block policy and bounds keep their defaults — set them in settings.json."

// openModelPick opens the model sub-view on the connected providers.
func (p *advisorPanel) openModelPick() {
	var refs []string
	if p.models != nil {
		refs = p.models()
	}
	p.pick.begin(refs, p.form.snap.Model.Model)
	p.picking = true
}

// SetModelSource wires the picker's list of connected "vendor/model" refs.
func (p *advisorPanel) SetModelSource(fn func() []string) { p.models = fn }

// SetLive points the panel at this session's effective config. The app calls it
// before opening /settings, so a session that pinned its own advisor config
// edits that pin rather than the file it no longer follows.
func (p *advisorPanel) SetLive(cfg setting.AdvisorSettings) {
	cloned := cfg.Clone()
	p.live = &cloned
}

// ── Rows ────────────────────────────────────────────────────────────────

// advisorFlag takes a bool's address: every toggle writes an explicit pointer,
// so an explicit "off" round-trips through settings.json instead of reading as
// "unset, therefore the default".
func advisorFlag(on bool) *bool { return &on }

// advisorBoolRow builds a checkbox row from the resolved value and the knob it
// writes. getter is a resolver on AdvisorSettings, never a raw pointer: the
// panel shows the behavior the advisor actually has.
func advisorBoolRow(label, note string, getter func(setting.AdvisorSettings) bool,
	set func(*setting.AdvisorSettings, bool)) configRow[setting.AdvisorSettings] {
	return configRow[setting.AdvisorSettings]{
		kind:       rowBool,
		label:      label,
		note:       note,
		indent:     2,
		boolGetter: func(s *setting.AdvisorSettings) bool { return getter(*s) },
		toggle:     func(s *setting.AdvisorSettings) { set(s, !getter(*s)) },
	}
}

// advisorModelEntry is the rowEntry id that opens the model picker.
const advisorModelEntry = "model"

// advisorRows is the panel body: the three decisions the advisor needs from a
// person. Turning it on is the first row so the feature switch is never buried;
// the rest describe what an enabled advisor does.
var advisorRows = []configRow[setting.AdvisorSettings]{
	{kind: rowSubHeader, label: "Review"},
	advisorBoolRow("Advisor", "independent review of the result",
		setting.AdvisorSettings.Enabled,
		func(s *setting.AdvisorSettings, on bool) { s.Enable = advisorFlag(on) }),
	{
		kind:    rowEntry,
		label:   "Model",
		entryID: advisorModelEntry,
		indent:  2,
		desc:    "who reviews",
		summary: func(s *setting.AdvisorSettings) string { return advisorModelLabel(s) },
	},
	{
		kind:      rowChoice,
		label:     "Depth",
		note:      "fast · standard · deep",
		indent:    2,
		choices:   []string{setting.AdvisorModeFast, setting.AdvisorModeStandard, setting.AdvisorModeDeep},
		getChoice: func(s *setting.AdvisorSettings) string { return s.Mode() },
		setChoice: func(s *setting.AdvisorSettings, v string) { s.Review.Mode = v },
	},
	{kind: rowSpacer},
	{kind: rowSave, label: "Save"},
}
