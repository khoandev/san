package input

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// rolesSelector builds a selector on the Roles tab with an injected snapshot.
// The model list is present because assign mode borrows it.
func rolesSelector(t *testing.T, info ModelRoleInfo, src *fakeRoleSource) *ProviderSelector {
	t.Helper()
	m := NewProviderSelector()
	m.active = true
	m.width, m.height = 100, 40
	m.allModels = []providerModelItem{
		{ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1", ProviderName: "anthropic"},
		{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", ProviderName: "anthropic"},
	}
	m.connectedProviders = []providerProviderItem{{Provider: "anthropic", DisplayName: "Anthropic"}}
	if src == nil {
		src = &fakeRoleSource{info: info}
	}
	m.SetRoleSource(src.source())
	m.refreshRoleInfo()
	m.activeTab = providerTabRoles
	m.rebuildVisibleItems()
	return &m
}

// fakeRoleSource records what the tab writes, so a test can assert both the
// message and the write it turns into.
type fakeRoleSource struct {
	info      ModelRoleInfo
	err       error
	errEffort error
	set       []string // "role=ref" per call
	effort    []string // "role=effort" per call; "role=" clears the binding
	clear     []string // role per call
	reload    int
}

func (f *fakeRoleSource) source() RoleSource {
	return RoleSource{
		Info: func() ModelRoleInfo { return f.info },
		Set: func(role, ref string) error {
			if f.err != nil {
				return f.err
			}
			f.set = append(f.set, role+"="+ref)
			if f.info.Roles == nil {
				f.info.Roles = map[string]string{}
			}
			f.info.Roles[role] = ref
			f.reload++
			return nil
		},
		SetEffort: func(role, effort string) error {
			if f.err != nil {
				return f.err
			}
			if f.errEffort != nil {
				return f.errEffort
			}
			f.effort = append(f.effort, role+"="+effort)
			if f.info.Efforts == nil {
				f.info.Efforts = map[string]string{}
			}
			if effort == "" {
				delete(f.info.Efforts, role)
			} else {
				f.info.Efforts[role] = effort
			}
			f.reload++
			return nil
		},
		Clear: func(role string) error {
			if f.err != nil {
				return f.err
			}
			f.clear = append(f.clear, role)
			delete(f.info.Roles, role)
			delete(f.info.Efforts, role)
			f.reload++
			return nil
		},
	}
}

func roleRowNamed(m *ProviderSelector, role string) providerListItem {
	for _, item := range m.visibleItems {
		if item.Kind == providerItemRole && item.Role != nil && item.Role.Role == role {
			return item
		}
	}
	return providerListItem{}
}

// TestRoleRowsCoverCatalogAndCustomRoles pins what the tab lists: the built-in
// catalog in display order, then any role a hand-edited settings file named that
// the catalog does not describe.
func TestRoleRowsCoverCatalogAndCustomRoles(t *testing.T) {
	rows := roleRows(ModelRoleInfo{
		Roles:   map[string]string{"deep": "anthropic/claude-opus-4-1", "review-x": "opus"},
		Project: map[string]string{"deep": "anthropic/claude-opus-4-1"},
		Session: "claude-sonnet-4-6",
	})

	catalog := llm.RoleCatalog()
	if len(rows) != len(catalog)+1 {
		t.Fatalf("rows = %d, want %d catalog roles plus one custom", len(rows), len(catalog))
	}
	for i, spec := range catalog {
		if rows[i].Role != string(spec.Name) {
			t.Fatalf("row %d = %q, want catalog order %q", i, rows[i].Role, spec.Name)
		}
		if rows[i].Consumer != spec.Consumer {
			t.Errorf("%s consumer = %q, want %q", rows[i].Role, rows[i].Consumer, spec.Consumer)
		}
	}
	if last := rows[len(rows)-1]; last.Role != "review-x" || last.Ref != "opus" {
		t.Fatalf("custom row = %+v, want the settings-only role last", last)
	}

	byRole := map[string]providerRoleItem{}
	for _, row := range rows {
		byRole[row.Role] = row
	}
	if !byRole["deep"].ProjectOverride {
		t.Error("deep should be marked as a project assignment")
	}
	if byRole["fast"].ProjectOverride {
		t.Error("fast has no project assignment")
	}
	if byRole["fast"].Ref != "" {
		t.Errorf("unassigned fast ref = %q, want empty", byRole["fast"].Ref)
	}
	if !byRole["default"].IsSession {
		t.Error("the default row must be the session-model row")
	}
}

// TestRolesTabShowsSessionModelWhenUnassigned covers the reading of a row: an
// unassigned role still runs, on the session's model, so the tab says which.
func TestRolesTabShowsSessionModelWhenUnassigned(t *testing.T) {
	m := rolesSelector(t, ModelRoleInfo{Session: "claude-sonnet-4-6"}, nil)

	rows := 0
	for _, item := range m.visibleItems {
		if item.Kind == providerItemRole {
			rows++
		}
	}
	if rows != len(llm.RoleCatalog()) {
		t.Fatalf("role rows = %d, want %d", rows, len(llm.RoleCatalog()))
	}

	render := func(role string) string {
		return xansi.Strip(m.renderRoleRow(roleRowNamed(m, role), false))
	}
	if got := render("fast"); !strings.Contains(got, "(session model: claude-sonnet-4-6)") {
		t.Errorf("unassigned row = %q, want the session model named", got)
	}

	// A binding written under a retired name fills the rung it folded onto, so
	// the fast row shows the model the work actually runs on.
	assigned := rolesSelector(t, ModelRoleInfo{
		Roles:   map[string]string{"smol": "anthropic/claude-haiku-4-5"},
		Session: "claude-sonnet-4-6",
	}, nil)
	if got := xansi.Strip(assigned.renderRoleRow(roleRowNamed(assigned, "fast"), false)); !strings.Contains(got, "anthropic/claude-haiku-4-5") {
		t.Errorf("assigned row = %q, want the binding shown", got)
	}

	project := rolesSelector(t, ModelRoleInfo{
		Roles:   map[string]string{"slow": "anthropic/claude-opus-4-1"},
		Project: map[string]string{"slow": "anthropic/claude-opus-4-1"},
		Session: "claude-sonnet-4-6",
	}, nil)
	if got := xansi.Strip(project.renderRoleRow(roleRowNamed(project, "deep"), false)); !strings.Contains(got, "project") {
		t.Errorf("project-assigned row = %q, want the project marker", got)
	}
}

// TestEnterOnRoleRowStartsAssignMode covers the assign flow up to the pick: the
// borrowed list is the model list, with a banner naming the role.
func TestEnterOnRoleRowStartsAssignMode(t *testing.T) {
	m := rolesSelector(t, ModelRoleInfo{Session: "claude-sonnet-4-6"}, nil)

	m.selectedIdx = indexOfRole(m, "deep")
	cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter on a role row must not emit a message yet")
	}
	if m.assignRole != "deep" {
		t.Fatalf("assignRole = %q, want deep", m.assignRole)
	}
	models := 0
	for _, item := range m.visibleItems {
		if item.Kind == providerItemModel {
			models++
		}
	}
	if models != len(m.allModels) {
		t.Fatalf("assign mode lists %d models, want %d", models, len(m.allModels))
	}

	view := xansi.Strip(m.Render())
	if !strings.Contains(view, "Assign deep") {
		t.Errorf("assign mode has no banner:\n%s", view)
	}
	if strings.Contains(view, "Providers") {
		t.Errorf("assign mode still shows the tab bar:\n%s", view)
	}
	if hints := xansi.Strip(m.renderHints()); !strings.Contains(hints, "Assign") && !strings.Contains(hints, "assign") {
		t.Errorf("assign footer does not explain the keys: %q", hints)
	}
}

// TestAssigningModelWritesTheRole covers the pick: choosing a model in assign
// mode reports the role and its new vendor-qualified reference, and the tab
// returns to Roles.
func TestAssigningModelWritesTheRole(t *testing.T) {
	src := &fakeRoleSource{info: ModelRoleInfo{Session: "claude-sonnet-4-6"}}
	m := rolesSelector(t, src.info, src)

	m.selectedIdx = indexOfRole(m, "deep")
	m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})

	m.selectedIdx = indexOfModel(m, "claude-opus-4-1")
	cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("choosing a model in assign mode emitted nothing")
	}
	msg, ok := cmd().(providerRoleAssignedMsg)
	if !ok {
		t.Fatalf("message = %T, want providerRoleAssignedMsg", cmd())
	}
	if msg.Role != "deep" || msg.Ref != "anthropic/claude-opus-4-1" || msg.Effort != "" {
		t.Fatalf("assigned %+v, want deep -> anthropic/claude-opus-4-1 at the model's default", msg)
	}
	if m.assignRole != "" || m.activeTab != providerTabRoles {
		t.Fatalf("after the pick: assignRole=%q tab=%v, want Roles", m.assignRole, m.activeTab)
	}

	state := &ProviderState{Selector: *m}
	UpdateProvider(OverlayDeps{}, state, msg)
	if len(src.set) != 1 || src.set[0] != "deep=anthropic/claude-opus-4-1" {
		t.Fatalf("writes = %v, want one write for deep", src.set)
	}
	// A model that does not reason has no effort step, and the empty effort is
	// still written so a binding left on the previous model cannot survive.
	if len(src.effort) != 1 || src.effort[0] != "deep=" {
		t.Fatalf("effort writes = %v, want one clear for deep", src.effort)
	}
	if state.Selector.roleNotice != "" {
		t.Fatalf("unexpected notice: %q", state.Selector.roleNotice)
	}
	if ref := roleRowNamed(&state.Selector, "deep").Role.Ref; ref != "anthropic/claude-opus-4-1" {
		t.Fatalf("row still reads %q after the write", ref)
	}
}

