// Global advisor wire-up: bridges setting.AdvisorSettings into a session-scoped
// advisor.Engine, runs reviews off the agent's goroutine, and records every
// review append-only in the transcript.
//
// The advisor is an independent review layer. It reads what a change produced,
// classifies what it finds P0–P3, and lets the block policy decide what that
// means for a merge, a finished subagent task or a plan step. It never writes
// code, never prompts the TUI, and never shares the main agent's conversation:
// every review is a fresh read-only fork with its own system prompt.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/todo"
	"github.com/genai-io/san/internal/transcript"
)

// advisorAgentID names the sidechain session each review writes, so
// `san --resume <fork-id>` replays exactly that review's LLM calls and the
// inspector can tell a review apart from the conversation it inspected.
const advisorAgentID = "advisor-review"

// advisorDiffTimeout bounds the git read a manual review starts with, so a
// pathological repository cannot hold the UI goroutine.
const advisorDiffTimeout = 10 * time.Second

// AdvisorServices groups the global advisor's session state. It is the one
// place the UI, the review runner and the transcript writer meet.
type AdvisorServices struct {
	// Store is the append-only review history for the running session. Always
	// non-nil: the render path reads it without a nil check, and it is reset —
	// never reassigned — when the conversation is cleared.
	Store *advisor.Store

	// Indicator is the live status snapshot behind the status bar and
	// /advisor. Always non-nil, and it outlives any one session so the last
	// verdict stays readable across a teardown.
	Indicator *AdvisorIndicator

	// session is the live per-session run plus its teardown handles, or nil
	// when the advisor is off. Set and cleared as a unit by wireAdvisor /
	// teardownAdvisor, so the engine, its cancel and its liveness gate can
	// never fall out of sync.
	session *advisorSession

	// gate is the pre-merge review the permission path consults. The slot is a
	// pointer because a bubbletea model is copied by value on every update, and
	// the value inside it is atomic because the permission check reads it on
	// the agent goroutine while wireAdvisor and teardownAdvisor rewrite it on
	// the UI goroutine. Build AdvisorServices with newAdvisorServices so the
	// slot exists; a nil slot simply wires no gate.
	gate *atomic.Pointer[advisor.MergeGate]
}

// newAdvisorServices builds the advisor's session state. The store and the
// indicator are always present — the render path reads them without a nil check
// — and the merge gate slot is allocated here because two goroutines share it.
func newAdvisorServices() AdvisorServices {
	return AdvisorServices{
		Store:     advisor.NewStore(),
		Indicator: NewAdvisorIndicator(),
		gate:      new(atomic.Pointer[advisor.MergeGate]),
	}
}

// mergeGate returns the live pre-merge gate, or nil when none is wired. Safe
// from the agent goroutine: it reads an atomic pointer and nothing else.
func (a *AdvisorServices) mergeGate() *advisor.MergeGate {
	if a == nil || a.gate == nil {
		return nil
	}
	return a.gate.Load()
}

// storeMergeGate replaces the gate. Called on the UI goroutine by wireAdvisor
// and teardownAdvisor, which own the runner the gate reviews with.
func (a *AdvisorServices) storeMergeGate(gate *advisor.MergeGate) {
	if a == nil || a.gate == nil {
		return
	}
	a.gate.Store(gate)
}

// advisorSession bundles the handles that make up one live advisor for the
// current conversation session. They are created together by wireAdvisor and
// dropped together by teardownAdvisor, which makes "all present or none"
// structural rather than a prose promise.
type advisorSession struct {
	// cfg is the configuration the runner was built from. ensureAgentRuntime
	// compares it against the live settings so an /advisor save, a settings-file
	// edit or a model switch rebuilds the runner on the next turn.
	cfg setting.AdvisorSettings

	// provider and modelID are the resolved review model. The client is
	// rebuilt per review (its session id names that review's sidechain), but
	// the resolution is kept here so the drift check stays cheap.
	provider llm.Provider
	modelID  string

	// policy is the block policy resolved from cfg and applied to every review
	// this session runs.
	policy advisor.Policy

	// cooldown suppresses a repeat review of the same target with the same
	// diff hash, so a trigger that fires twice on one change costs one review.
	cooldown *advisor.Cooldown

	// ctx is the session-scoped context every review inherits, and cancel
	// unblocks an in-flight fork immediately on /clear or quit instead of
	// letting it run to its own deadline.
	ctx    context.Context
	cancel context.CancelFunc

	// live flips false on teardown so a review landing afterwards drops
	// silently instead of publishing or persisting for a dead session. The
	// review goroutine captures this same pointer.
	live *atomic.Bool

	// busy reports that a review is in flight. One at a time: a second trigger
	// while the first runs is skipped rather than queued, so a burst of
	// triggers cannot multiply token spend. The next trigger re-runs.
	busy atomic.Bool

	// parentSessionID names the conversation each review belongs to, captured
	// at wire time so a review never attaches to a session opened later.
	parentSessionID string

	// cwd roots the read-only tools and the sidechain header. It is captured at
	// wire time — and re-wired when the workspace changes — so the review
	// goroutine never reads the UI goroutine's env.
	cwd string
}

