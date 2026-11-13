package setting

import (
	"testing"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/tool/perm"
)

// pinShell records the language the Bash tool speaks for one test, so the
// result does not depend on the host's SHELL.
func pinShell(t *testing.T, kind proc.ShellKind) {
	t.Helper()
	SetActiveShellKind(kind)
	t.Cleanup(func() { SetActiveShellKind(proc.ShellBash) })
}

// Fish is not bash: no fish command may be granted by a parser that cannot read
// it — not as a read-only call, not by a stored rule, not by "always allow".
func TestFishCommandsAreNeverAutoApproved(t *testing.T) {
	pinShell(t, proc.ShellFish)
	for _, cmd := range []string{"ls", "git status", "rg foo .", "cat README.md"} {
		if IsReadOnlyShellCommand("Bash", cmd) {
			t.Errorf("%q was classified read-only under fish", cmd)
		}
		if PlanModeAllows("Bash", map[string]any{"command": cmd}, nil) {
			t.Errorf("%q opened Plan Mode under fish", cmd)
		}
	}
	if _, ok := MatchAllowList("Bash", map[string]any{"command": "git status"}, []string{"Bash", "Bash(git:*)"}); ok {
		t.Error("a stored Bash rule granted a fish command")
	}
	if rules := ExactAllowRules("Bash", map[string]any{"command": "ls"}); rules != nil {
		t.Errorf("always-allow offered %v for a fish command", rules)
	}
}

// A fish command reaches the confirmation gate exactly like a mutating bash one:
// the mode default decides, because nothing proved it a read.
func TestFishCommandNeedsConfirmation(t *testing.T) {
	pinShell(t, proc.ShellFish)
	settings := &Data{}
	session := &SessionPermissions{
		Mode:            ModeNormal,
		AllowedTools:    make(map[string]bool),
		AllowedPatterns: make(map[string]bool),
	}
	for _, cmd := range []string{"ls", "git status", "rg foo ."} {
		if got := settings.CheckPermission("Bash", map[string]any{"command": cmd}, session); got != perm.Prompt {
			t.Errorf("CheckPermission(%q) = %v, want Prompt", cmd, got)
		}
	}
}

// The guard is a switch, not a blanket refusal: bash keeps every grant it had.
func TestBashCommandsKeepTheirGrants(t *testing.T) {
	pinShell(t, proc.ShellBash)
	if !IsReadOnlyShellCommand("Bash", "ls") {
		t.Error("bash read-only classification regressed")
	}
	if !PlanModeAllows("Bash", map[string]any{"command": "ls"}, nil) {
		t.Error("bash Plan Mode classification regressed")
	}
	if _, ok := MatchAllowList("Bash", map[string]any{"command": "git status"}, []string{"Bash(git:*)"}); !ok {
		t.Error("a stored Bash rule stopped matching bash")
	}
	if rules := ExactAllowRules("Bash", map[string]any{"command": "ls"}); len(rules) == 0 {
		t.Error("always-allow stopped offering rules for bash")
	}
}
