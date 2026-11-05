// /settings Advisor panel: the global review layer's configuration, in six
// zones — on/off, triggers, block policy, bounds, review, display — with the
// same user / project / session save control the other panels use, plus the one
// scope only the advisor has: this session.
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
	p.form.Enter()
	if p.live != nil {
		p.form.snap = p.live.Clone()
		p.form.baseline = p.live.Clone()
		p.form.cursor = findEditable(advisorRows, 0, +1, 0)
	}
}

func (p *advisorPanel) Dirty() bool           { return p.form.Dirty() }
func (p *advisorPanel) HintLine() string      { return p.form.HintLine() }
func (p *advisorPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	cmd, done, _ := p.form.HandleKey(msg)
	return cmd, done
}

func (p *advisorPanel) Render(width, height int) string {
	p.form.visible = max(height-1, 0)
	return p.form.Render(width)
}

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

// advisorIntRow builds a bounded number row.
func advisorIntRow(label, unit string, get func(setting.AdvisorSettings) int,
	set func(*setting.AdvisorSettings, int), footnote func(int) string) configRow[setting.AdvisorSettings] {
	return configRow[setting.AdvisorSettings]{
		kind:      rowInt,
		label:     label,
		unit:      unit,
		indent:    2,
		intGetter: func(s *setting.AdvisorSettings) int { return get(*s) },
		intSetter: func(s *setting.AdvisorSettings, v int) { set(s, v) },
		intMin:    0,
		intMax:    9999,
		footnote:  footnote,
	}
}

// advisorTextRow builds a free-text row.
func advisorTextRow(label, placeholder string, get func(setting.AdvisorSettings) string,
	set func(*setting.AdvisorSettings, string)) configRow[setting.AdvisorSettings] {
	return configRow[setting.AdvisorSettings]{
		kind:        rowText,
		label:       label,
		indent:      2,
		placeholder: placeholder,
		strGetter:   func(s *setting.AdvisorSettings) string { return get(*s) },
		strSetter:   func(s *setting.AdvisorSettings, v string) { set(s, v) },
	}
}

