// Global advisor wire-up. The advisor is an independent reviewer: after each
// completed turn it reads the new part of the conversation, may inspect the
// workspace with read-only tools, and — when it has something concrete to say —
// injects one short note back into the main conversation.
//
// It never blocks an action and never edits anything. The note is delivered
// through the main-loop notice channel, which shows it in the conversation and
// hands it to the agent as the next turn's input.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/session"
)

const (
	// advisorAgentID names the sidechain session each review writes, so
	// `san --resume <fork-id>` replays exactly that review's LLM calls and the
	// inspector can tell a review apart from the conversation it inspected.
	advisorAgentID = "advisor-review"
	// advisorMaxTokens caps one review's output.
	advisorMaxTokens = 8000
	// advisorTranscriptBudget caps the delta text handed to one review. A
	// larger delta is truncated from the oldest end.
	advisorTranscriptBudget = 96 * 1024
	// advisorToolResultBudget and advisorToolInputBudget keep one huge command
	// output from crowding out the conversation it belongs to.
	advisorToolResultBudget = 2000
	advisorToolInputBudget  = 600
	// advisorMaxFollowups caps consecutive reviews of turns the advisor's own
	// note opened. Advice about advice is not worth a model call, so the run
	// stops after this many and starts again on a turn the person opened.
	advisorMaxFollowups = 3
)

// advisorSession is one live advisor for the current conversation session. It
// is nil when the advisor is off, so a disabled advisor costs no goroutine and
// no model call.
type advisorSession struct {
	// provider and modelID are the resolved review model. The client is rebuilt
	// per review because its session id names that review's sidechain.
	provider llm.Provider
	modelID  string

	// ctx is the session-scoped context every review inherits, and cancel
	// unblocks an in-flight fork immediately on /clear or quit.
	ctx    context.Context
	cancel context.CancelFunc

	// live flips false on teardown so a review landing afterwards drops
	// silently instead of publishing into a dead session.
	live *atomic.Bool

	// busy enforces one review at a time. A turn that ends while a review runs
	// is remembered by the model's pending flag and reviewed once the running
	// review finishes, so its delta is queued rather than dropped.
	busy atomic.Bool

	// parentSessionID and cwd are captured at wire time so the review goroutine
	// never reads UI state.
	parentSessionID string
	cwd             string

	// mu guards the note dedupe and the last failure, which the indicator
	// mirrors for display.
	mu       sync.Mutex
	lastNote string
	lastErr  string
}