// wireAdvisor builds the session's advisor runner when the advisor is enabled
// and a model is connected. Called after Agent.Start so the conversation's
// identity exists; a disabled or invalid configuration builds nothing, leaving
// no goroutine, no cooldown and no extra model calls behind.
func (m *model) wireAdvisor() {
	// Tear down first: ensureAgentRuntime can re-enter after a stopped agent
	// and would otherwise overwrite the cancel un-called, pinning the old fork.
	m.teardownAdvisor()

	cfg := m.advisorConfig()
	if !cfg.Enabled() {
		return
	}
	if err := cfg.Validate(); err != nil {
		log.Logger().Warn("advisor config rejected at startup", zap.Error(err))
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
	m.services.Advisor.session = &advisorSession{
		cfg:             cfg,
		provider:        provider,
		modelID:         modelID,
		policy:          advisorPolicy(cfg),
		cooldown:        advisor.NewCooldown(),
		ctx:             ctx,
		cancel:          cancel,
		live:            live,
		parentSessionID: m.sessionID(),
		cwd:             m.env.CWD,
	}
	// The merge gate is built with the runner and dropped with it, so the
	// permission path can never consult a torn-down session's engine.
	m.services.Advisor.storeMergeGate(m.newMergeGate(m.services.Advisor.session))
}

// teardownAdvisor unwires the session's advisor: it cancels the session-scoped
// context, flips the liveness gate false and drops the run. Idempotent. Called
// from StopAgentRuntime, ResetAgentRuntime and the top of wireAdvisor so a
// rebuild never leaks the prior context.
func (m *model) teardownAdvisor() {
	s := m.services.Advisor.session
	if s == nil {
		return
	}
	s.cancel()
	s.live.Store(false)
	m.services.Advisor.session = nil
	m.services.Advisor.storeMergeGate(nil)
	// The indicator outlives sessions; drop the "running" phase so a torn-down
	// review cannot leave the status bar spinning forever.
	m.services.Advisor.Indicator.Reset()
}

// advisorConfig is the effective advisor configuration: the session's copy,
// seeded from settings at startup and replaced by the session's own override on
// resume.
func (m *model) advisorConfig() setting.AdvisorSettings { return m.env.Advisor }

// resolveAdvisorModel picks the review provider and model: the configured
// "vendor/model" ref when it resolves, the session model otherwise. A nil
// provider means nothing is connected and no review can run.
func (m *model) resolveAdvisorModel(cfg setting.AdvisorSettings) (llm.Provider, string) {
	if cfg.Model.Model != "" && m.services.LLM != nil {
		return m.resolveReviewerModel(cfg.Model.Model)
	}
	provider, info := m.resolveReviewer("")
	if info == nil {
		return provider, ""
	}
	return provider, info.ModelID
}

// advisorSignature is a stable fingerprint of everything that changes what a
// review does: the configuration and the resolved model. ensureAgentRuntime
// compares it against the running agent's value, so an /advisor settings change,
// a settings-file edit or a model switch rebuilds the runner on the next turn —
// the one choke point every turn passes through.
func (m *model) advisorSignature() string {
	cfg := m.advisorConfig()
	if !cfg.Enabled() {
		return ""
	}
	_, modelID := m.resolveAdvisorModel(cfg)
	return strings.Join([]string{
		cfg.Model.Model,
		modelID,
		cfg.Mode(),
		cfg.Model.ThinkingEffort,
		cfg.Review.CustomPrompt,
		fmt.Sprint(cfg.ResolvedMaxTokens()),
		fmt.Sprint(cfg.ResolvedTimeout()),
		fmt.Sprint(cfg.ResolvedMaxFindings()),
		fmt.Sprint(cfg.ResolvedMaxDiffBytes()),
		fmt.Sprint(cfg.ResolvedCooldown()),
		cfg.MergeThreshold(),
		cfg.TaskThreshold(),
		cfg.PlanStepThreshold(),
		cfg.ConfirmThreshold(),
		fmt.Sprint(cfg.AllowProceedWithRisk()),
	}, "\x00")
}

// advisorPolicy maps the settings block policy onto the domain policy. The
// setting layer names each threshold as a severity string so it stays free of
// the advisor's severity type; parsing it back is this function's job.
func advisorPolicy(cfg setting.AdvisorSettings) advisor.Policy {
	return advisor.Policy{
		BlockMerge:              advisorSeverity(cfg.MergeThreshold()),
		BlockTaskCompletion:     advisorSeverity(cfg.TaskThreshold()),
		BlockPlanStepCompletion: advisorSeverity(cfg.PlanStepThreshold()),
		RequireConfirmation:     advisorSeverity(cfg.ConfirmThreshold()),
		AllowProceedWithRisk:    cfg.AllowProceedWithRisk(),
	}
}

// advisorSeverity parses a settings threshold, mapping "" and anything
// unrecognized to the zero severity — which blocks nothing.
func advisorSeverity(name string) advisor.Severity {
	severity, ok := advisor.ParseSeverity(name)
	if !ok {
		return ""
	}
	return severity
}

// AdvisorSnapshot is the advisor's status at one instant, read by the status
// bar and /advisor. It carries copies, so a reader can hold it while a review
// finishes.
type AdvisorSnapshot struct {
	// Running reports that a review is in flight.
	Running bool
	// Latest is the most recent verdict, or nil when none has run.
	Latest *advisor.Review
	// Err is the failure of the last review that could not reach a verdict.
	Err string
}

// AdvisorIndicator is the advisor's status snapshot. Every method is safe from
// the review goroutine and the UI goroutine at once.
type AdvisorIndicator struct {
	mu      sync.RWMutex
	running int
	latest  *advisor.Review
	err     string
}

// NewAdvisorIndicator returns an idle indicator.
func NewAdvisorIndicator() *AdvisorIndicator { return &AdvisorIndicator{} }

// Begin records that a review started.
func (i *AdvisorIndicator) Begin() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.running++
	i.err = ""
}