// ladderProvider is a registered provider double that advertises a reasoning
// ladder, so the second assign step has rungs to offer. It lists no models: the
// rows under test are injected, not fetched.
type ladderProvider struct {
	staticListProvider
	efforts []string
	def     string
}

func (p *ladderProvider) ThinkingEfforts(string) []string     { return p.efforts }
func (p *ladderProvider) DefaultThinkingEffort(string) string { return p.def }

// registerLadderProvider registers a provider whose models advertise
// low/medium/high, the ladder an assignment's second step must list.
func registerLadderProvider(t *testing.T) llm.ProviderID {
	t.Helper()
	providerName := llm.ProviderID("effort-test")
	llm.Register(llm.Meta{Provider: providerName, AuthMethod: llm.AuthAPIKey}, func(context.Context) (llm.Provider, error) {
		return &ladderProvider{
			staticListProvider: staticListProvider{name: string(providerName) + ":api_key"},
			efforts:            []string{"low", "medium", "high"},
			def:                "medium",
		}, nil
	})
	t.Cleanup(func() { llm.Unregister(providerName, llm.AuthAPIKey) })
	return providerName
}

// reasoningSelector builds a Roles-tab selector whose borrowed model list holds
// one model that reasons, and walks it to the effort step for "deep".
func reasoningSelector(t *testing.T, providerName llm.ProviderID, src *fakeRoleSource) *ProviderSelector {
	t.Helper()
	m := rolesSelector(t, src.info, src)
	m.allModels = []providerModelItem{{
		ID: "thinker", DisplayName: "Thinker", ProviderName: string(providerName),
		AuthMethod: llm.AuthAPIKey, Thinks: true,
	}}
	m.connectedProviders = []providerProviderItem{{Provider: providerName, DisplayName: "Effort Test"}}

	m.selectedIdx = indexOfRole(m, "deep")
	m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.selectedIdx = indexOfModel(m, "thinker")
	m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	return m
}

