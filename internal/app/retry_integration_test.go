package app

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
)

// rateLimitedTurn is a provider that fails the way one under load does: the
// call that ends the turn is the 429 with the wait it asks for — nothing
// replays it behind the person's back — and everything after the person clicks
// Restart answers.
func rateLimitedTurn() *aitest.Driver {
	return aitest.New(
		aitest.Fails(&ai.Error{Kind: ai.KindRateLimit, Status: 429, Message: "slow down", RetryAfter: 3 * time.Second}),
		aitest.Says("the retried turn finished"),
		aitest.Says("the queued message finished"),
	)
}

// retryTurnModel is a live session: an existing conversation, a runtime started
// the way the app starts one, and a provider that will kill the next turn. The
// command it returns is the outbox listener the runtime started with — without
// it no event ever reaches the app.
func retryTurnModel(t *testing.T, provider llm.Provider) (*model, tea.Cmd) {
	t.Helper()
	m := retryDialogModel(t)
	m.env.LLMProvider = provider
	listen, err := m.ensureAgentRuntime()
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	t.Cleanup(func() { stopRuntime(t, m.services.Agent) })
	return m, listen
}

// pumpUntil runs the model's commands and feeds what the agent emits back
// through Update — the loop bubbletea runs in production — until cond holds.
func pumpUntil(t *testing.T, m *model, first tea.Cmd, what string, cond func() bool) {
	t.Helper()
	msgs := make(chan tea.Msg, 256)
	reloadQueueCommands(first, msgs)
	deadline := time.After(30 * time.Second)
	for !cond() {
		select {
		case msg := <-msgs:
			_, cmd := m.Update(msg)
			reloadQueueCommands(cmd, msgs)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// userMessages is the user half of one inference request, in order.
func userMessages(req *ai.Request) []string {
	var out []string
	for _, msg := range req.Messages {
		if msg.Role == ai.RoleUser {
			out = append(out, msg.Text())
		}
	}
	return out
}

// failedTurn drives one submitted message until the dialog opens, and returns
// the requests the provider saw before that.
func failedTurn(t *testing.T, m *model, driver *aitest.Driver, listen tea.Cmd) []*ai.Request {
	t.Helper()
	start := m.SubmitToAgent(core.UserMessage("fix the retry loop", nil))
	pumpUntil(t, m, tea.Batch(listen, start), "the provider failure dialog", func() bool {
		return m.userInput.RateLimit.IsActive()
	})
	return driver.Sent()
}

// A 429 that ends the turn raises the dialog with the provider's wait, and
// Restart really re-runs the turn: the next inference carries the same
// conversation, so nothing is retyped and no user message is added.
func TestRateLimitRetryRunsTheFailedTurnAgain(t *testing.T) {
	driver := rateLimitedTurn()
	m, listen := retryTurnModel(t, acpTestProvider{driver.Client()})

	sent := failedTurn(t, m, driver, listen)
	// One call, not three: nothing replays the failure behind the person's
	// back. The dialog is where the wait belongs.
	if len(sent) != 1 {
		t.Fatalf("provider calls before the dialog = %d, want 1", len(sent))
	}
	frame := ansi.Strip(m.userInput.RateLimit.Render())
	for _, want := range []string{"Provider rate limit", "429", "3s"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("dialog is missing %q:\n%s", want, frame)
		}
	}
	before := userMessages(sent[len(sent)-1])
	if want := []string{"summarize the repo", "fix the retry loop"}; !slices.Equal(before, want) {
		t.Fatalf("failed turn sent %v, want %v", before, want)
	}

	attempts := len(sent)
	restart := m.handleRateLimitChoice(input.RateLimitChoiceMsg{Restart: true})
	pumpUntil(t, m, restart, "the retried turn to finish", func() bool {
		return len(driver.Sent()) > attempts && !m.conv.Stream.Active &&
			strings.Contains(transcript(m), "the retried turn finished")
	})

	after := userMessages(driver.Last())
	if !slices.Equal(after, before) {
		t.Fatalf("the retried turn sent %v, want the same conversation %v", after, before)
	}
	if got := countUsers(m.services.Agent.Messages()); got != len(before) {
		t.Fatalf("the retried turn left %d user messages, want %d", got, len(before))
	}
}

// Close leaves everything where the failure put it: the runtime stays down, no
// inference follows, and the notice the transcript already carries is intact.
func TestRateLimitRetryCloseKeepsTheTurnDead(t *testing.T) {
	driver := rateLimitedTurn()
	m, listen := retryTurnModel(t, acpTestProvider{driver.Client()})

	failedTurn(t, m, driver, listen)
	calls := driver.Calls()

	m.handleRateLimitChoice(input.RateLimitChoiceMsg{})
	if m.userInput.RateLimit.IsActive() {
		t.Fatal("the dialog stayed open")
	}
	if m.services.Agent.Active() {
		t.Fatal("closing the dialog restarted the runtime")
	}
	time.Sleep(200 * time.Millisecond)
	if got := driver.Calls(); got != calls {
		t.Fatalf("closing the dialog ran %d more inferences", got-calls)
	}
	if !strings.Contains(transcript(m), "Turn failed; output is incomplete") {
		t.Fatalf("the failure notice is gone:\n%s", transcript(m))
	}
}

// A message typed while the failing turn was in flight waits its turn: the
// restart re-runs the failed exchange first, and the queued message reaches the
// provider once, with no copy left over from the dead turn.
func TestRateLimitRetryDeliversQueuedInputOnce(t *testing.T) {
	driver := rateLimitedTurn()
	m, listen := retryTurnModel(t, acpTestProvider{driver.Client()})

	// Submit, then queue behind it, exactly as a person typing during a turn
	// does: the stream is already active, so the second message waits.
	start := tea.Batch(listen, m.SubmitToAgent(core.UserMessage("fix the retry loop", nil)))
	m.SubmitToAgent(core.UserMessage("and rerun the tests", nil))
	pumpUntil(t, m, start, "the provider failure dialog", func() bool {
		return m.userInput.RateLimit.IsActive()
	})

	attempts := len(driver.Sent())
	pumpUntil(t, m, m.handleRateLimitChoice(input.RateLimitChoiceMsg{Restart: true}),
		"the queued message to be delivered", func() bool {
			sent := driver.Sent()
			return len(sent) > attempts && strings.Contains(strings.Join(userMessages(sent[len(sent)-1]), "\n"), "and rerun the tests")
		})

	for i, req := range driver.Sent() {
		users := userMessages(req)
		if n := strings.Count(strings.Join(users, "\n"), "and rerun the tests"); n > 1 {
			t.Fatalf("request %d carries the queued message %d times: %v", i, n, users)
		}
	}
	want := []string{"summarize the repo", "fix the retry loop", "and rerun the tests"}
	if got := userMessages(driver.Last()); !slices.Equal(got, want) {
		t.Fatalf("the queued message reached the provider as %v, want %v", got, want)
	}
}

// countUsers is how many user turns a conversation holds.
func countUsers(msgs []core.Message) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role == ai.RoleUser {
			n++
		}
	}
	return n
}
