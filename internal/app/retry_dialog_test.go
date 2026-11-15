package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
)

// retryDialogModel is a session with a conversation and a provider that can
// answer: the shell a dead turn leaves behind, ready for either answer. The
// extensions are the ones a runtime rebuild needs, since Restart brings the
// stopped agent back through ensureAgentRuntime.
func retryDialogModel(t *testing.T) *model {
	t.Helper()
	m, _ := resumedModel(t, []core.Message{
		core.UserMessage("summarize the repo", nil),
		core.AssistantMessage("Here is the summary.", "", nil),
	})
	e := extensionReloadModel(t, m.env.CWD)
	m.services.Skill, m.services.Command = e.services.Skill, e.services.Command
	m.services.Subagent, m.services.MCP = e.services.Subagent, e.services.MCP
	m.services.Plugin, m.services.Persona = e.services.Plugin, e.services.Persona
	m.services.Tool = tool.Default()
	m.learnedStores, m.userInput = e.learnedStores, e.userInput
	m.env.Width, m.env.Height = 100, 30
	m.env.LLMProvider = acpTestProvider{aitest.Always(aitest.Says("done")).Client()}
	return m
}

// transcript is every row the conversation is holding, notices included.
func transcript(m *model) string {
	rows := make([]string, 0, len(m.conv.Messages))
	for _, msg := range m.conv.Messages {
		rows = append(rows, msg.Content)
	}
	return strings.Join(rows, "\n")
}

func rateLimited() error {
	return &ai.Error{Kind: ai.KindRateLimit, Status: 429, Message: "slow down", RetryAfter: 7 * time.Second}
}

// A 429 that kills the turn raises the dialog with the provider's own wording
// and wait, and the transcript keeps the notice it always wrote.
func TestRateLimitDialogOpensOnARateLimitedStop(t *testing.T) {
	m := retryDialogModel(t)
	m.OnAgentStop(rateLimited())

	if !m.userInput.RateLimit.IsActive() {
		t.Fatal("a rate-limited turn did not raise the dialog")
	}
	frame := ansi.Strip(m.userInput.RateLimit.Render())
	for _, want := range []string{"Provider rate limit", "slow down", "7s", "Close", "Restart"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("dialog is missing %q:\n%s", want, frame)
		}
	}
	if !strings.Contains(transcript(m), "Turn failed; output is incomplete") {
		t.Fatalf("the persisted failure notice is gone:\n%s", transcript(m))
	}
	// It is the foreground panel, docked above the composer rather than a
	// full-screen picker: the error belongs next to the turn it killed.
	ov, active := m.activeOverlay()
	if !active || ov != &m.userInput.RateLimit {
		t.Fatalf("the dialog is not the foreground overlay: %T", ov)
	}
	if !isDockedModal(ov) {
		t.Fatal("the dialog took the full screen instead of docking")
	}
}

// The dialog owns the keyboard while it is up: the keys it advertises reach it
// through the same overlay routing every other panel uses.
func TestRateLimitDialogTakesTheKeyboard(t *testing.T) {
	m := retryDialogModel(t)
	m.OnAgentStop(rateLimited())

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.userInput.RateLimit.IsActive() {
		t.Fatal("escape did not reach the dialog")
	}
	if m.services.Agent.Active() {
		t.Fatal("closing from the keyboard started the agent")
	}
}

// An overloaded backend is the other side of the same failure: the turn died on
// something a retry fixes, so the dialog names it correctly.
func TestRateLimitDialogOpensOnAnOverloadedStop(t *testing.T) {
	m := retryDialogModel(t)
	m.OnAgentStop(&ai.Error{Kind: ai.KindOverloaded, Status: 529, Message: "overloaded_error"})

	if !m.userInput.RateLimit.IsActive() {
		t.Fatal("an overloaded turn did not raise the dialog")
	}
	if frame := ansi.Strip(m.userInput.RateLimit.Render()); !strings.Contains(frame, "Provider overloaded") {
		t.Fatalf("dialog misnames the failure:\n%s", frame)
	}
}

// A rejected credential cannot be retried away, so the turn fails the way it
// always did: a notice in the transcript and no dialog.
func TestRateLimitDialogStaysShutForAFailureARetryCannotFix(t *testing.T) {
	m := retryDialogModel(t)
	m.OnAgentStop(&ai.Error{Kind: ai.KindAuth, Status: 401, Message: "invalid api key"})

	if m.userInput.RateLimit.IsActive() {
		t.Fatal("a rejected credential raised the retry dialog")
	}
	if !strings.Contains(transcript(m), "Turn failed; output is incomplete") {
		t.Fatalf("the failure was not reported:\n%s", transcript(m))
	}
}

// Close only closes: the turn stays dead, the runtime stays stopped, and
// nothing claims a retry the person did not ask for.
func TestRateLimitDialogCloseLeavesTheTurnDead(t *testing.T) {
	m := retryDialogModel(t)
	m.OnAgentStop(rateLimited())

	if cmd := m.handleRateLimitChoice(input.RateLimitChoiceMsg{}); cmd != nil {
		t.Fatal("closing the dialog started work")
	}
	if m.userInput.RateLimit.IsActive() {
		t.Fatal("the dialog stayed open")
	}
	if m.services.Agent.Active() {
		t.Fatal("closing the dialog restarted the runtime")
	}
	if strings.Contains(transcript(m), "Retrying the failed turn") {
		t.Fatalf("closing announced a retry:\n%s", transcript(m))
	}
	if !strings.Contains(transcript(m), "Turn failed; output is incomplete") {
		t.Fatalf("the failure notice is gone:\n%s", transcript(m))
	}
}

// Restart starts the runtime again and asks it to re-run the exchange that
// failed, on the conversation the session already holds.
func TestRateLimitDialogRestartRunsTheTurnAgain(t *testing.T) {
	m := retryDialogModel(t)
	m.OnAgentStop(rateLimited())

	m.handleRateLimitChoice(input.RateLimitChoiceMsg{Restart: true})

	if m.userInput.RateLimit.IsActive() {
		t.Fatal("the dialog stayed open")
	}
	if !m.services.Agent.Active() {
		t.Fatalf("restart left the runtime down:\n%s", transcript(m))
	}
	if !strings.Contains(transcript(m), "Retrying the failed turn") {
		t.Fatalf("restart was not reported:\n%s", transcript(m))
	}
	if !m.conv.Stream.Active {
		t.Fatal("the retried turn is not marked in flight")
	}
}
