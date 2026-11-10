package conv

import (
	"strings"
	"testing"
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

// The badge sits with the mode and the plan, before the group: it answers how
// this session is running, which is the left cluster's question.
func TestAdvisorBadgeFollowsThePlanBadge(t *testing.T) {
	got := stripANSI(RenderModeStatus(OperationModeParams{
		ModelName: "m",
		Width:     160,
		PlanMode:  "plan",
		Advisor:   AdvisorBadge{Enabled: true, HasModel: true, Reviews: 1},
		Group:     "shop",
		GroupSelf: "me",
	}))
	plan := strings.Index(got, "plan")
	advisor := strings.Index(got, "advisor")
	group := strings.Index(got, "shop/me")
	if plan < 0 || advisor < 0 || group < 0 {
		t.Fatalf("status lacks a badge: %q", got)
	}
	if !(plan < advisor && advisor < group) {
		t.Fatalf("badge order = plan %d, advisor %d, group %d: %q", plan, advisor, group, got)
	}
}
