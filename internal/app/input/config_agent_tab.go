// /settings Agent tab: the YOLO gate and the per-agent role assignments behind
// one tab, so the two things that decide how an agent behaves — what it may do
// unattended and which role it runs on — sit together instead of on separate
// tabs.
//
// The gate keeps its own panel and its own save: `allowBypass` is a personal,
// user-file-only preference, while the role form offers a user/project scope
// control. Merging the two into one form would either hand the gate a project
// scope it must never have or take the scope control away from the roles, so
// the tab hosts the gate as a drill-in sub-view and leaves both saves intact.
package input

import (
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/setting"
)

// permissionsEntry is the entryID of the row that opens the YOLO gate.
const permissionsEntry = "permissions"

// agentPanel is the /settings Agent tab: the agents form with one entry row
// above it that drills into the YOLO gate.
type agentPanel struct {
	agents *agentsPanel
	perms  *permissionsPanel

	// inPerms reports the gate sub-view. While it is open the tab owns the keys,
	// so the shell routes esc here — backing out of the gate instead of closing
	// /settings.
	inPerms bool
}

func newAgentPanel(settings *setting.Settings) *agentPanel {
	p := &agentPanel{
		agents: newAgentsPanel(settings),
		perms:  newPermissionsPanel(settings),
	}
	// The gate row leads the same row list the roles use, so one cursor walks
	// the whole tab and it reads as a single form.
	agentRows := p.agents.form.rowsFn
	p.agents.form.rowsFn = func() []configRow[agentsConfig] {
		return append(p.gateRows(), agentRows()...)
	}
	return p
}

// gateRows is the section that names the gate's state and opens it.
func (p *agentPanel) gateRows() []configRow[agentsConfig] {
	return []configRow[agentsConfig]{
		{kind: rowSubHeader, label: "Permissions"},
		{
			kind:    rowEntry,
			label:   "YOLO mode",
			entryID: permissionsEntry,
			desc:    "shift+tab reaches YOLO mode",
			indent:  1,
			summary: func(*agentsConfig) string { return p.gateSummary() },
		},
		{kind: rowSpacer},
	}
}

// gateSummary names the gate's effective state beside its row, the way the role
// rows name the model theirs, so the tab answers the question without a drill-in.
func (p *agentPanel) gateSummary() string {
	if p.perms.baseline {
		return "allowed"
	}
	return "locked"
}

func (p *agentPanel) Title() string { return "agent" }

// Enter resets the gate sub-view and re-seeds both halves from the settings file
// the tab was built over.
func (p *agentPanel) Enter() {
	p.inPerms = false
	p.agents.Enter()
	p.perms.Enter()
}

func (p *agentPanel) Dirty() bool {
	if p.inPerms {
		return p.perms.Dirty()
	}
	return p.agents.Dirty()
}

func (p *agentPanel) HintLine() string {
	if p.inPerms {
		return kit.HintLine(radioHintLine(), keycap("esc")+" back")
	}
	return p.agents.HintLine()
}

// Modal reports the gate sub-view: while it is open the panel drives esc itself.
func (p *agentPanel) Modal() bool { return p.inPerms || p.agents.Modal() }

// Breadcrumb names the sub-view the shell shows in the header lockup.
func (p *agentPanel) Breadcrumb() string {
	if p.inPerms {
		return "permissions"
	}
	return p.agents.Breadcrumb()
}

func (p *agentPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if p.inPerms {
		// While the sub-view is open the shell hands esc here instead of
		// dismissing /settings, so esc is the way back to the tab body.
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
	cmd, done, activated := p.agents.form.HandleKey(msg)
	switch activated {
	case "":
	case permissionsEntry:
		p.perms.Enter()
		p.inPerms = true
	default:
		p.agents.openRolePick(activated)
	}
	return cmd, done
}

func (p *agentPanel) Render(width, height int) string {
	if p.inPerms {
		return p.perms.Render(width, height)
	}
	return p.agents.Render(width, height)
}
