package advisor

import "testing"

func findings(levels ...Severity) []Finding {
	out := make([]Finding, 0, len(levels))
	for _, level := range levels {
		out = append(out, Finding{Severity: level, Status: FindingOpen})
	}
	return out
}

func TestPolicyBlocksOnlyAtOrAboveThreshold(t *testing.T) {
	t.Parallel()

	p := Policy{BlockMerge: SeverityP1}
	if p.Blocks(ActionMerge, SeverityP0) != true {
		t.Error("P0 must block when the merge threshold is P1")
	}
	if p.Blocks(ActionMerge, SeverityP1) != true {
		t.Error("P1 must block at its own threshold")
	}
	if p.Blocks(ActionMerge, SeverityP2) {
		t.Error("P2 must not block when the merge threshold is P1")
	}
	if p.Blocks(ActionTaskCompletion, SeverityP0) {
		t.Error("an unset task threshold must never block")
	}

	// An unknown severity can never block: it is rejected at the parse
	// boundary, so a garbage level must not become a blocker on the way here.
	if (Policy{BlockMerge: SeverityP0}).Blocks(ActionMerge, Severity("")) {
		t.Error("empty severity blocked")
	}
	if (Policy{BlockMerge: SeverityP0}).Blocks(ActionMerge, Severity("P9")) {
		t.Error("unknown severity blocked")
	}
}

func TestPolicyNeedsConfirmation(t *testing.T) {
	t.Parallel()

	p := Policy{RequireConfirmation: SeverityP1}
	if !p.NeedsConfirmation(SeverityP0) || !p.NeedsConfirmation(SeverityP1) {
		t.Error("P0 and P1 must need confirmation")
	}
	if p.NeedsConfirmation(SeverityP2) || p.NeedsConfirmation(SeverityP3) {
		t.Error("P2 and P3 must not need confirmation")
	}
	if (Policy{}).NeedsConfirmation(SeverityP0) {
		t.Error("an unset threshold must not need confirmation")
	}
}

func TestEvaluateStatusPerLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		trigger  Trigger
		policy   Policy
		findings []Finding
		want     ReviewStatus
		severity Severity
	}{
		{"nothing found", TriggerBeforeMerge, Policy{BlockMerge: SeverityP0}, nil, StatusPassed, ""},
		{"P3 only", TriggerBeforeMerge, Policy{BlockMerge: SeverityP0}, findings(SeverityP3), StatusPassedWithNotes, SeverityP3},
		{"P2 only", TriggerUserRequest, Policy{}, findings(SeverityP2, SeverityP3), StatusPassedWithNotes, SeverityP2},
		{"P1 does not block by default", TriggerBeforeMerge, Policy{BlockMerge: SeverityP0}, findings(SeverityP1), StatusChangesRequired, SeverityP1},
		{"P1 blocks when configured", TriggerBeforeMerge, Policy{BlockMerge: SeverityP1}, findings(SeverityP1), StatusBlocked, SeverityP1},
		{"P0 blocks merge", TriggerBeforeMerge, Policy{BlockMerge: SeverityP0}, findings(SeverityP0), StatusBlocked, SeverityP0},
		{"P0 without a merge block", TriggerBeforeMerge, Policy{}, findings(SeverityP0), StatusChangesRequired, SeverityP0},
		{"P0 blocks task completion", TriggerSubagentTask, Policy{BlockTaskCompletion: SeverityP0}, findings(SeverityP0), StatusBlocked, SeverityP0},
		{"P0 does not block task completion", TriggerSubagentTask, Policy{}, findings(SeverityP0), StatusChangesRequired, SeverityP0},
		{"P0 blocks plan step", TriggerPlanStep, Policy{BlockPlanStepCompletion: SeverityP0}, findings(SeverityP0), StatusBlocked, SeverityP0},
		{"P0 never blocks an ungated trigger", TriggerLargeChange, Policy{BlockMerge: SeverityP0, BlockTaskCompletion: SeverityP0, BlockPlanStepCompletion: SeverityP0}, findings(SeverityP0), StatusChangesRequired, SeverityP0},
		{"P2 blocks under a strict merge policy", TriggerBeforeMerge, Policy{BlockMerge: SeverityP2}, findings(SeverityP2), StatusBlocked, SeverityP2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.trigger, tc.findings, tc.policy)
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
			if got.Severity != tc.severity {
				t.Errorf("severity = %q, want %q", got.Severity, tc.severity)
			}
		})
	}
}

func TestEvaluateIgnoresResolvedFindings(t *testing.T) {
	t.Parallel()

	items := []Finding{
		{Severity: SeverityP0, Status: FindingDismissed},
		{Severity: SeverityP1, Status: FindingAddressed},
		{Severity: SeverityP3, Status: FindingOpen},
	}
	got := Evaluate(TriggerBeforeMerge, items, Policy{BlockMerge: SeverityP0})
	if got.Status != StatusPassedWithNotes || got.Severity != SeverityP3 {
		t.Fatalf("Evaluate() = %+v, want passed_with_notes/P3", got)
	}
	if got.Counts.P0 != 0 || got.Counts.P1 != 0 {
		t.Fatalf("resolved findings counted: %+v", got.Counts)
	}
}

