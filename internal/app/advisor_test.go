package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/task"
)

// The review reads this text and nothing else, so it has to carry what
// happened — who asked, what the agent said, which tools ran — without the
// bulk of a full command output.
func TestRenderAdvisorTranscriptKeepsWhatHappened(t *testing.T) {
	msgs := []core.Message{
		core.UserMessage("fix the retry loop", nil),
		core.AssistantMessage("I will read the file.", "", []core.ToolCall{{ID: "c1", Name: "Read", Input: `{"file_path":"retry.go"}`}}),
		core.ToolResultMessage(core.ToolResult{
			ToolCallID: "c1", ToolName: "Read", Content: ai.TextContent(strings.Repeat("x", advisorToolResultBudget*3)),
		}),
		core.AssistantMessage("Done: the backoff now resets.", "", nil),
	}

	got := renderAdvisorTranscript(msgs)
	for _, want := range []string{"User: fix the retry loop", "Assistant: I will read the file.", "[tool call: Read]", "[tool result: Read]", "Assistant: Done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript is missing %q:\n%s", want, got)
		}
	}
	// One huge output must not crowd out the conversation it belongs to.
	if len(got) > advisorTranscriptBudget+advisorToolResultBudget {
		t.Fatalf("transcript = %d bytes, want it bounded", len(got))
	}
}

// The delta's first user input is what opened the turn: the advisor's note, or
// something the person wrote.
func TestDeltaOpenedByAdvisory(t *testing.T) {
	tests := []struct {
		name string
		msgs []core.Message
		want bool
	}{
		{"empty delta", nil, false},
		{"user asked", []core.Message{core.UserMessage("carry on", nil)}, false},
		{"advisor note", []core.Message{core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil)}, true},
		{"note then agent work", []core.Message{
			core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil),
			core.AssistantMessage("Fixed the nil case.", "", nil),
		}, true},
		{"later note does not open the delta", []core.Message{
			core.UserMessage("carry on", nil),
			core.AssistantMessage("Done.", "", nil),
			core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil),
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deltaOpenedByAdvisory(tt.msgs); got != tt.want {
				t.Fatalf("deltaOpenedByAdvisory = %v, want %v", got, tt.want)
			}
		})
	}
}

// The note is shown as one notice line, so a multi-line note must collapse to
// its first real sentence rather than stretch the conversation.
func TestAdvisorNoteLine(t *testing.T) {
	if got := advisorNoteLine("\n\n- the test asserts the old behaviour\n- second point"); got != "the test asserts the old behaviour" {
		t.Fatalf("note line = %q", got)
	}
	if got := advisorNoteLine(""); got != "note" {
		t.Fatalf("empty note line = %q, want a placeholder", got)
	}
	long := advisorNoteLine(strings.Repeat("á", 400))
	if len(long) > 120+len("…") || !strings.HasSuffix(long, "…") {
		t.Fatalf("long note line = %q, want it clipped", long)
	}
}

// clipText cuts on a rune boundary: half a rune in the transcript would make
// the whole review request invalid.
func TestClipTextCutsOnRuneBoundary(t *testing.T) {
	got := clipText(strings.Repeat("á", 10), 5)
	if !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("clipText = %q, want a clipped rune-aligned string", got)
	}
	if len(got) > 5+len("…") {
		t.Fatalf("clipText = %q, want it no longer than the budget", got)
	}
	if got := clipText("short", 100); got != "short" {
		t.Fatalf("clipText shortened text that fits: %q", got)
	}
}

// reviewModel builds the least a finished turn needs: a conversation to review
// and an advisor session that refuses to start a review, so the trigger's own
// bookkeeping is what the test observes.
func reviewModel(t *testing.T, msgs []core.Message, s *advisorSession) *model {
	t.Helper()
	m := &model{}
	m.services.Agent = &agent.Runtime{}
	m.services.Agent.SetMessages(msgs)
	m.services.Advisor = s
	return m
}

func liveAdvisorSession() (*advisorSession, *atomic.Bool) {
	live := &atomic.Bool{}
	live.Store(true)
	return &advisorSession{live: live, ctx: context.Background()}, live
}

