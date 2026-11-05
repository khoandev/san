package app

// The /advisor panel's app side: the command that opens it, the snapshot it
// renders, and the routing of the messages it emits. The panel itself refuses
// a reasonless dismissal; the app is what has to record a decision in both the
// store and the transcript, turn a finding into a task, and re-run a review
// without losing the verdict it supersedes.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/todo"
)

// panelFinding is one finding as a review records it: severity, a place, the
// evidence and the fix, so every pane of the detail view has something to show.
func panelFinding(severity advisor.Severity, title string) advisor.Finding {
	return advisor.Finding{
		Severity:   severity,
		Title:      title,
		Detail:     "The detail behind the finding.",
		File:       "internal/api.go",
		Line:       42,
		Evidence:   "log(token)",
		Impact:     "Anyone with the logs can impersonate the service.",
		Suggestion: "Drop the log line.",
	}
}

// panelReview is one review in the history, with the status the default policy
// gives its findings: the app never invents a verdict the domain would not.
func panelReview(id string, kind advisor.TargetKind, findings ...advisor.Finding) advisor.Review {
	review := advisor.Review{
		ID:        id,
		Trigger:   advisor.TriggerUserRequest,
		Target:    advisor.Target{Kind: kind, ID: "target", Label: "working tree"},
		Summary:   "Review summary.",
		DiffHash:  "hash-" + id,
		Model:     "stub-model",
		StartedAt: time.Now(),
		Findings:  findings,
	}
	review.ApplyPolicy(advisor.Policy{
		BlockMerge:          advisor.SeverityP0,
		BlockTaskCompletion: advisor.SeverityP0,
		RequireConfirmation: advisor.SeverityP1,
	})
	return review
}

// newPanelModelIn is a UI model whose workspace is cwd: a live advisor, a
// conversation the panel can repaint, a task list and a real terminal size.
func newPanelModelIn(t *testing.T, cwd string) *model {
	t.Helper()
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	m.env.CWD = cwd
	m.env.Width, m.env.Height = 100, 40
	m.conv = conv.NewModel(100)
	m.services.Tracker = todo.NewStore()
	// Rendering a notice needs the subagent registry the view reads for agent
	// colours, even with no agent in this conversation.
	m.services.Subagent = subagent.NewRegistry()
	m.wireAdvisor()
	t.Cleanup(m.teardownAdvisor)
	return m
}

func newPanelModel(t *testing.T) *model { return newPanelModelIn(t, t.TempDir()) }

// seedAdvisorReview records one review in the store, as a landed review would.
func seedAdvisorReview(t *testing.T, m *model, review advisor.Review) advisor.Review {
	t.Helper()
	stored, err := m.services.Advisor.Store.Append(review)
	if err != nil {
		t.Fatalf("seed review: %v", err)
	}
	return stored
}

// lastNotice returns the newest notice the UI appended.
func lastNotice(m *model) string {
	for i := len(m.conv.Messages) - 1; i >= 0; i-- {
		if m.conv.Messages[i].Role == core.ChatNotice {
			return m.conv.Messages[i].Content
		}
	}
	return ""
}

// advisorGitWorkspace is a repository with one committed file and one
// uncommitted change, which is what a review of "the working tree" reads.
func advisorGitWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "api.go"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "--quiet")
	run("config", "user.email", "advisor@example.com")
	run("config", "user.name", "Advisor Test")
	write("one\n")
	run("add", "api.go")
	run("commit", "-m", "initial")
	write("one\ntwo\n")
	return dir
}

// With no argument /advisor opens the findings panel on the session's history;
// a target missing its id is a usage problem, never a review of a guess.
func TestAdvisorCommandOpensThePanel(t *testing.T) {
	m := newPanelModel(t)
	seedAdvisorReview(t, m, panelReview("review-1", advisor.TargetDiff, panelFinding(advisor.SeverityP0, "Leaks the token")))

	line, cmd := m.advisorCommand("")
	if line != "" || cmd != nil {
		t.Fatalf("/advisor = %q, %v; want a silent open", line, cmd)
	}
	if !m.userInput.Advisor.IsActive() {
		t.Fatal("/advisor did not open the panel")
	}
	view := m.userInput.Advisor.Render()
	for _, want := range []string{"Advisor", "Leaks the token", "P0 1", "review-1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the panel does not show %q:\n%s", want, view)
		}
	}

	for _, tc := range []struct{ args, want string }{
		{"task", "Usage: /advisor task <id>"},
		{"commit", "Usage: /advisor commit <sha>"},
		{"step", "Usage: /advisor step <id>"},
		{"file", "Usage: /advisor file <path>"},
		{"file /etc/passwd", "inside the workspace"},
		{"nonsense", "Usage"},
	} {
		if line, _ := m.advisorCommand(tc.args); !strings.Contains(line, tc.want) {
			t.Errorf("/advisor %s = %q, want %q", tc.args, line, tc.want)
		}
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 1 {
		t.Errorf("a usage problem started a review: %d reviews", got)
	}
}

