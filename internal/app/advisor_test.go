package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/setting"
)

// cleanAdvisorAnswer is a review that found nothing.
const cleanAdvisorAnswer = `{"summary":"Looks fine.","findings":[]}`

// blockingAdvisorAnswer is a review that found one P0.
const blockingAdvisorAnswer = `{"summary":"Leaks a token.","findings":[{"severity":"P0","title":"Leaks a token","detail":"Logs the API key.","file":"internal/api.go","line":42}]}`

// advisorStubProvider answers every turn with one fixed text, which is all a
// review needs: the advisor reads the answer, it never streams to a user.
type advisorStubProvider struct{ answer string }

func (p *advisorStubProvider) Client(string, map[string]string) (*ai.Client, error) {
	return ai.NewClientWithDriver(aitest.Always(aitest.Says(p.answer)), ai.Model{ID: "stub-model", API: "stub"}), nil
}

func (p *advisorStubProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func (p *advisorStubProvider) Name() string { return "stub" }

// enabledAdvisor is the smallest configuration that turns the advisor on.
func enabledAdvisor() setting.AdvisorSettings {
	on := true
	return setting.AdvisorSettings{Enable: &on}
}

// newAdvisorTestModel builds a model with the advisor services in place and a
// provider that answers every review with answer. It carries no agent, so the
// review runner is exercised without a conversation.
func newAdvisorTestModel(t *testing.T, answer string) *model {
	t.Helper()
	return &model{
		mainNotices:   make(chan mainNotice, 8),
		advisorStarts: make(chan struct{}, 8),
		env: env{
			CWD:          t.TempDir(),
			LLMProvider:  &advisorStubProvider{answer: answer},
			CurrentModel: &llm.CurrentModelInfo{ModelID: "stub-model"},
			Advisor:      enabledAdvisor(),
		},
		services: services{
			Advisor: AdvisorServices{Store: advisor.NewStore(), Indicator: NewAdvisorIndicator()},
		},
	}
}

// waitAdvisorNotice returns the next notice the review runner published.
func waitAdvisorNotice(t *testing.T, ch <-chan mainNotice) mainNotice {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(10 * time.Second):
		t.Fatal("the review runner published no notice")
		return mainNotice{}
	}
}

// waitAdvisorIdle waits for the in-flight review to finish, so a test can make
// a second request without racing the first.
func waitAdvisorIdle(t *testing.T, s *advisorSession) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.busy.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the review runner never went idle")
		}
		time.Sleep(time.Millisecond)
	}
}

// A disabled advisor must cost nothing: no runner, no goroutine, no model call.
func TestWireAdvisorBuildsNothingWhenDisabled(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)

	m.env.Advisor = setting.AdvisorSettings{}
	m.wireAdvisor()
	if m.services.Advisor.session != nil {
		t.Fatal("an unset advisor built a runner")
	}

	off := false
	m.env.Advisor = setting.AdvisorSettings{Enable: &off}
	m.wireAdvisor()
	if m.services.Advisor.session != nil {
		t.Fatal("an explicitly disabled advisor built a runner")
	}

	if m.requestAdvisorReview(advisor.ReviewInput{Trigger: advisor.TriggerUserRequest}) {
		t.Fatal("a disabled advisor started a review")
	}
}

// The session override is the layer above settings, and an explicit false in it
// must survive the transcript round trip and keep the advisor off.
func TestSessionOverrideDisablesTheAdvisor(t *testing.T) {
	off := false
	blob := marshalAdvisor(setting.AdvisorSettings{Enable: &off})
	if blob == "" {
		t.Fatal("an explicit false was dropped from the persisted override")
	}

	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	m.env.Advisor = parseAdvisor(blob)
	m.wireAdvisor()
	if m.services.Advisor.session != nil {
		t.Fatal("the session override did not keep the advisor off")
	}
	if got := m.advisorSignature(); got != "" {
		t.Fatalf("a disabled advisor signed its config: %q", got)
	}
}

