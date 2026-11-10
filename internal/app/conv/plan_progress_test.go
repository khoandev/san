package conv

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/plan"
)

// executingPlan builds a plan whose steps carry the given statuses. Normalize
// puts the cursor on the first unfinished step, exactly as the store does.
func executingPlan(statuses ...plan.StepStatus) plan.Plan {
	steps := make([]plan.Step, len(statuses))
	for i, status := range statuses {
		steps[i] = plan.Step{
			ID:     fmt.Sprintf("step-%d", i),
			Title:  fmt.Sprintf("work %d", i+1),
			Status: status,
		}
	}
	p := plan.Plan{ID: "plan-1", Title: "Split auth", Status: plan.StatusExecuting, Steps: steps}
	p.Normalize()
	return p
}

func renderProgress(p plan.Plan, mode plan.Mode, width int) string {
	return stripANSI(RenderPlanProgress(PlanProgressParams{Mode: mode, Plan: p, Width: width}))
}

// The row's step text must be plan.StepDisplay's, so the tracker, the step
// prompt in the transcript and the panel header never disagree.
func TestRenderPlanProgressUsesStepDisplay(t *testing.T) {
	p := executingPlan(plan.StepInProgress, plan.StepPending)
	step, _ := p.CurrentStep()
	want := "  " + plan.StepDisplay(p, step, p.StepIndex) + " · running"
	if got := renderProgress(p, plan.ModeExecute, 100); got != want {
		t.Fatalf("progress row = %q, want %q", got, want)
	}
}

func TestRenderPlanProgressStatuses(t *testing.T) {
	paused := executingPlan(plan.StepInProgress, plan.StepPending)
	paused.Paused = true

	waiting := executingPlan(plan.StepInProgress, plan.StepPending)
	waiting.AwaitingStepChoice = true

	blocked := executingPlan(plan.StepBlocked, plan.StepPending)
	blocked.Steps[0].BlockReason = "tests  fail\non a flaky network"

	done := executingPlan(plan.StepCompleted, plan.StepSkipped)

	queued := executingPlan(plan.StepPending, plan.StepPending)

	for _, tc := range []struct {
		name string
		plan plan.Plan
		want []string
	}{
		{"paused", paused, []string{"paused"}},
		{"waiting", waiting, []string{"waiting for you"}},
		{"blocked", blocked, []string{"blocked", "tests fail on a flaky network"}},
		{"done", done, []string{"done"}},
		{"queued", queued, []string{"queued"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderProgress(tc.plan, plan.ModeExecute, 100)
			if strings.Contains(got, "\n") {
				t.Fatalf("progress must stay on one row: %q", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("progress row %q is missing %q", got, want)
				}
			}
		})
	}
}

// The row belongs to execution only: planning has no step in flight, an
// approved plan waiting for its first step is the panel's business, and a
// finished plan has nothing left to say.
func TestRenderPlanProgressIsSilentOffExecution(t *testing.T) {
	running := executingPlan(plan.StepInProgress, plan.StepPending)

	pending := executingPlan(plan.StepPending, plan.StepPending)
	pending.Status = plan.StatusPendingApproval

	completed := executingPlan(plan.StepCompleted, plan.StepCompleted)
	completed.Status = plan.StatusCompleted

	empty := plan.Plan{ID: "plan-empty", Status: plan.StatusExecuting}
	empty.Normalize()

	for _, tc := range []struct {
		name string
		plan plan.Plan
		mode plan.Mode
	}{
		{"chat mode", running, plan.ModeChat},
		{"plan mode", running, plan.ModePlan},
		{"pending approval", pending, plan.ModeExecute},
		{"terminal plan", completed, plan.ModeExecute},
		{"no steps", empty, plan.ModeExecute},
	} {
		if got := renderProgress(tc.plan, tc.mode, 100); got != "" {
			t.Fatalf("%s: want no progress row, got %q", tc.name, got)
		}
	}
}

func TestRenderPlanProgressFitsNarrowWidth(t *testing.T) {
	p := executingPlan(plan.StepInProgress, plan.StepPending)
	p.Steps[0].Title = strings.Repeat("very long step title ", 8)
	for _, width := range []int{12, 20, 40} {
		got := RenderPlanProgress(PlanProgressParams{Mode: plan.ModeExecute, Plan: p, Width: width})
		if strings.Contains(stripANSI(got), "\n") {
			t.Fatalf("width %d: progress wrapped: %q", width, got)
		}
		if gotWidth := lipgloss.Width(got); gotWidth > width {
			t.Fatalf("width %d: row is %d columns wide", width, gotWidth)
		}
	}
}