// selectEffortRow points the cursor at one label of the second assign step.
func selectEffortRow(t *testing.T, m *ProviderSelector, label string) {
	t.Helper()
	for i, item := range m.visibleItems {
		if item.Kind == providerItemEffort && item.Effort != nil && item.Effort.Label == label {
			m.selectedIdx = i
			return
		}
	}
	t.Fatalf("no effort row %q in the assign list", label)
}

// TestAssigningReasoningModelAsksForEffort covers the two-step assignment: a
// model that reasons is not written until its depth is picked, and the second
// step lists the model's own default before the rungs.
func TestAssigningReasoningModelAsksForEffort(t *testing.T) {
	providerName := registerLadderProvider(t)
	src := &fakeRoleSource{info: ModelRoleInfo{Session: "claude-sonnet-4-6"}}
	m := reasoningSelector(t, providerName, src)

	if !m.pickingEffort() {
		t.Fatalf("assign state = role %q ref %q, want the effort step", m.assignRole, m.assignRef)
	}
	if want := string(providerName) + "/thinker"; m.assignRef != want {
		t.Fatalf("assignRef = %q, want %q", m.assignRef, want)
	}
	if len(src.set) != 0 || len(src.effort) != 0 {
		t.Fatalf("the model was written before the effort: set=%v effort=%v", src.set, src.effort)
	}

	var got []string
	for _, item := range m.visibleItems {
		if item.Kind == providerItemEffort && item.Effort != nil {
			got = append(got, item.Effort.Display)
		}
	}
	want := []string{"model default", "low", "medium", "high"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("effort rows = %v, want %v", got, want)
	}
	if view := xansi.Strip(m.Render()); !strings.Contains(view, "model default") || !strings.Contains(view, "high") {
		t.Errorf("the effort list is not rendered:\n%s", view)
	}

	selectEffortRow(t, m, "high")
	cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("picking an effort emitted nothing")
	}
	msg, ok := cmd().(providerRoleAssignedMsg)
	if !ok || msg.Role != "deep" || msg.Ref != string(providerName)+"/thinker" || msg.Effort != "high" {
		t.Fatalf("message = %#v, want deep bound to the thinking model at high", cmd())
	}

	state := &ProviderState{Selector: *m}
	UpdateProvider(OverlayDeps{}, state, msg)
	if len(src.set) != 1 || src.set[0] != "deep="+string(providerName)+"/thinker" {
		t.Fatalf("model writes = %v, want the chosen model", src.set)
	}
	if len(src.effort) != 1 || src.effort[0] != "deep=high" {
		t.Fatalf("effort writes = %v, want deep=high", src.effort)
	}
	if state.Selector.roleNotice != "" {
		t.Fatalf("unexpected notice: %q", state.Selector.roleNotice)
	}
	row := roleRowNamed(&state.Selector, "deep")
	if row.Role.Ref != string(providerName)+"/thinker" || row.Role.Effort != "high" {
		t.Fatalf("row reads %+v after the write", row.Role)
	}
}

