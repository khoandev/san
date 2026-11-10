package app

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"

	"github.com/genai-io/san/internal/llm"
)

// advisorTestProvider is a provider whose client answers from a scripted
// driver, so a review runs its real fork without a network or a TUI.
type advisorTestProvider struct{ driver *aitest.Driver }

func (p advisorTestProvider) Client(string, map[string]string) (*ai.Client, error) {
	return p.driver.Client(), nil
}
func (p advisorTestProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (p advisorTestProvider) Name() string                                        { return "aitest" }

// One review spends one inference request; the session counter must record it
// under the advisor role so the status bar can show it.
func TestRunAdvisorTurnCountsTheReview(t *testing.T) {
	m := &model{services: services{
		Usage:            NewAgentUsage(),
		AdvisorIndicator: NewAdvisorIndicator(),
	}}
	live := &atomic.Bool{}
	live.Store(true)
	s := &advisorSession{
		provider: advisorTestProvider{aitest.New(aitest.Says("NO_ADVICE"))},
		modelID:  "aitest",
		ctx:      context.Background(),
		live:     live,
	}

	m.runAdvisorTurn(s, "User: implement the retry loop")

	got := m.services.Usage.Snapshot()
	if len(got) != 1 || got[0].Name != "advisor" || got[0].Count != 1 {
		t.Fatalf("usage = %+v, want one advisor request", got)
	}
	if s.busy.Load() {
		t.Fatal("review slot still busy after the run returned")
	}
}