// advisorRows is the panel body. Turning the advisor on is the first row so the
// feature switch is never buried: every other row describes what an enabled
// advisor does.
var advisorRows = []configRow[setting.AdvisorSettings]{
	{kind: rowSubHeader, label: "On / off"},
	advisorBoolRow("Advisor", "independent review of what the agent produced",
		setting.AdvisorSettings.Enabled,
		func(s *setting.AdvisorSettings, on bool) { s.Enable = advisorFlag(on) }),
	advisorBoolRow("Run in background", "keep reviews off the agent's own turn",
		setting.AdvisorSettings.RunsInBackground,
		func(s *setting.AdvisorSettings, on bool) { s.RunInBackground = advisorFlag(on) }),
	{kind: rowSpacer},

	{kind: rowSubHeader, label: "Triggers"},
	advisorBoolRow("After a subagent task", "", setting.AdvisorSettings.AfterSubagentTask,
		func(s *setting.AdvisorSettings, on bool) { s.Triggers.AfterSubagentTask = advisorFlag(on) }),
	advisorBoolRow("Before a merge or push", "", setting.AdvisorSettings.BeforeMerge,
		func(s *setting.AdvisorSettings, on bool) { s.Triggers.BeforeMerge = advisorFlag(on) }),
	advisorBoolRow("Before a plan step is done", "", setting.AdvisorSettings.BeforePlanStep,
		func(s *setting.AdvisorSettings, on bool) { s.Triggers.BeforePlanStep = advisorFlag(on) }),
	advisorBoolRow("On a large change", "", setting.AdvisorSettings.OnLargeChanges,
		func(s *setting.AdvisorSettings, on bool) { s.Triggers.OnLargeChanges = advisorFlag(on) }),
	advisorBoolRow("On a sensitive path", "", setting.AdvisorSettings.SensitivePaths,
		func(s *setting.AdvisorSettings, on bool) { s.Triggers.SensitivePaths = advisorFlag(on) }),
	{kind: rowSpacer},

	{kind: rowSubHeader, label: "Block policy"},
	advisorBoolRow("Merge on P0", "", setting.AdvisorSettings.BlocksMergeOnP0,
		func(s *setting.AdvisorSettings, on bool) { s.Blocks.MergeOnP0 = advisorFlag(on) }),
	advisorBoolRow("Merge on P1", "", setting.AdvisorSettings.BlocksMergeOnP1,
		func(s *setting.AdvisorSettings, on bool) { s.Blocks.MergeOnP1 = advisorFlag(on) }),
	advisorBoolRow("Task stays open on P0", "", setting.AdvisorSettings.BlocksTaskOnP0,
		func(s *setting.AdvisorSettings, on bool) { s.Blocks.TaskCompletionOnP0 = advisorFlag(on) }),
	advisorBoolRow("Plan step stays open on P0", "", setting.AdvisorSettings.BlocksPlanStepOnP0,
		func(s *setting.AdvisorSettings, on bool) { s.Blocks.PlanStepCompletionOnP0 = advisorFlag(on) }),
	advisorBoolRow("Ask on P1", "put a P1 to you before continuing", setting.AdvisorSettings.RequiresConfirmationOnP1,
		func(s *setting.AdvisorSettings, on bool) { s.Blocks.RequireConfirmationOnP1 = advisorFlag(on) }),
	advisorBoolRow("Allow proceed with risk", "let you go past a block after confirming",
		setting.AdvisorSettings.AllowProceedWithRisk,
		func(s *setting.AdvisorSettings, on bool) { s.Blocks.AllowProceedWithRisk = advisorFlag(on) }),
	{kind: rowSpacer},

	{kind: rowSubHeader, label: "Bounds"},
	advisorIntRow("Large change: files", "files",
		func(s setting.AdvisorSettings) int { return s.Thresholds.MinChangedFiles },
		func(s *setting.AdvisorSettings, v int) { s.Thresholds.MinChangedFiles = v },
		func(v int) string { return zeroMeans(v, "any count") }),
	advisorIntRow("Large change: lines", "lines",
		func(s setting.AdvisorSettings) int { return s.Thresholds.MinChangedLines },
		func(s *setting.AdvisorSettings, v int) { s.Thresholds.MinChangedLines = v },
		func(v int) string { return zeroMeans(v, "any count") }),
	advisorIntRow("Max diff size", "KB",
		func(s setting.AdvisorSettings) int { return s.Thresholds.MaxDiffBytes / 1024 },
		func(s *setting.AdvisorSettings, v int) { s.Thresholds.MaxDiffBytes = v * 1024 },
		func(v int) string { return zeroMeans(v, "256 KB") }),
	advisorIntRow("Cooldown per target", "seconds",
		func(s setting.AdvisorSettings) int { return s.Thresholds.CooldownSeconds },
		func(s *setting.AdvisorSettings, v int) { s.Thresholds.CooldownSeconds = v },
		func(v int) string { return zeroMeans(v, "60 s") }),
	{kind: rowSpacer},

	{kind: rowSubHeader, label: "Review"},
	{
		kind:  rowChoice,
		label: "Depth",
		note:  "fast · standard · deep",
		indent: 2,
		choices: []string{setting.AdvisorModeFast, setting.AdvisorModeStandard, setting.AdvisorModeDeep},
		getChoice: func(s *setting.AdvisorSettings) string { return s.Mode() },
		setChoice: func(s *setting.AdvisorSettings, v string) { s.Review.Mode = v },
	},
	advisorTextRow("Model", "default (session model)",
		func(s setting.AdvisorSettings) string { return s.Model.Model },
		func(s *setting.AdvisorSettings, v string) { s.Model.Model = v }),
	advisorTextRow("Thinking effort", "default",
		func(s setting.AdvisorSettings) string { return s.Model.ThinkingEffort },
		func(s *setting.AdvisorSettings, v string) { s.Model.ThinkingEffort = v }),
	advisorIntRow("Max tokens", "tokens",
		func(s setting.AdvisorSettings) int { return s.Model.MaxTokens },
		func(s *setting.AdvisorSettings, v int) { s.Model.MaxTokens = v },
		func(v int) string { return zeroMeans(v, "4000") }),
	advisorIntRow("Timeout", "seconds",
		func(s setting.AdvisorSettings) int { return s.Model.TimeoutSeconds },
		func(s *setting.AdvisorSettings, v int) { s.Model.TimeoutSeconds = v },
		func(v int) string { return zeroMeans(v, "180 s") }),
	advisorIntRow("Max findings", "findings",
		func(s setting.AdvisorSettings) int { return s.Model.MaxFindings },
		func(s *setting.AdvisorSettings, v int) { s.Model.MaxFindings = v },
		func(v int) string { return zeroMeans(v, "20") }),
	{kind: rowSpacer},

	{kind: rowSubHeader, label: "Display"},
	advisorBoolRow("Status bar badge", "", setting.AdvisorSettings.ShowStatusBar,
		func(s *setting.AdvisorSettings, on bool) { s.Display.StatusBar = advisorFlag(on) }),
	advisorBoolRow("Hide P3", "keep minor findings out of the panel list",
		setting.AdvisorSettings.HidesP3,
		func(s *setting.AdvisorSettings, on bool) { s.Display.HideP3 = advisorFlag(on) }),
	{
		kind:  rowChoice,
		label: "Group findings by",
		indent: 2,
		choices: []string{
			setting.AdvisorGroupBySeverity, setting.AdvisorGroupByFile, setting.AdvisorGroupByTask,
		},
		getChoice: func(s *setting.AdvisorSettings) string { return s.Grouping() },
		setChoice: func(s *setting.AdvisorSettings, v string) { s.Display.GroupBy = v },
	},
	{kind: rowSpacer},

	{kind: rowSave, label: "Save"},
}

// zeroMeans renders the footnote of a bounded number: zero is "use the default",
// which the row states rather than showing a bare 0.
func zeroMeans(v int, fallback string) string {
	if v == 0 {
		return "default: " + fallback
	}
	return ""
}