// TestEscInEffortStepOnlyBacksOut keeps the second step free to abandon: Esc
// returns to the model list with nothing written, and a second Esc cancels the
// assignment outright.
func TestEscInEffortStepOnlyBacksOut(t *testing.T) {
	providerName := registerLadderProvider(t)
	src := &fakeRoleSource{info: ModelRoleInfo{Session: "claude-sonnet-4-6"}}
	m := reasoningSelector(t, providerName, src)

	if cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		t.Fatalf("Esc in the effort step emitted %T", cmd())
	}
	if m.assignRef != "" || !m.assigning() {
		t.Fatalf("after Esc: ref=%q role=%q, want the model list again", m.assignRef, m.assignRole)
	}
	models := 0
	for _, item := range m.visibleItems {
		if item.Kind == providerItemModel {
			models++
		}
	}
	if models != 1 {
		t.Fatalf("model rows after Esc = %d, want the borrowed list back", models)
	}

	if cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		t.Fatalf("second Esc emitted %T", cmd())
	}
	if m.assignRole != "" || m.activeTab != providerTabRoles {
		t.Fatalf("after the second Esc: role=%q tab=%v, want Roles", m.assignRole, m.activeTab)
	}
	if len(src.set) != 0 || len(src.effort) != 0 || len(src.clear) != 0 {
		t.Fatalf("Esc wrote settings: set=%v effort=%v clear=%v", src.set, src.effort, src.clear)
	}
}

