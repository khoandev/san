// Hook-forwarding model methods and related helpers.
// These methods use m.services.Hook to fire lifecycle events,
// replacing the former env hook methods that used singletons directly.
package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/plugin"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/setting"
)

func (m *model) fireStopFailureHook(lastAssistantContent string, err error) {
	m.services.Hook.ExecuteAsync(hook.StopFailure, hook.HookInput{
		LastAssistantMessage: lastAssistantContent,
		Error:                err.Error(),
		StopHookActive:       m.services.Hook.StopHookActive(),
	})
}

func (m *model) executeStartupHooks(ctx context.Context) hook.HookOutcome {
	m.services.Hook.ExecuteAsync(hook.Setup, hook.HookInput{
		Trigger: "init",
	})
	// A session ID exists even for a new session; restored messages are what
	// mark a resume.
	source := "startup"
	if len(m.conv.Messages) > 0 {
		source = "resume"
	}
	return m.services.Hook.Execute(ctx, hook.SessionStart, hook.HookInput{
		Source: source,
		Model:  m.env.GetModelID(),
	})
}

// pendingStopHook identifies the pending decision and its session/runtime.
type pendingStopHook struct {
	session   *session.LegacySession
	recording *session.Session
	outbox    <-chan core.Event
}

type stopHookResultMsg struct {
	pending *pendingStopHook
	Blocked bool
	Reason  string
	Err     error
	Result  core.Result
}

func (m *model) fireIdleHooksCmd(result core.Result) tea.Cmd {
	pending := &pendingStopHook{session: m.services.Session, recording: m.services.Recording, outbox: m.AgentOutbox()}
	m.pendingStopHook = pending
	check := agent.NewStopCheck(m.services.Hook, core.LastAssistantChatContent(m.conv.Messages))
	return func() tea.Msg {
		decision := check.Run(context.Background())
		return stopHookResultMsg{pending: pending, Blocked: decision.Blocked, Reason: decision.Reason, Err: decision.Err, Result: result}
	}
}

// defaultUIHookBudget bounds a hook that the bubbletea goroutine waits on.
//
// The engine's own default is 600 seconds (hook.defaultTimeout), which suits a
// detached hook. Applied to a gate sitting between a keypress and the submit it
// means a hook that blocks — a network call, an unreachable host, a stray
// `read` — takes the entire UI with it for ten minutes: nothing repaints, and
// Esc and Ctrl+C queue up behind the very loop that would dispatch them.
//
// The cap applies even to a hook that configured a longer timeout of its own.
// That configuration is reasonable for a hook running off the UI goroutine; on
// this path it amounts to asking for a frozen terminal. A hook cut short fails
// open — the engine logs it, and the user's prompt is not blocked because their
// hook hung. A deployment with a legitimately slow gate can raise the budget
// via the hookUITimeout setting (see uiHookBudget).
const defaultUIHookBudget = 5 * time.Second

// uiHookBudget resolves the deadline for a hook the bubbletea goroutine waits
// on. It honors the hookUITimeout setting so a legitimately slow prompt gate can
// raise it, falling back to defaultUIHookBudget for an empty, unparseable, or
// non-positive value.
func (m *model) uiHookBudget() time.Duration {
	if raw := m.services.Setting.HookUITimeout(); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return defaultUIHookBudget
}

func (m *model) checkPromptHook(ctx context.Context, prompt string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, m.uiHookBudget())
	defer cancel()

	return agent.CheckPrompt(ctx, prompt, m.services.Hook, m.services.Reminder)
}

func (m *model) switchProvider(p llm.Provider) {
	m.env.LLMProvider = p
	m.services.LLM.SetProvider(p)
	m.wireHookCompleter()
}

// wireHookCompleter points prompt and agent hooks at the fast role. A hook
// completion is a one-shot utility call, repeated for every event and invisible
// to the conversation — exactly what a cheap role exists for. An unassigned
// role, an unconnected vendor or settings that are not wired all leave the
// session provider and model in charge.
func (m *model) wireHookCompleter() {
	target := m.resolveRole(llm.RoleFast)
	provider, modelID := target.Provider, target.ModelID
	if provider == nil {
		provider = m.env.LLMProvider
	}
	if modelID == "" {
		modelID = m.env.GetModelID()
	}
	m.services.Hook.SetLLMCompleter(buildHookCompleter(provider, modelID, m.resolveRoleEffort(target)), modelID)
}

// resolveRoleModelOver answers role's provider, model and effort from a
// settings snapshot and a provider store, for the headless runtimes (hook init,
// ACP sessions) that hold a *setting.Data rather than the TUI's live services.
// An unassigned role or an unresolvable binding falls back to the caller's
// session model with no role effort, so hook completion never fails because a
// role is stale.
func resolveRoleModelOver(roles llm.RoleBindings, store *llm.Store, role llm.ModelRole, fallback llm.Provider, fallbackModel string) (llm.Provider, string, string) {
	target := llm.NewStoreRoleResolver(store).Resolve(context.Background(), roles, string(role), fallback, fallbackModel)
	return target.Provider, target.ModelID, llm.ResolveTargetEffort(target.Provider, store, nil, target)
}

