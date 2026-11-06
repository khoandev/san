package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
)

// roleTestProvider is a provider identity for resolution tests: what matters is
// which provider answered, not whether it can infer.
type roleTestProvider struct{ name string }

func (p *roleTestProvider) Client(string, map[string]string) (*ai.Client, error) {
	return nil, errors.New("not used")
}

func (p *roleTestProvider) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }

func (p *roleTestProvider) Name() string { return p.name }

// roleTestResolver serves one connected vendor and fails the rest, the way a
// pool does when only one provider has credentials.
type roleTestResolver struct {
	connected ProviderID
	provider  Provider
}

func (r roleTestResolver) Resolve(_ context.Context, vendor ProviderID) (Provider, error) {
	if vendor == r.connected {
		return r.provider, nil
	}
	return nil, errors.New("vendor not connected")
}

func TestRoleResolverResolvesAssignedVendorReference(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})

	got := resolver.Resolve(context.Background(),
		Bindings(map[string]string{"slow": "anthropic/claude-sonnet-4-6"}), "slow", session, "gpt-5")

	if got.Provider != anthropic || got.ModelID != "claude-sonnet-4-6" {
		t.Fatalf("resolved = (%v, %q), want the bound vendor model", got.Provider, got.ModelID)
	}
	if got.Inherited || got.Ref != "anthropic/claude-sonnet-4-6" || got.Role != RoleSlow {
		t.Fatalf("target provenance = %+v, want an explicit slow assignment", got)
	}
}

func TestRoleResolverKeepsBareReferenceOnSessionProvider(t *testing.T) {
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(nil)

	got := resolver.Resolve(context.Background(), Bindings(map[string]string{"smol": "claude-haiku-4-5"}), "smol", session, "gpt-5")

	if got.Provider != session || got.ModelID != "claude-haiku-4-5" {
		t.Fatalf("resolved = (%v, %q), want the bare id on the session provider", got.Provider, got.ModelID)
	}
	if got.Inherited {
		t.Fatal("a bare assignment is not an inherited session model")
	}
}

func TestRoleResolverUnassignedRoleUsesSessionModel(t *testing.T) {
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(nil)

	for _, name := range []string{"slow", "smol", "task", "review"} {
		got := resolver.Resolve(context.Background(), Bindings(map[string]string{"designer": "claude-sonnet-4-6"}), name, session, "gpt-5")
		if got.Provider != session || got.ModelID != "gpt-5" {
			t.Fatalf("%s = (%v, %q), want the session model", name, got.Provider, got.ModelID)
		}
		if !got.Inherited {
			t.Fatalf("%s did not report inheritance", name)
		}
	}
}

func TestRoleResolverUnconnectedVendorFallsBackToSessionModel(t *testing.T) {
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: &roleTestProvider{name: "anthropic:api_key"}})

	got := resolver.Resolve(context.Background(), Bindings(map[string]string{"slow": "google/gemini-3-pro"}), "slow", session, "gpt-5")

	if got.Provider != session || got.ModelID != "gpt-5" || !got.Inherited {
		t.Fatalf("resolved = %+v, want the session model when the vendor is not connected", got)
	}
}

func TestRoleResolverExpandsNestedRoleAlias(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})

	got := resolver.Resolve(context.Background(),
		Bindings(map[string]string{"review": "@slow", "slow": "anthropic/claude-sonnet-4-6"}), "review", nil, "gpt-5")

	if got.Provider != anthropic || got.ModelID != "claude-sonnet-4-6" {
		t.Fatalf("nested alias = (%v, %q), want the slow role's model", got.Provider, got.ModelID)
	}
}

func TestRoleResolverDeployAliasesResolveToSessionModel(t *testing.T) {
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(nil)

	// "@default" and an alias cycle both end at the session model rather than
	// at an error or a loop: configuration cannot break the turn.
	for name, bindings := range map[string]RoleBindings{
		"default": Bindings(map[string]string{"review": "@default"}),
		"cycle":   Bindings(map[string]string{"review": "@other", "other": "@review"}),
		"empty":   Bindings(map[string]string{"review": ""}),
		"garbage": Bindings(map[string]string{"review": "@"}),
	} {
		got := resolver.Resolve(context.Background(), bindings, "review", session, "gpt-5")
		if got.Provider != session || got.ModelID != "gpt-5" {
			t.Fatalf("%s = (%v, %q), want the session model", name, got.Provider, got.ModelID)
		}
	}
}

