package llm

import (
	"context"
	"strings"

	"github.com/genai-io/san/internal/core"
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
// Resolution has one rule and no fallback chain:
//
//   - A role is one rung of a three-step ladder — fast, standard, deep — plus
//     default, which is the session's own model, and advisor, the slot reserved
//     for the background reviewer. An unassigned role uses the session model. A
//     reference that cannot be resolved — an unknown role, a vendor that is not
//     connected — uses the session model too. Work never fails because
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
//
// Retired per-agent role names still route: LegacyRoleAlias maps smol, explore
// and librarian onto fast, task and designer onto standard, and plan, slow and
// reviewer onto deep, so a settings file or an agent definition written before
// the ladder existed keeps resolving. A name that is not in the catalog and not
// retired stays a custom role: an agent definition may still declare its own.
type ModelRole string

const (
	// RoleDefault is normal interactive work: the session's own model. It is
	// not stored in modelRoles — the model picker owns that choice — so work
	// routed to it follows the session, and a hand-written modelRoles.default
	// is honoured only when it is resolved explicitly.
	RoleDefault ModelRole = "default"

	// RoleFast is fast, inexpensive work: work triage, the permission and
	// autopilot judges, hook completions, the self-learning review, and the
	// quick and explore agents. It is the rung a simple task lands on.
	RoleFast ModelRole = "fast"

	// RoleStandard is general subagent work with a clear definition of done:
	// the task agent and standard-tier delegation.
	RoleStandard ModelRole = "standard"

	// RoleDeep is thorough work where extra latency pays: Plan Mode's
	// architect, the reviewer agent and deep-tier delegation. Unassigned, the
	// session's own model answers, which is the strongest model the user
	// already chose.
	RoleDeep ModelRole = "deep"

	// RoleAdvisor is the background reviewer's own slot: the model that
	// inspects each finished turn. It is a role like the rungs, so the model a
	// review runs on is bound in the models hub and nowhere else. Whether the
	// review runs at all is the advisor's enable switch in /settings, not a
	// routing choice — an unassigned advisor still reviews, on the session
	// model.
	RoleAdvisor ModelRole = "advisor"

	// RoleSmol is a retired name for RoleFast.
	// Deprecated: use RoleFast; LegacyRoleAlias still routes it.
	RoleSmol ModelRole = "smol"

	// RoleSlow is a retired name for RoleDeep.
	// Deprecated: use RoleDeep; LegacyRoleAlias still routes it.
	RoleSlow ModelRole = "slow"

	// RoleTask is a retired name for RoleStandard.
	// Deprecated: use RoleStandard; LegacyRoleAlias still routes it.
	RoleTask ModelRole = "task"

	// RolePlan is a retired name for RoleDeep.
	// Deprecated: use RoleDeep; LegacyRoleAlias still routes it.
	RolePlan ModelRole = "plan"

	// RoleExplore is a retired name for RoleFast.
	// Deprecated: use RoleFast; LegacyRoleAlias still routes it.
	RoleExplore ModelRole = "explore"

	// RoleLibrarian is a retired name for RoleFast.
	// Deprecated: use RoleFast; LegacyRoleAlias still routes it.
	RoleLibrarian ModelRole = "librarian"

	// RoleReviewer is a retired name for RoleDeep.
	// Deprecated: use RoleDeep; LegacyRoleAlias still routes it.
	RoleReviewer ModelRole = "reviewer"

	// RoleDesigner is a retired name for RoleStandard.
	// Deprecated: use RoleStandard; LegacyRoleAlias still routes it.
	RoleDesigner ModelRole = "designer"
)

// LegacyRoleAlias returns the role that took over a retired name, or the
// trimmed, lowercased name unchanged when it is not retired. The table lives in
// core so settings can fold the same names without importing this package; it
// is the compatibility promise of the collapse from per-agent slots to a
// three-step ladder, so a settings file and an agent definition written before
// the ladder keep routing instead of silently falling back to the session
// model.
func LegacyRoleAlias(name string) string { return core.CanonicalModelRole(name) }

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

// roleCatalog is the built-in set in display order: the session's own model,
// then the ladder from cheapest work to hardest, then the advisor. Roles are
// added here only when san already has work to route through them: a slot
// nothing consumes is a setting that changes nothing. Every rung is a slot a
// user tunes, and a retired per-agent name is folded onto one of them by
// LegacyRoleAlias rather than occupying a row of its own.
var roleCatalog = []RoleSpec{
	{Name: RoleDefault, Description: "Normal interactive work and the session's own model.", Consumer: "the main conversation's turns"},
	{Name: RoleFast, Description: "Fast, inexpensive work: triage, judges, utilities and simple delegated tasks.", Consumer: "work triage, the permission and autopilot judges, hook completions, the self-learning review, and the quick and explore agents"},
	{Name: RoleStandard, Description: "General subagent work with a clear definition of done.", Consumer: "the bundled task agent and standard-tier delegation"},
	{Name: RoleDeep, Description: "Thorough work where extra latency pays: planning, review and hard tasks.", Consumer: "Plan Mode's architect, the bundled reviewer agent and deep-tier delegation"},
	// The advisor closes the list: its slot answers one thing, and it is the
	// only role with a switch of its own in /settings.
	{Name: RoleAdvisor, Description: "The background reviewer's model: it inspects each finished turn and may add one note.", Consumer: "the advisor, when it is switched on in /settings"},
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
// think for it. A role missing from both maps is unassigned and the session
// model answers.
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
// unassigned.
func (b RoleBindings) Ref(role string) string { return RoleBinding(b.Refs, role) }

// Effort returns the effort bound to role, trimmed, or "" when the role
// answers at its model's default.
func (b RoleBindings) Effort(role string) string { return RoleBinding(b.Efforts, role) }

// RoleBinding reads one role's value from a hand-edited settings map. It is the
// single lookup rule for modelRoles, modelRoleEfforts and the Roles tab: the
// name asked for, then the rung a retired name folds onto, then the retired
// siblings of that rung in core's inheritance order. A canonical key therefore
// wins over a retired one, while asking for "explore" still honours an
// "explore" entry before a shared "smol". "" means the role is unassigned.
func RoleBinding(entries map[string]string, role string) string {
	if len(entries) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(role))
	if name == "" {
		return ""
	}
	if value, ok := foldLookup(entries, name); ok {
		return value
	}
	rung := LegacyRoleAlias(name)
	if rung != name {
		if value, ok := foldLookup(entries, rung); ok {
			return value
		}
	}
	for _, retired := range core.RetiredModelRoles() {
		if retired.Role != rung {
			continue
		}
		if value, ok := foldLookup(entries, retired.Name); ok {
			return value
		}
	}
	return ""
}

// foldLookup reads one key from a hand-edited map, folding case and trimming.
func foldLookup(entries map[string]string, name string) (string, bool) {
	if value, ok := entries[name]; ok {
		return strings.TrimSpace(value), true
	}
	for key, value := range entries {
		if strings.ToLower(strings.TrimSpace(key)) == name {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
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

// Resolve answers the model and effort for role. An unassigned role uses the
// session model; an assigned one is resolved through its reference, including a
// reference that is itself another role. The returned Role names the rung that
// answered, so a retired name reports the slot it folded onto.
func (r *RoleResolver) Resolve(ctx context.Context, bindings RoleBindings, role string, fallback Provider, fallbackModel string) RoleTarget {
	target := r.resolveRole(ctx, bindings, role, fallback, fallbackModel, 0)
	target.Role = ModelRole(LegacyRoleAlias(role))
	return target
}

// resolveRole answers role and applies the effort that belongs to it. The role
// is asked for under the name the caller used, so a binding written under that
// name still wins; RoleBinding then folds a retired name onto its rung. depth
// bounds an "@role" chain so a hand-edited cycle resolves to the session model
// instead of looping.
func (r *RoleResolver) resolveRole(ctx context.Context, bindings RoleBindings, role string, fallback Provider, fallbackModel string, depth int) RoleTarget {
	inherited := RoleTarget{Provider: fallback, ModelID: strings.TrimSpace(fallbackModel), Inherited: true}
	if depth >= maxRoleAliasDepth {
		return inherited
	}
	target := r.resolve(ctx, bindings, bindings.Ref(role), fallback, fallbackModel, depth)
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
		// A nested alias is another role, so it follows the same alias and
		// effort rules as the caller's own role.
		role = LegacyRoleAlias(role)
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
