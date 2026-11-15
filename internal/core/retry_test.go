package core

import (
	"context"
	"testing"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// retryAgent starts an agent whose every turn answers "answered", with the
// outbox drained, and returns it once Run is going.
func retryAgent(t *testing.T, llm *aitest.Driver) Agent {
	t.Helper()
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(llm),
		System: NewSystem(),
		Tools:  NewTools(),
	})
	go func() {
		for range ag.Outbox() {
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ag.Run(ctx) }()
	return ag
}

// countRole is how many messages of one role the conversation holds.
func countRole(msgs []Message, role ai.Role) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role == role {
			n++
		}
	}
	return n
}

// A retry runs one exchange over the conversation as it stands: the question
// stays as it was and only a new answer joins the chain. This is the shape a
// provider failure leaves — a turn with no answer — and what the caller asks
// for when the user chooses to try again.
func TestRetryAnswersTheConversationAgain(t *testing.T) {
	llm := aitest.Always(aitest.Says("answered"))
	ag := retryAgent(t, llm)

	ag.Append(UserMessage("question", nil))
	waitFor(t, "the first answer", func() bool { return llm.Calls() == 1 })

	ag.Retry()
	waitFor(t, "the retried answer", func() bool { return llm.Calls() == 2 })

	msgs := ag.Messages()
	if n := countRole(msgs, ai.RoleUser); n != 1 {
		t.Fatalf("retry added a user message: %d in the conversation, want the original 1", n)
	}
	if len(msgs) != 3 {
		t.Fatalf("conversation = %d messages, want question plus two answers", len(msgs))
	}
}

// One retry is one exchange. A retry that never cleared would answer again
// every time the agent came back to the idle wait.
func TestRetrySpendsExactlyOneExchange(t *testing.T) {
	llm := aitest.Always(aitest.Says("answered"))
	ag := retryAgent(t, llm)

	ag.Append(UserMessage("question", nil))
	waitFor(t, "the first answer", func() bool { return llm.Calls() == 1 })

	ag.Retry()
	waitFor(t, "the retried answer", func() bool { return llm.Calls() == 2 })

	// Nothing else may start a turn: the retry latch is spent, and no message
	// was queued behind it.
	time.Sleep(50 * time.Millisecond)
	if n := llm.Calls(); n != 2 {
		t.Fatalf("inferences = %d, want exactly 2 (the answer and its retry)", n)
	}
}

// An empty conversation has no question to answer again, so the retry spends
// no inference — and leaves no latch behind for the next message to trip.
func TestRetryWithoutConversationStartsNothing(t *testing.T) {
	llm := aitest.Always(aitest.Says("answered"))
	ag := retryAgent(t, llm)

	ag.Retry()
	time.Sleep(50 * time.Millisecond)
	if n := llm.Calls(); n != 0 {
		t.Fatalf("retry on an empty conversation started %d inferences, want none", n)
	}

	ag.Append(UserMessage("question", nil))
	waitFor(t, "the answer", func() bool { return llm.Calls() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := llm.Calls(); n != 1 {
		t.Fatalf("inferences = %d, want 1: the dropped retry must not survive as a latch", n)
	}
}

// A message queued alongside a retry is one exchange, not two: the message is
// answered, and the retry it supersedes costs nothing extra.
func TestRetryIsAbsorbedByAQueuedMessage(t *testing.T) {
	llm := aitest.Always(aitest.Says("answered"))
	ag := retryAgent(t, llm)

	ag.Append(UserMessage("question", nil))
	waitFor(t, "the first answer", func() bool { return llm.Calls() == 1 })

	// The message goes first, the retry lands on top of it: whatever order the
	// loop observes them in, the queued message is what the exchange answers.
	ag.Append(UserMessage("follow-up", nil))
	ag.Retry()
	waitFor(t, "the follow-up answer", func() bool { return llm.Calls() == 2 })

	time.Sleep(50 * time.Millisecond)
	if n := llm.Calls(); n != 2 {
		t.Fatalf("inferences = %d, want 2: the queued message answers the retry too", n)
	}
	if n := countRole(ag.Messages(), ai.RoleUser); n != 2 {
		t.Fatalf("user messages = %d, want the two the caller sent", n)
	}
}