// wireAdvisor builds the session's advisor when it is enabled and a model is
// connected. Called after Agent.Start so the conversation's identity exists; a
// disabled or invalid configuration builds nothing.
func (m *model) wireAdvisor() {
	// Tear down first: ensureAgentRuntime can re-enter after a stopped agent
	// and would otherwise overwrite the cancel un-called.
	m.teardownAdvisor()

	cfg := m.env.Advisor
	if !cfg.Enabled() {
		return
	}
	provider, modelID := m.resolveAdvisorModel()
	if provider == nil || modelID == "" {
		log.Logger().Warn("advisor is enabled but no model is connected")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	live := &atomic.Bool{}
	live.Store(true)
	s := &advisorSession{
		provider:        provider,
		modelID:         modelID,
		ctx:             ctx,
		cancel:          cancel,
		live:            live,
		parentSessionID: m.sessionID(),
		cwd:             m.env.CWD,
	}
	// Enabling mid-session starts from here: replaying the whole old
	// conversation on the first enabled turn would spend tokens on work the
	// person has already moved past. Only the first wire of a conversation
	// initializes the cursor — a later rewire (a settings save, a cwd change, a
	// model-role switch, a rebuilt runtime) must keep it, or the delta the
	// previous turn produced would be skipped without ever being reviewed.
	if m.advisorReviewed < 0 {
		m.advisorReviewed = 0
		if m.services.Agent != nil {
			m.advisorReviewed = len(m.services.Agent.Messages())
		}
	}
	m.services.Advisor = s
}

// syncAdvisor rewires the session's advisor when the live settings no longer
// describe the wired one: an /advisor toggle, a settings save, a model switch.
// It runs on the turn path, where every drift source converges, and leaves the
// conversation's agent runtime alone — the advisor is not part of its build.
func (m *model) syncAdvisor() {
	if m.advisorSignature() == m.agentAdvisorSignature {
		return
	}
	m.wireAdvisor()
	m.agentAdvisorSignature = m.advisorSignature()
}

// teardownAdvisor unwires the session's advisor: it cancels the session-scoped
// context, flips the liveness gate false and drops the run. Idempotent.
func (m *model) teardownAdvisor() {
	s := m.services.Advisor
	if s == nil {
		return
	}
	s.cancel()
	s.live.Store(false)
	m.services.Advisor = nil
}

// advisorRole is the model role a review runs on. The advisor owns a slot of
// its own in the role catalog, so this is a constant: which model fills it is a
// routing choice made in /models like any other role's, and an unbound role
// reviews on the session model. The retired advisor.model and
// agents.roles.advisor spellings fold onto that binding when settings load.
const advisorRole = llm.RoleAdvisor

// resolveAdvisorModel picks the review provider and model: the advisor role
// resolved through the live modelRoles bindings, else the session model. A nil
// provider means nothing is connected and no review can run.
func (m *model) resolveAdvisorModel() (llm.Provider, string) {
	target := m.resolveRole(advisorRole)
	return target.Provider, target.ModelID
}

// advisorSignature is a stable fingerprint of everything that changes what a
// review does: whether it runs and the model the advisor role resolves to.
// ensureAgentRuntime compares it against the live runner's value, so an
// /advisor toggle, a role reboot in /models, a settings-file edit or a model
// switch rebuilds the runner on the next turn.
func (m *model) advisorSignature() string {
	if !m.env.Advisor.Enabled() {
		return ""
	}
	_, modelID := m.resolveRoleModel(advisorRole)
	return "advisor\x00" + modelID
}

// reviewCompletedTurn starts a review of what this turn added to the
// conversation. Called from OnTurnEnd on the UI goroutine; the review itself
// runs elsewhere.
//
// One review runs at a time. A turn that ends while another review is still in
// flight is queued instead of dropped: the cursor stays where it is, and the
// completion signal drains the accumulated delta once the running review
// finishes. That is what keeps the advisor from silently skipping a turn — the
// bug where the cursor advanced before the busy check.
func (m *model) reviewCompletedTurn(result core.Result) {
	s := m.services.Advisor
	if s == nil || !s.live.Load() {
		return
	}
	if !advisorReviewsTurn(result) {
		m.services.AdvisorIndicator.Skip(string(result.StopReason))
		return
	}
	if s.busy.Load() {
		m.advisorPending = true
		m.services.AdvisorIndicator.Skip("queued")
		return
	}
	m.startAdvisorReview(s)
}

// advisorReviewsTurn reports whether a turn that ended this way is work the
// advisor should read. A cancelled turn says nothing about the work, and an
// errored inference is a failure rather than an outcome. Every other ending —
// a normal stop, the output cap, the step cap, a hook ending the turn — left
// real work behind.
//
// The checks are defensive rather than reachable from today's trigger:
// OnTurnEnd returns before calling it on a cancel, and the SDK never emits a
// turn end for StopError (core/run.go treats a failed inference as an agent
// stop). They stay so a second call path cannot skip or review those turns
// silently.
func advisorReviewsTurn(result core.Result) bool {
	return result.StopReason != core.StopCanceled && result.StopReason != core.StopError
}

// startAdvisorReview renders the unreviewed delta and starts one review. The
// cursor moves only once a review is actually accepted, so a skip never loses
// the messages it would have read. UI goroutine only.
func (m *model) startAdvisorReview(s *advisorSession) {
	m.advisorPending = false
	msgs := m.services.Agent.Messages()
	// A cursor past the conversation means it was replaced under it (a
	// compaction or a resume); start from the top rather than slicing out of
	// range. A cursor below zero is a conversation that has never been wired.
	if m.advisorReviewed < 0 || m.advisorReviewed > len(msgs) {
		m.advisorReviewed = 0
	}
	delta := msgs[m.advisorReviewed:]

	// A turn the advisor's own note opened is reviewed like any other: the
	// agent's fix is exactly what needs a second look. A run of them is capped
	// so advice cannot loop into advice; the capped turn is skipped with the
	// cursor moving past it rather than replayed.
	openedByNote := deltaOpenedByAdvisory(delta)
	if openedByNote {
		if m.advisorFollowups >= advisorMaxFollowups {
			m.advisorFollowups = 0
			m.advisorReviewed = len(msgs)
			m.services.AdvisorIndicator.Skip("capped")
			return
		}
	} else {
		// A turn the person opened ends the run: the next note gets a fresh
		// budget.
		m.advisorFollowups = 0
	}
	transcript := renderAdvisorTranscript(delta)
	if transcript == "" {
		m.advisorReviewed = len(msgs)
		m.services.AdvisorIndicator.Skip("no new work")
		return
	}
	// The cursor advances after the busy check: a review that could not start
	// stays pending with its delta intact for the drain to pick up.
	if !s.busy.CompareAndSwap(false, true) {
		m.advisorPending = true
		m.services.AdvisorIndicator.Skip("queued")
		return
	}
	m.advisorReviewed = len(msgs)
	if openedByNote {
		m.advisorFollowups++
	}
	m.services.AdvisorIndicator.BeginReview()
	run := m.advisorReview
	if run == nil {
		run = m.runAdvisorTurn
	}
	go run(s, transcript)
}

// drainAdvisorPending starts the queued review, if any, after the previous one
// finished. Called on the UI goroutine from the completion handler.
func (m *model) drainAdvisorPending() {
	if !m.advisorPending {
		return
	}
	s := m.services.Advisor
	if s == nil || !s.live.Load() {
		m.advisorPending = false
		return
	}
	m.startAdvisorReview(s)
}

// advisorDoneMsg wakes the Update loop when a review finishes, so the queued
// delta is reviewed on the main goroutine.
type advisorDoneMsg struct{}

// awaitAdvisorDone yields an advisorDoneMsg each time a review finishes.
func awaitAdvisorDone(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return advisorDoneMsg{}
	}
}