// TestRoleRowsShowBoundEffort keeps the effort visible next to the model it
// qualifies, and shows the session's own effort on the default row.
func TestRoleRowsShowBoundEffort(t *testing.T) {
	m := rolesSelector(t, ModelRoleInfo{
		Roles:         map[string]string{"deep": "anthropic/claude-opus-4-1"},
		Efforts:       map[string]string{"deep": "high"},
		Session:       "claude-sonnet-4-6",
		SessionEffort: "low",
	}, nil)

	deep := xansi.Strip(m.renderRoleRow(roleRowNamed(m, "deep"), false))
	if !strings.Contains(deep, "high") || !strings.Contains(deep, "anthropic/claude-opus-4-1") {
		t.Errorf("deep row = %q, want its model and effort", deep)
	}
	def := xansi.Strip(m.renderRoleRow(roleRowNamed(m, "default"), false))
	if !strings.Contains(def, "low") {
		t.Errorf("default row = %q, want the session effort", def)
	}
}

// TestEscCancelsAssignWithoutWriting keeps Esc honest: it leaves assign mode and
// writes nothing, which is the difference between backing out and choosing.
func TestEscCancelsAssignWithoutWriting(t *testing.T) {
	src := &fakeRoleSource{info: ModelRoleInfo{Session: "claude-sonnet-4-6"}}
	m := rolesSelector(t, src.info, src)

	m.selectedIdx = indexOfRole(m, "deep")
	m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.assignRole != "deep" {
		t.Fatal("assign mode did not start")
	}
	if cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		t.Fatalf("Esc emitted %T, want nothing", cmd())
	}
	if m.assignRole != "" || m.activeTab != providerTabRoles {
		t.Fatalf("after Esc: assignRole=%q tab=%v, want the Roles tab", m.assignRole, m.activeTab)
	}
	if len(src.set) != 0 || len(src.effort) != 0 || len(src.clear) != 0 {
		t.Fatalf("Esc wrote settings: set=%v effort=%v clear=%v", src.set, src.effort, src.clear)
	}
	if view := xansi.Strip(m.Render()); !strings.Contains(view, "Roles") {
		t.Errorf("after Esc the tab bar is gone:\n%s", view)
	}
}

// TestCtrlDClearsRole covers unbinding: the highlighted role returns to the
// session model, and the default row — which has no binding — is left alone.
func TestCtrlDClearsRole(t *testing.T) {
	src := &fakeRoleSource{info: ModelRoleInfo{
		Roles:   map[string]string{"deep": "anthropic/claude-opus-4-1"},
		Efforts: map[string]string{"deep": "high"},
		Session: "claude-sonnet-4-6",
	}}
	m := rolesSelector(t, src.info, src)

	m.selectedIdx = indexOfRole(m, "deep")
	cmd := m.HandleKeypress(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+D on a bound role emitted nothing")
	}
	msg, ok := cmd().(providerRoleClearedMsg)
	if !ok || msg.Role != "deep" {
		t.Fatalf("message = %#v, want clearing deep", cmd())
	}
	state := &ProviderState{Selector: *m}
	UpdateProvider(OverlayDeps{}, state, msg)
	if len(src.clear) != 1 || src.clear[0] != "deep" {
		t.Fatalf("clears = %v, want deep", src.clear)
	}
	// One clear drops the model and the effort together: the next binding must
	// not inherit the previous model's depth.
	if row := roleRowNamed(&state.Selector, "deep"); row.Role.Ref != "" || row.Role.Effort != "" {
		t.Fatalf("row still reads %+v after clearing", row.Role)
	}

	session := rolesSelector(t, ModelRoleInfo{Session: "claude-sonnet-4-6"}, src)
	session.selectedIdx = indexOfRole(session, "default")
	if cmd := session.HandleKeypress(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatalf("Ctrl+D on the session row emitted %T", cmd())
	}
}

