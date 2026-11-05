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
		{"PowerShell", map[string]any{"command": "Set-Content a.txt x"}},
		{"Agent", map[string]any{"prompt": "do it"}},
		{"mcp__filesystem__write_file", map[string]any{"path": "a"}},
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
