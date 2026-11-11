// /settings Agents tab: the YOLO gate, the advisor's on/off switch, and the model
// role each bundled subagent runs on.
//
// The tab is laid out and driven like the Appearance tab, because it holds the
// same kind of thing: personal preferences. There is one scope — the user
// settings file — so there is no scope control and no Save row; a change is
// written the moment it is made, and the app is told so it can rewire the
// session.
//
// The advisor owns no model setting of its own. Its model is the "advisor" model
// role, bound in /models beside the rungs; this tab only says whether a review
// runs. A role is a routing slot, so the agent rows assign roles and never a
// concrete model: moving work to a stronger model stays a one-word edit, and an
// agent's own definition file is left alone. Each row reads the effective role
// (the assignment, or the role the definition declares) and resolves it through
// the same bindings the executor reads, so the row cannot advertise a model the
// run will not use.
package input

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// AgentsConfigSavedMsg reports a change the agents tab has already written to
// the user settings file. The app owns the live session config and the rewiring
// that follows from it.
type AgentsConfigSavedMsg struct {
	// Notice is the one-line confirmation, e.g. "advisor on".
	Notice string
}

// managedAgents are the agents the tab assigns a role to, in display order. The
// advisor is not one of them: its model is the "advisor" model role, and its one
// control here is the switch.
var managedAgents = []string{
	setting.AgentQuick,
	setting.AgentExplore,
	setting.AgentTask,
	setting.AgentReviewer,
}

// agentDescriptions names what each bundled agent is for, beside its row.
var agentDescriptions = map[string]string{
	setting.AgentQuick:    "small, well-scoped edits",
	setting.AgentExplore:  "read-only investigation",
	setting.AgentTask:     "general implementation work",
	setting.AgentReviewer: "independent review of a diff",
}

// agentRowKind discriminates the tab's rows.
type agentRowKind int

const (
	agentRowGate    agentRowKind = iota // drills into the YOLO gate
	agentRowAdvisor                     // the advisor's on/off switch
	agentRowRole                        // one agent's role, behind the picker
)

// agentRow is one line of the tab: the section it belongs to, what activating it
// does, and what it is called.
type agentRow struct {
	kind    agentRowKind
	section string
	agent   string
	label   string
	desc    string
}

// AgentRoleSource tells the agents tab which role an agent falls back to and
// what a role resolves to. The panel never reads the provider store or the live
// settings for this: the app owns both, the way AgentModelSource feeds the
// /agents list.
type AgentRoleSource struct {
	// Effective answers the role an agent runs on when the tab assigned none:
	// the role its definition declares, and the model and effort that role
	// resolves to. An empty role means the definition names a model instead.
	Effective func(agent string) (role, model, effort string)
	// Resolve answers the model and reasoning effort one role resolves to, for
	// the rows the picker offers. "" means nothing is connected.
	Resolve func(role string) (model, effort string)
}

// agentPanel is the /settings Agents tab: one list of rows, the YOLO gate and the
// role picker behind them, and the writes that follow a change.
type agentPanel struct {
	// settings is the live settings handle the panel seeds from.
	settings *setting.Settings

	// source resolves roles to models and names a definition's declared role.
	// The app injects it; a panel without one shows the roles alone.
	source AgentRoleSource

	// roles are the role names the picker offers, read at Enter.
	roles []string

	cursor int

	// picking and inPerms are the two sub-views. While either is open the panel
	// owns the keys and the shell routes esc here instead of closing the popup.
	picking bool
	pick    agentRolePick

	perms   *permissionsPanel
	inPerms bool

	// advisor is the working copy the switch writes; agentRoles is the
	// agents.roles map the picker writes. Both are seeded from the live settings
	// at Enter and replaced by what was actually written.
	advisor    setting.AdvisorSettings
	agentRoles map[string]string

	// saveErr holds the last failed write so Render can surface it inline
	// instead of silently swallowing it. Cleared on the next successful write
	// or on re-entry.
	saveErr error
}

func newAgentPanel(settings *setting.Settings) *agentPanel {
	return &agentPanel{settings: settings, perms: newPermissionsPanel(settings)}
}

