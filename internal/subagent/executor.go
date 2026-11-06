package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/mcp"
	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"go.uber.org/zap"
)

// ProviderResolver turns a vendor name into a live provider so a subagent can
// run on a different vendor than its parent. The app wires an *llm.ProviderPool;
// unresolved explicit "vendor/model" overrides fall back to the parent provider.
type ProviderResolver interface {
	Resolve(ctx context.Context, provider llm.ProviderID) (llm.Provider, error)
}

type executionDirectoryKey struct{}

// Executor runs agent LLM loops
type Executor struct {
	interactions               tool.AgentInteractions
	provider                   llm.Provider
	registry                   *Registry
	resolver                   ProviderResolver // resolves "vendor/model" overrides; nil = same-provider only
	roleResolver               RoleResolver     // resolves "@role" models; nil = the parent model answers
	cwd                        string
	parentModelID              string // Parent conversation's model ID (used when inheriting)
	defaultModel               string // Configured model for the default worker; empty inherits.
	parentPermissionModeGetter func() PermissionMode
	hooks                      hook.Handler
	sessionStore               SubagentSessionStore // Optional: when set, subagent sessions are persisted
	parentSessionID            string               // Parent session ID for linking subagent sessions
	projectInstructions        string               // project memory (AGENTS.md) for edit-capable subagents
	skillsPrompt               string               // available skills section for capable subagents
	resolveSkill               func(string) string  // nil uses the TUI registry
	mcpManager                 *mcp.Manager         // tool schemas, execution, per-subagent server sets
	disabledToolsMu            sync.RWMutex
	disabledTools              map[string]bool // effective global disabled tools, copied on set/read
	tasks                      *task.Manager
	permission                 func(bool, string) perm.PermissionFunc
	toolContext                func(context.Context, string) context.Context
	postTool                   func(core.ToolResult, any)
}

type SubagentSessionStore interface {
	RecordSubagentConversation(parentSessionID, title, modelID, cwd string, messages []core.Message) (string, string, error)
}

type runConfig struct {
	disabledTools map[string]bool
	config        *AgentConfig
	provider      llm.Provider // provider this run talks to (parent's, or a routed vendor)
	modelID       string
	effort        string // effort bound to the role that supplied modelID; "" = that model's own default
	maxSteps      int
	displayName   string
	brief         system.SubagentBrief // identity/charter for this run; immutable
	permMode      PermissionMode
}

// NewExecutor creates a new agent executor. parentModelID is used for model
// inheritance; hookEngine, when non-nil, fires subagent lifecycle hooks.
// Headless callers inherit the safe default permission policy unless they set a
// parent permission mode getter.
func NewExecutor(llmProvider llm.Provider, cwd string, parentModelID string, hookEngine hook.Handler) *Executor {
	return &Executor{
		provider:      llmProvider,
		registry:      Default(),
		cwd:           cwd,
		parentModelID: parentModelID,
		hooks:         hookEngine,
	}
}

// ConfigureOwner binds a host's worker resources before launching any runs.
// Permission must enforce the parent policy even for child bypass/allow rules.
// Background policies must never retain a prompt-scoped transport receiver.
func (e *Executor) ConfigureOwner(tasks *task.Manager, registry *Registry, permission func(bool, string) perm.PermissionFunc, toolContext func(context.Context, string) context.Context) {
	e.tasks, e.registry, e.permission, e.toolContext = tasks, registry, permission, toolContext
}

// SetPostTool observes owned handoffs before they are consumed by worker CWD handling.
func (e *Executor) SetPostTool(post func(core.ToolResult, any)) { e.postTool = post }

func (e *Executor) taskPool() *task.Manager {
	if e.tasks != nil {
		return e.tasks
	}
	return task.Default()
}

// SetInteractions configures UI forwarding before any workers are launched.
func (e *Executor) SetInteractions(interactions tool.AgentInteractions) {
	e.interactions = interactions
}

// SetParentPermissionMode provides the parent session's live permission mode.
// It is evaluated for every run so mode="default" follows mode changes made
// after the executor was configured, including entering bypass mode.
func (e *Executor) SetParentPermissionMode(getMode func() PermissionMode) {
	e.parentPermissionModeGetter = getMode
}

func (e *Executor) currentParentPermissionMode() PermissionMode {
	if e.parentPermissionModeGetter == nil {
		return PermissionDefault
	}
	return NormalizePermissionMode(string(e.parentPermissionModeGetter()))
}

// SetProjectInstructions provides the project's instruction memory
// (AGENTS.md). Edit-capable subagents receive it as a
// <system-reminder> so their changes follow project conventions; read-only
// agents do not carry it.
func (e *Executor) SetProjectInstructions(instructions string) {
	e.projectInstructions = instructions
}

// SetResolver enables cross-provider routing: a subagent whose model is an
// explicit "vendor/model" override resolves through this resolver instead of
// reusing the parent's provider. Unavailable routes fall back to the parent.
func (e *Executor) SetResolver(r ProviderResolver) {
	e.resolver = r
}

// RoleAssignment is one role resolved for a run: the provider and model that
// answer the work, the reasoning effort the user bound to the role, and whether
// the role resolved at all.
//
// Effort is already validated by the host against the ladder of the model it
// accompanies — the live catalog is the only thing that knows which rungs a
// model offers — so the executor sends it unchanged. "" means the model's own
// default.
type RoleAssignment struct {
	Provider llm.Provider
	ModelID  string
	Effort   string
	// Assigned reports that the role resolved to a model; false means the
	// session model answers instead and Effort is unused.
	Assigned bool
}

// RoleResolver turns a role name into the provider, model and effort that
// answer the work. It is a func rather than an interface because the host owns
// role bindings: the executor must not learn how settings are stored, only what
// the role currently resolves to.
type RoleResolver func(role string) RoleAssignment

// SetRoleResolver enables "@role" model references in agent definitions, so a
// definition can name the kind of work ("@slow") while the user decides which
// model answers it. An unassigned role, a nil resolver, or a reference that
// cannot be resolved keeps the parent model: incomplete configuration must
// never fail a run.
func (e *Executor) SetRoleResolver(r RoleResolver) {
	e.roleResolver = r
}

