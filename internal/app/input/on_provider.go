// Provider selector: data model, overlay state, messages, and top-level
// message routing for the unified model & provider selection overlay.
//
// Navigation and selection live in on_provider_nav.go; credential entry and
// the connect/refresh flow in on_provider_auth.go; data loading in
// on_provider_data.go; rendering in on_provider_view.go.
package input

import (
	"context"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
)

// providerTab represents which tab is active in the provider selector. The
// declaration order is the display order and the cycle order: roles first,
// because routing a role to a model and an effort is what this overlay is
// opened for, then the model list, then the providers behind both.
type providerTab int

const (
	providerTabRoles     providerTab = iota // model role assignment tab (the opening tab)
	providerTabModels                       // model selection tab
	providerTabProviders                    // provider management tab
)

// providerTabCount is how many tabs NextTab/PrevTab cycle through.
const providerTabCount = 3

// providerItemKind represents a row type in the visible-items list.
type providerItemKind int

const (
	providerItemProviderHeader providerItemKind = iota // non-selectable provider group header (Models tab)
	providerItemModel                                  // selectable model row (Models tab)
	providerItemProvider                               // provider row (Providers tab)
	providerItemAuthMethod                             // expanded auth-method sub-row (Providers tab)
	providerItemRole                                   // selectable role row (Roles tab)
	providerItemEffort                                 // selectable effort row (Roles tab, assign step 2)
)

// providerListItem is a single row in the flattened visible-items list.
type providerListItem struct {
	Kind        providerItemKind
	Model       *providerModelItem
	Provider    *providerProviderItem
	AuthMethod  *providerAuthMethodItem
	Role        *providerRoleItem
	Effort      *providerEffortItem
	ProviderIdx int // index into allProviders
}

// providerEffortItem is one choice in the second assign step: an effort rung
// the chosen model advertises, or the model's own default. Label is what gets
// stored ("" for the default, which clears any existing binding).
type providerEffortItem struct {
	Label   string
	Display string
	Default bool
}

// providerProviderItem represents a provider with its auth methods.
type providerProviderItem struct {
	Provider    llm.ProviderID
	DisplayName string
	AuthMethods []providerAuthMethodItem
	Connected   bool // whether this provider has at least one connected auth method
}

// providerAuthMethodItem represents an auth method in the second level.
type providerAuthMethodItem struct {
	Provider        llm.ProviderID
	AuthMethod      llm.AuthMethod
	DisplayName     string
	Status          llm.Status
	EnvVars         []string
	OptionalEnvVars []string
	Hint            string
}

// providerModelItem represents a model in the provider selector.
type providerModelItem struct {
	ID            string
	Name          string
	DisplayName   string
	ProviderName  string
	AuthMethod    llm.AuthMethod
	IsCurrent     bool
	ContextWindow int
	MaxOutput     int
	Lifecycle     llm.ModelLifecycle
	Replacement   string
	TextOnly      bool
	Thinks        bool
	// Scores are the benchmark figures the row draws, when a cached dataset
	// knows the model. Nil is the common case.
	Scores *llm.ModelScores
}

func newProviderModelItem(mdl llm.ModelInfo, providerName string, authMethod llm.AuthMethod, current *llm.CurrentModelInfo) providerModelItem {
	return providerModelItem{
		ID:            mdl.ID,
		Name:          mdl.Name,
		DisplayName:   modelInfoName(mdl, providerName, authMethod),
		ProviderName:  providerName,
		AuthMethod:    authMethod,
		IsCurrent:     current != nil && current.ModelID == mdl.ID && string(current.Provider) == providerName && current.AuthMethod == authMethod,
		ContextWindow: mdl.ContextWindow,
		MaxOutput:     mdl.MaxOutput,
		Lifecycle:     mdl.Lifecycle,
		Replacement:   mdl.Replacement,
		TextOnly:      mdl.TextOnly,
		Thinks:        mdl.Reasoning != nil,
		Scores:        modelScoresFor(mdl, providerName),
	}
}

// modelScoresFor is the row's scores: what the listing carried, or a lookup
// for a listing written before scores existed. The helper answers from the
// cached dataset without touching the network, so a stale cache still draws.
func modelScoresFor(mdl llm.ModelInfo, providerName string) *llm.ModelScores {
	if mdl.Scores != nil {
		return mdl.Scores
	}
	return llm.ModelScoresFor(llm.ProviderID(providerName), mdl.ID)
}

// modelInfoName resolves the name a model row shows. A free-tier model is
// named by the model underneath its ID rather than by the listing's marker;
// every other model keeps whatever name its provider gave it.
func modelInfoName(mdl llm.ModelInfo, providerName string, authMethod llm.AuthMethod) string {
	fallback := mdl.DisplayName
	if fallback == "" {
		fallback = mdl.Name
	}
	if fallback == "" {
		fallback = mdl.ID
	}
	return llm.ModelDisplayName(llm.ProviderID(providerName), authMethod, mdl.ID, fallback)
}

