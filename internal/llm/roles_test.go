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
		RoleBindings{"slow": "anthropic/claude-sonnet-4-6"}, "slow", session, "gpt-5")

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

	got := resolver.Resolve(context.Background(), RoleBindings{"smol": "claude-haiku-4-5"}, "smol", session, "gpt-5")

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
		got := resolver.Resolve(context.Background(), RoleBindings{"designer": "claude-sonnet-4-6"}, name, session, "gpt-5")
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

	got := resolver.Resolve(context.Background(), RoleBindings{"slow": "google/gemini-3-pro"}, "slow", session, "gpt-5")

	if got.Provider != session || got.ModelID != "gpt-5" || !got.Inherited {
		t.Fatalf("resolved = %+v, want the session model when the vendor is not connected", got)
	}
}

func TestRoleResolverExpandsNestedRoleAlias(t *testing.T) {
	anthropic := &roleTestProvider{name: "anthropic:api_key"}
	resolver := NewRoleResolver(roleTestResolver{connected: Anthropic, provider: anthropic})

	got := resolver.Resolve(context.Background(),
		RoleBindings{"review": "@slow", "slow": "anthropic/claude-sonnet-4-6"}, "review", nil, "gpt-5")

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
		"default": {"review": "@default"},
		"cycle":   {"review": "@other", "other": "@review"},
		"empty":   {"review": ""},
		"garbage": {"review": "@"},
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
	want := []ModelRole{RoleDefault, RoleSmol, RoleSlow, RoleTask, RolePlan, RoleDesigner}
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
	bindings := RoleBindings{"slow": "  anthropic/claude-sonnet-4-6  ", "SMOl": "claude-haiku-4-5"}
	if got := bindings.Ref("SLOW"); got != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("Ref(SLOW) = %q", got)
	}
	if got := bindings.Ref("smol"); got != "claude-haiku-4-5" {
		t.Fatalf("Ref(smol) = %q", got)
	}
	if got := bindings.Ref("task"); got != "" {
		t.Fatalf("unassigned Ref = %q, want empty", got)
	}
	var nilBindings RoleBindings
	if got := nilBindings.Ref("slow"); got != "" {
		t.Fatalf("nil bindings Ref = %q", got)
	}
}
