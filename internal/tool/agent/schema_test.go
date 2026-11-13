package agent

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool"
)

func TestAgentToolSchemaMatchesBuilder(t *testing.T) {
	if got := (&AgentTool{}).Schema().Description; got != agentSchema("").Description {
		t.Fatalf("Agent schema differs from its builder: %q", got)
	}
	for _, removed := range []string{"Available agent definitions", "Select explore", "Select edit", "YOLO", "General"} {
		if strings.Contains(agentSchema("").Description, removed) {
			t.Fatalf("Agent description still exposes worker variants: %q", removed)
		}
	}
}

// Delegation is the default for work that stands on its own; the brake is that
// the main conversation keeps what is one step or needs its context. The test
// pins both halves, because losing either one costs: without the first the main
// model does everything itself, without the second every turn spawns a worker.
func TestAgentSchemaPutsDelegationFirst(t *testing.T) {
	description := agentSchema("").Description
	for _, want := range []string{
		"Delegate independently scoped, self-contained work",
		"Do it yourself when it is one step or needs the conversation's context",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("Agent description should contain %q", want)
		}
	}
}

func TestAgentSchemaRetainsDelegationGuidance(t *testing.T) {
	description := agentSchema("").Description
	for _, want := range []string{
		"only task-local context",
		"the validation plan",
		"Report a failed tool and its reason",
		"Launch independent tasks concurrently",
		"foreground when you need the result to proceed",
		"background when you can continue useful work",
		"Do not duplicate delegated work",
		"verify the result before reporting it",
		"SendMessage to the returned Task ID continues the same session, model and tier",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("Agent description should retain %q guidance", want)
		}
	}
}

// sampleCatalog is a four-entry catalog in the shape the registry renders: one
// line per definition, no when-to-use lines. It stands in for the real catalog
// so the length guard measures this package's prose against a fixed cost rather
// than whatever a user's descriptions happen to weigh.
const sampleCatalog = "Available predefined agents:\n\n" +
	"- explore [read-only, @fast, fast]: Read-only investigation — where code lives, how it behaves, and what a dependency actually guarantees.\n" +
	"- quick [edit, @fast, fast]: Fast, inexpensive work — one small, well-specified change or a quick lookup.\n" +
	"- reviewer [read-only, @deep, deep]: Independent review of a diff, a design or a proposed change — findings only, never edits.\n" +
	"- task [edit, @standard, standard]: Bounded implementation work in this repository — a few files with a clear definition of done."

// TestAgentSchemaTeachesTiersAndStaysLean pins the two things the delegating
// model needs and the one thing this description kept losing: the four
// definitions, the three tiers with the brief each one buys, and a length that
// cannot creep back into a wall of prose.
func TestAgentSchemaTeachesTiersAndStaysLean(t *testing.T) {
	// The prose without a catalog is what this package owns, so it carries the
	// tighter bound; the catalog is the registry's to size. The bounds carry the
	// retry-safety sentence merged from upstream, which is prose this package
	// owns, and nothing more.
	if base := len(agentSchema("").Description); base > 970 {
		t.Errorf("catalog-free description = %d chars, want <= 970", base)
	}
	full := agentSchema(sampleCatalog).Description
	if len(full) > 1870 {
		t.Errorf("description with a four-entry catalog = %d chars, want <= 1870", len(full))
	}
	for _, want := range []string{
		"- explore [read-only, @fast, fast]",
		"- quick [edit, @fast, fast]",
		"- reviewer [read-only, @deep, deep]",
		"- task [edit, @standard, standard]",
		"- fast: one sentence of goal",
		"- standard: the goal, the paths involved, and the definition of done",
		"- deep: the goal, exact files, constraints, acceptance criteria",
		"Omit name and tier for routine work; san routes it",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("description should teach %q:\n%s", want, full)
		}
	}
	// A catalog-free schema cannot point at a definition that may not exist,
	// and the tier rubric still applies to the unnamed default worker.
	if strings.Contains(agentSchema("").Description, "Route by intent") {
		t.Error("catalog-free description advises a choice it cannot offer")
	}
	if !strings.Contains(agentSchema("").Description, "- fast: one sentence of goal") {
		t.Error("catalog-free description lost the brief rubric")
	}
}

// TestAgentTierParameter pins the routing knob: three rungs, optional, and
// omitted means san decides rather than the model guessing a model id.
func TestAgentTierParameter(t *testing.T) {
	properties := agentToolParameters["properties"].(map[string]any)
	property, ok := properties["tier"].(map[string]any)
	if !ok {
		t.Fatal("Agent schema should expose tier")
	}
	if property["type"] != "string" {
		t.Errorf("tier type = %v, want string", property["type"])
	}
	want := []string{"fast", "standard", "deep"}
	got, ok := property["enum"].([]string)
	if !ok || len(got) != len(want) {
		t.Fatalf("tier enum = %v, want %v", property["enum"], want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tier enum = %v, want %v", got, want)
		}
	}
	description, _ := property["description"].(string)
	for _, phrase := range []string{"Omit it to let san route", "a named definition supplies its own tier"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("tier description should say %q: %q", phrase, description)
		}
	}
}

