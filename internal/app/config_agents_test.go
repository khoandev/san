// The /settings agents tab's app side: a change made there reaches the live
// session through one path — the settings service is reloaded, env.Advisor
// follows the merged file, and an advisor built from the old config is rebuilt.
//
// The tab only says whether a review runs. Which model answers it is the
// "advisor" model role, bound in /models, so a role change arrives through the
// turn path's signature check rather than through this tab.
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

// agentTabModel builds a model whose settings are backed by real files in
// work, with the advisor role bound to haiku so a wired review has a model to
// name.
func agentTabModel(t *testing.T, work string) *model {
	t.Helper()
	if err := setting.UpdateModelRolesAt(map[string]string{setting.AgentAdvisor: "haiku"}, setting.ScopeUser); err != nil {
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

// testAgentTabHome prepares isolated home and workspace directories and returns
// the workspace path.
func testAgentTabHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)
	if err := setting.TrustWorkspace(work); err != nil {
		t.Fatal(err)
	}
	return work
}

// Turning the advisor on in the tab applies to the session at once: the live
// config follows the file, a review already in flight is torn down, and the new
// advisor reviews on the model the advisor role resolves to.
func TestAgentsConfigSavedWiresTheAdvisor(t *testing.T) {
	work := testAgentTabHome(t)
	m := agentTabModel(t, work)
	if m.env.Advisor.Enabled() {
		t.Fatal("the fixture started with the advisor on")
	}

	// A review already in flight before the change.
	ctx, cancel := context.WithCancel(context.Background())
	live := &atomic.Bool{}
	live.Store(true)
	m.services.Advisor = &advisorSession{ctx: ctx, cancel: cancel, live: live}

	// What the tab leaves behind on disk: the switch, and nothing else.
	on := true
	if err := setting.UpdateAdvisorAt(setting.AdvisorSettings{Enable: &on}, setting.ScopeUser); err != nil {
		t.Fatalf("UpdateAdvisorAt: %v", err)
	}

	m.handleAgentsConfigSaved(input.AgentsConfigSavedMsg{Notice: "advisor on"})

	if !m.env.Advisor.Enabled() {
		t.Fatal("env.Advisor did not follow the write")
	}
	if live.Load() {
		t.Fatal("the pre-change review session was not torn down")
	}
	session := m.services.Advisor
	if session == nil {
		t.Fatal("the enabled advisor did not wire a session")
	}
	if session.modelID != "haiku" {
		t.Fatalf("advisor model = %q, want the advisor role's haiku", session.modelID)
	}
	// The live signature follows, so the next turn does not rewire again.
	if m.agentAdvisorSignature != m.advisorSignature() {
		t.Fatalf("the live signature is stale: %q != %q", m.agentAdvisorSignature, m.advisorSignature())
	}
}

// Turning it off unwires the running review, and a second report of the same
// state does not rebuild it. Only a running session has an advisor to rebuild,
// so the fixture starts with one, as an active conversation would.
func TestAgentsConfigSavedUnwiresAndSettles(t *testing.T) {
	work := testAgentTabHome(t)
	m := agentTabModel(t, work)
	m.services.Advisor = runningAdvisorSession()
	on := true
	if err := setting.UpdateAdvisorAt(setting.AdvisorSettings{Enable: &on}, setting.ScopeUser); err != nil {
		t.Fatalf("UpdateAdvisorAt: %v", err)
	}
	m.handleAgentsConfigSaved(input.AgentsConfigSavedMsg{Notice: "advisor on"})
	session := m.services.Advisor
	if session == nil {
		t.Fatal("the advisor was not wired")
	}

	off := false
	if err := setting.UpdateAdvisorAt(setting.AdvisorSettings{Enable: &off}, setting.ScopeUser); err != nil {
		t.Fatalf("UpdateAdvisorAt: %v", err)
	}
	m.handleAgentsConfigSaved(input.AgentsConfigSavedMsg{Notice: "advisor off"})

	if m.env.Advisor.Enabled() {
		t.Fatal("env.Advisor still reports the advisor on")
	}
	if m.services.Advisor != nil {
		t.Fatal("the disabled advisor kept its session")
	}
	if session.live.Load() {
		t.Fatal("the torn-down review is still live")
	}

	// Nothing moved, so a repeat report must not rebuild anything — and with
	// the advisor off there is nothing to rebuild.
	m.handleAgentsConfigSaved(input.AgentsConfigSavedMsg{Notice: "advisor off"})
	if m.services.Advisor != nil {
		t.Fatal("a settled config wired an advisor")
	}
}

// The live config follows the file, not the message: the panel emits a notice
// only after a successful write, and the settings file stays the source of
// truth for what the session runs.
func TestAgentsConfigSavedReadsTheFileNotTheNotice(t *testing.T) {
	work := testAgentTabHome(t)
	m := agentTabModel(t, work)
	m.services.Advisor = runningAdvisorSession()
	// The file says on; a stale notice says nothing at all.
	on := true
	if err := setting.UpdateAdvisorAt(setting.AdvisorSettings{Enable: &on}, setting.ScopeUser); err != nil {
		t.Fatalf("UpdateAdvisorAt: %v", err)
	}

	m.handleAgentsConfigSaved(input.AgentsConfigSavedMsg{Notice: ""})

	if !m.env.Advisor.Enabled() {
		t.Fatal("the handler ignored the file the panel wrote")
	}
	if m.services.Advisor == nil {
		t.Fatal("a notice-less change did not wire the advisor")
	}
}

// runningAdvisorSession is a live review, the state an active conversation is
// in when the tab changes something.
func runningAdvisorSession() *advisorSession {
	ctx, cancel := context.WithCancel(context.Background())
	live := &atomic.Bool{}
	live.Store(true)
	return &advisorSession{ctx: ctx, cancel: cancel, live: live}
}
