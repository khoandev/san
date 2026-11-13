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

// Fish shares enough grammar with bash for the read-only list to hold, so an
// inspection opens the same grants bash gets — including Plan Mode, which used
// to refuse every fish read.
func TestFishInspectionsAreReadOnly(t *testing.T) {
	pinShell(t, proc.ShellFish)
	for _, cmd := range []string{
		"ls",
		"git status",
		"rg foo .",
		"cat README.md",
		"git log --oneline -5 | head",
		`find frontend -type f \( -name "*.tsx" -o -name "*.ts" \) -not -path "*/node_modules/*" | sort`,
		`cat frontend/package.json; echo "---"; cat frontend/tailwind.config.js 2>/dev/null || cat frontend/tailwind.config.ts 2>/dev/null; echo "---"; ls frontend`,
		`echo "a ^ b"`,
		`echo a\^b`,
		`printf '%s\n' '^'`,
		`rg '^func Test' internal`,
		`git log --oneline -5 --grep '^feat'`,
	} {
		if !IsReadOnlyShellCommand("Bash", cmd) {
			t.Errorf("%q was not classified read-only under fish", cmd)
		}
		if !PlanModeAllows("Bash", map[string]any{"command": cmd}, nil) {
			t.Errorf("%q was refused in Plan Mode under fish", cmd)
		}
	}
}

// Nothing fish spells differently may ride on that classification: a fish
// redirection the bash parser would read as a word, a substitution, or a command
// off the read-only list still lands on the confirmation gate.
func TestFishWritesStayGated(t *testing.T) {
	pinShell(t, proc.ShellFish)
	for _, cmd := range []string{
		"rm -rf build",
		"ls >/tmp/out",
		"ls 2>/tmp/err",
		"ls ^/tmp/err",  // fish 3's stderr redirect
		"ls ^^/tmp/err", // and its append form
		"ls &^/tmp/err",
		"ls >?/tmp/out", // fish's no-clobber redirect
		"ls >>?/tmp/out",
		"echo (rm -rf build)",
		"ls $HOME",
		"set -x",
		"builtin ls",
		"command ls",
		"eval ls",
		"ls; and rm -rf build",
		"ls | tee /tmp/out",
	} {
		if IsReadOnlyShellCommand("Bash", cmd) {
			t.Errorf("%q was classified read-only under fish", cmd)
		}
		if PlanModeAllows("Bash", map[string]any{"command": cmd}, nil) {
			t.Errorf("%q opened Plan Mode under fish", cmd)
		}
	}
}

// The grant stops at read-only calls: a fish command still matches no stored
// Bash(...) rule, and "always allow" still offers no bash-shaped rule for one.
func TestFishStillMatchesNoStoredRule(t *testing.T) {
	pinShell(t, proc.ShellFish)
	if _, ok := MatchAllowList("Bash", map[string]any{"command": "git status"}, []string{"Bash", "Bash(git:*)"}); ok {
		t.Error("a stored Bash rule granted a fish command")
	}
	if rules := ExactAllowRules("Bash", map[string]any{"command": "ls"}); rules != nil {
		t.Errorf("always-allow offered %v for a fish command", rules)
	}
}

// A read-only fish call is auto-permitted like its bash twin; every other fish
// call reaches the confirmation gate the way a mutating bash one does.
func TestFishConfirmationGate(t *testing.T) {
	pinShell(t, proc.ShellFish)
	settings := &Data{}
	session := func() *SessionPermissions {
		return &SessionPermissions{
			Mode:            ModeNormal,
			AllowedTools:    make(map[string]bool),
			AllowedPatterns: make(map[string]bool),
		}
	}
	cases := []struct {
		cmd  string
		want perm.Decision
	}{
		{"ls", perm.Permit},
		{"rm -rf build", perm.Prompt},
		{"ls ^/tmp/err", perm.Prompt},
	}
	for _, tc := range cases {
		if got := settings.CheckPermission("Bash", map[string]any{"command": tc.cmd}, session()); got != tc.want {
			t.Errorf("CheckPermission(%q) = %v, want %v", tc.cmd, got, tc.want)
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
