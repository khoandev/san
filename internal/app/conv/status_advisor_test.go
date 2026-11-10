package conv

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The advisor badge says what the reviewer is doing and what it last found. A
// disabled advisor keeps the row to the mode and the model.
func TestRenderModeStatusAdvisorBadge(t *testing.T) {
	base := OperationModeParams{ModelName: "claude-sonnet-4-6", Width: 120}
	tests := []struct {
		name  string
		badge AdvisorBadge
		want  string
	}{
		{"off", AdvisorBadge{}, ""},
		{"no model", AdvisorBadge{Enabled: true}, "advisor · no model"},
		{"reviewing", AdvisorBadge{Enabled: true, HasModel: true, Reviewing: true}, "advisor · reviewing"},
		{"noted", AdvisorBadge{Enabled: true, HasModel: true, Reviews: 1, LastNote: "- check the nil case"}, "advisor · noted"},
		{"checked", AdvisorBadge{Enabled: true, HasModel: true, Reviews: 2}, "advisor · checked"},
		{"error", AdvisorBadge{Enabled: true, HasModel: true, LastError: "timeout"}, "advisor · error"},
		{"skipped", AdvisorBadge{Enabled: true, HasModel: true, LastSkip: "queued"}, "advisor · queued"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := base
			params.Advisor = tt.badge
			got := stripANSI(RenderModeStatus(params))
			if tt.want == "" {
				if strings.Contains(got, "advisor") {
					t.Fatalf("disabled advisor rendered %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("badge = %q, want %q", got, tt.want)
			}
		})
	}
}

// The advisor badge rides in the right-hand cluster, after the model, so it
// shares that cluster's drop priority instead of pushing the mode off a narrow
// line. The plan badge and the group stay on the left with the mode.
func TestAdvisorBadgeRidesTheRightCluster(t *testing.T) {
	got := stripANSI(RenderModeStatus(OperationModeParams{
		ModelName: "m",
		Width:     160,
		PlanMode:  "plan",
		Advisor:   AdvisorBadge{Enabled: true, HasModel: true, Reviews: 1},
		Group:     "shop",
		GroupSelf: "me",
	}))
	plan := strings.Index(got, "plan")
	group := strings.Index(got, "shop/me")
	model := strings.Index(got, "m ·")
	advisor := strings.Index(got, "advisor")
	if plan < 0 || model < 0 || advisor < 0 || group < 0 {
		t.Fatalf("status lacks a badge: %q", got)
	}
	if !(plan < group && group < model && model < advisor) {
		t.Fatalf("badge order = plan %d, group %d, model %d, advisor %d: %q", plan, group, model, advisor, got)
	}
}

// Under pressure the advisor badge gives way with the usage counts before the
// model and the context fill it annotates.
func TestAdvisorBadgeDropsBeforeTheModel(t *testing.T) {
	contextLabel := RenderContextLabel(1000, 200000)
	justEnough := 3 + lipgloss.Width("m") + 3 + lipgloss.Width(contextLabel)
	got := stripANSI(RenderModeStatus(OperationModeParams{
		ModelName:     "m",
		Width:         justEnough,
		ContextTokens: 1000,
		ContextWindow: 200000,
		Advisor:       AdvisorBadge{Enabled: true, HasModel: true, Reviews: 1},
	}))
	if strings.Contains(got, "advisor") {
		t.Fatalf("narrow status line kept the advisor badge: %q", got)
	}
	if !strings.Contains(got, "m") || !strings.Contains(got, stripANSI(contextLabel)) {
		t.Fatalf("narrow status line dropped the model or context: %q", got)
	}
}