func TestAgentBackgroundGuidancePreservesContinuation(t *testing.T) {
	properties := agentToolParameters["properties"].(map[string]any)
	background := properties["run_in_background"].(map[string]any)
	description := background["description"].(string)
	for _, want := range []string{"Defaults to false for new work", "useful independent work"} {
		if !strings.Contains(description, want) {
			t.Errorf("background description should contain %q", want)
		}
	}
}

func TestAgentSchemaExplainsNameResolution(t *testing.T) {
	properties := agentToolParameters["properties"].(map[string]any)
	name, ok := properties["name"].(map[string]any)
	if !ok {
		t.Fatal("Agent schema should expose name")
	}
	want := "Optional. A name the catalog lists selects that definition. Any other name only labels the task, and san still routes the call on its brief."
	if description := name["description"]; description != want {
		t.Fatalf("name description = %q, want %q", description, want)
	}
}

// The catalog turns a list of definitions into a choice. Every row here is a
// decision the delegating model would otherwise make by name similarity, and
// the last line keeps a findings-only brief away from the writing worker.
func TestAgentSchemaRoutesByIntent(t *testing.T) {
	description := agentSchema(sampleCatalog).Description
	for _, want := range []string{
		"Route by intent",
		"- a question, or where code lives → explore",
		"- a fix with a definition of done → task",
		"- one small edit already spelled out → quick",
		"- checking an existing diff or design → reviewer",
		"Name an entry; other names are labels",
		"Findings never go to task",
		"a read-only definition never edits",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("routing table should teach %q:\n%s", want, description)
		}
	}
}

// ReadOnlyDelegation answers the Plan Mode gate, so the two easy mistakes are
// refusing the unnamed default (which a read-only session already runs as
// explore) and vouching for a named definition without the registry.
func TestReadOnlyDelegationTreatsAnUnnamedCallAsReadOnly(t *testing.T) {
	if !ReadOnlyDelegation(map[string]any{"prompt": "map it"}, nil) {
		t.Fatal("an unnamed call defaults to the read-only worker and must be allowed")
	}
	if !ReadOnlyDelegation(map[string]any{"prompt": "map it", "name": "   "}, func(string) bool { return false }) {
		t.Fatal("a blank name is not a lookup and must stay allowed")
	}
	if ReadOnlyDelegation(map[string]any{"name": "explore", "prompt": "map it"}, nil) {
		t.Fatal("a named call without a registry verdict is not read-only")
	}
}

// A call approval already routed must be judged by the definition it will run,
// not by the name it omitted: a route to an edit worker is not read-only even
// though the call itself named nothing.
func TestReadOnlyDelegationJudgesTheRoutedDefinition(t *testing.T) {
	readOnly := func(name string) bool { return name == "explore" || name == "reviewer" }
	for agent, want := range map[string]bool{"quick": false, "task": false, "explore": true, "reviewer": true} {
		args := map[string]any{"prompt": "brief", "_route": tool.AgentRoute{Tier: "fast", Agent: agent}}
		if got := ReadOnlyDelegation(args, readOnly); got != want {
			t.Errorf("ReadOnlyDelegation(routed to %s) = %v, want %v", agent, got, want)
		}
	}
	// A pin that routed nothing is still the unnamed default worker.
	if !ReadOnlyDelegation(map[string]any{"prompt": "brief", "_route": tool.AgentRoute{}}, readOnly) {
		t.Error("an unrouted pin made an unnamed call look like an edit")
	}
}

func TestReadOnlyDelegationFollowsTheRegistry(t *testing.T) {
	readOnly := func(name string) bool { return name == "explore" || name == "reviewer" }
	cases := map[string]bool{"explore": true, "reviewer": true, "task": false, "quick": false, "unknown": false}
	for name, want := range cases {
		if got := ReadOnlyDelegation(map[string]any{"name": name, "prompt": "brief"}, readOnly); got != want {
			t.Errorf("ReadOnlyDelegation(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAgentSchemaOmitsPermissionMode(t *testing.T) {
	properties := agentToolParameters["properties"].(map[string]any)
	if _, exists := properties["mode"]; exists {
		t.Fatal("Agent schema still exposes a permission selector")
	}
}

func TestAgentSchemaOmitsModelOverride(t *testing.T) {
	properties := agentToolParameters["properties"].(map[string]any)
	if _, ok := properties["model"]; ok {
		t.Fatal("Agent schema should not expose a model override")
	}
}

func TestAgentStopSchemaRequiresOnlyTaskID(t *testing.T) {
	params := (&AgentStopTool{}).Schema().Definition.(map[string]any)
	required := params["required"].([]string)
	if len(required) != 1 || required[0] != "task_id" {
		t.Fatalf("AgentStop required fields = %#v, want [task_id]", required)
	}
}
