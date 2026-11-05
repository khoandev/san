package setting

import (
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
