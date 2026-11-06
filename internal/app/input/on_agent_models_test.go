package input

import (
	"context"
	"slices"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/subagent"
)

// anyVendorResolver resolves every vendor, the way a session with connected
// providers does. Without it a "vendor/model" binding is the session model, so a
// nil resolver would test the fallback instead of the assignment.
type anyVendorResolver struct{ provider llm.Provider }

func (r anyVendorResolver) Resolve(context.Context, llm.ProviderID) (llm.Provider, error) {
	return r.provider, nil
}

// fakeAgentModels resolves through the real role resolver over a fixed binding
// map, so the list is tested against the rule the executor follows rather than
// a stub that agrees with itself.
func fakeAgentModels(assigned map[string]string, session string) AgentModelSource {
	resolver := llm.NewRoleResolver(anyVendorResolver{provider: &connectFailProvider{}})
	roles := llm.Bindings(assigned)
	return AgentModelSource{
		Model: func(ref string) (string, bool) {
			if role, isRole := llm.ParseRoleAlias(ref); isRole {
				target := resolver.Resolve(context.Background(), roles, role, nil, session)
				return target.ModelID, target.Inherited
			}
			target := resolver.ResolveRef(context.Background(), ref, nil, session)
			return target.ModelID, target.Inherited
		},
		Worker: func() string { return assigned["task"] },
	}
}

// bundledAgentSelector loads the embedded definitions the way a fresh install
// does and opens the selector on them.
func bundledAgentSelector(t *testing.T, src AgentModelSource, width int) (*AgentSelector, *subagent.Registry) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	registry, err := subagent.Load(cwd, nil)
	if err != nil {
		t.Fatalf("load agents: %v", err)
	}
	selector := NewAgentSelector(registry)
	selector.SetModelSource(src)
	if err := selector.EnterSelect(width, 40); err != nil {
		t.Fatalf("enter: %v", err)
	}
	return &selector, registry
}

func agentItemNamed(selector *AgentSelector, name string) (agentItem, bool) {
	for _, item := range selector.list.filtered {
		if strings.EqualFold(item.Name, name) {
			return item, true
		}
	}
	return agentItem{}, false
}

// TestAgentListShowsBundledDefinitions covers what the Built-in tab is: the
// default worker's preview row first, then every embedded definition once, all
// of them toggleable and none of them leaking into the file-backed tabs.
func TestAgentListShowsBundledDefinitions(t *testing.T) {
	selector, registry := bundledAgentSelector(t, fakeAgentModels(nil, "claude-sonnet-4-6"), 120)

	if selector.list.activeTab != int(agentTabBuiltin) {
		t.Fatalf("initial tab = %d, want Built-in", selector.list.activeTab)
	}

	got := len(selector.list.filtered)
	if got != 6 {
		t.Fatalf("built-in rows = %d, want 1 preview + 5 bundled", got)
	}
	if first := selector.list.filtered[0]; !first.BuiltinPreview || first.Name != "Agent" {
		t.Fatalf("first row = %+v, want the default worker's preview", first)
	}

	var names []string
	for _, item := range selector.list.filtered[1:] {
		if item.BuiltinPreview {
			t.Fatalf("second preview row: %+v", item)
		}
		if item.Source != "built-in" {
			t.Errorf("%s source = %q, want built-in", item.Name, item.Source)
		}
		names = append(names, item.Name)
	}
	want := []string{"designer", "explore", "librarian", "reviewer", "task"}
	if !slices.Equal(names, want) {
		t.Fatalf("bundled rows = %v, want %v (stable order, no duplicates)", names, want)
	}
	for _, name := range names {
		if item, _ := agentItemNamed(selector, name); item.Model == "" {
			t.Errorf("%s has no model reference", name)
		}
	}
	if selector.list.tabCount(int(agentTabBuiltin)) != 6 {
		t.Fatalf("built-in tab count = %d, want 6", selector.list.tabCount(int(agentTabBuiltin)))
	}

	// Where the definition came from is stated on the row: a bundled agent has
	// no file to point at, so its source is the badge, not a path.
	view := xansi.Strip(selector.Render())
	if !strings.Contains(view, "[Built-in]") {
		t.Errorf("bundled rows are not marked as built-in:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := xansi.StringWidth(line); w > 120 {
			t.Fatalf("row overflows the panel (%d cols): %q", w, line)
		}
	}

	// Toggling a bundled agent is allowed and reaches the user store, so a
	// built-in can still be switched off.
	selector.list.nav.Selected = 1
	cmd := selector.Toggle()
	if cmd == nil {
		t.Fatal("toggling a bundled agent produced no command")
	}
	msg, ok := cmd().(AgentToggleMsg)
	if !ok || msg.AgentName != names[0] || msg.Enabled {
		t.Fatalf("toggle message = %+v, want %s disabled", msg, names[0])
	}
	if registry.IsEnabled(names[0]) {
		t.Errorf("%s is still enabled after the toggle", names[0])
	}

	// The other tabs hold file-backed definitions only, so a bundled agent is
	// never listed twice.
	for _, tab := range []agentTab{agentTabProject, agentTabUser} {
		selector.list.cycleTab(int(tab) - selector.list.activeTab)
		for _, item := range selector.list.filtered {
			if item.Source == "built-in" {
				t.Fatalf("%s leaked into the %v tab", item.Name, tab)
			}
		}
	}
}