// Every turn that produced work is reviewed, however it ended: a cancel or a
// failed inference is not work, but the output cap, the step cap and a hook
// ending the turn all leave a conversation worth a second look.
func TestReviewCompletedTurnStopReasons(t *testing.T) {
	msgs := []core.Message{core.UserMessage("fix it", nil), core.AssistantMessage("fixed", "", nil)}
	tests := []struct {
		reason core.StopReason
		review bool
	}{
		{core.StopEndTurn, true},
		{core.StopTruncated, true},
		{core.StopMaxSteps, true},
		{core.StopHook, true},
		{core.StopRefused, true},
		{core.StopCanceled, false},
		{core.StopError, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			s, _ := liveAdvisorSession()
			m := reviewModel(t, msgs, s)
			started := make(chan string, 1)
			m.advisorReview = func(_ *advisorSession, transcript string) { started <- transcript }
			m.reviewCompletedTurn(core.Result{StopReason: tt.reason})
			select {
			case <-started:
				if !tt.review {
					t.Fatalf("a %s turn started a review", tt.reason)
				}
			case <-time.After(50 * time.Millisecond):
				if tt.review {
					t.Fatalf("a %s turn never reached the advisor", tt.reason)
				}
			}
			if !tt.review && m.advisorReviewed != 0 {
				t.Fatalf("a %s turn moved the cursor to %d", tt.reason, m.advisorReviewed)
			}
		})
	}
}