// SetSkillsDirectory provides the skills directory section so subagents
// with the Skill tool can see and invoke available skills.
func (e *Executor) SetSkillsDirectory(skillsPrompt string) {
	e.skillsPrompt = skillsPrompt
}

// SetMCPDependencies wires MCP tool access and server connections.
func (e *Executor) SetMCPDependencies(registry *mcp.Manager) {
	e.mcpManager = registry
}

// SetDisabledTools supplies the effective global disabled-tool policy inherited
// by subsequently built subagents. The map is copied so settings reloads and
// concurrent runs cannot mutate a tool set while it is being constructed.
func (e *Executor) SetDisabledTools(disabled map[string]bool) {
	e.disabledToolsMu.Lock()
	e.disabledTools = maps.Clone(disabled)
	e.disabledToolsMu.Unlock()
}

func (e *Executor) disabledToolsSnapshot() map[string]bool {
	e.disabledToolsMu.RLock()
	defer e.disabledToolsMu.RUnlock()
	return maps.Clone(e.disabledTools)
}

// SetSessionStore configures session persistence for subagent conversations.
// When set, completed subagent conversations are saved under the parent session.
func (e *Executor) SetSessionStore(store SubagentSessionStore, parentSessionID string) {
	e.sessionStore = store
	e.parentSessionID = parentSessionID
}

// SetDefaultModel configures the default worker independently of tool arguments.
// Configure before launching runs. Empty or "inherit" selects the parent model.
func (e *Executor) SetDefaultModel(model string) {
	e.defaultModel = strings.TrimSpace(model)
}

// configuredModel is the model reference a run asks for: an unnamed default
// worker follows the configured worker model, a definition the caller named
// uses its own. A posture default is the exception: the executor substitutes
// the explore definition without the caller naming it, so the run must use
// explore's role instead of the default worker's model.
func (e *Executor) configuredModel(config *AgentConfig, name string) string {
	unnamed := strings.TrimSpace(name) == ""
	if config.displayOnly || (unnamed && strings.TrimSpace(config.Name) == "") {
		return e.defaultModel
	}
	return config.Model
}

// ParentModelID returns the parent model ID
func (e *Executor) ParentModelID() string {
	return e.parentModelID
}

// Run executes an agent request and returns the result.
// For background agents, this should be called in a goroutine.
//
// Every exit path fires the SubagentStop hook with the same AgentID the
// SubagentStart hook carried. Cancelled runs persist their conversation and
// partial output for inspection.
func (e *Executor) Run(ctx context.Context, req tool.AgentExecRequest) (*AgentResult, error) {
	if req.ResumeTaskID != "" {
		return nil, fmt.Errorf("continuation requires background execution")
	}
	if req.ParentToolCallID == "" {
		req.ParentToolCallID = core.ToolCallIDFromContext(ctx)
	}
	run, err := e.prepareRun(ctx, req)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(task.WithSessionID(ctx, e.parentSessionID))
	defer cancel()
	worker := e.taskPool().CreateForegroundAgentTask(generateShortID(), run.cfg.config.Name, req.Description, ctx, cancel)
	run.req.TaskID = worker.GetID()
	run.checkpoint = newContinuationState(run, e.parentSessionID, worker.GetID())
	worker.SetLaunchState(run.checkpoint)
	worker.SetPermissionMode(string(run.cfg.permMode))
	result, err := e.runPrepared(ctx, run)
	if result != nil {
		worker.SetIdentity(run.cfg.config.Name, result.AgentID)
		worker.SetInspectionFile(result.TranscriptPath)
		worker.AppendOutput([]byte(result.Content))
		worker.UpdateProgress(result.StepCount, result.TokenUsage.Input+result.TokenUsage.Output)
	}
	endErr := err
	if endErr == nil && result != nil && !result.Success {
		endErr = fmt.Errorf("%s", result.Error)
	}
	worker.Complete(endErr)
	return result, err
}

func (e *Executor) runPrepared(ctx context.Context, run *preparedRun) (*AgentResult, error) {
	e.logRunStart(run)
	e.fireSubagentStart(run.req, run.hookID)

	result, err := e.executePreparedRun(ctx, run)
	if err != nil {
		if unfinished := e.buildUnfinishedAgentResult(run, result); unfinished != nil {
			return unfinished, err
		}
		e.fireSubagentStop(run.req, run.hookID, "", "")
		return nil, fmt.Errorf("LLM completion failed: %w", err)
	}

	return e.buildAgentResult(run, result), nil
}

