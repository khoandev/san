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
	return fakeAgentModelsWithEfforts(assigned, nil, session)
}

// fakeAgentModelsWithEfforts is the same list with modelRoleEfforts bound too,
// so a row can be checked for the depth it will actually ask for.
func fakeAgentModelsWithEfforts(assigned, efforts map[string]string, session string) AgentModelSource {
	resolver := llm.NewRoleResolver(anyVendorResolver{provider: &connectFailProvider{}})
	roles := llm.BindingsOf(assigned, efforts)
	return AgentModelSource{
		Model: func(ref string) (string, string, bool) {
			var target llm.RoleTarget
			if role, isRole := llm.ParseRoleAlias(ref); isRole {
				target = resolver.Resolve(context.Background(), roles, role, nil, session)
			} else {
				target = resolver.ResolveRef(context.Background(), ref, nil, session)
			}
			return target.ModelID, target.Effort, target.Inherited
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
	src := fakeAgentModels(map[string]string{"reviewer": "anthropic/claude-opus-4-1"}, "claude-sonnet-4-6")
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
	if reviewer.Model != "@reviewer" {
		t.Errorf("reviewer model reference = %q, want the definition's own @reviewer", reviewer.Model)
	}
	if reviewer.EffectiveModel != "claude-opus-4-1" || reviewer.InheritedModel {
		t.Fatalf("reviewer resolves to %q inherited=%v, want claude-opus-4-1", reviewer.EffectiveModel, reviewer.InheritedModel)
	}

	view := xansi.Strip(selector.Render())
	if !strings.Contains(view, "@reviewer → claude-opus-4-1") {
		t.Errorf("assigned role row does not name its model:\n%s", view)
	}
	if strings.Contains(view, "@reviewer → session model") {
		t.Errorf("assigned role claims the session model:\n%s", view)
	}

	// explore's own "@explore" is unassigned in this map, and so is the smol it
	// falls back to: the session model answers, and the row says so instead of
	// showing a model that will not run.
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
	if !strings.Contains(view, "@explore → session model") {
		t.Errorf("unassigned role row does not say session model:\n%s", view)
	}
	if !strings.Contains(view, "session model: claude-sonnet-4-6") {
		t.Errorf("detail line does not name the session model:\n%s", view)
	}
}

// TestAgentRowNamesTheRoleEffort pins the third fact of a role row: the depth
// the role asks for travels to the list beside the model it resolved to, so the
// user sees what the run will send rather than only where it routes.
func TestAgentRowNamesTheRoleEffort(t *testing.T) {
	src := fakeAgentModelsWithEfforts(
		map[string]string{"reviewer": "anthropic/claude-opus-4-1"},
		map[string]string{"reviewer": "high"},
		"claude-sonnet-4-6",
	)
	selector, _ := bundledAgentSelector(t, src, 120)

	reviewer, ok := agentItemNamed(selector, "reviewer")
	if !ok {
		t.Fatal("reviewer not listed")
	}
	if reviewer.EffectiveEffort != "high" {
		t.Fatalf("reviewer effort = %q, want the role's high", reviewer.EffectiveEffort)
	}
	view := xansi.Strip(selector.Render())
	if !strings.Contains(view, "@reviewer → claude-opus-4-1 (high)") {
		t.Errorf("role row does not name its effort:\n%s", view)
	}

	// A role with no effort bound shows the model alone: "(default)" would be
	// a claim the list cannot make, since the rung belongs to the model.
	plain, _ := bundledAgentSelector(t, fakeAgentModels(map[string]string{"reviewer": "anthropic/claude-opus-4-1"}, "claude-sonnet-4-6"), 120)
	view = xansi.Strip(plain.Render())
	if strings.Contains(view, "claude-opus-4-1 (") {
		t.Errorf("unbound effort rendered a suffix:\n%s", view)
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
	if !strings.Contains(view, "read-only sessions run as explore") || !strings.Contains(view, "gpt-5") {
		t.Errorf("worker row does not say which charter it runs or the model it will run:\n%s", view)
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
	if !strings.Contains(view, "@reviewer") {
		t.Errorf("row lost its configured reference:\n%s", view)
	}
	if strings.Contains(view, "@reviewer →") || strings.Contains(view, "session model:") {
		t.Errorf("row claims a resolution it does not have:\n%s", view)
	}
}
