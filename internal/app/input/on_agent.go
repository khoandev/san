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
	"github.com/genai-io/san/internal/subagent"
)

// agentTab identifies a category tab in the agent selector. The values double
// as indices into the selector's tabbedList tab order.
type agentTab int

const (
	agentTabBuiltin agentTab = iota
	agentTabProject
	agentTabUser
)

type agentItem struct {
	Name           string
	Description    string
	Model          string // the model reference as configured ("@slow", "inherit")
	PermissionMode string
	Tools          string
	Summary        string // compact label for the default worker
	Source         string // "built-in", "user", "project", "plugin"
	PluginName     string // populated when Source == "plugin" or name has "ns:" prefix
	Enabled        bool
	BuiltinPreview bool // default worker description, not a toggleable definition

	// EffectiveModel is the model that will answer this definition's work, and
	// InheritedModel reports that it is the session model because the reference
	// resolved to nothing. Both are filled from the injected AgentModelSource;
	// empty means the list does not know, and the row shows Model as written.
	EffectiveModel string
	InheritedModel bool
}

// AgentToggleMsg is sent when an agent's enabled state is toggled.
type AgentToggleMsg struct {
	AgentName string
	Enabled   bool
}

// AgentModelSource tells the agent list which model each definition will run
// on. The selector never reads settings or the provider store: the app owns
// both and hands the answers in, so the list cannot drift from what the
// executor will actually use.
type AgentModelSource struct {
	// Model resolves a model reference ("@slow", "vendor/model", "inherit") to
	// the model that will answer, reporting whether the session model answered
	// instead.
	Model func(ref string) (string, bool)
	// Worker returns the default worker's model reference: the subagentModel
	// setting when set, otherwise the task role's binding. "" is the session
	// model, which is what an unnamed worker inherits.
	Worker func() string
}

// AgentSelector holds the state for the agent selector overlay. The tab/filter/
// keypress/frame mechanics live in the embedded tabbedList; this type owns
// agent loading, the row layout, and the enable/disable action.
type AgentSelector struct {
	registry *subagent.Registry
	models   AgentModelSource
	list     tabbedList[agentItem]
}

// SetModelSource injects the model resolver the list shows with. It is set
// while the app is built, because the session model and the role bindings it
// resolves against both change while the app runs.
func (s *AgentSelector) SetModelSource(src AgentModelSource) { s.models = src }

func NewAgentSelector(reg *subagent.Registry) AgentSelector {
	return AgentSelector{
		registry: reg,
		list: tabbedList[agentItem]{
			tabs: []tabSpec{
				{name: "Built-in", disableIfEmpty: true},
				{name: "Project"},
				{name: "User"},
			},
			// Project and User hold definitions; Built-in describes the default worker.
			preferred:   []int{int(agentTabProject), int(agentTabUser), int(agentTabBuiltin)},
			noun:        "agents",
			placeholder: "Type to filter agents...",
			hints:       []string{"↑/↓ navigate", "Enter enable/disable", "←/→/Tab switch tab", "Esc cancel"},
			matchesTab:  agentMatchesTab,
			searchKeys: func(a agentItem) []string {
				return []string{a.Name, a.Description, a.PermissionMode, a.Summary, a.Model}
			},
			nav: kit.ListNav{MaxVisible: 10},
		},
	}
}