// RunBackground executes an agent in the background and returns the task.
func (e *Executor) RunBackground(req tool.AgentExecRequest) (*task.AgentTask, error) {
	req.Background = true
	ctx, cancel := context.WithCancel(task.WithSessionID(context.Background(), e.parentSessionID))
	// Resolve inherited permissions before registration and goroutine scheduling,
	// not later when the parent may already have changed modes.
	if e.interactions == nil {
		req.OnQuestion = nil // Headless background workers have no question channel.
	}
	var run *preparedRun
	var agentTask *task.AgentTask
	var err error
	if req.ResumeTaskID != "" {
		if _, ok := e.sessionStore.(liveSessionStore); !ok {
			cancel()
			return nil, fmt.Errorf("native continuation requires live session persistence")
		}
		found, ok := e.taskPool().Get(req.ResumeTaskID)
		if ok {
			agentTask, ok = found.(*task.AgentTask)
		}
		if !ok || agentTask.GetStatus().ParentSessionID != e.parentSessionID {
			cancel()
			return nil, fmt.Errorf("task not found in parent session")
		}
		if agentTask.IsRunning() {
			cancel()
			return nil, fmt.Errorf("task is currently running")
		}
		state, ok := agentTask.LaunchState().(*continuationState)
		if !ok || state.sessionID == "" {
			cancel()
			return nil, fmt.Errorf("native continuation checkpoint unavailable")
		}
		// The frozen configuration, not the request name: an unnamed call may
		// have been substituted by the parent's posture at launch, and a
		// continuation must keep checking the definition it actually ran.
		originalName := state.req.Agent
		if state.cfg != nil && state.cfg.config != nil {
			originalName = state.cfg.config.Name
		}
		if _, enabled := e.resolveAgentConfig(originalName); !enabled {
			cancel()
			return nil, fmt.Errorf("the original agent definition is now disabled")
		}
		if err = e.validateRequest(req); err != nil {
			cancel()
			return nil, err
		}
		prior := agentTask.GetStatus()
		if req.ResumeAttempt > 0 && req.ResumeAttempt != prior.Attempt {
			cancel()
			return nil, fmt.Errorf("selected execution changed before continuation")
		}
		next := state.req
		next.Background = true
		next.Prompt, next.ResumeTaskID = req.Prompt, req.ResumeTaskID
		next.ParentToolCallID, next.Workflow = req.ParentToolCallID, req.Workflow
		next.OnIdentity = req.OnIdentity
		next.OnQuestion = req.OnQuestion
		next.OnTextDelta, next.OnTextEnd = req.OnTextDelta, req.OnTextEnd
		next.OnToolStart, next.OnToolResult = req.OnToolStart, req.OnToolResult
		next.ActivityMaxChars = req.ActivityMaxChars
		// Each explicit continuation gets a finite step budget. Retain the
		// original budget unless the caller explicitly supplies a new one;
		// configuration, model and permission policy remain frozen.
		cfg := *state.cfg
		if req.MaxSteps > 0 {
			cfg.maxSteps = req.MaxSteps
		}
		run = &preparedRun{originalPrompt: state.originalPrompt, priorFailure: string(prior.Status) + " " + prior.Error, req: next, cfg: &cfg, cwd: state.cwd, checkpoint: state, startedAt: time.Now(), hookID: "a" + generateShortID()}
		run.req.TaskID = agentTask.GetID()
		// Validate and claim native history before changing task state. A broken
		// checkpoint is a failed continuation request, not another execution
		// failure that wakes main and invites an unbounded recovery loop.
		if err = e.openNative(run); err != nil {
			cancel()
			return nil, err
		}
		if _, err = agentTask.BeginAttemptAfter(prior.Attempt, e.parentSessionID, ctx, cancel); err != nil {
			_ = run.native.Close()
			cancel()
			return nil, err
		}
	} else {
		run, err = e.prepareRun(ctx, req)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	identity := run.cfg.config.Name

	if agentTask == nil {
		agentTask = e.taskPool().CreateAgentTask(
			generateShortID(),
			identity,
			req.Description,
			ctx,
			cancel,
		)
		run.checkpoint = newContinuationState(run, e.parentSessionID, agentTask.GetID())
		agentTask.SetLaunchState(run.checkpoint)
	}
	agentTask.SetIdentity(identity, "")
	agentTask.SetPermissionMode(string(run.cfg.permMode))

	run.req.TaskID = agentTask.GetID()
	run.reportIdentity() // Report on the spawning call, before returning the launch.
	if e.tasks != nil {
		// Owned background runs cannot retain the spawning call's reporters.
		run.req.OnIdentity, run.req.OnQuestion = nil, nil
		run.req.OnTextDelta, run.req.OnTextEnd = nil, nil
		run.req.OnToolStart, run.req.OnToolResult = nil, nil
	}
	run.req.OnActivity = func(msg string) {
		agentTask.AppendProgress(msg)
		agentTask.SetLiveView(msg)
	}

	generation := agentTask.GetStatus().Attempt
	e.taskPool().Run(func() {
		defer cancel()

		result, err := e.runPrepared(ctx, run)
		if run.native != nil {
			agentTask.SetInspectionFile(run.native.TranscriptPath())
		}
		if run.checkpoint != nil {
			agentTask.SetIdentity(identity, run.checkpoint.sessionID)
		}
		if err != nil {
			// A cancelled run still returns its partial work — keep it in the
			// task output and record its persisted session for inspection.
			if result != nil {
				if result.Content != "" {
					agentTask.AppendOutput([]byte(result.Content + "\n"))
				}
				agentTask.SetIdentity(identity, result.AgentID)
				if run.native == nil {
					agentTask.SetOutputFile(result.TranscriptPath)
				}
				agentTask.UpdateProgress(result.StepCount, result.TokenUsage.Input+result.TokenUsage.Output)
			}
			agentTask.AppendOutput([]byte(fmt.Sprintf("Error: %v\n", err)))
			agentTask.CompleteAttempt(generation, err)
			return
		}

		if result.Content != "" {
			agentTask.AppendOutput([]byte(result.Content))
		}

		agentTask.SetIdentity(identity, result.AgentID)
		if run.native == nil {
			agentTask.SetOutputFile(result.TranscriptPath)
		}
		agentTask.UpdateProgress(result.StepCount, result.TokenUsage.Input+result.TokenUsage.Output)

		if result.Success {
			agentTask.CompleteAttempt(generation, nil)
		} else {
			agentTask.CompleteAttempt(generation, fmt.Errorf("%s", result.Error))
		}
	})

	return agentTask, nil
}

func (e *Executor) validateRequest(req tool.AgentExecRequest) error {
	if strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("agent prompt cannot be empty")
	}
	switch strings.TrimSpace(req.Mode) {
	case "", "default", "explore", "edit":
		return nil
	default:
		return fmt.Errorf("invalid agent mode %q: must be explore, edit, or default", req.Mode)
	}
}

func baseAgentConfig() *AgentConfig {
	return &AgentConfig{
		Description:    baseAgentDescription,
		Model:          "inherit",
		PermissionMode: PermissionDefault,
		MaxSteps:       defaultMaxSteps,
	}
}

// resolveAgentConfigFrom returns the base configuration for an omitted or
// unknown name. A matching enabled definition overrides the base configuration;
// a matching disabled definition is rejected instead of silently bypassed.
func resolveAgentConfigFrom(registry *Registry, name string) (*AgentConfig, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return baseAgentConfig(), true
	}
	if registry == nil {
		return displayOnlyAgentConfig(name), true
	}
	if config, exists, enabled := registry.LookupAgent(name); exists {
		return config, enabled
	}
	return displayOnlyAgentConfig(name), true
}

// displayOnlyAgentConfig preserves an unknown requested name for identity and
// UI display while otherwise using the base agent template. In particular it
// has no custom prompt, skills, tool rules, model override, or mode override.
func displayOnlyAgentConfig(name string) *AgentConfig {
	config := baseAgentConfig()
	config.Name = strings.TrimSpace(name)
	config.displayOnly = true
	return config
}

