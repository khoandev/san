// Provider selector: the Roles tab, where a model role is bound to the model
// that answers its work, and the assign mode that borrows the Models list to
// pick one.
package input

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// ModelRoleInfo is what the Roles tab shows: the effective role bindings, the
// roles the project scope assigns itself, and the session model an unassigned
// role follows. The overlay never reads settings directly — the app hands it
// this snapshot, so persistence and the reload that follows live in one place.
type ModelRoleInfo struct {
	Roles   map[string]string
	Project map[string]string
	Session string
}

// RoleSource connects the Roles tab to settings: the snapshot to display and
// the two writes that change it. Both return the settings error instead of
// swallowing it, so a failed write is visible where the user made it.
type RoleSource struct {
	Info  func() ModelRoleInfo
	Set   func(role, ref string) error
	Clear func(role string) error
}

// SetRoleSource injects the role surface. It is set on each /models open,
// because settings are reloaded in between.
func (s *ProviderSelector) SetRoleSource(src RoleSource) { s.roleSource = src }

// providerRoleItem is one row of the Roles tab before it is rendered: a role,
// what it is bound to, and what that binding is for.
type providerRoleItem struct {
	Role string
	// Ref is the assigned reference; "" when the role follows the session model.
	Ref         string
	Description string
	Consumer    string
	// ProjectOverride marks a role the project scope assigns. It outranks the
	// user's own binding, so a row that looks wrong in a repository is
	// explained here rather than by reading two settings files.
	ProjectOverride bool
	// IsSession is the default role: it means "the session's own model", which
	// the model picker owns, so Enter opens that picker rather than assigning.
	IsSession bool
}

// roleRows lists every role the session can route through: the built-in catalog
// in display order, then whatever a hand-edited settings file named that the
// catalog does not describe — an agent definition may name its own role.
func roleRows(info ModelRoleInfo) []providerRoleItem {
	catalog := llm.RoleCatalog()
	rows := make([]providerRoleItem, 0, len(catalog)+len(info.Roles))
	known := make(map[string]bool, len(catalog))
	for _, spec := range catalog {
		role := string(spec.Name)
		known[role] = true
		rows = append(rows, providerRoleItem{
			Role:            role,
			Ref:             roleRef(info.Roles, role),
			Description:     spec.Description,
			Consumer:        spec.Consumer,
			ProjectOverride: roleRef(info.Project, role) != "",
			IsSession:       role == string(llm.RoleDefault),
		})
	}

	var custom []string
	for name := range info.Roles {
		role := strings.ToLower(strings.TrimSpace(name))
		if role != "" && !known[role] {
			custom = append(custom, role)
			known[role] = true
		}
	}
	sort.Strings(custom)
	for _, role := range custom {
		rows = append(rows, providerRoleItem{
			Role:            role,
			Ref:             roleRef(info.Roles, role),
			ProjectOverride: roleRef(info.Project, role) != "",
		})
	}
	return rows
}

// roleRef reads a binding by role name, folding case: modelRoles is hand-edited,
// so a key written as "Slow" still fills the slow role.
func roleRef(roles map[string]string, role string) string {
	for name, ref := range roles {
		if strings.EqualFold(strings.TrimSpace(name), role) {
			return strings.TrimSpace(ref)
		}
	}
	return ""
}

// refreshRoleInfo re-reads the injected snapshot. It is called when a role
// changes and when the tab is opened, never per keystroke: the reader loads
// project settings from disk.
func (s *ProviderSelector) refreshRoleInfo() {
	if s.roleSource.Info == nil {
		s.roleInfo = ModelRoleInfo{}
		return
	}
	s.roleInfo = s.roleSource.Info()
}

// assigning reports that the list is the Models list borrowed to pick a role's
// model.
func (s *ProviderSelector) assigning() bool { return s.assignRole != "" }

