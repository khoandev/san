package setting

import (
	"sync/atomic"

	"github.com/genai-io/san/internal/proc"
)

// activeShellKind is the language the Bash tool speaks in this run. It is empty
// until the binary records the interpreter proc resolved, which keeps bash as
// the default: a library or a test never inherits the host's SHELL, and only a
// real san run can switch the classifier over to another shell.
var activeShellKind atomic.Value // proc.ShellKind

// SetActiveShellKind records the interpreter the Bash tool runs under. Call it
// once at startup, before the first permission decision.
func SetActiveShellKind(kind proc.ShellKind) { activeShellKind.Store(kind) }

// shellSpeaksBash reports whether san's bash parser may classify a command this
// shell will run. Fish is not bash-compatible at all, so it never may: a fish
// command is never an automatic grant and never matches a stored Bash(...) rule.
func shellSpeaksBash() bool {
	kind, _ := activeShellKind.Load().(proc.ShellKind)
	return kind != proc.ShellFish
}