func (p *agentPanel) Title() string { return "agents" }

// Enter seeds both halves from the settings file the panel was built over and
// re-reads the roles the picker offers.
func (p *agentPanel) Enter() {
	p.picking, p.inPerms = false, false
	p.advisor, p.agentRoles = setting.AdvisorSettings{}, nil
	if p.settings != nil {
		if data := p.settings.Snapshot(); data != nil {
			p.advisor = data.Advisor.Clone()
			p.agentRoles = maps.Clone(data.Agents.Roles)
		}
	}
	p.roles = p.knownRoles()
	p.cursor = 0
	p.saveErr = nil
	p.perms.Enter()
}

// knownRoles is the ladder plus every custom role modelRoles binds, sorted, so
// the picker offers the roles this user actually configured — a custom role is
// as assignable as a rung. The advisor's own slot is left out: it fills the
// review, not a subagent.
func (p *agentPanel) knownRoles() []string {
	roles := []string{
		string(llm.RoleDefault), string(llm.RoleFast),
		string(llm.RoleStandard), string(llm.RoleDeep),
	}
	var custom []string
	if p.settings != nil {
		for name := range p.settings.ModelRoles() {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == setting.AgentAdvisor || slices.Contains(roles, name) || slices.Contains(custom, name) {
				continue
			}
			custom = append(custom, name)
		}
	}
	slices.Sort(custom)
	return append(roles, custom...)
}

// Dirty reports a change that could not be written. Every row applies
// immediately, so a clean tab is the normal state.
func (p *agentPanel) Dirty() bool { return p.saveErr != nil }

func (p *agentPanel) HintLine() string {
	switch {
	case p.inPerms:
		return kit.HintLine(radioHintLine(), keycap("esc")+" back")
	case p.picking:
		return p.pick.hint()
	}
	if p.rows()[p.cursor].kind == agentRowAdvisor {
		return kit.HintLine(keycap("↑↓")+" move", keycap("←→")+" off/on", keycap("enter")+" toggle")
	}
	return kit.HintLine(keycap("↑↓")+" move", keycap("enter")+" open")
}

// Modal reports a sub-view: while one is open the panel drives esc itself, so it
// backs out of the sub-view instead of closing /settings.
func (p *agentPanel) Modal() bool { return p.inPerms || p.picking }

// Breadcrumb names the sub-view the shell shows in the header lockup.
func (p *agentPanel) Breadcrumb() string {
	switch {
	case p.inPerms:
		return "permissions"
	case p.picking:
		return p.pick.crumb()
	}
	return ""
}

func (p *agentPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if p.inPerms {
		// The shell hands esc here instead of dismissing /settings, so esc is
		// the way back to the tab body.
		if msg.String() == "esc" {
			p.inPerms = false
			return nil, false
		}
		cmd, done := p.perms.HandleKey(msg)
		if done {
			// The gate saved; the tab stays open so the new state is visible.
			p.inPerms = false
		}
		return cmd, false
	}
	if p.picking {
		closed, picked, choice := p.pick.handleKey(msg)
		if picked {
			p.picking = false
			return p.apply(choice), false
		}
		if closed {
			p.picking = false
		}
		return nil, false
	}

	rows := p.rows()
	p.cursor = min(max(p.cursor, 0), len(rows)-1)
	switch msg.String() {
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
		p.saveErr = nil
	case "down", "j":
		p.cursor = min(p.cursor+1, len(rows)-1)
		p.saveErr = nil
	case "left", "h", "right", "l":
		if rows[p.cursor].kind == agentRowAdvisor {
			return p.setAdvisor(msg.String() == "right" || msg.String() == "l"), false
		}
	case "enter", " ":
		return p.activate(rows[p.cursor]), false
	}
	return nil, false
}

func (p *agentPanel) Render(width, height int) string {
	switch {
	case p.inPerms:
		return p.perms.Render(width, height)
	case p.picking:
		return p.pick.render(width)
	}
	return renderSettingBody(p.renderBody(width), width, height)
}

// SetRoleSource wires the resolver behind every row's model label and the
// picker's per-role detail.
func (p *agentPanel) SetRoleSource(src AgentRoleSource) { p.source = src }

