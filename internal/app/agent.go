// Agent session lifecycle: building params, delegating to *agent.Runtime,
// and wrapping channels in tea.Cmds for the TUI.
package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/group"
	"github.com/genai-io/san/internal/interaction"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/mcp"
	"github.com/genai-io/san/internal/persona"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/transcript"
)

// ============================================================
// Build params from model state
// ============================================================

// activePersona returns the currently-selected persona, or nil for the
// built-in default (no settings.persona, or it doesn't resolve). A configured
// name that doesn't resolve logs a warning and falls back to the default.
func (m *model) activePersona() *persona.Persona {
	snap := m.services.Setting.Snapshot()
	if snap == nil || snap.Persona == "" || snap.Persona == persona.DefaultName {
		return nil
	}
	p, ok := m.services.Persona.Get(snap.Persona)
	if !ok || p.IsBuiltin() {
		log.Logger().Warn("configured persona not found; using built-in default",
			zap.String("persona", snap.Persona))
		return nil
	}
	return p
}

// personaPrompt resolves the system.Persona (prompt-part overrides) to build
// with — the active persona's three parts, or empty (the built-in default)
// when no persona is selected.
func (m *model) personaPrompt() system.Persona {
	if p := m.activePersona(); p != nil {
		return system.Persona{Identity: p.Identity, Behavior: p.Behavior, Rules: p.Rules}
	}
	return system.Persona{}
}

// applyPersonaSkills loads the active persona's bundled skills into the skill
// registry (in-memory, active by default), or clears them when no persona is
// selected. The skills-directory reminder picks the set up on its next emit.
func (m *model) applyPersonaSkills() {
	if p := m.activePersona(); p != nil {
		var states map[string]string
		if p.Settings != nil {
			states = p.Settings.Skills
		}
		m.services.Skill.LoadPersona(p.SkillDirs, states)
		return
	}
	m.services.Skill.ClearPersona()
}

// applyPersonaAgents restricts the visible subagents to the active persona's
// `agents` allow-list (empty/none = all visible), or clears the restriction
// when no persona is selected. In-memory, mirroring applyPersonaSkills.
func (m *model) applyPersonaAgents() {
	if p := m.activePersona(); p != nil && p.Settings != nil && len(p.Settings.Agents) > 0 {
		m.services.Subagent.LoadPersona(p.Settings.Agents)
		return
	}
	m.services.Subagent.ClearPersona()
}

// promptParams fills in the fields that shape the system prompt and the
// toolset, and only those. It is free of side effects, unlike buildAgentParams
// below, which layers the writer, hook wiring, and permission closures on
// top — so anything that only wants to know what the agent would send (see
// contextUsage) can call this without starting anything.
//
// Both callers going through one constructor is what keeps them in step: a new
// prompt-shaping input added here reaches the inspector for free, where a
// second hand-written literal would silently under-report.
func (m *model) promptParams() agent.BuildParams {
	return agent.BuildParams{
		CWD:     m.env.CWD,
		Persona: m.personaPrompt(),
		AgentCatalog: func() string {
			if m.services.Subagent == nil {
				return ""
			}
			return m.services.Subagent.AgentCatalog()
		},
		DisabledTools: m.disabledTools(),
		MCPTools:      mcp.AsCoreTools(m.services.MCP.ToolSchemas(), m.services.MCP),

		// Inject the Evolve trigger tool (tailored to the enabled capabilities)
		// so the model can queue its own self-learning reviews.
		ExtraTools: m.selfLearnExtraTools(),

		AutoMemory: m.autoMemory(),
	}
}

// syncMCPTools replaces the mcp__* tools in the running agent's registry with
// what the servers now advertise. A server that loads tools lazily, or gates
// them behind a login, changes its list while connected, and the registry is
// what the loop reads at every step — so the next step sees the change without
// the agent being rebuilt.
//
// It runs on the MCP session's own goroutine, which is why it touches only
// services: core.Tools locks, and so do Session.Tools and DisabledTools.
func (m *model) syncMCPTools() {
	tools := m.services.Agent.Tools()
	if tools == nil {
		return // no agent yet — whatever it is built with will be current
	}
	disabled := m.services.Setting.DisabledTools()

	live := make(map[string]bool)
	for _, t := range mcp.AsCoreTools(m.services.MCP.ToolSchemas(), m.services.MCP) {
		name := t.Schema().Name
		// The /tool panel's disable applies to MCP tools here for the same
		// reason agent.BuildParams applies it when the agent is built.
		if disabled[name] {
			continue
		}
		live[name] = true
		tools.Add(t, "mcp:"+name)
	}
	for _, schema := range tools.Schemas() {
		if mcp.IsMCPTool(schema.Name) && !live[schema.Name] {
			tools.Remove(schema.Name, "mcp:"+schema.Name)
		}
	}
}

