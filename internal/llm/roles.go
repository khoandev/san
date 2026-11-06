package llm

import (
	"context"
	"strings"
)

// Model roles route a kind of work to a model without naming that model where
// the work happens.
//
// A role is a routing slot, not a behaviour: it answers "which model", while a
// subagent answers "what kind of worker". Keeping them apart means a user can
// move every review to a stronger model — or a cheaper one — by editing one
// setting, while agent definitions keep referring to their role and stay
// unchanged.
//
// Resolution has one rule and one escape hatch:
//
//   - An unassigned role (or a reference that cannot be resolved: an unknown
//     role, a vendor that is not connected) uses the session's own model. Work
//     never fails because configuration is incomplete or stale.
//   - An assigned reference wins over the session model. A bare model id stays
//     on the session's provider; "vendor/model" routes to that connected vendor.
//
// The session model answers an unassigned role on purpose rather than guessing
// from a catalog: which of several authenticated models is "cheap" depends on
// the plan behind it, and a wrong guess spends the user's money silently.
type ModelRole string

const (
	// RoleDefault is normal interactive work: the session's own model. It is
	// not stored in modelRoles — the model picker owns that choice — so work
	// routed to it follows the session, and a hand-written modelRoles.default
	// is honoured only when it is resolved explicitly.
	RoleDefault ModelRole = "default"

	// RoleSmol is fast, inexpensive utility work: the permission judge, the
	// autopilot judge, hook completions and the self-learning review.
	RoleSmol ModelRole = "smol"

	// RoleSlow is thorough review where extra latency is acceptable: the
	// advisor's review of a completed turn.
	RoleSlow ModelRole = "slow"

	// RoleTask is general subagent work with no specialist definition.
	RoleTask ModelRole = "task"

	// RolePlan is Plan Mode's architect: the model that investigates and
	// proposes a plan while the session is read-only.
	RolePlan ModelRole = "plan"

	// RoleDesigner is interface and visual work, used by the bundled designer
	// agent.
	RoleDesigner ModelRole = "designer"
)

// ModelInherit is the reference that means "the session model", the same
// spelling a subagent's model field uses. An empty reference means the same.
const ModelInherit = "inherit"

// RoleSpec describes a role for the pickers and for the definitions that
// consume it.
type RoleSpec struct {
	Name ModelRole
	// Description is the one line shown beside the role in the model hub.
	Description string
	// Consumer names the work routed through it, so a user choosing a model
	// knows what it will actually answer.
	Consumer string
}

// roleCatalog is the built-in set in display order. Roles are added here only
// when san already has work to route through them: a slot nothing consumes is a
// setting that changes nothing.
var roleCatalog = []RoleSpec{
	{RoleDefault, "Normal interactive work and the session's own model.", "the main conversation's turns"},
	{RoleSmol, "Fast, inexpensive utility work.", "the permission judge, autopilot judge, hook completions and the self-learning review"},
	{RoleSlow, "Thorough review where extra latency is acceptable.", "the advisor's review of completed turns"},
	{RoleTask, "General subagent work with no specialist defined.", "the default worker the Agent tool spawns"},
	{RolePlan, "Plan-mode architecture and read-only investigation.", "turns while Plan Mode is on"},
	{RoleDesigner, "Interface and visual work.", "the bundled designer agent"},
}

// RoleCatalog returns the built-in roles in display order. The caller owns the
// slice; every mutation of the catalog goes through this package.
func RoleCatalog() []RoleSpec {
	return append([]RoleSpec(nil), roleCatalog...)
}

// RoleSpecFor returns the built-in role named role. Custom roles — names an
// agent definition refers to and a user assigned — are valid references but have
// no spec: their consumer is whichever definition named them.
func RoleSpecFor(role string) (RoleSpec, bool) {
	name := strings.ToLower(strings.TrimSpace(role))
	for _, spec := range roleCatalog {
		if string(spec.Name) == name {
			return spec, true
		}
	}
	return RoleSpec{}, false
}

// IsRoleName reports whether name can be a role name: lowercase letters, digits
// and hyphens, starting with a letter. Case is folded so "@Slow" and "slow"
// name one role; a role name is never a model id, which keeps the two
// namespaces from colliding.
func IsRoleName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		case i > 0 && r == '-':
		default:
			return false
		}
	}
	return true
}

// roleAliasPrefix marks a reference as a role rather than a model id. "@" is
// canonical; a lone model id never starts with it.
const roleAliasPrefix = "@"

// maxRoleAliasDepth bounds alias chains such as review -> "@slow" -> "@task".
// A cycle resolves to the session model instead of looping.
const maxRoleAliasDepth = 8

// ParseRoleAlias reports whether ref names a role and returns its name, so
// "@slow" reads as "slow". A ref without the prefix is not a role.
func ParseRoleAlias(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, roleAliasPrefix) {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ref, roleAliasPrefix)))
	if !IsRoleName(name) {
		return "", false
	}
	return name, true
}

// RoleBindings is a user's modelRoles map: role name to the model reference
// that fills it. A missing key means the role is unassigned and follows the
// session model.
type RoleBindings map[string]string

