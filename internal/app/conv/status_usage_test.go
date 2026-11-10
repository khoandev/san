package conv

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The usage segment is "icon number" per role that ran, in the order the
// snapshot supplies; an empty count or name is not rendered at all.
func TestRenderAgentUsage(t *testing.T) {
	got := stripANSI(RenderAgentUsage([]AgentUsageCount{
		{Name: "advisor", Count: 3},
		{Name: "quick", Count: 2},
		{Name: "explore", Count: 0},
		{Name: "reviewer", Count: 1},
		{Name: "custom-a", Count: 4},
		{Name: "  ", Count: 9},
	}))
	if got != "◆3 »2 ✓1 C4" {
		t.Fatalf("usage = %q, want %q", got, "◆3 »2 ✓1 C4")
	}

	if got := RenderAgentUsage(nil); got != "" {
		t.Fatalf("empty usage rendered %q", got)
	}
	if got := RenderAgentUsage([]AgentUsageCount{{Name: "quick", Count: 0}}); got != "" {
		t.Fatalf("zero count rendered %q", got)
	}
}

// The counts are reference material: under width pressure they must give way
// before the model name and the context fill.
func TestStatusBarDropsUsageBeforeModelAndContext(t *testing.T) {
	contextLabel := RenderContextLabel(1000, 200000)
	plainContext := stripANSI(contextLabel)
	wide := OperationModeParams{
		ModelName:     "Opus 5",
		Width:         120,
		ContextTokens: 1000,
		ContextWindow: 200000,
		AgentUsage:    []AgentUsageCount{{Name: "quick", Count: 2}},
	}
	if got := stripANSI(RenderModeStatus(wide)); !strings.Contains(got, "»2") {
		t.Fatalf("wide status line lost the usage segment: %q", got)
	}

	// Exactly enough for the model, a separator and the context label: the
	// usage segment is the one over budget.
	justEnough := 3 + lipgloss.Width("Opus 5") + 3 + lipgloss.Width(contextLabel)
	wide.Width = justEnough
	got := stripANSI(RenderModeStatus(wide))
	if strings.Contains(got, "»2") {
		t.Fatalf("narrow status line kept the usage segment: %q", got)
	}
	if !strings.Contains(got, "Opus 5") || !strings.Contains(got, plainContext) {
		t.Fatalf("narrow status line dropped model or context: %q", got)
	}
}
