// The /settings agents tab: one role row for the advisor and one per bundled
// agent, the role picker behind them, the scope control, the dirty indicator and
// the save that writes both blocks.
//
// The panel never touches the session — it writes the user/project file and
// emits AgentsConfigSavedMsg. These tests pin that boundary down, along with
// the rule that a row shows the model its role resolves to.
package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// newTestAgentsPanel builds the agents panel with settings=nil, so Enter seeds
// the zero config and touches no disk.
func newTestAgentsPanel(t *testing.T) *agentsPanel {
	t.Helper()
	p := newAgentsPanel(nil)
	p.Enter()
	return p
}

// testRoleSource answers the two questions the panel asks with fixed models, so
// the row labels are assertable without a provider store.
func testRoleSource() AgentRoleSource {
	models := map[string]string{"fast": "haiku", "standard": "worker", "deep": "opus", "default": "session-model"}
	return AgentRoleSource{
		Resolve: func(role string) (string, string) {
			model, ok := models[role]
			if !ok {
				return "", ""
			}
			if role == "deep" {
				return model, "high"
			}
			return model, ""
		},
		Effective: func(agent string) (string, string, string) {
			switch agent {
			case setting.AgentQuick, setting.AgentExplore:
				return "fast", "haiku", ""
			case setting.AgentTask:
				return "standard", "worker", ""
			case setting.AgentReviewer:
				return "deep", "opus", "high"
			}
			return "", "", ""
		},
	}
}