// publishAdvisorDone signals the main loop that a review ended. Non-blocking:
// a full channel drops the signal rather than deadlocking a quitting app.
func (m *model) publishAdvisorDone() {
	select {
	case m.advisorDone <- struct{}{}:
	default:
	}
}

// onAdvisorDone re-arms the listener and starts any review queued while the
// last one ran.
func (m *model) onAdvisorDone() tea.Cmd {
	next := awaitAdvisorDone(m.advisorDone)
	m.drainAdvisorPending()
	return next
}

// runAdvisorTurn executes one review. Every side effect is a log line, a
// mutex-guarded field or a channel send, so it may run on any goroutine.
func (m *model) runAdvisorTurn(s *advisorSession, transcript string) {
	var note string
	var reviewErr error
	abandoned := false
	// Release the slot, record the outcome and wake the main loop on every
	// path — a note, no note, an error or a teardown — or a queued review would
	// wait forever. The signal goes last so the drain sees a free slot.
	defer func() {
		s.busy.Store(false)
		if abandoned {
			m.services.AdvisorIndicator.Abandon()
		} else {
			m.services.AdvisorIndicator.Finish(note, reviewErr)
		}
		m.publishAdvisorDone()
	}()

	ctx, cancel := context.WithTimeout(s.ctx, advisor.DefaultTimeout)
	defer cancel()

	engine, closeFork := m.advisorEngine(s)
	var err error
	var steps int
	note, steps, err = engine.Advise(ctx, transcript)
	closeFork()
	// The requests were spent whether or not the review produced a note, so
	// the advisor's own count includes a review that came back empty.
	m.services.Usage.Add("advisor", steps)
	if !s.live.Load() {
		abandoned = true
		return
	}
	if err != nil {
		switch {
		case ctx.Err() == nil, errors.Is(ctx.Err(), context.DeadlineExceeded):
			reviewErr = err
			s.setError(err)
			log.Logger().Warn("advisor review failed", zap.Error(err))
		default:
			// A session teardown cancelled the review: it has no outcome, so
			// it must not look like a checked turn on the badge.
			abandoned = true
		}
		return
	}
	if note == "" {
		s.clearError()
		return
	}
	if s.acceptNote(note) {
		// The notice shows the note in the conversation and hands it to the
		// agent as a user message; see notify.go.
		m.notifyMain(mainNotice{
			Display: "advisor · " + advisorNoteLine(note),
			Content: "<advisory>\n" + note + "\n</advisory>",
		})
	}
}

// advisorEngine builds the fork for one review, opening the sidechain session
// that lets the inspector replay it. The returned func releases it.
func (m *model) advisorEngine(s *advisorSession) (*advisor.Engine, func()) {
	var onEvent func(core.Event)
	var forkID string
	cleanup := func() {}
	if store := m.services.SessionStore; store != nil {
		rec, err := store.Create(session.LegacyCreateOptions{
			AgentID:         advisorAgentID,
			Provider:        advisorProviderName(s.provider),
			Model:           s.modelID,
			MaxTokens:       advisorMaxTokens,
			Cwd:             s.cwd,
			ParentSessionID: s.parentSessionID,
			Sidechain:       true,
		})
		if err != nil {
			// The review is still worth running without a replayable sidechain.
			log.Logger().Warn("could not open advisor session", zap.Error(err))
		} else {
			onEvent = rec.RecordEvent
			forkID = rec.ID()
			cleanup = func() { _ = rec.Close() }
		}
	}

	client := llm.NewClient(s.provider, s.modelID, advisorMaxTokens)
	client.SetSessionID(forkID)
	client.SetWorkingDir(func() string { return s.cwd })

	return advisor.NewEngine(advisor.EngineConfig{
		Client:         client.TurnClient,
		CallOptions:    client.CallOptions,
		ShapeInference: client.ShapeInference,
		Tools:          advisor.ReadOnlyTools(s.cwd),
		Model:          s.modelID,
		Timeout:        advisor.DefaultTimeout,
		CWD:            s.cwd,
		OnEvent:        onEvent,
	}), cleanup
}