func TestRoleResolverResolveRefHandlesAgentModelForms(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})

	for _, tc := range []struct {
		name     string
		ref      string
		provider Provider
		model    string
	}{
		{"empty inherits", "", session, "gpt-5"},
		{"inherit inherits", "inherit", session, "gpt-5"},
		{"vendor routes", "anthropic/claude-sonnet-4-6", anthropic, "claude-sonnet-4-6"},
		{"bare stays", "claude-haiku-4-5", session, "claude-haiku-4-5"},
		{"unconnected vendor inherits", "google/gemini-3-pro", session, "gpt-5"},
		{"unknown role inherits", "@nobody", session, "gpt-5"},
	} {
		got := resolver.ResolveRef(context.Background(), tc.ref, session, "gpt-5")
		if got.Provider != tc.provider || got.ModelID != tc.model {
			t.Fatalf("%s: ResolveRef(%q) = (%v, %q), want (%v, %q)",
				tc.name, tc.ref, got.Provider, got.ModelID, tc.provider, tc.model)
		}
	}
}

func TestRoleResolverNilProviderPoolStillResolvesBareReferences(t *testing.T) {
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(nil)

	got := resolver.ResolveRef(context.Background(), "some-model", session, "gpt-5")
	if got.Provider != session || got.ModelID != "some-model" {
		t.Fatalf("bare ref without a pool = (%v, %q)", got.Provider, got.ModelID)
	}
	if got := resolver.ResolveRef(context.Background(), "anthropic/x", session, "gpt-5"); got.Provider != session {
		t.Fatalf("vendor ref without a pool = %+v, want the session model", got)
	}

	var nilResolver *RoleResolver
	if got := nilResolver.ResolveRef(context.Background(), "", session, "gpt-5"); got.Provider != session || got.ModelID != "gpt-5" {
		t.Fatalf("nil resolver = %+v, want the session model", got)
	}
}

func TestRoleCatalogAndNames(t *testing.T) {
	// Per-agent slots come first after the session's own model, because they are
	// what a user tunes; the utility roles they fall back to come last.
	want := []ModelRole{
		RoleDefault, RolePlan, RoleTask, RoleExplore, RoleLibrarian,
		RoleReviewer, RoleDesigner, RoleSmol, RoleSlow,
	}
	got := RoleCatalog()
	if len(got) != len(want) {
		t.Fatalf("catalog has %d roles, want %d", len(got), len(want))
	}
	for i, spec := range got {
		if spec.Name != want[i] {
			t.Fatalf("catalog[%d] = %q, want %q", i, spec.Name, want[i])
		}
		if spec.Description == "" || spec.Consumer == "" {
			t.Fatalf("role %q is missing its description or consumer", spec.Name)
		}
	}
	// Every fallback names a role in the catalog, and following them cannot
	// loop: an unassigned role must always reach a terminal slot.
	for _, spec := range got {
		role, depth := spec, 0
		for role.Fallback != "" {
			next, ok := RoleSpecFor(string(role.Fallback))
			if !ok {
				t.Fatalf("role %q falls back to unknown role %q", role.Name, role.Fallback)
			}
			role = next
			depth++
			if depth > len(want) {
				t.Fatalf("fallback chain from %q loops", spec.Name)
			}
		}
	}
	// The returned slice is a copy: a caller cannot rename a built-in role.
	got[0].Name = "renamed"
	if RoleCatalog()[0].Name != RoleDefault {
		t.Fatal("RoleCatalog handed out its own storage")
	}
	if _, ok := RoleSpecFor("SLOW"); !ok {
		t.Fatal("RoleSpecFor should fold case")
	}
	if _, ok := RoleSpecFor("review"); ok {
		t.Fatal("a custom role has no built-in spec")
	}

	for name, valid := range map[string]bool{
		"slow": true, "my-role": true, "role2": true,
		"": false, "-bad": false, "2bad": false, "With_Caps": false, "a b": false,
	} {
		if got := IsRoleName(name); got != valid {
			t.Fatalf("IsRoleName(%q) = %v, want %v", name, got, valid)
		}
	}

	for ref, wantRole := range map[string]string{"@slow": "slow", " @Slow ": "slow", "@my-role": "my-role"} {
		if role, ok := ParseRoleAlias(ref); !ok || role != wantRole {
			t.Fatalf("ParseRoleAlias(%q) = (%q, %v), want %q", ref, role, ok, wantRole)
		}
	}
	for _, ref := range []string{"slow", "anthropic/claude", "@", "@Slow Role", ""} {
		if role, ok := ParseRoleAlias(ref); ok {
			t.Fatalf("ParseRoleAlias(%q) parsed as role %q", ref, role)
		}
	}
}

