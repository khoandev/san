package agent

import (
	"errors"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// Retry reaches the live agent, and is refused rather than silently dropped
// when there is no runtime or when the last run has stopped. The app turns
// either refusal into a notice instead of believing a turn is on its way.
func TestRuntimeRetryRequiresALiveAgent(t *testing.T) {
	var inactive Runtime
	if err := inactive.Retry(); !errors.Is(err, ErrRuntimeInactive) {
		t.Fatalf("inactive Retry error = %v, want ErrRuntimeInactive", err)
	}

	ag := newControlledAgent(false)
	sess := sessionWithAgents(ag)
	if err := sess.Start(BuildParams{}, []core.Message{core.UserMessage("question", nil)}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-ag.release:
		default:
			close(ag.release)
		}
		stopRuntime(t, sess)
	})
	<-ag.started

	if err := sess.Retry(); err != nil {
		t.Fatalf("Retry on a live runtime: %v", err)
	}
	if n := ag.retries.Load(); n != 1 {
		t.Fatalf("agent retries = %d, want the request forwarded to the live agent", n)
	}

	// A stopped run keeps its conversation for the restart that follows, but
	// it answers no retry: the caller has to rebuild the runtime first.
	sess.mu.Lock()
	sess.run.stopped = true
	sess.mu.Unlock()
	if err := sess.Retry(); !errors.Is(err, ErrRuntimeStopped) {
		t.Fatalf("stopped Retry error = %v, want ErrRuntimeStopped", err)
	}
	if n := ag.retries.Load(); n != 1 {
		t.Fatalf("agent retries = %d, want the stopped run left alone", n)
	}
}
