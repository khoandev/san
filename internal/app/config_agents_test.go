// The /settings agents tab's app side: one save reaches the live session
// through a single path — the settings service is reloaded, env.Advisor follows
// the merged file, and an advisor built from the old role is rebuilt.
package app

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// saveModel builds a model whose settings are backed by real files in home/work,
// so Reload has something to read back, with modelRoles.fast bound to haiku.
func saveModel(t *testing.T, home, work string) *model {
	t.Helper()
	if err := setting.UpdateModelRolesAt(map[string]string{"fast": "haiku"}, setting.ScopeUser); err != nil {
		t.Fatalf("seed modelRoles: %v", err)
	}
	loaded, err := setting.LoadForCwd(work)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	return &model{
		env: env{
			CWD:          work,
			LLMProvider:  &switchProviderStub{name: "anthropic"},
			CurrentModel: &llm.CurrentModelInfo{ModelID: "session-model", Provider: llm.Anthropic},
		},
		services: services{LLM: &llm.Conn{}, Setting: setting.New(loaded)},
		conv:     conv.NewModel(80),
	}
}

// A save that assigns the advisor a role applies to the session at once: the
// live config follows, and the advisor reviews on the model that role resolves
// to instead of the deep default.
func TestAgentsConfigSavedRewiresTheAdvisor(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)
	if err := setting.TrustWorkspace(work); err != nil {
		t.Fatal(err)
	}

	m := saveModel(t, home, work)
	if m.env.Advisor.Enabled() {
		t.Fatal("the fixture started with the advisor on")
	}

	// A review already in flight before the save, with the signature the
	// pre-save config produced.
	ctx, cancel := context.WithCancel(context.Background())
	live := &atomic.Bool{}
	live.Store(true)
	m.services.Advisor = &advisorSession{ctx: ctx, cancel: cancel, live: live}

	// What the panel leaves behind on disk.
	on := true
	roles := map[string]string{setting.AgentAdvisor: "fast"}
	if err := setting.UpdateAgentsAt(roles, setting.AdvisorSettings{Enable: &on}, setting.ScopeUser); err != nil {
		t.Fatalf("UpdateAgentsAt: %v", err)
	}

	m.handleAgentsConfigSaved(input.AgentsConfigSavedMsg{
		Scope:   input.AgentsScopeUser,
		Advisor: setting.AdvisorSettings{Enable: &on},
		Roles:   roles,
	})

	if !m.env.Advisor.Enabled() {
		t.Fatal("env.Advisor did not follow the save")
	}
	if got := m.services.Setting.AgentRole(setting.AgentAdvisor); got != "fast" {
		t.Fatalf("live agents.roles.advisor = %q, want fast", got)
	}
	if live.Load() {
		t.Fatal("the pre-save review session was not torn down")
	}
	session := m.services.Advisor
	if session == nil {
		t.Fatal("the saved role did not wire an advisor")
	}
	if session.modelID != "haiku" {
		t.Fatalf("advisor model = %q, want the fast role's haiku", session.modelID)
	}
}

// A project-scope save lands in the workspace's file and the live settings
// service reads it back, so the next delegation picks the new role up.
func TestAgentsConfigSavedAppliesAProjectScope(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)
	if err := setting.TrustWorkspace(work); err != nil {
		t.Fatal(err)
	}

	m := saveModel(t, home, work)
	roles := map[string]string{setting.AgentTask: "deep"}
	if err := setting.UpdateAgentsAt(roles, setting.AdvisorSettings{}, setting.ScopeProject); err != nil {
		t.Fatalf("UpdateAgentsAt: %v", err)
	}

	m.handleAgentsConfigSaved(input.AgentsConfigSavedMsg{
		Scope: input.AgentsScopeProject,
		Roles: roles,
	})

	if got := m.services.Setting.AgentRole(setting.AgentTask); got != "deep" {
		t.Fatalf("live agents.roles.task = %q, want the project's deep", got)
	}
}
