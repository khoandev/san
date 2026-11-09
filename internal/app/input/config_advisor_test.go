// The /settings Advisor panel: the on/off switch, the model picker, the scope
// control, the dirty indicator and the save that hands the config to the app.
//
// The panel itself never touches the session — it writes the user/project file
// and emits AdvisorConfigSavedMsg. These tests pin that boundary down.
package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/setting"
)

// newTestAdvisorPanel builds the Advisor panel with settings=nil, so Enter
// seeds the zero config and touches no disk.
func newTestAdvisorPanel(t *testing.T) *advisorPanel {
	t.Helper()
	p := newAdvisorPanel(nil)
	p.Enter()
	return p
}

// moveToSave walks the cursor down to the Save row, the same way a user does.
func moveToSave(t *testing.T, p *advisorPanel) {
	t.Helper()
	rows := p.form.rowsFn()
	for range len(rows) {
		if rows[p.form.cursor].kind == rowSave {
			return
		}
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatal("the Save row is unreachable")
}

// The Advisor tab is part of /settings, between Permissions and General.
func TestAdvisorTabIsInSettings(t *testing.T) {
	c := NewSettingsSelector(nil)
	found := false
	for _, p := range c.panels {
		if p.Title() == "advisor" {
			found = true
		}
	}
	if !found {
		t.Fatal("the /settings popup has no advisor tab")
	}
}

// Entering seeds the working copy and the unsaved-edits marker follows it.
func TestAdvisorPanelToggleMarksDirty(t *testing.T) {
	p := newTestAdvisorPanel(t)
	if p.form.snap.Enabled() {
		t.Fatal("the zero config should start with the advisor off")
	}
	if p.Dirty() {
		t.Fatal("a freshly entered panel should be clean")
	}

	p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace}) // Advisor on/off
	if !p.form.snap.Enabled() {
		t.Fatal("space should turn the advisor on")
	}
	if !p.Dirty() {
		t.Fatal("an edited panel should be dirty")
	}

	p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace}) // back to the baseline
	if p.Dirty() {
		t.Fatal("back on the baseline the panel should be clean")
	}
}

// ←→ cycles the save scope through user and project, wrapping at either end.
func TestAdvisorPanelCyclesScopes(t *testing.T) {
	p := newTestAdvisorPanel(t)
	if p.form.scope != scopeUser {
		t.Fatalf("scope = %q, want the default user", p.form.scope)
	}
	steps := []struct {
		key  tea.KeyMsg
		want configScope
	}{
		{tea.KeyPressMsg{Code: tea.KeyRight}, scopeProject},
		{tea.KeyPressMsg{Code: tea.KeyRight}, scopeUser},   // wrap
		{tea.KeyPressMsg{Code: tea.KeyLeft}, scopeProject}, // wrap back
		{tea.KeyPressMsg{Code: tea.KeyLeft}, scopeUser},
	}
	for i, step := range steps {
		p.HandleKey(step.key)
		if p.form.scope != step.want {
			t.Fatalf("step %d: scope = %q, want %q", i, p.form.scope, step.want)
		}
	}
}