// Finish records a review's verdict, or why it produced none.
func (i *AdvisorIndicator) Finish(review advisor.Review, err error) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.running > 0 {
		i.running--
	}
	if err != nil {
		i.err = strings.TrimSpace(err.Error())
		return
	}
	done := review.Clone()
	i.latest = &done
	i.err = ""
}

// Fail records a failure that produced no review at all.
func (i *AdvisorIndicator) Fail(err error) { i.Finish(advisor.Review{}, err) }

// Reset drops the running phase, as a teardown does.
func (i *AdvisorIndicator) Reset() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.running = 0
}

// Snapshot returns the current status.
func (i *AdvisorIndicator) Snapshot() AdvisorSnapshot {
	if i == nil {
		return AdvisorSnapshot{}
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	snap := AdvisorSnapshot{Running: i.running > 0, Err: i.err}
	if i.latest != nil {
		latest := i.latest.Clone()
		snap.Latest = &latest
	}
	return snap
}

// requestAdvisorReview starts one review off the agent's goroutine and reports
// whether it started. It is the single entry point every trigger shares, so the
// enable check, the cooldown and the one-at-a-time rule are enforced in one
// place rather than at each call site.
//
// A review never blocks the caller: the verdict arrives as a notice and in the
// history. Merge gating, which needs the verdict before the command runs, will
// use a synchronous form of the same runner.
func (m *model) requestAdvisorReview(in advisor.ReviewInput) bool {
	s := m.services.Advisor.session
	if s == nil {
		return false
	}
	return m.submitAdvisorReview(s, in)
}

// submitAdvisorReview is the body every entry point shares once the session is
// in hand: liveness, cooldown, one-at-a-time, then the background run. Taking
// the session as a parameter lets a caller that captured it on the UI goroutine
// submit from its own goroutine without touching model state.
func (m *model) submitAdvisorReview(s *advisorSession, in advisor.ReviewInput) bool {
	req, ok := m.claimAdvisorReview(s, in)
	if !ok {
		return false
	}
	go m.runAdvisorReview(s, req)
	return true
}

// claimAdvisorReview applies the gates every trigger shares — liveness, the
// cooldown, and the one-at-a-time rule — and takes the run slot, returning the
// request to execute. A caller that needs the verdict does the run itself on its
// own goroutine; a caller that does not hands it to runAdvisorReview.
func (m *model) claimAdvisorReview(s *advisorSession, in advisor.ReviewInput) (advisor.Request, bool) {
	if s == nil || !s.live.Load() {
		return advisor.Request{}, false
	}
	if in.Trigger == "" {
		in.Trigger = advisor.TriggerUserRequest
	}
	if in.Target.Kind == "" {
		in.Target = advisor.Target{Kind: advisor.TargetSession}
	}
	if !s.cooldown.Due(in.Target, in.DiffHash, s.cfg.ResolvedCooldown()) {
		return advisor.Request{}, false
	}
	if !s.busy.CompareAndSwap(false, true) {
		return advisor.Request{}, false
	}
	s.cooldown.Mark(in.Target, in.DiffHash)
	return advisor.BuildRequest(s.cfg, in), true
}

// requestTaskReview hands a finished subagent task to the advisor. The task
// manager calls it from its own goroutine, so it only posts to the channel: the
// review runner, the workspace path and the settings it reads all belong to the
// UI goroutine.
func (m *model) requestTaskReview(info task.TaskInfo) {
	if info.Type != task.TaskTypeAgent || info.Status != task.StatusCompleted {
		return
	}
	select {
	case m.advisorTasks <- info:
	default:
		// A burst of completions drops one rather than blocking the task
		// manager; the tree they changed is what the next review reads.
	}
}

// onAdvisorTask starts the review of a finished subagent task and re-arms the
// listener. UI goroutine only.
func (m *model) onAdvisorTask(info task.TaskInfo) tea.Cmd {
	m.startTaskReview(info)
	return awaitAdvisorTask(m.advisorTasks)
}

// startTaskReview reviews the change a finished task left behind. The read runs
// on its own goroutine — a git call must never stall the UI — and the review is
// submitted with the session captured here, so the goroutine never reads model
// state.
func (m *model) startTaskReview(info task.TaskInfo) {
	s := m.services.Advisor.session
	// The trigger is read from the live settings, not the copy the runner was
	// built with: turning it off takes effect on the next finished task instead
	// of on the next rebuild.
	if s == nil || !s.live.Load() || !m.advisorConfig().AfterSubagentTask() {
		return
	}
	in := advisor.ReviewInput{
		Trigger: advisor.TriggerSubagentTask,
		Target: advisor.Target{
			Kind:  advisor.TargetTask,
			ID:    info.ID,
			Label: taskSubject(info),
		},
		Goal:   info.Description,
		Checks: taskReviewChecks(info),
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), advisorDiffTimeout)
		defer cancel()
		diff, err := proc.ReadWorkingDiff(ctx, s.cwd, proc.DiffOptions{MaxBytes: s.cfg.ResolvedMaxDiffBytes()})
		if err != nil {
			log.Logger().Warn("advisor could not read the finished task's diff",
				zap.String("task", info.ID), zap.Error(err))
			return
		}
		in.Diff, in.DiffHash = diff.Diff, diff.Hash
		in.Files, in.DiffTruncated = diff.Files, diff.Truncated
		m.submitAdvisorReview(s, in)
	}()
}

