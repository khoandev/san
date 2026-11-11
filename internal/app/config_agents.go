// The /settings agents tab's app side: applying a change made there to the live
// session and rewiring whatever it invalidated.
//
// The panel owns the row list and the file write. What is left here is the part
// that touches session state: env.Advisor is what the per-turn trigger reads, so
// a write has to land there — and, when it changed whether or how the advisor
// runs, the session's advisor is rebuilt from it.
//
// Agent roles need no push of their own: the executor's role source and the
// /agents list both ask the settings service per call, so the reload below is
// what makes a running session answer on the new role.
package app

import (
	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/log"
)

// handleAgentsConfigSaved applies a change the agents tab already wrote to the
// user settings file, so the live config follows the merged result and an
// advisor built from the old settings is rebuilt before it can review anything
// else.
func (m *model) handleAgentsConfigSaved(msg input.AgentsConfigSavedMsg) tea.Cmd {
	// The panel already persisted; refresh the in-memory handle so the merged
	// config below sees the saved file.
	if m.services.Setting != nil {
		if err := m.services.Setting.Reload(m.env.CWD); err != nil {
			log.Logger().Warn("reload settings after agents save failed", zap.Error(err))
		}
		m.env.Advisor = m.services.Setting.Advisor()
	}

	// A write can turn the advisor on or off. syncAdvisor rewires only on that
	// drift and tears down before rebuilding, so it also cancels an in-flight
	// review the change invalidated. Before the agent starts there is nothing to
	// review; that path wires from the same settings.
	if m.services.Advisor != nil || (m.services.Agent != nil && m.services.Agent.Active()) {
		m.syncAdvisor()
	}

	if msg.Notice != "" {
		m.conv.AddNotice(msg.Notice)
	}
	return nil
}
