// /settings agents tab: the model role each agent runs on — the advisor plus the
// four bundled definitions — with the model that role resolves to shown on the
// row, and the advisor's one extra state: off.
//
// A role is a routing slot, so the tab assigns roles and never a concrete model:
// moving every review to a stronger model stays a one-word edit, and an agent's
// own definition file is left alone. The rows read the effective role (the
// assignment, or the role the definition declares) and resolve it through the
// same bindings the executor reads, so the row cannot advertise a model the run
// will not use. Both blocks are saved in one read-modify-write (see
// setting.UpdateAgentsAt), which is also what migrates a legacy advisor.model.
//
// The tab owns agents.roles and the advisor block only: the agents.autoTier
// switch lives in /config, and every other setting in the file is left as it is.
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

// AgentsScope names where an agents-tab save goes. It mirrors setting.Scope;
// turning the advisor on for this session alone is what /advisor does instead.
type AgentsScope string

const (
	AgentsScopeUser    AgentsScope = "user"
	AgentsScopeProject AgentsScope = "project"
)

// AgentsConfigSavedMsg reports the blocks an agents-tab save wrote. The panel
// has already persisted them; the app owns the live session config and the
// rewiring that follows from it.
type AgentsConfigSavedMsg struct {
	Scope   AgentsScope
	Advisor setting.AdvisorSettings
	Roles   map[string]string
}

// advisorDefaultRole is the role the advisor reviews on when the tab has
// assigned none: the rung the advisor exists for.
const advisorDefaultRole = llm.RoleDeep

// managedAgents are the agents the tab assigns roles for, in display order. The
// advisor comes first because it is the only one with an off switch.
var managedAgents = []string{
	setting.AgentAdvisor,
	setting.AgentQuick,
	setting.AgentExplore,
	setting.AgentTask,
	setting.AgentReviewer,
}

// agentsConfig is the tab's working copy: the advisor's block plus the role
// assigned to each agent. A name missing from Roles keeps the role its own
// definition declares.
type agentsConfig struct {
	Advisor setting.AdvisorSettings
	Roles   map[string]string
}

// clone copies the working copy, so the baseline and the saved message each own
// their map and tri-state pointer.
func (c agentsConfig) clone() agentsConfig {
	c.Advisor = c.Advisor.Clone()
	c.Roles = maps.Clone(c.Roles)
	return c
}

