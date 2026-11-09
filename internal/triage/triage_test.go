package triage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// stubProvider plays one canned answer and records every model it was asked
// for, so a test can tell "classified" from "never called".
type stubProvider struct {
	content string
	fail    error
	hang    bool
	calls   int
	models  []string
	last    *aitest.Driver
}

func (s *stubProvider) Client(model string, _ map[string]string) (*ai.Client, error) {
	s.calls++
	s.models = append(s.models, model)
	turn := aitest.Says(s.content)
	switch {
	case s.fail != nil:
		turn = aitest.Fails(s.fail)
	case s.hang:
		turn = aitest.Hangs()
	}
	s.last = aitest.Always(turn)
	return s.last.Client(), nil
}

func (s *stubProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (s *stubProvider) Name() string                                        { return "stub" }

// routingRegistry holds the four bundled definitions with the modes the real
// files declare, so a verdict can be checked against the boundary it may not
// cross. Names listed in disabled are turned off through the same store the
// /agents screen writes.
func routingRegistry(t *testing.T, disabled ...string) *subagent.Registry {
	t.Helper()
	// The enable/disable store is the user's real agents.json; point HOME at a
	// temp dir so a test never disables a definition on the machine it runs on.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	registry := subagent.NewRegistry()
	if err := registry.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	registry.Register(&subagent.AgentConfig{Name: "explore", Model: "@fast", Tier: subagent.TierFast, PermissionMode: subagent.PermissionExplore})
	registry.Register(&subagent.AgentConfig{Name: "quick", Model: "@fast", Tier: subagent.TierFast, PermissionMode: subagent.PermissionAcceptEdits})
	registry.Register(&subagent.AgentConfig{Name: "task", Model: "@standard", Tier: subagent.TierStandard, PermissionMode: subagent.PermissionAcceptEdits})
	registry.Register(&subagent.AgentConfig{Name: "reviewer", Model: "@deep", Tier: subagent.TierDeep, PermissionMode: subagent.PermissionExplore})
	for _, name := range disabled {
		if err := registry.SetEnabled(name, false, setting.ScopeUser); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

// bareTierRegistry holds one definition that declares no rung, so a test can
// check that an unusable verdict tier never costs a definition its identity.
func bareTierRegistry(t *testing.T) *subagent.Registry {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	registry := subagent.NewRegistry()
	if err := registry.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	registry.Register(&subagent.AgentConfig{Name: "bare", Model: "@fast", PermissionMode: subagent.PermissionAcceptEdits})
	return registry
}

func newJudge(t *testing.T, content string) (*Judge, *stubProvider) {
	t.Helper()
	provider := &stubProvider{content: content}
	return New(provider, "fast-model", routingRegistry(t)), provider
}

func TestClassifyRunsOnTheFastRole(t *testing.T) {
	judge, provider := newJudge(t, `{"agent":"quick","tier":"fast","reason":"one rename"}`)

	decision, ok := judge.Classify(context.Background(), Request{Prompt: "rename the field in store.go"})
	if !ok {
		t.Fatal("the brief was not classified")
	}
	if decision.Agent != "quick" || decision.Tier != "fast" || decision.Reason != "one rename" {
		t.Fatalf("decision = %+v, want quick/fast", decision)
	}
	if len(provider.models) != 1 || provider.models[0] != "fast-model" {
		t.Fatalf("classified on %v, want the fast role's model", provider.models)
	}
	// The rubric and the answer format are the only things this inference owns.
	// A review is the case a small model under-routes on size alone, so the
	// rubric must say that findings, not diff size, choose the deep rung.
	system := provider.last.Last().System
	for _, want := range []string{"fast:", "standard:", "deep:", `"tier"`, "short design review is still deep", "never to task"} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt should contain %q", want)
		}
	}
}

// TestRouteSpeaksTheExecutorContract pins the adapter the app installs: the
// judge answers the subagent package's routing call, and keeps saying nothing
// when there is no fast role to classify with.
func TestRouteSpeaksTheExecutorContract(t *testing.T) {
	judge, _ := newJudge(t, `{"agent":"reviewer","tier":"deep","reason":"large diff"}`)
	decision, ok := judge.Route(context.Background(), subagent.RouteRequest{Prompt: "review the diff"})
	if !ok || decision.Agent != "reviewer" || decision.Tier != "deep" {
		t.Fatalf("route = %+v, ok=%v; want reviewer/deep", decision, ok)
	}

	if decision, ok := New(nil, "", routingRegistry(t)).Route(context.Background(), subagent.RouteRequest{Prompt: "brief"}); ok {
		t.Fatalf("an unassigned judge routed the call: %+v", decision)
	}
}

func TestClassifyKeepsADeepVerdict(t *testing.T) {
	judge, _ := newJudge(t, `{"agent":"task","tier":"deep","reason":"cross-package"}`)

	decision, ok := judge.Classify(context.Background(), Request{Prompt: "untangle the session projection"})
	if !ok || decision.Agent != "task" || decision.Tier != "deep" {
		t.Fatalf("decision = %+v, ok=%v; want task/deep", decision, ok)
	}
}

func TestBrokenVerdictFallsBackToStandard(t *testing.T) {
	for name, content := range map[string]string{
		"prose":        "I would say this is a small change.",
		"malformed":    `{"agent": "quick", "tier":}`,
		"unknown name": `{"agent":"ghost","tier":"fast"}`,
		"empty answer": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			judge, _ := newJudge(t, content)
			decision, ok := judge.Classify(context.Background(), Request{Prompt: "brief"})
			if !ok {
				t.Fatal("an unusable answer must still route, at the fallback rung")
			}
			if decision.Agent != "task" || decision.Tier != "standard" {
				t.Fatalf("decision = %+v, want the standard fallback", decision)
			}
		})
	}
}

