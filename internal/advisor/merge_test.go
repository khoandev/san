package advisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/proc"
)

// mergeDiff is the diff a fake reader returns: one changed file with a stable
// hash, so a second call with the same hash is recognisably the same code.
func mergeDiff(hash string) proc.GitDiff {
	return proc.GitDiff{
		Base:  "base-sha",
		Diff:  "diff --git a/internal/api.go b/internal/api.go\n+token := os.Getenv(\"TOKEN\")\n",
		Files: []string{"internal/api.go"},
		Hash:  hash,
	}
}

// mergeReview builds a review of the merge target with the given findings,
// resolved the way the engine resolves them: from the findings, never from what
// the model claimed.
func mergeReview(findings ...Finding) Review {
	review := Review{
		ID:      "review-merge",
		Trigger: TriggerBeforeMerge,
		Target:  Target{Kind: TargetMerge, ID: mergeTargetID, Label: "git merge"},
	}
	for i := range findings {
		if findings[i].Status == "" {
			findings[i].Status = FindingOpen
		}
	}
	review.Findings = findings
	review.ApplyPolicy(Policy{})
	return review
}

// mergeGateFixture is a gate over in-memory collaborators, so the table below
// exercises the rules rather than a repository or a model.
type mergeGateFixture struct {
	gate       *MergeGate
	policy     Policy
	diff       proc.GitDiff
	diffErr    error
	review     Review // what a fresh run answers
	stored     Review // what history already holds, when reuse is on
	reviewErr  error
	reused     bool
	diffReads  int
	reviewRuns int
	recorded   []Review
}

func newMergeGateFixture(t *testing.T, policy Policy) *mergeGateFixture {
	t.Helper()
	f := &mergeGateFixture{policy: policy, diff: mergeDiff("hash-1")}
	f.gate = NewMergeGate(MergeGateConfig{
		Enabled: func() bool { return true },
		Policy:  func() Policy { return f.policy },
		ReadDiff: func(context.Context) (proc.GitDiff, error) {
			f.diffReads++
			return f.diff, f.diffErr
		},
		Review: func(_ context.Context, in ReviewInput) (Review, error) {
			f.reviewRuns++
			if f.reviewErr != nil {
				return Review{}, f.reviewErr
			}
			review := f.review.Clone()
			if review.ID == "" {
				review = mergeReview()
			}
			return review, nil
		},
		Reuse: func(target Target, diffHash string) (Review, bool) {
			if !f.reused {
				return Review{}, false
			}
			review := f.stored.Clone()
			return review, review.Target.Kind == target.Kind && review.DiffHash == diffHash
		},
		Record: func(review Review) { f.recorded = append(f.recorded, review) },
	})
	return f
}

func mergeCommandCall() (string, map[string]any) {
	return "Bash", map[string]any{"command": "git merge origin/main"}
}

// check runs the gate on the call a model would make to merge.
func (f *mergeGateFixture) check() MergeOutcome {
	name, input := mergeCommandCall()
	return f.gate.Check(context.Background(), name, input)
}

// checkMerge runs any gate on that same call, for the cases with no fixture.
func checkMerge(gate *MergeGate) MergeOutcome {
	name, input := mergeCommandCall()
	return gate.Check(context.Background(), name, input)
}