// TestRoleWriteFailureStaysOnScreen keeps a failed write visible where the user
// made it, instead of closing the overlay on a silent no-op.
func TestRoleWriteFailureStaysOnScreen(t *testing.T) {
	src := &fakeRoleSource{info: ModelRoleInfo{Session: "claude-sonnet-4-6"}, err: errors.New("disk is read-only")}
	m := rolesSelector(t, src.info, src)
	m.selectedIdx = indexOfRole(m, "deep")
	m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.selectedIdx = indexOfModel(m, "claude-opus-4-1")

	cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("choosing a model emitted nothing")
	}
	state := &ProviderState{Selector: *m}
	UpdateProvider(OverlayDeps{}, state, cmd())

	if state.Selector.roleNotice == "" {
		t.Fatal("a failed write left no notice")
	}
	if view := xansi.Strip(state.Selector.Render()); !strings.Contains(view, "disk is read-only") {
		t.Errorf("the notice is not rendered:\n%s", view)
	}
}

// TestDefaultRoleRowOpensModelsTab keeps the one row that is not an assignment:
// the default role IS the session model, so Enter goes to the model picker.
func TestDefaultRoleRowOpensModelsTab(t *testing.T) {
	m := rolesSelector(t, ModelRoleInfo{Session: "claude-sonnet-4-6"}, nil)

	m.selectedIdx = indexOfRole(m, "default")
	if cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("Enter on the default row emitted %T", cmd())
	}
	if m.activeTab != providerTabModels {
		t.Fatalf("tab = %v, want Models", m.activeTab)
	}
	if m.assignRole != "" {
		t.Fatalf("the default row started assign mode for %q", m.assignRole)
	}
}