func TestDisabledDefinitionFallsBackToStandard(t *testing.T) {
	provider := &stubProvider{content: `{"agent":"quick","tier":"fast"}`}
	judge := New(provider, "fast-model", routingRegistry(t, "quick"))

	decision, ok := judge.Classify(context.Background(), Request{Prompt: "one rename"})
	if !ok || decision.Agent != "task" || decision.Tier != "standard" {
		t.Fatalf("decision = %+v, ok=%v; a disabled definition must fall back", decision, ok)
	}
}

// TestDefinitionSurvivesAnUnusableRung pins the rubric's order: the definition
// is the decision, so a verdict with a missing or nonsense rung keeps the
// definition it chose at the rung that definition declares.
func TestDefinitionSurvivesAnUnusableRung(t *testing.T) {
	for name, tc := range map[string]struct{ content, agent, tier string }{
		"no rung":      {`{"agent":"explore","reason":"where is it"}`, "explore", "fast"},
		"unknown rung": {`{"agent":"reviewer","tier":"enormous"}`, "reviewer", "deep"},
	} {
		t.Run(name, func(t *testing.T) {
			judge, _ := newJudge(t, tc.content)
			decision, ok := judge.Classify(context.Background(), Request{Prompt: "brief"})
			if !ok {
				t.Fatal("the brief was not classified")
			}
			if decision.Agent != tc.agent || decision.Tier != tc.tier {
				t.Fatalf("decision = %+v, want %s at its declared %s rung", decision, tc.agent, tc.tier)
			}
		})
	}

	// A definition that declares no rung still survives the bad tier.
	judge := New(&stubProvider{content: `{"agent":"bare","tier":""}`}, "fast-model", bareTierRegistry(t))
	decision, ok := judge.Classify(context.Background(), Request{Prompt: "brief"})
	if !ok || decision.Agent != "bare" || decision.Tier != "standard" {
		t.Fatalf("decision = %+v, ok=%v; want the definition at the standard rung", decision, ok)
	}
}

// TestVerdictBudgetLeavesRoomForReasoning pins the output cap: reasoning tokens
// count against it, so an explicit effort takes the cap off the wire, while a
// plain classification stays bounded.
func TestVerdictBudgetLeavesRoomForReasoning(t *testing.T) {
	plain, plainProvider := newJudge(t, `{"agent":"quick","tier":"fast"}`)
	if _, ok := plain.Classify(context.Background(), Request{Prompt: "one rename"}); !ok {
		t.Fatal("the brief was not classified")
	}
	if got := plainProvider.last.Last().MaxTokens; got != maxVerdictTokens {
		t.Fatalf("cap = %d, want %d", got, maxVerdictTokens)
	}

	effort, effortProvider := newJudge(t, `{"agent":"quick","tier":"fast"}`)
	effort.SetThinkingEffort("high")
	if _, ok := effort.Classify(context.Background(), Request{Prompt: "one rename"}); !ok {
		t.Fatal("the brief was not classified")
	}
	if got := effortProvider.last.Last().MaxTokens; got != 0 {
		t.Fatalf("cap = %d with an effort set, want the cap off the wire", got)
	}
	if defaultTimeout != 4*time.Second {
		t.Fatalf("default timeout = %s, want the bounded 4s classification", defaultTimeout)
	}
}

// A read-only session delegates investigation: a verdict that names an editing
// charter is unusable there, so it is clamped to the read-only default instead
// of being repaired into a worker that writes.
func TestReadOnlySessionNeverGetsAnEditCharter(t *testing.T) {
	judge, _ := newJudge(t, `{"agent":"quick","tier":"fast","reason":"one rename"}`)

	decision, ok := judge.Classify(context.Background(), Request{Prompt: "one rename", ReadOnly: true})
	if !ok || decision.Agent != "explore" || decision.Tier != "standard" {
		t.Fatalf("decision = %+v, ok=%v; want explore at the fallback rung", decision, ok)
	}

	// A read-only verdict survives untouched.
	judge, _ = newJudge(t, `{"agent":"reviewer","tier":"deep"}`)
	if decision, ok := judge.Classify(context.Background(), Request{Prompt: "review the diff", ReadOnly: true}); !ok || decision.Agent != "reviewer" || decision.Tier != "deep" {
		t.Fatalf("decision = %+v, ok=%v; want the read-only reviewer", decision, ok)
	}
}