func (m *model) buildAgentParams() (agent.BuildParams, error) {
	if m.services.Setting == nil || m.env.SessionPermissions == nil {
		return agent.BuildParams{}, fmt.Errorf("main-session runtime requires a permission policy")
	}
	params := m.promptParams()

	// Plan Mode can run on the plan role's model without the session's own model
	// changing: provider, model id, output cap and thinking effort all follow
	// whichever model answers this turn. The plan role's own effort wins over
	// the session's selection, because that selection belongs to a different
	// model; without one, the session's choice is validated on the plan model.
	modelInfo, provider, modelID := m.env.CurrentModel, m.env.LLMProvider, m.env.GetModelID()
	effort := m.env.EffectiveThinkingEffort()
	if target, ok := m.planModelTarget(); ok {
		modelInfo = m.roleModelInfo(target)
		provider, modelID = target.Provider, target.ModelID
		effort = m.resolveRoleEffort(target)
		if effort == "" {
			effort = llm.ResolveThinkingEffortForModel(target.Provider, m.services.LLM.Store(), modelInfo, m.env.ThinkingEffort)
		}
	}

	maxTokens := kit.GetMaxOutput(m.services.LLM.Store(), modelInfo, setting.DefaultMaxTokens)
	rec, err := m.openCurrentSession()
	if err != nil {
		return params, err
	}
	owner := agent.MainSession{Session: rec, Store: m.services.SessionStore}
	if err := owner.Validate(); err != nil {
		return params, err
	}
	for _, n := range m.earlyNotices {
		_ = rec.Append(context.Background(), transcript.Record{Type: transcript.NoticeAdded,
			Notice: &transcript.NoticeRecord{Text: n.Content, Agent: n.AgentNotice,
				AfterMessageID: conv.MemoryRecallNoticeAnchor(n.Content)}})
	}
	m.earlyNotices = nil

	// Build the autopilot judge into the atomic slot so /autopilot Save can
	// hot-swap it mid-session; the closures below Load it per call.
	m.rebuildAutopilotJudge()

	// Stream timeout overrides. Priority: SAN_* env var > settings.json > core
	// default (zero value => core picks FirstChunkTimeout=5m, IdleTimeout=60s).
	streamFirstChunk := firstValidDuration(
		setting.Getenv("STREAM_FIRST_CHUNK_TIMEOUT"),
		m.services.Setting.StreamFirstChunkTimeout(),
	)
	streamIdle := firstValidDuration(
		setting.Getenv("STREAM_IDLE_TIMEOUT"),
		m.services.Setting.StreamIdleTimeout(),
	)

	params.Provider = provider
	params.ModelID = modelID
	params.MaxTokens = maxTokens
	params.ThinkingEffort = effort
	params.FileCache = m.env.FileCache
	params.CWDFunc = func() string { return m.autopilot.Load().cwd }

	bridge := m.conv.AgentToUI
	params.AskUser = func(ctx context.Context, req *tool.QuestionRequest) (*tool.QuestionResponse, error) {
		return bridge.Ask(ctx, tool.AgentInteractionSource{}, req)
	}
	params.ToolActivity = bridge.SendForToolCall
	params.BashPromptResponder = func(ctx context.Context) tool.BashPromptResponder {
		// Interactive answering is opt-in via the Bash steer, read live (via
		// the synchronized snapshot — this runs on the agent goroutine) so a
		// mid-session toggle takes effect: without it, bash stays on the
		// normal non-tty path even in autopilot.
		if !m.autopilotEngaged() || !m.liveAutopilotConfig().Steers.BashPrompt {
			return nil
		}
		return bashPromptResponder{model: m, bridge: bridge}
	}

	if m.memoryRecall == nil {
		m.memoryRecall = &memoryRecall{surfaced: map[string]bool{}}
	}
	params.StreamFirstChunkTimeout = streamFirstChunk
	params.StreamIdleTimeout = streamIdle
	// Bind extension audits here, where the application owns their lifecycle.
	// Session validation and agent construction never rebind shared services.
	params.SessionID = rec.ID()
	params.OnEvent = rec.RecordEvent
	if hooks := m.services.Hook; hooks != nil {
		hooks.SetSession(rec.ID(), rec.TranscriptPath())
		hooks.SetAuditCallback(rec.RecordHookAudit)
		params.HookEngine = hooks
	}
	if skills := m.services.Skill; skills != nil {
		skills.SetStateChangeObserver(rec.RecordSkillStateChange)
	}
	policy := agent.PermissionPolicy{
		Settings: m.services.Setting, Session: m.env.SessionPermissions, AutoMemory: params.AutoMemory,
		Audit: func(record transcript.Record) { _ = rec.Append(context.Background(), record) },
	}
	params.PermissionRules = policy.Decide
	params.CanApproveByHook = policy.CanApproveByHook
	params.PermissionJudge = func(ctx context.Context, name string, args map[string]any, reason string) agent.PermJudgeResult {
		rt := m.autopilot.Load()
		if rt == nil {
			return agent.PermJudgeResult{}
		}
		return policy.Judge(ctx, name, args, reason, agent.JudgeSnapshot{
			Engaged: m.autopilotEngaged(), Enabled: rt.cfg.Steers.PermissionOn(),
			CWD: rt.cwd, UnderGit: rt.underGit, Mission: rt.cfg.ActiveMission(),
			Judge: rt.judge.Permission, Observe: m.recordDecision,
		})
	}
	params.ResultFilter = agent.WithRecall(
		agent.ResultPager{Store: owner.Store, SessionID: owner.Session.ID()}.Filter,
		m.memoryRecall.current.Load,
	)
	return params, nil
}

// sessionModelInfo is the session's own provider and model: the fallback every
// role resolution returns to, so a piece of work never fails because a role is
// unassigned or points at a vendor this session has not connected.
func (m *model) sessionModelInfo() *llm.CurrentModelInfo {
	info := &llm.CurrentModelInfo{ModelID: m.env.GetModelID()}
	if m.env.CurrentModel != nil {
		info.Provider, info.AuthMethod = m.env.CurrentModel.Provider, m.env.CurrentModel.AuthMethod
	}
	return info
}

// modelRoleBindings is the live modelRoles and modelRoleEfforts maps, or empty
// when settings are not wired (a bare test model) so an unassigned role simply
// inherits. Both maps are handed to the resolver together, so a role's model
// and the depth it asks for are read as one binding.
func (m *model) modelRoleBindings() llm.RoleBindings {
	if m.services.Setting == nil {
		return llm.RoleBindings{}
	}
	return llm.BindingsOf(m.services.Setting.ModelRoles(), m.services.Setting.ModelRoleEfforts())
}