// A background task completion arrives as a notice and reaches the running
// turn, so the advisor reviews that work when the turn ends.
func TestTaskCompletionTurnReachesTheAdvisor(t *testing.T) {
	m, provider := noticeDeliveryModel(t)
	attachTurnSession(t, m)
	m.services.Hook = hook.NewEngine(setting.NewData(), "", t.TempDir(), "")
	m.env.Width = 80
	m.services.Task = task.NewManager()
	m.wireTaskLifecycle(nil)
	t.Cleanup(func() { task.SetLifecycleHandler(nil) })
	m.conv.Stream.Active = true // the agent is mid-turn when the worker finishes

	s, _ := liveAdvisorSession()
	s.cancel = func() {}
	m.services.Advisor = s
	reviews := make(chan string, 1)
	m.advisorReview = func(_ *advisorSession, transcript string) { reviews <- transcript }

	worker := m.services.Task.CreateAgentTask("worker", "Backend", "research", context.Background(), func() {})
	worker.Complete(nil)
	msg, ok := taskCompletionMessage(worker.GetStatus())
	if !ok {
		t.Fatal("completed worker produced no notice")
	}
	m.deliverNotice(fromBrokerMessage(msg))
	select {
	case chain := <-provider.requests:
		delivered := false
		for _, got := range chain {
			if strings.Contains(got.Text(), "task-notification") {
				delivered = true
			}
		}
		if !delivered {
			t.Fatalf("the completion notice never reached the agent: %+v", chain)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the completion notice never reached the running turn")
	}
	stopRuntime(t, m.services.Agent)

	// The turn over that input ends here, as the runtime reports it.
	m.OnTurnEnd(core.Result{StopReason: core.StopEndTurn})

	select {
	case transcript := <-reviews:
		if !strings.Contains(transcript, "task-notification") {
			t.Fatalf("the completion turn was not what the advisor read:\n%s", transcript)
		}
	case <-time.After(time.Second):
		t.Fatal("a completed background task's turn never reached the advisor")
	}
}

// A turn that ends while a review is still running is queued: the cursor stays
// where it is so the delta is not lost, and the pending flag records the work.
func TestReviewCompletedTurnQueuesWhileBusy(t *testing.T) {
	s, _ := liveAdvisorSession()
	s.busy.Store(true)
	msgs := []core.Message{core.UserMessage("fix it", nil), core.AssistantMessage("fixed", "", nil)}
	m := reviewModel(t, msgs, s)

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	if !m.advisorPending {
		t.Fatal("a turn that ended mid-review was not queued")
	}
	if m.advisorReviewed != 0 {
		t.Fatalf("cursor = %d while a review runs, want it unchanged", m.advisorReviewed)
	}
}

// The review that follows reads every turn that ended while the previous one
// ran, once, and only then does the cursor catch up.
func TestAdvisorDrainReviewsTheQueuedTurns(t *testing.T) {
	s, _ := liveAdvisorSession()
	s.busy.Store(true)
	m := reviewModel(t, []core.Message{core.UserMessage("first", nil), core.AssistantMessage("one", "", nil)}, s)
	reviews := make(chan string, 2)
	m.advisorReview = func(_ *advisorSession, transcript string) { reviews <- transcript }

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	m.services.Agent.SetMessages(append(m.services.Agent.Messages(),
		core.UserMessage("second", nil), core.AssistantMessage("two", "", nil)))
	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	if !m.advisorPending || m.advisorReviewed != 0 {
		t.Fatalf("queued state = pending %v, cursor %d", m.advisorPending, m.advisorReviewed)
	}

	s.busy.Store(false) // The previous review finished and signalled.
	m.drainAdvisorPending()
	select {
	case transcript := <-reviews:
		for _, want := range []string{"first", "one", "second", "two"} {
			if !strings.Contains(transcript, want) {
				t.Fatalf("queued review is missing %q:\n%s", want, transcript)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("the queued review never started")
	}
	if m.advisorPending {
		t.Fatal("the drain left the review queued")
	}
	if want := len(m.services.Agent.Messages()); m.advisorReviewed != want {
		t.Fatalf("cursor = %d after the drain, want %d", m.advisorReviewed, want)
	}
}

// Each message is reviewed once: a turn end with nothing new starts no review,
// so an already-reviewed delta is never replayed.
func TestAdvisorNeverReviewsTheSameMessageTwice(t *testing.T) {
	s, _ := liveAdvisorSession()
	m := reviewModel(t, []core.Message{core.UserMessage("fix it", nil), core.AssistantMessage("fixed", "", nil)}, s)
	reviews := make(chan string, 4)
	m.advisorReview = func(_ *advisorSession, transcript string) { reviews <- transcript }

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	select {
	case <-reviews:
	case <-time.After(time.Second):
		t.Fatal("the first review never started")
	}
	s.busy.Store(false) // The review finished; nothing new followed it.

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	select {
	case transcript := <-reviews:
		t.Fatalf("a reviewed message was reviewed again:\n%s", transcript)
	case <-time.After(50 * time.Millisecond):
	}
	if m.advisorPending {
		t.Fatal("an empty delta was queued as pending")
	}
}

// The completion signal reaches the main loop through the buffered channel, and
// the handler it wakes starts the queued review.
func TestAdvisorCompletionDrainsThroughTheMainLoop(t *testing.T) {
	s, _ := liveAdvisorSession()
	s.busy.Store(true)
	m := reviewModel(t, []core.Message{core.UserMessage("fix it", nil), core.AssistantMessage("fixed", "", nil)}, s)
	m.advisorDone = make(chan struct{}, 1)
	started := make(chan struct{}, 1)
	m.advisorReview = func(*advisorSession, string) { started <- struct{}{} }

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	s.busy.Store(false)
	m.publishAdvisorDone()
	select {
	case <-m.advisorDone:
	default:
		t.Fatal("the review completion was not signalled")
	}
	m.onAdvisorDone()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("the completion handler did not start the queued review")
	}
}

// A turn the advisor's note opened is reviewed like any other: the agent's fix
// is exactly what needs a second look. The note itself stays out of the
// transcript.
func TestReviewCompletedTurnReviewsAnAdvisoryTurn(t *testing.T) {
	s, _ := liveAdvisorSession()
	msgs := []core.Message{
		core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil),
		core.AssistantMessage("Checked it.", "", nil),
	}
	m := reviewModel(t, msgs, s)
	reviews := make(chan string, 1)
	m.advisorReview = func(_ *advisorSession, transcript string) { reviews <- transcript }

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	select {
	case transcript := <-reviews:
		if strings.Contains(transcript, "<advisory") {
			t.Fatalf("the note was fed back into the review:\n%s", transcript)
		}
		if !strings.Contains(transcript, "Checked it.") {
			t.Fatalf("the agent's answer was not reviewed:\n%s", transcript)
		}
	case <-time.After(time.Second):
		t.Fatal("an advisory-opened turn was not reviewed")
	}
	if m.advisorFollowups != 1 {
		t.Fatalf("followups = %d, want 1", m.advisorFollowups)
	}
	if m.advisorReviewed != len(msgs) {
		t.Fatalf("cursor = %d, want %d", m.advisorReviewed, len(msgs))
	}
}

// The cap stops advice about advice: the fourth consecutive advisory-opened
// turn is skipped with the cursor moving past it, and the next note starts a
// fresh budget.
func TestAdvisorFollowupCapSkipsTheFourthTurn(t *testing.T) {
	s, _ := liveAdvisorSession()
	msgs := []core.Message{
		core.UserMessage("<advisory>\n- check the nil case\n</advisory>", nil),
		core.AssistantMessage("Checked it.", "", nil),
	}
	m := reviewModel(t, msgs, s)
	reviews := make(chan string, 1)
	m.advisorReview = func(_ *advisorSession, transcript string) { reviews <- transcript }
	m.advisorFollowups = advisorMaxFollowups

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	select {
	case <-reviews:
		t.Fatal("a capped advisory turn started another review")
	default:
	}
	if m.advisorReviewed != len(msgs) {
		t.Fatalf("cursor = %d, want %d", m.advisorReviewed, len(msgs))
	}
	if m.advisorFollowups != 0 {
		t.Fatalf("followups = %d after the cap, want a reset", m.advisorFollowups)
	}
}

// A turn the person opened ends the run, so the next note starts with its full
// budget again.
func TestReviewCompletedTurnResetsFollowupsOnAUserTurn(t *testing.T) {
	s, _ := liveAdvisorSession()
	msgs := []core.Message{core.UserMessage("fix it", nil), core.AssistantMessage("fixed", "", nil)}
	m := reviewModel(t, msgs, s)
	m.advisorReview = func(*advisorSession, string) {}
	m.advisorFollowups = advisorMaxFollowups - 1

	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	if m.advisorFollowups != 0 {
		t.Fatalf("followups = %d after a user turn, want 0", m.advisorFollowups)
	}
}

// The same note twice is noise, not a second finding: it is injected once.
func TestAdvisorAcceptsEachNoteOnce(t *testing.T) {
	s, _ := liveAdvisorSession()
	if !s.acceptNote("- the test asserts the old behaviour") {
		t.Fatal("the first note was rejected")
	}
	if s.acceptNote("- the   test asserts the OLD behaviour") {
		t.Fatal("a reworded duplicate note was injected again")
	}
	if !s.acceptNote("- a different finding") {
		t.Fatal("a new note was rejected")
	}
}

// The status bar follows the indicator: idle, reviewing, then what the last
// review found or failed on.
func TestStatusBarShowsTheAdvisorState(t *testing.T) {
	on := true
	s, _ := liveAdvisorSession()
	m := model{
		services: services{LLM: &llm.Conn{}, Hook: hook.NewEngine(setting.NewData(), "", t.TempDir(), "")},
		plan:     plan.NewStore(),
	}
	m.services.Advisor = s
	m.services.AdvisorIndicator = NewAdvisorIndicator()
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.env.Width = 120
	status := func() string { return ansi.Strip(m.renderModeStatus()) }

	if got := status(); !strings.Contains(got, "advisor") {
		t.Fatalf("an enabled advisor left no badge: %q", got)
	}
	m.services.AdvisorIndicator.BeginReview()
	if got := status(); !strings.Contains(got, "advisor · reviewing") {
		t.Fatalf("reviewing badge = %q", got)
	}
	m.services.AdvisorIndicator.Finish("- check the nil case", nil)
	if got := status(); !strings.Contains(got, "advisor · noted") {
		t.Fatalf("noted badge = %q", got)
	}
	m.services.AdvisorIndicator.Finish("", nil)
	if got := status(); !strings.Contains(got, "advisor · checked") {
		t.Fatalf("checked badge = %q", got)
	}
	m.services.AdvisorIndicator.Finish("", errors.New("review timed out"))
	if got := status(); !strings.Contains(got, "advisor · error") {
		t.Fatalf("error badge = %q", got)
	}

	off := false
	m.env.Advisor.Enable = &off
	if got := status(); strings.Contains(got, "advisor") {
		t.Fatalf("a disabled advisor still rendered: %q", got)
	}
}

// The status line shows each role's request count as "role count", fed by the
// session counter rather than by the transcript.
func TestStatusBarShowsAgentUsage(t *testing.T) {
	m := model{
		services: services{
			LLM:   &llm.Conn{},
			Hook:  hook.NewEngine(setting.NewData(), "", t.TempDir(), ""),
			Usage: NewAgentUsage(),
		},
		plan: plan.NewStore(),
	}
	m.services.Usage.Add("quick", 2)
	m.services.Usage.Add("advisor", 3)
	m.env.Width = 120

	got := ansi.Strip(m.renderModeStatus())
	for _, want := range []string{"quick 2", "advisor 3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status line %q is missing %q", got, want)
		}
	}

	m.services.Usage.Reset()
	if got := ansi.Strip(m.renderModeStatus()); strings.Contains(got, "quick 2") || strings.Contains(got, "advisor 3") {
		t.Fatalf("cleared usage still rendered: %q", got)
	}
}