// TestRolesTabSearchFilters keeps "Type to filter roles..." true: typing narrows
// the rows to the matching role names.
func TestRolesTabSearchFilters(t *testing.T) {
	m := rolesSelector(t, ModelRoleInfo{Session: "claude-sonnet-4-6"}, nil)

	m.appendModelSearch("dee")
	rows := 0
	for _, item := range m.visibleItems {
		if item.Kind == providerItemRole {
			rows++
			if item.Role.Role != "deep" {
				t.Errorf("filter matched %q, want only deep", item.Role.Role)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("filtered rows = %d, want 1", rows)
	}
}

// TestRoleSourceWritesUserScopeAndReloads covers the live surface behind the
// tab: an assignment lands in the user's own settings file (a personal routing
// choice, not the repository's), a project assignment shows up as the project's,
// and the worker is rebuilt so the new model applies without a restart.
func TestRoleSourceWritesUserScopeAndReloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()

	writeSettings := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSettings(filepath.Join(confdir.Dir(project), "settings.json"),
		`{"modelRoles":{"plan":"anthropic/claude-opus-4-1"},"modelRoleEfforts":{"plan":"high"}}`)
	// An untrusted workspace keeps only restrictive project settings, so the
	// project's role bindings apply only once the folder is trusted.
	if err := setting.TrustWorkspace(project); err != nil {
		t.Fatal(err)
	}

	type roleFile struct {
		ModelRoles       map[string]string `json:"modelRoles"`
		ModelRoleEfforts map[string]string `json:"modelRoleEfforts"`
	}
	readFile := func(path string) roleFile {
		t.Helper()
		var parsed roleFile
		data, err := os.ReadFile(path)
		if err != nil {
			return parsed
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		return parsed
	}
	readRoles := func(path string) map[string]string { return readFile(path).ModelRoles }
	readEfforts := func(path string) map[string]string { return readFile(path).ModelRoleEfforts }

	settings := setting.New(setting.NewData())
	if err := settings.Reload(project); err != nil {
		t.Fatal(err)
	}
	reconfigured := 0
	conn := &llm.Conn{}
	conn.SetCurrentModel(&llm.CurrentModelInfo{ModelID: "claude-sonnet-4-6", Provider: llm.Anthropic})
	c := NewSlashCommandController(SlashCommandEnv{
		Setting:              settings,
		Cwd:                  project,
		LLM:                  conn,
		ReconfigureAgentTool: func() { reconfigured++ },
	})
	src := c.roleSource()

	info := src.Info()
	if info.Session != "claude-sonnet-4-6" {
		t.Errorf("session model = %q, want the connection's model", info.Session)
	}
	// The project file binds plan, which loads as the deep rung.
	if info.Roles["deep"] != "anthropic/claude-opus-4-1" || info.Project["deep"] != "anthropic/claude-opus-4-1" {
		t.Fatalf("project role not read: roles=%v project=%v", info.Roles, info.Project)
	}
	if info.ProjectEfforts["deep"] != "high" {
		t.Fatalf("project efforts = %v, want the deep rung at high", info.ProjectEfforts)
	}

	// The assignment is written at user scope under its rung, so the project's
	// deep binding does not hide it.
	if err := src.Set("fast", "anthropic/claude-haiku-4-5"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	userFile := filepath.Join(confdir.Dir(home), "settings.json")
	if roles := readRoles(userFile); roles["fast"] != "anthropic/claude-haiku-4-5" {
		t.Fatalf("user settings = %v, want the fast assignment", roles)
	}
	if roles := readRoles(filepath.Join(confdir.Dir(project), "settings.json")); len(roles) != 1 || roles["plan"] == "" {
		t.Fatalf("the project file was rewritten: %v", roles)
	}
	if reconfigured != 1 {
		t.Errorf("worker rebuilt %d times after a write, want 1", reconfigured)
	}
	if got := src.Info().Roles["fast"]; got != "anthropic/claude-haiku-4-5" {
		t.Errorf("reloaded fast = %q, want the assignment", got)
	}

	// The effort is a second write at the same scope, and the empty effort is
	// how "model default" is stored.
	if err := src.SetEffort("fast", "high"); err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	if efforts := readEfforts(userFile); efforts["fast"] != "high" {
		t.Fatalf("user efforts = %v, want fast=high", efforts)
	}
	if got := src.Info().Efforts["fast"]; got != "high" {
		t.Errorf("reloaded fast effort = %q, want high", got)
	}
	if reconfigured != 2 {
		t.Errorf("worker rebuilt %d times after the effort write, want 2", reconfigured)
	}
	if err := src.SetEffort("fast", ""); err != nil {
		t.Fatalf("SetEffort(clear): %v", err)
	}
	if efforts := readEfforts(userFile); efforts["fast"] != "" {
		t.Fatalf("user efforts still bind fast: %v", efforts)
	}
	if got := src.Info().Efforts["fast"]; got != "" {
		t.Errorf("fast effort still reads %q after clearing", got)
	}

	if err := src.Clear("fast"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if roles := readRoles(userFile); roles["fast"] != "" {
		t.Fatalf("user settings still bind fast: %v", roles)
	}
	if got := src.Info().Roles["fast"]; got != "" {
		t.Errorf("fast still resolves to %q after clearing", got)
	}
	if roles := readRoles(userFile); len(roles) != 0 {
		t.Fatalf("a retired key survived the writes: %v", roles)
	}
	if reconfigured != 4 {
		t.Errorf("worker rebuilt %d times, want 4", reconfigured)
	}
}

func indexOfRole(m *ProviderSelector, role string) int {
	for i, item := range m.visibleItems {
		if item.Kind == providerItemRole && item.Role != nil && item.Role.Role == role {
			return i
		}
	}
	return 0
}

func indexOfModel(m *ProviderSelector, id string) int {
	for i, item := range m.visibleItems {
		if item.Kind == providerItemModel && item.Model != nil && item.Model.ID == id {
			return i
		}
	}
	return 0
}
