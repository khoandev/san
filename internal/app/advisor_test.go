package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/setting"
)

// The review reads this text and nothing else, so it has to carry what
// happened — who asked, what the agent said, which tools ran — without the
// bulk of a full command output.
func TestRenderAdvisorTranscriptKeepsWhatHappened(t *testing.T) {
	msgs := []core.Message{
		core.UserMessage("fix the retry loop", nil),
		core.AssistantMessage("I will read the file.", "", []core.ToolCall{{ID: "c1", Name: "Read", Input: `{"file_path":"retry.go"}`}}),
		core.ToolResultMessage(core.ToolResult{
			ToolCallID: "c1", ToolName: "Read", Content: ai.TextContent(strings.Repeat("x", advisorToolResultBudget*3)),
		}),
		core.AssistantMessage("Done: the backoff now resets.", "", nil),
	}

	got := renderAdvisorTranscript(msgs)
	for _, want := range []string{"User: fix the retry loop", "Assistant: I will read the file.", "[tool call: Read]", "[tool result: Read]", "Assistant: Done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript is missing %q:\n%s", want, got)
		}
	}
	// One huge output must not crowd out the conversation it belongs to.
	if len(got) > advisorTranscriptBudget+advisorToolResultBudget {
		t.Fatalf("transcript = %d bytes, want it bounded", len(got))
	}
}

// The advisor's own note is not work to review: feeding the agent's reaction to
// it back into the reviewer would loop advice into advice.
func TestLastInputIsAdvisory(t *testing.T) {
	tests := []struct {
		name string
		msgs []core.Message
		want bool
	}{
		{"empty delta", nil, false},
		{"user asked", []core.Message{core.UserMessage("carry on", nil)}, false},
		{"advisor note", []core.Message{core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil)}, true},
		{"note then agent work", []core.Message{
			core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil),
			core.AssistantMessage("Fixed the nil case.", "", nil),
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastInputIsAdvisory(tt.msgs); got != tt.want {
				t.Fatalf("lastInputIsAdvisory = %v, want %v", got, tt.want)
			}
		})
	}
}

// The note is shown as one notice line, so a multi-line note must collapse to
// its first real sentence rather than stretch the conversation.
func TestAdvisorNoteLine(t *testing.T) {
	if got := advisorNoteLine("\n\n- the test asserts the old behaviour\n- second point"); got != "the test asserts the old behaviour" {
		t.Fatalf("note line = %q", got)
	}
	if got := advisorNoteLine(""); got != "note" {
		t.Fatalf("empty note line = %q, want a placeholder", got)
	}
	long := advisorNoteLine(strings.Repeat("á", 400))
	if len(long) > 120+len("…") || !strings.HasSuffix(long, "…") {
		t.Fatalf("long note line = %q, want it clipped", long)
	}
}

// clipText cuts on a rune boundary: half a rune in the transcript would make
// the whole review request invalid.
func TestClipTextCutsOnRuneBoundary(t *testing.T) {
	got := clipText(strings.Repeat("á", 10), 5)
	if !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("clipText = %q, want a clipped rune-aligned string", got)
	}
	if len(got) > 5+len("…") {
		t.Fatalf("clipText = %q, want it no longer than the budget", got)
	}
	if got := clipText("short", 100); got != "short" {
		t.Fatalf("clipText shortened text that fits: %q", got)
	}
}

// reviewModel builds the least a finished turn needs: a conversation to review
// and an advisor session that refuses to start a review, so the trigger's own
// bookkeeping is what the test observes.
func reviewModel(t *testing.T, msgs []core.Message, s *advisorSession) *model {
	t.Helper()
	m := &model{}
	m.services.Agent = &agent.Runtime{}
	m.services.Agent.SetMessages(msgs)
	m.services.Advisor = s
	return m
}

func liveAdvisorSession() (*advisorSession, *atomic.Bool) {
	live := &atomic.Bool{}
	live.Store(true)
	return &advisorSession{live: live, ctx: context.Background()}, live
}