func TestWireAdvisorBuildsRunnerFromLiveModel(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	m.wireAdvisor()

	s := m.services.Advisor.session
	if s == nil {
		t.Fatal("an enabled advisor built no runner")
	}
	if s.modelID != "stub-model" {
		t.Errorf("review model = %q, want the live session model", s.modelID)
	}
	if s.cwd != m.env.CWD {
		t.Errorf("review cwd = %q, want %q", s.cwd, m.env.CWD)
	}
	if !s.live.Load() {
		t.Error("a fresh runner is not live")
	}
	if s.cooldown == nil {
		t.Error("a fresh runner has no cooldown")
	}
	if s.ctx.Err() != nil {
		t.Errorf("a fresh runner's context is already done: %v", s.ctx.Err())
	}
	// The block policy follows the settings: a merge is blocked on P0 by default.
	if s.policy.BlockMerge != advisor.SeverityP0 {
		t.Errorf("merge threshold = %q, want P0", s.policy.BlockMerge)
	}
	if s.policy.BlockTaskCompletion != advisor.SeverityP0 {
		t.Errorf("task threshold = %q, want P0", s.policy.BlockTaskCompletion)
	}
	if s.policy.RequireConfirmation != advisor.SeverityP1 {
		t.Errorf("confirm threshold = %q, want P1", s.policy.RequireConfirmation)
	}
}

func TestTeardownAdvisorCancelsTheSessionContext(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	m.wireAdvisor()
	s := m.services.Advisor.session
	m.services.Advisor.Indicator.Begin()

	m.teardownAdvisor()

	if m.services.Advisor.session != nil {
		t.Fatal("teardown left the runner wired")
	}
	if s.ctx.Err() == nil {
		t.Fatal("teardown left the review context live")
	}
	if s.live.Load() {
		t.Fatal("teardown left the liveness gate open")
	}
	if m.services.Advisor.Indicator.Snapshot().Running {
		t.Fatal("teardown left the status bar spinning")
	}
	m.teardownAdvisor() // idempotent: a second teardown is not an error
}

// A review that returns after the conversation is gone must not publish a
// verdict or write it into the history of a session it never inspected.
func TestReviewLandingAfterTeardownIsDropped(t *testing.T) {
	m := newAdvisorTestModel(t, blockingAdvisorAnswer)
	m.wireAdvisor()
	s := m.services.Advisor.session
	m.teardownAdvisor()

	m.runAdvisorReview(s, advisor.Request{
		Trigger: advisor.TriggerBeforeMerge,
		Target:  advisor.Target{Kind: advisor.TargetDiff},
		Diff:    "diff --git a/internal/api.go b/internal/api.go",
	})

	if got := len(m.services.Advisor.Store.Reviews()); got != 0 {
		t.Fatalf("a review from a dead session entered the history: %d reviews", got)
	}
	select {
	case n := <-m.mainNotices:
		t.Fatalf("a review from a dead session was published: %+v", n)
	default:
	}
}

// The whole path: a request starts a background review, the verdict is resolved
// by the policy, appended to the history, written to the transcript, and
// announced on the UI channel with the sidechain that lets it be replayed.
func TestAdvisorReviewIsPersistedAndPublished(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(session.CreateOptions{AgentID: "main"})
	if err != nil {
		t.Fatal(err)
	}

	m := newAdvisorTestModel(t, blockingAdvisorAnswer)
	m.services.Session = sess
	m.services.SessionStore = store
	m.wireAdvisor()

	started := m.requestAdvisorReview(advisor.ReviewInput{
		Trigger:  advisor.TriggerBeforeMerge,
		Target:   advisor.Target{Kind: advisor.TargetDiff, Label: "working tree"},
		Diff:     "diff --git a/internal/api.go b/internal/api.go\n+log(token)",
		DiffHash: "hash-1",
		Files:    []string{"internal/api.go"},
	})
	if !started {
		t.Fatal("the review did not start")
	}

	notice := waitAdvisorNotice(t, m.mainNotices)
	// The model said nothing about blocking; the policy decided, so a P0 under
	// the default merge policy blocks.
	if !strings.Contains(notice.Display, string(advisor.StatusBlocked)) {
		t.Errorf("notice = %q, want the blocked verdict", notice.Display)
	}
	if !strings.Contains(notice.Display, "P0 1") {
		t.Errorf("notice = %q, want the P0 count", notice.Display)
	}
	if !strings.Contains(notice.Display, "san --resume") {
		t.Errorf("notice = %q, want the sidechain replay hint", notice.Display)
	}

	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 1 {
		t.Fatalf("history = %d reviews, want 1", len(reviews))
	}
	stored := reviews[0]
	if stored.Status != advisor.StatusBlocked || stored.Severity != advisor.SeverityP0 {
		t.Errorf("stored review = %+v", stored)
	}
	if stored.ID == "" || stored.StartedAt.IsZero() || stored.DiffHash != "hash-1" {
		t.Errorf("stored review lost its identity: %+v", stored)
	}
	if len(stored.Findings) != 1 || stored.Findings[0].ID == "" {
		t.Errorf("stored findings = %+v", stored.Findings)
	}

	// The review is in the transcript, so a resume replays it.
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read(sess.ID())
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	persisted := history.Advisor.Reviews
	if len(persisted) != 1 {
		t.Fatalf("transcript holds %d reviews, want 1", len(persisted))
	}
	if persisted[0].ID != stored.ID || persisted[0].Status != advisor.StatusBlocked {
		t.Errorf("persisted review = %+v", persisted[0])
	}

	// The sidechain the review ran in is its own session, listed apart.
	summaries, err := store.ListAllContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sidechains := 0
	for _, summary := range summaries {
		if summary.IsSidechain {
			sidechains++
		}
	}
	if sidechains != 1 {
		t.Fatalf("sidechain sessions = %d, want 1", sidechains)
	}
}