// The four combinations the policy has to get right: a P0 and a P1, each under
// a merge policy that blocks and one that does not. A block rejects; a finding
// the policy leaves alone asks the human; nothing worth asking about permits.
func TestMergeGatePolicyTable(t *testing.T) {
	p0 := Finding{Severity: SeverityP0, Title: "leaks the API token"}
	p1 := Finding{Severity: SeverityP1, Title: "drops the last page"}

	cases := []struct {
		name       string
		finding    Finding
		policy     Policy
		want       MergeVerdict
		wantReason []string
	}{
		{
			name:       "p0 blocks",
			finding:    p0,
			policy:     Policy{BlockMerge: SeverityP0},
			want:       MergeReject,
			wantReason: []string{"review-merge", "P0", "leaks the API token", "/advisor"},
		},
		{
			name:       "p0 does not block",
			finding:    p0,
			policy:     Policy{},
			want:       MergePermit,
			wantReason: []string{"review-merge", "P0", "leaks the API token"},
		},
		{
			name:       "p1 blocks when the policy says so",
			finding:    p1,
			policy:     Policy{BlockMerge: SeverityP1},
			want:       MergeReject,
			wantReason: []string{"review-merge", "P1", "drops the last page"},
		},
		{
			name:       "p1 outside the block threshold",
			finding:    p1,
			policy:     Policy{BlockMerge: SeverityP0},
			want:       MergePermit,
			wantReason: []string{"review-merge", "P1"},
		},
		{
			name:       "p1 asks the human",
			finding:    p1,
			policy:     Policy{BlockMerge: SeverityP0, RequireConfirmation: SeverityP1},
			want:       MergeConfirm,
			wantReason: []string{"review-merge", "P1", "your call"},
		},
		{
			name:       "a blocked merge the human may risk",
			finding:    p0,
			policy:     Policy{BlockMerge: SeverityP0, AllowProceedWithRisk: true},
			want:       MergeConfirm,
			wantReason: []string{"review-merge", "P0", "blocking issue"},
		},
		{
			name:       "a clean diff permits",
			policy:     Policy{BlockMerge: SeverityP0, RequireConfirmation: SeverityP1},
			want:       MergePermit,
			wantReason: []string{"review-merge", "whole diff against base"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMergeGateFixture(t, tc.policy)
			if tc.finding.Severity != "" {
				f.review = mergeReview(tc.finding)
			} else {
				f.review = mergeReview()
			}
			name, input := mergeCommandCall()
			out := f.gate.Check(context.Background(), name, input)

			if out.Verdict != tc.want {
				t.Fatalf("verdict = %s, want %s (reason %q)", out.Verdict, tc.want, out.Reason)
			}
			for _, want := range tc.wantReason {
				if !strings.Contains(out.Reason, want) {
					t.Errorf("reason %q is missing %q", out.Reason, want)
				}
			}
			if f.reviewRuns != 1 {
				t.Errorf("review ran %d times, want 1", f.reviewRuns)
			}
		})
	}
}

// A merge command is reviewed against the whole diff: the gate reads it, reviews
// it once and answers from that review.
func TestMergeGateReviewsTheWholeDiff(t *testing.T) {
	f := newMergeGateFixture(t, Policy{BlockMerge: SeverityP0})
	f.review = mergeReview(Finding{Severity: SeverityP0, Title: "leaks the API token"})

	var seen ReviewInput
	base := f.gate.cfg.Review
	f.gate.cfg.Review = func(ctx context.Context, in ReviewInput) (Review, error) {
		seen = in
		return base(ctx, in)
	}

	out := f.check()
	if out.Verdict != MergeReject {
		t.Fatalf("verdict = %s, want reject", out.Verdict)
	}
	if seen.Trigger != TriggerBeforeMerge {
		t.Errorf("trigger = %q, want the merge trigger", seen.Trigger)
	}
	if seen.Target.Kind != TargetMerge || seen.Target.ID != mergeTargetID {
		t.Errorf("target = %+v, want the merge target", seen.Target)
	}
	if seen.DiffHash != "hash-1" || seen.Diff == "" {
		t.Errorf("review input lost the diff: hash %q, %d bytes", seen.DiffHash, len(seen.Diff))
	}
	if seen.Target.Base != "base-sha" {
		t.Errorf("target base = %q, want the commit the diff was taken against", seen.Target.Base)
	}
	if len(seen.Files) != 1 || seen.Files[0] != "internal/api.go" {
		t.Errorf("files = %v, want the changed path", seen.Files)
	}
}

