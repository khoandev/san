// The merge gate's app wiring: the wired advisor reviews a merge before the
// permission path lets it run, the verdict lands in the session's history and
// the notice channel, and a repeat attempt on unchanged code reuses it.
package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/mcp"
)

// mergeCall is the shell call the gate exists for.
func mergeCall() map[string]any { return map[string]any{"command": "git merge origin/main"} }

// commitBase turns the workspace into a repository with one commit, so the next
// change is a diff against a base commit rather than an unborn branch.
func commitBase(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-qm", "base"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
}

// The resolver is read per call, because the agent runtime outlives the advisor
// session that supplies the gate: an unwired advisor resolves nothing, a wired
// one resolves its gate, and a torn-down one stops gating at once.
func TestMergeGateResolverFollowsTheAdvisorSession(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	if gate := m.mergeGateResolver()(); gate != nil {
		t.Fatal("an unwired advisor resolved a merge gate")
	}

	m.wireAdvisor()
	if m.mergeGateResolver()() == nil {
		t.Fatal("a wired advisor resolved no merge gate")
	}

	m.teardownAdvisor()
	if gate := m.mergeGateResolver()(); gate != nil {
		t.Fatal("a torn-down advisor still resolved its gate")
	}
}

// The runtime the app builds carries the gate: buildAgentParams is the one
// place the permission path is composed, so a resolver missing there would
// leave every interactive merge unreviewed.
func TestBuildAgentParamsCarriesTheMergeGate(t *testing.T) {
	m, _ := resumedModel(t, nil)
	m.services.MCP = mcp.NewManagerForTest(nil)
	m.services.Advisor = newAdvisorServices()
	m.env.LLMProvider = &advisorStubProvider{answer: cleanAdvisorAnswer}
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "stub-model"}
	m.env.Advisor = enabledAdvisor()
	m.mainNotices = make(chan mainNotice, 8)
	m.advisorStarts = make(chan struct{}, 8)
	m.wireAdvisor()
	t.Cleanup(m.teardownAdvisor)

	params, err := m.buildAgentParams()
	if err != nil {
		t.Fatal(err)
	}
	if params.MergeGate == nil {
		t.Fatal("the built params carry no merge gate resolver")
	}
	if params.MergeGate() == nil {
		t.Fatal("the resolver found no gate while the advisor is live")
	}

	m.teardownAdvisor()
	if params.MergeGate() != nil {
		t.Fatal("the resolver still answers for a torn-down advisor")
	}
}

// The whole path in the app: a P0 review refuses the merge, the verdict is in
// the history the /advisor panel reads, and the notice reaches the UI channel.
// A second attempt on the same diff reuses the review instead of paying for
// another.
func TestMergeGateReviewsTheDiffAndAnnouncesTheVerdict(t *testing.T) {
	m := newStepReviewModel(t, blockingAdvisorAnswer)
	// A base commit plus one changed file, so the gate reads a whole diff
	// against base — the uncommitted work on top of the branch.
	commitBase(t, m.env.CWD)
	changed := filepath.Join(m.env.CWD, "internal_api.go")
	if err := os.WriteFile(changed, []byte("func main() { log(token) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	gate := m.services.Advisor.mergeGate()
	if gate == nil {
		t.Fatal("the wired advisor built no merge gate")
	}
	out := gate.Check(context.Background(), "Bash", mergeCall())
	if out.Verdict != advisor.MergeReject {
		t.Fatalf("verdict = %v, want the merge refused (%s)", out.Verdict, out.Reason)
	}
	if out.Review.ID == "" || !strings.Contains(out.Reason, out.Review.ID) {
		t.Fatalf("reason %q does not name the review %q", out.Reason, out.Review.ID)
	}
	if !strings.Contains(out.Reason, "advisor blocked this merge") || !strings.Contains(out.Reason, "P0") {
		t.Fatalf("reason %q does not state the verdict", out.Reason)
	}

	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 1 {
		t.Fatalf("history = %d reviews, want the one merge review", len(reviews))
	}
	stored := reviews[0]
	if stored.Trigger != advisor.TriggerBeforeMerge || stored.Target.Kind != advisor.TargetMerge {
		t.Errorf("stored review = %+v, want a pre-merge review", stored)
	}
	if stored.DiffHash == "" || stored.Target.Base == "" {
		t.Errorf("stored review lost the diff it covered: %+v", stored)
	}
	if stored.ID != out.Review.ID {
		t.Errorf("stored review = %q, want the verdict's %q", stored.ID, out.Review.ID)
	}
	// The panel and the status bar read the same review.
	if snap := m.services.Advisor.Indicator.Snapshot(); snap.Latest == nil || snap.Latest.ID != stored.ID {
		t.Errorf("status snapshot = %+v, want the merge review", snap)
	}
	notice := waitAdvisorNotice(t, m.mainNotices)
	if !strings.Contains(notice.Display, string(advisor.StatusBlocked)) {
		t.Errorf("notice = %q, want the blocked verdict", notice.Display)
	}

	// The same diff is the same review: a retry after the refusal costs
	// nothing and cannot drift into a different verdict.
	again := gate.Check(context.Background(), "Bash", mergeCall())
	if again.Verdict != advisor.MergeReject || again.Review.ID != stored.ID {
		t.Fatalf("repeat verdict = %v/%q, want the stored %q", again.Verdict, again.Review.ID, stored.ID)
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 1 {
		t.Fatalf("history = %d reviews, want the reused one", got)
	}
}

// A review that cannot run is not a pass: with merges blocked on P0 the gate
// refuses, and the error review explains why in the history and the notice.
func TestMergeGateFailsClosedWhenTheReviewCannotRun(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer) // outside any repository
	m.wireAdvisor()

	out := m.services.Advisor.mergeGate().Check(context.Background(), "Bash", mergeCall())
	if out.Verdict != advisor.MergeReject {
		t.Fatalf("verdict = %v, want the merge refused while no review is possible", out.Verdict)
	}
	if !strings.Contains(out.Reason, "could not review this merge") {
		t.Errorf("reason = %q, want the failure stated", out.Reason)
	}
	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 1 || reviews[0].Status != advisor.StatusError {
		t.Fatalf("history = %+v, want the one error review", reviews)
	}
	if !strings.Contains(reviews[0].Error, "git") && reviews[0].Error == "" {
		t.Errorf("error review = %+v, want the cause recorded", reviews[0])
	}
	notice := waitAdvisorNotice(t, m.mainNotices)
	if !strings.Contains(notice.Display, "review failed") {
		t.Errorf("notice = %q, want the failure announced", notice.Display)
	}
}