// agentModelForRef resolves an agent definition's model reference for the
// /agents list: the model and reasoning effort that will answer its work, and
// whether the session model answered instead. A role goes through the same
// bindings the executor reads, so the list cannot advertise a model the run
// will not use.
func (m *model) agentModelForRef(ref string) (string, string, bool) {
	resolver := llm.NewStoreRoleResolver(m.llmStore())
	var target llm.RoleTarget
	if role, isRole := llm.ParseRoleAlias(ref); isRole {
		target = resolver.Resolve(context.Background(), m.modelRoleBindings(), role, m.env.LLMProvider, m.env.GetModelID())
	} else {
		target = resolver.ResolveRef(context.Background(), ref, m.env.LLMProvider, m.env.GetModelID())
	}
	return target.ModelID, m.resolveRoleEffort(target), target.Inherited
}

// resolveRole answers the provider and model for one model role. The role's
// modelRoles binding decides; an unassigned role, an "@role" chain that loops,
// or a binding whose vendor is not connected all fall back to the session
// model. This is the one place the app turns a role name into a model.
func (m *model) resolveRole(role llm.ModelRole) llm.RoleTarget {
	store := m.llmStore()
	return llm.NewStoreRoleResolver(store).Resolve(
		context.Background(), m.modelRoleBindings(), string(role),
		m.env.LLMProvider, m.env.GetModelID(),
	)
}

// resolveRoleModel answers just the provider and model id for role — the shape
// callers that hand a client or a judge a model need. A caller that also sends
// a reasoning effort asks resolveRoleEffort for the same target, so the two
// answers always describe one resolution.
func (m *model) resolveRoleModel(role llm.ModelRole) (llm.Provider, string) {
	target := m.resolveRole(role)
	return target.Provider, target.ModelID
}

// resolveRoleEffort answers the reasoning effort to send for a resolved role:
// the label bound to modelRoleEfforts for it, validated against the ladder of
// the model that actually answers. A stored label is a hint, not a value — a
// role rebound to a model without that rung sends the model's own default
// instead of a label the vendor would reject — so "" here means "send none or
// let the model decide", exactly like the picker's "default".
func (m *model) resolveRoleEffort(target llm.RoleTarget) string {
	return llm.ResolveTargetEffort(target.Provider, m.llmStore(), m.roleModelInfo(target), target)
}

// resolveRoleWithOverride answers role unless the caller's own setting names a
// model, in which case that reference wins. The precedence is the same
// everywhere it is used: an explicit setting (autoPilot.model, advisor.model,
// subagentModel) > the role's binding > the session model.
func (m *model) resolveRoleWithOverride(role llm.ModelRole, override string) llm.RoleTarget {
	override = strings.TrimSpace(override)
	if override == "" {
		return m.resolveRole(role)
	}
	return llm.NewStoreRoleResolver(m.llmStore()).ResolveRef(
		context.Background(), override, m.env.LLMProvider, m.env.GetModelID(),
	)
}

// llmStore is the provider store, or nil when nothing is connected. A nil store
// only costs vendor-qualified references their routing; it never fails a turn.
func (m *model) llmStore() *llm.Store {
	if m.services.LLM == nil {
		return nil
	}
	return m.services.LLM.Store()
}

// roleModelInfo turns a resolved target into the CurrentModelInfo the
// thinking-effort helpers key on. A vendor-qualified resolution carries that
// vendor; a bare id or the session fallback carries the session's provider.
func (m *model) roleModelInfo(target llm.RoleTarget) *llm.CurrentModelInfo {
	info := m.sessionModelInfo()
	info.ModelID = target.ModelID
	if vendor, _, ok := llm.ParseVendorModel(target.Ref); ok {
		info.Provider, info.AuthMethod = vendor, ""
	}
	return info
}

// planModelTarget is the model Plan Mode runs on. Plan Mode always resolves the
// deep role, so everything bound there reaches the turn: a different model, an
// "@role" chain, or an effort of the role's own on a model the session already
// holds. False — the session model answers and nothing changes — when Plan Mode
// is off, the plan store or settings are not wired, the deep role is unassigned,
// its reference resolves nowhere (an "@role" chain that loops, a vendor this
// session has not connected), or it lands on the session model with no effort
// bound. Plan Mode never fails a turn over an unassigned role; it runs where the
// conversation already runs and says so through warnPlanModelFallback.
func (m *model) planModelTarget() (llm.RoleTarget, bool) {
	if m.plan == nil || m.plan.Mode() != plan.ModePlan || m.services.Setting == nil {
		return llm.RoleTarget{}, false
	}
	target := m.resolveRole(llm.RoleDeep)
	// An inherited target is the session model by definition: either the role
	// is unassigned or its reference did not resolve. Either way there is
	// nothing here to route.
	if target.Inherited || target.ModelID == "" {
		return llm.RoleTarget{}, false
	}
	// The same model with no effort of the role's own is the session model
	// again; reporting it would only churn the runtime on every toggle.
	if target.ModelID == m.env.GetModelID() &&
		providerName(target.Provider) == providerName(m.env.LLMProvider) &&
		target.Effort == "" {
		return llm.RoleTarget{}, false
	}
	return target, true
}

// planDeepRoleNotice is what Plan Mode says when the deep role has no model: the
// turn still runs, on the session model, so the person learns the routing gap
// and where to close it instead of wondering why planning feels cheap.
const planDeepRoleNotice = "Plan Mode has no model assigned to the deep role, so it runs on the session model. " +
	"Assign one in /models → Roles → deep to plan on a stronger model."

