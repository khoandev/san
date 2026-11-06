package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/setting"
)

func TestAgentCatalogContainsOnlyEnabledDiscoveryMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r := NewRegistry()
	if err := r.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(t.TempDir(), "reviewer.md")
	if err := os.WriteFile(promptPath, []byte("---\nname: reviewer\ndescription: Reviews changes\n---\nBODY_SENTINEL: full charter belongs only to the worker.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &AgentConfig{Name: "reviewer", Description: "Reviews\nchanges", WhenToUse: "Before\nmerging", Model: "MODEL_SENTINEL", PermissionMode: PermissionBypass,
		AllowTools: ToolList{{Name: "Bash", Pattern: "COMMAND_SENTINEL"}}, SourceFile: promptPath}
	r.Register(config)
	r.Register(&AgentConfig{Name: "a-plugin:reader", Description: "Reads source"})
	r.Register(&AgentConfig{Name: "scout", Description: "Scouts code", Model: "@explore", PermissionMode: PermissionExplore})
	r.Register(&AgentConfig{Name: "disabled", Description: "DISABLED_SENTINEL"})
	if err := r.SetEnabled("disabled", false, setting.ScopeProject); err != nil {
		t.Fatal(err)
	}
	catalog := r.AgentCatalog()
	for _, want := range []string{
		"Available predefined agents:",
		"- reviewer [edit]: Reviews changes",
		"Use when: Before merging",
		"- a-plugin:reader [default]: Reads source",
		"- scout [read-only, @explore]: Scouts code",
	} {
		if !strings.Contains(catalog, want) {
			t.Fatalf("catalog lost %q: %s", want, catalog)
		}
	}
	for _, omitted := range []string{"BODY_SENTINEL", "MODEL_SENTINEL", "COMMAND_SENTINEL", "DISABLED_SENTINEL", "bypassPermissions", "Tools:"} {
		if strings.Contains(catalog, omitted) {
			t.Fatalf("catalog leaked %q: %s", omitted, catalog)
		}
	}
	if strings.Index(catalog, "- a-plugin:reader") > strings.Index(catalog, "- reviewer") {
		t.Fatal("catalog ordering is unstable")
	}
	if config.SystemPrompt != "" {
		t.Fatal("discovery eagerly loaded the full charter")
	}
	r.LoadPersona([]string{"reviewer"})
	if strings.Contains(r.AgentCatalog(), "a-plugin:reader") {
		t.Fatal("persona-hidden agent remained discoverable")
	}
	if err := r.SetEnabled("reviewer", false, setting.ScopeProject); err != nil {
		t.Fatal(err)
	}
	if got := r.AgentCatalog(); got != "" {
		t.Fatalf("empty catalog should omit its heading: %q", got)
	}
}

// ReadOnly is the Plan Mode gate's only evidence about a definition, so it has
// to answer what the executor would actually do: a name the runtime will not
// run — unknown, disabled, or hidden by the active persona — is never
// vouchered for, and the declared mode is normalized the same way a run does.
func TestReadOnlyReportsTheEffectiveWriteBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r := NewRegistry()
	if err := r.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	r.Register(&AgentConfig{Name: "explore", Description: "Investigates", PermissionMode: PermissionExplore})
	r.Register(&AgentConfig{Name: "scout", Description: "Scouts", PermissionMode: "read-only"})
	r.Register(&AgentConfig{Name: "task", Description: "Implements", PermissionMode: PermissionAcceptEdits})
	r.Register(&AgentConfig{Name: "plain", Description: "Declares no mode"})

	for name, want := range map[string]bool{
		"explore":   true,
		"scout":     true, // the long spelling normalizes to explore
		"task":      false,
		"plain":     false, // an unset mode is not explore
		"missing":   false,
		" EXPLORE ": true, // the lookup folds case and space
	} {
		if got := r.ReadOnly(name); got != want {
			t.Errorf("ReadOnly(%q) = %v, want %v", name, got, want)
		}
	}

	if err := r.SetEnabled("explore", false, setting.ScopeProject); err != nil {
		t.Fatal(err)
	}
	if r.ReadOnly("explore") {
		t.Fatal("a disabled definition cannot be delegated")
	}

	r.LoadPersona([]string{"task"})
	if r.ReadOnly("scout") {
		t.Fatal("a persona-hidden definition cannot be delegated")
	}
	r.ClearPersona()
	if !r.ReadOnly("scout") {
		t.Fatal("clearing the persona must make the definition delegable again")
	}
}
