package subagent

import (
	"testing"

	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool"
)

// postureRegistry is the minimum a posture test needs: the bundled explore
// definition and one edit-capable definition to prove a named call is untouched.
func postureRegistry() *Registry {
	r := NewRegistry()
	r.Register(&AgentConfig{
		Name: "explore", Description: "Investigates", Model: "@explore",
		PermissionMode: PermissionExplore, AllowTools: ToolNames(tool.ToolRead, tool.ToolBash),
		SystemPrompt: "EXPLORE_CHARTER",
	})
	r.Register(&AgentConfig{
		Name: "task", Description: "Implements", Model: "@task",
		PermissionMode: PermissionAcceptEdits, AllowTools: ToolNames(tool.ToolRead, tool.ToolEdit),
		SystemPrompt: "TASK_CHARTER",
	})
	return r
}

func postureExecutor(t *testing.T, r *Registry, parent PermissionMode) *Executor {
	t.Helper()
	executor := NewExecutor(nil, t.TempDir(), "parent-model", nil)
	executor.ConfigureOwner(nil, r, nil, nil)
	executor.SetParentPermissionMode(func() PermissionMode { return parent })
	return executor
}

// TestUnnamedAgentRunsExploreInReadOnlyPosture is the whole point of the
// posture default: Plan Mode must investigate before it edits, so an unnamed
// Agent call runs the read-only charter, not the base implementation one.
func TestUnnamedAgentRunsExploreInReadOnlyPosture(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	executor := postureExecutor(t, postureRegistry(), PermissionExplore)
	config, ok := executor.resolveAgentConfig("")
	if !ok {
		t.Fatal("unnamed agent config not found")
	}
	if config.Name != "explore" || config.GetSystemPrompt() != "EXPLORE_CHARTER" {
		t.Fatalf("read-only default = %q / %q, want the explore charter", config.Name, config.GetSystemPrompt())
	}
	// The substituted definition runs on its own role: the caller passing no
	// name must not silently re-route it to the default worker's model.
	if got := executor.configuredModel(config, ""); got != "@explore" {
		t.Fatalf("read-only default model = %q, want @explore", got)
	}

	// A name the caller did give stays untouched in the same posture.
	named, ok := executor.resolveAgentConfig("task")
	if !ok || named.Name != "task" || named.GetSystemPrompt() != "TASK_CHARTER" {
		t.Fatalf("named definition in read-only posture = %+v, %v; want task", named, ok)
	}
}

// TestUnnamedAgentKeepsBaseWorkerOutsideReadOnlyPosture pins the other half:
// nothing changes for a session that can write.
func TestUnnamedAgentKeepsBaseWorkerOutsideReadOnlyPosture(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, parent := range []PermissionMode{PermissionDefault, PermissionAcceptEdits, PermissionBypass} {
		executor := postureExecutor(t, postureRegistry(), parent)
		executor.SetDefaultModel("worker-model")
		config, ok := executor.resolveAgentConfig("")
		if !ok {
			t.Fatalf("%s: unnamed agent config not found", parent)
		}
		if config.Name != "" || config.GetSystemPrompt() != "" {
			t.Fatalf("%s: unnamed default = %+v, want the base worker", parent, config)
		}
		if got := executor.configuredModel(config, ""); got != "worker-model" {
			t.Fatalf("%s: unnamed model = %q, want the configured worker model", parent, got)
		}
	}
}

// TestReadOnlyDefaultFallsBackWhenExploreIsGone keeps a disabled or absent
// explore from failing a call the parent never named.
func TestReadOnlyDefaultFallsBackWhenExploreIsGone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	r := postureRegistry()
	if err := r.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled("explore", false, setting.ScopeUser); err != nil {
		t.Fatal(err)
	}
	executor := postureExecutor(t, r, PermissionExplore)
	if config, ok := executor.resolveAgentConfig(""); !ok || config.Name != "" {
		t.Fatalf("disabled explore = %+v, %v; want the base worker", config, ok)
	}

	empty := postureExecutor(t, NewRegistry(), PermissionExplore)
	if config, ok := empty.resolveAgentConfig(""); !ok || config.Name != "" {
		t.Fatalf("absent explore = %+v, %v; want the base worker", config, ok)
	}
}

// TestAgentSelectionPreviewNamesThePostureDefault keeps approval honest: the
// prompt must describe Explore, on the model that will answer, before the user
// approves the spawn.
func TestAgentSelectionPreviewNamesThePostureDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	executor := postureExecutor(t, postureRegistry(), PermissionExplore)
	info, raw, ok := NewExecutorAdapter(executor).ResolveAgentSelection("")
	if !ok {
		t.Fatal("unnamed selection not resolved")
	}
	if info.Name != "explore" || info.PermissionMode != string(PermissionExplore) {
		t.Fatalf("preview = %+v, want explore in explore mode", info)
	}
	if label := displayAgentName(info.Name); label != "Explore" {
		t.Fatalf("preview label = %q, want Explore", label)
	}
	if info.Model != "parent-model" {
		t.Fatalf("preview model = %q, want the model that answers (parent-model)", info.Model)
	}
	if config, ok := raw.(*AgentConfig); !ok || config.Name != "explore" {
		t.Fatalf("preview did not bind the explore definition: %#v", raw)
	}
}