// moveToRow walks the cursor to the row carrying entryID, up to the top of the
// list first, the same way a user does.
func moveToRow(t *testing.T, p *agentsPanel, entryID string) {
	t.Helper()
	for range p.form.rowsFn() {
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	rows := p.form.rowsFn()
	for range rows {
		row := rows[p.form.cursor]
		if row.kind == rowEntry && row.entryID == entryID {
			return
		}
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatalf("the %q row is unreachable", entryID)
}

// moveToSave walks the cursor down to the Save row.
func moveToSave(t *testing.T, p *agentsPanel) {
	t.Helper()
	for range p.form.rowsFn() {
		if p.form.rowsFn()[p.form.cursor].kind == rowSave {
			return
		}
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatal("the Save row is unreachable")
}

// The roles live in /settings's Agent tab, between Appearance and General, and
// no separate Permissions tab remains: the gate is a row inside Agent.
func TestAgentsTabIsInSettings(t *testing.T) {
	c := NewSettingsSelector(nil)
	var titles []string
	for _, p := range c.panels {
		titles = append(titles, p.Title())
	}
	if got := strings.Join(titles, ","); got != "appearance,agent,general" {
		t.Fatalf("/settings tabs = %q, want appearance,agent,general", got)
	}
	for _, gone := range []string{"advisor", "permissions", "agents"} {
		if slicesContains(titles, gone) {
			t.Fatalf("%q is still a top-level tab: %v", gone, titles)
		}
	}
	if _, ok := c.panels[1].(*agentPanel); !ok {
		t.Fatalf("the middle tab is %T, want the merged agent tab", c.panels[1])
	}
}

// The merged tab keeps both halves reachable from one row list: the gate row
// leads, every agent role row follows, and the roles keep their user/project
// scope control.
func TestAgentTabHostsTheGateAndTheRoles(t *testing.T) {
	tab := newAgentPanel(nil)
	tab.Enter()
	if got := tab.Title(); got != "agent" {
		t.Fatalf("tab title = %q, want agent", got)
	}

	rows := tab.agents.form.rowsFn()
	if rows[1].kind != rowEntry || rows[1].entryID != permissionsEntry {
		t.Fatalf("rows[1] = %+v, want the gate entry row", rows[1])
	}
	if got := rows[1].summary(nil); got != "allowed" {
		t.Fatalf("gate summary = %q, want allowed", got)
	}
	entryIDs := map[string]bool{}
	for _, row := range rows {
		if row.kind == rowEntry {
			entryIDs[row.entryID] = true
		}
	}
	for _, agent := range managedAgents {
		if !entryIDs[agent] {
			t.Fatalf("agent %q has no role row: %v", agent, entryIDs)
		}
	}
	if scopes := tab.agents.form.scopesOrDefault(); len(scopes) != 2 || scopes[0] != scopeUser || scopes[1] != scopeProject {
		t.Fatalf("role form scopes = %v, want user then project", scopes)
	}
	hasSave := false
	for _, row := range rows {
		hasSave = hasSave || row.kind == rowSave
	}
	if !hasSave {
		t.Fatalf("the merged tab lost its Save row")
	}

	// The body shows the gate above the roles, and the whole tab still fits a
	// 24-row terminal.
	body := ansi.Strip(tab.Render(70, 20))
	for _, want := range []string{"PERMISSIONS", "YOLO mode", "ADVISOR", "AGENTS"} {
		if !strings.Contains(body, want) {
			t.Fatalf("tab body is missing %q: %q", want, body)
		}
	}
	c := NewSettingsSelector(nil)
	c.Enter(80, 24)
	c.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyTab}) // Agent
	if got := lipgloss.Height(c.Render()); got > 24 {
		t.Fatalf("Agent tab height = %d, exceeds a 24-row terminal", got)
	}
	if got := ansi.Strip(c.Render()); !strings.Contains(got, "AGENT") || !strings.Contains(got, "GENERAL") {
		t.Fatalf("the tab strip is missing a tab: %q", got)
	}

	// Enter on the leading row opens the gate; it stays inside the tab, and
	// esc backs out instead of closing /settings.
	if _, done := tab.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done || !tab.inPerms {
		t.Fatalf("enter on the gate row: done=%v inPerms=%v, want a sub-view inside the tab", done, tab.inPerms)
	}
	if !tab.Modal() || tab.Breadcrumb() != "permissions" {
		t.Fatalf("gate sub-view: modal=%v crumb=%q", tab.Modal(), tab.Breadcrumb())
	}
	if tab.agents.form.Dirty() {
		t.Fatalf("the roles must stay clean while the gate sub-view is open")
	}
	tab.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if tab.inPerms {
		t.Fatalf("esc should leave the gate sub-view")
	}
}

// Locking the gate from inside the merged tab writes allowBypass=false and
// keeps /settings open, so the new state is visible where it was set.
func TestAgentTabGateSavesWithoutClosingThePopup(t *testing.T) {
	home := tempHome(t)

	tab := newAgentPanel(nil)
	tab.Enter()
	tab.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // open the gate
	if !tab.inPerms {
		t.Fatalf("the gate sub-view did not open")
	}
	tab.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Allowed → Locked
	if !tab.Dirty() {
		t.Fatalf("moving off the baseline should mark the tab dirty")
	}
	cmd, done := tab.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done {
		t.Fatalf("a gate save must not dismiss /settings")
	}
	if tab.inPerms {
		t.Fatalf("a gate save should return to the tab body")
	}
	msg, ok := cmd().(AllowBypassSavedMsg)
	if !ok || msg.Allowed {
		t.Fatalf("expected AllowBypassSavedMsg{Allowed:false}, got %#v", cmd())
	}
	if got := tab.gateSummary(); got != "locked" {
		t.Fatalf("gate summary = %q, want locked", got)
	}
	if data := readPersistedAllowBypass(t, home); data == nil || *data {
		t.Fatalf("persisted allowBypass = %v, want false", data)
	}
}

// slicesContains is a tiny helper so the test reads as one assertion.
func slicesContains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// The panel seeds the role map and the advisor block from settings, and starts
// clean.
func TestAgentsPanelSeedsFromSettings(t *testing.T) {
	on := true
	settings := setting.New(&setting.Data{
		Advisor: setting.AdvisorSettings{Enable: &on},
		Agents:  setting.AgentsSettings{Roles: map[string]string{setting.AgentReviewer: "fast"}},
	})
	p := newAgentsPanel(settings)
	p.Enter()

	if !p.form.snap.Advisor.Enabled() {
		t.Fatal("panel seeded the advisor off, want the file's config")
	}
	if got := p.form.snap.Roles[setting.AgentReviewer]; got != "fast" {
		t.Fatalf("seeded reviewer role = %q, want fast", got)
	}
	if p.Dirty() {
		t.Fatal("a panel seeded from the file starts clean")
	}
	// The seeded map is a clone: editing the form must not write into the
	// settings service's copy.
	p.form.snap.Roles[setting.AgentReviewer] = "deep"
	if got := settings.AgentRole(setting.AgentReviewer); got != "fast" {
		t.Fatalf("the panel edits live settings storage: %q", got)
	}
}

// Every role row is labelled with the role in effect and the model it resolves
// to, which is the point of the tab.
func TestAgentsPanelRowsShowTheResolvedModel(t *testing.T) {
	p := newTestAgentsPanel(t)
	p.SetRoleSource(testRoleSource())

	body := p.Render(100, 40)
	for _, want := range []string{
		"ADVISOR", "ROLE", "AGENTS", "QUICK", "EXPLORE", "TASK", "REVIEWER",
		"@deep → opus · high", // the advisor's default role, resolved
		"@fast → haiku",       // quick and explore declare the fast role
		"@standard → worker",  // task declares the standard role
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel body is missing %q:\n%s", want, body)
		}
	}
}