// renderAdvisorTranscript turns a conversation delta into the plain text one
// review reads. Tool output is clipped hard: the reviewer needs to know what
// happened, not to re-read every line a command printed.
func renderAdvisorTranscript(msgs []core.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		switch msg.Role {
		case ai.RoleUser:
			if results := msg.ToolResults(); len(results) > 0 {
				for _, tr := range results {
					fmt.Fprintf(&b, "[tool result: %s]\n%s\n\n", tr.ToolName, clipText(tr.Content.Text(), advisorToolResultBudget))
				}
				continue
			}
			text := strings.TrimSpace(msg.Text())
			if text == "" || strings.HasPrefix(text, "<advisory") {
				continue
			}
			fmt.Fprintf(&b, "User: %s\n\n", clipText(text, advisorTranscriptBudget/2))
		case ai.RoleAssistant:
			if text := strings.TrimSpace(msg.Text()); text != "" {
				fmt.Fprintf(&b, "Assistant: %s\n\n", clipText(text, advisorTranscriptBudget/2))
			}
			for _, call := range msg.ToolCalls() {
				fmt.Fprintf(&b, "[tool call: %s] %s\n\n", call.Name, clipText(call.Input, advisorToolInputBudget))
			}
		}
		if b.Len() >= advisorTranscriptBudget {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// deltaOpenedByAdvisory reports whether the delta starts with the note the
// advisor injected, i.e. the turn that note triggered. The note itself is left
// out of the review transcript, so only the agent's reaction is reviewed.
func deltaOpenedByAdvisory(msgs []core.Message) bool {
	for _, msg := range msgs {
		if msg.Role != ai.RoleUser || len(msg.ToolResults()) > 0 {
			continue
		}
		return strings.HasPrefix(strings.TrimSpace(msg.Text()), "<advisory")
	}
	return false
}

// clipText cuts s to at most n bytes on a rune boundary.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// advisorNoteLine is the one-line display of a note.
func advisorNoteLine(note string) string {
	for _, line := range strings.Split(note, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*• "))
		if line != "" {
			return clipText(line, 120)
		}
	}
	return "note"
}

// setError records the last failure that produced no note.
func (s *advisorSession) setError(err error) {
	s.mu.Lock()
	s.lastErr = strings.TrimSpace(err.Error())
	s.mu.Unlock()
}

func (s *advisorSession) clearError() {
	s.mu.Lock()
	s.lastErr = ""
	s.mu.Unlock()
}

// acceptNote records the note and reports whether it is new. Repeating the same
// advice on consecutive turns is noise, not a second finding.
func (s *advisorSession) acceptNote(note string) bool {
	normalized := normalizeNote(note)
	s.mu.Lock()
	defer s.mu.Unlock()
	if normalized == normalizeNote(s.lastNote) {
		return false
	}
	s.lastNote = note
	s.lastErr = ""
	return true
}

func normalizeNote(note string) string {
	return strings.Join(strings.Fields(strings.ToLower(note)), " ")
}

// advisorStatusLine reports the live configuration and the last note, from the
// same snapshot the status-bar badge renders.
func (m *model) advisorStatusLine() string {
	cfg := m.env.Advisor
	s := m.services.Advisor
	if s == nil {
		if !cfg.Enabled() {
			return "advisor · off"
		}
		return "advisor · on · no model connected"
	}
	badge := m.services.AdvisorIndicator.Snapshot()
	state := "on"
	if badge.Reviewing {
		state = "reviewing"
	}
	line := fmt.Sprintf("advisor · %s · %s", state, s.modelID)
	if badge.LastNote != "" {
		line += "\nlast: " + advisorNoteLine(badge.LastNote)
	}
	if badge.LastError != "" {
		line += "\nlast error: " + badge.LastError
	}
	return line
}

// advisorCommand is /advisor. It reports the layer's state, or turns it on or
// off for this session without touching the settings file.
func (m *model) advisorCommand(args string) (string, tea.Cmd) {
	cfg := m.env.Advisor
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "", "status":
		return m.advisorStatusLine(), nil
	case "on":
		on := true
		cfg.Enable = &on
		m.env.Advisor = cfg
		m.wireAdvisor()
		m.agentAdvisorSignature = m.advisorSignature()
		return "Advisor on for this session.", nil
	case "off":
		off := false
		cfg.Enable = &off
		m.env.Advisor = cfg
		m.teardownAdvisor()
		m.agentAdvisorSignature = m.advisorSignature()
		return "Advisor off for this session.", nil
	default:
		return "Usage: /advisor [on|off|status]", nil
	}
}

// advisorProviderName names the review provider for the sidechain header,
// tolerating an unset provider.
func advisorProviderName(p llm.Provider) string {
	if p == nil {
		return ""
	}
	return p.Name()
}