// equal compares what the tab can edit, for the unsaved-edits marker.
func (c agentsConfig) equal(other agentsConfig) bool {
	return c.Advisor.Equal(other.Advisor) && maps.Equal(c.Roles, other.Roles)
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

// agentsPanel is the /settings agents tab: a configForm over the agentsConfig
// working copy, plus the role sub-view that assigns one agent's role.
type agentsPanel struct {
	form configForm[agentsConfig]

	// settings is the live settings handle the form seeds from. The panel reads
	// only the role names it offers from it, at Enter, so a role added while
	// the popup is open shows up the next time it opens.
	settings *setting.Settings

	// source resolves roles to models and names a definition's declared role.
	// The app injects it; a panel without one shows the roles alone.
	source AgentRoleSource

	// roles are the role names the picker offers, read at Enter.
	roles []string

	// picking is the role sub-view: while it is open the panel owns the keys
	// and the shell routes esc to it instead of closing the popup.
	picking bool
	pick    agentRolePick
}

func newAgentsPanel(settings *setting.Settings) *agentsPanel {
	p := &agentsPanel{settings: settings}
	p.form = configForm[agentsConfig]{
		workspace: func() (string, *setting.Settings) { return "", settings },
		rowsFn:    func() []configRow[agentsConfig] { return p.rows() },
		scopes:    []configScope{scopeUser, scopeProject},
		seed: func(data *setting.Data) agentsConfig {
			return agentsConfig{Advisor: data.Advisor.Clone(), Roles: maps.Clone(data.Agents.Roles)}
		},
		equal:    func(a, b agentsConfig) bool { return a.equal(b) },
		validate: func(c agentsConfig) error { return setting.ValidateAgentRoles(c.Roles) },
		persist: func(c agentsConfig, scope configScope) error {
			return setting.UpdateAgentsAt(c.Roles, c.Advisor, setting.Scope(scope))
		},
		saved: func(c agentsConfig, scope configScope) tea.Msg {
			saved := c.clone()
			return AgentsConfigSavedMsg{Scope: AgentsScope(scope), Advisor: saved.Advisor, Roles: saved.Roles}
		},
	}
	return p
}

func (p *agentsPanel) Title() string { return "agents" }

// Enter seeds the working copy from the settings file the panel was built over
// and re-reads the roles the picker offers.
func (p *agentsPanel) Enter() {
	p.picking = false
	p.form.Enter()
	p.roles = p.knownRoles()
}

// knownRoles is the ladder plus every custom role modelRoles binds, sorted, so
// the picker offers the roles this user actually configured — a custom role is
// as assignable as a rung.
func (p *agentsPanel) knownRoles() []string {
	roles := []string{
		string(llm.RoleDefault), string(llm.RoleFast),
		string(llm.RoleStandard), string(llm.RoleDeep),
	}
	var custom []string
	if p.settings != nil {
		for name := range p.settings.ModelRoles() {
			if !slices.Contains(roles, name) {
				custom = append(custom, name)
			}
		}
	}
	slices.Sort(custom)
	return append(roles, custom...)
}

func (p *agentsPanel) Dirty() bool { return p.form.Dirty() }

func (p *agentsPanel) HintLine() string {
	if p.picking {
		return p.pick.hint()
	}
	return kit.HintLine(
		keycap("↑↓")+" navigate",
		keycap("enter")+" change role",
		keycap("←→")+" scope",
		keycap("esc")+" discard",
	)
}

// Modal reports the role sub-view: while it is open the panel drives esc
// itself, so it backs out of the picker instead of closing /settings.
func (p *agentsPanel) Modal() bool { return p.picking }

// Breadcrumb names the sub-view the shell shows in the header lockup.
func (p *agentsPanel) Breadcrumb() string {
	if p.picking {
		return p.pick.crumb()
	}
	return ""
}

func (p *agentsPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if p.picking {
		closed, picked, choice := p.pick.handleKey(msg)
		if picked {
			p.apply(choice)
		}
		if picked || closed {
			p.picking = false
		}
		return nil, false
	}
	cmd, done, activated := p.form.HandleKey(msg)
	if activated != "" {
		p.openRolePick(activated)
	}
	return cmd, done
}

func (p *agentsPanel) Render(width, height int) string {
	if p.picking {
		return p.pick.render(width)
	}
	p.form.visible = max(height-1, 0)
	return p.form.Render(width)
}

// SetRoleSource wires the resolver behind every row's model label and the
// picker's per-role detail.
func (p *agentsPanel) SetRoleSource(src AgentRoleSource) { p.source = src }

// openRolePick opens the role sub-view on the row's agent.
func (p *agentsPanel) openRolePick(agent string) {
	p.pick.begin(agent, p.choicesFor(agent), p.currentChoice(agent))
	p.picking = true
}

// apply writes the picker's choice into the working copy. The advisor's off
// entry keeps the assigned role, so turning it back on resumes the same model.
func (p *agentsPanel) apply(choice agentRoleChoice) {
	switch choice.kind {
	case roleChoiceOff:
		off := false
		p.form.snap.Advisor.Enable = &off
	case roleChoiceDefinition:
		delete(p.form.snap.Roles, p.pick.agent)
	default:
		if p.form.snap.Roles == nil {
			p.form.snap.Roles = make(map[string]string)
		}
		p.form.snap.Roles[p.pick.agent] = choice.role
		// Choosing a role for the advisor is what turns it on; there is no
		// separate switch on this tab.
		if p.pick.agent == setting.AgentAdvisor {
			on := true
			p.form.snap.Advisor.Enable = &on
		}
	}
}

// choicesFor lists what the picker offers one agent: the advisor may be
// switched off, a bundled agent may fall back to its own definition, and every
// agent may move to any role the ladder or modelRoles names.
func (p *agentsPanel) choicesFor(agent string) []agentRoleChoice {
	var choices []agentRoleChoice
	if agent == setting.AgentAdvisor {
		choices = append(choices, agentRoleChoice{kind: roleChoiceOff, label: "off", detail: "no review after a turn"})
	} else {
		choices = append(choices, agentRoleChoice{kind: roleChoiceDefinition, label: "definition default", detail: p.effectiveRoleDetail(agent)})
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
func (p *agentsPanel) currentChoice(agent string) agentRoleChoice {
	c := &p.form.snap
	if agent == setting.AgentAdvisor {
		if !c.Advisor.Enabled() {
			return agentRoleChoice{kind: roleChoiceOff}
		}
	} else if assignedRole(c.Roles, agent) == "" {
		return agentRoleChoice{kind: roleChoiceDefinition}
	}
	role := assignedRole(c.Roles, agent)
	if role == "" {
		role = string(advisorDefaultRole)
	}
	return agentRoleChoice{kind: roleChoiceRole, role: role}
}

// resolve answers the model and effort a role resolves to, or nothing when the
// panel was built without a source.
func (p *agentsPanel) resolve(role string) (string, string) {
	if p.source.Resolve == nil {
		return "", ""
	}
	return p.source.Resolve(role)
}

// effectiveRoleDetail describes what the definition's own role resolves to, for
// the picker's "definition default" entry.
func (p *agentsPanel) effectiveRoleDetail(agent string) string {
	if p.source.Effective == nil {
		return ""
	}
	role, model, effort := p.source.Effective(agent)
	if role == "" {
		return "the definition names its own model"
	}
	return roleModelLabel(model, effort)
}

// summaryFor renders one row's right-hand hint: the role in effect and the
// model it resolves to. The state the user cannot change from this tab — the
// advisor switched off, or a definition that names a model instead of a role —
// is named instead of leaving the row blank.
func (p *agentsPanel) summaryFor(agent string, c *agentsConfig) string {
	if agent == setting.AgentAdvisor && !c.Advisor.Enabled() {
		return "off"
	}
	if role := assignedRole(c.Roles, agent); role != "" {
		return roleLabel(role) + roleModelLabel(p.resolve(role))
	}
	if agent == setting.AgentAdvisor {
		return roleLabel(string(advisorDefaultRole)) + roleModelLabel(p.resolve(string(advisorDefaultRole)))
	}
	if p.source.Effective != nil {
		role, model, effort := p.source.Effective(agent)
		if role != "" {
			return roleLabel(role) + roleModelLabel(model, effort)
		}
	}
	return "definition default"
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

// ── Rows ────────────────────────────────────────────────────────────────

// rows is the whole panel: the advisor's role, the four bundled agents' roles,
// and the save control. The advisor row is labelled Role because it is the only
// agent whose name the section header already gives.
func (p *agentsPanel) rows() []configRow[agentsConfig] {
	rows := []configRow[agentsConfig]{
		{kind: rowSubHeader, label: "Advisor"},
		p.row(setting.AgentAdvisor, "Role", "reviews each finished turn"),
		{kind: rowSpacer},
		{kind: rowSubHeader, label: "Agents"},
	}
	descriptions := map[string]string{
		setting.AgentQuick:    "small, well-scoped edits",
		setting.AgentExplore:  "read-only investigation",
		setting.AgentTask:     "general implementation work",
		setting.AgentReviewer: "independent review of a diff",
	}
	for _, agent := range managedAgents {
		if agent == setting.AgentAdvisor {
			continue
		}
		rows = append(rows, p.row(agent, agent, descriptions[agent]))
	}
	return append(rows,
		configRow[agentsConfig]{kind: rowSpacer},
		configRow[agentsConfig]{kind: rowSave, label: "Save"},
	)
}

// row builds one agent's entry row: enter opens the role picker for it.
func (p *agentsPanel) row(agent, label, desc string) configRow[agentsConfig] {
	return configRow[agentsConfig]{
		kind:    rowEntry,
		label:   label,
		entryID: agent,
		desc:    desc,
		indent:  1,
		summary: func(c *agentsConfig) string { return p.summaryFor(agent, c) },
	}
}
