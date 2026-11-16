// The provider-failure dialog. A turn that dies on a failure a retry can still
// fix — a 429, an overloaded backend, a dropped connection — raises a docked
// modal instead of ending silently in the transcript: it reports what the
// provider said, how long it asked to wait, and offers the two answers that
// matter. Close leaves the turn dead and the runtime stopped; Restart runs the
// same turn again on the conversation the session already holds, so nothing is
// retyped and no second user message is created.
//
// Nothing waits behind the dialog: the first failure of that kind ends the
// turn, so the wait the provider asked for is the one the dialog shows.
//
// AutoPilot never sees the dialog: an unattended run recovers through its own
// backoff (autopilotRecoverCmd), with no hand to press a button.
package app

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
)

// openRetryDialog raises the dialog for a failed turn a retry could still save.
// False when nothing opened: no failure, a failure no retry fixes (a rejected
// credential, a prompt past the context window), or a copilot driving the run.
func (m *model) openRetryDialog(err error) bool {
	if err == nil || !ai.IsRetryable(err) || m.autopilotEngaged() {
		return false
	}
	m.userInput.RateLimit.Show(retryRequest(err), m.env.Width, m.env.Height)
	return true
}

// retryRequest names the failure the way the dialog shows it: the provider's
// category as the heading, its own wording as the detail, its wait as the hint.
func retryRequest(err error) input.RateLimitRequest {
	return input.RateLimitRequest{
		Title:  retryTitle(err),
		Detail: err.Error(),
		Hint:   ai.RetryAfter(err),
	}
}

// retryTitle is the heading for each failure category. IsRetryable admits
// exactly the kinds named here, so the fallback is the transport failure a
// stream reports without a typed error.
func retryTitle(err error) string {
	switch {
	case ai.IsKind(err, ai.KindRateLimit):
		return "Provider rate limit"
	case ai.IsKind(err, ai.KindOverloaded):
		return "Provider overloaded"
	}
	return "Connection lost"
}

// handleRateLimitChoice answers the dialog.
//
// Close only closes it: the failed-turn notice stays in the transcript, the
// runtime stays stopped, and the session carries on from the next message the
// way any idle session does. Restart starts the runtime again and asks the
// agent to re-run the exchange that failed — the provider sees the same
// conversation, so the turn resumes where it died.
func (m *model) handleRateLimitChoice(msg input.RateLimitChoiceMsg) tea.Cmd {
	m.userInput.RateLimit.Hide()
	if !msg.Restart {
		return nil
	}
	start, err := m.ensureAgentRuntime()
	if err != nil {
		m.conv.AddNotice("Failed to start agent: " + err.Error())
		return tea.Batch(m.CommitMessages()...)
	}
	if err := m.services.Agent.Retry(); err != nil {
		m.conv.AddNotice("Could not retry the failed turn: " + err.Error())
		return tea.Batch(append([]tea.Cmd{start}, m.CommitMessages()...)...)
	}
	m.conv.AddNotice("Retrying the failed turn")
	cmds := append([]tea.Cmd{start}, m.CommitMessages()...)
	// The retried exchange announces itself with InferenceStart, which starts the
	// stream. The wait before that first event is the wait a submitted message
	// shows, so it is drawn the same way.
	if !m.conv.Stream.Active {
		m.conv.Stream = conv.StreamState{Active: true, Waiting: true, StartedAt: time.Now()}
		cmds = append(cmds, m.conv.Spinner.Tick)
	}
	return tea.Batch(cmds...)
}