// EnterSelect activates the selector and loads agents from the registry.
func (s *AgentSelector) EnterSelect(width, height int) error {
	configs := s.registry.ListConfigs()
	disabledByScope := map[setting.Scope]map[string]bool{
		setting.ScopeProject: s.registry.DisabledAt(setting.ScopeProject),
		setting.ScopeUser:    s.registry.DisabledAt(setting.ScopeUser),
	}

	agents := make([]agentItem, 0, len(configs)+1)
	agents = append(agents, s.builtinAgentItems()...)
	// One row per definition. The registry is keyed by name, so this only keeps
	// a registry definition from colliding with the one row that is not a
	// definition: the default worker's.
	seen := map[string]bool{"built-in/agent": true}
	for _, c := range configs {
		cfg := subagent.ToAgentConfigInfo(c)
		lowerName := strings.ToLower(cfg.Name)
		var pluginName string
		if idx := strings.Index(cfg.Name, ":"); idx > 0 {
			pluginName = cfg.Name[:idx]
		}
		source := cfg.Source
		key := source + "/" + lowerName
		if seen[key] {
			continue
		}
		seen[key] = true
		// Built-in and user agents keep their disabled state in user scope,
		// project agents in project scope.
		scope := setting.ScopeProject
		if source == "user" || source == "built-in" {
			scope = setting.ScopeUser
		}
		item := agentItem{
			Name:           cfg.Name,
			Description:    cfg.Description,
			Model:          cfg.Model,
			PermissionMode: formatAgentPermMode(cfg.PermissionMode),
			Tools:          formatAgentTools(cfg.Tools),
			Source:         source,
			PluginName:     pluginName,
			Enabled:        !disabledByScope[scope][lowerName],
		}
		s.resolveModel(&item)
		agents = append(agents, item)
	}

	sortAgentItems(agents)
	s.list.load(agents, width, height)
	return nil
}

// builtinAgentItems describes the sole default worker, not a registered
// definition: it is what the Agent tool runs when no specialist is named, so it
// has no toggle and its model follows the task role unless subagentModel
// overrides it.
func (s *AgentSelector) builtinAgentItems() []agentItem {
	item := agentItem{
		Name:           "Agent",
		Summary:        "inherits session permissions; model follows the task role",
		Source:         "built-in",
		Enabled:        true,
		BuiltinPreview: true,
	}
	if s.models.Worker != nil {
		item.Model = strings.TrimSpace(s.models.Worker())
	}
	s.resolveModel(&item)
	return []agentItem{item}
}

// sortAgentItems keeps the list stable and readable: the default worker's row
// first, then names in order. The registry hands definitions out in map order,
// which would otherwise shuffle rows on every open.
func sortAgentItems(items []agentItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].BuiltinPreview != items[j].BuiltinPreview {
			return items[i].BuiltinPreview
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}

// resolveModel records the model a definition will actually run on. Without an
// injected source the reference is shown as written, which is the most the list
// can say on its own.
func (s *AgentSelector) resolveModel(item *agentItem) {
	if s.models.Model == nil {
		return
	}
	ref := strings.TrimSpace(item.Model)
	if ref == "" {
		// An empty model is the session model by definition; asking about
		// "inherit" is how a row learns which model that is.
		ref = llm.ModelInherit
	}
	model, inherited := s.models.Model(ref)
	if model == "" {
		return
	}
	item.EffectiveModel, item.InheritedModel = model, inherited
}

// modelLabel is the row cell for a definition's model: the configured
// reference, and for a role the model it resolves to — so "@slow" reads as a
// model rather than a slot name.
func (a agentItem) modelLabel() string {
	ref := strings.TrimSpace(a.Model)
	if _, isRole := llm.ParseRoleAlias(ref); !isRole {
		return ref
	}
	switch {
	case a.InheritedModel:
		return ref + " → session model"
	case a.EffectiveModel != "":
		return ref + " → " + a.EffectiveModel
	}
	return ref
}

// workerLabel names the model the default worker runs on. The worker inherits
// the session model when nothing configures it, so that is what it says.
func (a agentItem) workerLabel() string {
	if a.EffectiveModel != "" {
		if a.InheritedModel {
			return "session model (" + a.EffectiveModel + ")"
		}
		return a.EffectiveModel
	}
	if ref := strings.TrimSpace(a.Model); ref != "" {
		return ref
	}
	return "session model"
}

// modelNote completes the model cell on the selected row's detail line: a role
// that fell back to the session model names the model it fell back to.
func (a agentItem) modelNote() string {
	if a.EffectiveModel == "" || strings.Contains(a.modelLabel(), a.EffectiveModel) {
		return ""
	}
	return a.EffectiveModel
}

