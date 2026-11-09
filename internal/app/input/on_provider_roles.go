// Provider selector: the Roles tab, where a model role is bound to the model
// that answers its work, and the assign mode that borrows the Models list to
// pick one.
package input

import (
	"context"
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
// roles and efforts the project scope assigns itself, and the session model an
// unassigned role follows. The overlay never reads settings directly — the app
// hands it this snapshot, so persistence and the reload that follows live in
// one place.
type ModelRoleInfo struct {
	Roles   map[string]string
	Project map[string]string
	// Efforts is the effective role→reasoning-effort binding, the same merge
	// (user, then project) the resolver reads. A role missing from it answers
	// at its model's own default.
	Efforts map[string]string
	// ProjectEfforts is what the project scope binds, for the row badge.
	ProjectEfforts map[string]string
	Session        string
	// SessionEffort is the session's own thinking effort, shown on the default
	// row because that row *is* the session model.
	SessionEffort string
}

// RoleSource connects the Roles tab to settings: the snapshot to display and
// the three writes that change it. All return the settings error instead of
// swallowing it, so a failed write is visible where the user made it.
type RoleSource struct {
	Info func() ModelRoleInfo
	Set  func(role, ref string) error
	// SetEffort binds how hard the role's model is asked to think. An empty
	// effort clears the binding, which is how "model default" is stored.
	SetEffort func(role, effort string) error
	Clear     func(role string) error
}

// SetRoleSource injects the role surface. It is set on each /models open,
// because settings are reloaded in between.
func (s *ProviderSelector) SetRoleSource(src RoleSource) { s.roleSource = src }

// providerRoleItem is one row of the Roles tab before it is rendered: a role,
// what it is bound to, and what that binding is for.
type providerRoleItem struct {
	Role string
	// Ref is the assigned reference; "" when the role follows the session model.
	Ref string
	// Effort is the reasoning effort bound to the role; "" when its model
	// answers at its own default.
	Effort      string
	Description string
	Consumer    string
	// ProjectOverride marks a role the project scope configures — a model, an
	// effort, or both. It outranks the user's own binding, so a row that looks
	// wrong in a repository is explained here rather than by reading two
	// settings files.
	ProjectOverride bool
	// IsSession is the default role: it means "the session's own model", which
	// the model picker owns, so Enter opens that picker rather than assigning.
	IsSession bool
	// SessionEffort is the session's own effort, rendered on the IsSession row.
	SessionEffort string
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
		rows = append(rows, roleRow(info, role, spec.Description, spec.Consumer))
	}

	// A hand-edited file may name a role the catalog does not describe. Both
	// maps are scanned: an effort-only entry still routes work, so hiding it
	// would leave a binding the tab could never clear.
	seen := make(map[string]bool, len(info.Roles)+len(info.Efforts))
	for name := range info.Roles {
		seen[strings.ToLower(strings.TrimSpace(name))] = true
	}
	for name := range info.Efforts {
		seen[strings.ToLower(strings.TrimSpace(name))] = true
	}
	var custom []string
	for role := range seen {
		// A retired per-agent name folds onto a rung, so it occupies no row of
		// its own: its binding is shown on the rung it now answers for.
		if role == "" || known[role] || llm.LegacyRoleAlias(role) != role {
			continue
		}
		custom = append(custom, role)
	}
	sort.Strings(custom)
	for _, role := range custom {
		rows = append(rows, roleRow(info, role, "", ""))
	}
	return rows
}

// roleRow fills one row from the snapshot: its bindings, and what it is for.
func roleRow(info ModelRoleInfo, role, description, consumer string) providerRoleItem {
	isSession := role == string(llm.RoleDefault)
	row := providerRoleItem{
		Role:            role,
		Ref:             roleRef(info.Roles, role),
		Effort:          roleRef(info.Efforts, role),
		Description:     description,
		Consumer:        consumer,
		ProjectOverride: roleRef(info.Project, role) != "" || roleRef(info.ProjectEfforts, role) != "",
		IsSession:       isSession,
	}
	if isSession {
		// The default row has no binding of its own: it names the session
		// model, so it shows the session's effort with it.
		row.Effort = ""
		row.SessionEffort = info.SessionEffort
	}
	return row
}