// activate runs the hovered row. Only the switch writes anything by itself; the
// other rows open a sub-view that writes its own change.
func (p *agentPanel) activate(row agentRow) tea.Cmd {
	switch row.kind {
	case agentRowGate:
		p.perms.Enter()
		p.inPerms = true
	case agentRowAdvisor:
		return p.setAdvisor(!p.advisor.Enabled())
	case agentRowRole:
		p.openRolePick(row.agent)
	}
	return nil
}

// ── Writes ──────────────────────────────────────────────────────────────

// setAdvisor turns the review on or off in the user settings file and reports
// the change. The write replaces the whole block, so an explicit off persists
// instead of being folded back into "unset".
func (p *agentPanel) setAdvisor(on bool) tea.Cmd {
	cfg := p.advisor.Clone()
	cfg.Enable = &on
	if err := setting.UpdateAdvisorAt(cfg, setting.ScopeUser); err != nil {
		p.saveErr = err
		return nil
	}
	p.advisor = cfg
	p.saveErr = nil
	notice := "advisor off"
	if on {
		notice = "advisor on"
	}
	return func() tea.Msg { return AgentsConfigSavedMsg{Notice: notice} }
}

// apply writes the picker's choice into the user settings file. The role map is
// replaced wholesale, so clearing an assignment (back to the definition's own
// role) actually persists.
func (p *agentPanel) apply(choice agentRoleChoice) tea.Cmd {
	agent := p.pick.agent
	roles := maps.Clone(p.agentRoles)
	if roles == nil {
		roles = make(map[string]string)
	}
	if choice.kind == roleChoiceDefinition {
		delete(roles, agent)
	} else {
		roles[agent] = choice.role
	}
	if err := setting.UpdateAgentRolesAt(roles, setting.ScopeUser); err != nil {
		p.saveErr = err
		return nil
	}
	p.agentRoles = roles
	p.saveErr = nil
	return func() tea.Msg { return AgentsConfigSavedMsg{Notice: agent + " role saved"} }
}

// ── Rows ────────────────────────────────────────────────────────────────

// rows is the whole tab, in display order: the gate, the advisor's switch, then
// one row per bundled agent.
func (p *agentPanel) rows() []agentRow {
	rows := []agentRow{
		{section: "Permissions", kind: agentRowGate, label: "YOLO mode", desc: "shift+tab reaches YOLO mode"},
		{section: "Advisor", kind: agentRowAdvisor, label: "Advisor", desc: "reviews each finished turn"},
	}
	for _, agent := range managedAgents {
		rows = append(rows, agentRow{
			section: "Agents",
			kind:    agentRowRole,
			agent:   agent,
			label:   agentLabel(agent),
			desc:    agentDescriptions[agent],
		})
	}
	return rows
}

// agentLabel title-cases an agent name for its row, so the labels read as names
// while the entry identity stays the lowercase name settings use.
func agentLabel(agent string) string {
	if agent == "" {
		return ""
	}
	return strings.ToUpper(agent[:1]) + agent[1:]
}

// renderBody draws the rows, recording where the hovered section starts so the
// window can keep its heading in view.
func (p *agentPanel) renderBody(width int) settingBody {
	rows := p.rows()
	body := settingBody{lines: make([]string, 0, len(rows)*2)}
	previous := ""
	sectionRow := 0
	for i, row := range rows {
		if row.section != previous {
			if previous != "" {
				body.lines = append(body.lines, "")
			}
			sectionRow = len(body.lines)
			body.lines = append(body.lines, renderAppearanceSection(row.section, width))
			previous = row.section
		}
		if i == p.cursor {
			body.sectionRow = sectionRow
			body.cursorRow = len(body.lines)
		}
		if row.kind == agentRowAdvisor {
			body.lines = append(body.lines, renderToggleRow(row.label, row.desc, p.advisor.Enabled(), i == p.cursor, width))
			continue
		}
		body.lines = append(body.lines, renderAgentValueRow(row, p.summaryFor(row), i == p.cursor, width))
	}
	if p.saveErr != nil {
		body.errors = []string{"", appearanceErrorStyle.Render("⚠ couldn't save: " + p.saveErr.Error())}
	}
	return body
}