// /advisor and the badge read the same snapshot.
func TestAdvisorStatusReadsTheIndicator(t *testing.T) {
	on := true
	m := &model{}
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.services.AdvisorIndicator = NewAdvisorIndicator()
	m.services.Advisor = &advisorSession{modelID: "review-model"}

	m.services.AdvisorIndicator.BeginReview()
	if line := m.advisorStatusLine(); !strings.Contains(line, "reviewing") {
		t.Fatalf("status = %q, want the review reported", line)
	}
	m.services.AdvisorIndicator.Finish("- check the nil case", nil)
	line := m.advisorStatusLine()
	if !strings.Contains(line, "last: check the nil case") {
		t.Fatalf("status = %q, want the last note", line)
	}
}

// A rewire is not a new conversation. The cursor lives on the model so a
// settings save, a cwd change, a role switch or a rebuilt runtime cannot move
// it: otherwise the delta the previous turn produced would be skipped without
// ever reaching a review.
func TestWireAdvisorKeepsTheReviewCursor(t *testing.T) {
	on := true
	m := advisorRoleModel(t, nil)
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.advisorReviewed = 1

	m.wireAdvisor()
	if m.services.Advisor == nil {
		t.Fatal("the advisor was not wired")
	}
	if m.advisorReviewed != 1 {
		t.Fatalf("rewire moved the cursor to %d, want 1", m.advisorReviewed)
	}
}