// readOnlyDefaultAgent is the bundled definition an unnamed Agent call runs as
// while the parent session is read-only: a read-only session investigates
// before it edits, so the default worker must be the investigator rather than
// the base implementation charter.
const readOnlyDefaultAgent = "explore"

// resolveAgentConfig returns the definition an Agent call runs under the parent's
// current posture.
func (e *Executor) resolveAgentConfig(name string) (*AgentConfig, bool) {
	return e.resolveAgentConfigIn(name, e.currentParentPermissionMode())
}

// resolveAgentConfigIn returns the definition an Agent call runs. An omitted
// name means "the default worker", and which definition that is depends on the
// parent's posture: a read-only session delegates investigation, so it gets the
// bundled explore definition; every other posture keeps the base charter. A
// missing or disabled explore falls back to the base worker instead of failing
// a call the parent never named. parent is passed in so one launch resolves the
// posture once instead of reading a mode that may change mid-preparation.
func (e *Executor) resolveAgentConfigIn(name string, parent PermissionMode) (*AgentConfig, bool) {
	if strings.TrimSpace(name) == "" {
		if config := e.readOnlyDefaultConfig(parent); config != nil {
			return config, true
		}
	}
	return resolveAgentConfigFrom(e.registry, name)
}

// readOnlyDefaultConfig returns the bundled explore definition while parent is
// read-only and that definition is enabled; nil otherwise.
func (e *Executor) readOnlyDefaultConfig(parent PermissionMode) *AgentConfig {
	if e.registry == nil || parent != PermissionExplore {
		return nil
	}
	config, exists, enabled := e.registry.LookupAgent(readOnlyDefaultAgent)
	if !exists || !enabled {
		return nil
	}
	return config
}

func (e *Executor) resolveRequestAgentConfig(req tool.AgentExecRequest, parent PermissionMode) (*AgentConfig, bool) {
	if req.ResolvedAgentConfig != nil {
		config, ok := req.ResolvedAgentConfig.(*AgentConfig)
		return config, ok && config != nil
	}
	return e.resolveAgentConfigIn(req.Agent, parent)
}

// parentPosture is the parent mode a launch inherits, read at most once. A
// request that fixes its own mode derives it from the request instead of the
// session, so the definition the call runs as and the mode it inherits cannot
// straddle a mode change the launch never observed. A named definition with an
// explicit mode never consults the parent at all; the value returned there is a
// placeholder the decision does not read.
func (e *Executor) parentPosture(req tool.AgentExecRequest) PermissionMode {
	named := strings.TrimSpace(req.Agent) != ""
	switch strings.TrimSpace(req.Mode) {
	case "explore":
		if named {
			return PermissionDefault
		}
		return PermissionExplore
	case "edit":
		if named {
			return PermissionDefault
		}
		return PermissionAcceptEdits
	default:
		return e.currentParentPermissionMode()
	}
}

func (e *Executor) prepareRunConfig(ctx context.Context, req tool.AgentExecRequest) (*runConfig, error) {
	// One read of the parent posture per launch: the definition the call runs
	// as and the mode it inherits must not see a mode change mid-preparation.
	parent := e.parentPosture(req)
	config, ok := e.resolveRequestAgentConfig(req, parent)
	if !ok {
		return nil, fmt.Errorf("unknown or disabled agent: %s", req.Agent)
	}

	// Freeze mutable registry/approval-time configuration, including lazily
	// loaded prompt content. Continuation must not observe later policy edits.
	config = &AgentConfig{
		Name: config.Name, Description: config.Description, Color: config.Color,
		WhenToUse: config.WhenToUse, Model: config.Model, PermissionMode: config.PermissionMode,
		AllowTools: slices.Clone(config.AllowTools), DenyTools: slices.Clone(config.DenyTools),
		Skills: slices.Clone(config.Skills), SystemPrompt: config.GetSystemPrompt(),
		MaxSteps: config.MaxSteps, Source: config.Source, McpServers: slices.Clone(config.McpServers),
		displayOnly: config.displayOnly,
	}
	permMode := requestPermissionModeIn(config, req, parent)
	displayName := displayAgentName(config.Name)

	maxSteps := config.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	if req.MaxSteps > 0 {
		maxSteps = req.MaxSteps
	}

	provider, modelID, effort, err := e.resolveModel(ctx, req.Model, e.configuredModel(config, req.Agent))
	if err != nil {
		return nil, err
	}

	return &runConfig{
		disabledTools: e.disabledToolsSnapshot(),
		config:        config,
		provider:      provider,
		modelID:       modelID,
		effort:        effort,
		maxSteps:      maxSteps,
		displayName:   displayName,
		brief:         e.buildBrief(config, permMode),
		permMode:      permMode,
	}, nil
}

func (e *Executor) fireSubagentStart(req tool.AgentExecRequest, agentHookID string) {
	if e.hooks == nil {
		return
	}
	e.hooks.ExecuteAsync(hook.SubagentStart, hook.HookInput{
		AgentName:   strings.TrimSpace(req.Agent),
		AgentID:     agentHookID,
		Description: req.Description,
	})
}