// planDeepRoleUnassigned reports whether Plan Mode has nowhere to route: the
// deep role has no binding at all. A binding that fails to resolve is not this
// case — the turn lands on the session model either way, but the settings do
// name an intent, and only the missing binding is worth a notice.
func (m *model) planDeepRoleUnassigned() bool {
	if m.services.Setting == nil {
		return false
	}
	return strings.TrimSpace(m.services.Setting.ModelRole(string(llm.RoleDeep))) == ""
}

// warnPlanModelFallback tells the person once per gap that Plan Mode is running
// on the session model because the deep role is unassigned. The flag clears as
// soon as the role is assigned, so removing the assignment later warns again
// rather than staying silent for the rest of the session.
func (m *model) warnPlanModelFallback() {
	if !m.planDeepRoleUnassigned() {
		m.planDeepWarned = false
		return
	}
	if m.plan == nil || m.plan.Mode() != plan.ModePlan || m.planDeepWarned {
		return
	}
	m.planDeepWarned = true
	m.conv.AddNotice(planDeepRoleNotice)
}

// planModelSignature fingerprints the model and depth Plan Mode runs on: "" when
// the session model answers. ensureAgentRuntime compares it against the live
// agent's build, the same way it compares the toolset signatures beside it, so
// rebinding the plan role's effort reaches the runtime as a model change does.
func (m *model) planModelSignature() string {
	target, ok := m.planModelTarget()
	if !ok {
		return ""
	}
	return providerName(target.Provider) + "/" + target.ModelID + "/" + m.resolveRoleEffort(target)
}

// syncPlanModelDisplay points the status line at the model Plan Mode runs on, so
// the name it shows never lies about the model answering. Display only: nothing
// here touches the session model, its persisted choice, or the runtime.
func (m *model) syncPlanModelDisplay() {
	target, ok := m.planModelTarget()
	if !ok {
		m.env.PlanModel = nil
		return
	}
	m.env.PlanModel = m.roleModelInfo(target)
}

// syncPlanModelRuntime rebuilds the agent for a plan-role change on the next
// turn: an idle runtime is stopped so ensureAgentRuntime builds with the new
// model. A turn in flight keeps the model it started on — stopping it would
// discard its work — and the build-time signature check below catches the switch
// when that turn ends.
func (m *model) syncPlanModelRuntime() {
	if m.services.Agent == nil || !m.services.Agent.Active() || m.conv.Stream.Active {
		return
	}
	if m.planModelSignature() == m.agentPlanModel {
		return
	}
	if err := m.StopAgentRuntime(); err != nil {
		log.Logger().Warn("plan-mode model switch could not stop the runtime", zap.Error(err))
	}
}

// providerName is a provider's identity for comparisons; "" for no provider.
func providerName(p llm.Provider) string {
	if p == nil {
		return ""
	}
	return p.Name()
}

// resolveJudgeModel picks the provider and model for the autopilot judge:
// the caller's ref (autoPilot.model) when set, otherwise the fast role,
// otherwise the session model.
func (m *model) resolveJudgeModel(ref string) (llm.Provider, string) {
	p, info := m.resolveJudge(ref)
	return p, info.ModelID
}

// resolveJudge resolves a judge model ref to its provider and model, the
// autopilot/fast path. The info names whichever provider was actually picked,
// for capability lookups.
func (m *model) resolveJudge(ref string) (llm.Provider, *llm.CurrentModelInfo) {
	target := m.resolveJudgeTarget(ref)
	return target.Provider, m.roleModelInfo(target)
}

// resolveJudgeTarget is the same resolution with its provenance kept, so a
// caller that also sends an effort validates the role's label against the model
// that actually answers.
func (m *model) resolveJudgeTarget(ref string) llm.RoleTarget {
	return m.resolveRoleWithOverride(llm.RoleFast, ref)
}

// autopilotJudgeEffort is the precedence the autopilot judge follows: its own
// saved rung when the user set one, otherwise the effort bound to the role
// behind its model. An explicit setting is validated against the judge model it
// will be sent to; a role label travels already validated for the same reason.
func autopilotJudgeEffort(saved string, provider llm.Provider, store *llm.Store, info *llm.CurrentModelInfo, roleEffort string) string {
	if strings.TrimSpace(saved) != "" {
		return llm.ResolveThinkingEffortForModel(provider, store, info, saved)
	}
	return roleEffort
}

// workerModel is the model the Agent tool's default worker runs on: the
// subagentModel setting when the user set one (including an explicit "inherit"),
// otherwise the standard role's binding, otherwise "" — which leaves the executor
// inheriting the parent's model. The binding is returned as a reference, not a
// resolved model id, so the executor still routes a "vendor/model" binding to
// that vendor.
func (m *model) workerModel() string {
	if m.services.Setting == nil {
		return ""
	}
	return effectiveWorkerModel(m.services.Setting.SubagentModel(), m.services.Setting.ModelRole(string(llm.RoleStandard)))
}

// agentRoleFor answers the role /settings assigns to a built-in agent
// (agents.roles), or "" when the agent keeps the role its definition declared.
// It reads live settings on every launch, so a save reaches an executor that is
// already running; the executor calls it per run rather than capturing a map.
func (m *model) agentRoleFor(name string) string {
	if m.services.Setting == nil {
		return ""
	}
	return m.services.Setting.AgentRole(name)
}

// effectiveWorkerModel is workerModel's precedence rule, shared with the
// headless runner, which holds a *setting.Data rather than a TUI model.
func effectiveWorkerModel(subagentModel, taskRole string) string {
	if override := strings.TrimSpace(subagentModel); override != "" {
		return override
	}
	return strings.TrimSpace(taskRole)
}

