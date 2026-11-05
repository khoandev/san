package agent

// These tests drive the merge gate through the permission path buildAgent
// installs — the same wrapper, ahead of the same PermissionGate — because the
// ordering is the property that matters: a refused merge never reaches the
// permission rules, a reviewed one still passes them, and a call that
// integrates nothing never pays for a review.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/tool/perm"
)

// mergeGateWait bounds one gated call in these tests. A gate that ran a review
// it should have skipped fails here instead of hanging the package.
const mergeGateWait = 5 * time.Second

// mergeGateFixture is one merge gate wired the way buildAgent wires it, with
// the three collaborators a test has to own — the diff read, the review run and
// the reuse lookup — replaced by counters and fixed answers.
type mergeGateFixture struct {
	policy advisor.Policy
	review advisor.Review
	diff   proc.GitDiff

	// diffGate, when set, holds the diff read open so a test can prove the gate
	// never asked for it.
	diffGate chan struct{}

	store      *advisor.Store
	diffReads  int
	reviewRuns int
	nextCalls  int

	gate       *advisor.MergeGate
	permission *PermissionGate
	check      core.Gate
}

// newMergeGateFixture builds the gate and the stack around it: the merge gate
// in front of a permission gate that permits everything, so nothing but the
// advisor can block a call.
func newMergeGateFixture(t *testing.T, policy advisor.Policy, review advisor.Review) *mergeGateFixture {
	t.Helper()
	f := &mergeGateFixture{
		policy: policy,
		review: review,
		// A whole-diff snapshot: base commit, changed file, and the hash that
		// identifies "exactly this change".
		diff: proc.GitDiff{
			Base:  "base-sha",
			Diff:  "diff --git a/internal/api.go b/internal/api.go\n+log(token)",
			Files: []string{"internal/api.go"},
			Hash:  "hash-1",
		},
		store: advisor.NewStore(),
	}
	f.gate = advisor.NewMergeGate(advisor.MergeGateConfig{
		Policy:  func() advisor.Policy { return f.policy },
		Timeout: 30 * time.Second,
		ReadDiff: func(ctx context.Context) (proc.GitDiff, error) {
			f.diffReads++
			if f.diffGate != nil {
				select {
				case <-f.diffGate:
				case <-ctx.Done():
					return proc.GitDiff{}, ctx.Err()
				}
			}
			return f.diff, nil
		},
		Review: func(_ context.Context, in advisor.ReviewInput) (advisor.Review, error) {
			f.reviewRuns++
			// What the app's runner does with a verdict: store it, so the
			// gate's own reuse lookup can find it on the next attempt.
			next := f.review.Clone()
			next.Trigger, next.Target, next.DiffHash = in.Trigger, in.Target, in.DiffHash
			return f.store.Append(next)
		},
		Reuse: f.store.ReviewFor,
	})
	f.permission = NewPermissionGate(func(string, map[string]any) PermDecisionResult {
		return PermDecisionResult{Decision: perm.Permit, Reason: "permitted by test"}
	})
	t.Cleanup(f.permission.Close)
	resolve := func() *advisor.MergeGate { return f.gate }
	f.check = withMergeGate(resolve, f.permission, func(context.Context, sdkagent.PreToolContext) (sdkagent.Decision, error) {
		f.nextCalls++
		return sdkagent.Decision{}, nil
	})
	return f
}

// run executes one tool call through the merge-gated stack, answering a
// confirmation prompt with answer (nil denies, as a user declining would).
func (f *mergeGateFixture) run(t *testing.T, name string, input map[string]any, answer *PermGateResponse) (blocked, prompted bool, reason, prompt string) {
	t.Helper()
	type outcome struct {
		decision sdkagent.Decision
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		decision, err := f.check(context.Background(), sdkagent.PreToolContext{
			Call: ai.ToolCall{Name: name, Input: mustJSON(input)},
		})
		done <- outcome{decision: decision, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("%s: %v", name, got.err)
		}
		return got.decision.Block, false, got.decision.Reason, ""
	case req := <-f.permission.requests:
		prompt = req.Description
		if answer == nil {
			answer = &PermGateResponse{Allow: false, Reason: "denied by test"}
		}
		req.Response <- *answer
	case <-time.After(mergeGateWait):
		t.Fatalf("%s: the merge gate never answered", name)
		return
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("%s: %v", name, got.err)
	}
	return got.decision.Block, true, got.decision.Reason, prompt
}

// mergeInput is a shell call running command.
func mergeInput(command string) map[string]any { return map[string]any{"command": command} }

// blockingMergeReview is a P0 the merge policy refuses under.
func blockingMergeReview() advisor.Review {
	return advisor.Review{
		Status:   advisor.StatusBlocked,
		Severity: advisor.SeverityP0,
		Summary:  "Leaks the API key.",
		Findings: []advisor.Finding{{
			Severity: advisor.SeverityP0,
			Title:    "Leaks the API key",
			Detail:   "Logs the token at startup.",
			File:     "internal/api.go",
			Line:     42,
		}},
	}
}

