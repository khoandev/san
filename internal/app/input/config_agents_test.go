// The /settings agents tab: the YOLO gate, the advisor's on/off switch, and one
// role row per bundled agent, laid out and driven like the Appearance tab.
//
// Like Appearance, a change is written the moment it is made and the tab emits
// AgentsConfigSavedMsg for the app to apply; there is no scope control and no
// Save row. These tests pin that boundary down, along with the rule that a role
// row shows the model its role resolves to.
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

// newTestAgentTab builds the agents tab with settings=nil, so Enter seeds the
// zero config and the tab touches no disk until a row is activated.
func newTestAgentTab(t *testing.T) *agentPanel {
	t.Helper()
	p := newAgentPanel(nil)
	p.Enter()
	return p
}

// testRoleSource answers the two questions the tab asks with fixed models, so
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

// moveToAgentRow walks the cursor to the row carrying agent, from the top of the
// list, the same way a user does.
func moveToAgentRow(t *testing.T, p *agentPanel, agent string) {
	t.Helper()
	for range p.rows() {
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	for range p.rows() {
		row := p.rows()[p.cursor]
		if row.kind == agentRowRole && row.agent == agent {
			return
		}
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatalf("the %q row is unreachable", agent)
}

// moveToSwitch parks the cursor on the advisor's switch row.
func moveToSwitch(t *testing.T, p *agentPanel) {
	t.Helper()
	for range p.rows() {
		if p.rows()[p.cursor].kind == agentRowAdvisor {
			return
		}
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatal("the advisor row is unreachable")
}

// The tab is its own tab in /settings, styled like Appearance, and no separate
// Permissions or Advisor tab remains: both live inside it.
func TestAgentsTabIsItsOwnSettingsTab(t *testing.T) {
	c := NewSettingsSelector(nil)
	var titles []string
	for _, p := range c.panels {
		titles = append(titles, p.Title())
	}
	if got := strings.Join(titles, ","); got != "appearance,agents,general" {
		t.Fatalf("/settings tabs = %q, want appearance,agents,general", got)
	}
	for _, gone := range []string{"advisor", "permissions"} {
		if slicesContains(titles, gone) {
			t.Fatalf("%q is still a top-level tab: %v", gone, titles)
		}
	}
	if _, ok := c.panels[1].(*agentPanel); !ok {
		t.Fatalf("the middle tab is %T, want the agents tab", c.panels[1])
	}
}

// The tab is one appearance-shaped list: the gate row leads, the advisor's
// switch follows, then one drill-in row per bundled agent.
func TestAgentTabRowsCoverTheGateTheAdvisorAndEveryAgent(t *testing.T) {
	p := newTestAgentTab(t)
	if got := p.Title(); got != "agents" {
		t.Fatalf("tab title = %q, want agents", got)
	}

	rows := p.rows()
	if rows[0].kind != agentRowGate {
		t.Fatalf("rows[0] = %+v, want the gate row", rows[0])
	}
	if rows[1].kind != agentRowAdvisor {
		t.Fatalf("rows[1] = %+v, want the advisor switch", rows[1])
	}
	var agents []string
	for _, row := range rows {
		if row.kind == agentRowRole {
			agents = append(agents, row.agent)
		}
	}
	if strings.Join(agents, ",") != strings.Join(managedAgents, ",") {
		t.Fatalf("agent rows = %v, want %v", agents, managedAgents)
	}

	body := ansi.Strip(p.Render(70, 20))
	for _, want := range []string{"PERMISSIONS", "YOLO mode", "ADVISOR", "Advisor", "AGENTS", "Quick", "Explore", "Task", "Reviewer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("tab body is missing %q:\n%s", want, body)
		}
	}
}

// Like Appearance, the tab has no scope control and no Save row: it configures
// the user's file only, and a change lands when it is made.
func TestAgentTabHasNoScopeOrSaveControl(t *testing.T) {
	p := newTestAgentTab(t)
	body := p.Render(100, 40)
	for _, unwanted := range []string{"project", "Save"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the tab body offers %q, which belongs to the old form:\n%s", unwanted, body)
		}
	}
	if hint := p.HintLine(); strings.Contains(hint, "save") {
		t.Errorf("the hint line offers a save step: %q", hint)
	}
}

// The gate is a drill-in row inside the tab: enter opens it, esc backs out
// instead of closing /settings, and a save returns to the body without closing
// the popup.
func TestAgentTabGateDrillsInAndSavesWithoutClosingThePopup(t *testing.T) {
	home := tempHome(t)

	p := newTestAgentTab(t)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // open the gate
	if !p.inPerms {
		t.Fatalf("the gate sub-view did not open")
	}
	if !p.Modal() || p.Breadcrumb() != "permissions" {
		t.Fatalf("gate sub-view: modal=%v crumb=%q", p.Modal(), p.Breadcrumb())
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Allowed → Locked
	if !p.perms.Dirty() {
		t.Fatalf("moving off the baseline should mark the gate dirty")
	}
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done {
		t.Fatalf("a gate save must not dismiss /settings")
	}
	if p.inPerms {
		t.Fatalf("a gate save should return to the tab body")
	}
	if msg, ok := cmd().(AllowBypassSavedMsg); !ok || msg.Allowed {
		t.Fatalf("expected AllowBypassSavedMsg{Allowed:false}, got %#v", cmd())
	}
	if got := p.summaryFor(p.rows()[0]); got != "locked" {
		t.Fatalf("gate summary = %q, want locked", got)
	}
	if data := readPersistedAllowBypass(t, home); data == nil || *data {
		t.Fatalf("persisted allowBypass = %v, want false", data)
	}

	// Esc leaves the sub-view, not the popup.
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.inPerms {
		t.Fatal("the gate did not reopen")
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if p.inPerms {
		t.Fatalf("esc should leave the gate sub-view")
	}
}

// The advisor's row is a plain switch: ←→ or enter flips it, the write lands at
// once in the user file, and the tab reports it to the app. The advisor owns no
// model setting here — that role is bound in /models.
func TestAgentTabAdvisorSwitchWritesImmediately(t *testing.T) {
	home := tempHome(t)

	p := newTestAgentTab(t)
	moveToSwitch(t, p)
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if done {
		t.Fatal("a switch must not dismiss /settings")
	}
	if cmd == nil {
		t.Fatal("switching the advisor on should emit AgentsConfigSavedMsg")
	}
	if msg, ok := cmd().(AgentsConfigSavedMsg); !ok || msg.Notice != "advisor on" {
		t.Fatalf("message = %#v, want advisor on", cmd())
	}
	if !p.advisor.Enabled() {
		t.Fatal("the working copy did not follow the switch")
	}
	if p.Dirty() {
		t.Fatal("a written switch leaves the tab clean")
	}
	if !readPersistedAdvisor(t, home) {
		t.Fatal("the switch did not reach the user settings file")
	}

	// ← flips it back, and the explicit off is what persists.
	cmd, _ = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if msg, ok := cmd().(AgentsConfigSavedMsg); !ok || msg.Notice != "advisor off" {
		t.Fatalf("message = %#v, want advisor off", cmd())
	}
	if readPersistedAdvisor(t, home) {
		t.Fatal("an explicit off did not persist")
	}

	// Enter is the same switch, for a keyboard without usable arrows.
	cmd, _ = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if msg, ok := cmd().(AgentsConfigSavedMsg); !ok || msg.Notice != "advisor on" {
		t.Fatalf("enter on the switch = %#v, want advisor on", cmd())
	}
}

// A failed write is surfaced in place instead of being swallowed: the tab keeps
// the error, reports itself dirty, and emits nothing.
func TestAgentTabAdvisorSwitchSurfacesAFailedWrite(t *testing.T) {
	home := tempHome(t)
	// A regular file where the .san directory must be makes the write fail.
	if err := os.WriteFile(filepath.Join(home, ".san"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newTestAgentTab(t)
	moveToSwitch(t, p)
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if done || cmd != nil {
		t.Fatalf("a failed write must not close the popup or emit: done=%v cmd=%#v", done, cmd)
	}
	if p.saveErr == nil || !p.Dirty() {
		t.Fatalf("a failed write should be kept and surfaced: err=%v dirty=%v", p.saveErr, p.Dirty())
	}
	if body := ansi.Strip(p.Render(100, 40)); !strings.Contains(body, "couldn't save") {
		t.Errorf("the error is not rendered:\n%s", body)
	}
}

// Enter on an agent row opens the role picker; picking a role writes it at once,
// persists it to the user file, and the row then shows the model it resolves to.
func TestAgentTabAssignsARole(t *testing.T) {
	home := tempHome(t)

	p := newTestAgentTab(t)
	p.SetRoleSource(testRoleSource())
	moveToAgentRow(t, p, setting.AgentQuick)

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
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done {
		t.Fatal("picking a role must not dismiss /settings")
	}
	if p.picking {
		t.Fatal("a completed pick closes the picker")
	}
	if msg, ok := cmd().(AgentsConfigSavedMsg); !ok || msg.Notice != "quick role saved" {
		t.Fatalf("message = %#v, want quick role saved", cmd())
	}
	if got := p.agentRoles[setting.AgentQuick]; got != string(llm.RoleDefault) {
		t.Fatalf("quick role = %q, want the picked default", got)
	}
	if got := readPersistedRoles(t, home)[setting.AgentQuick]; got != string(llm.RoleDefault) {
		t.Fatalf("persisted quick role = %q, want the picked default", got)
	}
	if body := ansi.Strip(p.Render(100, 40)); !strings.Contains(body, "@default → session-model") {
		t.Errorf("the row does not show the picked role's model:\n%s", body)
	}
}

// Choosing "definition default" clears the assignment, so the agent falls back
// to the role its own file declares.
func TestAgentTabDefinitionDefaultClearsTheAssignment(t *testing.T) {
	home := tempHome(t)
	settings := setting.New(&setting.Data{
		Agents: setting.AgentsSettings{Roles: map[string]string{setting.AgentQuick: "fast"}},
	})
	p := newAgentPanel(settings)
	p.SetRoleSource(testRoleSource())
	p.Enter()

	moveToAgentRow(t, p, setting.AgentQuick)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	for p.pick.cursor > 0 {
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	cmd, _ := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("clearing an assignment should emit a saved message")
	}
	if _, kept := p.agentRoles[setting.AgentQuick]; kept {
		t.Fatalf("the assignment survived: %v", p.agentRoles)
	}
	if _, kept := readPersistedRoles(t, home)[setting.AgentQuick]; kept {
		t.Fatal("the cleared assignment is still in the user file")
	}
	if body := ansi.Strip(p.Render(100, 40)); !strings.Contains(body, "@fast → haiku") {
		t.Errorf("the row did not fall back to the definition's role:\n%s", body)
	}
}

// Esc backs out of the picker without touching the working copy or the file.
func TestAgentTabPickerEscChangesNothing(t *testing.T) {
	home := tempHome(t)

	p := newTestAgentTab(t)
	p.SetRoleSource(testRoleSource())
	moveToAgentRow(t, p, setting.AgentTask)

	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})

	if p.picking {
		t.Fatal("esc should close the picker")
	}
	if len(p.agentRoles) != 0 {
		t.Fatalf("esc wrote a role: %v", p.agentRoles)
	}
	if p.Dirty() {
		t.Fatal("esc should leave the tab clean")
	}
	if _, err := os.Stat(filepath.Join(home, ".san", "settings.json")); err == nil {
		t.Fatal("esc wrote the settings file")
	}
}

// Every role row is labelled with the role in effect and the model it resolves
// to, which is the point of the tab.
func TestAgentTabRowsShowTheResolvedModel(t *testing.T) {
	p := newTestAgentTab(t)
	p.SetRoleSource(testRoleSource())

	body := ansi.Strip(p.Render(100, 40))
	for _, want := range []string{
		"Quick", "@fast → haiku", // quick and explore declare the fast role
		"Task", "@standard → worker", // task declares the standard role
		"Reviewer", "@deep → opus · high", // reviewer declares the deep role
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tab body is missing %q:\n%s", want, body)
		}
	}
}

// A definition whose file names a concrete model has no role to show, and an
// agent with no source at all falls back to the plain state rather than an
// invented role.
func TestAgentTabRowWithoutARole(t *testing.T) {
	p := newTestAgentTab(t)
	p.SetRoleSource(AgentRoleSource{
		Resolve:   func(string) (string, string) { return "", "" },
		Effective: func(string) (string, string, string) { return "", "", "" },
	})
	if body := ansi.Strip(p.Render(100, 40)); !strings.Contains(body, "definition default") {
		t.Errorf("a model-pinned definition should say so:\n%s", body)
	}

	bare := newTestAgentTab(t)
	if body := ansi.Strip(bare.Render(100, 40)); !strings.Contains(body, "definition default") {
		t.Errorf("a tab without a source should still name the state:\n%s", body)
	}
}

// The tab seeds both halves from settings and starts clean, editing a clone
// rather than the live settings storage.
func TestAgentTabSeedsFromSettings(t *testing.T) {
	on := true
	settings := setting.New(&setting.Data{
		Advisor: setting.AdvisorSettings{Enable: &on},
		Agents:  setting.AgentsSettings{Roles: map[string]string{setting.AgentReviewer: "fast"}},
	})
	p := newAgentPanel(settings)
	p.Enter()

	if !p.advisor.Enabled() {
		t.Fatal("tab seeded the advisor off, want the file's config")
	}
	if got := p.agentRoles[setting.AgentReviewer]; got != "fast" {
		t.Fatalf("seeded reviewer role = %q, want fast", got)
	}
	if p.Dirty() {
		t.Fatal("a tab seeded from the file starts clean")
	}
	p.agentRoles[setting.AgentReviewer] = "deep"
	if got := settings.AgentRole(setting.AgentReviewer); got != "fast" {
		t.Fatalf("the tab edits live settings storage: %q", got)
	}
}

// The picker offers the ladder, the entry that clears an assignment, and the
// custom roles modelRoles binds — sorted after the ladder. The advisor's own
// slot is not a subagent role, so it is not offered.
func TestAgentRolePickerChoices(t *testing.T) {
	settings := setting.New(&setting.Data{
		ModelRoles: map[string]string{"judge": "haiku", "fast": "haiku", setting.AgentAdvisor: "opus"},
	})
	p := newAgentPanel(settings)
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
}

// The picker opens on the entry in effect, so enter-then-enter is a no-op.
func TestAgentRolePickerOpensOnTheCurrentChoice(t *testing.T) {
	settings := setting.New(&setting.Data{
		Agents: setting.AgentsSettings{Roles: map[string]string{setting.AgentTask: "deep"}},
	})
	p := newAgentPanel(settings)
	p.SetRoleSource(testRoleSource())
	p.Enter()

	moveToAgentRow(t, p, setting.AgentTask)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := p.pick.choices[p.pick.cursor].label; got != "@deep" {
		t.Fatalf("picker opened on %q, want the assigned @deep", got)
	}
}

// The tab renders at every width the popup can give it, and fits a 24-row
// terminal with the shell's chrome.
func TestAgentTabRendersAtEveryWidth(t *testing.T) {
	p := newTestAgentTab(t)
	p.SetRoleSource(testRoleSource())
	for _, width := range []int{40, 60, 80, 120} {
		body := ansi.Strip(p.Render(width, 30))
		if body == "" {
			t.Fatalf("width %d rendered nothing", width)
		}
		if strings.Contains(body, "\x00") {
			t.Fatalf("width %d rendered a NUL", width)
		}
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimRight(line, " ")
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d: row is %d cols wide: %q", width, got, line)
			}
		}
	}

	c := NewSettingsSelector(nil)
	c.Enter(80, 24)
	c.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyTab}) // agents
	if got := lipgloss.Height(c.Render()); got > 24 {
		t.Fatalf("agents tab height = %d, exceeds a 24-row terminal", got)
	}
	if got := ansi.Strip(c.Render()); !strings.Contains(got, "AGENTS") || !strings.Contains(got, "GENERAL") {
		t.Fatalf("the tab strip is missing a tab: %q", got)
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

// readPersistedAdvisor reads back the advisor switch from the user settings file
// the tab writes to.
func readPersistedAdvisor(t *testing.T, home string) bool {
	t.Helper()
	var data struct {
		Advisor struct {
			Enable *bool `json:"enable"`
		} `json:"advisor"`
	}
	readUserSettings(t, home, &data)
	return data.Advisor.Enable != nil && *data.Advisor.Enable
}

// readPersistedRoles reads back the agents.roles map from the user settings
// file.
func readPersistedRoles(t *testing.T, home string) map[string]string {
	t.Helper()
	var data struct {
		Agents struct {
			Roles map[string]string `json:"roles"`
		} `json:"agents"`
	}
	readUserSettings(t, home, &data)
	return data.Agents.Roles
}

// readUserSettings decodes the user settings file into shape.
func readUserSettings(t *testing.T, home string, shape any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".san", "settings.json"))
	if err != nil {
		t.Fatalf("settings file not written: %v", err)
	}
	if err := json.Unmarshal(raw, shape); err != nil {
		t.Fatalf("settings file not valid JSON: %v\n%s", err, raw)
	}
}