func (e *Executor) buildAgent(ctx context.Context, run *preparedRun, onToolExec func(string, map[string]any), onEvent func(core.Event)) (core.Agent, func(), error) {
	rc := run.cfg
	cleanup := func() {}

	if len(rc.config.McpServers) > 0 && e.mcpManager != nil {
		mcpCleanup, errs := mcp.ConnectServers(ctx, e.mcpManager, rc.config.McpServers)
		if mcpCleanup != nil {
			cleanup = mcpCleanup
		}
		for _, err := range errs {
			log.Logger().Warn("Agent MCP server connection failed", zap.Error(err))
		}
	}

	// Tools — adapt legacy tool registry + MCP tools
	var mcpGetter func() []core.ToolSchema
	if e.mcpManager != nil {
		mcpGetter = e.mcpManager.ToolSchemas
	}
	disabled := make(map[string]bool, len(rc.disabledTools))
	for name, blocked := range rc.disabledTools {
		if blocked {
			disabled[name] = true
		}
	}
	// Original prohibitions remain in force, and newer global disables may
	// tighten a continuation. Re-enabling a tool never widens its old policy.
	for name, blocked := range e.disabledToolsSnapshot() {
		if blocked {
			disabled[name] = true
		}
	}
	toolSet := newAgentToolSet(rc.config.AllowTools.Names(), rc.config.DenyTools.BareNames(), disabled, mcpGetter)
	schemas := filterSchemasForPermission(toolSet.Tools(), rc.permMode, rc.config.AllowTools, e.interactions != nil || e.permission != nil)
	brief := rc.brief
	brief.ToolConstraints = effectiveToolConstraints(rc.config, rc.permMode, schemas)
	brief.ToolGuidance = fileReadToolGuidance(schemas, rc.permMode)
	// Subagent system prompt deliberately omits skills and memory — those
	// ride on the first user message as <system-reminder> blocks built by
	// loadConversation, keeping subagents on the same harness channel
	// pattern as the main agent.
	sys := system.Build(core.ScopeSubagent,
		system.WithSubagentIdentity(brief),
		system.WithEnvironment(system.Environment{Cwd: run.currentCwd(), Shell: proc.DefaultShellName()}),
	)
	run.system = sys
	var ag core.Agent
	var callDirs sync.Map
	adaptOpts := []tool.AdaptOption{
		tool.WithExecutionCWD(func(ctx context.Context) (string, error) {
			if cwd, ok := callDirs.LoadAndDelete(core.ToolCallIDFromContext(ctx)); ok {
				return cwd.(string), nil
			}
			return "", fmt.Errorf("worker execution directory was not authorized")
		}),
		tool.WithToolContext(func(ctx context.Context) context.Context {
			ctx = task.WithManager(task.WithSessionID(ctx, e.parentSessionID), e.taskPool())
			if e.toolContext != nil {
				ctx = e.toolContext(ctx, rc.config.SourceFile)
			}
			return ctx
		}),
		tool.WithSideEffectHandler(run.storeSideEffect),
		tool.WithMessagesGetterProvider(func() []core.Message {
			if ag == nil {
				return nil
			}
			return ag.Messages()
		}),
	}
	if e.interactions != nil {
		adaptOpts = append(adaptOpts, tool.WithAskUser(func(ctx context.Context, request *tool.QuestionRequest) (*tool.QuestionResponse, error) {
			return e.interactions.Ask(ctx, run.interactionSource(ctx), request)
		}))
	} else if run.req.OnQuestion != nil {
		adaptOpts = append(adaptOpts, tool.WithAskUser(tool.AskUserFunc(run.req.OnQuestion)))
	}
	tools := tool.AdaptToolRegistry(schemas, run.currentCwd, adaptOpts...)

	// Add MCP tool executors
	if e.mcpManager != nil {
		for _, t := range mcp.AsCoreTools(schemas, e.mcpManager) {
			tools.Add(t, "mcp:"+t.Schema().Name)
		}
	}

	var prompt perm.PermissionFunc
	if e.interactions != nil {
		prompt = e.interactionApproval(run)
	}
	permFn := subagentPermissionFuncWithPrompt(rc.permMode, rc.config.AllowTools, rc.config.DenyTools, prompt)
	if e.permission != nil {
		// The owner authorizes every call, including safe tools and child allow
		// rules. Child constraints may narrow but never widen parent policy.
		constraints := subagentPermissionFuncWithPrompt(rc.permMode, rc.config.AllowTools, rc.config.DenyTools, func(context.Context, string, map[string]any) (bool, string) { return true, "owner review" })
		permFn = func(ctx context.Context, name string, input map[string]any) (bool, string) {
			if ok, reason := constraints(ctx, name, input); !ok {
				return false, reason
			}
			cwd, _ := ctx.Value(executionDirectoryKey{}).(string)
			parent := e.permission(run.req.Background, cwd)
			if parent == nil {
				return false, "owner permission policy unavailable"
			}
			return parent(ctx, name, input)
		}
	}
	permissionGate := tool.Permission(permFn)
	if e.permission != nil {
		permissionGate = tool.HookedPermission(e.hooks, workerPermission{check: permFn})
	}
	gate := core.Gates(activity(onToolExec), func(ctx context.Context, call sdkagent.PreToolContext) (sdkagent.Decision, error) {
		if e.toolContext != nil {
			ctx = e.toolContext(ctx, rc.config.SourceFile)
		}
		cwd := run.currentCwd()
		ctx = context.WithValue(ctx, executionDirectoryKey{}, cwd)
		decision, err := permissionGate(core.WithToolCallID(ctx, call.Call.ID), call)
		if err == nil && !decision.Block {
			callDirs.Store(call.Call.ID, cwd)
		}
		return decision, err
	})

	llmClient := newRunClient(rc)
	llmClient.SetSessionID(e.parentSessionID)
	llmClient.SetWorkingDir(run.currentCwd)
	ag = core.NewAgent(core.Config{
		Client:         llmClient.TurnClient,
		CallOptions:    llmClient.CallOptions,
		ShapeInference: llmClient.ShapeInference,
		ContextBudget:  llmClient.ContextBudget,
		System:         sys,
		Tools:          tools,
		Gate:           gate,
		CompactFunc:    subagentCompactFunc(llmClient, run.originalPrompt),
		MaxSteps:       rc.maxSteps,
		OutboxBuf:      -1,
		OnEvent:        onEvent,
	})

	return ag, cleanup, nil
}

// newRunClient is the client every run talks through: the run's provider and
// model, plus the reasoning effort the role behind them asked for. The role
// that supplied the model also supplied the depth, so a subagent defined as
// "@explore" thinks as hard as the user asked the explore role to. The host
// validated the label against this model's ladder, so an empty effort means
// "send none" rather than "unknown", and the model's own default applies.
func newRunClient(rc *runConfig) *llm.Client {
	client := llm.NewClient(rc.provider, rc.modelID, 0)
	client.SetThinkingEffort(rc.effort)
	return client
}

