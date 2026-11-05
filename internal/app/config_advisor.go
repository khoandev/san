// The /settings Advisor tab's app side: applying a saved config to the live
// session, persisting the session pin, rewiring a runner the save invalidated,
// and reporting the values another settings level still supplies.
//
// The panel owns the form and the file write. What is left here is the part
// that touches session state: env.Advisor is what every trigger, the status
// badge and the merge gate actually read, and the transcript record is what
// makes a session-level pin survive a resume.
package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/setting"
)

// handleAdvisorConfigSaved applies a saved Advisor config. A user/project save
// has already written its file, so the live config follows the merged result;
// a session save wrote no file and becomes this conversation's pin. Either way
// the transcript gets the state patch, and a runner built from the old config
// is rebuilt before it can review anything else.
func (m *model) handleAdvisorConfigSaved(msg input.AdvisorConfigSavedMsg) tea.Cmd {
	// The signature is taken before the config changes: a save that edits
	// neither the model nor the bounds (the display filter, say) must not tear
	// down a review that is already running.
	before := m.advisorSignature()

	// The panel already persisted; refresh the in-memory handle so the merge
	// below (and any later reader) sees the saved file.
	if m.services.Setting != nil {
		if err := m.services.Setting.Reload(m.env.CWD); err != nil {
			log.Logger().Warn("reload settings after advisor save failed", zap.Error(err))
		}
	}

	switch msg.Scope {
	case input.AdvisorScopeSession:
		// No file: this conversation runs with the pin, and the state patch
		// below is what restores it on resume.
		m.env.Advisor = msg.Config.Clone()
	default:
		// The other level may still supply fields the saved block left unset,
		// so the effective config is the merged one, not the saved copy.
		m.env.Advisor = m.services.Setting.Advisor()
	}

	if err := m.appendSessionState(); err != nil {
		log.Logger().Warn("persist advisor config in the session failed", zap.Error(err))
	}

	agentActive := m.services.Agent != nil && m.services.Agent.Active()
	if m.advisorSignature() != before && (agentActive || m.services.Advisor.session != nil) {
		// wireAdvisor tears down first, so this also cancels an in-flight
		// review when the save turned the advisor off or replaced its model.
		m.wireAdvisor()
	}

	m.conv.AddNotice("Advisor config saved (" + string(msg.Scope) + ")")
	if msg.Scope != input.AdvisorScopeSession {
		// The warning is the newer news: it has to be the last line, or a
		// "saved" confirmation reads as if the behavior changed.
		m.notifyAdvisorOverride(msg)
	}
	return nil
}

// advisorGroupCheck is one section of the Advisor panel and the fields it owns.
// Comparing the picked fields with Equal compares the behavior the advisor
// actually has — tri-state defaults and bounded numbers resolved the same way
// the panel shows them — rather than the JSON spelling.
type advisorGroupCheck struct {
	label string
	pick  func(setting.AdvisorSettings) setting.AdvisorSettings
}

// advisorGroupChecks mirrors the panel's sections. A group whose saved fields
// differ from the effective ones is a group another level still supplies.
var advisorGroupChecks = []advisorGroupCheck{
	{"on/off", func(a setting.AdvisorSettings) setting.AdvisorSettings {
		return setting.AdvisorSettings{Enable: a.Enable, RunInBackground: a.RunInBackground}
	}},
	{"triggers", func(a setting.AdvisorSettings) setting.AdvisorSettings {
		return setting.AdvisorSettings{Triggers: a.Triggers}
	}},
	{"block policy", func(a setting.AdvisorSettings) setting.AdvisorSettings {
		return setting.AdvisorSettings{Blocks: a.Blocks}
	}},
	{"bounds", func(a setting.AdvisorSettings) setting.AdvisorSettings {
		return setting.AdvisorSettings{Thresholds: a.Thresholds}
	}},
	{"review", func(a setting.AdvisorSettings) setting.AdvisorSettings {
		return setting.AdvisorSettings{Review: a.Review}
	}},
	{"model & resources", func(a setting.AdvisorSettings) setting.AdvisorSettings {
		return setting.AdvisorSettings{Model: a.Model}
	}},
	{"display", func(a setting.AdvisorSettings) setting.AdvisorSettings {
		return setting.AdvisorSettings{Display: a.Display}
	}},
}

// advisorOverriddenGroups names the panel sections where the saved value and
// the effective value disagree.
func advisorOverriddenGroups(saved, effective setting.AdvisorSettings) []string {
	var groups []string
	for _, check := range advisorGroupChecks {
		if !check.pick(saved).Equal(check.pick(effective)) {
			groups = append(groups, check.label)
		}
	}
	return groups
}

// notifyAdvisorOverride says when a save was partly overridden by the other
// settings level, instead of leaving the user with a "saved" notice for a
// behavior that did not change.
func (m *model) notifyAdvisorOverride(msg input.AdvisorConfigSavedMsg) {
	snap := m.services.Setting.Snapshot()
	if snap == nil {
		return
	}
	groups := advisorOverriddenGroups(msg.Config, snap.Advisor)
	if len(groups) == 0 {
		return
	}
	other := "project"
	if msg.Scope == input.AdvisorScopeProject {
		other = "user"
	}
	m.conv.AddNotice("Note: advisor " + strings.Join(groups, ", ") +
		" comes from " + other + "-level settings")
}
