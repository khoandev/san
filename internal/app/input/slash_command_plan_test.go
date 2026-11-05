package input

import (
	"slices"
	"testing"

	"github.com/genai-io/san/internal/command"
)

// Plan Mode adds exactly one command. This pins the whole slash-command
// surface — hidden developer commands included — so a second Plan Mode entry
// point cannot slip in beside /plan without a conscious decision here.
func TestSlashCommandSurfaceHasOnePlanEntryPoint(t *testing.T) {
	want := []string{
		"models", "clear", "fork", "resume", "history", "tasks", "help", "tools",
		"skills", "agents", "context", "compact", "init", "memory", "mcp",
		"plugin", "reload-plugins", "think", "loop", "workflow", "search",
		"identity", "persona", "settings", "config", "autopilot", "goal",
		"name", "group", "trust", "evolve", "selflearn-demo", "btw", "recap",
		"plan",
	}
	handlers := builtinCommandHandlers()
	got := make([]string, 0, len(handlers))
	for name := range handlers {
		got = append(got, name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("commands = %v\nwant      %v", got, want)
	}
	planCommands := 0
	for _, name := range got {
		if name == "plan" || name == "plan-mode" || name == "execute-plan" {
			planCommands++
		}
	}
	if planCommands != 1 {
		t.Fatalf("Plan Mode exposed %d commands, want 1 (/plan)", planCommands)
	}
}

// /plan is discoverable in the catalog, and it is a builtin rather than a
// prompt command that would run the ordinary agent turn.
func TestPlanIsCataloged(t *testing.T) {
	builtins := (&command.Registry{}).BuiltinNames()
	info, ok := builtins["plan"]
	if !ok {
		t.Fatal("/plan is missing from the slash-command catalog")
	}
	if info.Description == "" {
		t.Fatal("/plan has no description")
	}
}