func fileReadToolGuidance(schemas []core.ToolSchema, mode PermissionMode) string {
	read, shell := false, ""
	for _, schema := range schemas {
		switch {
		case schema.Name == tool.ToolRead:
			read = true
		case tool.IsShellTool(schema.Name):
			shell = schema.Name
		}
	}
	if read {
		guidance := "Use Read for file contents, with offset/limit for excerpts."
		if shell != "" {
			guidance += " Use the shell for search, file discovery, git, and commands; avoid cat/head/tail/sed just to display a file."
		}
		return guidance
	}
	var guidance string
	switch shell {
	case tool.ToolBash:
		guidance = "Read is unavailable. Read file contents with Bash using cat path, head -n 100 path, or rg -n '^' path, subject to the tool constraints."
	case tool.ToolPowerShell:
		guidance = "Read is unavailable. Read file contents with PowerShell using Get-Content path or Get-Content path -TotalCount 100, subject to the tool constraints."
	default:
		return "No built-in file-reading tool is available. Report the task as blocked if the source cannot be inspected with the tools offered; do not claim an unverified review is complete."
	}
	if NormalizePermissionMode(string(mode)) == PermissionExplore {
		guidance += " Python, Node, and other interpreters are not classified as read-only, even when a script only reads files."
	}
	return guidance
}

// subagentCompactFunc summarizes the conversation on the run's own model so
// long subagent runs survive context-window pressure instead of dying on
// prompt-too-long. The task is re-attached verbatim after the summary: it is
// the one message a one-shot worker cannot afford to have paraphrased.
func subagentCompactFunc(client *llm.Client, task string) func(context.Context, []core.Message) (string, error) {
	return func(ctx context.Context, msgs []core.Message) (string, error) {
		text := core.BuildCompactionText(msgs)
		resp, err := client.Complete(ctx, system.CompactPrompt(), []core.Message{core.UserMessage(text, nil)}, nil, core.CompactMaxTokens)
		if err != nil {
			return "", err
		}
		summary := strings.TrimSpace(resp.Content.Text())
		if summary == "" {
			return "", fmt.Errorf("compaction produced empty summary")
		}
		return withOriginalTask(summary, task), nil
	}
}

// withOriginalTask appends the run's task, verbatim, to a compaction summary.
func withOriginalTask(summary, task string) string {
	if task == "" {
		return summary
	}
	return summary + "\n\nYour original task, verbatim:\n\n" + task
}

func (e *Executor) loadConversation(ag core.Agent, ctx context.Context, rc *runConfig, req tool.AgentExecRequest) error {
	ag.Append(core.UserMessage(e.taskPrompt(rc, req), nil))
	return nil
}

// taskPrompt is the run's first user message: the prompt with its
// harness-managed <system-reminder> blocks, matching the main agent's pattern.
func (e *Executor) taskPrompt(rc *runConfig, req tool.AgentExecRequest) string {
	reminders := e.collectSubagentReminders(e.skillsDirectoryFor(rc.config), rc.permMode, rc.config.AllowTools)
	return reminder.AttachToContent(req.Prompt, reminders)
}

// collectSubagentReminders returns the <system-reminder> blocks for the
// subagent's first user message. Subagents get the skills directory (so they
// can invoke capabilities) and — when they can modify the workspace — the
// project's instruction memory, so their edits follow project conventions.
// User memory stays with the main loop: a subagent is a one-shot worker
// bounded by its own charter.
func (e *Executor) collectSubagentReminders(skills string, mode PermissionMode, allow ToolList) []string {
	var reminders []string
	reminders = append(reminders, wrapNonEmpty(skills)...)
	if canEditWorkspace(mode, allow, e.interactions != nil) {
		reminders = append(reminders, wrapNonEmpty(reminder.WrapMemory("project", e.projectInstructions))...)
	}
	return reminders
}

func wrapNonEmpty(body string) []string {
	if w := reminder.Wrap(body); w != "" {
		return []string{w}
	}
	return nil
}

// canEditWorkspace reports whether the subagent can change files in the
// workspace — the signal for whether project conventions are worth handing it.
// True when the permission mode auto-accepts mutations (edit/bypass), or when
// an explicit allow_tools rule grants an edit-class tool (Edit/Write/…) even
// under a mode, such as default, that would otherwise deny them. A live UI can
// approve default-mode edits once, so those workers need the same conventions.
func canEditWorkspace(mode PermissionMode, allow ToolList, canPrompt bool) bool {
	switch operationMode(mode) {
	case setting.ModeAutoAccept, setting.ModeBypassPermissions:
		return true
	}
	return slices.ContainsFunc(allow.Names(), perm.IsEditTool) ||
		(canPrompt && allow == nil && setting.ModeDefault(tool.ToolWrite, operationMode(mode)).Behavior == perm.Prompt)
}

func interpretStopReason(result *core.Result, maxSteps int) (success bool, errMsg string) {
	success = result.StopReason == core.StopEndTurn
	switch result.StopReason {
	case core.StopMaxSteps:
		errMsg = fmt.Sprintf("reached maximum steps (%d)", maxSteps)
	case core.StopTruncated:
		errMsg = "the answer was cut off, and carrying on did not finish it"
	case core.StopRefused:
		errMsg = "the model declined to answer"
	case core.StopCanceled:
		errMsg = "agent cancelled"
	case core.StopHook, core.StopError:
		errMsg = result.StopDetail
	}
	return success, errMsg
}

// fireSubagentStop fires on every run exit — success, cancellation, or error
// — with the same AgentID that SubagentStart carried, so hook consumers can
// pair the two events. The session id travels via the transcript path.
func (e *Executor) fireSubagentStop(req tool.AgentExecRequest, agentHookID, agentTranscriptPath, resultContent string) {
	if e.hooks == nil {
		return
	}

	e.hooks.ExecuteAsync(hook.SubagentStop, hook.HookInput{
		AgentName:            strings.TrimSpace(req.Agent),
		AgentID:              agentHookID,
		AgentTranscriptPath:  agentTranscriptPath,
		LastAssistantMessage: resultContent,
		StopHookActive:       e.hooks.StopHookActive(),
	})
}