// TestAgentRowsShowTheModelThatWillRun pins the reading of a role reference: an
// assigned role names its model, an unassigned one says the session model
// answers, and the detail line names which one that is.
func TestAgentRowsShowTheModelThatWillRun(t *testing.T) {
	src := fakeAgentModels(map[string]string{"slow": "anthropic/claude-opus-4-1"}, "claude-sonnet-4-6")
	selector, _ := bundledAgentSelector(t, src, 120)
	// The role column is what this test reads, so it must fit the panel at the
	// widths /agents is actually used at.
	for _, width := range []int{80, 120} {
		narrow, _ := bundledAgentSelector(t, src, width)
		for _, line := range strings.Split(xansi.Strip(narrow.Render()), "\n") {
			if w := xansi.StringWidth(line); w > width {
				t.Fatalf("row overflows at width %d (%d cols): %q", width, w, line)
			}
		}
	}

	reviewer, ok := agentItemNamed(selector, "reviewer")
	if !ok {
		t.Fatal("reviewer not listed")
	}
	if reviewer.Model != "@slow" {
		t.Errorf("reviewer model reference = %q, want the configured @slow", reviewer.Model)
	}
	if reviewer.EffectiveModel != "claude-opus-4-1" || reviewer.InheritedModel {
		t.Fatalf("reviewer resolves to %q inherited=%v, want claude-opus-4-1", reviewer.EffectiveModel, reviewer.InheritedModel)
	}

	view := xansi.Strip(selector.Render())
	if !strings.Contains(view, "@slow → claude-opus-4-1") {
		t.Errorf("assigned role row does not name its model:\n%s", view)
	}
	if strings.Contains(view, "@slow → session model") {
		t.Errorf("assigned role claims the session model:\n%s", view)
	}

	// explorer's "@smol" is unassigned in this map: the session model answers,
	// and the row says so instead of showing a model that will not run.
	explore, _ := agentItemNamed(selector, "explore")
	if !explore.InheritedModel || explore.EffectiveModel != "claude-sonnet-4-6" {
		t.Fatalf("unassigned role = %q inherited=%v, want the session model", explore.EffectiveModel, explore.InheritedModel)
	}
	selector.list.nav.Selected = 0
	for i, item := range selector.list.filtered {
		if item.Name == "explore" {
			selector.list.nav.Selected = i
		}
	}
	view = xansi.Strip(selector.Render())
	if !strings.Contains(view, "@smol → session model") {
		t.Errorf("unassigned role row does not say session model:\n%s", view)
	}
	if !strings.Contains(view, "session model: claude-sonnet-4-6") {
		t.Errorf("detail line does not name the session model:\n%s", view)
	}
}

// TestAgentWorkerRowNamesTheTaskModel covers the default worker: it follows the
// task role, so the preview row names whatever that role resolves to.
func TestAgentWorkerRowNamesTheTaskModel(t *testing.T) {
	src := fakeAgentModels(map[string]string{"task": "openai/gpt-5"}, "claude-sonnet-4-6")
	selector, _ := bundledAgentSelector(t, src, 120)

	worker := selector.list.filtered[0]
	if !worker.BuiltinPreview {
		t.Fatalf("first row = %+v, want the default worker", worker)
	}
	if worker.EffectiveModel != "gpt-5" || worker.InheritedModel {
		t.Fatalf("worker model = %q inherited=%v, want gpt-5 from the task role", worker.EffectiveModel, worker.InheritedModel)
	}
	view := xansi.Strip(selector.Render())
	if !strings.Contains(view, "model follows the task role") || !strings.Contains(view, "gpt-5") {
		t.Errorf("worker row does not name the model it will run:\n%s", view)
	}
	if cmd := selector.Toggle(); cmd != nil {
		t.Fatal("the default worker row must stay non-toggleable")
	}
}

// TestAgentRowsWithoutResolverKeepTheReference keeps the list honest when the
// app has not supplied a resolver: the reference is shown as written rather
// than invented.
func TestAgentRowsWithoutResolverKeepTheReference(t *testing.T) {
	selector, _ := bundledAgentSelector(t, AgentModelSource{}, 120)

	reviewer, ok := agentItemNamed(selector, "reviewer")
	if !ok {
		t.Fatal("reviewer not listed")
	}
	if reviewer.EffectiveModel != "" || reviewer.InheritedModel {
		t.Fatalf("reviewer model = %q, want nothing resolved", reviewer.EffectiveModel)
	}
	view := xansi.Strip(selector.Render())
	if !strings.Contains(view, "@slow") {
		t.Errorf("row lost its configured reference:\n%s", view)
	}
	if strings.Contains(view, "@slow →") || strings.Contains(view, "session model:") {
		t.Errorf("row claims a resolution it does not have:\n%s", view)
	}
}