// taskReviewChecks states the task's own result as a check the review may
// verify, so the reviewer judges the claim rather than only the diff.
func taskReviewChecks(info task.TaskInfo) []string {
	name := strings.TrimSpace(info.AgentName)
	line := "subagent task " + info.ID
	if name != "" {
		line = "subagent " + name + " task " + info.ID
	}
	line += " reported " + string(info.Status)
	if err := strings.TrimSpace(info.Error); err != "" {
		line += ": " + err
	}
	return []string{line}
}

// advisorTaskMsg wakes the Update loop with a finished subagent task.
type advisorTaskMsg struct{ info task.TaskInfo }

// awaitAdvisorTask yields one advisorTaskMsg per finished task.
func awaitAdvisorTask(ch <-chan task.TaskInfo) tea.Cmd {
	return func() tea.Msg {
		return advisorTaskMsg{info: <-ch}
	}
}

// planStepReviewedMsg carries a completed step's verdict back to the UI loop,
// which owns the plan store.
type planStepReviewedMsg struct {
	// step is the step the review inspected, captured before the store mutates
	// so the loop can still name it once the step is recorded.
	step plan.Step
	// outcome is the step's own report, replayed to the loop so it can finish
	// the turn exactly as it would have without a review.
	outcome plan.StepResult
	// review is the verdict, and found reports whether there is one: a review
	// that never ran or never reached a verdict leaves both empty.
	review advisor.Review
	found  bool
}