// A conversation that has never been wired starts from its own end: enabling
// the advisor mid-session — or resuming one, which resets the cursor to -1 —
// must not hand the whole existing history to the reviewer.
func TestWireAdvisorStartsAtTheEndOfAnExistingConversation(t *testing.T) {
	on := true
	m := advisorRoleModel(t, nil)
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.services.Agent = &agent.Runtime{}
	msgs := []core.Message{core.UserMessage("old", nil), core.AssistantMessage("done", "", nil)}
	m.services.Agent.SetMessages(msgs)
	m.advisorReviewed = -1

	m.wireAdvisor()
	if m.advisorReviewed != len(msgs) {
		t.Fatalf("cursor = %d, want the end of the conversation (%d)", m.advisorReviewed, len(msgs))
	}
}

// The delta that accumulated before a rewire is the next turn's work: the
// cursor still points at it after the rewire, and the next turn end consumes it.
func TestRewireKeepsAnUnreviewedDelta(t *testing.T) {
	on := true
	m := advisorRoleModel(t, nil)
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.services.Agent = &agent.Runtime{}
	msgs := []core.Message{
		core.UserMessage("first", nil),
		core.AssistantMessage("done", "", nil),
		core.UserMessage("second", nil),
	}
	m.services.Agent.SetMessages(msgs)
	m.advisorReviewed = 1 // "first" and its answer were reviewed; "second" was not.

	m.wireAdvisor()
	if m.advisorReviewed != 1 {
		t.Fatalf("rewire moved the cursor to %d, want 1", m.advisorReviewed)
	}
	reviews := make(chan string, 1)
	m.advisorReview = func(_ *advisorSession, transcript string) { reviews <- transcript }
	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
	select {
	case transcript := <-reviews:
		if !strings.Contains(transcript, "second") {
			t.Fatalf("the unreviewed delta was not reviewed:\n%s", transcript)
		}
	case <-time.After(time.Second):
		t.Fatal("the unreviewed delta was never reviewed")
	}
	if m.advisorReviewed != len(msgs) {
		t.Fatalf("cursor = %d after the turn, want %d", m.advisorReviewed, len(msgs))
	}
}