// A definition whose file names a concrete model has no role to show, and an
// agent with no source at all falls back to the plain state rather than an
// invented role.
func TestAgentsPanelRowWithoutARole(t *testing.T) {
	p := newTestAgentsPanel(t)
	p.SetRoleSource(AgentRoleSource{
		Resolve:   func(string) (string, string) { return "", "" },
		Effective: func(string) (string, string, string) { return "", "", "" },
	})
	if body := p.Render(100, 40); !strings.Contains(body, "definition default") {
		t.Errorf("a model-pinned definition should say so:\n%s", body)
	}

	bare := newTestAgentsPanel(t)
	if body := bare.Render(100, 40); !strings.Contains(body, "definition default") {
		t.Errorf("a panel without a source should still name the state:\n%s", body)
	}
}

// Enter on a role row opens the picker; picking an assignment writes it into the
// working copy and marks the panel dirty.
func TestAgentsPanelAssignsARole(t *testing.T) {
	p := newTestAgentsPanel(t)
	p.SetRoleSource(testRoleSource())
	moveToRow(t, p, setting.AgentQuick)

	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.picking {
		t.Fatal("enter on an agent row should open the role picker")
	}
	if crumb := p.Breadcrumb(); crumb != "Role" {
		t.Fatalf("breadcrumb = %q, want Role", crumb)
	}
	if !p.Modal() {
		t.Fatal("the picker owns esc while it is open")
	}

	// The picker opens on "definition default"; step down to the ladder's first
	// role and pick it.
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // default
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.picking {
		t.Fatal("a completed pick closes the picker")
	}
	if got := p.form.snap.Roles[setting.AgentQuick]; got != string(llm.RoleDefault) {
		t.Fatalf("quick role = %q, want the picked default", got)
	}
	if !p.Dirty() {
		t.Fatal("a changed role should mark the panel dirty")
	}
	if body := p.Render(100, 40); !strings.Contains(body, "@default → session-model") {
		t.Errorf("the row does not show the picked role's model:\n%s", body)
	}
}