// TestRouteCarriesTheReadOnlyPosture guards the adapter seam: the executor
// tells the judge whether the call would run read-only, and dropping that flag
// would quietly hand a plan-mode session the edit charter a verdict named.
func TestRouteCarriesTheReadOnlyPosture(t *testing.T) {
	judge, _ := newJudge(t, `{"agent":"quick","tier":"fast","reason":"one rename"}`)

	readOnly, ok := judge.Route(context.Background(), subagent.RouteRequest{Prompt: "one rename", ReadOnly: true})
	if !ok || readOnly.Agent != "explore" || readOnly.Tier != "standard" {
		t.Fatalf("read-only route = %+v, ok=%v; want the explore fallback", readOnly, ok)
	}
	writing, ok := judge.Route(context.Background(), subagent.RouteRequest{Prompt: "one rename"})
	if !ok || writing.Agent != "quick" || writing.Tier != "fast" {
		t.Fatalf("writing route = %+v, ok=%v; want the verdict untouched", writing, ok)
	}
}

func TestReadOnlyFallbackInvestigates(t *testing.T) {
	judge, _ := newJudge(t, "no json at all")

	decision, ok := judge.Classify(context.Background(), Request{Prompt: "brief", ReadOnly: true})
	if !ok || decision.Agent != "explore" || decision.Tier != "standard" {
		t.Fatalf("decision = %+v, ok=%v; want explore at the standard rung", decision, ok)
	}
}

func TestProviderFailureFallsBackWithoutFailingTheCall(t *testing.T) {
	provider := &stubProvider{fail: errors.New("endpoint down")}
	judge := New(provider, "fast-model", routingRegistry(t))

	decision, ok := judge.Classify(context.Background(), Request{Prompt: "brief"})
	if !ok || decision.Agent != "task" || decision.Tier != "standard" {
		t.Fatalf("decision = %+v, ok=%v; a provider error must route at the fallback", decision, ok)
	}
}

func TestClassificationTimesOutIntoTheFallback(t *testing.T) {
	provider := &stubProvider{hang: true}
	judge := New(provider, "fast-model", routingRegistry(t))
	judge.SetTimeout(40 * time.Millisecond)

	start := time.Now()
	decision, ok := judge.Classify(context.Background(), Request{Prompt: "brief"})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("classification took %s, want the bounded deadline", elapsed)
	}
	if !ok || decision.Agent != "task" || decision.Tier != "standard" {
		t.Fatalf("decision = %+v, ok=%v; a timeout must route at the fallback", decision, ok)
	}
}

// A judge built without a model is the last guard on classification. The app
// picks the model through fast → standard → session before it builds one, so
// this case means the host passed nothing at all: decline rather than spend an
// inference the judge cannot make.
func TestJudgeWithoutAModelDeclines(t *testing.T) {
	provider := &stubProvider{content: `{"agent":"quick","tier":"fast"}`}
	judge := New(provider, "  ", routingRegistry(t))

	if judge.Assigned() {
		t.Fatal("a judge without a model reported itself assigned")
	}
	if decision, ok := judge.Classify(context.Background(), Request{Prompt: "brief"}); ok {
		t.Fatalf("an unassigned judge routed the call: %+v", decision)
	}
	if provider.calls != 0 {
		t.Fatalf("provider called %d times; a judge without a model must not classify", provider.calls)
	}
	if New(nil, "fast-model", routingRegistry(t)).Assigned() {
		t.Fatal("a judge without a provider reported itself assigned")
	}
}

// Spending an inference to re-decide a call that already named a definition or
// a rung could only disagree with the caller, so triage declines first.
func TestCallerDecisionSkipsTheJudge(t *testing.T) {
	for name, req := range map[string]Request{
		"named":  {Prompt: "brief", Name: "explore"},
		"tiered": {Prompt: "brief", Tier: "deep"},
	} {
		t.Run(name, func(t *testing.T) {
			judge, provider := newJudge(t, `{"agent":"quick","tier":"fast"}`)
			if decision, ok := judge.Classify(context.Background(), req); ok {
				t.Fatalf("a call that decided %+v was classified as %+v", req, decision)
			}
			if provider.calls != 0 {
				t.Fatalf("provider called %d times; a decided call must not be classified", provider.calls)
			}
		})
	}

	// A blank signal is not a decision: the call still left the choice open.
	judge, provider := newJudge(t, `{"agent":"quick","tier":"fast"}`)
	if _, ok := judge.Classify(context.Background(), Request{Prompt: "brief", Name: " ", Tier: " "}); !ok {
		t.Fatal("a blank name and tier left the call unclassified")
	}
	if provider.calls != 1 {
		t.Fatalf("provider called %d times, want one classification", provider.calls)
	}
}