func TestEvaluateTreatsDeferredAsOutstanding(t *testing.T) {
	t.Parallel()

	items := []Finding{{Severity: SeverityP0, Status: FindingDeferred}}
	got := Evaluate(TriggerBeforeMerge, items, Policy{BlockMerge: SeverityP0})
	if got.Status != StatusBlocked {
		t.Fatalf("deferred P0 resolved to %q, want blocked (defer must not bypass a blocker)", got.Status)
	}
}

func TestApplyPolicyOverridesModelClaim(t *testing.T) {
	t.Parallel()

	r := Review{
		Trigger:  TriggerBeforeMerge,
		Target:   Target{Kind: TargetDiff},
		Status:   StatusPassed,
		Findings: findings(SeverityP0),
	}
	r.ApplyPolicy(Policy{BlockMerge: SeverityP0})
	if r.Status != StatusBlocked || r.Severity != SeverityP0 {
		t.Fatalf("ApplyPolicy() = %q/%q, want blocked/P0", r.Status, r.Severity)
	}

	failed := Review{Trigger: TriggerBeforeMerge, Target: Target{Kind: TargetDiff}, Status: StatusError, Error: "timeout"}
	failed.ApplyPolicy(Policy{BlockMerge: SeverityP0})
	if failed.Status != StatusError {
		t.Fatalf("a failed review was re-resolved to %q", failed.Status)
	}
}

func TestRecommendReEvaluatesAgainstMergePolicy(t *testing.T) {
	t.Parallel()

	// A subagent-task review that only noted a P0 (lenient task policy) must
	// still stop a merge under a strict merge policy.
	taskReview := Review{
		Trigger:  TriggerSubagentTask,
		Target:   Target{Kind: TargetTask, ID: "task-1"},
		Findings: findings(SeverityP0),
	}
	taskReview.ApplyPolicy(Policy{})
	if taskReview.Status != StatusChangesRequired {
		t.Fatalf("task review status = %q, want changes_required", taskReview.Status)
	}
	if got := Recommend(taskReview, Policy{BlockMerge: SeverityP0}); got != MergeBlocked {
		t.Errorf("Recommend() = %q, want blocked", got)
	}
	if got := Recommend(taskReview, Policy{}); got != ReviewRequired {
		t.Errorf("Recommend() with no merge block = %q, want review_required", got)
	}

	clean := Review{Status: StatusPassed}
	if got := Recommend(clean, Policy{BlockMerge: SeverityP0}); got != SafeToMerge {
		t.Errorf("Recommend(clean) = %q, want safe_to_merge", got)
	}
	notes := Review{Status: StatusPassedWithNotes, Findings: findings(SeverityP3)}
	if got := Recommend(notes, Policy{BlockMerge: SeverityP0}); got != SafeToMerge {
		t.Errorf("Recommend(P3 only) = %q, want safe_to_merge", got)
	}
	critical := Review{Status: StatusChangesRequired, Findings: findings(SeverityP1)}
	if got := Recommend(critical, Policy{BlockMerge: SeverityP1}); got != MergeBlocked {
		t.Errorf("Recommend(P1 under P1 policy) = %q, want blocked", got)
	}
	failed := Review{Status: StatusError, Error: "provider unavailable"}
	if got := Recommend(failed, Policy{}); got != ReviewRequired {
		t.Errorf("Recommend(failed review) = %q, want review_required", got)
	}
}

func TestRecommendationLabel(t *testing.T) {
	t.Parallel()

	want := map[Recommendation]string{
		SafeToMerge:    "safe to merge",
		ReviewRequired: "review required",
		MergeBlocked:   "blocked",
	}
	for rec, label := range want {
		if got := rec.Label(); got != label {
			t.Errorf("%q.Label() = %q, want %q", rec, got, label)
		}
	}
}

func TestActionForTrigger(t *testing.T) {
	t.Parallel()

	cases := map[Trigger]Action{
		TriggerBeforeMerge:  ActionMerge,
		TriggerSubagentTask: ActionTaskCompletion,
		TriggerPlanStep:     ActionPlanStepCompletion,
	}
	for trigger, want := range cases {
		got, ok := ActionForTrigger(trigger)
		if !ok || got != want {
			t.Errorf("ActionForTrigger(%q) = (%q, %v), want (%q, true)", trigger, got, ok, want)
		}
	}
	for _, trigger := range []Trigger{TriggerLargeChange, TriggerUserRequest, ""} {
		if _, ok := ActionForTrigger(trigger); ok {
			t.Errorf("ActionForTrigger(%q) reported a gated action", trigger)
		}
	}
}