// resolveModel picks the provider, model id and reasoning effort for a run, by
// priority:
// 1. Explicit request override (req.Model)
// 2. Agent configuration (config.Model)
// 3. Parent conversation model ("inherit" or empty)
//
// An explicit "vendor/model" override routes to that vendor through the
// resolver only when a linked provider can be resolved. Otherwise it falls
// back to the parent conversation. An "@role" reference asks the host's role
// resolver, so the definition names the kind of work and the user names the
// model — and the depth. An unassigned role falls back to the parent. Every
// other form — an alias, a bare model id, or "inherit" — stays on the parent's
// provider with no role effort, preserving prior behavior.
func (e *Executor) resolveModel(ctx context.Context, requestModel, configModel string) (llm.Provider, string, string, error) {
	ref := strings.TrimSpace(requestModel)
	if ref == "" {
		ref = strings.TrimSpace(configModel)
	}
	if ref == "" || ref == "inherit" {
		return e.provider, e.parentModelID, "", nil
	}
	// A role reference ("@smol") names a routing slot, not a model, so it never
	// reaches a provider as a model name. An unassigned role, or one whose
	// binding does not resolve, inherits the parent model.
	if role, ok := llm.ParseRoleAlias(ref); ok {
		if assignment := e.resolveRole(role); assignment.Assigned {
			return assignment.Provider, assignment.ModelID, assignment.Effort, nil
		}
		return e.provider, e.parentModelID, "", nil
	}
	// Something that only looks like an alias ("@", "@Slow Role") is a typo:
	// sending it as a model id would fail the run, so the parent model answers.
	if strings.HasPrefix(ref, "@") {
		return e.provider, e.parentModelID, "", nil
	}
	if vendor, modelID, ok := llm.ParseVendorModel(ref); ok {
		if e.resolver == nil {
			return e.provider, e.parentModelID, "", nil
		}
		p, err := e.resolver.Resolve(ctx, vendor)
		if err != nil || p == nil {
			return e.provider, e.parentModelID, "", nil
		}
		return p, modelID, "", nil
	}
	// A bare id or alias stays on the parent provider; a model it lacks fails
	// the first request and retries on the parent model.
	return e.provider, resolveModelAlias(ref), "", nil
}

// resolveRole asks the host's role resolver for the provider, model and effort
// assigned to role. It reports an unassigned result whenever the parent model
// should answer instead: no resolver, an unassigned role, or a binding that
// resolved to nothing.
func (e *Executor) resolveRole(role string) RoleAssignment {
	// "@default" is the session's own model. The model picker owns that choice,
	// so an agent definition cannot route interactive work elsewhere by naming
	// the default role.
	if role == string(llm.RoleDefault) || e.roleResolver == nil {
		return RoleAssignment{}
	}
	assignment := e.roleResolver(role)
	if !assignment.Assigned || assignment.Provider == nil || assignment.ModelID == "" {
		return RoleAssignment{}
	}
	return assignment
}

// effectiveModelName is the model shown before a run is approved: a role
// reference is resolved to the model that will actually answer, so the user
// approves a model rather than a slot name. Every other form keeps its
// configured spelling, including ""/"inherit", which the caller renders as the
// parent model.
func (e *Executor) effectiveModelName(config *AgentConfig, name string) string {
	ref := e.configuredModel(config, name)
	role, ok := llm.ParseRoleAlias(ref)
	if !ok {
		return ref
	}
	if assignment := e.resolveRole(role); assignment.Assigned {
		return assignment.ModelID
	}
	return e.parentModelID
}

func shouldRetryWithParentModel(err error, modelID, parentModelID string) bool {
	if err == nil || parentModelID == "" || modelID == "" || modelID == parentModelID {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "model_not_found") || strings.Contains(msg, "model not found") || strings.Contains(msg, "model_not_exist")
}

// operationMode maps a subagent PermissionMode to the setting.OperationMode that
// drives the shared mode-default table (setting.ModeDefault). dontAsk never
// prompts; auto aliases to acceptEdits until the safety classifier ships.
func operationMode(mode PermissionMode) setting.OperationMode {
	switch NormalizePermissionMode(string(mode)) {
	case PermissionExplore:
		return setting.ModeReadOnly
	case PermissionAcceptEdits, PermissionAuto:
		return setting.ModeAutoAccept
	case PermissionBypass:
		return setting.ModeBypassPermissions
	case PermissionDontAsk:
		return setting.ModeDontAsk
	default:
		return setting.ModeNormal
	}
}

// InheritedPermissionMode is the mode an unnamed subagent inherits from a session in
// mode. Autopilot's review agent cannot answer for a subagent, so it inherits
// only the edit posture.
func InheritedPermissionMode(mode setting.OperationMode) PermissionMode {
	switch mode {
	case setting.ModeAutoAccept, setting.ModeAutoPilot:
		return PermissionAcceptEdits
	case setting.ModeBypassPermissions:
		return PermissionBypass
	case setting.ModeDontAsk:
		return PermissionDontAsk
	case setting.ModeReadOnly:
		return PermissionExplore
	default:
		return PermissionDefault
	}
}

// skillsDirectoryFor returns the skills section body for the agent, gated on
// its tool allow list — a subagent without the Skill tool gets no skills
// section.
func (e *Executor) skillsDirectoryFor(config *AgentConfig) string {
	if config == nil {
		return ""
	}
	if config.AllowTools == nil || config.AllowTools.HasName("Skill") {
		return e.skillsPrompt
	}
	return ""
}

// subagentPermissionFunc returns the subagent permission gate. The pipeline
// matches docs/concepts/permission-model.md: deny_tools, circuit breaker,
// confirmation tiers, allow_tools, mode default. Ordinary Prompt decisions use
// the configured UI responder, or fail closed when none is available. bypassPermissions skips the
// confirmation tiers — only the root/home-removal circuit breaker holds in
// every mode.
//
// One communication carve-out sits between deny and allow_tools: for a
// mode-gated worker (no explicit allow_tools list) SendMessage is permitted in
// every mode, so even a read-only or default worker can report to main or
// answer a peer — it only moves text between agents and never touches the
// workspace. deny_tools still blocks it, and an explicit allow_tools list is
// honored as written (leave SendMessage out to silence a worker). Spawning is
// not gated here: the Agent tool is parent-only, so workers never see it — the
// agent model is flat.
func subagentPermissionFunc(mode PermissionMode, allowRules, denyRules ToolList) perm.PermissionFunc {
	return subagentPermissionFuncWithPrompt(mode, allowRules, denyRules, nil)
}