// A second merge attempt on unchanged code reuses the recorded review: the
// verdict is the same one, and no second model call is paid for.
func TestMergeGateReusesTheReviewForTheSameHash(t *testing.T) {
	f := newMergeGateFixture(t, Policy{BlockMerge: SeverityP0})
	f.stored = mergeReview(Finding{Severity: SeverityP0, Title: "leaks the API token"})
	f.stored.DiffHash = "hash-1"
	f.reused = true

	out := f.check()
	if out.Verdict != MergeReject {
		t.Fatalf("verdict = %s, want the reused rejection", out.Verdict)
	}
	if f.reviewRuns != 0 {
		t.Errorf("review ran %d times, want the recorded one reused", f.reviewRuns)
	}
	if !strings.Contains(out.Reason, "review-merge") {
		t.Errorf("reused reason %q does not name the review", out.Reason)
	}

	// A different diff for the same target is different code: it is reviewed
	// again rather than inheriting the old verdict.
	f.diff = mergeDiff("hash-2")
	f.review = mergeReview(Finding{Severity: SeverityP0, Title: "leaks the API token"})
	out = f.check()
	if f.reviewRuns != 1 {
		t.Fatalf("review ran %d times, want the changed diff reviewed", f.reviewRuns)
	}
	if out.Verdict != MergeReject {
		t.Fatalf("verdict = %s, want the new review's rejection", out.Verdict)
	}
}

// A review that never reached a verdict is not reusable: the next merge attempt
// must try again rather than inherit "we could not check".
func TestMergeGateDoesNotReuseAnErrorReview(t *testing.T) {
	f := newMergeGateFixture(t, Policy{})
	f.reused = true
	f.stored = Review{ID: "review-error", Status: StatusError, Error: "provider timeout",
		Target: Target{Kind: TargetMerge, ID: mergeTargetID}}
	f.review = mergeReview()

	out := f.check()
	if f.reviewRuns != 1 {
		t.Fatalf("review ran %d times, want the error review retried", f.reviewRuns)
	}
	if out.Review.Status == StatusError {
		t.Fatal("the gate answered from a review that never reached a verdict")
	}
}

// Nothing that is not a merge pays for the gate: no diff read, no review, no
// latency, and the permission path decides.
func TestMergeGateIgnoresEverythingElse(t *testing.T) {
	f := newMergeGateFixture(t, Policy{BlockMerge: SeverityP0})
	cases := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{"a build command", "Bash", map[string]any{"command": "go build ./..."}},
		{"a quoted merge", "Bash", map[string]any{"command": "echo 'git merge main'"}},
		{"a commit", "Bash", map[string]any{"command": "git commit -m ship"}},
		{"a pull", "Bash", map[string]any{"command": "git pull origin main"}},
		{"a non-shell tool", "Read", map[string]any{"file_path": "internal/api.go"}},
		{"a shell with no command", "Bash", map[string]any{}},
		{"a nil input", "PowerShell", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := f.gate.Check(context.Background(), tc.tool, tc.input)
			if out.Verdict != MergeNotGated {
				t.Fatalf("verdict = %s, want not-gated", out.Verdict)
			}
		})
	}
	if f.diffReads != 0 || f.reviewRuns != 0 || len(f.recorded) != 0 {
		t.Fatalf("a non-merge call paid for the gate: %d diff reads, %d reviews, %d records",
			f.diffReads, f.reviewRuns, len(f.recorded))
	}
}

// A disabled trigger costs less than a merge: the command is not even parsed.
func TestMergeGateOffTouchesNothing(t *testing.T) {
	f := newMergeGateFixture(t, Policy{BlockMerge: SeverityP0})
	f.gate.cfg.Enabled = func() bool { return false }

	if out := f.check(); out.Verdict != MergeNotGated {
		t.Fatalf("verdict = %s, want not-gated", out.Verdict)
	}
	if f.diffReads != 0 || f.reviewRuns != 0 {
		t.Fatal("a disabled gate read a diff or ran a review")
	}
}

// A review that cannot run neither passes silently nor blocks by accident: it
// warns and permits, and records the failure so the panel can say why no verdict
// exists.
func TestMergeGateFailsOpenWithAWarning(t *testing.T) {
	f := newMergeGateFixture(t, Policy{})
	f.reviewErr = errors.New("provider timeout")

	out := f.check()
	if out.Verdict != MergePermit {
		t.Fatalf("verdict = %s, want a warned permit", out.Verdict)
	}
	if !strings.Contains(out.Reason, "provider timeout") || !strings.Contains(out.Reason, "warning") {
		t.Fatalf("reason %q does not warn about the failed review", out.Reason)
	}
	if len(f.recorded) != 1 || f.recorded[0].Status != StatusError {
		t.Fatalf("recorded = %+v, want one error review", f.recorded)
	}
	if f.recorded[0].Error == "" || f.recorded[0].Trigger != TriggerBeforeMerge {
		t.Errorf("error review = %+v, want it aimed at the merge trigger", f.recorded[0])
	}
	if f.recorded[0].Target.Kind != TargetMerge {
		t.Errorf("error review target = %+v, want the merge target", f.recorded[0].Target)
	}
}

