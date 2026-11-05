package advisor

import (
	"fmt"
	"strings"
)

// Action is a step a review can gate. The same findings resolve differently per
// action, which is why the policy is keyed by action rather than a single
// on/off: a P1 may hold back a merge while leaving a finished subagent task
// usable.
type Action string

const (
	// ActionMerge gates merging a branch, creating a PR/MR, or pushing to the
	// target branch.
	ActionMerge Action = "merge"
	// ActionTaskCompletion gates treating a finished subagent task as clean.
	ActionTaskCompletion Action = "task_completion"
	// ActionPlanStepCompletion gates recording a plan step as done.
	ActionPlanStepCompletion Action = "plan_step_completion"
)

// Policy is the block policy resolved from settings. Each action carries the
// least severe level that blocks it; the empty value means "never blocks", so a
// disabled threshold needs no separate boolean and cannot be contradicted by
// one.
type Policy struct {
	// BlockMerge, BlockTaskCompletion and BlockPlanStepCompletion are the
	// lowest level that blocks the corresponding action. "" = advisory only.
	BlockMerge              Severity
	BlockTaskCompletion     Severity
	BlockPlanStepCompletion Severity
	// RequireConfirmation is the lowest level at which the human must confirm
	// before continuing. It does not block by itself.
	RequireConfirmation Severity
	// AllowProceedWithRisk lets the human continue past a block after
	// confirming; false makes the block final until the finding is fixed.
	AllowProceedWithRisk bool
}

// Threshold returns the blocking level for an action, or "" when it never
// blocks.
func (p Policy) Threshold(action Action) Severity {
	switch action {
	case ActionMerge:
		return p.BlockMerge
	case ActionTaskCompletion:
		return p.BlockTaskCompletion
	case ActionPlanStepCompletion:
		return p.BlockPlanStepCompletion
	}
	return ""
}

// Blocks reports whether a finding at this level stops the action. An unknown
// or empty severity never blocks: the policy answers only about levels it was
// configured for, and callers validate findings before they get here.
func (p Policy) Blocks(action Action, severity Severity) bool {
	threshold := p.Threshold(action)
	if threshold == "" || !severity.Valid() {
		return false
	}
	return severity.Rank() <= threshold.Rank()
}

// NeedsConfirmation reports whether a finding at this level should be put to the
// human before the action continues, whether or not it blocks.
func (p Policy) NeedsConfirmation(severity Severity) bool {
	if p.RequireConfirmation == "" || !severity.Valid() {
		return false
	}
	return severity.Rank() <= p.RequireConfirmation.Rank()
}

// ActionForTrigger maps a trigger to the action it gates. A review run on the
// human's request, or one triggered by a large change, gates nothing: it
// reports.
func ActionForTrigger(t Trigger) (Action, bool) {
	switch t {
	case TriggerBeforeMerge:
		return ActionMerge, true
	case TriggerSubagentTask:
		return ActionTaskCompletion, true
	case TriggerPlanStep:
		return ActionPlanStepCompletion, true
	}
	return "", false
}

// Verdict is a review's resolved outcome: the status, the outstanding severity
// and the per-level tally it was derived from.
type Verdict struct {
	Status   ReviewStatus
	Severity Severity
	Counts   Counts
}

// Evaluate resolves a set of findings into a verdict for the action the trigger
// gates. The model's own claimed status is deliberately not an input: a review
// that says "passed" while listing a P0 must not pass, and one that says
// "blocked" with nothing in it must not block.
func Evaluate(trigger Trigger, findings []Finding, policy Policy) Verdict {
	var unresolved Counts
	for _, f := range findings {
		if f.Unresolved() {
			unresolved.add(f.Severity)
		}
	}
	action, gated := ActionForTrigger(trigger)

	verdict := Verdict{Counts: unresolved}
	switch {
	case unresolved.P0 > 0:
		verdict.Severity = SeverityP0
	case unresolved.P1 > 0:
		verdict.Severity = SeverityP1
	case unresolved.P2 > 0:
		verdict.Severity = SeverityP2
	case unresolved.P3 > 0:
		verdict.Severity = SeverityP3
	}

	blocking := func(level Severity) bool { return gated && policy.Blocks(action, level) }

	switch {
	case unresolved.P0 > 0 && blocking(SeverityP0),
		unresolved.P1 > 0 && blocking(SeverityP1),
		unresolved.P2 > 0 && blocking(SeverityP2):
		verdict.Status = StatusBlocked
	case unresolved.P0 > 0, unresolved.P1 > 0:
		verdict.Status = StatusChangesRequired
	case unresolved.P2 > 0, unresolved.P3 > 0:
		verdict.Status = StatusPassedWithNotes
	default:
		verdict.Status = StatusPassed
	}
	return verdict
}

// Recommendation is what a review says about merging the work it inspected.
type Recommendation string

const (
	// SafeToMerge means nothing outstanding that a merge policy cares about.
	SafeToMerge Recommendation = "safe_to_merge"
	// ReviewRequired means the human should look before merging.
	ReviewRequired Recommendation = "review_required"
	// MergeBlocked means the merge policy refuses the merge.
	MergeBlocked Recommendation = "blocked"
)

// Label is the human-facing wording for a recommendation.
func (r Recommendation) Label() string {
	switch r {
	case SafeToMerge:
		return "safe to merge"
	case ReviewRequired:
		return "review required"
	case MergeBlocked:
		return "blocked"
	}
	return string(r)
}

// Recommend answers the merge question for one review, re-evaluating its
// findings against the merge policy rather than trusting the status the review
// resolved to for its own trigger. A subagent-task review that merely noted a
// P0 under a lenient task policy must still stop a merge under a strict merge
// policy, and this is where that happens.
func Recommend(review Review, policy Policy) Recommendation {
	if review.Status == StatusError {
		// A review that never reached a verdict cannot vouch for the code.
		return ReviewRequired
	}
	counts := review.UnresolvedCounts()
	switch {
	case counts.P0 > 0 && policy.Blocks(ActionMerge, SeverityP0),
		counts.P1 > 0 && policy.Blocks(ActionMerge, SeverityP1),
		counts.P2 > 0 && policy.Blocks(ActionMerge, SeverityP2):
		return MergeBlocked
	case counts.P0 > 0, counts.P1 > 0, counts.P2 > 0:
		return ReviewRequired
	}
	return SafeToMerge
}

// ApplyPolicy resolves the review's status and highest severity from its
// findings under the given policy, discarding whatever the model claimed. A
// StatusError review is left alone: it never reached a verdict, so there are no
// findings to judge.
func (r *Review) ApplyPolicy(policy Policy) {
	if r.Status == StatusError {
		return
	}
	verdict := Evaluate(r.Trigger, r.Findings, policy)
	r.Status = verdict.Status
	r.Severity = verdict.Severity
}

// Resolve moves a finding to a new status, enforcing the one rule the domain
// owns: dismissing a P0 or P1 is a decision, so it carries a reason. Without
// that, "dismiss" becomes an undocumented way past a blocker.
func (f *Finding) Resolve(status FindingStatus, note string) error {
	if !status.Valid() {
		return fmt.Errorf("unknown finding status %q", status)
	}
	note = strings.TrimSpace(note)
	if status == FindingDismissed && (f.Severity == SeverityP0 || f.Severity == SeverityP1) && note == "" {
		return fmt.Errorf("dismissing a %s finding requires a reason", f.Severity)
	}
	f.Status = status
	f.Note = note
	return nil
}
