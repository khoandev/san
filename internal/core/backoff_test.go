package core

import (
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"

	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoffDelayFloorWins(t *testing.T) {
	// frac=0 zeroes the exponential term, so the Retry-After floor stands.
	if got := backoffDelay(1, 5*time.Second, 0); got != 5*time.Second {
		t.Fatalf("backoffDelay floor = %v, want 5s", got)
	}
}

func TestBackoffDelayGrowsAndCaps(t *testing.T) {
	// frac=1 (upper edge of full jitter): attempt n ≈ base·2^(n-1), capped.
	a1 := backoffDelay(1, 0, 1)
	a2 := backoffDelay(2, 0, 1)
	if a1 != backoffBase {
		t.Fatalf("attempt1 = %v, want %v", a1, backoffBase)
	}
	if a2 != 2*backoffBase {
		t.Fatalf("attempt2 = %v, want %v", a2, 2*backoffBase)
	}
	if capped := backoffDelay(20, 0, 1); capped != backoffMax {
		t.Fatalf("attempt20 = %v, want cap %v", capped, backoffMax)
	}
}

func TestBackoffSleepHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Use a floor large enough that, without the cancel check, the call would
	// block well past the test. A canceled ctx must return immediately.
	if err := BackoffSleep(ctx, 1, 10*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("BackoffSleep on canceled ctx = %v, want context.Canceled", err)
	}
}

// failsThenAnswers is an endpoint that is down for n calls and then works.
func failsThenAnswers(err error, n int) *aitest.Driver {
	turns := make([]aitest.Turn, 0, n+1)
	for range n {
		turns = append(turns, aitest.Fails(err))
	}
	return aitest.New(append(turns, aitest.Says("ok"))...)
}

func newRetryAgent(t *testing.T, d ai.Driver, retries int, timeout time.Duration) *agent {
	t.Helper()
	ag := NewAgent(Config{
		ID:                      "test",
		Client:                  testClient(d),
		System:                  NewSystem(),
		Tools:                   NewTools(),
		MaxTurnRetries:          retries,
		StreamFirstChunkTimeout: timeout,
		StreamIdleTimeout:       timeout,
	})
	go func() {
		for range ag.Outbox() {
		}
	}()
	a := ag.(*agent)
	a.SetMessages([]Message{UserMessage("hi", nil)})
	return a
}

// stalled is what a driver reports when a stream goes quiet: a network
// failure, which ai.IsRetryable admits.
var stalled = &ai.Error{Kind: ai.KindNetwork, Message: "stream stalled"}