// criticalMergeReview is a P1: real, but below the default merge threshold.
func criticalMergeReview() advisor.Review {
	return advisor.Review{
		Status:   advisor.StatusChangesRequired,
		Severity: advisor.SeverityP1,
		Summary:  "The retry path has no test.",
		Findings: []advisor.Finding{{
			Severity: advisor.SeverityP1,
			Title:    "No test for the retry path",
			Detail:   "A regression here would ship silently.",
			File:     "internal/api.go",
		}},
	}
}

// The four combinations of finding level and policy, plus the human's answer on
// the one row that asks for it. Each row also pins whether the permission rules
// below the gate were consulted: a refusal is final, a permit is not.
func TestMergeGateResolvesTheMergePolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   advisor.Policy
		review   advisor.Review
		answer   *PermGateResponse
		blocked  bool
		prompted bool
		// wantReason holds what the model must read on a refusal: the verdict,
		// the findings and where to see the rest.
		wantReason []string
	}{
		{
			name:    "P0 with the merge blocked",
			policy:  advisor.Policy{BlockMerge: advisor.SeverityP0},
			review:  blockingMergeReview(),
			blocked: true,
			wantReason: []string{
				"advisor blocked this merge", "P0: Leaks the API key", "/advisor",
			},
		},
		{
			name:   "P0 with the merge advisory",
			policy: advisor.Policy{},
			review: blockingMergeReview(),
		},
		{
			name:       "P1 requiring confirmation, refused",
			policy:     advisor.Policy{BlockMerge: advisor.SeverityP0, RequireConfirmation: advisor.SeverityP1},
			review:     criticalMergeReview(),
			answer:     &PermGateResponse{Allow: false},
			blocked:    true,
			prompted:   true,
			wantReason: []string{"wants your call", "P1: No test for the retry path"},
		},
		{
			name:     "P1 requiring confirmation, accepted",
			policy:   advisor.Policy{BlockMerge: advisor.SeverityP0, RequireConfirmation: advisor.SeverityP1},
			review:   criticalMergeReview(),
			answer:   &PermGateResponse{Allow: true, Reason: "ship it"},
			prompted: true,
		},
		{
			name:   "P1 with confirmation off",
			policy: advisor.Policy{BlockMerge: advisor.SeverityP0},
			review: criticalMergeReview(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMergeGateFixture(t, tc.policy, tc.review)
			blocked, prompted, reason, prompt := f.run(t, "Bash", mergeInput("git merge origin/main"), tc.answer)

			if blocked != tc.blocked {
				t.Fatalf("blocked = %v, want %v (reason %q)", blocked, tc.blocked, reason)
			}
			if prompted != tc.prompted {
				t.Fatalf("prompted = %v, want %v", prompted, tc.prompted)
			}
			if f.reviewRuns != 1 {
				t.Errorf("reviews = %d, want the diff reviewed once", f.reviewRuns)
			}
			for _, want := range tc.wantReason {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q does not mention %q", reason, want)
				}
			}
			if blocked && f.nextCalls != 0 {
				t.Error("a refused merge still reached the permission rules")
			}
			if !blocked && f.nextCalls != 1 {
				t.Errorf("an allowed merge reached the permission rules %d times, want once", f.nextCalls)
			}

			// Whatever the verdict, the review the gate resolved is in the
			// history the panel reads, aimed at the merge.
			reviews := f.store.Reviews()
			if len(reviews) != 1 {
				t.Fatalf("stored reviews = %d, want the one the gate resolved", len(reviews))
			}
			stored := reviews[0]
			if stored.Trigger != advisor.TriggerBeforeMerge || stored.Target.Kind != advisor.TargetMerge {
				t.Errorf("stored review = %+v, want a pre-merge review", stored)
			}
			if blocked && !strings.Contains(reason, stored.ID) {
				t.Errorf("reason %q does not name the review %q it came from", reason, stored.ID)
			}
			if prompted {
				if !strings.Contains(prompt, stored.ID) || !strings.Contains(prompt, "P1: No test for the retry path") {
					t.Errorf("prompt %q does not carry the review's verdict", prompt)
				}
			}
		})
	}
}