// formatAgentPermMode names a definition's permission mode in the eight columns
// the row gives it. The stored spellings are long ("acceptEdits",
// "bypassPermissions"), so they are folded to the words a user picks between
// rather than truncated mid-word.
func formatAgentPermMode(mode string) string {
	switch subagent.NormalizePermissionMode(mode) {
	case subagent.PermissionExplore:
		return "explore"
	case subagent.PermissionAcceptEdits, subagent.PermissionAuto:
		return "edit"
	case subagent.PermissionBypass:
		return "bypass"
	case subagent.PermissionDontAsk:
		return "dont-ask"
	case subagent.PermissionDefault:
		return "default"
	default:
		return string(subagent.NormalizePermissionMode(mode))
	}
}

func formatAgentTools(tools []string) string {
	if tools == nil {
		return "all tools"
	}
	if len(tools) == 0 {
		return "none"
	}
	return strings.Join(tools, ", ")
}

func (s *AgentSelector) IsActive() bool { return s.list.active }

// agentMatchesTab returns true when an agent belongs to the given tab.
// Plugin agents are folded into Project or User tab depending on install path
// (a "user-plugin" source is treated as User; bare "plugin" defaults to Project).
func agentMatchesTab(a agentItem, tab int) bool {
	switch agentTab(tab) {
	case agentTabBuiltin:
		return a.Source == "built-in"
	case agentTabProject:
		return a.Source == "project" || a.Source == "plugin" || a.Source == "project-plugin"
	case agentTabUser:
		return a.Source == "user" || a.Source == "user-plugin"
	}
	return false
}

// activeScope is where the active tab saves: Built-in and User tabs in user
// scope, the Project tab in project scope.
func (s *AgentSelector) activeScope() setting.Scope {
	if s.list.activeTab == int(agentTabProject) {
		return setting.ScopeProject
	}
	return setting.ScopeUser
}

func (s *AgentSelector) Toggle() tea.Cmd {
	if len(s.list.filtered) == 0 || s.list.nav.Selected >= len(s.list.filtered) {
		return nil
	}
	selected := &s.list.filtered[s.list.nav.Selected]
	if selected.BuiltinPreview {
		return nil
	}
	selected.Enabled = !selected.Enabled
	for i := range s.list.items {
		if !s.list.items[i].BuiltinPreview && s.list.items[i].Name == selected.Name {
			s.list.items[i].Enabled = selected.Enabled
			break
		}
	}
	_ = s.registry.SetEnabled(selected.Name, selected.Enabled, s.activeScope())
	return func() tea.Msg {
		return AgentToggleMsg{AgentName: selected.Name, Enabled: selected.Enabled}
	}
}

func (s *AgentSelector) HandleKeypress(key tea.KeyMsg) tea.Cmd {
	return s.list.handleKey(key, s.Toggle)
}

// ── Rendering ──────────────────────────────────────────────────────────────────

func (s *AgentSelector) Render() string {
	return s.list.render(s.renderItemList)
}