// selectRole handles Enter on a role row. The default role is the session's own
// model, so it opens the model list instead of assigning a second model to it;
// every other role starts assign mode.
func (s *ProviderSelector) selectRole(item providerListItem) tea.Cmd {
	if item.Role == nil {
		return nil
	}
	if item.Role.IsSession {
		s.assignRole = ""
		s.switchTab(providerTabModels)
		return nil
	}
	s.assignRole = item.Role.Role
	s.roleNotice = ""
	s.resetNavigation()
	s.resetModelSearch()
	s.rebuildVisibleItems()
	return nil
}

// assignModel binds the highlighted model to the role being assigned. The
// reference is written vendor-qualified, the form the resolver can route even
// when the session later moves to another provider's model.
func (s *ProviderSelector) assignModel(m *providerModelItem) tea.Cmd {
	role := s.assignRole
	if m == nil || role == "" {
		return nil
	}
	ref := m.ProviderName + "/" + m.ID
	s.finishAssign()
	return func() tea.Msg { return providerRoleAssignedMsg{Role: role, Ref: ref} }
}

// finishAssign leaves assign mode for the Roles tab, whether the user picked a
// model or backed out. Nothing is written here: the write is the message the
// caller handles.
func (s *ProviderSelector) finishAssign() {
	s.assignRole = ""
	s.activeTab = providerTabRoles
	s.resetNavigation()
	s.resetModelSearch()
	s.refreshRoleInfo()
	s.rebuildVisibleItems()
}

// handleRemove routes Ctrl+D to whatever the active view can remove: a stored
// credential on the Providers tab, a role binding on the Roles tab. Assign mode
// has nothing to remove — a model is being chosen, not deleted.
func (s *ProviderSelector) handleRemove() tea.Cmd {
	if s.assigning() {
		return nil
	}
	if s.activeTab != providerTabRoles {
		return s.handleCredentialRemove()
	}
	if s.selectedIdx < 0 || s.selectedIdx >= len(s.visibleItems) {
		return nil
	}
	return s.clearRoleBinding(s.visibleItems[s.selectedIdx])
}

// clearRoleBinding asks for the highlighted role to return to the session model.
// The default row is left alone: it has no binding to clear.
func (s *ProviderSelector) clearRoleBinding(item providerListItem) tea.Cmd {
	if item.Role == nil || item.Role.IsSession {
		return nil
	}
	role := item.Role.Role
	s.roleNotice = ""
	return func() tea.Msg { return providerRoleClearedMsg{Role: role} }
}

// handleRoleWrite performs the write a message asked for and refreshes the tab
// from settings. A failure stays on screen instead of closing the overlay, so
// the user can see why the assignment did not take.
func (s *ProviderSelector) handleRoleWrite(role, ref string, assign bool) {
	var err error
	if assign {
		if s.roleSource.Set == nil {
			err = fmt.Errorf("settings are unavailable")
		} else {
			err = s.roleSource.Set(role, ref)
		}
	} else if s.roleSource.Clear == nil {
		err = fmt.Errorf("settings are unavailable")
	} else {
		err = s.roleSource.Clear(role)
	}

	if err != nil {
		s.roleNotice = fmt.Sprintf("Could not %s %s: %v", roleVerb(assign), role, err)
	} else {
		s.roleNotice = ""
	}
	s.refreshRoleInfo()
	s.rebuildVisibleItems()
}

func roleVerb(assign bool) string {
	if assign {
		return "assign"
	}
	return "clear"
}

// roleSource is the live role surface the /models overlay reads and writes: the
// current bindings, and writes at user scope — a personal routing choice
// belongs in the user's own settings, not in the repository. After a write the
// settings are reloaded and the worker rebuilt, so the next agent run uses the
// new model instead of the one loaded at session start.
func (c *SlashCommandController) roleSource() RoleSource {
	return RoleSource{
		Info: func() ModelRoleInfo {
			if c.env.Setting == nil {
				return ModelRoleInfo{}
			}
			return ModelRoleInfo{
				Roles:   c.env.Setting.ModelRoles(),
				Project: setting.ModelRolesAt(c.env.Cwd, setting.ScopeProject),
				Session: c.sessionModelID(),
			}
		},
		Set: func(role, ref string) error {
			if err := setting.SetModelRole(role, ref, setting.ScopeUser); err != nil {
				return err
			}
			return c.afterRoleWrite()
		},
		Clear: func(role string) error {
			if err := setting.ClearModelRole(role, setting.ScopeUser); err != nil {
				return err
			}
			return c.afterRoleWrite()
		},
	}
}

