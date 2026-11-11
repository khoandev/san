// The /settings agents tab's role sub-view: one flat list of the roles an agent
// can run on.
//
// A role is a routing slot, not a model, so the list offers roles and shows the
// model each one resolves to — the picker never writes a model reference, which
// is what keeps an agent's assignment a single word in settings.json. The list
// is one level deep and short, so it needs no filter: the ladder plus whatever
// custom roles modelRoles binds.
package input

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

// agentRoleChoiceKind says what picking an entry does to the working copy.
type agentRoleChoiceKind int

const (
	// roleChoiceDefinition drops the assignment, leaving the agent on the role
	// its own definition file declares.
	roleChoiceDefinition agentRoleChoiceKind = iota
	// roleChoiceRole assigns the named role.
	roleChoiceRole
)

// agentRoleChoice is one entry of the picker.
type agentRoleChoice struct {
	kind   agentRoleChoiceKind
	role   string // set for roleChoiceRole
	label  string
	detail string // the model it resolves to, or why the entry exists
}

// same reports whether two entries name the same choice, so the picker can open
// on the one in effect.
func (c agentRoleChoice) same(other agentRoleChoice) bool {
	return c.kind == other.kind && c.role == other.role
}

// agentRolePick is the picker's state: the entries for one agent and the
// selected row.
type agentRolePick struct {
	agent   string
	choices []agentRoleChoice
	cursor  int
}

// agentRoleListRows caps how many entries the picker shows at once.
const agentRoleListRows = 12

// begin resets the picker for an open, placing the cursor on the entry in
// effect.
func (p *agentRolePick) begin(agent string, choices []agentRoleChoice, current agentRoleChoice) {
	p.agent = agent
	p.choices = choices
	p.cursor = 0
	for i, c := range choices {
		if c.same(current) {
			p.cursor = i
			return
		}
	}
}

// handleKey applies one keypress. closed reports that the picker is done, picked
// that choice carries the entry the user chose.
func (p *agentRolePick) handleKey(msg tea.KeyMsg) (closed, picked bool, choice agentRoleChoice) {
	switch msg.String() {
	case "esc":
		return true, false, agentRoleChoice{}
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "j":
		p.cursor = min(p.cursor+1, max(len(p.choices)-1, 0))
	case "enter", "space":
		if len(p.choices) == 0 {
			return true, false, agentRoleChoice{}
		}
		return true, true, p.choices[min(p.cursor, len(p.choices)-1)]
	}
	return false, false, agentRoleChoice{}
}

func (p *agentRolePick) render(width int) string {
	var b strings.Builder
	b.WriteString(apDescStyle.Render("Which model role this agent runs on. The role decides the model; where its models are bound stays in the models tab."))
	b.WriteString("\n\n")

	start := max(min(p.cursor-agentRoleListRows/2, len(p.choices)-agentRoleListRows), 0)
	end := min(start+agentRoleListRows, len(p.choices))
	for i := start; i < end; i++ {
		choice := p.choices[i]
		mark, label := strings.Repeat(" ", kit.Width(kit.CurrentSymbols.Cursor)+1), apLabelStyle.Render(choice.label)
		if i == p.cursor {
			mark, label = apCursorStyle.Render(kit.CurrentSymbols.Cursor+" "), apBreadcrumbSubStyle.Render(choice.label)
		}
		line := mark + label
		if choice.detail != "" {
			room := max(width-lipgloss.Width(line)-2, 8)
			line += "  " + apSummaryStyle.Render(kit.TruncateText(choice.detail, room))
		}
		b.WriteString(kit.TruncateText(line, max(width-2, 20)) + "\n")
	}
	return b.String()
}

func (p *agentRolePick) hint() string {
	return kit.HintLine(keycap("↑↓")+" navigate", keycap("enter")+" pick", keycap("esc")+" back")
}

func (p *agentRolePick) crumb() string { return "Role" }
