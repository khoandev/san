package app

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool"
)

// retryBannerModel is the shell a failed attempt lands in: enough services for
// the conv handlers to commit and report, nothing more.
func retryBannerModel() *model {
	m := fixedComposerModel(80, 24)
	m.services.Agent = &agent.Runtime{}
	m.services.Tool = tool.NewRegistry()
	m.env.SessionPermissions = setting.NewSessionPermissions()
	m.pendingDecisions = &sync.Map{}
	return m
}

func rateLimitError() error {
	return &ai.Error{Kind: ai.KindRateLimit, Status: 429, Message: "slow down", RetryAfter: 7 * time.Second}
}

// A failed attempt the provider says to repeat is announced in the view, with
// the provider's own words and the wait it asked for, and stops being
// announced the moment the attempt it promised starts.
func TestRetryBannerShowsTheProvidersWaitUntilTheRetryStarts(t *testing.T) {
	m := retryBannerModel()
	conv.Update(m, &m.conv, conv.AgentOutboxMsg{Event: sdkagent.InferenceStart{Attempt: 1}})
	if cmd, _ := conv.Update(m, &m.conv, conv.AgentOutboxMsg{Event: sdkagent.InferenceEnd{Attempt: 1, Err: rateLimitError()}}); cmd == nil {
		t.Fatal("a retryable failure scheduled no redraw for its wait")
	}

	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"slow down", "retrying in", "(attempt 2)"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("view is missing %q while the retry waits:\n%s", want, frame)
		}
	}

	conv.Update(m, &m.conv, conv.AgentOutboxMsg{Event: sdkagent.InferenceStart{Attempt: 2}})
	frame = ansi.Strip(m.View().Content)
	if strings.Contains(frame, "slow down") || strings.Contains(frame, "retrying in") {
		t.Fatalf("view still announces the wait while the retry runs:\n%s", frame)
	}
}

// The row counts down on its own: each tick rearms the next one while the wait
// lasts, and a tick that lands after the row is gone ends the chain.
func TestRetryBannerTickRearmsOnlyWhileTheWaitLasts(t *testing.T) {
	m := retryBannerModel()
	if cmd, ok := conv.Update(m, &m.conv, conv.RetryTickMsg{}); !ok || cmd != nil {
		t.Fatalf("a stray tick returned (%v, %v), want it consumed and ended", cmd, ok)
	}

	conv.Update(m, &m.conv, conv.AgentOutboxMsg{Event: sdkagent.InferenceEnd{Attempt: 1, Err: rateLimitError()}})
	if cmd, ok := conv.Update(m, &m.conv, conv.RetryTickMsg{}); !ok || cmd == nil {
		t.Fatalf("a tick during the wait returned (%v, %v), want the next one", cmd, ok)
	}

	conv.Update(m, &m.conv, conv.AgentOutboxMsg{Event: sdkagent.InferenceStart{Attempt: 2}})
	if cmd, ok := conv.Update(m, &m.conv, conv.RetryTickMsg{}); !ok || cmd != nil {
		t.Fatalf("a tick after the wait returned (%v, %v), want the chain ended", cmd, ok)
	}
}

// A failure no retry can fix belongs in the transcript, not in a live row that
// would promise an attempt nobody is making.
func TestRetryBannerIgnoresFailuresARetryCannotFix(t *testing.T) {
	m := retryBannerModel()
	conv.Update(m, &m.conv, conv.AgentOutboxMsg{Event: sdkagent.InferenceEnd{
		Attempt: 1,
		Err:     &ai.Error{Kind: ai.KindAuth, Status: 401, Message: "bad key"},
	}})

	if frame := ansi.Strip(m.View().Content); strings.Contains(frame, "bad key") {
		t.Fatalf("an auth failure was announced as a retry:\n%s", frame)
	}
}