// sessionModelID is the model the session itself runs, shown as what an
// unassigned role falls back to.
func (c *SlashCommandController) sessionModelID() string {
	if c.env.LLM == nil {
		return ""
	}
	return c.env.LLM.ModelID()
}

func (c *SlashCommandController) afterRoleWrite() error {
	if c.env.Setting != nil {
		if err := c.env.Setting.Reload(c.env.Cwd); err != nil {
			return err
		}
	}
	if c.env.ReconfigureAgentTool != nil {
		c.env.ReconfigureAgentTool()
	}
	return nil
}

// ── Roles tab rows ──────────────────────────────────────────────────────────

// providerRoleColumnWidth aligns the roles tab into a table: role, the model
// that answers it, then what it is for.
const providerRoleColumnWidth = 14

func (s *ProviderSelector) rebuildRolesTab() {
	rows := roleRows(s.roleInfo)
	query := strings.ToLower(s.searchQuery)
	for i := range rows {
		// The name matches fuzzily; the consumer line, which is a whole
		// sentence, only on a substring — a fuzzy subsequence would match nearly
		// every role and empty the filter of meaning.
		if query != "" &&
			!kit.FuzzyMatch(strings.ToLower(rows[i].Role), query) &&
			!strings.Contains(strings.ToLower(rows[i].Consumer), query) {
			continue
		}
		s.visibleItems = append(s.visibleItems, providerListItem{Kind: providerItemRole, Role: &rows[i]})
	}
}

func (s *ProviderSelector) renderRoleRow(item providerListItem, isSelected bool) string {
	r := item.Role
	if r == nil {
		return ""
	}

	answer := roleAnswer(r.Ref, s.roleInfo.Session)
	if r.ProjectOverride {
		answer += "  " + lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning).Render("project")
	}
	line := kit.FormatAlignedRow("", r.Role, providerRoleColumnWidth, answer)

	// What the role is for, in whatever room is left, so a long description
	// never wraps a row — the list assumes one line per item.
	if r.Consumer != "" {
		if budget := s.panel().ContentWidth() - lipgloss.Width(line) - 4; budget >= 12 {
			line += "  " + kit.DimStyle().Render(kit.TruncateText(r.Consumer, budget))
		}
	}
	return kit.RenderSelectableRow(line, isSelected)
}

// roleAnswer is what fills a role: the assigned reference, or the session model
// it follows. An unassigned role still runs, so the row says which model will
// answer rather than leaving the column blank.
func roleAnswer(ref, session string) string {
	ref = strings.TrimSpace(ref)
	if ref != "" && !strings.EqualFold(ref, llm.ModelInherit) {
		return ref
	}
	if session == "" {
		return kit.DimStyle().Render("(session model)")
	}
	return kit.DimStyle().Render("(session model: " + session + ")")
}

// renderAssignBanner replaces the tab bar while a role is being assigned, so the
// borrowed model list cannot be mistaken for the Models tab.
func (s *ProviderSelector) renderAssignBanner() string {
	style := lipgloss.NewStyle().
		Foreground(kit.TabActiveFg).
		Background(kit.TabActiveBg).
		Bold(true).
		Padding(0, 2)
	banner := style.Render("Assign " + s.assignRole)
	if s.roleNotice != "" {
		return banner + "  " + lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error).Render(s.roleNotice)
	}
	return banner + kit.DimStyle().Render("  pick a model · Esc back to Roles")
}

// renderRoleNotice shows a failed write under the tab bar on the Roles tab.
func (s *ProviderSelector) renderRoleNotice() string {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error).PaddingLeft(2).Render(s.roleNotice)
}
