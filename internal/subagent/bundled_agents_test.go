package subagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/tool"

	// Register the built-in tools so the bundled agents' allow_tools lists are
	// checked against the same registry the running app exposes.
	_ "github.com/genai-io/san/internal/tool/register"
)

// bundledNames is the default set a fresh install must have.
var bundledNames = []string{"designer", "explore", "librarian", "reviewer", "task"}

// TestBundledAgentsLoadOnAFreshInstall covers the promise of the embedded set:
// a new install has five usable agents, each carrying its charter and a source
// that says where it came from.
func TestBundledAgentsLoadOnAFreshInstall(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, config := range r.ListConfigs() {
		if config.Source == "built-in" {
			names = append(names, config.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, bundledNames) {
		t.Fatalf("bundled agents = %v, want %v", names, bundledNames)
	}

	for _, name := range bundledNames {
		config, ok := r.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if config.Source != "built-in" {
			t.Errorf("%s source = %q, want built-in", name, config.Source)
		}
		if config.SourceFile != "" {
			t.Errorf("%s reads its prompt from %q; a bundled agent must carry it", name, config.SourceFile)
		}
		if body := config.GetSystemPrompt(); body == "" {
			t.Errorf("%s has no charter", name)
		}
		if config.Description == "" || config.WhenToUse == "" {
			t.Errorf("%s is missing discovery metadata: %+v", name, config)
		}
		if config.PermissionMode == "" || len(config.AllowTools) == 0 {
			t.Errorf("%s is missing its mode or tool list: %+v", name, config)
		}
		// A bundled model is a role reference: an "@" prefix and a role name,
		// so a typo cannot silently become a model id nobody serves.
		if name, ok := llm.ParseRoleAlias(config.Model); !ok {
			t.Errorf("%s model = %q, want an @role reference", config.Name, config.Model)
		} else if !llm.IsRoleName(name) {
			t.Errorf("%s model names an invalid role: %q", config.Name, config.Model)
		}
	}
}

// TestBundledAgentPromptIsSealed confirms the charter cannot be replaced by a
// file: the prompt is the embedded body, and the once that would lazily read
// SourceFile is already spent.
func TestBundledAgentPromptIsSealed(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := r.Get("reviewer")
	if !ok {
		t.Fatal("reviewer not registered")
	}
	embedded := config.GetSystemPrompt()
	if embedded == "" {
		t.Fatal("reviewer has no charter")
	}

	other := filepath.Join(t.TempDir(), "other.md")
	if err := os.WriteFile(other, []byte("REPLACED_PROMPT"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SourceFile = other
	if got := config.GetSystemPrompt(); got != embedded {
		t.Fatalf("a file replaced the embedded charter: %q", got)
	}
}

// TestUserFileOverridesBundledAgent pins the priority: the embedded set is the
// fallback, so a definition of the same name under ~/.san/agents wins.
func TestUserFileOverridesBundledAgent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeAgentFile(t, filepath.Join(home, ".san", "agents"), "explore",
		"name: explore\ndescription: my own explorer")

	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := r.Get("explore")
	if !ok {
		t.Fatal("explore not registered")
	}
	if config.Description != "my own explorer" || config.Source != "user" {
		t.Fatalf("user definition did not win: %+v", config)
	}
}

// TestBundledAgentCanBeDisabled confirms agents.json still governs the embedded
// set: nothing about being built in makes an agent unswitchable.
func TestBundledAgentCanBeDisabled(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".san", "agents.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"disabled":["explore","reviewer"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"explore", "reviewer"} {
		config, exists, enabled := r.LookupAgent(name)
		if !exists || config == nil {
			t.Fatalf("%s disappeared from the registry", name)
		}
		if enabled {
			t.Errorf("%s is still enabled despite agents.json", name)
		}
	}
	if _, _, enabled := r.LookupAgent("task"); !enabled {
		t.Error("disabling one bundled agent disabled another")
	}
	if got := r.AgentCatalog(); got != "" && (contains(got, "- explore:") || contains(got, "- reviewer:")) {
		t.Fatalf("catalog lists a disabled bundled agent:\n%s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// TestBundledAgentToolsExist guards the one mistake an embedded agent cannot
// make: naming a tool that is not registered (a Claude Code spelling such as
// Grep, Glob, Find or LSP), which would silently narrow the schema and produce
// a worker that cannot do its job.
func TestBundledAgentToolsExist(t *testing.T) {
	allowed := []string{tool.ToolRead, tool.ToolWrite, tool.ToolEdit, tool.ToolBash,
		tool.ToolWebFetch, tool.ToolWebSearch, tool.ToolSkill, tool.ToolSendMessage}

	present := make(map[string]bool)
	for _, schema := range tool.GetToolSchemas() {
		present[schema.Name] = true
	}
	if len(present) == 0 {
		t.Fatal("the tool registry is empty; the guard would pass vacuously")
	}

	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range bundledNames {
		config, ok := r.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		for _, rule := range config.AllowTools {
			if !slices.Contains(allowed, rule.Name) {
				t.Errorf("%s allows %q, which is not a tool a bundled agent may use", name, rule.Name)
			}
			if !present[rule.Name] && !tool.IsShellTool(rule.Name) {
				t.Errorf("%s allows %q, which is not in tool.GetToolSchemas()", name, rule.Name)
			}
		}
		if slices.Contains(config.AllowTools.Names(), "Grep") {
			t.Errorf("%s allows Grep, which san does not have", name)
		}
	}
}

// TestExploreAgentRefusesMutation is the read-only boundary as the runtime
// applies it: a definition's mode and allow_tools cannot be talked into an
// edit, however the call is phrased.
func TestExploreAgentRefusesMutation(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := r.Get("explore")
	if !ok {
		t.Fatal("explore not registered")
	}
	check := subagentPermissionFuncWithPrompt(config.PermissionMode, config.AllowTools, config.DenyTools, nil)

	for _, name := range []string{tool.ToolEdit, tool.ToolWrite} {
		allowed, reason := check(context.Background(), name, map[string]any{"file_path": "README.md"})
		if allowed {
			t.Errorf("explore allowed %s: %s", name, reason)
		}
	}
	if allowed, _ := check(context.Background(), tool.ToolRead, map[string]any{"file_path": "README.md"}); !allowed {
		t.Error("explore refused Read")
	}
	if allowed, reason := check(context.Background(), tool.ToolBash, map[string]any{"command": "git status"}); !allowed {
		t.Errorf("explore refused a read-only command: %s", reason)
	}
	if allowed, _ := check(context.Background(), tool.ToolBash, map[string]any{"command": "rm -rf build"}); allowed {
		t.Error("explore allowed a mutating command")
	}
}

// TestParseAgentFileAcceptsCamelPermissionModeAlias keeps the third documented
// spelling working, so a definition exported by another tool still carries its
// mode instead of silently falling back to default.
func TestParseAgentFileAcceptsCamelPermissionModeAlias(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "camel", "name: camel\ndescription: camel mode\npermissionMode: explore")

	config, err := parseAgentFile(filepath.Join(dir, "camel.md"))
	if err != nil {
		t.Fatalf("parseAgentFile: %v", err)
	}
	if config.PermissionMode != PermissionExplore {
		t.Fatalf("permissionMode alias = %q, want explore", config.PermissionMode)
	}
}
