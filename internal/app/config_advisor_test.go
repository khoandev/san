// The app side of the /settings Advisor tab: applying a save to the live
// session, persisting the pin in the transcript, keeping the three levels
// straight, and the status badge the whole thing feeds.
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/todo"
)

// advisorConfigTestModel is a UI model that can apply an Advisor config save:
// a live advisor config, a settings handle, a conversation and a real session
// the state patch can land in.
func advisorConfigTestModel(t *testing.T, cwd string) (*model, *session.Session, *session.Store) {
	t.Helper()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(session.CreateOptions{AgentID: "main"})
	if err != nil {
		t.Fatal(err)
	}

	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	m.env.CWD = cwd
	m.env.SessionPermissions = setting.NewSessionPermissions()
	m.conv = conv.NewModel(100)
	m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "review this"}}
	m.services.Agent = &agent.Runtime{}
	m.services.Agent.SetMessages([]ai.Message{{ID: "m1", Role: ai.RoleUser, Content: ai.TextContent("review this")}})
	m.services.Setting = setting.New(setting.NewData())
	m.services.Session = sess
	m.services.SessionStore = store
	m.services.Tracker = todo.NewStore()
	return m, sess, store
}

// A session save writes no file: it pins this conversation's runtime config
// and lands in the transcript, so a resume restores the pin.
func TestAdvisorConfigSessionSavePinsRuntimeAndTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	m, sess, store := advisorConfigTestModel(t, t.TempDir())
	off := false
	m.handleAdvisorConfigSaved(input.AdvisorConfigSavedMsg{
		Scope:  input.AdvisorScopeSession,
		Config: setting.AdvisorSettings{Enable: &off},
	})

	if m.env.Advisor.Enabled() {
		t.Fatal("the session save did not reach the live config")
	}
	if got := lastNotice(m); !strings.Contains(got, "Advisor config saved (session)") {
		t.Errorf("notice = %q, want the saved confirmation", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".san", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("a session save must not write a settings file (stat err = %v)", err)
	}

	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read(sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	pinned := parseAdvisor(history.AdvisorSettings)
	if pinned.IsZero() {
		t.Fatal("the session pin was not persisted in the transcript")
	}
	if pinned.Enabled() {
		t.Fatal("the persisted pin does not match the saved config")
	}
}

// A user save writes the file and reloads the effective config; the transcript
// carries the resulting session state so a resume agrees with the file.
func TestAdvisorConfigUserSaveAppliesTheEffectiveConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cwd := t.TempDir()
	m, sess, store := advisorConfigTestModel(t, cwd)
	on := true
	// The panel writes the user file before it emits the message.
	if err := setting.UpdateAdvisorAt(setting.AdvisorSettings{Enable: &on}, setting.ScopeUser); err != nil {
		t.Fatalf("user save: %v", err)
	}

	m.handleAdvisorConfigSaved(input.AdvisorConfigSavedMsg{
		Scope:  input.AdvisorScopeUser,
		Config: setting.AdvisorSettings{Enable: &on},
	})

	if !m.env.Advisor.Enabled() {
		t.Fatal("the user save was not applied to the live config")
	}
	if got := lastNotice(m); !strings.Contains(got, "Advisor config saved (user)") {
		t.Errorf("notice = %q, want the saved confirmation", got)
	}

	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read(sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	effective := parseAdvisor(history.AdvisorSettings)
	if !effective.Enabled() {
		t.Fatal("the transcript does not reflect the saved effective config")
	}
}

// A user save that the project file overrides says so, instead of showing a
// "saved" notice for a behavior that did not change.
func TestAdvisorConfigSaveReportsAProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cwd := t.TempDir()
	t.Chdir(cwd) // UpdateAdvisorAt writes the project file under the process cwd
	t.Setenv("SAN_TRUST_WORKSPACE", "1")
	off := false
	if err := setting.UpdateAdvisorAt(setting.AdvisorSettings{Enable: &off}, setting.ScopeProject); err != nil {
		t.Fatalf("project save: %v", err)
	}
	on := true
	if err := setting.UpdateAdvisorAt(setting.AdvisorSettings{Enable: &on}, setting.ScopeUser); err != nil {
		t.Fatalf("user save: %v", err)
	}

	m, _, _ := advisorConfigTestModel(t, cwd)
	m.handleAdvisorConfigSaved(input.AdvisorConfigSavedMsg{
		Scope:  input.AdvisorScopeUser,
		Config: setting.AdvisorSettings{Enable: &on},
	})

	// The project file wins the merge, so the live config follows it.
	if m.env.Advisor.Enabled() {
		t.Fatal("the live config ignored the project-level override")
	}
	if got := lastNotice(m); !strings.Contains(got, "project-level settings") {
		t.Errorf("notice = %q, want the override warning", got)
	}
	if groups := advisorOverriddenGroups(
		setting.AdvisorSettings{Enable: &on}, setting.AdvisorSettings{Enable: &off},
	); len(groups) != 1 || groups[0] != "on/off" {
		t.Errorf("overridden groups = %v, want [on/off]", groups)
	}
}

// The badge is the status line's view of the review layer: it shows the latest
// verdict and the blocking counts, stays silent when the advisor is off or its
// display knob is off, and never puts P2/P3 on the line.
func TestAdvisorBadgeFollowsSettingsAndLatestReview(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	if badge := m.advisorBadge(); badge.Show {
		t.Errorf("a model with no review shows a badge: %+v", badge)
	}

	review := panelReview("review-badge", advisor.TargetDiff,
		panelFinding(advisor.SeverityP0, "blocking"),
		panelFinding(advisor.SeverityP1, "critical"),
		panelFinding(advisor.SeverityP2, "note"),
		panelFinding(advisor.SeverityP3, "minor"),
	)
	m.services.Advisor.Indicator.Adopt(review)

	badge := m.advisorBadge()
	if !badge.Show {
		t.Fatal("the badge stays hidden with a review to report")
	}
	if badge.Status != review.Status {
		t.Errorf("badge status = %q, want %q", badge.Status, review.Status)
	}
	if badge.P0 != 1 || badge.P1 != 1 {
		t.Errorf("badge counts P0=%d P1=%d, want 1 and 1", badge.P0, badge.P1)
	}

	// Turning the advisor off hides the badge even though the verdict stays in
	// the history.
	m.env.Advisor = setting.AdvisorSettings{}
	if badge := m.advisorBadge(); badge.Show {
		t.Errorf("a disabled advisor shows a badge: %+v", badge)
	}

	// So does turning its display knob off.
	on, off := true, false
	m.env.Advisor = setting.AdvisorSettings{
		Enable:  &on,
		Display: setting.AdvisorDisplay{StatusBar: &off},
	}
	if badge := m.advisorBadge(); badge.Show {
		t.Errorf("a badge with display.statusBar=false is showing: %+v", badge)
	}
}

// The badge is wired into the mode status line, beside the mode indicator.
func TestAdvisorBadgeIsOnTheStatusLine(t *testing.T) {
	m := fixedComposerModel(120, 24)
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "test-model"}
	m.services.Advisor = newAdvisorServices()
	on := true
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.services.Advisor.Indicator.Adopt(panelReview(
		"review-line", advisor.TargetDiff, panelFinding(advisor.SeverityP0, "blocking")))

	status := ansi.Strip(m.renderModeStatus())
	if !strings.Contains(status, "advisor · changes required") {
		t.Fatalf("status line = %q, want the advisor badge", status)
	}
	if !strings.Contains(status, "P0 1") {
		t.Fatalf("status line = %q, want the P0 count", status)
	}
}
