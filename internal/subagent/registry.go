package subagent

import (
	"sort"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// Registry manages agent definitions.
type Registry struct {
	mu           sync.RWMutex
	agents       map[string]*AgentConfig
	userStore    *AgentStore     // User-level enabled/disabled states
	projectStore *AgentStore     // Project-level enabled/disabled states
	personaAllow map[string]bool // active persona's visible-agent allow-list (nil = no restriction)
	cwd          string          // Current working directory

	// agentRoles answers the model role /settings assigns to a definition, or ""
	// when it keeps the one its own file declares. The catalog reads it per
	// build, so the resolver must read live settings rather than a snapshot.
	agentRoles func(name string) string
}

// NewRegistry creates an empty registry for user, project, and plugin-defined agents.
func NewRegistry() *Registry {
	return &Registry{agents: make(map[string]*AgentConfig)}
}

// defaultRegistry is the package-level agent registry.
// Layer-two initialization replaces it atomically when loading definitions.
var defaultRegistry = NewRegistry()

// SetAgentRoleSource wires the resolver that answers which model role /settings
// assigns to a definition. Passing nil returns the catalog to what each
// definition declares itself.
func (r *Registry) SetAgentRoleSource(fn func(name string) string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentRoles = fn
}

// Register adds an agent configuration to the registry
func (r *Registry) Register(config *AgentConfig) {
	config.Name = strings.TrimSpace(config.Name)
	if config.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[strings.ToLower(config.Name)] = config
}

// Get retrieves an agent configuration by name
func (r *Registry) Get(name string) (*AgentConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	config, ok := r.agents[strings.ToLower(name)]
	return config, ok
}

// retiredAgentNames maps a definition that no longer ships to the one that took
// over its work: the upstream-docs half of librarian is now explore's, and the
// interface half of designer is now task's. It is the compatibility promise of
// the four-agent set — an Agent call, an agents.json entry or a persona written
// before the collapse still names something real — and it is deliberately not
// in AgentCatalog, because a retired name is an old spelling, not a choice.
var retiredAgentNames = map[string]string{
	"librarian": "explore",
	"designer":  "task",
}

// LookupAgent returns an agent definition together with whether it exists and
// is enabled. Keeping the two states separate lets callers reject an explicitly
// disabled definition while treating an unknown name as a display-only label.
// A retired name resolves to the definition that absorbed its work, unless the
// user defined one under that name themselves — a file always wins over an
// alias.
func (r *Registry) LookupAgent(name string) (config *AgentConfig, exists, enabled bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lowerName := strings.ToLower(strings.TrimSpace(name))
	config, exists = r.agents[lowerName]
	if !exists {
		if canonical, retired := retiredAgentNames[lowerName]; retired {
			lowerName = canonical
			config, exists = r.agents[lowerName]
		}
	}
	if !exists {
		return nil, false, false
	}
	return config, true, !r.isDisabledInternal(lowerName)
}

// ReadOnly reports whether the named definition cannot write: its normalized
// mode is explore and it is enabled under the active persona and the
// enable/disable stores. An unknown or disabled name is not read-only — the
// runtime cannot vouch for a definition it will not run.
func (r *Registry) ReadOnly(name string) bool {
	config, exists, enabled := r.LookupAgent(name)
	if !exists || !enabled {
		return false
	}
	return NormalizePermissionMode(string(config.PermissionMode)) == PermissionExplore
}

// ListConfigs returns all registered agent configurations that are visible
// under the active persona allow-list.
func (r *Registry) ListConfigs() []*AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	configs := make([]*AgentConfig, 0, len(r.agents))
	for name, config := range r.agents {
		if r.personaAllow != nil && !r.personaAllow[name] {
			continue
		}
		configs = append(configs, config)
	}
	return configs
}

// InitStores initializes the user and project stores for enabled/disabled state
func (r *Registry) InitStores(cwd string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cwd = cwd
	r.userStore = NewUserAgentStore()
	r.projectStore = NewProjectAgentStore(cwd)
	return nil
}

// IsEnabled returns whether an agent is enabled
// An agent is enabled unless explicitly disabled in either store
// Project-level settings take priority over user-level
func (r *Registry) IsEnabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lowerName := strings.ToLower(name)

	// The active persona's allow-list restricts the visible set: an agent not
	// on it is treated as disabled while that persona is selected.
	if r.personaAllow != nil && !r.personaAllow[lowerName] {
		return false
	}

	// Check project store first (higher priority)
	if r.projectStore != nil && r.projectStore.IsDisabled(lowerName) {
		return false
	}

	// Check user store
	if r.userStore != nil && r.userStore.IsDisabled(lowerName) {
		return false
	}

	return true
}

// SetEnabled enables or disables an agent in scope's agents file.
func (r *Registry) SetEnabled(name string, enabled bool, scope setting.Scope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.store(scope); s != nil {
		return s.SetDisabled(strings.ToLower(name), !enabled)
	}
	return nil
}