// reviewPlanStep reviews a step that just claimed completion, before the store
// records it, and returns the command that carries the verdict back to the UI
// loop. The record-and-advance decision waits for that verdict, so a refused
// completion never briefly reads as a clean one.
//
// nil means no review runs — the step did not claim completion, the trigger is
// off, no runner is live, or no model is connected — and the caller records the
// step exactly as it would have without an advisor. The diff read and the review
// itself run inside the command, off the UI goroutine: a git call plus a model
// round-trip must never stall the panel.
func (m *model) reviewPlanStep(step plan.Step, outcome plan.StepResult) tea.Cmd {
	if outcome.Status != plan.StepCompleted {
		return nil
	}
	s := m.services.Advisor.session
	if s == nil || !s.live.Load() || !m.advisorConfig().BeforePlanStep() {
		return nil
	}
	step = step.Clone()
	return func() tea.Msg {
		msg := planStepReviewedMsg{step: step, outcome: outcome}
		ctx, cancel := context.WithTimeout(context.Background(), advisorDiffTimeout)
		defer cancel()
		diff, err := proc.ReadWorkingDiff(ctx, s.cwd, proc.DiffOptions{MaxBytes: s.cfg.ResolvedMaxDiffBytes()})
		if err != nil {
			// A step whose change cannot be read is still reported: the review
			// is an extra check, not a second way for a step to fail.
			log.Logger().Warn("advisor could not read the step's diff",
				zap.String("step", step.ID), zap.Error(err))
			return msg
		}
		files := append(append([]string(nil), outcome.Files...), diff.Files...)
		req, ok := m.claimAdvisorReview(s, advisor.ReviewInput{
			Trigger:       advisor.TriggerPlanStep,
			Target:        advisor.Target{Kind: advisor.TargetStep, ID: step.ID, Label: step.Title},
			Goal:          step.Objective,
			Acceptance:    step.Verification,
			Checks:        planStepChecks(outcome),
			Diff:          diff.Diff,
			DiffHash:      diff.Hash,
			Files:         files,
			DiffTruncated: diff.Truncated,
		})
		if !ok {
			return msg
		}
		stored, forkID, ok := m.runClaimedReview(nil, s, req)
		if !ok {
			return msg
		}
		m.publishAdvisorVerdict(stored, forkID)
		msg.review, msg.found = stored, true
		return msg
	}
}

// applyStepReview joins a completed step's verdict to the plan store. A verdict
// the block policy refuses keeps the step completed — the work was done and
// reported — but marks it as needing review and pauses the plan, so the executor
// cannot start the next step on work a review rejected. Anything lighter is
// already in the review history and leaves the plan exactly as it was.
//
// It reports whether the plan was paused. Called on the UI goroutine, in the
// handler that finishes the step turn.
func (m *model) applyStepReview(step plan.Step, review advisor.Review) bool {
	if review.Trigger != advisor.TriggerPlanStep {
		return false
	}
	severity := review.Highest()
	if !advisorPolicy(m.advisorConfig()).Blocks(advisor.ActionPlanStepCompletion, severity) {
		return false
	}
	if err := m.plan.MarkStepNeedsReview(step.ID, review.ID, string(severity)); err != nil {
		log.Logger().Warn("advisor could not mark the step as needing review",
			zap.String("step", step.ID), zap.String("review", review.ID), zap.Error(err))
		return false
	}
	m.showPlanPanel()
	m.conv.AddNotice(stepNeedsReviewNotice(step, review, severity))
	return true
}