// A disabled advisor is the zero session, so the per-turn trigger must be a nil
// check rather than a settings read that could panic before the agent starts.
func TestReviewCompletedTurnWithoutAdvisor(t *testing.T) {
	m := reviewModel(t, []core.Message{core.AssistantMessage("done", "", nil)}, nil)
	m.reviewCompletedTurn(core.Result{StopReason: core.StopEndTurn})
}

// Off costs nothing: no session, no goroutine, no model call.
func TestWireAdvisorBuildsNothingWhenDisabled(t *testing.T) {
	m := &model{}
	m.wireAdvisor()
	if m.services.Advisor != nil {
		t.Fatal("a disabled advisor was wired")
	}
}

// Enabled with nothing connected is a configuration the person can create, and
// it must not arm a session that would fail every turn.
func TestWireAdvisorNeedsAModel(t *testing.T) {
	on := true
	m := &model{}
	m.env.Advisor = setting.AdvisorSettings{Enable: &on}
	m.wireAdvisor()
	if m.services.Advisor != nil {
		t.Fatal("an advisor with no connected model was wired")
	}
}

// Teardown is what cancels an in-flight review: the session context is the only
// handle the review goroutine has on the session that started it.
func TestTeardownAdvisorCancelsTheSessionContext(t *testing.T) {
	m := &model{}
	ctx, cancel := context.WithCancel(context.Background())
	live := &atomic.Bool{}
	live.Store(true)
	m.services.Advisor = &advisorSession{ctx: ctx, cancel: cancel, live: live}

	m.teardownAdvisor()
	if ctx.Err() == nil {
		t.Fatal("teardown did not cancel the session context")
	}
	if live.Load() || m.services.Advisor != nil {
		t.Fatal("teardown left the session reachable")
	}
	m.teardownAdvisor() // Idempotent: a second teardown has nothing to do.
}

// /advisor [on|off|status] pins the session without touching settings.json, and
// reports what the layer is actually doing.
func TestAdvisorCommandTogglesTheSession(t *testing.T) {
	m := &model{}

	line, _ := m.advisorCommand("status")
	if line != "advisor · off" {
		t.Fatalf("status = %q, want the advisor off", line)
	}

	line, _ = m.advisorCommand("on")
	if line == "" || !m.env.Advisor.Enabled() {
		t.Fatalf("on = %q, want the session's config enabled", line)
	}
	line, _ = m.advisorCommand("")
	if !strings.Contains(line, "no model connected") {
		t.Fatalf("status after on = %q, want the missing model reported", line)
	}

	m.advisorCommand("off")
	if m.env.Advisor.Enabled() || m.env.Advisor.Enable == nil {
		t.Fatalf("off = %#v, want an explicit session disable", m.env.Advisor)
	}
	if line, _ := m.advisorCommand("bogus"); !strings.HasPrefix(line, "Usage:") {
		t.Fatalf("usage = %q", line)
	}
}
