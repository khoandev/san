// The /settings Advisor tab's app side: applying a saved config to the live
// session and rewiring the advisor the save invalidated.
//
// The panel owns the form and the file write. What is left here is the part
// that touches session state: env.Advisor is what the per-turn trigger reads,
// so a save has to land there — and, when it changed whether or how the advisor
// runs, the session's advisor is rebuilt from it.
package app

import (
	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/log"
)

// handleAdvisorConfigSaved applies a saved Advisor config. A save has already
// written its file, so the live config follows the merged result, and an
// advisor built from the old config is rebuilt before it can review anything
// else.
func (m *model) handleAdvisorConfigSaved(msg input.AdvisorConfigSavedMsg) tea.Cmd {
	// The panel already persisted; refresh the in-memory handle so the merged
	// config below sees the saved file.
	if m.services.Setting != nil {
		if err := m.services.Setting.Reload(m.env.CWD); err != nil {
			log.Logger().Warn("reload settings after advisor save failed", zap.Error(err))
		}
		m.env.Advisor = m.services.Setting.Advisor()
	}

	// A save can turn the advisor on, off or onto another model. syncAdvisor
	// rewires only on that drift and tears down before rebuilding, so it also
	// cancels an in-flight review that the save invalidated. Before the agent
	// starts there is nothing to review; that path wires from the same settings.
	if m.services.Advisor != nil || (m.services.Agent != nil && m.services.Agent.Active()) {
		m.syncAdvisor()
	}

	m.conv.AddNotice("Advisor config saved (" + string(msg.Scope) + ")")
	return nil
}