func TestRoleBindingsRefTrimsAndFolds(t *testing.T) {
	bindings := Bindings(map[string]string{"slow": "  anthropic/claude-sonnet-4-6  ", "SMOl": "claude-haiku-4-5"})
	if got := bindings.Ref("SLOW"); got != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("Ref(SLOW) = %q", got)
	}
	if got := bindings.Ref("smol"); got != "claude-haiku-4-5" {
		t.Fatalf("Ref(smol) = %q", got)
	}
	if got := bindings.Ref("task"); got != "" {
		t.Fatalf("unassigned Ref = %q, want empty", got)
	}
	if got := bindings.Effort("smol"); got != "" {
		t.Fatalf("unbound Effort = %q, want empty", got)
	}
	var nilBindings RoleBindings
	if got := nilBindings.Ref("slow"); got != "" {
		t.Fatalf("nil bindings Ref = %q", got)
	}
	if got := nilBindings.Effort("slow"); got != "" {
		t.Fatalf("nil bindings Effort = %q", got)
	}
}

// TestRoleBindingsEffortTrimsAndFolds pins the second map's reading rule: it is
// hand-edited in settings.json exactly like modelRoles.
func TestRoleBindingsEffortTrimsAndFolds(t *testing.T) {
	bindings := BindingsOf(nil, map[string]string{"Reviewer": "  High ", "smol": "low"})
	if got := bindings.Effort("reviewer"); got != "High" {
		t.Fatalf("Effort(REVIEWER) = %q", got)
	}
	if got := bindings.Effort("smol"); got != "low" {
		t.Fatalf("Effort(smol) = %q", got)
	}
	if got := bindings.Effort("task"); got != "" {
		t.Fatalf("unbound Effort = %q, want empty", got)
	}
	// Both maps stay independent: an effort never implies a model reference.
	if got := bindings.Ref("smol"); got != "" {
		t.Fatalf("Ref(smol) = %q, want empty", got)
	}
}

// TestRoleResolverUnassignedFollowsFallbackRole pins the compatibility promise
// of the per-agent slots: a user who never binds explore, librarian or reviewer
// keeps the model those agents used before their roles existed.
func TestRoleResolverUnassignedFollowsFallbackRole(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})
	bindings := Bindings(map[string]string{
		"smol": "anthropic/claude-haiku-4-5",
		"slow": "anthropic/claude-sonnet-4-6",
	})

	for role, want := range map[string]string{
		"explore":   "claude-haiku-4-5",
		"librarian": "claude-haiku-4-5",
		"reviewer":  "claude-sonnet-4-6",
	} {
		got := resolver.Resolve(context.Background(), bindings, role, session, "gpt-5")
		if got.Provider != anthropic || got.ModelID != want {
			t.Fatalf("%s = (%v, %q), want the fallback role's model %q", role, got.Provider, got.ModelID, want)
		}
		if got.Inherited {
			t.Fatalf("%s reported inheritance although its fallback role is bound", role)
		}
	}

	// Binding the slot itself wins over the fallback role, with or without a
	// vendor of its own.
	for _, ref := range []string{"anthropic/claude-opus-4-7", "gpt-5-mini"} {
		own := Bindings(map[string]string{"explore": ref, "smol": "anthropic/claude-haiku-4-5"})
		got := resolver.Resolve(context.Background(), own, "explore", session, "gpt-5")
		want := "claude-opus-4-7"
		if ref == "gpt-5-mini" {
			want = "gpt-5-mini" // a bare id stays on the session provider
		}
		if got.ModelID != want || got.Inherited {
			t.Fatalf("assigned explore %q = %+v, want its own binding %q", ref, got, want)
		}
	}

	// Nothing bound anywhere: the session model answers, as it always did.
	sessionOnly := resolver.Resolve(context.Background(), Bindings(nil), "explore", session, "gpt-5")
	if sessionOnly.Provider != session || sessionOnly.ModelID != "gpt-5" || !sessionOnly.Inherited {
		t.Fatalf("unbound fallback chain = %+v, want the session model", sessionOnly)
	}
}