// roleResolverFor is the "@role" resolver an agent executor receives, so a
// definition can hold a role ("@slow") and the user can move the model — and
// the depth — behind it. bindings is called per resolution rather than
// captured, because a settings reload must reach an executor that is already
// running. A role bound to nothing at all — or an alias chain that loops, or a
// vendor that is not connected — reports nothing, so the run keeps the parent
// model and behaves exactly as it did before roles carried an effort.
func roleResolverFor(bindings func() llm.RoleBindings, store *llm.Store, provider llm.Provider, modelID string) subagent.RoleResolver {
	resolver := llm.NewStoreRoleResolver(store)
	return func(role string) subagent.RoleAssignment {
		var roles llm.RoleBindings
		if bindings != nil {
			roles = bindings()
		}
		target := resolver.Resolve(context.Background(), roles, role, provider, modelID)
		// A role bound to nothing but an effort still says how hard its work
		// should think, on whatever model answers — the session model here. A
		// role bound to neither hands the run back to the executor untouched.
		if target.Inherited && target.Effort == "" {
			return subagent.RoleAssignment{}
		}
		return subagent.RoleAssignment{
			Provider: target.Provider,
			ModelID:  target.ModelID,
			Effort:   llm.ResolveTargetEffort(target.Provider, store, nil, target),
			Assigned: true,
		}
	}
}

// autopilotModelEfforts lists the reasoning rungs a "vendor/model" ref offers,
// or nil for a ref that cannot be resolved or a model that does not reason.
func (m *model) autopilotModelEfforts(ref string) []string {
	if _, _, ok := llm.ParseVendorModel(ref); !ok {
		return nil
	}
	p, info := m.resolveJudge(ref)
	return llm.ThinkingEffortsForModel(p, m.services.LLM.Store(), info)
}

// connectedModelRefs lists "vendor/model" refs for the model pickers (the
// /autopilot judge and /models): the cached models of each connected provider
// under its connected auth method.
func (m *model) connectedModelRefs() []string {
	store := m.services.LLM.Store()
	if store == nil {
		return nil
	}
	cached := store.CachedModelsByProvider()
	var refs []string
	for vendor, conn := range store.Connections() {
		for _, mdl := range cached[vendor+":"+string(conn.AuthMethod)] {
			refs = append(refs, vendor+"/"+mdl.ID)
		}
	}
	slices.Sort(refs)
	return refs
}

// recordDecision tallies one judge decision for the status-bar count and
// stashes it for the renderer, keyed by the tool call ID carried in ctx and
// consumed (load + delete) in TakeJudgeDecision — so the handoff map only ever
// holds calls whose result has not arrived yet. The approved flag
// picks the counter, so the count and the inline annotations can never drift.
// The count is bumped even when ctx carries no tool call ID; only the stash
// needs one.
func (m *model) recordDecision(ctx context.Context, approved bool, reason string) {
	if approved {
		m.judgeApprovals.Add(1)
	} else {
		m.judgeEscalations.Add(1)
	}
	if id := core.ToolCallIDFromContext(ctx); id != "" {
		m.pendingDecisions.Store(id, core.JudgeDecision{Approved: approved, Reason: reason})
	}
}

// TakeJudgeDecision consumes the decision stashed for a tool call (load + delete),
// so it stamps exactly one rendered result and the handoff map holds only
// in-flight calls. Returns nil when the call was not judged. Mirrors the
// tool side-effect handoff (Tool.PopSideEffect), consumed at the same point in
// OnToolResult.
func (m *model) TakeJudgeDecision(callID string) *core.JudgeDecision {
	// No empty-callID guard: recordDecision never stores under an empty key, so
	// LoadAndDelete("") already misses and returns nil.
	v, ok := m.pendingDecisions.LoadAndDelete(callID)
	if !ok {
		return nil
	}
	decision := v.(core.JudgeDecision)
	return &decision
}