// A review runs only for a turn that ended on its own terms: a cancelled or
// cut-short turn says nothing about the work, and reviewing it would spend
// model calls on half a thought.
func TestReviewCompletedTurnOnlyForFinishedTurns(t *testing.T) {
	s, _ := liveAdvisorSession()
	s.busy.Store(true) // Keeps the trigger from starting a real review.
	m := reviewModel(t, []core.Message{core.AssistantMessage("done", "", nil)}, s)

	m.reviewCompletedTurn(core.Result{StopReason: core.StopCanceled})
	if s.reviewed != 0 {
		t.Fatalf("a cancelled turn moved the cursor to %d", s.reviewed)
	}
}

// The cursor follows the conversation even when the review is skipped, so a
// turn the advisor was too busy for is dropped rather than replayed later.
func TestReviewCompletedTurnAdvancesCursorWhileBusy(t *testing.T) {
	s, _ := liveAdvisorSession()
	s.busy.Store(true)
	msgs := []core.Message{core.UserMessage("fix it", nil), core.AssistantMessage("fixed", "", nil)}
	m := reviewModel(t, msgs, s)

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	if s.reviewed != len(msgs) {
		t.Fatalf("cursor = %d, want %d", s.reviewed, len(msgs))
	}
}

// A turn that only carried the advisor's own note is not worth reviewing: the
// agent's reaction to it would be advice about advice.
func TestReviewCompletedTurnSkipsTheAdvisorsOwnNote(t *testing.T) {
	s, _ := liveAdvisorSession()
	msgs := []core.Message{
		core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil),
		core.AssistantMessage("Checked it.", "", nil),
	}
	m := reviewModel(t, msgs, s)

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	if s.reviewed != len(msgs) {
		t.Fatalf("cursor = %d, want %d", s.reviewed, len(msgs))
	}
	if s.busy.Load() {
		t.Fatal("the note's own turn started a review")
	}
}

// A disabled advisor is the zero session, so the per-turn trigger must be a nil
// check rather than a settings read that could panic before the agent starts.
func TestReviewCompletedTurnWithoutAdvisor(t *testing.T) {
	m := reviewModel(t, []core.Message{core.AssistantMessage("done", "", nil)}, nil)
	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
}

// Off costs nothing: no session, no goroutine, no model call.
func TestWireAdvisorBuildsNothingWhenDisabled(t *testing.T) {
	m := &model{}
	m.wireAdvisor()
	if m.services.Advisor != nil {
		t.Fatal("a disabled advisor was wired")
	}
}

// Enabled with nothing connected is a configuration the person can create, and
// it must not arm a session that would fail every turn.
func TestWireAdvisorNeedsAModel(t *testing.T) {
	on := true
	m := &model{}
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.wireAdvisor()
	if m.services.Advisor != nil {
		t.Fatal("an advisor with no connected model was wired")
	}
}

// Teardown is what cancels an in-flight review: the session context is the only
// handle the review goroutine has on the session that started it.
func TestTeardownAdvisorCancelsTheSessionContext(t *testing.T) {
	m := &model{}
	ctx, cancel := context.WithCancel(context.Background())
	live := &atomic.Bool{}
	live.Store(true)
	m.services.Advisor = &advisorSession{ctx: ctx, cancel: cancel, live: live}

	m.teardownAdvisor()
	if ctx.Err() == nil {
		t.Fatal("teardown did not cancel the session context")
	}
	if live.Load() || m.services.Advisor != nil {
		t.Fatal("teardown left the session reachable")
	}
	m.teardownAdvisor() // Idempotent: a second teardown has nothing to do.
}

// /advisor [on|off|status] pins the session without touching settings.json, and
// reports what the layer is actually doing.
func TestAdvisorCommandTogglesTheSession(t *testing.T) {
	m := &model{}

	line, _ := m.advisorCommand("status")
	if line != "advisor · off" {
		t.Fatalf("status = %q, want the advisor off", line)
	}

	line, _ = m.advisorCommand("on")
	if line == "" || !m.env.Advisor.Enabled() {
		t.Fatalf("on = %q, want the session's config enabled", line)
	}
	line, _ = m.advisorCommand("")
	if !strings.Contains(line, "no model connected") {
		t.Fatalf("status after on = %q, want the missing model reported", line)
	}

	m.advisorCommand("off")
	if m.env.Advisor.Enabled() || m.env.Advisor.Enable == nil {
		t.Fatalf("off = %#v, want an explicit session disable", m.env.Advisor)
	}
	if line, _ := m.advisorCommand("bogus"); !strings.HasPrefix(line, "Usage:") {
		t.Fatalf("usage = %q", line)
	}
}