// ProviderSelector holds the state for the unified model & provider kit.
type ProviderSelector struct {
	active bool
	width  int
	height int
	store  *llm.Store

	// Tab
	activeTab providerTab

	// Data
	connectedProviders []providerProviderItem // providers with models (Models tab headers)
	allProviders       []providerProviderItem // all providers (Providers tab)
	allModels          []providerModelItem

	// Flattened visible-items list (rebuilt on state changes)
	visibleItems []providerListItem
	// modelColumns is the width of the name column, sized to whatever is
	// currently listed and recomputed with visibleItems. Every other column on
	// a model row has a fixed width.
	modelColumns int
	selectedIdx  int
	scrollOffset int
	maxVisible   int

	// Providers tab: expanded provider
	expandedProviderIdx int // index into allProviders; -1 = none

	// Inline credential form (one row per env var); see on_provider_auth.go
	credForm credentialForm

	// Inline custom provider form (baseURL / apiKey); see on_provider_custom.go
	customFormActive bool
	customFormInputs [2]textinput.Model
	customFormFocus  int
	customFormErr    string

	// Inline Ollama form (base URL only); see on_provider_ollama.go
	ollamaFormActive bool
	ollamaURLInput   textinput.Model
	ollamaFormErr    string

	// Inline confirm-remove prompt
	confirmRemoveActive  bool
	confirmRemoveEnvVar  string
	confirmRemoveItemIdx int // index into visibleItems for the pending remove

	// Models tab: search filter and the two flags that disambiguate keys
	// whose meaning depends on what the user is doing.
	searchQuery    string              // active filter text; "" means no filter
	filteredModels []providerModelItem // allModels narrowed to searchQuery

	// searchFocused routes Space: true while the search box has focus (the user
	// is typing a query) so Space inserts a literal space; false while
	// navigating the list so Space marks the highlighted model instead.
	searchFocused bool

	// modelMarked routes Enter: true once the user has explicitly marked a
	// model with Space, so Enter confirms that mark regardless of cursor; false
	// until then, so Enter acts on the highlighted row. (The active model is
	// rendered [*] on open, but that display state is not a mark.)
	modelMarked bool

	// Provider connection result (shown inline)
	lastConnectResult  string
	lastConnectAuthIdx int // item index that triggered the connection
	lastConnectSuccess bool

	// loginPrompt holds the instruction for an interactive sign-in still in
	// flight — the page to open and, for device flows, the code to type there.
	// It replaces the hint line until the sign-in resolves; a zero value means
	// nothing is pending.
	loginPrompt llm.LoginPrompt

	// cancelLogin stops an interactive sign-in when the user closes the
	// selector. A device flow otherwise keeps polling GitHub every few seconds
	// for its full 15-minute lifetime with nobody left to read the result.
	cancelLogin context.CancelFunc

	// spinnerTick advances on each providerConnectingTickMsg; it is the live
	// dot's frame while a connect/refresh is in flight.
	spinnerTick int

	// Roles tab: the injected settings surface, the snapshot it last read, the
	// last write error, and the role whose model is being picked. Assign mode
	// borrows the Models list, so a non-empty assignRole means the tab bar is
	// replaced by a banner naming that role.
	roleSource RoleSource
	roleInfo   ModelRoleInfo
	roleNotice string
	assignRole string

	// Assign mode runs in two steps: pick a model, then pick how hard it should
	// think for that role. assignRef holds the model chosen by step one while
	// step two lists the effort rungs — nothing is written until an effort is
	// chosen, so backing out costs nothing. "" means the model list is showing.
	assignRef     string
	assignEfforts []providerEffortItem
}

// NewProviderSelector creates a new provider selector ProviderSelector.
func NewProviderSelector() ProviderSelector {
	return ProviderSelector{
		active:              false,
		selectedIdx:         0,
		maxVisible:          20,
		expandedProviderIdx: -1,
	}
}

// IsActive returns whether the selector is active.
func (s *ProviderSelector) IsActive() bool {
	return s.active
}

// providerModelSelectedMsg is sent when a model is selected.
type providerModelSelectedMsg struct {
	ModelID      string
	ProviderName string
	AuthMethod   llm.AuthMethod
}

// providerConnectResultMsg is sent when inline connection completes.
type providerConnectResultMsg struct {
	AuthIdx    int
	Success    bool
	Message    string
	NewStatus  llm.Status
	Provider   llm.ProviderID
	AuthMethod llm.AuthMethod
	Models     []llm.ModelInfo
}

// providerLoginPromptMsg carries the instruction an in-flight interactive
// sign-in needs the user to follow.
type providerLoginPromptMsg struct {
	AuthIdx int
	Prompt  llm.LoginPrompt
}

