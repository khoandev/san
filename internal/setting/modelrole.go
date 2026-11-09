package setting

import (
	"strings"

	"github.com/genai-io/san/internal/core"
)

// foldSettings applies the migrations that keep a hand-written settings file
// routing after a setting moves: retired role names fold onto the rung that
// answers their work, and the advisor's old per-agent model slot moves onto
// agents.roles. Every read and every write runs it, so a file written by an
// older san keeps working and a save rewrites the legacy spelling.
func foldSettings(d *Data) {
	foldModelRoles(d)
	foldAgentRoles(d)
}

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

// foldAgentRoles migrates the advisor's retired per-agent model slot onto
// agents.roles and normalizes the agent→role map.
//
// advisor.model written as "@<role>" names a routing slot, not a model: the
// agents block is where a role is assigned now, so the entry moves there and
// the old key is cleared. A concrete reference — "vendor/model" or a bare id —
// is left alone, because it pins a model rather than naming a role, and only
// the advisor has ever accepted one.
func foldAgentRoles(d *Data) {
	if role, ok := agentRoleRef(d.Advisor.Model); ok {
		// An entry already assigned wins, however it is spelled; the retired
		// slot only fills the gap.
		if d.AgentRole(AgentAdvisor) == "" {
			if d.Agents.Roles == nil {
				d.Agents.Roles = make(map[string]string)
			}
			d.Agents.Roles[AgentAdvisor] = role
		}
		d.Advisor.Model = ""
	}
	d.Agents.Roles = foldAgentRoleMap(d.Agents.Roles)
}

// agentRoleRef reads an "@<role>" reference as the role it names. A bare name
// is not a role reference here: agent definitions spell aliases with the "@"
// prefix, and a bare id is a model id.
func agentRoleRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "@") {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ref, "@")))
	if !ValidModelRoleName(name) {
		return "", false
	}
	return core.CanonicalModelRole(name), true
}

// foldAgentRoleMap normalizes one agent→role map: keys are lowercased agent
// names, values are lowercased role names with a redundant "@" dropped and a
// retired name folded onto the rung that answers its work — the same fold
// modelRoles takes, so both maps name the same roles. An entry that cannot be
// a role is dropped, exactly as foldRoleMap drops one.
func foldAgentRoleMap(entries map[string]string) map[string]string {
	if len(entries) == 0 {
		return entries
	}
	folded := make(map[string]string, len(entries))
	for name, value := range entries {
		agent := strings.ToLower(strings.TrimSpace(name))
		role := strings.ToLower(strings.TrimSpace(value))
		role = strings.TrimSpace(strings.TrimPrefix(role, "@"))
		if agent == "" || !ValidModelRoleName(role) {
			continue
		}
		folded[agent] = core.CanonicalModelRole(role)
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
