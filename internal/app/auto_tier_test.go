package app

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/acp/protocol"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// autoTierProvider answers one canned reply per Client() and counts the calls,
// which is how these tests tell "classified" from "never asked".
type autoTierProvider struct {
	reply string
	calls int
}

func (p *autoTierProvider) Client(string, map[string]string) (*ai.Client, error) {
	p.calls++
	return aitest.Always(aitest.Says(p.reply)).Client(), nil
}

func (p *autoTierProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (p *autoTierProvider) Name() string                                        { return "auto-tier-stub" }

// autoTierRegistry holds the definitions a routed call may name.
func autoTierRegistry() *subagent.Registry {
	registry := subagent.NewRegistry()
	registry.Register(&subagent.AgentConfig{Name: "explore", Model: "@fast", PermissionMode: subagent.PermissionExplore})
	registry.Register(&subagent.AgentConfig{Name: "quick", Model: "@fast", PermissionMode: subagent.PermissionAcceptEdits})
	registry.Register(&subagent.AgentConfig{Name: "task", Model: "@standard", PermissionMode: subagent.PermissionAcceptEdits})
	return registry
}

// autoTierResolver is the role resolver an agent executor receives: the fast
// role answers with the given provider and model, everything else is unassigned.
func autoTierResolver(provider llm.Provider, model string) subagent.RoleResolver {
	return func(role string) subagent.RoleAssignment {
		if role != string(llm.RoleFast) || model == "" {
			return subagent.RoleAssignment{}
		}
		return subagent.RoleAssignment{Provider: provider, ModelID: model, Assigned: true}
	}
}

func autoTierAdapter(t *testing.T, registry *subagent.Registry, resolve subagent.RoleResolver) *subagent.ExecutorAdapter {
	t.Helper()
	parent := &autoTierProvider{reply: "parent"}
	executor := subagent.NewExecutor(parent, t.TempDir(), "session-model", nil)
	executor.ConfigureOwner(nil, registry, nil, nil)
	executor.SetParentPermissionMode(func() subagent.PermissionMode { return subagent.PermissionDefault })
	executor.SetRoleResolver(resolve)
	return subagent.NewExecutorAdapter(executor)
}

// TestInstallAutoTier pins the two conditions the switch has: the setting is on,
// and the fast role names a model. Anything else leaves the adapter without a
// router, so the Agent call behaves exactly as it did before routing existed.
func TestInstallAutoTier(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		fastModel  string
		wantRouted bool
	}{
		{"on and bound", true, "fast-model", true},
		{"off", false, "fast-model", false},
		{"on but the fast role is unassigned", true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := autoTierRegistry()
			classifier := &autoTierProvider{reply: `{"agent":"quick","tier":"fast","reason":"one rename"}`}
			resolve := autoTierResolver(classifier, tc.fastModel)
			adapter := autoTierAdapter(t, registry, resolve)
			installAutoTier(adapter, registry, tc.enabled, resolve)

			decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "rename the field"})
			if routed != tc.wantRouted {
				t.Fatalf("routed = %v, want %v (decision %+v)", routed, tc.wantRouted, decision)
			}
			if !routed {
				if classifier.calls != 0 {
					t.Fatalf("classifier called %d times without a router", classifier.calls)
				}
				return
			}
			if decision.Agent != "quick" || decision.Tier != "fast" {
				t.Fatalf("decision = %+v, want quick/fast", decision)
			}
			if classifier.calls != 1 {
				t.Fatalf("classifier called %d times, want one classification", classifier.calls)
			}
		})
	}
}

// TestAutoTierEnabledFollowsTheSetting pins the TUI switch read: absent
// settings mean on, an explicit false turns it off.
func TestAutoTierEnabledFollowsTheSetting(t *testing.T) {
	if !(&model{}).autoTierEnabled() {
		t.Fatal("a model without settings must follow the default: on")
	}
	off := false
	toggled := &model{services: services{Setting: setting.New(&setting.Data{Agents: setting.AgentsSettings{AutoTier: &off}})}}
	if toggled.autoTierEnabled() {
		t.Fatal("agents.autoTier=false was ignored")
	}
}

// TestInstallAutoTierNeverRoutesAContinuation keeps the promise the Agent tool
// makes to a resumed run: its policy is the one it was approved under.
func TestInstallAutoTierNeverRoutesAContinuation(t *testing.T) {
	registry := autoTierRegistry()
	classifier := &autoTierProvider{reply: `{"agent":"quick","tier":"fast"}`}
	resolve := autoTierResolver(classifier, "fast-model")
	adapter := autoTierAdapter(t, registry, resolve)
	installAutoTier(adapter, registry, true, resolve)

	if decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "continue", Resume: true}); routed {
		t.Fatalf("a continuation was routed: %+v", decision)
	}
	if classifier.calls != 0 {
		t.Fatalf("classifier called %d times for a continuation", classifier.calls)
	}
}

// writeAutoTierSettings writes the session settings an ACP fixture reads back
// when it opens a session: the fast role bound to a model, and the switch as
// the case under test. nil leaves the switch unset (default on).
func writeAutoTierSettings(t *testing.T, autoTier *bool) {
	t.Helper()
	settings := map[string]any{
		"modelRoles": map[string]string{"fast": "test-model"},
	}
	if autoTier != nil {
		settings["agents"] = map[string]any{"autoTier": *autoTier}
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(os.Getenv("HOME"), ".san")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestACPSharesTheTierRouterWithTheTUI proves the wiring rather than the
// helper: an ACP session spends one classification inference on an unnamed
// delegation, and spends none when agents.autoTier is false.
func TestACPSharesTheTierRouterWithTheTUI(t *testing.T) {
	off := false
	for _, tc := range []struct {
		name     string
		autoTier *bool
		want     int32
	}{
		{"default on", nil, 1},
		{"explicit off", &off, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var classified atomic.Int32
			driver := aitest.Always(func(ctx context.Context, req *ai.Request) iter.Seq2[ai.Delta, error] {
				switch {
				case strings.Contains(req.System, "You classify a delegation brief"):
					classified.Add(1)
					return aitest.Says(`{"agent":"quick","tier":"fast","reason":"one line"}`)(ctx, req)
				case workerRequest(req):
					return aitest.Says("worker completed")(ctx, req)
				case hasResult(req):
					return aitest.Says("parent completed")(ctx, req)
				}
				return aitest.Asks(ai.ToolCall{
					ID:    "launch",
					Name:  "Agent",
					Input: `{"prompt":"WORKER_BRIEF","description":"router test"}`,
				})(ctx, req)
			})
			f := newACPFixture(t, driver, func(context.Context, protocol.RequestPermissionRequest) (protocol.RequestPermissionResponse, error) {
				return acpChoice("allow_once"), nil
			})
			writeAutoTierSettings(t, tc.autoTier)
			id := f.session(t, t.TempDir())
			if _, err := f.prompt(id, "delegate"); err != nil {
				t.Fatal(err)
			}
			if got := classified.Load(); got != tc.want {
				t.Fatalf("classification inferences = %d, want %d", got, tc.want)
			}
		})
	}
}
