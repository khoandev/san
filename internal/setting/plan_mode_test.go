package setting

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool/perm"
)

func planModeSession() *SessionPermissions {
	sp := NewSessionPermissions()
	sp.SetPlanMode(true)
	return sp
}

func TestPlanModeRejectsWorkspaceWrites(t *testing.T) {
	d := &Data{}
	session := planModeSession()
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"Edit", map[string]any{"file_path": "a.go"}},
		{"Write", map[string]any{"file_path": "a.go", "content": "x"}},
		{"Bash", map[string]any{"command": "rm -rf build"}},
		{"Bash", map[string]any{"command": "go test ./... > out.txt"}},
		// An inspection-shaped command is not an inspection: sed in place,
		// xargs handing the listing to rm, and go compiling.
		{"Bash", map[string]any{"command": "sed -i '' 's/a/b/' internal/x.go"}},
		{"Bash", map[string]any{"command": "find internal -name '*.go' | xargs rm"}},
		{"Bash", map[string]any{"command": "go test ./internal/..."}},
		{"Bash", map[string]any{"command": "gofmt -w internal/x.go"}},
		{"PowerShell", map[string]any{"command": "Set-Content a.txt x"}},
		// No delegation predicate is installed here, which is the posture's
		// default: every Agent call is refused, named or not.
		{"Agent", map[string]any{"prompt": "do it"}},
		{"mcp__filesystem__write_file", map[string]any{"path": "a"}},
		{"mcp__github__create_issue", map[string]any{"title": "x"}},
		{"mcp__git__commit", map[string]any{"message": "x"}},
		{"mcp__codegraph__delete_graph", map[string]any{}},
	}
	for _, tc := range cases {
		got := d.HasPermissionToUseTool(tc.tool, tc.args, session)
		if got.Behavior != perm.Reject || got.Reason != "plan mode: read-only" {
			t.Errorf("%s = %+v, want plan-mode rejection", tc.tool, got)
		}
	}
}

func TestPlanModeAllowsReadsAndQuestions(t *testing.T) {
	d := &Data{}
	session := planModeSession()
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"Read", map[string]any{"file_path": "a.go"}},
		{"TaskGet", map[string]any{"id": "1"}},
		{"WebSearch", map[string]any{"query": "docs"}},
		{"AskUserQuestion", map[string]any{}},
		{"Bash", map[string]any{"command": "ls -la"}},
		{"Bash", map[string]any{"command": "git status"}},
		{"Bash", map[string]any{"command": "rg plan internal"}},
		// Inspecting the tree while planning is the job, banners and pipes
		// included.
		{"Bash", map[string]any{"command": `ls docs/ 2>/dev/null | head -80; echo "---"; ls internal/`}},
		// A read-only MCP server is how planning sees a code graph or a
		// library's docs; only the writers are refused.
		{"mcp__codegraph__codegraph_explore", map[string]any{"query": "plan store"}},
		{"mcp__context7__resolve-library-id", map[string]any{"libraryName": "bubbletea"}},
		{"mcp__filesystem__read_file", map[string]any{"path": "a.go"}},
		// The inspections planning actually runs: an inventory through
		// xargs, a range read through sed, and doc lookups.
		{"Bash", map[string]any{"command": "find internal -name '*.go' | xargs wc -l | sort -n | tail -60"}},
		{"Bash", map[string]any{"command": "sed -n 120,175p internal/llm/registry.go"}},
		{"Bash", map[string]any{"command": "go doc fmt.Println"}},
		// A skill's instructions are context, not an effect.
		{"Skill", map[string]any{"skill": "review"}},
	}
	for _, tc := range cases {
		if got := d.HasPermissionToUseTool(tc.tool, tc.args, session); got.Behavior == perm.Reject {
			t.Errorf("%s was rejected in plan mode: %+v", tc.tool, got)
		}
	}
}

