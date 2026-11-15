package conv

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
)

// retryRuntime answers the two terminal callbacks the clear-on-end cases use.
type retryRuntime struct{ streamRuntime }

func (*retryRuntime) OnTurnEnd(core.Result) tea.Cmd { return nil }
func (*retryRuntime) OnAgentStop(error) tea.Cmd     { return nil }

func rateLimited(hint time.Duration) error {
	return &ai.Error{Kind: ai.KindRateLimit, Status: 429, Message: "slow down", RetryAfter: hint}
}

// The row carries the provider's own words and the wait it asked for, counted
// down from the moment the attempt failed, so a retry in flight reads as one.
func TestRetryWaitCountsDownTheProvidersHint(t *testing.T) {
	now := time.Now()
	var wait RetryWait
	if !wait.Begin(rateLimited(7*time.Second), 1, now) {
		t.Fatal("a 429 was not announced")
	}

	line := stripANSI(wait.Line(120, now.Add(2*time.Second)))
	for _, want := range []string{"slow down", "retrying in 5s", "(attempt 2)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q is missing %q", line, want)
		}
	}
}

// A provider that gave no hint still gets a row: the attempt failed, and the
// next one is coming, which is what the silence would otherwise hide.
func TestRetryWaitWithoutAHintStillAnnouncesTheRetry(t *testing.T) {
	now := time.Now()
	var wait RetryWait
	wait.Begin(rateLimited(0), 2, now)

	line := stripANSI(wait.Line(120, now))
	if !strings.Contains(line, "retrying (attempt 3)") {
		t.Fatalf("line = %q, want a retry with its attempt and no invented wait", line)
	}
}

// A hint already spent is not a countdown to a past moment.
func TestRetryWaitStopsCountingOnceTheHintIsSpent(t *testing.T) {
	now := time.Now()
	var wait RetryWait
	wait.Begin(rateLimited(3*time.Second), 1, now)

	line := stripANSI(wait.Line(120, now.Add(10*time.Second)))
	if strings.Contains(line, "retrying in") {
		t.Fatalf("line = %q, want no countdown once the wait is over", line)
	}
	if !strings.Contains(line, "retrying") {
		t.Fatalf("line = %q, want the retry still announced", line)
	}
}

// Only a failure the provider calls transient is announced: a rejected key or
// a canceled turn has no attempt coming, and promising one would be a lie.
func TestRetryWaitIgnoresErrorsARetryCannotFix(t *testing.T) {
	for _, err := range []error{
		&ai.Error{Kind: ai.KindAuth, Status: 401, Message: "bad key"},
		&ai.Error{Kind: ai.KindInvalidRequest, Status: 400, Message: "bad request"},
		context.Canceled,
		errors.New("provider unavailable"),
	} {
		var wait RetryWait
		if wait.Begin(err, 1, time.Now()) {
			t.Fatalf("%v was announced as a retry", err)
		}
		if wait.Active() || wait.Line(120, time.Now()) != "" {
			t.Fatalf("%v left a row behind", err)
		}
	}

	var cutoff RetryWait
	if !cutoff.Begin(io.ErrUnexpectedEOF, 1, time.Now()) {
		t.Fatal("a mid-stream cutoff was not announced as retryable")
	}
}

// The row belongs to the gap between two attempts: the attempt that replaces
// the failed one clears it, so the retry never shows over its own answer.
func TestRetryWaitClearsWhenTheNextAttemptStarts(t *testing.T) {
	m, rt := NewModel(80), &retryRuntime{}
	applyAgentEvent(rt, &m, sdkagent.InferenceStart{Attempt: 1})
	applyAgentEvent(rt, &m, sdkagent.InferenceEnd{Attempt: 1, Err: rateLimited(5 * time.Second), Response: &ai.Response{}})
	if !m.Retry.Active() {
		t.Fatal("a failed attempt left no row to announce its retry")
	}

	applyAgentEvent(rt, &m, sdkagent.InferenceStart{Attempt: 2})
	if m.Retry.Active() || m.Retry.Line(120, time.Now()) != "" {
		t.Fatal("the retry started but the row it announced stayed up")
	}
}

// Whatever ends the turn ends the wait with it: a turn that finished and an
// agent that stopped both leave nothing for a retry to replace.
func TestRetryWaitClearsWhenTheTurnEnds(t *testing.T) {
	for name, end := range map[string]core.Event{
		"turn ended":    core.TurnEnded{},
		"agent stopped": core.AgentStopped{},
	} {
		t.Run(name, func(t *testing.T) {
			m, rt := NewModel(80), &retryRuntime{}
			m.Retry.Begin(rateLimited(5*time.Second), 1, time.Now())

			handleAgentEvent(rt, &m, end)
			if m.Retry.Active() || m.Retry.Line(120, time.Now()) != "" {
				t.Fatal("the row outlived the turn it was waiting on")
			}
		})
	}
}