// renderAgentValueRow draws one drill-in row: the focus bar, the label in the
// shared control column, the value in effect, then the muted description.
func renderAgentValueRow(row agentRow, value string, hovered bool, width int) string {
	head, _ := appearanceRowHead(row.label, hovered)
	line := head + appearanceLabelStyle.Render(value)
	if row.desc != "" {
		line = withDesc(line, row.desc, width)
	}
	return line
}

// summaryFor renders one row's right-hand value: the gate's state, or the role
// in effect and the model it resolves to. A definition that names a concrete
// model is named instead of inventing a role for it.
func (p *agentPanel) summaryFor(row agentRow) string {
	switch row.kind {
	case agentRowGate:
		if p.perms.baseline {
			return "allowed"
		}
		return "locked"
	case agentRowAdvisor:
		// The switch draws its own value.
		return ""
	}
	if role := assignedRole(p.agentRoles, row.agent); role != "" {
		return roleLabel(role) + roleModelLabel(p.resolve(role))
	}
	if p.source.Effective != nil {
		role, model, effort := p.source.Effective(row.agent)
		if role != "" {
			return roleLabel(role) + roleModelLabel(model, effort)
		}
	}
	return "definition default"
}

// ── Role picker ─────────────────────────────────────────────────────────

// openRolePick opens the role sub-view on the row's agent.
func (p *agentPanel) openRolePick(agent string) {
	p.pick.begin(agent, p.choicesFor(agent), p.currentChoice(agent))
	p.picking = true
}

// choicesFor lists what the picker offers one agent: falling back to its own
// definition, and every role the ladder or modelRoles names.
func (p *agentPanel) choicesFor(agent string) []agentRoleChoice {
	choices := []agentRoleChoice{
		{kind: roleChoiceDefinition, label: "definition default", detail: p.effectiveRoleDetail(agent)},
	}
	for _, role := range p.roles {
		choices = append(choices, agentRoleChoice{
			kind:   roleChoiceRole,
			role:   role,
			label:  roleLabel(role),
			detail: roleModelLabel(p.resolve(role)),
		})
	}
	return choices
}

// currentChoice names the entry in effect for one agent, so the picker opens on
// it.
func (p *agentPanel) currentChoice(agent string) agentRoleChoice {
	role := assignedRole(p.agentRoles, agent)
	if role == "" {
		return agentRoleChoice{kind: roleChoiceDefinition}
	}
	return agentRoleChoice{kind: roleChoiceRole, role: role}
}

// resolve answers the model and effort a role resolves to, or nothing when the
// panel was built without a source.
func (p *agentPanel) resolve(role string) (string, string) {
	if p.source.Resolve == nil {
		return "", ""
	}
	return p.source.Resolve(role)
}

// effectiveRoleDetail describes what the definition's own role resolves to, for
// the picker's "definition default" entry.
func (p *agentPanel) effectiveRoleDetail(agent string) string {
	if p.source.Effective == nil {
		return ""
	}
	role, model, effort := p.source.Effective(agent)
	if role == "" {
		return "the definition names its own model"
	}
	return roleModelLabel(model, effort)
}

// assignedRole reads the role the working copy assigns to name, folding case and
// surrounding space the way the settings fold does.
func assignedRole(roles map[string]string, name string) string {
	if role := strings.TrimSpace(roles[name]); role != "" {
		return role
	}
	for key, role := range roles {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(role)
		}
	}
	return ""
}

// roleLabel spells a role the way an agent definition does, with the "@" that
// marks it as a routing slot.
func roleLabel(role string) string {
	if role == "" {
		return ""
	}
	return "@" + role
}

// roleModelLabel renders the resolved model beside a role: " → model", with the
// reasoning effort after a dot when one is bound. Empty model means the panel
// does not know, so only the role is shown.
func roleModelLabel(model, effort string) string {
	if model == "" {
		return ""
	}
	label := " → " + model
	if effort != "" {
		label += " · " + effort
	}
	return label
}