// A dismissal is a recorded decision: it moves the finding in the store, lands
// in the transcript beside the review it belongs to, repaints the panel and
// says what happened. Without a reason it changes nothing.
func TestAdvisorPanelDismissUpdatesStoreAndTranscript(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(session.CreateOptions{AgentID: "main"})
	if err != nil {
		t.Fatal(err)
	}

	m := newPanelModel(t)
	m.services.Session = sess
	m.services.SessionStore = store
	stored := seedAdvisorReview(t, m, panelReview("review-1", advisor.TargetDiff, panelFinding(advisor.SeverityP0, "Leaks the token")))
	m.persistAdvisorReview(stored)
	m.showAdvisorPanel()

	// No reason: the app refuses as firmly as the panel does.
	m.handleAdvisorAction(input.AdvisorActionMsg{
		Action: input.AdvisorActionDismiss, ReviewID: stored.ID, FindingID: stored.Findings[0].ID,
	})
	if latest, _ := m.services.Advisor.Store.Latest(); latest.Findings[0].Status != advisor.FindingOpen {
		t.Fatalf("a reasonless dismissal moved the finding to %q", latest.Findings[0].Status)
	}
	if notice := lastNotice(m); !strings.Contains(notice, "reason is required") {
		t.Fatalf("notice = %q, want the refusal", notice)
	}

	m.handleAdvisorAction(input.AdvisorActionMsg{
		Action: input.AdvisorActionDismiss, ReviewID: stored.ID, FindingID: stored.Findings[0].ID,
		Note: "the token is redacted upstream",
	})

	latest, ok := m.services.Advisor.Store.Latest()
	if !ok {
		t.Fatal("the review left the history")
	}
	if latest.Findings[0].Status != advisor.FindingDismissed {
		t.Errorf("finding status = %q, want dismissed", latest.Findings[0].Status)
	}
	if !strings.Contains(latest.Findings[0].Note, "redacted upstream") {
		t.Errorf("finding note = %q, want the reason", latest.Findings[0].Note)
	}
	if view := m.userInput.Advisor.Render(); !strings.Contains(view, "dismissed") {
		t.Errorf("the open panel was not repainted:\n%s", view)
	}
	if notice := lastNotice(m); !strings.Contains(notice, "dismissed") || !strings.Contains(notice, "redacted upstream") {
		t.Errorf("notice = %q, want the decision and its reason", notice)
	}

	// The transcript carries the move as its own record, after the review.
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read(sess.ID())
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	if len(history.Advisor.Reviews) != 1 {
		t.Fatalf("transcript holds %d reviews, want 1", len(history.Advisor.Reviews))
	}
	persisted := history.Advisor.Reviews[0].Findings[0]
	if persisted.Status != advisor.FindingDismissed || !strings.Contains(persisted.Note, "redacted upstream") {
		t.Fatalf("persisted finding = %+v, want the dismissal", persisted)
	}
}

// A deferred finding needs an owner, so follow-up creates a tracker item that
// names the review and finding it came from and stands on its own.
func TestAdvisorPanelFollowUpCreatesATask(t *testing.T) {
	m := newPanelModel(t)
	stored := seedAdvisorReview(t, m, panelReview("review-1", advisor.TargetDiff, panelFinding(advisor.SeverityP2, "Slow path")))
	m.showAdvisorPanel()

	m.handleAdvisorAction(input.AdvisorActionMsg{
		Action: input.AdvisorActionFollowUp, ReviewID: stored.ID, FindingID: stored.Findings[0].ID,
	})

	items := m.services.Tracker.List()
	if len(items) != 1 {
		t.Fatalf("tracker holds %d items, want the follow-up", len(items))
	}
	item := items[0]
	if !strings.Contains(item.Subject, string(advisor.SeverityP2)) || !strings.Contains(item.Subject, "Slow path") {
		t.Errorf("subject = %q, want the severity and the finding title", item.Subject)
	}
	if item.Metadata["advisor_review_id"] != stored.ID || item.Metadata["advisor_finding_id"] != stored.Findings[0].ID {
		t.Errorf("metadata = %v, want the review and finding ids", item.Metadata)
	}
	if !strings.Contains(item.Description, "internal/api.go:42") || !strings.Contains(item.Description, stored.ID) {
		t.Errorf("description = %q, want the location and the review id", item.Description)
	}
	if notice := lastNotice(m); !strings.Contains(notice, "Follow-up added to tasks") {
		t.Errorf("notice = %q, want the follow-up confirmation", notice)
	}
}