// stepNeedsReviewNotice says why the plan stopped and where the finding can be
// read in full. It points at the panel rather than restating findings: the
// review is the detail, and it is one keystroke away.
func stepNeedsReviewNotice(step plan.Step, review advisor.Review, severity advisor.Severity) string {
	title := strings.TrimSpace(step.Title)
	if title == "" {
		title = step.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Step %q is recorded as completed, but the advisor refused it", title)
	if severity != "" {
		fmt.Fprintf(&b, " (%s)", severity)
	}
	if summary := firstLineOf(review.Summary); summary != "" {
		b.WriteString(": ")
		b.WriteString(summary)
	}
	b.WriteString("\nThe plan is paused. Read the findings in /advisor, then resume, skip, or cancel in the Execution Panel.")
	return b.String()
}

// planStepChecks states the step's own report as claims the review may verify,
// so the reviewer judges what the agent said it did rather than only the diff.
func planStepChecks(outcome plan.StepResult) []string {
	checks := make([]string, 0, len(outcome.Evidence)+1)
	if result := firstLineOf(outcome.Result); result != "" {
		checks = append(checks, "the step reported: "+result)
	}
	for _, evidence := range outcome.Evidence {
		if line := firstLineOf(evidence); line != "" {
			checks = append(checks, "claimed evidence: "+line)
		}
	}
	return checks
}

// runAdvisorReview executes one review and records it. It runs on its own
// goroutine: every UI mutation goes through a channel, and the liveness gate is
// re-checked after the fork returns because teardown is most likely to land
// while the review is waiting on the provider.
func (m *model) runAdvisorReview(s *advisorSession, req advisor.Request) {
	stored, forkID, ok := m.runClaimedReview(nil, s, req)
	if !ok {
		return
	}
	m.publishAdvisorVerdict(stored, forkID)
}

// runClaimedReview executes a review whose slot claimAdvisorReview took, records
// it in the history and the transcript, publishes the started/completed states,
// and releases the slot. It reports false when no verdict was recorded — a
// failed review, or one that landed after its session was torn down. A nil ctx
// means the review runs under the session's context alone; a caller that must
// have the verdict now (a plan step, a merge) passes its own deadline.
//
// Every side effect is a mutex or a channel send, so this may run on any
// goroutine: the plan-step trigger calls it from its own command goroutine
// because it needs the verdict before the executor continues.
func (m *model) runClaimedReview(ctx context.Context, s *advisorSession, req advisor.Request) (advisor.Review, string, bool) {
	defer s.busy.Store(false)

	m.services.Advisor.Indicator.Begin()
	m.publishAdvisorStarted()

	review, forkID, err := m.executeAdvisorReview(ctx, s, req)
	if !s.live.Load() {
		// The conversation this review belongs to is gone: its verdict is
		// dropped rather than published or appended to a successor session.
		return advisor.Review{}, "", false
	}
	if err != nil {
		m.services.Advisor.Indicator.Fail(err)
		log.Logger().Warn("advisor review failed",
			zap.String("trigger", string(req.Trigger)),
			zap.Error(err))
		m.notifyMain(mainNotice{Display: advisorFailureLine(err)})
		return advisor.Review{}, "", false
	}

	stored, err := m.services.Advisor.Store.Append(review)
	if err != nil {
		m.services.Advisor.Indicator.Fail(err)
		log.Logger().Warn("advisor review rejected", zap.Error(err))
		m.notifyMain(mainNotice{Display: advisorFailureLine(err)})
		return advisor.Review{}, "", false
	}
	m.services.Advisor.Indicator.Finish(stored, nil)
	m.persistAdvisorReview(stored)
	log.Logger().Info("advisor review",
		zap.String("status", string(stored.Status)),
		zap.String("trigger", string(stored.Trigger)),
		zap.String("fork-session", forkID))
	return stored, forkID, true
}

// publishAdvisorVerdict announces a recorded review and joins it back to the
// work it inspected: the one-line verdict in the transcript, and the tracker
// flag when the review refused a finished task.
func (m *model) publishAdvisorVerdict(stored advisor.Review, forkID string) {
	m.notifyMain(mainNotice{Display: advisorReviewLine(stored, forkID)})
	m.noteTaskReview(stored)
}

// noteTaskReview joins a finished task's verdict back to its tracker item. A
// blocked verdict is the one case that changes what the task reads as: the item
// keeps StatusCompleted — the work did finish — but it carries the severity and
// the review id, so /tasks cannot show it as a clean completion. Lighter
// findings are already in the history, and the task stays clean and quiet.
//
// The tracker store is mutex-guarded and the notice goes through notifyMain, so
// running this on the review goroutine touches no UI state.
func (m *model) noteTaskReview(review advisor.Review) {
	if review.Trigger != advisor.TriggerSubagentTask || review.Target.Kind != advisor.TargetTask {
		return
	}
	if review.Status != advisor.StatusBlocked {
		return
	}
	severity := review.Highest()
	if m.services.Tracker != nil {
		todo.MarkNeedsReview(m.services.Tracker, review.Target.ID, review.ID, string(severity))
	}
	m.notifyMain(mainNotice{Display: taskNeedsReviewLine(review, severity)})
}

// taskNeedsReviewLine names the task a blocked review refused, so the notice
// says which finished worker stopped reading as done.
func taskNeedsReviewLine(review advisor.Review, severity advisor.Severity) string {
	label := strings.TrimSpace(review.Target.Label)
	if label == "" {
		label = review.Target.ID
	}
	line := "task " + label + " needs review"
	if severity != "" {
		line += " · " + string(severity)
	}
	return line + " · " + review.ID
}

// executeAdvisorReview runs the fork for one review, opening the sidechain
// session that lets the inspector replay it. A caller-supplied ctx bounds the
// run further (a step turn, a merge waiting on the verdict); it never extends
// the session's own lifetime, which a teardown cancels.
func (m *model) executeAdvisorReview(ctx context.Context, s *advisorSession, req advisor.Request) (advisor.Review, string, error) {
	runCtx, cancel := reviewContext(ctx, s.ctx)
	defer cancel()
	cwd := s.cwd
	var onEvent func(core.Event)
	var forkID string
	if store := m.services.SessionStore; store != nil {
		rec, err := store.Create(session.CreateOptions{
			AgentID:         advisorAgentID,
			Provider:        advisorProviderName(s.provider),
			Model:           s.modelID,
			MaxTokens:       s.cfg.ResolvedMaxTokens(),
			Cwd:             cwd,
			ParentSessionID: s.parentSessionID,
			Sidechain:       true,
		})
		if err != nil {
			// The review is still worth running without a replayable sidechain;
			// losing the audit trail is not a reason to skip the check.
			log.Logger().Warn("could not open advisor session", zap.Error(err))
		} else {
			defer func() { _ = rec.Close() }()
			onEvent = rec.RecordEvent
			forkID = rec.ID()
		}
	}

	client := llm.NewClient(s.provider, s.modelID, s.cfg.ResolvedMaxTokens())
	client.SetThinkingEffort(s.cfg.Model.ThinkingEffort)
	// The review is its own conversation, so it carries its own session id: a
	// vendor that gates on the session token sees one per review.
	client.SetSessionID(forkID)
	client.SetWorkingDir(func() string { return cwd })

	engine := advisor.NewEngine(advisor.EngineConfig{
		Client:         client.TurnClient,
		CallOptions:    client.CallOptions,
		ShapeInference: client.ShapeInference,
		// Read-only, and only the read tools this binary actually registers.
		Tools:       advisor.ReadOnlyTools(cwd),
		Policy:      s.policy,
		Model:       s.modelID,
		Mode:        s.cfg.Mode(),
		MaxTokens:   s.cfg.ResolvedMaxTokens(),
		Timeout:     s.cfg.ResolvedTimeout(),
		MaxFindings: s.cfg.ResolvedMaxFindings(),
		CWD:         cwd,
		OnEvent:     onEvent,
	})
	review, err := engine.Run(runCtx, req)
	return review, forkID, err
}

// reviewContext joins a caller's deadline with the session's teardown: the
// review stops when either ends, and cancelling the join never cancels the
// session context itself.
func reviewContext(caller, session context.Context) (context.Context, context.CancelFunc) {
	if caller == nil {
		return context.WithCancel(session)
	}
	ctx, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(session, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// persistAdvisorReview appends the review to the session transcript. The write
// is append-only: a re-run adds history instead of replacing the verdict it
// supersedes, and a resume replays it in order.
func (m *model) persistAdvisorReview(review advisor.Review) {
	sess := m.services.Session
	if sess == nil {
		return
	}
	stored := review
	if err := sess.Append(context.Background(), transcript.Record{
		Type:          transcript.AdvisorReviewed,
		AdvisorReview: &stored,
	}); err != nil {
		log.Logger().Warn("advisor review not persisted", zap.Error(err))
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

// advisorReviewLine is the one-line result notice. Detail belongs to the panel;
// this says what the verdict was and where to replay the review.
func advisorReviewLine(review advisor.Review, forkID string) string {
	var b strings.Builder
	b.WriteString("advisor · ")
	b.WriteString(string(review.Status))
	if counts := review.UnresolvedCounts(); counts.Total() > 0 {
		b.WriteString(" · ")
		b.WriteString(advisorCountsLine(counts))
	}
	if summary := firstLineOf(review.Summary); summary != "" {
		b.WriteString(" · ")
		b.WriteString(summary)
	}
	if forkID != "" {
		b.WriteString(" (san --resume " + forkID + ")")
	}
	return b.String()
}

// advisorFailureLine is the notice for a review that produced no verdict. It is
// never phrased as a pass: a review that could not run says nothing about the
// code.
func advisorFailureLine(err error) string {
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "see log"
	}
	return "advisor · review failed · " + msg
}

// advisorCountsLine renders the outstanding findings by level, skipping levels
// with none so the line stays short.
func advisorCountsLine(counts advisor.Counts) string {
	var parts []string
	for _, row := range []struct {
		name  string
		count int
	}{
		{"P0", counts.P0}, {"P1", counts.P1}, {"P2", counts.P2}, {"P3", counts.P3},
	} {
		if row.count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", row.name, row.count))
		}
	}
	return strings.Join(parts, " ")
}

// firstLineOf returns the first non-empty line of s, trimmed.
func firstLineOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// advisorCommand is /advisor: report the advisor's state, or run one review of
// the working tree now. The browsing panel arrives with the UI step; this is the
// manual trigger, and it is what makes the layer usable before a trigger fires.
func (m *model) advisorCommand(args string) (string, tea.Cmd) {
	cfg := m.advisorConfig()
	if !cfg.Enabled() {
		return "Advisor is off. Set advisor.enable in settings to review changes.", nil
	}
	// The runner is normally built when the agent runtime starts, so a review
	// asked for before the first turn would find none. Build it on demand: this
	// is a human asking for the advisor, and nothing else will run before the
	// first turn. wireAdvisor is idempotent, and skipping it while a runner
	// exists keeps a live review from being torn down by a status read.
	if m.services.Advisor.session == nil {
		m.wireAdvisor()
	}
	switch strings.TrimSpace(args) {
	case "", "status":
		return m.advisorStatusLine(), nil
	case "review":
		return m.startWorkingTreeReview()
	default:
		return "Usage: /advisor [review|status]", nil
	}
}

// advisorStatusLine reports the live configuration and the last verdict.
func (m *model) advisorStatusLine() string {
	cfg := m.advisorConfig()
	_, modelID := m.resolveAdvisorModel(cfg)
	state := "idle"
	if m.services.Advisor.session == nil {
		state = "off (no model connected)"
	}
	snap := m.services.Advisor.Indicator.Snapshot()
	if snap.Running {
		state = "reviewing"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "advisor · %s · %s · %s", state, cfg.Mode(), modelID)
	if snap.Latest != nil {
		fmt.Fprintf(&b, "\nlast: %s", advisorReviewLine(*snap.Latest, ""))
	}
	if snap.Err != "" {
		fmt.Fprintf(&b, "\nlast error: %s", snap.Err)
	}
	return b.String()
}

// startWorkingTreeReview reads the working diff and reviews it. The read is
// bounded and synchronous: this is a human-initiated command, and the diff hash
// it produces is what the cooldown and the history key on.
func (m *model) startWorkingTreeReview() (string, tea.Cmd) {
	if m.services.Advisor.session == nil {
		return "Advisor has no model to review with.", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), advisorDiffTimeout)
	defer cancel()
	diff, err := proc.ReadWorkingDiff(ctx, m.env.CWD, proc.DiffOptions{MaxBytes: m.advisorConfig().ResolvedMaxDiffBytes()})
	if err != nil {
		return "Advisor could not read a diff: " + err.Error(), nil
	}
	started := m.requestAdvisorReview(advisor.ReviewInput{
		Trigger:       advisor.TriggerUserRequest,
		Target:        advisor.Target{Kind: advisor.TargetDiff, Label: "working tree"},
		Diff:          diff.Diff,
		DiffHash:      diff.Hash,
		Files:         diff.Files,
		DiffTruncated: diff.Truncated,
	})
	if !started {
		return "Advisor did not start a review: one is already running, or this diff was just reviewed.", nil
	}
	return "Advisor is reviewing the working tree…", nil
}

// publishAdvisorStarted signals the main loop that a review began, so the UI can
// react on its own goroutine. Non-blocking: a full channel drops the signal and
// the status bar simply picks the state up on its next repaint.
func (m *model) publishAdvisorStarted() {
	select {
	case m.advisorStarts <- struct{}{}:
	default:
	}
}

// advisorStartedMsg wakes the Update loop when a review starts.
type advisorStartedMsg struct{}

// awaitAdvisorStart yields an advisorStartedMsg each time a review starts.
func awaitAdvisorStart(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return advisorStartedMsg{}
	}
}

// onAdvisorStarted re-arms the listener. The status bar reads the indicator at
// render time, so no extra ticker is needed yet.
func (m *model) onAdvisorStarted() tea.Cmd {
	return awaitAdvisorStart(m.advisorStarts)
}

// marshalAdvisor encodes the session's advisor override for the transcript,
// returning "" for an unset config so an untouched session carries no advisor
// state.
func marshalAdvisor(a setting.AdvisorSettings) string {
	if a.IsZero() {
		return ""
	}
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseAdvisor decodes a persisted override; a blank or malformed blob yields
// the zero config, which falls back to the settings file.
func parseAdvisor(s string) setting.AdvisorSettings {
	var a setting.AdvisorSettings
	if s != "" {
		_ = json.Unmarshal([]byte(s), &a)
	}
	return a
}
