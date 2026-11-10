package conv

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/plan"
)

// PlanProgressParams describes one frame of the plan progress row.
type PlanProgressParams struct {
	// Mode is the session's Plan Mode. The row exists only while a plan
	// executes: planning is not progress, and an approved plan waiting for its
	// first step is the panel's business.
	Mode plan.Mode
	// Plan is the session's active plan.
	Plan  plan.Plan
	Width int
}

// RenderPlanProgress renders the plan's progress as the one row the tracker
// area shows above the composer while an approved plan runs. It is a
// transcript-shaped row, not a panel: the step in flight, where it sits in the
// plan, and what it is doing.
//
// The step text comes from plan.StepDisplay, so this row, the step prompt the
// executor writes into the transcript, and the panel header cannot disagree
// about which step is running. The counts come from plan.Counts for the same
// reason. Nothing here must occupy more than one row: the conversation above
// the composer is the point of the screen.
func RenderPlanProgress(params PlanProgressParams) string {
	// Execute Mode with a plan that is actually executing: an approved plan
	// still waiting for its first step belongs to the panel, not to a progress
	// row that would announce a step nobody started.
	if params.Mode != plan.ModeExecute || params.Plan.Status != plan.StatusExecuting || params.Plan.Terminal() {
		return ""
	}
	step, ok := params.Plan.CurrentStep()
	if !ok {
		return ""
	}
	line := plan.StepDisplay(params.Plan, step, params.Plan.StepIndex)
	status := planProgressStatus(params.Plan, step)
	if status != "" {
		line += " · " + status
	}
	if width := params.Width - 2; width > 0 {
		line = kit.TruncateText(line, width)
	}
	return "  " + planProgressStyle(status).Render(line)
}

// planProgressStatus names what the current step is doing. Every branch reads
// the plan store's own fields; none of them infers intent from a timestamp.
func planProgressStatus(p plan.Plan, step plan.Step) string {
	_, handled, blocked, total := p.Counts()
	if total > 0 && handled == total {
		return "done"
	}
	switch {
	case step.Status == plan.StepBlocked:
		out := "blocked"
		if blocked > 1 {
			out += fmt.Sprintf(" (%d)", blocked)
		}
		if reason := strings.Join(strings.Fields(step.BlockReason), " "); reason != "" {
			out += " — " + kit.TruncateText(reason, 40)
		}
		return out
	case p.AwaitingStepChoice:
		return "waiting for you"
	case p.Paused:
		return "paused"
	case step.Status == plan.StepInProgress:
		return "running"
	}
	return "queued"
}

// planProgressStyle tones the row by what it says, matching the tracker's
// palette so the two blocks above the composer read as one.
func planProgressStyle(status string) lipgloss.Style {
	colour := kit.CurrentTheme.Accent
	switch {
	case status == "done":
		colour = kit.CurrentTheme.Success
	case strings.HasPrefix(status, "blocked"):
		colour = kit.CurrentTheme.Warning
	case status == "paused", status == "waiting for you":
		colour = kit.CurrentTheme.Muted
	}
	return lipgloss.NewStyle().Foreground(colour)
}
