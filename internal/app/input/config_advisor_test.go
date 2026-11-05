// The /settings Advisor panel: the live session seed, the scope control, the
// dirty indicator and the save that hands the config to the app.
//
// The panel itself never touches the session or the transcript — it writes
// the user/project file and emits AdvisorConfigSavedMsg. These tests pin that
// boundary down, so the app-level tests can own the rest.
package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// The shell hands the panel this session's config before it opens, so even a
// panel reached by switching tabs edits the live pin rather than the file.
func TestAdvisorPanelTakesTheLiveConfigFromThePopup(t *testing.T) {
	c := NewSettingsSelector(nil)
	off := false
	c.SetAdvisorConfig(setting.AdvisorSettings{Enable: &off})
	c.Enter(120, 40)
	c.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyTab}) // appearance → permissions
	c.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyTab}) // permissions → advisor
	p, ok := c.ActivePanel().(*advisorPanel)
	if !ok {
		t.Fatalf("active panel = %T, want the advisor panel", c.ActivePanel())
	}
	if p.form.snap.Enabled() {
		t.Fatal("the panel ignored the config the shell pushed in")
	}
}

// The panel edits this session's live config — the pin it is running with —
// not the file it was seeded from.
func TestAdvisorPanelSeedsFromTheLiveSession(t *testing.T) {
	on := true
	settings := setting.New(&setting.Data{Advisor: setting.AdvisorSettings{Enable: &on}})
	p := newAdvisorPanel(settings)
	off := false
	p.SetLive(setting.AdvisorSettings{Enable: &off})
	p.Enter()

	if p.form.snap.Enabled() {
		t.Fatal("the panel ignored the session's live config")
	}
	if p.Dirty() {
		t.Fatal("a panel seeded from the live config starts clean")
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

// ←→ cycles the save scope through user, project and this session, wrapping at
// either end.
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
		{tea.KeyPressMsg{Code: tea.KeyRight}, scopeSession},
		{tea.KeyPressMsg{Code: tea.KeyRight}, scopeUser},   // wrap
		{tea.KeyPressMsg{Code: tea.KeyLeft}, scopeSession}, // wrap back
		{tea.KeyPressMsg{Code: tea.KeyLeft}, scopeProject},
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

// A session-scope save writes no file: the message carries the pin, the app
// applies it and the transcript keeps it.
func TestAdvisorPanelSessionSaveWritesNoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	p := newTestAdvisorPanel(t)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace}) // advisor on
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // user → project
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // project → session
	if p.form.scope != scopeSession {
		t.Fatalf("scope = %q, want session", p.form.scope)
	}
	moveToSave(t, p)
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || cmd == nil {
		t.Fatal("saving at session scope should close and emit a message")
	}
	msg, ok := cmd().(AdvisorConfigSavedMsg)
	if !ok || msg.Scope != AdvisorScopeSession || !msg.Config.Enabled() {
		t.Fatalf("message = %#v, want a session pin with the advisor on", cmd())
	}
	if _, err := os.Stat(filepath.Join(home, ".san", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("a session save must not write a file (stat err = %v)", err)
	}
}

// The panel renders at every width the popup can give it, names its sections,
// and shows the scope control.
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
	// Sections are upper-cased by the form; the first is on/off.
	for _, want := range []string{"ON / OFF", "TRIGGERS", "BLOCK POLICY", "BOUNDS", "REVIEW", "DISPLAY"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel body is missing section %q", want)
		}
	}
	for _, scope := range []string{"user", "project", "session"} {
		if !strings.Contains(body, scope) {
			t.Errorf("panel body is missing scope %q", scope)
		}
	}
}