// A second merge attempt on unchanged code is the same review: the diff hash is
// the identity, so a retry after a refusal costs nothing. Changed code has a new
// hash and is reviewed again.
func TestMergeGateReusesTheReviewOfTheSameDiff(t *testing.T) {
	f := newMergeGateFixture(t, advisor.Policy{BlockMerge: advisor.SeverityP0}, blockingMergeReview())

	blocked, _, first, _ := f.run(t, "Bash", mergeInput("git merge origin/main"), nil)
	if !blocked {
		t.Fatalf("the first merge was not refused: %q", first)
	}
	if f.reviewRuns != 1 {
		t.Fatalf("reviews = %d, want 1", f.reviewRuns)
	}

	// A different integration verb on the same tree is the same change.
	blocked, _, second, _ := f.run(t, "Bash", mergeInput("git push origin main"), nil)
	if !blocked {
		t.Fatalf("the repeated merge was not refused: %q", second)
	}
	if f.reviewRuns != 1 {
		t.Fatalf("reviews = %d, want the same verdict reused by diff hash", f.reviewRuns)
	}
	if first != second {
		t.Errorf("the reused verdict changed:\nfirst  = %q\nsecond = %q", first, second)
	}
	if got := len(f.store.Reviews()); got != 1 {
		t.Fatalf("stored reviews = %d, want the one review both attempts shared", got)
	}

	// The code changed, so the identical command must be reviewed afresh.
	f.diff.Hash = "hash-2"
	blocked, _, third, _ := f.run(t, "Bash", mergeInput("git merge origin/main"), nil)
	if !blocked {
		t.Fatalf("the merge of changed code was not refused: %q", third)
	}
	if f.reviewRuns != 2 {
		t.Fatalf("reviews = %d, want a fresh review for a different diff", f.reviewRuns)
	}
	reviews := f.store.Reviews()
	if len(reviews) != 2 || reviews[0].DiffHash == reviews[1].DiffHash {
		t.Fatalf("history = %+v, want two reviews of two different diffs", reviews)
	}
}

// Every call that integrates nothing goes straight to the permission rules: no
// diff read, no model call, no wait. The diff read here never returns, so a gate
// that looked at it anyway would refuse here instead of passing quietly.
func TestMergeGateLeavesOtherCallsAlone(t *testing.T) {
	f := newMergeGateFixture(t, advisor.Policy{BlockMerge: advisor.SeverityP0}, blockingMergeReview())
	f.diffGate = make(chan struct{})
	t.Cleanup(func() { close(f.diffGate) })

	for _, tc := range []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{"a test run", "Bash", mergeInput("go test ./...")},
		{"a status read", "Bash", mergeInput("git status --short")},
		{"a merge abort", "Bash", mergeInput("git merge --abort")},
		{"a non-shell tool", "Read", map[string]any{"file_path": "internal/api.go"}},
		{"another shell", "PowerShell", mergeInput("git status")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			blocked, prompted, reason, _ := f.run(t, tc.tool, tc.input, nil)
			if blocked || prompted {
				t.Fatalf("%s was gated: %q", tc.name, reason)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("%s waited %v on the merge gate", tc.name, elapsed)
			}
		})
	}
	if f.diffReads != 0 {
		t.Errorf("diff reads = %d, want none for calls that integrate nothing", f.diffReads)
	}
	if f.reviewRuns != 0 {
		t.Errorf("reviews = %d, want none for calls that integrate nothing", f.reviewRuns)
	}
}

// The whole path: the model asks for a merge, the gate refuses it, and the
// reason reaches the model as the tool's error — while the command itself never
// runs.
func TestMergeGateRejectReasonReachesTheModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	f := newMergeGateFixture(t, advisor.Policy{BlockMerge: advisor.SeverityP0}, blockingMergeReview())
	command := "git merge origin/main && touch merged"
	driver := aitest.New(
		aitest.Asks(ai.ToolCall{ID: "merge-1", Name: "Bash", Input: mustJSON(mergeInput(command))}),
		aitest.Says("I will fix the leak first."),
	)

	var toolErr error
	params := BuildParams{
		Provider: runOnceProvider{driver}, ModelID: "fake", CWD: dir,
		PermissionRules: func(string, map[string]any) PermDecisionResult {
			return PermDecisionResult{Decision: perm.Permit, Reason: "permitted by test"}
		},
		MergeGate: func() *advisor.MergeGate { return f.gate },
		OnEvent: func(ev core.Event) {
			if end, ok := ev.(sdkagent.ToolEnd); ok {
				toolErr = end.Err
			}
		},
	}
	if _, err := RunOnce(context.Background(), params, core.UserMessage("merge the branch", nil), nil); err != nil {
		t.Fatal(err)
	}

	if toolErr == nil {
		t.Fatal("the refused merge did not fail the tool call")
	}
	review := f.store.Reviews()
	if len(review) != 1 {
		t.Fatalf("stored reviews = %d, want the refusal recorded", len(review))
	}
	for _, want := range []string{"advisor blocked this merge", review[0].ID, "P0: Leaks the API key", "/advisor"} {
		if !strings.Contains(toolErr.Error(), want) {
			t.Errorf("tool error %q does not mention %q", toolErr, want)
		}
	}

	// What the model reads on its next call is the same explanation: the tool
	// result is where the reason has to be actionable.
	sent := driver.Sent()
	if len(sent) < 2 {
		t.Fatalf("the model was called %d times, want the tool result round trip", len(sent))
	}
	var result string
	for _, message := range sent[len(sent)-1].Messages {
		for _, tool := range message.Content.ToolResults() {
			if tool.ToolCallID == "merge-1" {
				result = tool.Text()
			}
		}
	}
	if result == "" {
		t.Fatal("the model was not told what the refused call returned")
	}
	if !strings.Contains(result, review[0].ID) || !strings.Contains(result, "P0: Leaks the API key") {
		t.Errorf("tool result %q does not carry the review", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "merged")); !os.IsNotExist(err) {
		t.Fatalf("the refused merge ran anyway (%v)", err)
	}
}
