package app

import (
	"strings"
	"sync"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/triage"
	"go.uber.org/zap"
)

// autoTierUnassignedOnce keeps the "bind the fast role" notice to one line per
// process: the Agent tool is reconfigured on every model, workspace and settings
// change, and a warning per rebuild would bury the run it belongs to.
var autoTierUnassignedOnce sync.Once

// installAutoTier gives an Agent adapter the brief classifier, when the feature
// is on and the fast role names a model. One inference per unnamed delegation is
// the whole cost, so an unassigned fast role installs nothing: the session model
// is not a cheap substitute, and every delegation then runs the tier its
// definition declared. Routing narrows, never widens — the classifier answers
// with a definition and a rung, and the executor still refuses an edit charter
// in a read-only session.
func installAutoTier(adapter *subagent.ExecutorAdapter, registry *subagent.Registry, enabled bool, resolve subagent.RoleResolver) {
	if !enabled || adapter == nil || resolve == nil {
		return
	}
	assignment := resolve(string(llm.RoleFast))
	if !assignment.Assigned || assignment.Provider == nil || strings.TrimSpace(assignment.ModelID) == "" {
		autoTierUnassignedOnce.Do(func() {
			log.Logger().Info("automatic tier routing needs a model in the fast role; delegations keep their declared tier",
				zap.String("setting", "agents.autoTier"),
				zap.String("modelRole", string(llm.RoleFast)))
		})
		return
	}
	judge := triage.New(assignment.Provider, assignment.ModelID, registry)
	judge.SetThinkingEffort(assignment.Effort)
	adapter.SetRouter(judge)
}

// autoTierEnabled reports whether automatic routing is on. A model built without
// a settings service follows the default: on.
func (m *model) autoTierEnabled() bool {
	return m.services.Setting == nil || m.services.Setting.AutoTier()
}

// autoTierEnabled reports whether automatic routing is on for this session. ACP
// reads the same setting the TUI does, from the settings file loaded for the
// session's working directory.
func (s *acpSession) autoTierEnabled() bool {
	return s.permissions == nil || s.permissions.settings == nil || s.permissions.settings.Agents.AutoTierOn()
}
