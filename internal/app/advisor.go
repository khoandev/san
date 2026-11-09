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
	"github.com/genai-io/san/internal/setting"
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
	// is left for the next turn instead of queuing model calls.
	busy atomic.Bool

	// reviewed is the number of conversation messages already reviewed. It is
	// touched only on the UI goroutine.
	reviewed int

	// parentSessionID and cwd are captured at wire time so the review goroutine
	// never reads UI state.
	parentSessionID string
	cwd             string

	// mu guards the display state read by /advisor.
	mu       sync.Mutex
	running  bool
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
	provider, modelID := m.resolveAdvisorModel(cfg)
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
	// person has already moved past.
	if m.services.Agent != nil {
		s.reviewed = len(m.services.Agent.Messages())
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

// advisorRole answers the model role a review runs on: the role the agents tab
// assigns to the advisor (agents.roles.advisor), otherwise the deep rung — the
// role the advisor exists for, a thorough review where the extra latency is
// acceptable. A legacy advisor.model written as "@role" names the same slot;
// loading and saving settings fold it onto agents.roles, so this reads it only
// for settings built without that fold (a test, or a file edited mid-session).
func (m *model) advisorRole(cfg setting.AdvisorSettings) llm.ModelRole {
	if m.services.Setting != nil {
		if assigned := m.services.Setting.AgentRole(setting.AgentAdvisor); assigned != "" {
			return llm.ModelRole(assigned)
		}
	}
	if role, isRole := llm.ParseRoleAlias(cfg.Model); isRole {
		return llm.ModelRole(role)
	}
	return llm.RoleDeep
}

// advisorModelPin answers the concrete model reference that pins a review, if
// one applies: advisor.model's old shape, which the agents tab has no control
// for. A role reference is not a pin — advisorRole reads it — and an assigned
// role supersedes a pin, the same precedence UpdateAgentsAt writes.
func (m *model) advisorModelPin(cfg setting.AdvisorSettings) (string, bool) {
	ref := strings.TrimSpace(cfg.Model)
	if ref == "" {
		return "", false
	}
	if _, isRole := llm.ParseRoleAlias(ref); isRole {
		return "", false
	}
	if m.services.Setting != nil && m.services.Setting.AgentRole(setting.AgentAdvisor) != "" {
		return "", false
	}
	return ref, true
}

// resolveAdvisorModel picks the review provider and model: a concrete
// advisor.model pin when the file still carries one, else the role assigned to
// the advisor resolved through the live modelRoles bindings, else the session
// model. A nil provider means nothing is connected and no review can run.
func (m *model) resolveAdvisorModel(cfg setting.AdvisorSettings) (llm.Provider, string) {
	if ref, pinned := m.advisorModelPin(cfg); pinned {
		target := m.resolveRoleWithOverride(llm.RoleDeep, ref)
		return target.Provider, target.ModelID
	}
	target := m.resolveRole(m.advisorRole(cfg))
	return target.Provider, target.ModelID
}

// advisorSignature is a stable fingerprint of everything that changes what a
// review does: whether it runs, which role answers it, and the model that role
// resolves to. ensureAgentRuntime compares it against the live runner's value,
// so an /advisor save, a role change in the agents tab, a settings-file edit or
// a model switch rebuilds the runner on the next turn.
func (m *model) advisorSignature() string {
	cfg := m.env.Advisor
	if !cfg.Enabled() {
		return ""
	}
	if ref, pinned := m.advisorModelPin(cfg); pinned {
		target := m.resolveRoleWithOverride(llm.RoleDeep, ref)
		return "pin:" + ref + "\x00" + target.ModelID
	}
	role := m.advisorRole(cfg)
	_, modelID := m.resolveRoleModel(role)
	return "role:" + string(role) + "\x00" + modelID
}

// reviewCompletedTurn starts a review of what this turn added to the
// conversation. Called from OnTurnEnd on the UI goroutine; the review itself
// runs elsewhere.
func (m *model) reviewCompletedTurn(result core.Result) {
	s := m.services.Advisor
	if s == nil || !s.live.Load() || result.StopReason != core.StopEndTurn {
		return
	}
	msgs := m.services.Agent.Messages()
	if s.reviewed > len(msgs) {
		s.reviewed = 0
	}
	delta := msgs[s.reviewed:]
	s.reviewed = len(msgs)

	// A turn the advisor itself opened is not reviewed again: its note is in
	// the transcript, and reviewing the agent's reaction to it would loop.
	if lastInputIsAdvisory(delta) {
		return
	}
	transcript := renderAdvisorTranscript(delta)
	if transcript == "" {
		return
	}
	// One review at a time. The cursor above already moved, so a skipped review
	// is dropped rather than replayed later; the next turn reviews its own
	// delta.
	if !s.busy.CompareAndSwap(false, true) {
		return
	}
	go m.runAdvisorTurn(s, transcript)
}

// runAdvisorTurn executes one review. Every side effect is a log line, a
// mutex-guarded field or a channel send, so it may run on any goroutine.
func (m *model) runAdvisorTurn(s *advisorSession, transcript string) {
	defer s.busy.Store(false)
	s.setRunning(true)
	defer s.setRunning(false)

	ctx, cancel := context.WithTimeout(s.ctx, advisor.DefaultTimeout)
	defer cancel()

	engine, closeFork := m.advisorEngine(s)
	note, err := engine.Advise(ctx, transcript)
	closeFork()
	if !s.live.Load() {
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			s.setError(err)
			log.Logger().Warn("advisor review failed", zap.Error(err))
		}
		return
	}
	if note == "" {
		s.clearError()
		return
	}
	if !s.acceptNote(note) {
		return
	}
	// The notice shows the note in the conversation and hands it to the agent
	// as a user message; see notify.go.
	m.notifyMain(mainNotice{
		Display: "advisor · " + advisorNoteLine(note),
		Content: "<advisory>\n" + note + "\n</advisory>",
	})
}

// advisorEngine builds the fork for one review, opening the sidechain session
// that lets the inspector replay it. The returned func releases it.
func (m *model) advisorEngine(s *advisorSession) (*advisor.Engine, func()) {
	var onEvent func(core.Event)
	var forkID string
	cleanup := func() {}
	if store := m.services.SessionStore; store != nil {
		rec, err := store.Create(session.CreateOptions{
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

// lastInputIsAdvisory reports whether the delta's last user input is the note
// the advisor injected, which is what stops review and advice from looping.
func lastInputIsAdvisory(msgs []core.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
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

// setRunning records that a review is in flight for /advisor.
func (s *advisorSession) setRunning(on bool) {
	s.mu.Lock()
	s.running = on
	s.mu.Unlock()
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

// snapshot is what /advisor reports.
func (s *advisorSession) snapshot() (running bool, last, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.lastNote, s.lastErr
}

// advisorStatusLine reports the live configuration and the last note.
func (m *model) advisorStatusLine() string {
	cfg := m.env.Advisor
	s := m.services.Advisor
	if s == nil {
		if !cfg.Enabled() {
			return "advisor · off"
		}
		return "advisor · on · no model connected"
	}
	running, last, lastErr := s.snapshot()
	state := "on"
	if running {
		state = "reviewing"
	}
	line := fmt.Sprintf("advisor · %s · %s", state, s.modelID)
	if last != "" {
		line += "\nlast: " + advisorNoteLine(last)
	}
	if lastErr != "" {
		line += "\nlast error: " + lastErr
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
