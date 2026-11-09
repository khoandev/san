package core

import "strings"

// A model role names the kind of work a model is chosen for: "fast" for cheap
// utility work, "standard" for a bounded change, "deep" for planning and
// review. Settings and the model router both read that vocabulary, so the
// retired per-agent names and the rung that answers each one live here once
// rather than being copied into both packages.

// RetiredModelRole is one retired role name and the rung that answers its work.
type RetiredModelRole struct {
	Name string
	Role string
}

// retiredModelRoles lists the retired names in inheritance order: when one
// settings file binds more than one retired name, the first one bound wins for
// the rung they share.
var retiredModelRoles = []RetiredModelRole{
	{Name: "smol", Role: "fast"},
	{Name: "explore", Role: "fast"},
	{Name: "librarian", Role: "fast"},
	{Name: "task", Role: "standard"},
	{Name: "designer", Role: "standard"},
	{Name: "slow", Role: "deep"},
	{Name: "plan", Role: "deep"},
	{Name: "reviewer", Role: "deep"},
}

// RetiredModelRoles returns the retired names in inheritance order. The caller
// owns the slice.
func RetiredModelRoles() []RetiredModelRole {
	return append([]RetiredModelRole(nil), retiredModelRoles...)
}

// CanonicalModelRole returns the rung a retired role name folded onto, or the
// trimmed, lowercased name unchanged when it is not retired. Case is folded so
// "@Slow" and "slow" name one rung.
func CanonicalModelRole(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, retired := range retiredModelRoles {
		if retired.Name == name {
			return retired.Role
		}
	}
	return name
}