// When the user has asked for merges to be blocked, "we could not check" must
// not read as "we checked": the same failure rejects.
func TestMergeGateFailsClosedWhenMergesAreBlocked(t *testing.T) {
	f := newMergeGateFixture(t, Policy{BlockMerge: SeverityP0})
	f.diffErr = errors.New("not a git repository")

	out := f.check()
	if out.Verdict != MergeReject {
		t.Fatalf("verdict = %s, want a rejection", out.Verdict)
	}
	if !strings.Contains(out.Reason, "not a git repository") || !strings.Contains(out.Reason, "review") {
		t.Fatalf("reason %q does not name the failure and the review", out.Reason)
	}
	if len(f.recorded) != 1 || f.recorded[0].Status != StatusError {
		t.Fatalf("recorded = %+v, want one error review", f.recorded)
	}
	if f.recorded[0].DiffHash != "" {
		t.Errorf("diff hash = %q, want none: the diff was never read", f.recorded[0].DiffHash)
	}
}

// A merge review that has no runner configured is a failure, not a pass: the
// policy still decides, and the default policy permits with a warning.
func TestMergeGateWithoutARunnerWarns(t *testing.T) {
	gate := NewMergeGate(MergeGateConfig{})
	name, input := mergeCommandCall()
	out := gate.Check(context.Background(), name, input)
	if out.Verdict != MergePermit {
		t.Fatalf("verdict = %s, want a warned permit", out.Verdict)
	}
	if !strings.Contains(out.Reason, "no review runner") {
		t.Fatalf("reason = %q, want it to name the missing runner", out.Reason)
	}
}

// A gate read from a nil receiver and a nil timeout are both safe: the gate is
// called from the permission path, which must not panic on a half-configured
// session.
func TestMergeGateNilSafety(t *testing.T) {
	var nilGate *MergeGate
	if out := checkMerge(nilGate); out.Verdict != MergeNotGated {
		t.Fatalf("nil gate verdict = %s, want not-gated", out.Verdict)
	}
	f := newMergeGateFixture(t, Policy{})
	if got := f.gate.timeout(); got != defaultMergeGateTimeout {
		t.Fatalf("timeout = %v, want the default", got)
	}
	f.gate.cfg.Timeout = time.Second
	if got := f.gate.timeout(); got != time.Second {
		t.Fatalf("timeout = %v, want the configured one", got)
	}
}

// Only three finding titles reach the reason: a tool error has to stay legible,
// and the rest are one keystroke away in the panel.
func TestMergeReasonCapsFindingTitles(t *testing.T) {
	review := mergeReview(
		Finding{Severity: SeverityP0, Title: "aaaa"},
		Finding{Severity: SeverityP0, Title: "bbbb"},
		Finding{Severity: SeverityP0, Title: "cccc"},
		Finding{Severity: SeverityP0, Title: "dddd"},
	)
	reason := mergeReason("advisor blocked this merge", review, SeverityP0)
	if !strings.Contains(reason, "(+1 more)") {
		t.Fatalf("reason %q does not say the list was capped", reason)
	}
	if strings.Contains(reason, "dddd") {
		t.Fatalf("reason %q names a title past the cap", reason)
	}

	// A resolved finding no longer counts, so a dismissed blocker stops
	// appearing in the reason without changing the review.
	review.Findings[0].Status = FindingDismissed
	reason = mergeReason("advisor blocked this merge", Review{
		ID: "review-merge", Trigger: TriggerBeforeMerge,
		Target: Target{Kind: TargetMerge, ID: mergeTargetID},
		Findings: []Finding{
			{Severity: SeverityP0, Title: "dismissed", Status: FindingDismissed},
			{Severity: SeverityP2, Title: "still open", Status: FindingOpen},
		},
	}, SeverityP2)
	if strings.Contains(reason, "dismissed") || !strings.Contains(reason, "still open") {
		t.Fatalf("reason = %q, want only the unresolved findings", reason)
	}
}
