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
//   - An unassigned role follows its catalog fallback role (explore and
//     librarian follow smol, reviewer follows slow), and falls through to the
//     session's own model when that fallback is unassigned too. A reference
//     that cannot be resolved — an unknown role, a vendor that is not
//     connected — uses the session model. Work never fails because
//     configuration is incomplete or stale.
//   - An assigned reference wins over the session model. A bare model id stays
//     on the session's provider; "vendor/model" routes to that connected vendor.
//   - A role's effort travels with whichever model answered it: the effort
//     bound to the role the caller named wins, else the effort of the role that
//     supplied the model. The caller validates it against that model's ladder
//     (ResolveTargetEffort), because only the live catalog knows its rungs.
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

	// RoleExplore is read-only codebase investigation, used by the bundled
	// explore agent. Unassigned, it follows the smol role, which is the model
	// that answered investigation before this slot existed.
	RoleExplore ModelRole = "explore"

	// RoleLibrarian is upstream documentation and API research, used by the
	// bundled librarian agent. Unassigned, it follows the smol role.
	RoleLibrarian ModelRole = "librarian"

	// RoleReviewer is independent review of a diff or a design, used by the
	// bundled reviewer agent. Unassigned, it follows the slow role.
	RoleReviewer ModelRole = "reviewer"

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
	// Fallback is the role an unassigned role follows, so a new per-agent slot
	// does not change behaviour for users who never bind it: explore and
	// librarian fall back to smol, reviewer to slow. "" means the session model
	// answers, which is what default and the utility roles do.
	Fallback ModelRole
}