// roleRef reads a binding by role name through the resolver's own lookup rule,
// so a rung row shows the binding its work actually runs on — including one
// written under a retired name.
func roleRef(roles map[string]string, role string) string {
	return llm.RoleBinding(roles, role)
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

// pickingEffort reports the second assign step, where the chosen model's
// reasoning rungs are listed instead of the model list.
func (s *ProviderSelector) pickingEffort() bool { return s.assigning() && s.assignRef != "" }

// selectRole handles Enter on a role row. The default role is the session's own
// model, so it opens the model list instead of assigning a second model to it;
// every other role starts assign mode.
func (s *ProviderSelector) selectRole(item providerListItem) tea.Cmd {
	if item.Role == nil {
		return nil
	}
	if item.Role.IsSession {
		s.assignRole = ""
		s.clearEffortStep()
		s.switchTab(providerTabModels)
		return nil
	}
	s.assignRole = item.Role.Role
	s.clearEffortStep()
	s.roleNotice = ""
	s.resetNavigation()
	s.resetModelSearch()
	s.rebuildVisibleItems()
	return nil
}

// assignModel is step one of an assignment: the highlighted model is bound to
// the role being assigned. The reference is written vendor-qualified, the form
// the resolver can route even when the session later moves to another
// provider's model.
//
// A model that reasons gets a second step — how hard it should think for this
// role — and nothing is written until that is answered, which is what makes
// Esc at the second step cost nothing. A model without rungs is written
// straight away, with an empty effort that also clears any stale binding.
func (s *ProviderSelector) assignModel(m *providerModelItem) tea.Cmd {
	role := s.assignRole
	if m == nil || role == "" {
		return nil
	}
	ref := m.ProviderName + "/" + m.ID
	efforts := s.modelEfforts(m)
	if len(efforts) == 0 {
		s.finishAssign()
		return func() tea.Msg { return providerRoleAssignedMsg{Role: role, Ref: ref} }
	}
	s.assignRef = ref
	s.assignEfforts = effortChoices(efforts)
	s.resetNavigation()
	s.resetModelSearch()
	s.rebuildVisibleItems()
	return nil
}

// assignEffort is step two: the picked rung completes the assignment. An empty
// label is the model's own default, stored as "no binding".
func (s *ProviderSelector) assignEffort(e *providerEffortItem) tea.Cmd {
	role, ref := s.assignRole, s.assignRef
	if e == nil || role == "" || ref == "" {
		return nil
	}
	effort := e.Label
	s.finishAssign()
	return func() tea.Msg { return providerRoleAssignedMsg{Role: role, Ref: ref, Effort: effort} }
}

// clearEffortStep drops the second assign step's state, so returning to the
// model list cannot leave a stale effort list behind.
func (s *ProviderSelector) clearEffortStep() {
	s.assignRef = ""
	s.assignEfforts = nil
}

// finishAssign leaves assign mode for the Roles tab, whether the user picked a
// model or backed out. Nothing is written here: the write is the message the
// caller handles.
func (s *ProviderSelector) finishAssign() {
	s.assignRole = ""
	s.clearEffortStep()
	s.activeTab = providerTabRoles
	s.resetNavigation()
	s.resetModelSearch()
	s.refreshRoleInfo()
	s.rebuildVisibleItems()
}

// modelEfforts lists the reasoning rungs the model row advertises. The store
// carries the live catalog, so this is the same ladder the resolver validates a
// stored effort against when the role actually runs.
func (s *ProviderSelector) modelEfforts(m *providerModelItem) []string {
	if m == nil || !m.Thinks {
		return nil
	}
	p, err := llm.GetProvider(context.Background(), llm.ProviderID(m.ProviderName), m.AuthMethod)
	if err != nil || p == nil {
		return nil
	}
	current := &llm.CurrentModelInfo{ModelID: m.ID, Provider: llm.ProviderID(m.ProviderName), AuthMethod: m.AuthMethod}
	return llm.ThinkingEffortsForModel(p, s.store, current)
}

// effortChoices builds the second step's list: the model's own default first,
// because that is what an unbound role does, then the rungs least to most.
func effortChoices(efforts []string) []providerEffortItem {
	choices := make([]providerEffortItem, 0, len(efforts)+1)
	choices = append(choices, providerEffortItem{Display: "model default", Default: true})
	for _, effort := range efforts {
		choices = append(choices, providerEffortItem{Label: effort, Display: effort})
	}
	return choices
}

// rebuildEffortStep lists the second step's choices.
func (s *ProviderSelector) rebuildEffortStep() {
	for i := range s.assignEfforts {
		s.visibleItems = append(s.visibleItems, providerListItem{Kind: providerItemEffort, Effort: &s.assignEfforts[i]})
	}
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
func (s *ProviderSelector) handleRoleWrite(role, ref, effort string, assign bool) {
	var err error
	if assign {
		switch {
		case s.roleSource.Set == nil || s.roleSource.SetEffort == nil:
			err = fmt.Errorf("settings are unavailable")
		default:
			// Model first, effort second: a stored effort is a hint the resolver
			// re-validates against whatever model the role runs on, so a failed
			// effort write leaves a usable binding rather than none.
			if err = s.roleSource.Set(role, ref); err == nil {
				err = s.roleSource.SetEffort(role, effort)
			}
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
			sessionEffort := ""
			if c.env.GetThinkingEffort != nil {
				sessionEffort = c.env.GetThinkingEffort()
			}
			return ModelRoleInfo{
				Roles:          c.env.Setting.ModelRoles(),
				Project:        setting.ModelRolesAt(c.env.Cwd, setting.ScopeProject),
				Efforts:        c.env.Setting.ModelRoleEfforts(),
				ProjectEfforts: setting.ModelRoleEffortsAt(c.env.Cwd, setting.ScopeProject),
				Session:        c.sessionModelID(),
				SessionEffort:  sessionEffort,
			}
		},
		Set: func(role, ref string) error {
			if err := setting.SetModelRole(role, ref, setting.ScopeUser); err != nil {
				return err
			}
			return c.afterRoleWrite()
		},
		SetEffort: func(role, effort string) error {
			// An empty effort clears the binding, which is how "model default"
			// is stored: the role answers at whatever rung its model picks.
			if err := setting.SetModelRoleEffort(role, effort, setting.ScopeUser); err != nil {
				return err
			}
			return c.afterRoleWrite()
		},
		Clear: func(role string) error {
			// Clearing the role drops its effort too, so the next binding does
			// not inherit the previous model's depth.
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
	// The effort is a qualifier of the model, not a second column: it reads
	// dim, right after the reference it applies to.
	if effort := r.Effort; effort != "" {
		answer += "  " + kit.DimStyle().Render(effort)
	} else if r.IsSession && r.SessionEffort != "" {
		answer += "  " + kit.DimStyle().Render(r.SessionEffort)
	}
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

// renderEffortRow renders one choice of the second assign step: the model's own
// default, then its rungs. The row is the label and, for the default, what
// picking it means.
func (s *ProviderSelector) renderEffortRow(item providerListItem, isSelected bool) string {
	e := item.Effort
	if e == nil {
		return ""
	}
	info := ""
	if e.Default {
		info = kit.DimStyle().Render("whatever the model picks on its own")
	}
	return kit.RenderSelectableRow(kit.FormatAlignedRow("", e.Display, providerRoleColumnWidth, info), isSelected)
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
	title := "Assign " + s.assignRole
	if s.pickingEffort() {
		title += " → " + s.assignRef
	}
	banner := style.Render(title)
	if s.roleNotice != "" {
		return banner + "  " + lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error).Render(s.roleNotice)
	}
	if s.pickingEffort() {
		return banner + kit.DimStyle().Render("  pick an effort · Esc back to the model list")
	}
	return banner + kit.DimStyle().Render("  pick a model · Esc back to Roles")
}

// renderRoleNotice shows a failed write under the tab bar on the Roles tab.
func (s *ProviderSelector) renderRoleNotice() string {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error).PaddingLeft(2).Render(s.roleNotice)
}