// firstValidDuration returns the first value that parses as a time.Duration
// (e.g. "30s", "3m"), in priority order. Empty/invalid values are skipped;
// zero means "no override" and lets the core default apply.
func firstValidDuration(vals ...string) time.Duration {
	for _, v := range vals {
		if v == "" {
			continue
		}
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 0
}

// ============================================================
// Agent lifecycle (delegates to services.Agent)
// ============================================================

// disabledTools respects configured worker-messaging disables. Group sessions
// additionally hold SendMessage on so their members remain reachable.
func (m *model) disabledTools() map[string]bool {
	disabled := maps.Clone(m.services.Setting.DisabledTools())
	for name := range toolsHeldOn() {
		delete(disabled, name)
	}
	return disabled
}

// toolsHeldOn names the tools this session keeps on whatever the settings
// say, and why: the runtime layer over the configured one, read by the agent
// build and shown in /tool.
func toolsHeldOn() map[string]string {
	if g, _ := group.Current(); g != "" {
		return map[string]string{
			tool.ToolSendMessage: "on while in a group",
		}
	}
	return nil
}

// disabledToolsSignature is a stable fingerprint of the effective disabled-tool
// set. ensureAgentRuntime compares it against the live agent's build-time value
// so a /tool enable/disable (which the built-in and MCP tool filters both read)
// rebuilds the toolset on the next turn.
func (m *model) disabledToolsSignature() string {
	disabled := m.disabledTools()
	names := make([]string, 0, len(disabled))
	for name, off := range disabled {
		if off {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return strings.Join(names, "\x00")
}

// ensureAgentRuntime starts the agent with its retained native conversation.
func (m *model) ensureAgentRuntime() (tea.Cmd, error) {
	// Plan Mode's model is chosen per turn, so the status line has to follow the
	// mode even when the runtime is reused as-is.
	m.syncPlanModelDisplay()
	if m.services.Agent.Active() {
		// The toolset and the model are fixed at agent-build time. If the
		// enabled Evolve capabilities, the disabled-tool set or the model Plan
		// Mode runs on drifted since the build (a /evolve save, a /tool toggle,
		// a plan-role assignment, an external settings edit), stop the stale
		// session and fall through to rebuild with the current one — the single
		// choke point every turn passes through, so all drift sources are
		// covered uniformly.
		if m.selfLearnCapabilities() == m.agentEvolveCaps &&
			m.disabledToolsSignature() == m.agentDisabledToolsSignature &&
			m.autoMemory() == m.agentAutoMemory &&
			m.planModelSignature() == m.agentPlanModel {
			// The advisor is not part of the agent's build, so a settings edit
			// or an /advisor toggle rewires just it.
			m.syncAdvisor()
			return nil, nil
		}
		if err := m.StopAgentRuntime(); err != nil {
			return nil, err
		}
	}

	// Rebuild the subagent executor alongside the main agent so it inherits the
	// same effective disabled-tool snapshot after settings/persona drift.
	m.ReconfigureAgentTool()
	params, err := m.buildAgentParams()
	if err != nil {
		return nil, err
	}
	if w := m.services.Session; w != nil && w.Err() != nil {
		return nil, w.Err()
	}
	coreMessages := m.services.Agent.Messages()

	m.cancelManualCompaction()
	if err := m.services.Agent.Start(params, coreMessages); err != nil {
		return nil, err
	}
	m.agentSession = m.services.Session
	// Record what the toolset was built with, for the drift check above.
	m.agentEvolveCaps = m.selfLearnCapabilities()
	m.agentDisabledToolsSignature = m.disabledToolsSignature()
	m.agentPlanModel = m.planModelSignature()
	// A freshly built prompt knows nothing of the mode the session is in, so a
	// rebuild while planning (a plan-role model change, a /tool toggle, a
	// resume) would hand the agent a prompt that permits writes. Reinstate it
	// here: this is the one place every runtime passes through.
	m.syncPlanModePrompt()
	if m.services.Reminder.Prepared() && params.AutoMemory != m.agentAutoMemory {
		// The index reminder was prepared once, naming the old stores.
		m.services.Reminder.RequeueSystemReminders()
		if params.AutoMemory.Off() {
			m.services.Reminder.Enqueue("Auto memory is now off: stop reading and saving memory files.")
		}
	}
	m.agentAutoMemory = params.AutoMemory
	conv.MemoryStores = params.AutoMemory

	// Wire L1 self-learning *after* Agent.Start so the ReviewFunc can capture
	// the live Agent + System for its fork. Builds nothing if every skill action is
	// denied (§3.1 zero-overhead guarantee).
	m.wireSelfLearn(params)
	// Wire the global advisor from the same live provider/model the agent was
	// built with. Builds nothing when the advisor is off, so a disabled review
	// layer costs no goroutine and no model call.
	m.wireAdvisor()
	m.agentAdvisorSignature = m.advisorSignature()

	cmds := []tea.Cmd{
		conv.DrainAgentOutbox(m.services.Agent.Outbox()),
		conv.PollPermGate(m.services.Agent.PermissionGate()),
	}
	return tea.Batch(cmds...), nil
}

// agentSendFailedMsg reports a message the agent never accepted; a delivered
// message needs no round trip.
type agentSendFailedMsg struct{ err error }

type pendingMessage = agent.PendingInput

func (m *model) sendToAgent(p pendingMessage) tea.Cmd {
	m.pendingStopHook = nil
	m.pendingMessages.Enqueue(p)
	return m.sendPendingMessages()
}

func (m *model) sendPendingMessages() tea.Cmd {
	if !m.pendingMessages.CanDeliver(false) {
		return nil
	}
	if !m.services.Agent.Active() {
		return nil
	}
	if !m.conv.Stream.Active && m.pendingMessages.Len() > 0 {
		m.turnOpenedBy = m.pendingMessages.First().ID
	}
	if m.pendingPluginRoot != "" {
		m.services.Agent.SetPluginRoot(m.pendingPluginRoot)
		m.pendingPluginRoot = ""
	}
	if err := m.pendingMessages.Deliver(m.services.Agent.Send, m.attachPendingReminders, m.startMemoryRecall); err != nil {
		return func() tea.Msg { return agentSendFailedMsg{err: err} }
	}
	if !m.conv.Stream.Active {
		m.conv.Stream = conv.StreamState{Active: true, Waiting: true, StartedAt: time.Now()}
		return m.conv.Spinner.Tick
	}
	return nil
}

// memoryRecall is the pick running beside the current turn, read by the
// agent goroutine's result filter.
type memoryRecall struct {
	current  atomic.Pointer[agent.Recall]
	surfaced map[string]bool // attached in this conversation, never twice
}

// startMemoryRecall starts picking the memories a message sent to the agent
// needs, replacing a pick for an earlier one. The message does not wait.
func (m *model) startMemoryRecall(query string) {
	if m.memoryRecall == nil || strings.TrimSpace(query) == "" || conv.IsAgentEnvelope(query) || m.agentAutoMemory.Off() {
		return
	}
	if !m.services.Reminder.Prepared() || m.memoryRecall.surfaced == nil {
		m.memoryRecall.surfaced = map[string]bool{} // a new conversation has seen none
	}
	// The pick is deliberately not a role: it runs at the model's lowest rung
	// beside the turn, and the plan routes only smol work (judges, hooks, the
	// self-learning review) through modelRoles.
	info := m.sessionModelInfo()
	r := agent.Recaller{Provider: m.env.LLMProvider, Model: info.ModelID, Effort: agent.LowestEffort(m.env.LLMProvider, m.llmStore(), info)}
	m.memoryRecall.current.Swap(r.Start(m.agentAutoMemory, query, maps.Clone(m.memoryRecall.surfaced))).Stop()
}

// endMemoryRecall drops the turn's pick and returns what it attached, which
// later picks in this conversation skip.
func (m *model) endMemoryRecall() []string {
	if m.memoryRecall == nil {
		return nil
	}
	pick := m.memoryRecall.current.Swap(nil)
	pick.Stop()
	recalled := pick.Attached()
	for _, path := range recalled {
		m.memoryRecall.surfaced[path] = true
	}
	return recalled
}

// attachPendingReminders drains the reminder queue and appends any pending
// <system-reminder> blocks to the user message content. The harness uses this
// channel to deliver session/project context (skills, memory, one-time notices)
// without invalidating the system-prompt cache prefix.
func (m *model) attachPendingReminders(msg core.Message) core.Message {
	// Reminders (the roster among them) precede held member messages.
	text := m.attachWaitingMessages(m.services.Reminder.PrepareInput(msg.Text()))
	if text == msg.Text() {
		return msg
	}
	content := ai.TextContent(text)
	for _, block := range msg.Content {
		if block.Type != ai.BlockText {
			content = append(content, block)
		}
	}
	msg.Content = content
	return msg
}

// wireReminderProviders registers the harness providers attached once per
// conversation. Each provider's render closure
// captures the services struct pointer so it always reads the live state
// — that way settings reload and skill toggles surface in the next emission
// without ever mutating the cached system prompt.
func (m *model) wireReminderProviders() {
	// Skill.PromptSection already produces a self-introduced body
	// ("Use the Skill tool to invoke these capabilities: ...") so it goes
	// inside <system-reminder> verbatim, matching Claude Code's shape.
	m.services.Reminder.Register(reminder.ProviderSkillsDirectory, func() string {
		return m.services.Skill.PromptSection()
	})
	m.services.Reminder.Register(reminder.ProviderMemoryUser, func() string {
		return reminder.WrapMemory("user", m.env.CachedUserInstructions)
	})
	m.services.Reminder.Register(reminder.ProviderMemoryProject, func() string {
		return reminder.WrapMemory("project", m.env.CachedProjectInstructions)
	})
	m.services.Reminder.Register(reminder.ProviderGroup, group.Roster)
	// Agent-written auto memory, read at render time so a PostCompact or cwd
	// change picks up the latest index. Kept as its own scope so agent-written
	// entries never mix with the user-authored memory above.
	m.services.Reminder.Register(reminder.ProviderMemoryAuto, func() string {
		return agent.AutoMemoryReminder(m.autoMemory())
	})
}

const agentStopTimeout = 2 * time.Second

func (m *model) StopAgentRuntime() error {
	m.cancelManualCompaction()
	m.pendingStopHook = nil
	ctx, cancel := context.WithTimeout(context.Background(), agentStopTimeout)
	defer cancel()
	if err := m.services.Agent.Stop(ctx); err != nil {
		log.Logger().Warn("agent runtime shutdown incomplete", zap.Error(err))
		return fmt.Errorf("stop agent runtime: %w", err)
	}
	m.teardownSelfLearn()
	m.teardownAdvisor()
	return nil
}

// ResetAgentRuntime stops the agent and clears its conversation and UI state.
func (m *model) ResetAgentRuntime() error {
	m.cancelManualCompaction()
	m.pendingStopHook = nil
	ctx, cancel := context.WithTimeout(context.Background(), agentStopTimeout)
	defer cancel()
	if err := m.services.Agent.Reset(ctx); err != nil {
		log.Logger().Warn("agent runtime reset incomplete", zap.Error(err))
		return fmt.Errorf("reset agent runtime: %w", err)
	}
	m.pendingMessages.ResetAdmission()
	m.pendingPluginRoot = ""
	m.resetInteractions()
	// The next conversation carries neither the old notices nor, until its
	// first message, the skills/memory reminders.
	m.services.Reminder.ResetConversation()
	// Compaction must not restore the old conversation's files.
	m.env.FileCache.Clear()
	// Stop feeding the L1 judge AND cancel the session-scoped context
	// so an in-flight fork unblocks immediately instead of holding tokens /
	// HTTP for up to forkDeadline.
	m.teardownSelfLearn()
	// The advisor belongs to the conversation that is going away: cancel its
	// context and drop its review history, so nothing from the old session is
	// published or persisted into the new one. The cursor goes with it — the
	// next conversation starts from its own end, not this one's position.
	m.teardownAdvisor()
	m.advisorReviewed = -1
	m.advisorPending = false
	m.advisorFollowups = 0
	m.services.AdvisorIndicator.Reset()
	// The per-role request counts belong to the conversation going away.
	m.services.Usage.Reset()
	m.env.Advisor = setting.AdvisorSettings{}
	if m.services.Setting != nil {
		m.env.Advisor = m.services.Setting.Advisor()
	}
	return nil
}

// ============================================================
// Agent outbox and permission gate
// ============================================================

func (m *model) AgentOutbox() <-chan core.Event { return m.services.Agent.Outbox() }

func (m *model) ListenForAgentEvents() tea.Cmd {
	outbox := m.services.Agent.Outbox()
	if outbox == nil {
		return nil
	}
	return conv.DrainAgentOutbox(outbox)
}

func (m *model) HandlePermGate(req *agent.PermGateRequest) tea.Cmd {
	if req == nil || (req.Context != nil && req.Context.Err() != nil) {
		return conv.PollPermGate(m.services.Agent.PermissionGate())
	}
	permissionView, err := m.preparePermissionRequest(req)
	if err != nil {
		// Preparation failures are tool errors, not requests for human consent.
		// The gate owns a buffered response channel; never enqueue an invalid
		// call or let a generic preview hide the actionable validation reason.
		select {
		case req.Response <- agent.PermGateResponse{Reason: fmt.Sprintf("cannot prepare %s approval: %v", req.ToolName, err)}:
		default:
		}
		return conv.PollPermGate(m.services.Agent.PermissionGate())
	}
	return tea.Batch(m.enqueueInteraction(&conv.InteractionRequest{
		Context: req.Context, Owner: m.conv.AgentToUI, Permission: req, PermissionView: permissionView,
		Source: tool.AgentInteractionSource{WorkingDirectory: m.env.CWD, PermissionMode: m.env.SessionMode()},
	}), conv.PollPermGate(m.services.Agent.PermissionGate()))
}

// ============================================================
// Agent tool configuration
// ============================================================

// parentPosture is the permission mode a worker inherits from the session. Plan
// Mode is a read-only posture of its own: the session mode may still read
// "normal" while the planning gate refuses every write, so a worker that
// inherits the parent's posture must start from explore rather than from a mode
// that can edit.
func parentPosture(perms *setting.SessionPermissions) subagent.PermissionMode {
	if perms == nil {
		return subagent.PermissionDefault
	}
	if perms.PlanModeActive() {
		return subagent.PermissionExplore
	}
	return subagent.InheritedPermissionMode(perms.CurrentMode())
}

func (m *model) preparePermissionRequest(req *agent.PermGateRequest) (*perm.PermissionRequest, error) {
	return interaction.PreparePermission(req, m.env.CWD, m.env.SessionMode(), m.services.Setting, m.env.SessionPermissions)
}

func (m *model) ReconfigureAgentTool() {
	if m.env.LLMProvider == nil {
		return
	}
	m.ensureMemoryContextLoaded()

	executor := subagent.NewExecutor(m.env.LLMProvider, m.env.CWD, m.env.GetModelID(), m.services.Hook)
	executor.SetInteractions(m.conv.AgentToUI)
	executor.SetDefaultModel(m.workerModel())
	executor.SetResolver(llm.NewProviderPool(m.services.LLM.Store()))
	resolver := roleResolverFor(m.modelRoleBindings, m.llmStore(), m.env.LLMProvider, m.env.GetModelID())
	executor.SetRoleResolver(resolver)
	executor.SetAgentRoleSource(m.agentRoleFor)
	// Every finished run reports its inference steps to the session counter,
	// keyed by the role /settings assigned the definition (otherwise by the
	// definition's own name). The observer runs on the worker's goroutine, so
	// it only touches the mutex-guarded counter.
	executor.SetRunObserver(func(name string, steps int) {
		role := strings.TrimPrefix(strings.TrimSpace(m.agentRoleFor(name)), "@")
		if role == "" {
			role = name
		}
		m.services.Usage.Add(role, steps)
	})
	// The Agent tool's catalog advertises the role each definition will run on,
	// so a model choosing an agent reads what /settings configured rather than
	// what the definition file alone declares.
	m.services.Subagent.SetAgentRoleSource(m.agentRoleFor)
	perms := m.env.SessionPermissions
	executor.SetParentPermissionMode(func() subagent.PermissionMode {
		return parentPosture(perms)
	})
	if m.services.SessionStore != nil && m.sessionID() != "" {
		executor.SetSessionStore(m.services.SessionStore, m.sessionID())
	}
	executor.SetProjectInstructions(m.env.CachedProjectInstructions)
	executor.SetSkillsDirectory(m.services.Skill.PromptSection())
	executor.SetMCPDependencies(m.services.MCP)
	executor.SetDisabledTools(m.services.Setting.DisabledTools())

	adapter := subagent.NewExecutorAdapter(executor)
	installAutoTier(adapter, executor.AgentRegistry(), m.autoTierEnabled(), resolver, m.env.LLMProvider, m.env.GetModelID())
	type executorSetter interface{ SetExecutor(tool.AgentExecutor) }
	type searchPathSetter interface{ SetSearchPaths([]string) }
	home, _ := os.UserHomeDir()
	for _, name := range []string{tool.ToolAgent, tool.ToolSendMessage, tool.ToolWorkflow} {
		t, ok := m.services.Tool.Get(name)
		if !ok {
			continue
		}
		if setter, ok := t.(executorSetter); ok {
			setter.SetExecutor(adapter)
		}
		// Saved workflows resolve project-first, then user, the way subagent
		// definitions do. A home directory we could not read is left out
		// rather than joined from "", which would name the project twice.
		if setter, ok := t.(searchPathSetter); ok {
			dirs := []string{filepath.Join(confdir.Dir(m.env.CWD), "workflows")}
			if home != "" {
				dirs = append(dirs, filepath.Join(confdir.Dir(home), "workflows"))
			}
			setter.SetSearchPaths(dirs)
		}
	}
}

// ============================================================
// LLM client
// ============================================================

func (m *model) buildLLMClient() *llm.Client {
	c := llm.NewClient(m.env.LLMProvider, m.env.GetModelID(), kit.GetMaxOutput(m.services.LLM.Store(), m.env.CurrentModel, setting.DefaultMaxTokens))
	c.SetThinkingEffort(m.env.EffectiveThinkingEffort())
	// Which conversation this client answers for, and where. The session is the
	// writer's, so the identity a vendor sees survives a resume; it is absent
	// before the first session is opened, and the vendor then falls back to
	// this installation.
	if m.agentSession != nil {
		c.SetSessionID(m.agentSession.ID())
	}
	c.SetWorkingDir(func() string { return m.env.CWD })
	return c
}