// hookModelFor resolves the model and effort hooks complete on for a settings
// snapshot, falling back to the given session model. The store comes from the
// default connection: a headless runtime that never initialized one simply
// cannot route a vendor-qualified binding and falls back.
func hookModelFor(settings *setting.Data, fallback llm.Provider, fallbackModel string) (llm.Provider, string, string) {
	var roles llm.RoleBindings
	if settings != nil {
		roles = llm.BindingsOf(settings.ModelRoles, settings.ModelRoleEfforts)
	}
	var store *llm.Store
	if conn := llm.Default(); conn != nil {
		store = conn.Store()
	}
	return resolveRoleModelOver(roles, store, llm.RoleFast, fallback, fallbackModel)
}

func (m *model) refreshMemoryContext(cwd, loadReason string) {
	files := system.LoadMemoryFiles(cwd)
	var userParts, projectParts []string
	for _, f := range files {
		switch f.Level {
		case "global":
			userParts = append(userParts, f.Content)
		case "project", "local":
			projectParts = append(projectParts, f.Content)
		}
		m.services.Hook.ExecuteAsync(hook.InstructionsLoaded, hook.HookInput{
			FilePath:   f.Path,
			MemoryType: memoryTypeForLevel(f.Level),
			LoadReason: loadReason,
		})
	}
	m.env.CachedUserInstructions = joinSections(userParts)
	m.env.CachedProjectInstructions = joinSections(projectParts)
}

// syncSettingsToHookEngine hands the engine the current settings with plugin
// hooks folded in.
//
// The merge has to happen on the snapshot that is actually passed on. Settings
// .Snapshot() deep-copies on every call, so merging into a separate one — as
// both reload paths used to — writes the plugin hooks into a clone that is
// immediately discarded, and the engine receives a clean copy with none of
// them. A plugin's hooks then stopped firing after any cwd change or plugin
// install, silently. init.go gets this right by binding the snapshot first;
// doing the merge here means a caller cannot get it wrong.
func (m *model) syncSettingsToHookEngine() {
	settings := m.services.Setting.Snapshot()
	plugin.MergePluginHooksIntoSettings(settings)
	m.services.Hook.SetSettings(settings)
}

func memoryTypeForLevel(level string) string {
	switch level {
	case "global":
		return "User"
	case "local":
		return "Local"
	default:
		return "Project"
	}
}

func joinSections(parts []string) string {
	return strings.Join(parts, "\n\n")
}

// buildRecordedHookCompleter builds the hook completion whose call is recorded
// as a side call of the conversation that owns it.
func buildRecordedHookCompleter(p llm.Provider, owner *session.Session) hook.RecordedLLMCompleter {
	if p == nil {
		return nil
	}
	return func(ctx context.Context, systemPrompt, userMessage, model string) (hook.LLMCompletion, error) {
		scope := owner.Completion(session.PurposeHook)
		c := llm.NewClient(p, model, 0)
		// A recorded hook runs outside any conversation too, but the vendor
		// that states the working directory in the prompt still needs it.
		c.SetWorkingDir(func() string {
			dir, err := os.Getwd()
			if err != nil {
				return ""
			}
			return dir
		})
		resp, err := c.Complete(ctx, systemPrompt, []core.Message{core.UserMessage(userMessage, nil)}, nil, 4096, scope.Admission())
		result := hook.LLMCompletion{Text: resp.Content.Text(), Finish: scope.FinishHook}
		var admission *ai.AdmissionError
		var recording *llm.RecordingError
		if errors.As(err, &admission) || errors.As(err, &recording) {
			err = &hook.RecordingError{Err: err}
		}
		return result, err
	}
}

// buildHookCompleter builds the one-shot completion a hook runs on. hookModel
// and effort are the model and rung the role behind the hook resolved to, so
// the effort is only sent when the completion really runs on that model: a
// hook command that names a model of its own gets no rung nobody validated.
func buildHookCompleter(p llm.Provider, hookModel, effort string) hook.LLMCompleter {
	if p == nil {
		return nil
	}
	return func(ctx context.Context, systemPrompt, userMessage, model string) (string, error) {
		c := llm.NewClient(p, model, 0)
		if effort != "" && model == hookModel {
			c.SetThinkingEffort(effort)
		}
		// A hook runs outside any conversation, so it has no session id to
		// carry — but the working directory is still where this answer belongs,
		// and one vendor states it in the prompt.
		c.SetWorkingDir(func() string {
			dir, err := os.Getwd()
			if err != nil {
				return ""
			}
			return dir
		})
		resp, err := c.Complete(ctx, systemPrompt, []core.Message{core.UserMessage(userMessage, nil)}, nil, 4096)
		if err != nil {
			return "", err
		}
		return resp.Content.Text(), nil
	}
}