// DisabledAt returns the agents disabled in scope.
func (r *Registry) DisabledAt(scope setting.Scope) map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s := r.store(scope); s != nil {
		return s.Disabled()
	}
	return make(map[string]bool)
}

// store is the agents store for scope; nil before InitStores.
func (r *Registry) store(scope setting.Scope) *AgentStore {
	if scope == setting.ScopeUser {
		return r.userStore
	}
	return r.projectStore
}

// LoadPersona restricts the visible agent set to an allow-list while a persona
// is active: only the named agents are spawnable and shown in the agents
// directory. An empty/blank list clears the restriction (all agents visible).
// In-memory only — never written to the user/project enable-disable stores, so
// it composes with them and disappears when the persona is cleared.
func (r *Registry) LoadPersona(allow []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	m := make(map[string]bool, len(allow))
	for _, n := range allow {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			m[n] = true
		}
	}
	if len(m) == 0 {
		r.personaAllow = nil
		return
	}
	r.personaAllow = m
}

// ClearPersona removes any persona allow-list, making all agents visible again
// (subject to the user/project enable-disable stores).
func (r *Registry) ClearPersona() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.personaAllow = nil
}

// isDisabledInternal checks if an agent is disabled (must be called with lock held)
func (r *Registry) isDisabledInternal(name string) bool {
	if r.personaAllow != nil && !r.personaAllow[name] {
		return true
	}
	if r.projectStore != nil && r.projectStore.IsDisabled(name) {
		return true
	}
	if r.userStore != nil && r.userStore.IsDisabled(name) {
		return true
	}
	return false
}

// AgentCatalog returns discovery metadata for the Agent tool description.
// Only enabled definitions are listed, sorted by name. Each entry names the
// definition, the write boundary it runs behind, and — when it runs on one —
// the role whose model and effort its work uses, because that last pair is
// what a user tunes in /models, plus the tier of work the definition is for,
// which is what a delegating model choosing on cost alone reads. Full prompts,
// tool rules and exact model ids are not included.
func (r *Registry) AgentCatalog() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type entry struct {
		name, desc, whenToUse, label string
	}

	var entries []entry
	for name, config := range r.agents {
		if r.isDisabledInternal(name) {
			continue
		}
		entries = append(entries, entry{
			name:      config.Name,
			desc:      strings.Join(strings.Fields(config.Description), " "),
			whenToUse: strings.Join(strings.Fields(config.WhenToUse), " "),
			label:     catalogLabel(config, r.modelRefFor(config)),
		})
	}

	if len(entries) == 0 {
		return ""
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	var sb strings.Builder
	sb.WriteString("Available predefined agents:\n\n")
	for i, e := range entries {
		sb.WriteString("- " + e.name + " [" + e.label + "]: " + e.desc)
		if e.whenToUse != "" {
			sb.WriteString("\n  " + guidanceLine(e.whenToUse))
		}
		if i < len(entries)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// modelRefFor is the model reference the definition's work will actually run on:
// the role /settings assigns to it when one is set, otherwise the one its own
// file declares. AgentCatalog calls it with r.mu held, so it reads the resolver
// field without taking the lock again.
func (r *Registry) modelRefFor(config *AgentConfig) string {
	if r.agentRoles != nil {
		if role := strings.TrimSpace(r.agentRoles(config.Name)); role != "" {
			return "@" + strings.TrimPrefix(role, "@")
		}
	}
	return config.Model
}

// catalogLabel summarises what a definition declares: the mode its author set,
// the role whose model and effort the work is billed to, and the tier of work it
// is for. The role is the effective one — an assignment in /settings replaces the
// one the file declares, and a delegating model reads this catalog before it
// picks an agent, so the two must not disagree. It is the declared mode, not the
// boundary the runtime ends up enforcing — mode "default" still asks before a
// mutation, and asks resolve against the parent's posture.
func catalogLabel(config *AgentConfig, modelRef string) string {
	label := modeLabel(config.PermissionMode)
	if role, ok := llm.ParseRoleAlias(modelRef); ok {
		label += ", @" + role
	}
	if tier := NormalizeAgentTier(string(config.Tier)); tier != "" {
		label += ", " + string(tier)
	}
	return label
}

// modeLabel names the declared mode in the words a delegating model acts on:
// "read-only" never changes the workspace, so sending it a fix wastes a round
// trip, while "edit" may. "default" is a definition that declared nothing and
// still needs approval for a mutation, so it is reported as declared rather
// than folded into either promise.
func modeLabel(mode PermissionMode) string {
	switch NormalizePermissionMode(string(mode)) {
	case PermissionExplore:
		return "read-only"
	case PermissionAcceptEdits, PermissionBypass, PermissionAuto:
		return "edit"
	default:
		return "default"
	}
}

// guidanceLine renders the hint under a catalog entry. A definition written in
// the canonical "When: … / Not when: …" shape already reads as a sentence;
// older user files keep the "Use when:" prefix their author wrote for.
func guidanceLine(when string) string {
	if strings.HasPrefix(strings.ToLower(when), "when:") {
		return when
	}
	return "Use when: " + when
}
