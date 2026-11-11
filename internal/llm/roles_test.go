package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
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
	if got.Inherited || got.Ref != "anthropic/claude-sonnet-4-6" || got.Role != RoleDeep {
		t.Fatalf("target provenance = %+v, want the deep rung the retired slow slot folded onto", got)
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

	for _, name := range []string{"deep", "fast", "standard", "review"} {
		got := resolver.Resolve(context.Background(), Bindings(map[string]string{"custom-slot": "claude-sonnet-4-6"}), name, session, "gpt-5")
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
	// The ladder is display order: the session's own model, then cheapest work
	// to hardest, then the advisor's own slot. A retired per-agent name occupies
	// no row of its own.
	want := []ModelRole{RoleDefault, RoleFast, RoleStandard, RoleDeep, RoleAdvisor}
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
	// The returned slice is a copy: a caller cannot rename a built-in role.
	got[0].Name = "renamed"
	if RoleCatalog()[0].Name != RoleDefault {
		t.Fatal("RoleCatalog handed out its own storage")
	}
	if _, ok := RoleSpecFor("FAST"); !ok {
		t.Fatal("RoleSpecFor should fold case")
	}
	if _, ok := RoleSpecFor("review"); ok {
		t.Fatal("a custom role has no built-in spec")
	}
	if _, ok := RoleSpecFor("slow"); ok {
		t.Fatal("a retired name is an alias, not a catalog row")
	}
	if got, want := LegacyRoleAlias("SLOW"), string(RoleDeep); got != want {
		t.Fatalf("LegacyRoleAlias(SLOW) = %q, want %q", got, want)
	}
	if got := LegacyRoleAlias("review"); got != "review" {
		t.Fatalf("LegacyRoleAlias(review) = %q, want the name unchanged", got)
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

// TestLegacyRoleAliases pins the compatibility table: a settings file or an
// agent definition written before the ladder existed still routes. The
// companion mirror in internal/setting is pinned to the same literal.
func TestLegacyRoleAliases(t *testing.T) {
	want := map[string]string{
		"smol":      "fast",
		"explore":   "fast",
		"librarian": "fast",
		"task":      "standard",
		"designer":  "standard",
		"plan":      "deep",
		"slow":      "deep",
		"reviewer":  "deep",
	}
	for name, role := range want {
		if got := LegacyRoleAlias(name); got != role {
			t.Errorf("LegacyRoleAlias(%q) = %q, want %q", name, got, role)
		}
		if got := LegacyRoleAlias(" " + strings.ToUpper(name) + " "); got != role {
			t.Errorf("LegacyRoleAlias(%q) = %q, want %q", strings.ToUpper(name), got, role)
		}
	}
	// Every retired name is listed exactly once in inheritance order, and only
	// for a rung that exists in the catalog.
	retired := core.RetiredModelRoles()
	if len(retired) != len(want) {
		t.Fatalf("retired table has %d entries, want %d", len(retired), len(want))
	}
	seen := make(map[string]bool, len(retired))
	for _, entry := range retired {
		if seen[entry.Name] {
			t.Fatalf("retired table lists %q twice", entry.Name)
		}
		seen[entry.Name] = true
		if got := want[entry.Name]; got != entry.Role {
			t.Fatalf("retired table maps %q to %q, want %q", entry.Name, entry.Role, got)
		}
		if _, ok := RoleSpecFor(entry.Role); !ok {
			t.Fatalf("retired name %q folds onto a role outside the catalog", entry.Name)
		}
	}
}

// TestRoleResolverLegacyNamesRouteToTheirRung pins the promise of the collapse:
// a binding written under a retired name still answers for the rung that
// absorbed it, and the resolver reports that rung.
func TestRoleResolverLegacyNamesRouteToTheirRung(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})
	bindings := Bindings(map[string]string{
		"smol": "anthropic/claude-haiku-4-5",
		"task": "anthropic/claude-sonnet-4-6",
		"slow": "anthropic/claude-opus-4-7",
	})

	for name, want := range map[string]struct {
		model string
		role  ModelRole
	}{
		"smol":      {"claude-haiku-4-5", RoleFast},
		"explore":   {"claude-haiku-4-5", RoleFast},
		"librarian": {"claude-haiku-4-5", RoleFast},
		"task":      {"claude-sonnet-4-6", RoleStandard},
		"designer":  {"claude-sonnet-4-6", RoleStandard},
		"plan":      {"claude-opus-4-7", RoleDeep},
		"slow":      {"claude-opus-4-7", RoleDeep},
		"reviewer":  {"claude-opus-4-7", RoleDeep},
	} {
		got := resolver.Resolve(context.Background(), bindings, name, session, "gpt-5")
		if got.Provider != anthropic || got.ModelID != want.model {
			t.Fatalf("%s = (%v, %q), want %q", name, got.Provider, got.ModelID, want.model)
		}
		if got.Inherited {
			t.Fatalf("%s reported inheritance although its rung is bound", name)
		}
		if got.Role != want.role {
			t.Fatalf("%s reported role %q, want %q", name, got.Role, want.role)
		}
	}

	// A named binding of its own wins over the retired sibling, and a canonical
	// key wins over the retired name it inherited the work from.
	own := Bindings(map[string]string{
		"explore": "anthropic/claude-opus-4-7",
		"smol":    "anthropic/claude-haiku-4-5",
		"fast":    "anthropic/claude-sonnet-4-6",
	})
	if got := resolver.Resolve(context.Background(), own, "explore", session, "gpt-5"); got.ModelID != "claude-opus-4-7" {
		t.Fatalf("named explore = %q, want its own binding", got.ModelID)
	}
	if got := resolver.Resolve(context.Background(), own, "fast", session, "gpt-5"); got.ModelID != "claude-sonnet-4-6" {
		t.Fatalf("fast = %q, want the canonical binding over the retired smol", got.ModelID)
	}

	// Nothing bound anywhere: the session model answers, as it always did.
	sessionOnly := resolver.Resolve(context.Background(), Bindings(nil), "reviewer", session, "gpt-5")
	if sessionOnly.Provider != session || sessionOnly.ModelID != "gpt-5" || !sessionOnly.Inherited {
		t.Fatalf("unbound legacy role = %+v, want the session model", sessionOnly)
	}
}

// TestRoleResolverCanonicalAliasCycleStops keeps the depth bound meaningful on
// the ladder names too: "@fast" -> "@standard" -> "@fast" resolves to the
// session model instead of looping.
func TestRoleResolverCanonicalAliasCycleStops(t *testing.T) {
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(nil)
	cycle := Bindings(map[string]string{"fast": "@standard", "standard": "@fast"})

	got := resolver.Resolve(context.Background(), cycle, "fast", session, "gpt-5")
	if got.Provider != session || got.ModelID != "gpt-5" || !got.Inherited {
		t.Fatalf("canonical cycle = %+v, want the session model", got)
	}
}

// TestRoleResolverCarriesEffort pins the provenance rule: the role the caller
// named supplies the effort, otherwise the role that supplied the model does.
func TestRoleResolverCarriesEffort(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	session := &roleTestProvider{name: "openai:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})

	// The effort of a retired name travels with the model it supplied.
	fallback := BindingsOf(
		map[string]string{"smol": "anthropic/claude-haiku-4-5"},
		map[string]string{"smol": "low"},
	)
	if got := resolver.Resolve(context.Background(), fallback, "explore", session, "gpt-5"); got.ModelID != "claude-haiku-4-5" || got.Effort != "low" {
		t.Fatalf("explore via smol = %+v, want smol's model and effort", got)
	}

	// The named role's own effort wins over the retired sibling's.
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