// roleCatalog is the built-in set in display order. Roles are added here only
// when san already has work to route through them: a slot nothing consumes is a
// setting that changes nothing. The per-agent slots come first after the
// session's own model, because they are what a user tunes; the utility roles
// they fall back to come last.
var roleCatalog = []RoleSpec{
	{Name: RoleDefault, Description: "Normal interactive work and the session's own model.", Consumer: "the main conversation's turns"},
	{Name: RolePlan, Description: "Plan-mode architecture and read-only investigation.", Consumer: "turns while Plan Mode is on"},
	{Name: RoleTask, Description: "General subagent work with no specialist defined.", Consumer: "the default worker the Agent tool spawns"},
	{Name: RoleExplore, Description: "Fast read-only codebase investigation.", Consumer: "the bundled explore agent, which investigates before the main conversation works", Fallback: RoleSmol},
	{Name: RoleLibrarian, Description: "Read-only upstream documentation and API research.", Consumer: "the bundled librarian agent, which reads vendor docs and changelogs", Fallback: RoleSmol},
	{Name: RoleReviewer, Description: "Thorough independent review of a diff or a design.", Consumer: "the bundled reviewer agent, which reviews without editing", Fallback: RoleSlow},
	{Name: RoleDesigner, Description: "Interface and visual work.", Consumer: "the bundled designer agent"},
	{Name: RoleSmol, Description: "Fast, inexpensive utility work.", Consumer: "the permission judge, autopilot judge, hook completions and the self-learning review"},
	{Name: RoleSlow, Description: "Thorough review where extra latency is acceptable.", Consumer: "the advisor's review of a completed turn, and the reviewer agent while its own slot is unbound"},
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

// RoleBindings is a user's routing configuration: modelRoles decides which
// model fills a role, modelRoleEfforts decides how hard that model is asked to
// think for it. A role missing from both maps is unassigned and follows its
// catalog fallback, then the session model.
type RoleBindings struct {
	// Refs is role name to the model reference that fills it.
	Refs map[string]string
	// Efforts is role name to a provider-native effort label. A role without
	// an entry answers at its model's own default.
	Efforts map[string]string
}

// Bindings wraps a settings modelRoles map as role bindings. It shares the
// map's storage: callers pass a copy (Settings.ModelRoles owns one) rather than
// a live settings field, so the resolver never reads a map the UI is editing.
func Bindings(roles map[string]string) RoleBindings { return RoleBindings{Refs: roles} }

// BindingsOf wraps both settings maps, the form every caller with live settings
// should use so a role's depth travels with its model.
func BindingsOf(roles, efforts map[string]string) RoleBindings {
	return RoleBindings{Refs: roles, Efforts: efforts}
}

// Ref returns the reference bound to role, trimmed, or "" when the role is
// unassigned. The map is hand-edited in settings.json, so a key written as
// "Slow" still fills the slow role: role names are one namespace, not two
// spellings of one namespace.
func (b RoleBindings) Ref(role string) string { return b.lookup(b.Refs, role) }

// Effort returns the effort bound to role, trimmed, or "" when the role
// answers at its model's default. The lookup folds case exactly like Ref.
func (b RoleBindings) Effort(role string) string { return b.lookup(b.Efforts, role) }

// lookup reads one role from a hand-edited map, folding the key's case.
func (b RoleBindings) lookup(entries map[string]string, role string) string {
	if len(entries) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(role))
	if name == "" {
		return ""
	}
	if value, ok := entries[name]; ok {
		return strings.TrimSpace(value)
	}
	for key, value := range entries {
		if strings.ToLower(strings.TrimSpace(key)) == name {
			return strings.TrimSpace(value)
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
	// Effort is the reasoning effort bound to the role, unvalidated: only the
	// live model catalog knows which rungs a model offers, so the caller
	// validates it with ResolveTargetEffort before sending it. "" means the
	// model's own default.
	Effort string
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

// Resolve answers the model and effort for role. An unassigned role follows its
// catalog fallback role, then the session model; an assigned one is resolved
// through its reference, including a reference that is itself another role.
func (r *RoleResolver) Resolve(ctx context.Context, bindings RoleBindings, role string, fallback Provider, fallbackModel string) RoleTarget {
	name := strings.ToLower(strings.TrimSpace(role))
	target := r.resolveRole(ctx, bindings, name, fallback, fallbackModel, 0)
	target.Role = ModelRole(name)
	return target
}

// resolveRole answers role, walking the catalog fallback chain when nothing is
// bound to it, and applies the effort that belongs to the role the caller
// named. depth bounds both an "@role" chain and a fallback chain so a
// hand-edited cycle resolves to the session model instead of looping.
func (r *RoleResolver) resolveRole(ctx context.Context, bindings RoleBindings, role string, fallback Provider, fallbackModel string, depth int) RoleTarget {
	inherited := RoleTarget{Provider: fallback, ModelID: strings.TrimSpace(fallbackModel), Inherited: true}
	if depth >= maxRoleAliasDepth {
		return inherited
	}
	ref := bindings.Ref(role)
	target := r.resolve(ctx, bindings, ref, fallback, fallbackModel, depth)
	if ref == "" {
		// Nothing fills this role: follow its catalog fallback, which is how a
		// per-agent slot keeps the behaviour it had before it existed.
		if spec, ok := RoleSpecFor(role); ok && spec.Fallback != "" {
			target = r.resolveRole(ctx, bindings, string(spec.Fallback), fallback, fallbackModel, depth+1)
		}
	}
	// The effort of the role the caller named wins; otherwise the effort that
	// came with the role which supplied the model stands.
	if effort := bindings.Effort(role); effort != "" {
		target.Effort = effort
	}
	return target
}

// ResolveRef answers the model for a raw reference, the form an agent
// definition's model field holds: "@role", "vendor/model", a bare model id, or
// ""/"inherit" for the session model. A raw reference carries no bindings, so
// it never picks up an effort: there is no role of the caller's to ask.
func (r *RoleResolver) ResolveRef(ctx context.Context, ref string, fallback Provider, fallbackModel string) RoleTarget {
	return r.resolve(ctx, RoleBindings{}, ref, fallback, fallbackModel, 0)
}

// resolve is the one implementation behind Resolve and ResolveRef. bindings is
// empty for a raw reference, which is how a bare model id stops: a plain
// reference has no role to expand and no effort to carry.
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
		// A nested alias is another role, so it follows the same fallback and
		// effort rules as the caller's own role.
		target := r.resolveRole(ctx, bindings, role, fallback, fallbackModel, depth+1)
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

// ResolveTargetEffort turns a role's stored effort into the effort to send for
// the model that will answer it. model names the resolved provider, model and
// auth method — the caller already builds that for the store's cache keys (see
// the app's roleModelInfo). The stored label is a hint, not a value: it is
// validated against the live catalog, so rebinding a role to a model without
// that rung sends the model's own default instead of a label the vendor would
// reject. "" means "send no effort" — the model does not reason, or the role
// has none bound.
func ResolveTargetEffort(p Provider, store *Store, model *CurrentModelInfo, target RoleTarget) string {
	if strings.TrimSpace(target.Effort) == "" || p == nil || target.ModelID == "" {
		return ""
	}
	if model == nil || model.ModelID != target.ModelID {
		model = &CurrentModelInfo{ModelID: target.ModelID}
		if vendor, _, ok := ParseVendorModel(target.Ref); ok {
			model.Provider = vendor
		}
	}
	return ResolveThinkingEffortForModel(p, store, model, target.Effort)
}
