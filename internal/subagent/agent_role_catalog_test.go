// The Agent tool's catalog advertises the model role each definition runs on, so
// a delegating model chooses on what /settings configured rather than on what a
// definition file alone declares.
package subagent

import (
	"strings"
	"testing"
)

// catalogRegistry builds a registry holding the fixture definitions the catalog
// tests read: one that declares a role, one that names a concrete model.
func catalogRegistry(t *testing.T) *Registry {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r := NewRegistry()
	if err := r.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	r.Register(&AgentConfig{Name: "quick", Description: "Small edits", Model: "@fast", PermissionMode: PermissionAcceptEdits})
	r.Register(&AgentConfig{Name: "pinned", Description: "Names a model", Model: "anthropic/claude-sonnet-4-5"})
	return r
}

// An assignment replaces the declaration in the catalog, and a definition that
// named a concrete model starts reporting the assigned role instead of staying
// silent. Clearing the source returns both to their files.
func TestAgentCatalogShowsTheAssignedRole(t *testing.T) {
	r := catalogRegistry(t)

	if got := r.AgentCatalog(); !strings.Contains(got, "- quick [edit, @fast]") {
		t.Fatalf("catalog lost the declared role: %s", got)
	}

	roles := map[string]string{"quick": "deep", "pinned": "@standard"}
	r.SetAgentRoleSource(func(name string) string {
		// The resolver folds case and space, the way settings does.
		if role, ok := roles[strings.ToLower(strings.TrimSpace(name))]; ok {
			return role
		}
		return ""
	})

	catalog := r.AgentCatalog()
	for _, want := range []string{"- quick [edit, @deep]", "- pinned [default, @standard]"} {
		if !strings.Contains(catalog, want) {
			t.Fatalf("catalog lost the assigned role %q: %s", want, catalog)
		}
	}
	if strings.Contains(catalog, "@fast") {
		t.Fatalf("catalog still advertises the superseded declared role: %s", catalog)
	}

	// A nil source hands the catalog back to the definitions.
	r.SetAgentRoleSource(nil)
	if got := r.AgentCatalog(); !strings.Contains(got, "- quick [edit, @fast]") {
		t.Fatalf("clearing the source did not restore the declaration: %s", got)
	}
}

// The catalog never invents a role: an agent the resolver knows nothing about,
// and a definition whose model field is not a role, keep the label they had.
func TestAgentCatalogIgnoresAnEmptyAssignment(t *testing.T) {
	r := catalogRegistry(t)
	r.SetAgentRoleSource(func(string) string { return "  " })

	catalog := r.AgentCatalog()
	if !strings.Contains(catalog, "- quick [edit, @fast]") {
		t.Fatalf("a blank assignment changed the label: %s", catalog)
	}
	if !strings.Contains(catalog, "- pinned [default]:") {
		t.Fatalf("a model-pinned definition gained a role: %s", catalog)
	}
}