func subagentPermissionFuncWithPrompt(mode PermissionMode, allowRules, denyRules ToolList, prompt perm.PermissionFunc) perm.PermissionFunc {
	mode = NormalizePermissionMode(string(mode))
	opMode := operationMode(mode)
	display := displayPermissionMode(mode)

	return func(ctx context.Context, name string, input map[string]any) (bool, string) {
		if denyRules.Matches(name, input) {
			return false, fmt.Sprintf("tool %s is blocked by deny_tools", name)
		}
		// Circuit breaker: a recursive removal of the filesystem root or the
		// home directory is a hard deny in every mode, bypass included.
		if reason := setting.CircuitBreakerReason(name, input); reason != "" {
			return false, fmt.Sprintf("tool %s blocked: %s", name, reason)
		}
		// Bypass mode skips both confirmation tiers: after deny_tools and the
		// breaker above, permit every executable worker tool. Keep the
		// parent-only restriction below so a worker still cannot spawn agents
		// or manipulate main-only state.
		if opMode == setting.ModeBypassPermissions && !tool.IsParentOnlyTool(name) {
			return true, ""
		}
		// Outside bypass, both confirmation tiers are hard denies, so a greedy
		// allow_tools pattern cannot smuggle destruction through.
		if reason, _ := setting.ConfirmationTier(name, input); reason != "" {
			return false, fmt.Sprintf("tool %s blocked: %s", name, reason)
		}
		// Parent-only tools never reach a worker, even via allow_tools —
		// checked here so the safe-tool auto-permit below cannot resurrect a
		// hallucinated tracker or cron call.
		if tool.IsParentOnlyTool(name) {
			return false, fmt.Sprintf("tool %s is reserved for the main conversation", name)
		}
		// Explore is an authoritative read-only boundary. Explicit allow rules
		// may narrow its tools but cannot elevate it to workspace mutation.
		if mode == PermissionExplore {
			switch {
			case perm.IsSafeTool(name), name == tool.ToolSendMessage, name == tool.ToolSkill:
				return true, ""
			case tool.IsShellTool(name):
				command, _ := input["command"].(string)
				if setting.IsReadOnlyShellCommand(name, command) {
					return true, ""
				}
				return false, fmt.Sprintf("tool %s is denied in %s mode: only commands classified as read-only are allowed; general-purpose interpreters are not classified as read-only", name, display)
			}
			return false, fmt.Sprintf("tool %s is denied in %s mode", name, display)
		}
		// Communication carve-out (see doc comment): a mode-gated worker may
		// always reach main or a peer via SendMessage. A worker with an explicit
		// allow_tools list is governed by that list instead.
		if name == tool.ToolSendMessage && allowRules == nil {
			return true, ""
		}
		if allowRules.Allows(name, input) {
			return true, ""
		}
		// A provably read-only Bash command (search, listing, git inspection)
		// is as safe as the dedicated read-only tools, which also run without
		// being listed in allow_tools. Checked after deny_tools so agents can
		// still block Bash outright.
		if tool.IsShellTool(name) {
			if cmd, ok := input["command"].(string); ok && setting.IsReadOnlyShellCommand(name, cmd) {
				return true, ""
			}
		}
		// allow_tools mentions this tool but no pattern matched — the agent
		// declared a constrained whitelist, so deny rather than fall through.
		if allowRules.HasName(name) {
			return false, fmt.Sprintf("tool %s call is outside the allow_tools constraint", name)
		}
		// Loading a skill's instructions has no side effects of its own — the
		// actions those instructions trigger are still gated per tool call —
		// so Skill works in every mode, matching the skills listing already
		// embedded in the worker's prompt.
		if name == tool.ToolSkill {
			return true, ""
		}
		switch setting.ModeDefault(name, opMode).Behavior {
		case perm.Permit:
			return true, ""
		case perm.Reject:
			return false, fmt.Sprintf("tool %s is denied in %s mode", name, display)
		default: // perm.Prompt only; hard rejects above never reach the UI.
			if prompt != nil {
				return prompt(ctx, name, input)
			}
			return false, fmt.Sprintf("%s permission denied: command or tool would require approval in %s mode; no live session UI is available. Pre-authorize it in allow_tools or have the parent execute it", name, display)
		}
	}
}

// filterSchemasForPermission narrows the LLM-visible tool set to what the
// agent can actually use under its mode + allow_tools. UX hint only — the
// permission gate is still authoritative. A non-nil allowTools acts as a
// whitelist regardless of mode.
func filterSchemasForPermission(schemas []core.ToolSchema, mode PermissionMode, allowTools ToolList, canPrompt bool) []core.ToolSchema {
	mode = NormalizePermissionMode(string(mode))
	whitelist := allowTools != nil

	filtered := make([]core.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		if whitelist {
			if allowTools.HasName(schema.Name) && (mode != PermissionExplore || modeAllowsSchema(mode, schema.Name)) {
				filtered = append(filtered, schema)
			}
			continue
		}
		if modeAllowsSchema(mode, schema.Name) || (canPrompt && !tool.IsParentOnlyTool(schema.Name) && setting.ModeDefault(schema.Name, operationMode(mode)).Behavior == perm.Prompt) {
			filtered = append(filtered, schema)
		}
	}
	return filtered
}

func modeAllowsSchema(mode PermissionMode, name string) bool {
	// SendMessage is a communication tool, kept visible in every mode so a
	// mode-gated worker can report to main (the gate permits it — see
	// subagentPermissionFunc). A worker with an explicit allow_tools list takes
	// the whitelist branch in filterSchemasForPermission instead of this one.
	if perm.IsSafeTool(name) || name == tool.ToolSendMessage {
		return true
	}
	switch mode {
	case PermissionBypass, PermissionAuto:
		return true
	case PermissionAcceptEdits:
		if perm.IsEditTool(name) {
			return true
		}
	}
	// Read-only Bash invocations are permitted in every mode, so the Bash
	// schema stays visible even to explore/read-only agents — it is their
	// search tool. Skill stays visible for the same reason the gate permits
	// it: the worker's prompt already lists available skills.
	return tool.IsShellTool(name) || name == tool.ToolSkill
}

// newAgentToolSet creates a tool.Set for subagents with global and per-agent
// exclusions eagerly initialized.
func newAgentToolSet(allow, disallow []string, disabled map[string]bool, mcpGetter func() []core.ToolSchema) *tool.Set {
	s := &tool.Set{
		Allow: slices.Clone(allow), Disallow: slices.Clone(disallow), Disabled: maps.Clone(disabled),
		MCP: mcpGetter, IsAgent: true,
	}
	s.InitDisallowSet()
	return s
}

// generateShortID creates a short random hex ID for background tasks.
func generateShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