// The loop replays nothing. A transient failure ends the turn on the first
// failure and reaches the person as a dialog, with the provider's own wait in
// it; a silent replay would only trade that dialog for a wait nobody sees.
func TestThinkActSurfacesTransientStreamErrorImmediately(t *testing.T) {
	llm := failsThenAnswers(stalled, 2)
	a := newRetryAgent(t, llm, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err == nil {
		t.Fatal("ThinkAct should surface the transient failure")
	}
	if llm.Calls() != 1 {
		t.Fatalf("Infer calls = %d, want 1 (no silent replay)", llm.Calls())
	}
}

// The knob still works for a caller that asks for a budget, even though no san
// run asks for one: the failure rides out instead of ending the turn.
func TestThinkActReplaysATransientFailureWhenTheBudgetAsks(t *testing.T) {
	llm := failsThenAnswers(stalled, 2)
	a := newRetryAgent(t, llm, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := a.ThinkAct(ctx)
	if err != nil {
		t.Fatalf("ThinkAct returned error after the replays: %v", err)
	}
	if result.Content != "ok" {
		t.Fatalf("content = %q, want ok", result.Content)
	}
	if llm.Calls() != 3 {
		t.Fatalf("Infer calls = %d, want 3 (2 failures + 1 success)", llm.Calls())
	}
}

func TestThinkActDoesNotRetryFatalError(t *testing.T) {
	// A 400 as the driver hands it over — typed, so ai.IsRetryable leaves it
	// fatal.
	llm := aitest.Always(aitest.Fails(&ai.Error{
		Kind: ai.KindInvalidRequest, Status: 400, Message: "bad request",
	}))
	a := newRetryAgent(t, llm, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err == nil {
		t.Fatal("ThinkAct should surface a fatal error")
	}
	if llm.Calls() != 1 {
		t.Fatalf("Infer calls = %d, want 1 (no retry on fatal)", llm.Calls())
	}
}

// A provider's 429 is the case the dialog was built for: it ends the turn at
// once, so the wait the provider asked for is the one the dialog shows.
func TestThinkActSurfacesAProviderRateLimitImmediately(t *testing.T) {
	llm := failsThenAnswers(&ai.Error{Kind: ai.KindRateLimit, Status: 429, Message: "slow down"}, 2)
	a := newRetryAgent(t, llm, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := a.ThinkAct(ctx)
	if err == nil {
		t.Fatal("ThinkAct should surface the rate limit")
	}
	if !ai.IsRetryable(err) {
		t.Fatalf("error = %v, want a retryable one", err)
	}
	if llm.Calls() != 1 {
		t.Fatalf("Infer calls = %d, want 1 (no silent replay)", llm.Calls())
	}
}

// A terminal failure the SDK could not type is a transport failure, which is
// the one place the stream rule differs from the completed-call rule.
func TestThinkActSurfacesAnOpaqueStreamFailureImmediately(t *testing.T) {
	llm := failsThenAnswers(&ai.Error{Kind: ai.KindNetwork, Message: "unexpected EOF"}, 1)
	a := newRetryAgent(t, llm, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err == nil {
		t.Fatal("ThinkAct should surface the stream failure")
	}
	if llm.Calls() != 1 {
		t.Fatalf("Infer calls = %d, want 1 (no silent replay)", llm.Calls())
	}
}

// The reactive half: an overflow the provider reported has to reach
// isPromptTooLong, or the loop retries a prompt that cannot fit instead of
// shrinking it. Deliberately not also retryable — one compaction, then the
// call goes out again.
func TestThinkActCompactsOnProviderContextOverflow(t *testing.T) {
	llm := failsThenAnswers(&ai.Error{
		Kind: ai.KindContextExceeded, Status: 400, Message: "prompt is too long",
	}, 1)
	compacted := 0
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(llm),
		System: NewSystem(),
		Tools:  NewTools(),
		CompactFunc: func(context.Context, []Message) (Compaction, error) {
			compacted++
			return Compaction{Summary: "the story so far"}, nil
		},
	}).(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()
	ag.SetMessages([]Message{
		UserMessage("one", nil), AssistantMessage("two", "", nil), UserMessage("three", nil),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := ag.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct returned error: %v", err)
	}
	if compacted != 1 {
		t.Fatalf("compactions = %d, want 1", compacted)
	}
	if llm.Calls() != 2 {
		t.Fatalf("Infer calls = %d, want 2 (overflow, compact, retry)", llm.Calls())
	}
}

func TestStreamInferIdleTimeoutEndsTheTurn(t *testing.T) {
	llm := aitest.Always(aitest.Hangs())
	a := newRetryAgent(t, llm, 0, 40*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err == nil {
		t.Fatal("ThinkAct should fail once a stalled stream times out")
	}
	if llm.Calls() != 1 { // the stalled call is not replayed
		t.Fatalf("Infer calls = %d, want 1", llm.Calls())
	}
}

// TestThinkActReturnsResultWhenRetriesAreExhausted pins the Result contract on
// the failure path. A turn that dies on a provider outage has still appended
// messages and burned tokens, and the caller persists a run from its Result —
// so returning only an error silently discards the work. subagent's
// finalizeResult is the concrete casualty: no Result means the transcript is
// never written to disk.
func TestThinkActReturnsResultWhenTheTurnFails(t *testing.T) {
	llm := aitest.Always(aitest.Fails(stalled))
	a := newRetryAgent(t, llm, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := a.ThinkAct(ctx)
	if err == nil {
		t.Fatal("ThinkAct should surface the error")
	}
	if result == nil {
		t.Fatal("ThinkAct returned nil Result on a failed turn; the caller cannot persist the run")
	}
	if result.StopReason != StopError {
		t.Errorf("StopReason = %q, want %q", result.StopReason, StopError)
	}
	if result.StopDetail == "" {
		t.Error("StopDetail is empty; it should carry the underlying failure")
	}
	if len(result.Messages) == 0 {
		t.Error("Messages is empty; the conversation is what the caller persists")
	}
}