// Bindings wraps a settings modelRoles map as role bindings. It shares the
// map's storage: callers pass a copy (Settings.ModelRoles owns one) rather than
// a live settings field, so the resolver never reads a map the UI is editing.
func Bindings(roles map[string]string) RoleBindings { return RoleBindings(roles) }

// Ref returns the reference bound to role, trimmed, or "" when the role is
// unassigned. The map is hand-edited in settings.json, so a key written as
// "Slow" still fills the slow role: role names are one namespace, not two
// spellings of one namespace.
func (b RoleBindings) Ref(role string) string {
	if len(b) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(role))
	if name == "" {
		return ""
	}
	if ref, ok := b[name]; ok {
		return strings.TrimSpace(ref)
	}
	for key, ref := range b {
		if strings.ToLower(strings.TrimSpace(key)) == name {
			return strings.TrimSpace(ref)
		}
	}
	return ""
}

// RoleProviderResolver resolves a connected vendor to its provider, the way a
// subagent's "vendor/model" override already does. *ProviderPool implements it;
// a nil resolver simply cannot honour vendor-qualified references.
type RoleProviderResolver interface {
	Resolve(ctx context.Context, vendor ProviderID) (Provider, error)
}

// RoleTarget is one resolved reference: the provider and model that should
// answer the work, plus where the choice came from.
type RoleTarget struct {
	Provider Provider
	ModelID  string
	// Role is the role the reference named, "" when the caller passed a plain
	// model reference.
	Role ModelRole
	// Ref is the configured reference that selected the model; "" when the
	// session model answered.
	Ref string
	// Inherited reports that the session model answered — the role is
	// unassigned, the reference was empty or "inherit", or it pointed at a
	// vendor that is not connected.
	Inherited bool
}

// RoleResolver turns role names and model references into the provider and
// model that answer the work. It holds no settings: the caller passes the
// bindings, so one resolver serves the live settings and a test's fixed map
// alike.
type RoleResolver struct {
	providers RoleProviderResolver
}

// NewRoleResolver builds a resolver over a provider pool. providers may be nil,
// in which case vendor-qualified references fall back to the session model.
func NewRoleResolver(providers RoleProviderResolver) *RoleResolver {
	return &RoleResolver{providers: providers}
}

// NewStoreRoleResolver builds a resolver over a store's configured providers.
func NewStoreRoleResolver(store *Store) *RoleResolver {
	return NewRoleResolver(NewProviderPool(store))
}

// Resolve answers the model for role. An unassigned role follows the session
// model; an assigned one is resolved through its reference, including a
// reference that is itself another role.
func (r *RoleResolver) Resolve(ctx context.Context, bindings RoleBindings, role string, fallback Provider, fallbackModel string) RoleTarget {
	name := strings.ToLower(strings.TrimSpace(role))
	target := r.resolve(ctx, bindings, bindings.Ref(name), fallback, fallbackModel, 0)
	target.Role = ModelRole(name)
	return target
}

// ResolveRef answers the model for a raw reference, the form an agent
// definition's model field holds: "@role", "vendor/model", a bare model id, or
// ""/"inherit" for the session model.
func (r *RoleResolver) ResolveRef(ctx context.Context, ref string, fallback Provider, fallbackModel string) RoleTarget {
	return r.resolve(ctx, nil, ref, fallback, fallbackModel, 0)
}

// resolve is the one implementation behind Resolve and ResolveRef. bindings is
// nil for a raw reference, which is how a nested alias stops: a plain reference
// has no roles to expand.
func (r *RoleResolver) resolve(ctx context.Context, bindings RoleBindings, ref string, fallback Provider, fallbackModel string, depth int) RoleTarget {
	inherited := RoleTarget{Provider: fallback, ModelID: strings.TrimSpace(fallbackModel), Inherited: true}

	ref = strings.TrimSpace(ref)
	if ref == "" || strings.EqualFold(ref, ModelInherit) {
		return inherited
	}

	// A reference that reads as an alias but names no role is a typo, not a
	// model id: a bare "@" or "@Slow Role" would otherwise reach the provider as
	// a model name and fail the turn.
	if strings.HasPrefix(ref, roleAliasPrefix) {
		role, ok := ParseRoleAlias(ref)
		if !ok || depth >= maxRoleAliasDepth {
			return inherited
		}
		target := r.resolve(ctx, bindings, bindings.Ref(role), fallback, fallbackModel, depth+1)
		target.Role = ModelRole(role)
		return target
	}

	var providers RoleProviderResolver
	if r != nil {
		providers = r.providers
	}

	// A vendor-qualified reference routes to that vendor; an unconnected one
	// falls back rather than failing the work, matching the subagent executor.
	if vendor, model, ok := ParseVendorModel(ref); ok {
		if providers != nil {
			if p, err := providers.Resolve(ctx, vendor); err == nil && p != nil {
				return RoleTarget{Provider: p, ModelID: model, Ref: ref}
			}
		}
		return inherited
	}

	// A bare id stays on the session provider. A model that provider lacks
	// fails its first request, where the caller can retry on the session model.
	return RoleTarget{Provider: fallback, ModelID: ref, Ref: ref}
}