// Re-running appends history: the review it supersedes stays exactly as it was
// recorded. Running it again on unchanged code costs nothing and says why.
func TestAdvisorPanelRerunAppendsAReview(t *testing.T) {
	m := newPanelModelIn(t, advisorGitWorkspace(t))
	old := seedAdvisorReview(t, m, panelReview("review-old", advisor.TargetDiff, panelFinding(advisor.SeverityP0, "Leaks the token")))

	m.handleAdvisorAction(input.AdvisorActionMsg{Action: input.AdvisorActionRerun, ReviewID: old.ID})
	waitAdvisorNotice(t, m.mainNotices)
	waitAdvisorIdle(t, m.services.Advisor.session)

	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 2 {
		t.Fatalf("history = %d reviews, want the old verdict and the new one", len(reviews))
	}
	if reviews[0].ID != old.ID || len(reviews[0].Findings) != 1 || reviews[0].Findings[0].Status != advisor.FindingOpen {
		t.Errorf("the re-run changed the review it superseded: %+v", reviews[0])
	}
	next := reviews[1]
	if next.ID == old.ID {
		t.Fatal("the re-run reused the old review id instead of appending")
	}
	if next.DiffHash == old.DiffHash || next.DiffHash == "" {
		t.Errorf("new review hashed %q, want the diff it actually reviewed", next.DiffHash)
	}

	// Nothing changed since: the hash answers the request without a model call.
	m.handleAdvisorAction(input.AdvisorActionMsg{Action: input.AdvisorActionRerun, ReviewID: next.ID})
	if notice := lastNotice(m); !strings.Contains(notice, "nothing changed") {
		t.Errorf("notice = %q, want the unchanged-diff refusal", notice)
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 2 {
		t.Errorf("history = %d reviews, want the unchanged diff to add none", got)
	}
}

// A merge review is read by the session's policy, and the panel states the
// recommendation in the policy's own words.
func TestAdvisorViewResolvesTheMergeRecommendation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		findings []advisor.Finding
		want     advisor.Recommendation
		rendered string
	}{
		{"clean", nil, advisor.SafeToMerge, "safe to merge"},
		{"p1", []advisor.Finding{panelFinding(advisor.SeverityP1, "No retry test")}, advisor.ReviewRequired, "review required"},
		{"p0", []advisor.Finding{panelFinding(advisor.SeverityP0, "Leaks the token")}, advisor.MergeBlocked, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newPanelModel(t)
			seedAdvisorReview(t, m, panelReview("review-merge", advisor.TargetMerge, tc.findings...))

			if got := m.advisorView().Merge; got != tc.want {
				t.Fatalf("recommendation = %q, want %q", got, tc.want)
			}
			m.showAdvisorPanel()
			if view := m.userInput.Advisor.Render(); !strings.Contains(view, tc.rendered) {
				t.Fatalf("the panel does not state %q:\n%s", tc.rendered, view)
			}
		})
	}

	// A review of something that is not a merge carries no merge verdict at all.
	m := newPanelModel(t)
	seedAdvisorReview(t, m, panelReview("review-task", advisor.TargetTask, panelFinding(advisor.SeverityP0, "Leaks the token")))
	if got := m.advisorView().Merge; got != "" {
		t.Fatalf("a task review resolved a merge recommendation: %q", got)
	}
}

// Closing the panel from the app side leaves nothing open, and an action that

// While the panel is open it owns the keyboard: the session's review is
// filtered and dismissed from there, and the prompt behind it sees nothing.
func TestAdvisorPanelTakesTheKeyboardWhenOpen(t *testing.T) {
	m := newPanelModel(t)
	seedAdvisorReview(t, m, panelReview("review-1", advisor.TargetDiff,
		panelFinding(advisor.SeverityP0, "Leaks the token"),
		panelFinding(advisor.SeverityP1, "No retry test")))
	if _, cmd := m.advisorCommand(""); cmd != nil {
		t.Fatalf("opening the panel returned a command")
	}
	if _, ok := m.activeOverlay(); !ok {
		t.Fatal("the open panel is not the active overlay")
	}

	m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	view := m.userInput.Advisor.Render()
	if !strings.Contains(view, "severity P0") || strings.Contains(view, "No retry test") {
		t.Fatalf("the keypress did not reach the panel's filter:\n%s", view)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.userInput.Advisor.IsActive() {
		t.Fatal("esc did not close the panel")
	}
	if value := m.userInput.Textarea.Value(); value != "" {
		t.Fatalf("the keys leaked into the prompt behind the panel: %q", value)
	}
}

// names a review the session no longer holds reports it instead of panicking.
func TestAdvisorPanelActionsOnAMissingReview(t *testing.T) {
	m := newPanelModel(t)
	m.showAdvisorPanel()
	for _, action := range []input.AdvisorAction{
		input.AdvisorActionDismiss, input.AdvisorActionDefer, input.AdvisorActionFix,
		input.AdvisorActionFollowUp, input.AdvisorActionRerun,
	} {
		m.handleAdvisorAction(input.AdvisorActionMsg{Action: action, ReviewID: "gone", FindingID: "gone", Note: "reason"})
	}
	if notice := lastNotice(m); notice == "" {
		t.Fatal("acting on a missing review said nothing")
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 0 {
		t.Fatalf("a missing review created %d reviews", got)
	}
	if got := len(m.services.Tracker.List()); got != 0 {
		t.Fatalf("a missing review created %d tasks", got)
	}

	m.handleAdvisorAction(input.AdvisorActionMsg{Action: input.AdvisorActionClose})
	if m.userInput.Advisor.IsActive() {
		t.Fatal("close left the panel open")
	}
}