// A delegated investigation is still read-only work, so the gate hands the
// Agent verdict to the app: only this package cannot read the agent registry,
// and a missing predicate has to mean "no delegation" rather than "allow".
func TestPlanModeDelegatesOnlyReadOnlyAgents(t *testing.T) {
	d := &Data{}
	session := planModeSession()
	session.SetPlanModeDelegates(func(args map[string]any) bool {
		name, _ := args["name"].(string)
		// The app's real predicate treats an unnamed call as the posture
		// default; mirror that so the allowed cases stay honest.
		if strings.TrimSpace(name) == "" {
			return true
		}
		return name == "explore" || name == "reviewer"
	})

	allowed := []map[string]any{
		{"prompt": "map the plan store"},
		{"name": "explore", "prompt": "map the plan store"},
		{"name": "reviewer", "prompt": "review the draft"},
	}
	for _, args := range allowed {
		if got := d.HasPermissionToUseTool("Agent", args, session); got.Behavior == perm.Reject {
			t.Errorf("Agent(%v) was refused in plan mode: %+v", args["name"], got)
		}
	}

	refused := []map[string]any{
		{"name": "task", "prompt": "implement it"},
		{"name": "quick", "prompt": "rename the field"},
	}
	for _, args := range refused {
		got := d.HasPermissionToUseTool("Agent", args, session)
		if got.Behavior != perm.Reject || got.Reason != "plan mode: read-only" {
			t.Errorf("Agent(%v) = %+v, want plan-mode rejection", args["name"], got)
		}
	}

	// A snapshot taken while the predicate is installed keeps it: the gate
	// makes one decision against one posture, and the function value never
	// changes underneath it.
	if snap := session.Snapshot(); snap.PlanModeDelegates == nil {
		t.Fatal("Snapshot dropped the delegation predicate")
	}

	// Without a predicate the posture keeps its default: no delegation at all,
	// even for a name the app would have vouched for.
	for _, args := range allowed {
		got := d.HasPermissionToUseTool("Agent", args, planModeSession())
		if got.Behavior != perm.Reject || got.Reason != "plan mode: read-only" {
			t.Errorf("Agent(%v) without a predicate = %+v, want plan-mode rejection", args["name"], got)
		}
	}

	// The verdict must not survive into a posture that is not planning.
	session.SetPlanMode(false)
	if got := d.HasPermissionToUseTool("Agent", map[string]any{"name": "task"}, session); got.Reason == "plan mode: read-only" {
		t.Fatalf("leaving plan mode kept the read-only verdict: %+v", got)
	}
}

func TestPlanModeOutranksBypassAndAskRules(t *testing.T) {
	d := &Data{Permissions: PermissionSettings{Ask: []string{"Edit"}}}
	session := planModeSession()
	session.SetMode(ModeBypassPermissions)
	got := d.HasPermissionToUseTool("Write", map[string]any{"file_path": "a"}, session)
	if got.Behavior != perm.Reject || got.Reason != "plan mode: read-only" {
		t.Fatalf("bypass escaped plan mode: %+v", got)
	}
}

// A mounted tool's name is the only signal Plan Mode has, so the leading verb
// decides: reads pass, writers are refused, and a file path or a noun later in
// the name never flips the verdict.
func TestPlanModeMountedToolVerdicts(t *testing.T) {
	cases := map[string]bool{
		"mcp__codegraph__codegraph_explore": true,
		"mcp__x__list_posts":                true,
		"mcp__x__get_settings":              true,
		"mcp__x__search_code":               true,
		"mcp__x__write_file":                false,
		"mcp__x__updates":                   false,
		"mcp__x__drop_table":                false,
		"mcp__x__exec_command":              false,
		"Bash":                              false,
		"Read":                              false,
	}
	for tool, want := range cases {
		if got := planModeReadOnlyForeignTool(tool); got != want {
			t.Errorf("planModeReadOnlyForeignTool(%q) = %v, want %v", tool, got, want)
		}
	}
}

func TestPlanModeLeavesDenyRulesFirst(t *testing.T) {
	d := &Data{Permissions: PermissionSettings{Deny: []string{"Read(secret.txt)"}}}
	session := planModeSession()
	got := d.HasPermissionToUseTool("Read", map[string]any{"file_path": "secret.txt"}, session)
	if got.Behavior != perm.Reject || got.Reason != "deny rule: Read(secret.txt)" {
		t.Fatalf("deny rule = %+v", got)
	}
}

func TestPlanModeSurvivesModeCycling(t *testing.T) {
	sp := planModeSession()
	sp.ResetPosture()
	sp.SetMode(ModeNormal)
	if !sp.PlanModeActive() {
		t.Fatal("ResetPosture must not clear Plan Mode")
	}
	if snap := sp.Snapshot(); !snap.PlanMode {
		t.Fatal("Snapshot must carry Plan Mode")
	}
	sp.SetPlanMode(false)
	if sp.PlanModeActive() {
		t.Fatal("SetPlanMode(false) did not clear it")
	}
}
