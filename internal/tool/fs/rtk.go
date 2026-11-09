package fs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/task"
)

// rtkLookPath finds rtk on PATH. It is a variable so tests can point it at a
// stand-in instead of editing PATH, which would poison the bash path proc
// resolves once for the whole process.
var rtkLookPath = exec.LookPath

// rtkRewriteTimeout bounds the `rtk rewrite` helper. A rewrite measured on rtk
// 1.2.0 costs ~150ms, so this only catches a wedged binary; the deadline is a
// variable so tests can shrink it.
var rtkRewriteTimeout = 2 * time.Second

// Exit codes with which `rtk rewrite` prints a rewrite: 0 when allow rules
// cover every command in the chain, 3 when an ask rule matched or no rule did.
// Every other code (1 no rewrite, 2 deny) means the command runs unchanged.
const (
	rtkRewriteAllowed = 0
	rtkRewriteAsk     = 3
)

// rewriteShellCommand returns command as rtk wants to run it, so the Bash tool
// hands the model compressed output without the model opting in: "git status"
// comes back as "rtk git status", "cat f" as "rtk read f".
//
// It is called at execution time, after approval, so permission rules, the
// approval preview, the transcript and the task registry keep the command the
// model wrote — rtk is a filter in front of the same command, not a different
// tool. It fails open on every path (no binary, timeout, unexpected exit code,
// empty or identical output), because a compression layer must never be able to
// break a command, and it stays silent: rtk's own stderr never reaches the
// model.
//
// rtk is detected from PATH and needs no configuration. SAN_DISABLE_RTK=1 turns
// it off. A command that already starts with rtk comes back unchanged, and
// "rtk bypass <cmd>" is the model's way to ask for raw output for one command.
func rewriteShellCommand(ctx context.Context, cwd, command string) string {
	if strings.TrimSpace(command) == "" || envSwitchOn("SAN_DISABLE_RTK") {
		return command
	}
	binary, err := rtkLookPath("rtk")
	if err != nil {
		return command
	}

	rewriteCtx, cancel := context.WithTimeout(ctx, rtkRewriteTimeout)
	defer cancel()

	// rtk reads only argv; a nil stdin keeps a hook-aware binary from waiting
	// on input this process will never send.
	cmd := exec.CommandContext(rewriteCtx, binary, "rewrite", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return command
		}
		if code := exitErr.ExitCode(); code != rtkRewriteAllowed && code != rtkRewriteAsk {
			return command
		}
	}

	// rtk prints the rewritten command with no trailing newline, and returns a
	// command already in rtk form as itself, so identity is not a rewrite.
	rewritten := strings.TrimSpace(string(out))
	if rewritten == "" || rewritten == command {
		return command
	}
	return rewritten
}

// rtkCommand is the command the shell runs: command with rtk's rewrite applied
// where rtk can filter it.
//
// PowerShell has no rtk coverage, and a background service is left raw — rtk
// would put a process between san and a daemon san has to signal and stop.
func rtkCommand(ctx context.Context, shell proc.Shell, cwd string, lifetime task.Lifetime, command string) string {
	if shell.Kind == proc.ShellPowerShell || lifetime == task.LifetimeService {
		return command
	}
	return rewriteShellCommand(ctx, cwd, command)
}

// envSwitchOn reports whether a SAN_* kill switch is set. Any value counts as
// on except the explicit off words, so SAN_DISABLE_RTK=1 and =true both read
// the way the name suggests.
func envSwitchOn(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