// The advisor's off entry switches it off and keeps the role, so turning it back
// on resumes the same model.
func TestAgentsPanelAdvisorOffKeepsTheRole(t *testing.T) {
	on := true
	settings := setting.New(&setting.Data{
		Advisor: setting.AdvisorSettings{Enable: &on},
		Agents:  setting.AgentsSettings{Roles: map[string]string{setting.AgentAdvisor: "fast"}},
	})
	p := newAgentsPanel(settings)
	p.SetRoleSource(testRoleSource())
	p.Enter()

	moveToRow(t, p, setting.AgentAdvisor)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.picking {
		t.Fatal("enter on the advisor row should open the role picker")
	}
	// The picker opens on the assigned role; Off is the first entry.
	for p.pick.cursor > 0 {
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if p.form.snap.Advisor.Enabled() {
		t.Fatal("picking off should switch the advisor off")
	}
	if got := p.form.snap.Roles[setting.AgentAdvisor]; got != "fast" {
		t.Fatalf("advisor role = %q, want it kept", got)
	}
	if !p.Dirty() {
		t.Fatal("switching the advisor off should mark the panel dirty")
	}

	// Choosing a role turns it back on.
	moveToRow(t, p, setting.AgentAdvisor)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // off → default
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.form.snap.Advisor.Enabled() {
		t.Fatal("choosing a role for the advisor should turn it on")
	}
	if got := p.form.snap.Roles[setting.AgentAdvisor]; got != string(llm.RoleDefault) {
		t.Fatalf("advisor role = %q, want the picked default", got)
	}
}

// Esc backs out of the picker without touching the working copy.
func TestAgentsPanelPickerEscChangesNothing(t *testing.T) {
	p := newTestAgentsPanel(t)
	p.SetRoleSource(testRoleSource())
	moveToRow(t, p, setting.AgentTask)

	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	before := len(p.form.snap.Roles)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})

	if p.picking {
		t.Fatal("esc should close the picker")
	}
	if len(p.form.snap.Roles) != before {
		t.Fatalf("esc wrote a role: %v", p.form.snap.Roles)
	}
	if p.Dirty() {
		t.Fatal("esc should leave the panel clean")
	}
}