func (s *AgentSelector) renderItemList(sb *strings.Builder, panel kit.Panel) {
	startIdx, endIdx := s.list.nav.VisibleRange()

	if startIdx > 0 {
		sb.WriteString(kit.MoreAbove())
		sb.WriteString("\n")
	}

	// Compute the name and model column widths from the visible items so the
	// mode and tool columns align while still adapting to long names.
	maxNameLen, maxModelLen := 12, 14
	for i := startIdx; i < endIdx; i++ {
		if w := lipgloss.Width(s.list.filtered[i].Name); w > maxNameLen {
			maxNameLen = w
		}
		if w := lipgloss.Width(s.list.filtered[i].modelLabel()); w > maxModelLen {
			maxModelLen = w
		}
	}
	maxNameLen = min(maxNameLen, 28)
	// A role row carries two facts — the role and the model it resolved to — so
	// the column grows for it, but never past what leaves the tool column
	// legible.
	maxModelLen = min(maxModelLen, 30)
	maxModelLen = min(maxModelLen, max(14, panel.ContentWidth()-4-(2+1+1+maxNameLen+2+2+8+2+8)))

	descStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	badge := kit.BadgeStyle()

	for i := startIdx; i < endIdx; i++ {
		a := s.list.filtered[i]

		var statusIcon string
		var statusStyle lipgloss.Style
		if a.BuiltinPreview {
			statusIcon = "◇"
			statusStyle = kit.SelectorStatusNone()
		} else if a.Enabled {
			statusIcon = "●"
			statusStyle = kit.SelectorStatusConnected()
		} else {
			statusIcon = "○"
			statusStyle = kit.SelectorStatusNone()
		}

		// Pad by display width, not bytes: one CJK agent name would otherwise
		// shift the model, mode and badge columns on that row only.
		name := kit.TruncateText(a.Name, maxNameLen)
		paddedName := name + strings.Repeat(" ", max(0, maxNameLen-lipgloss.Width(name)))

		var line string
		if a.BuiltinPreview {
			// The default worker is a description, not a definition: the row
			// says what it inherits, and which model it lands on.
			preview := a.Summary + "  ·  " + a.workerLabel()
			if a.Description != "" {
				preview += "  " + a.Description
			}
			previewWidth := max(8, panel.ContentWidth()-4-(2+1+1+maxNameLen+2)-4)
			line = fmt.Sprintf("%s %s  %s", statusStyle.Render(statusIcon), paddedName, descStyle.Render(kit.TruncateText(preview, previewWidth)))
		} else {
			model := kit.TruncateText(a.modelLabel(), maxModelLen)
			paddedModel := model + strings.Repeat(" ", max(0, maxModelLen-lipgloss.Width(model)))

			mode := kit.TruncateText(a.PermissionMode, 8)
			paddedMode := mode + strings.Repeat(" ", max(0, 8-lipgloss.Width(mode)))

			badgeText := ""
			switch {
			case a.PluginName != "":
				badgeText = "[Plugin: " + a.PluginName + "]"
			case a.Source == "built-in":
				badgeText = "[Built-in]"
			}

			// Leave the remaining width for the configured tool list.
			rowFixed := 2 + 1 + 1 + maxNameLen + 2 + maxModelLen + 2 + 8 + 2
			if badgeText != "" {
				rowFixed += 1 + len(badgeText)
			}
			toolsWidth := max(8, panel.ContentWidth()-4-rowFixed-4)
			tools := kit.TruncateText(a.Tools, toolsWidth)

			line = fmt.Sprintf("%s %s  %s  %s  %s",
				statusStyle.Render(statusIcon),
				paddedName,
				paddedModel,
				paddedMode,
				descStyle.Render(tools),
			)
			if badgeText != "" {
				line += " " + badge.Render(badgeText)
			}
		}

		// Render the row without the selector row styles' PaddingLeft(2) so
		// the row's left edge lines up with tabs/search/
		// separator. Width(...) right-pads each row to the full inner content
		// area so the right edge also matches the separator line.
		rowWidth := max(20, panel.ContentWidth()-4)
		sb.WriteString(kit.RenderPanelRow(line, i == s.list.nav.Selected, rowWidth))
		sb.WriteString("\n")

		// Description sub-line aligned under the agent name (4 cols in:
		// 2 cursor + 1 icon + 1 space). A role row that fell back to the
		// session model names that model here, where there is room for it.
		if i == s.list.nav.Selected && !a.BuiltinPreview && (a.Description != "" || a.modelNote() != "") {
			// The model note comes first: a long description would otherwise
			// push it past the line and truncate it away.
			detail := ""
			if note := a.modelNote(); note != "" {
				detail = "session model: " + note
			}
			if a.Description != "" {
				if detail != "" {
					detail += "  ·  "
				}
				detail += a.Description
			}
			subStyle := lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Muted).
				PaddingLeft(4)
			descLineWidth := max(10, panel.ContentWidth()-8)
			sb.WriteString(subStyle.Render(kit.TruncateText(detail, descLineWidth)))
			sb.WriteString("\n")
		}

		// Spacer for breathing room between rows (skip after the last item;
		// PadViewport will handle the trailing blank lines).
		if i < endIdx-1 {
			sb.WriteString("\n")
		}
	}

	if endIdx < len(s.list.filtered) {
		sb.WriteString(kit.MoreBelow())
		sb.WriteString("\n")
	}
}