func TestAdvisorCooldownSuppressesRepeatReview(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	m.wireAdvisor()
	s := m.services.Advisor.session
	in := advisor.ReviewInput{
		Trigger:  advisor.TriggerUserRequest,
		Target:   advisor.Target{Kind: advisor.TargetDiff},
		DiffHash: "same-diff",
	}

	if !m.requestAdvisorReview(in) {
		t.Fatal("the first review did not start")
	}
	if m.requestAdvisorReview(in) {
		t.Fatal("a second review started while the first was still running")
	}
	waitAdvisorNotice(t, m.mainNotices)
	waitAdvisorIdle(t, s)

	// The same change again is refused by the cooldown; a different change runs.
	if m.requestAdvisorReview(in) {
		t.Fatal("the cooldown did not suppress a repeat review of the same diff")
	}
	in.DiffHash = "other-diff"
	if !m.requestAdvisorReview(in) {
		t.Fatal("a different diff was suppressed by the cooldown")
	}
	waitAdvisorNotice(t, m.mainNotices)
}

// A review that cannot reach a verdict is reported as a failure, never as a
// pass, and it does not enter the history as one.
func TestAdvisorReviewFailureIsNotAPass(t *testing.T) {
	m := newAdvisorTestModel(t, "I think this looks fine to me.")
	m.wireAdvisor()
	if !m.requestAdvisorReview(advisor.ReviewInput{
		Trigger:  advisor.TriggerUserRequest,
		Target:   advisor.Target{Kind: advisor.TargetDiff},
		DiffHash: "unparseable",
	}) {
		t.Fatal("the review did not start")
	}
	notice := waitAdvisorNotice(t, m.mainNotices)
	if !strings.Contains(notice.Display, "review failed") {
		t.Errorf("notice = %q, want a failure", notice.Display)
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 0 {
		t.Fatalf("a failed review entered the history: %d reviews", got)
	}
	if snap := m.services.Advisor.Indicator.Snapshot(); snap.Err == "" {
		t.Error("the failure is not visible in the status snapshot")
	}
}

// Starting a review and tearing the session down in the same breath must not
// race the runner or leak a verdict into the next session.
func TestAdvisorStartAndTeardownAreRaceFree(t *testing.T) {
	for i := 0; i < 20; i++ {
		m := newAdvisorTestModel(t, cleanAdvisorAnswer)
		m.wireAdvisor()
		m.requestAdvisorReview(advisor.ReviewInput{
			Trigger:  advisor.TriggerUserRequest,
			Target:   advisor.Target{Kind: advisor.TargetDiff},
			DiffHash: "race",
		})
		m.teardownAdvisor()
		if m.services.Advisor.session != nil {
			t.Fatal("teardown left the runner wired")
		}
	}
}

func TestAdvisorCommandReportsDisabled(t *testing.T) {
	m := &model{services: services{
		Advisor: AdvisorServices{Store: advisor.NewStore(), Indicator: NewAdvisorIndicator()},
	}}
	line, cmd := m.advisorCommand("")
	if !strings.Contains(line, "off") {
		t.Errorf("/advisor = %q, want it to say the advisor is off", line)
	}
	if cmd != nil {
		t.Error("/advisor on a disabled advisor returned a command")
	}
}

// /advisor is reachable before the first turn has started the agent runtime,
// which is the only other place the runner is built. The command must build its
// own rather than report a disconnected model.
func TestAdvisorCommandWiresBeforeTheFirstTurn(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	if m.services.Advisor.session != nil {
		t.Fatal("the test model already has a runner")
	}

	line, _ := m.advisorCommand("status")
	if strings.Contains(line, "no model") {
		t.Fatalf("/advisor status = %q, want the runner it just built", line)
	}
	if m.services.Advisor.session == nil {
		t.Fatal("/advisor did not build a runner")
	}
	if got := m.services.Advisor.session.modelID; got != "stub-model" {
		t.Errorf("lazily wired review model = %q, want the session model", got)
	}
}

func TestAdvisorCommandStatusAndUsage(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	m.wireAdvisor()

	line, _ := m.advisorCommand("status")
	if !strings.Contains(line, "idle") || !strings.Contains(line, "stub-model") {
		t.Errorf("/advisor status = %q", line)
	}
	line, _ = m.advisorCommand("nonsense")
	if !strings.Contains(line, "Usage") {
		t.Errorf("/advisor nonsense = %q, want usage", line)
	}
}

// The drift check reads this signature on every turn, so it must change when
// anything that affects a review changes and stay put otherwise.
func TestAdvisorSignatureTracksSettingsAndModel(t *testing.T) {
	m := newAdvisorTestModel(t, cleanAdvisorAnswer)
	base := m.advisorSignature()
	if base == "" {
		t.Fatal("an enabled advisor signed its config with an empty string")
	}
	if again := m.advisorSignature(); again != base {
		t.Fatal("an unchanged configuration changed its signature")
	}

	m.env.Advisor.Review.Mode = setting.AdvisorModeDeep
	if m.advisorSignature() == base {
		t.Fatal("a review-mode change did not change the signature")
	}

	off := false
	m.env.Advisor.Enable = &off
	if got := m.advisorSignature(); got != "" {
		t.Fatalf("a disabled advisor signed its config: %q", got)
	}
}

func TestAdvisorPolicyFollowsSettings(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name  string
		cfg   setting.AdvisorSettings
		check func(*testing.T, advisor.Policy)
	}{
		{
			name: "defaults",
			cfg:  setting.AdvisorSettings{},
			check: func(t *testing.T, p advisor.Policy) {
				if p.BlockMerge != advisor.SeverityP0 || p.BlockTaskCompletion != advisor.SeverityP0 ||
					p.BlockPlanStepCompletion != advisor.SeverityP0 || p.RequireConfirmation != advisor.SeverityP1 ||
					p.AllowProceedWithRisk {
					t.Fatalf("default policy = %+v", p)
				}
			},
		},
		{
			name: "strict merge",
			cfg:  setting.AdvisorSettings{Blocks: setting.AdvisorBlocks{MergeOnP1: &on}},
			check: func(t *testing.T, p advisor.Policy) {
				if p.BlockMerge != advisor.SeverityP1 {
					t.Fatalf("merge threshold = %q, want P1", p.BlockMerge)
				}
			},
		},
		{
			name: "advisory merge",
			cfg:  setting.AdvisorSettings{Blocks: setting.AdvisorBlocks{MergeOnP0: &off}},
			check: func(t *testing.T, p advisor.Policy) {
				if p.BlockMerge != "" {
					t.Fatalf("merge threshold = %q, want advisory only", p.BlockMerge)
				}
			},
		},
		{
			name: "lenient task and step",
			cfg: setting.AdvisorSettings{Blocks: setting.AdvisorBlocks{
				TaskCompletionOnP0: &off, PlanStepCompletionOnP0: &off, RequireConfirmationOnP1: &off,
			}},
			check: func(t *testing.T, p advisor.Policy) {
				if p.BlockTaskCompletion != "" || p.BlockPlanStepCompletion != "" || p.RequireConfirmation != "" {
					t.Fatalf("policy = %+v, want nothing blocking", p)
				}
			},
		},
		{
			name: "proceed with risk",
			cfg:  setting.AdvisorSettings{Blocks: setting.AdvisorBlocks{AllowProceedWithRisk: &on}},
			check: func(t *testing.T, p advisor.Policy) {
				if !p.AllowProceedWithRisk {
					t.Fatal("the human could not proceed past a block")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, advisorPolicy(tc.cfg))
		})
	}
}
