package app

import (
	"strings"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/triage"
	"go.uber.org/zap"
)

// installAutoTier gives an Agent adapter the brief classifier, when the feature
// is on. One inference per unnamed delegation is the whole cost, and the model
// that runs it comes from a fallback chain: the fast role, then the standard
// role, then the session's own model. A fresh install usually has no fast role
// bound, so requiring one would silently leave every delegation running the
// tier its definition declared — the very case routing exists to fix.
//
// Routing narrows, never widens: the classifier answers with a definition and a
// rung, and the executor still refuses an edit charter in a read-only session.
func installAutoTier(adapter *subagent.ExecutorAdapter, registry *subagent.Registry, enabled bool, resolve subagent.RoleResolver, session llm.Provider, sessionModel string) {
	if !enabled || adapter == nil || resolve == nil {
		return
	}
	provider, model, effort, role := classifierBinding(resolve, session, sessionModel)
	if provider == nil || strings.TrimSpace(model) == "" {
		log.Logger().Debug("automatic tier routing is off: no model to classify with",
			zap.String("setting", "agents.autoTier"),
			zap.String("modelRole", string(llm.RoleFast)),
			zap.String("fallbackModelRole", string(llm.RoleStandard)))
		return
	}
	log.Logger().Debug("automatic tier routing",
		zap.String("setting", "agents.autoTier"),
		zap.String("modelRole", role),
		zap.String("model", model))
	judge := triage.New(provider, model, registry)
	judge.SetThinkingEffort(effort)
	adapter.SetRouter(judge)
}

// classifierBinding picks the model one classification runs on: the fast role,
// else the standard role, else the session's own model. The effort bound to a
// role travels with its model; the session fallback has no role, so it keeps
// the model's default effort. role names the rung that answered ("fast",
// "standard" or "session") for the log line, and is empty when nothing did.
func classifierBinding(resolve subagent.RoleResolver, session llm.Provider, sessionModel string) (llm.Provider, string, string, string) {
	for _, role := range []llm.ModelRole{llm.RoleFast, llm.RoleStandard} {
		assignment := resolve(string(role))
		if assignment.Assigned && assignment.Provider != nil && strings.TrimSpace(assignment.ModelID) != "" {
			return assignment.Provider, assignment.ModelID, assignment.Effort, string(role)
		}
	}
	if session != nil && strings.TrimSpace(sessionModel) != "" {
		return session, sessionModel, "", "session"
	}
	return nil, "", "", ""
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
