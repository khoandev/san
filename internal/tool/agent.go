package tool

import (
	"context"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool/toolresult"
)

const (
	// IconAgent is the display icon for agent tool results.
	IconAgent = "a"
)

// messagesGetterKey is the context key for parent messages getter (used by fork).
type messagesGetterKey struct{}

// agentIDKey carries the broker address of the agent whose tools are running,
// so SendMessage can stamp the sender. Empty for the main conversation.
type agentIDKey struct{}

// WithAgentID marks the context with the running subagent's broker address.
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDKey{}, id)
}

// AgentIDFromContext returns the running subagent's broker address, or "" when
// the caller is the main conversation.
func AgentIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(agentIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithMessagesGetter returns a context carrying a messages getter for fork support.
func WithMessagesGetter(ctx context.Context, getter MessagesGetter) context.Context {
	return context.WithValue(ctx, messagesGetterKey{}, getter)
}

// GetMessagesGetter returns the messages getter from context, if any.
func GetMessagesGetter(ctx context.Context) MessagesGetter {
	if g, ok := ctx.Value(messagesGetterKey{}).(MessagesGetter); ok {
		return g
	}
	return nil
}

// AgentRouteRequest asks the host what an Agent call will run as before it
// runs. Name and Tier are the call's own signals: a name the host recognizes as
// a definition, or a depth, has already decided. A name that is not a definition
// only labels the task, so it is classified like an unnamed call and keeps the
// label as display identity. Resume marks a continuation, which keeps the
// policy it was approved under instead of being routed again.
type AgentRouteRequest struct {
	Name   string
	Tier   string
	Prompt string
	Resume bool
}

// AgentRoute is the routing decision pinned onto a call so approval and
// execution agree: the rung the brief is for, the definition the call runs as,
// and the exact configuration execution must use. A decision made at approval
// is pinned, so a registry reload, a settings change or a mode change while the
// approval is pending cannot move what the user approved.
type AgentRoute struct {
	// Tier is the depth this call asked for: fast, standard or deep. Empty
	// keeps the definition's own rung.
	Tier string
	// Agent is the definition the call runs as. Empty keeps the default
	// worker, which is what an unnamed call runs anyway.
	Agent string
	// Label is the caller's own name for the task when it named no
	// definition. It never changes what runs: Agent does, and Label only
	// names the work being delegated.
	Label string
	// Info is the approval-time projection: the name, model and posture the
	// user is shown. It is never consulted during execution.
	Info AgentConfigInfo
	// Raw is the exact configuration to pin for execution.
	Raw any
}

// AgentExecutor is the interface for executing agents.
// This allows the Agent tool to be decoupled from the agent package.
type AgentExecutor interface {
	// Run may return an unfinished result alongside an error; callers must
	// preserve its partial content and transcript identity.
	Run(ctx context.Context, req AgentExecRequest) (*AgentExecResult, error)
	RunBackground(req AgentExecRequest) (AgentTaskInfo, error)
	AgentConfig(name string) (AgentConfigInfo, bool)
	ResolveAgentSelection(name string) (AgentConfigInfo, any, bool)
	ParentModelID() string
}

// AgentRouter is the optional capability of answering "what will this call run
// as" for a call that named no definition, and for a call whose name is only a
// label. It is separate from AgentExecutor so a host without a classifier keeps
// the old behavior: no router, no routing, and every call runs exactly what it
// asked for.
//
// A host decides whether the name is a definition: it must answer with no
// substitution for one it recognizes, and may route a call whose name it does
// not, keeping that name as AgentRoute.Label. Routing narrows the posture,
// never widens it.
type AgentRouter interface {
	// RouteAgent returns the pinned decision for req. ok reports that the host
	// made one at all; false means the call keeps the definition it named (or
	// the default worker) and routes nowhere.
	RouteAgent(ctx context.Context, req AgentRouteRequest) (AgentRoute, bool)
}

// ActivityFunc is called when the agent reports activity.
type ActivityFunc func(msg string)

// MessagesGetter returns the current parent conversation messages.
// Used by fork to inherit conversation context.
type MessagesGetter func() []core.Message

// AgentExecRequest contains parameters for agent execution.
type AgentExecRequest struct {
	OnIdentity          func(toolresult.AgentIdentity) // display-only; reported before work begins
	ResumeTaskID        string                         // Continue the same task and native child session with Prompt only.
	ResumeAttempt       int                            // Internal approval-time execution fence; not model-facing.
	ParentToolCallID    string                         // internal spawning call correlation; never model-facing
	Workflow            *WorkflowOrigin                // internal workflow attribution; never model-facing
	Agent               string
	ResolvedAgentConfig any // exact approval-time configuration; never model-facing
	// Route is the routing decision this call was approved with. Execution
	// uses it verbatim; only a call approved without one is routed again.
	Route AgentRoute
	// Tier is the depth the call asked for, mirrored from Route.Tier so a run
	// can read it without knowing how approval pinned it.
	Tier        string
	Prompt      string
	Description string
	Background  bool
	Model       string
	MaxSteps    int
	Mode        string
	// TaskID is the stable task address for foreground and background runs.
	// The executor registers its inbox only while an execution is active.
	TaskID     string
	OnActivity ActivityFunc
	// ActivityMaxChars widens tool-call descriptions for views that can wrap
	// them. Zero keeps the short default used by ordinary agent summaries.
	ActivityMaxChars int
	OnTextDelta      func(string)                             // streamed assistant text for a live view
	OnTextEnd        func(error)                              // finishes or discards this inference's text
	OnToolStart      func(id, call string)                    // one live row per tool call
	OnToolResult     func(id, call, output string, err error) // completes that row by call ID
	OnQuestion       AskQuestionFunc
}

// AgentExecResult contains the result of agent execution.
type AgentExecResult struct {
	SourceMessageSeq  uint64
	Identity          toolresult.AgentIdentity
	AgentID           string
	AgentName         string
	OutputFile        string
	Model             string
	Success           bool
	Content           string
	StepCount         int
	ToolUses          int
	TotalInputTokens  int
	TotalOutputTokens int
	Duration          time.Duration
	Activity          []string
	Error             string
}

// AgentTaskInfo contains info about a background agent task.
type AgentTaskInfo struct {
	Identity  toolresult.AgentIdentity
	TaskID    string
	AgentName string
	// Model is the model the frozen launch configuration resolves to. A
	// background task has produced no result yet, so this is the only place
	// its route can be reported from.
	Model      string
	OutputFile string
}

// AgentConfigInfo contains agent configuration for display. It is the single
// projection of an agent definition, shared by the Agent tool (GetAgentConfig)
// and the TUI agent selector.
type AgentConfigInfo struct {
	Name           string
	Description    string
	Color          string
	Model          string
	PermissionMode string
	Tools          []string // nil = all tools
	SourceFile     string
	// Source indicates where the agent definition came from:
	// "user", "project", or a plugin scope. Empty defaults to project.
	Source string
}
