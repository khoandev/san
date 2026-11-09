package subagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool"

	// Register the built-in tools so the bundled agents' allow_tools lists are
	// checked against the same registry the running app exposes.
	_ "github.com/genai-io/san/internal/tool/register"
)

// bundledNames is the default set a fresh install must have: one definition per
// rung of work, plus the read-only reviewer.
var bundledNames = []string{"explore", "quick", "reviewer", "task"}

// TestBundledAgentsLoadOnAFreshInstall covers the promise of the embedded set:
// a new install has four usable agents, each carrying its charter and a source
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
		// Every bundled definition declares the rung of work it is for, and
		// runs on that rung's role: an "@" prefix and a role name, so a typo
		// cannot silently become a model id nobody serves, and moving one rung
		// in /models cannot move another's model or effort.
		if config.Tier == "" || !ValidAgentTier(config.Tier) {
			t.Errorf("%s tier = %q, want a declared rung", name, config.Tier)
		}
		if role, ok := llm.ParseRoleAlias(config.Model); !ok {
			t.Errorf("%s model = %q, want an @role reference", name, config.Model)
		} else if !llm.IsRoleName(role) {
			t.Errorf("%s model names an invalid role: %q", name, config.Model)
		} else if want := "@" + string(config.Tier); config.Model != want {
			t.Errorf("%s model = %q, want @%s so its model matches its tier", name, config.Model, config.Tier)
		}
		// Discovery metadata is what the delegating model reads before it
		// picks: one line of scope, then when to use it and when not to.
		if strings.Contains(config.Description, "\n") {
			t.Errorf("%s description must be one line: %q", name, config.Description)
		}
		if !strings.HasPrefix(config.WhenToUse, "When:") || !strings.Contains(config.WhenToUse, "Not when:") {
			t.Errorf("%s when-to-use must state when and when not: %q", name, config.WhenToUse)
		}
	}

	// The catalog must carry the same constraints, since that string is the
	// only thing the delegating model sees.
	catalog := r.AgentCatalog()
	for _, want := range []string{
		"- explore [read-only, @fast, fast]: ",
		"- quick [edit, @fast, fast]: ",
		"- reviewer [read-only, @deep, deep]: ",
		"- task [edit, @standard, standard]: ",
	} {
		if !strings.Contains(catalog, want) {
			t.Errorf("catalog does not tell the delegating model %q:\n%s", want, catalog)
		}
	}
	// A retired name is an old spelling, not a choice: the four real
	// definitions are all the delegating model is offered.
	for _, retired := range []string{"- librarian", "- designer"} {
		if strings.Contains(catalog, retired) {
			t.Errorf("catalog offers the retired definition %q:\n%s", retired, catalog)
		}
	}
}

// TestRetiredAgentNamesResolveToTheirSuccessor pins the compatibility promise:
// a call, an agents.json entry or a persona written before the collapse still
// names work that runs, and it runs as the definition that absorbed it.
func TestRetiredAgentNamesResolveToTheirSuccessor(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"librarian":  "explore",
		"designer":   "task",
		" Librarian": "explore",
	} {
		config, exists, enabled := r.LookupAgent(name)
		if !exists || !enabled || config.Name != want {
			t.Errorf("LookupAgent(%q) = %+v, exists=%v enabled=%v; want %s", name, config, exists, enabled, want)
			continue
		}
		// The Agent call path resolves through the same lookup, so the call
		// runs the successor's charter, tools and mode.
		if config, ok := resolveAgentConfigFrom(r, name); !ok || config.Name != want {
			t.Errorf("resolveAgentConfigFrom(%q) = %+v, %v; want %s", name, config, ok, want)
		}
	}
	// Disabling the successor disables the alias with it: a retired name must
	// not be a way around agents.json.
	if err := r.SetEnabled("explore", false, setting.ScopeUser); err != nil {
		t.Fatal(err)
	}
	if _, _, enabled := r.LookupAgent("librarian"); enabled {
		t.Error("librarian still runs while the definition that absorbed it is disabled")
	}
	if _, _, enabled := r.LookupAgent("task"); !enabled {
		t.Error("one alias's disable leaked onto another definition")
	}
}

// TestUserFileWinsOverARetiredName keeps an alias honest: a user who writes
// their own librarian.md gets it run, because a file is a real definition and
// an alias is only a fallback for a name nothing defines.
func TestUserFileWinsOverARetiredName(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeAgentFile(t, filepath.Join(home, ".san", "agents"), "librarian",
		"name: librarian\ndescription: my own librarian")

	r, err := Load(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, exists, enabled := r.LookupAgent("librarian")
	if !exists || !enabled || config.Description != "my own librarian" {
		t.Fatalf("LookupAgent(librarian) = %+v, exists=%v enabled=%v; want the user's file", config, exists, enabled)
	}
	if _, exists := r.Get("explore"); !exists {
		t.Fatal("the bundled explore definition should still be registered")
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
	if got := r.AgentCatalog(); got != "" && (strings.Contains(got, "- explore [") || strings.Contains(got, "- reviewer [")) {
		t.Fatalf("catalog lists a disabled bundled agent:\n%s", got)
	}
}

// TestBundledAgentToolsExist guards the one mistake an embedded agent cannot
// make: naming a tool that is not registered (a Claude Code spelling such as
// Grep, Glob, Find or LSP), which would silently narrow the schema and produce
// a worker that cannot do its job.
func TestBundledAgentToolsExist(t *testing.T) {
	// AskUserQuestion is a safe interaction tool: a routed worker must be able
	// to ask the user, the way an unnamed worker could before routing existed.
	allowed := []string{tool.ToolRead, tool.ToolWrite, tool.ToolEdit, tool.ToolBash,
		tool.ToolWebFetch, tool.ToolWebSearch, tool.ToolSkill, tool.ToolSendMessage,
		tool.ToolAskUserQuestion}

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

// TestTierIsNormalizedAndValidated covers the frontmatter contract of the new
// field: a retired per-agent name folds onto its rung, and a label that is not
// a rung fails the definition instead of being reported as a tier the
// delegating model cannot act on.
func TestTierIsNormalizedAndValidated(t *testing.T) {
	config, err := parseAgentBytes("old.md", []byte("---\nname: old\ntier: reviewer\n---\n\nBody.\n"))
	if err != nil || config == nil {
		t.Fatalf("parseAgentBytes: %v", err)
	}
	if config.Tier != TierDeep {
		t.Fatalf("tier = %q, want the reviewer name folded onto deep", config.Tier)
	}

	if _, err := parseAgentBytes("bad.md", []byte("---\nname: bad\ntier: enormous\n---\n\nBody.\n")); err == nil {
		t.Fatal("an unknown tier parsed as a valid definition")
	}

	// A definition that declares no tier is valid and reports none, so an
	// existing user file keeps loading.
	config, err = parseAgentBytes("plain.md", []byte("---\nname: plain\n---\n\nBody.\n"))
	if err != nil || config == nil || config.Tier != "" {
		t.Fatalf("tierless definition = %+v, %v; want no tier", config, err)
	}
}