// providerModelsLoadedMsg is sent when async model loading completes.
type providerModelsLoadedMsg struct {
	Models []providerModelItem
}

// providerRoleAssignedMsg reports a role's new model reference and reasoning
// effort, both written at user scope by the tab's injected setters. An empty
// Effort means the model's own default, which also clears an older binding.
type providerRoleAssignedMsg struct {
	Role   string
	Ref    string
	Effort string
}

// providerRoleClearedMsg reports that a role returned to the session model.
type providerRoleClearedMsg struct {
	Role string
}

// providerStatusDisplayInfo contains display information for a provider status.
type providerStatusDisplayInfo struct {
	icon  string
	style lipgloss.Style
	desc  string
}

// providerStatusDisplayMap maps provider status to display information.
var providerStatusDisplayMap = map[llm.Status]providerStatusDisplayInfo{
	llm.StatusConnected: {"●", kit.SelectorStatusConnected(), ""},
	llm.StatusAvailable: {"○", kit.SelectorStatusReady(), "(available)"},
}

// providerGetStatusDisplay returns the icon, style, and description for a provider status.
func providerGetStatusDisplay(status llm.Status) (icon string, style lipgloss.Style, desc string) {
	if info, ok := providerStatusDisplayMap[status]; ok {
		return info.icon, info.style, info.desc
	}
	return "◌", kit.SelectorStatusNone(), ""
}

func providerBestAuthMethodStatus(methods []providerAuthMethodItem) llm.Status {
	for _, m := range methods {
		if m.Status == llm.StatusConnected {
			return llm.StatusConnected
		}
	}
	for _, m := range methods {
		if m.Status == llm.StatusAvailable {
			return llm.StatusAvailable
		}
	}
	return llm.StatusNotConfigured
}

// ProviderState holds provider UI state for the TUI model.
// Domain state (LLM, Store, CurrentModel, tokens, thinking) lives
// on the parent app model, not here.
type ProviderState struct {
	Selector      ProviderSelector
	StatusMessage string // Temporary status shown in status bar
	statusToken   int64
}

// SetStatusMessage sets the temporary status message displayed in the status bar.
func (s *ProviderState) SetStatusMessage(msg string) int64 {
	s.statusToken++
	s.StatusMessage = msg
	return s.statusToken
}

// UpdateProvider routes provider connection and selection messages.
func UpdateProvider(deps OverlayDeps, state *ProviderState, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case providerConnectingTickMsg:
		// Keep ticking the spinner while connect/refresh is in flight; stop once
		// the matching providerConnectResultMsg lands and IsConnecting goes false.
		if state.Selector.IsConnecting() {
			state.Selector.AdvanceSpinner()
			return providerConnectingTickCmd(), true
		}
		return nil, true
	case providerLoginPromptMsg:
		state.Selector.HandleLoginPrompt(msg)
		return nil, true
	case providerConnectResultMsg:
		cmd := state.Selector.HandleConnectResult(msg)
		if msg.Success && deps.ReloadModelStore != nil {
			deps.ReloadModelStore()
		}
		return cmd, true
	case providerModelSelectedMsg:
		return handleProviderModelSelected(deps, state, msg), true
	case providerModelsLoadedMsg:
		state.Selector.HandleModelsLoaded(msg)
		if deps.ReloadModelStore != nil {
			deps.ReloadModelStore()
		}
		return nil, true
	case providerRoleAssignedMsg:
		state.Selector.handleRoleWrite(msg.Role, msg.Ref, msg.Effort, true)
		return nil, true
	case providerRoleClearedMsg:
		state.Selector.handleRoleWrite(msg.Role, "", "", false)
		return nil, true
	case kit.StatusExpiredMsg:
		if msg.Token == state.statusToken {
			state.StatusMessage = ""
		}
		return nil, true
	}
	return nil, false
}

func handleProviderModelSelected(deps OverlayDeps, state *ProviderState, msg providerModelSelectedMsg) tea.Cmd {
	p, err := llm.GetProvider(context.Background(), llm.ProviderID(msg.ProviderName), msg.AuthMethod)
	if err == nil {
		err = deps.SwitchProvider(p, func() error {
			if _, err := state.Selector.SetModel(msg.ModelID, msg.ProviderName, msg.AuthMethod); err != nil {
				return err
			}
			deps.SetCurrentModel(&llm.CurrentModelInfo{
				ModelID:    msg.ModelID,
				Provider:   llm.ProviderID(msg.ProviderName),
				AuthMethod: msg.AuthMethod,
			})
			return nil
		})
	}
	if err != nil {
		deps.Conv.Append(core.ChatMessage{Role: core.ChatNotice, Content: "Error: " + err.Error()})
	}
	return tea.Batch(deps.CommitMessages()...)
}