// A save writes the roles and the advisor block together and leaves the rest of
// the file alone.
func TestAgentsPanelSaveWritesBothBlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".san"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".san", "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","agents":{"autoTier":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newTestAgentsPanel(t)
	moveToRow(t, p, setting.AgentReviewer)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // definition default → default
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Turn the advisor off through its only switch.
	moveToRow(t, p, setting.AgentAdvisor)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // off

	moveToSave(t, p)
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatal("saving should ask the popup to close")
	}
	if cmd == nil {
		t.Fatal("saving should emit AgentsConfigSavedMsg")
	}
	msg, ok := cmd().(AgentsConfigSavedMsg)
	if !ok || msg.Scope != AgentsScopeUser {
		t.Fatalf("message = %#v, want a user-scope save", cmd())
	}
	if got := msg.Roles[setting.AgentReviewer]; got != string(llm.RoleDefault) {
		t.Fatalf("message roles = %v, want the picked default", msg.Roles)
	}
	if msg.Advisor.Enabled() {
		t.Fatal("message advisor = on, want the off state the panel saved")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the user settings file was not written: %v", err)
	}
	var data struct {
		Theme  string `json:"theme"`
		Agents struct {
			AutoTier *bool             `json:"autoTier"`
			Roles    map[string]string `json:"roles"`
		} `json:"agents"`
		Advisor struct {
			Enable *bool `json:"enable"`
		} `json:"advisor"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("settings file is not valid JSON: %v\n%s", err, raw)
	}
	if got := data.Agents.Roles[setting.AgentReviewer]; got != string(llm.RoleDefault) {
		t.Errorf("agents.roles.reviewer = %q, want the picked default\n%s", got, raw)
	}
	if data.Advisor.Enable == nil || *data.Advisor.Enable {
		t.Errorf("advisor enable = %v, want an explicit false\n%s", data.Advisor.Enable, raw)
	}
	// The tab does not own these: they must survive the save.
	if data.Theme != "dark" {
		t.Errorf("theme = %q, want it untouched", data.Theme)
	}
	if data.Agents.AutoTier == nil || *data.Agents.AutoTier {
		t.Errorf("agents.autoTier = %v, want it untouched", data.Agents.AutoTier)
	}
}

// A project-scope save targets the workspace's .san directory.
func TestAgentsPanelSaveWritesTheProjectFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())

	p := newTestAgentsPanel(t)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // user → project
	if p.form.scope != scopeProject {
		t.Fatalf("scope = %q, want project", p.form.scope)
	}
	moveToSave(t, p)
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || cmd == nil {
		t.Fatal("saving at project scope should close and emit a message")
	}
	if msg, ok := cmd().(AgentsConfigSavedMsg); !ok || msg.Scope != AgentsScopeProject {
		t.Fatalf("message = %#v, want a project-scope save", cmd())
	}
	if _, err := os.Stat(filepath.Join(".san", "settings.json")); err != nil {
		t.Fatalf("the project settings file was not written: %v", err)
	}
}

// ←→ cycles the save scope through user and project, wrapping at either end.
func TestAgentsPanelCyclesScopes(t *testing.T) {
	p := newTestAgentsPanel(t)
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

// The panel renders at every width the popup can give it, and shows the scope
// control the save needs.
func TestAgentsPanelRendersAtEveryWidth(t *testing.T) {
	p := newTestAgentsPanel(t)
	p.SetRoleSource(testRoleSource())
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
	for _, scope := range []string{"user", "project"} {
		if !strings.Contains(body, scope) {
			t.Errorf("panel body is missing scope %q", scope)
		}
	}
}

// Every row stays inside the narrowest card the popup hands a panel: the
// description goes before the summary, and a long summary is clipped rather than
// wrapped.
func TestAgentsPanelFitsTheNarrowestCard(t *testing.T) {
	p := newTestAgentsPanel(t)
	p.SetRoleSource(AgentRoleSource{
		Resolve: func(string) (string, string) {
			return "claude-sonnet-4-5-20250929-thinking", "xhigh"
		},
	})
	for _, width := range []int{40, 60, 120} {
		for _, line := range strings.Split(p.Render(width, 30), "\n") {
			line = strings.TrimRight(line, " ")
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d: row is %d cols wide: %q", width, got, line)
			}
		}
	}
}

// The picker offers the ladder, the entry that clears an assignment, and the
// custom roles modelRoles binds — sorted after the ladder.
func TestAgentsRolePickerChoices(t *testing.T) {
	settings := setting.New(&setting.Data{
		ModelRoles: map[string]string{"judge": "haiku", "fast": "haiku"},
	})
	p := newAgentsPanel(settings)
	p.SetRoleSource(testRoleSource())
	p.Enter()

	var labels []string
	for _, choice := range p.choicesFor(setting.AgentTask) {
		labels = append(labels, choice.label)
	}
	want := []string{"definition default", "@default", "@fast", "@standard", "@deep", "@judge"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Fatalf("choices = %v, want %v", labels, want)
	}

	// The advisor has no definition to fall back to: it is the one agent that
	// can be switched off instead.
	labels = nil
	for _, choice := range p.choicesFor(setting.AgentAdvisor) {
		labels = append(labels, choice.label)
	}
	if labels[0] != "off" {
		t.Fatalf("advisor choices = %v, want off first", labels)
	}
	for _, label := range labels {
		if label == "definition default" {
			t.Fatalf("the advisor should not offer a definition default: %v", labels)
		}
	}
}

// The picker opens on the entry in effect, so enter-then-enter is a no-op.
func TestAgentsRolePickerOpensOnTheCurrentChoice(t *testing.T) {
	settings := setting.New(&setting.Data{
		Agents: setting.AgentsSettings{Roles: map[string]string{setting.AgentTask: "deep"}},
	})
	p := newAgentsPanel(settings)
	p.SetRoleSource(testRoleSource())
	p.Enter()

	moveToRow(t, p, setting.AgentTask)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := p.pick.choices[p.pick.cursor].label; got != "@deep" {
		t.Fatalf("picker opened on %q, want the assigned @deep", got)
	}
}
