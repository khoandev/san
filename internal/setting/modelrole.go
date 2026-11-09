package setting

import (
	"strings"

	"github.com/genai-io/san/internal/core"
)

// foldModelRoles rewrites the retired per-agent role names — smol, plan, task
// and their siblings — onto the rung that answers their work, so what a
// settings file holds and what the pickers show is the same four roles. It runs
// on every read and every write, which is what keeps a file written before the
// collapse from silently routing its work to the session model.
func foldModelRoles(d *Data) {
	d.ModelRoles = foldRoleMap(d.ModelRoles)
	d.ModelRoleEfforts = foldRoleMap(d.ModelRoleEfforts)
}

// foldRoleMap folds one role→value map: an explicit rung key wins over a
// retired name that folds onto it, and when two retired names share a rung the
// first in core's inheritance order wins, so one file always folds the same way.
func foldRoleMap(entries map[string]string) map[string]string {
	if len(entries) == 0 {
		return entries
	}
	folded := make(map[string]string, len(entries))
	for name, value := range entries {
		key := strings.ToLower(strings.TrimSpace(name))
		if core.CanonicalModelRole(key) == key {
			folded[key] = strings.TrimSpace(value)
		}
	}
	for _, retired := range core.RetiredModelRoles() {
		if _, bound := folded[retired.Role]; bound {
			continue
		}
		for name, value := range entries {
			if strings.ToLower(strings.TrimSpace(name)) == retired.Name {
				folded[retired.Role] = strings.TrimSpace(value)
				break
			}
		}
	}
	return folded
}

// clearRoleKey drops every spelling of one role from a binding map: the
// canonical key, a case variant, and a retired name that folds onto it, so
// clearing "fast" also clears a leftover "smol" instead of letting the fold
// resurrect it on the next read.
func clearRoleKey(entries map[string]string, key string) {
	for existing := range entries {
		if core.CanonicalModelRole(existing) == key {
			delete(entries, existing)
		}
	}
}