// TestRoleResolverCarriesEffort pins the provenance rule: the role the caller
// named supplies the effort, otherwise the role that supplied the model does.
func TestRoleResolverCarriesEffort(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})

	// Effort of a fallback role travels with the model it supplied.
	fallback := BindingsOf(
		map[string]string{"smol": "anthropic/claude-haiku-4-5"},
		map[string]string{"smol": "low"},
	)
	if got := resolver.Resolve(context.Background(), fallback, "explore", session, "gpt-5"); got.ModelID != "claude-haiku-4-5" || got.Effort != "low" {
		t.Fatalf("explore via smol = %+v, want smol's model and effort", got)
	}

	// The named role's own effort wins over its fallback's.
	both := BindingsOf(
		map[string]string{"smol": "anthropic/claude-haiku-4-5"},
		map[string]string{"smol": "low", "explore": "high"},
	)
	if got := resolver.Resolve(context.Background(), both, "explore", session, "gpt-5"); got.Effort != "high" {
		t.Fatalf("explore effort = %q, want the named role's high", got.Effort)
	}

	// An effort can be bound without a model: the role's work gets that depth
	// on whichever model answers.
	effortOnly := BindingsOf(nil, map[string]string{"task": "xhigh"})
	got := resolver.Resolve(context.Background(), effortOnly, "task", session, "gpt-5")
	if got.Provider != session || got.ModelID != "gpt-5" || !got.Inherited || got.Effort != "xhigh" {
		t.Fatalf("effort-only role = %+v, want the session model with xhigh", got)
	}

	// A raw reference names no role, so it can carry no role effort.
	if got := resolver.ResolveRef(context.Background(), "anthropic/claude-haiku-4-5", session, "gpt-5"); got.Effort != "" {
		t.Fatalf("ResolveRef carried effort %q", got.Effort)
	}
}

// roleEffortProvider advertises a reasoning ladder, the shape ResolveTargetEffort
// validates against.
type roleEffortProvider struct {
	roleTestProvider
	efforts []string
	def     string
}

func (p *roleEffortProvider) ThinkingEfforts(string) []string     { return p.efforts }
func (p *roleEffortProvider) DefaultThinkingEffort(string) string { return p.def }

// TestResolveTargetEffortValidatesAgainstModel keeps the stored label a hint: a
// rung the model does not offer resolves to that model's default, and a model
// with no ladder sends no effort at all.
func TestResolveTargetEffortValidatesAgainstModel(t *testing.T) {
	p := &roleEffortProvider{
		roleTestProvider: roleTestProvider{name: "anthropic:api_key"},
		efforts:          []string{"low", "medium", "high"},
		def:              "medium",
	}
	model := &CurrentModelInfo{ModelID: "claude-sonnet-4-6", Provider: Anthropic}

	for effort, want := range map[string]string{
		"high":  "high",
		"High":  "high",
		"ultra": "medium", // not on this model's ladder: its default answers
		"":      "",
	} {
		target := RoleTarget{Provider: p, ModelID: "claude-sonnet-4-6", Effort: effort}
		if got := ResolveTargetEffort(p, nil, model, target); got != want {
			t.Errorf("ResolveTargetEffort(%q) = %q, want %q", effort, got, want)
		}
	}

	// A model that does not reason has no rung to send.
	plain := &roleTestProvider{name: "openai:api_key"}
	if got := ResolveTargetEffort(plain, nil, model, RoleTarget{Provider: plain, ModelID: "gpt-5", Effort: "high"}); got != "" {
		t.Fatalf("non-reasoning model got effort %q", got)
	}
	// No provider, no model: nothing to validate, nothing to send.
	if got := ResolveTargetEffort(nil, nil, nil, RoleTarget{ModelID: "x", Effort: "high"}); got != "" {
		t.Fatalf("nil provider got effort %q", got)
	}
}