// A user-scope save writes the block to the user settings file and emits the
// message the app applies; the panel itself closes.
func TestAdvisorPanelSaveWritesTheUserFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	p := newTestAdvisorPanel(t)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace}) // advisor on
	moveToSave(t, p)

	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatal("saving should ask the popup to close")
	}
	if cmd == nil {
		t.Fatal("saving should emit AdvisorConfigSavedMsg")
	}
	msg, ok := cmd().(AdvisorConfigSavedMsg)
	if !ok {
		t.Fatalf("message = %#v, want AdvisorConfigSavedMsg", cmd())
	}
	if msg.Scope != AdvisorScopeUser || !msg.Config.Enabled() {
		t.Fatalf("message = %+v, want a saved user config with the advisor on", msg)
	}

	path := filepath.Join(home, ".san", "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the user settings file was not written: %v", err)
	}
	var data struct {
		Advisor struct {
			Enable *bool `json:"enable"`
		} `json:"advisor"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("settings file is not valid JSON: %v\n%s", err, raw)
	}
	if data.Advisor.Enable == nil || !*data.Advisor.Enable {
		t.Fatalf("persisted advisor enable = %v, want true\n%s", data.Advisor.Enable, raw)
	}
}

// A project-scope save targets the workspace's .san directory.
func TestAdvisorPanelSaveWritesTheProjectFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())

	p := newTestAdvisorPanel(t)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace}) // advisor on
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // user → project
	if p.form.scope != scopeProject {
		t.Fatalf("scope = %q, want project", p.form.scope)
	}
	moveToSave(t, p)
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || cmd == nil {
		t.Fatal("saving at project scope should close and emit a message")
	}
	if msg, ok := cmd().(AdvisorConfigSavedMsg); !ok || msg.Scope != AdvisorScopeProject {
		t.Fatalf("message = %#v, want a project-scope save", cmd())
	}

	if _, err := os.Stat(filepath.Join(".san", "settings.json")); err != nil {
		t.Fatalf("the project settings file was not written: %v", err)
	}
}

// The panel renders at every width the popup can give it, and names its two
// decisions plus the scope control.
func TestAdvisorSettingsPanelRendersAtEveryWidth(t *testing.T) {
	p := newTestAdvisorPanel(t)
	for _, width := range []int{40, 60, 80, 120} {
		body := p.Render(width, 30)
		if body == "" {
			t.Fatalf("width %d rendered nothing", width)
		}
		if strings.Contains(body, "\x00") {
			t.Fatalf("width %d rendered a NUL", width)
		}
	}
	body := p.Render(100, 60)
	for _, want := range []string{"REVIEW", "Advisor", "MODEL", "session model"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel body is missing %q", want)
		}
	}
	for _, scope := range []string{"user", "project"} {
		if !strings.Contains(body, scope) {
			t.Errorf("panel body is missing scope %q", scope)
		}
	}
}

// Every row stays inside the narrowest card the popup hands a panel: the notes
// that do not fit are dropped rather than clipped.
func TestAdvisorPanelFitsTheNarrowestCard(t *testing.T) {
	p := newTestAdvisorPanel(t)
	for _, width := range []int{40, 60, 120} {
		for _, line := range strings.Split(p.Render(width, 30), "\n") {
			line = strings.TrimRight(line, " ")
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d: row is %d cols wide: %q", width, got, line)
			}
		}
	}
}

// The model row opens a picker over the connected providers and stores the pick
// as a "vendor/model" ref the app resolves through resolveJudge.
func TestAdvisorPanelPicksAModel(t *testing.T) {
	p := newTestAdvisorPanel(t)
	p.SetModelSource(func() []string {
		return []string{"anthropic/claude-sonnet-4-5", "anthropic/claude-haiku-4-5", "openai/gpt-5"}
	})

	// The row after the Advisor toggle is Model.
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.picking {
		t.Fatal("enter on the Model row should open the picker")
	}
	if crumb := p.Breadcrumb(); crumb != "Model" {
		t.Fatalf("breadcrumb = %q, want Model", crumb)
	}

	// Provider list: session model, then each connected provider.
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // session model → anthropic
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if crumb := p.Breadcrumb(); crumb != "Model › anthropic" {
		t.Fatalf("breadcrumb = %q, want the provider's models", crumb)
	}
	// Anthropic's models are listed as given: sonnet, then haiku.
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if p.picking {
		t.Fatal("a completed pick closes the picker")
	}
	if got := p.form.snap.Model; got != "anthropic/claude-haiku-4-5" {
		t.Fatalf("model = %q, want the picked ref", got)
	}
	if !p.Dirty() {
		t.Fatal("a changed model should mark the panel dirty")
	}
}

// Esc backs out of the model stage and then the picker, without touching the
// config.
func TestAdvisorPanelModelPickerEscBacksOut(t *testing.T) {
	p := newTestAdvisorPanel(t)
	p.SetModelSource(func() []string { return []string{"openai/gpt-5"} })
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // open the picker
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})  // session model → openai
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // model stage
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !p.picking {
		t.Fatal("esc in the model stage should return to the provider list")
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if p.picking {
		t.Fatal("esc on the provider list should close the picker")
	}
	if p.form.snap.Model != "" {
		t.Fatalf("model = %q, want it untouched", p.form.snap.Model)
	}
}

// The planner seeds the panel from the settings file it was built over, which
// is what an existing config looks like when /settings opens.
func TestAdvisorPanelSeedsFromTheSettingsFile(t *testing.T) {
	on := true
	settings := setting.New(&setting.Data{Advisor: setting.AdvisorSettings{Enable: &on, Model: "openai/gpt-5"}})
	p := newAdvisorPanel(settings)
	p.Enter()

	if !p.form.snap.Enabled() || p.form.snap.Model != "openai/gpt-5" {
		t.Fatalf("panel seeded %+v, want the file's config", p.form.snap)
	}
	if p.Dirty() {
		t.Fatal("a panel seeded from the file starts clean")
	}
}
