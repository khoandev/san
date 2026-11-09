//go:build !windows

package fs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/task"
)

// The rtk rewrite contract these tests pin was measured on rtk 1.2.0:
//
//	rtk rewrite 'git status'          -> exit 3, "rtk git status", no trailing newline
//	rtk rewrite 'ls -la'              -> exit 3, "rtk ls -la"
//	rtk rewrite 'cat README.md'       -> exit 3, "rtk read README.md"
//	rtk rewrite 'git status | head'   -> exit 3, "rtk git status | head"
//	rtk rewrite 'lsof -i'             -> exit 1, no output
//	rtk rewrite 'rtk git status'      -> exit 3, "rtk git status" (identity)
//
// 0 and 3 print a rewrite (allow rules covered every command, or they did not);
// 1 and 2 print nothing and mean the command runs unchanged.

// fakeRTK writes an executable rtk stand-in and points the package's lookup at
// it. PATH is left alone on purpose: proc caches the bash path once per
// process, so a test that rewrites PATH can hide bash from every later test.
func fakeRTK(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rtk")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake rtk: %v", err)
	}
	previous := rtkLookPath
	t.Cleanup(func() { rtkLookPath = previous })
	rtkLookPath = func(name string) (string, error) {
		if name != "rtk" {
			return previous(name)
		}
		return path, nil
	}
	return path
}

func TestRTKRewriteShellCommand(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name    string
		script  string
		command string
		want    string
	}{
		{
			name:    "ask rewrite",
			script:  `if [ "$1" = rewrite ]; then printf 'rtk %s' "$2"; exit 3; fi`,
			command: "git status",
			want:    "rtk git status",
		},
		{
			name:    "allow rewrite",
			script:  `if [ "$1" = rewrite ]; then printf 'rtk %s' "$2"; exit 0; fi`,
			command: "ls -la",
			want:    "rtk ls -la",
		},
		// rtk maps cat to its own read subcommand, not to "rtk cat".
		{
			name:    "subcommand rewrite",
			script:  `if [ "$1" = rewrite ]; then printf 'rtk read %s' "${2#cat }"; exit 3; fi`,
			command: "cat README.md",
			want:    "rtk read README.md",
		},
		{
			name:    "no rewrite",
			script:  `exit 1`,
			command: "lsof -i",
			want:    "lsof -i",
		},
		{
			name:    "deny",
			script:  `if [ "$1" = rewrite ]; then printf 'rtk %s' "$2"; exit 2; fi`,
			command: "git status",
			want:    "git status",
		},
		{
			name:    "identity is not a rewrite",
			script:  `if [ "$1" = rewrite ]; then printf '%s' "$2"; exit 3; fi`,
			command: "rtk git status",
			want:    "rtk git status",
		},
		{
			name:    "empty output",
			script:  `exit 0`,
			command: "git status",
			want:    "git status",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeRTK(t, tc.script)
			if got := rewriteShellCommand(context.Background(), cwd, tc.command); got != tc.want {
				t.Fatalf("rewriteShellCommand(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestRTKRewriteWithoutBinary(t *testing.T) {
	previous := rtkLookPath
	rtkLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { rtkLookPath = previous })

	if got := rewriteShellCommand(context.Background(), t.TempDir(), "git status"); got != "git status" {
		t.Fatalf("rewrite without a binary = %q, want the command unchanged", got)
	}
}

func TestRTKRewriteDisabledByEnv(t *testing.T) {
	fakeRTK(t, `if [ "$1" = rewrite ]; then printf 'rtk %s' "$2"; exit 3; fi`)
	t.Setenv("SAN_DISABLE_RTK", "1")

	if got := rewriteShellCommand(context.Background(), t.TempDir(), "git status"); got != "git status" {
		t.Fatalf("rewrite with SAN_DISABLE_RTK=1 = %q, want the command unchanged", got)
	}
}

func TestRTKRewriteTimesOut(t *testing.T) {
	// A grandchild that inherits the output pipe is the case WaitDelay exists
	// for: killing rtk alone leaves the pipe open, and without the delay
	// cmd.Output would block until that child exits.
	for _, tc := range []struct {
		name   string
		script string
	}{
		{"deadline", `if [ "$1" = rewrite ]; then exec sleep 5; fi`},
		{"grandchild holds the pipe", `if [ "$1" = rewrite ]; then sleep 5 & wait; fi`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeRTK(t, tc.script)
			previous := rtkRewriteTimeout
			rtkRewriteTimeout = 50 * time.Millisecond
			t.Cleanup(func() { rtkRewriteTimeout = previous })

			start := time.Now()
			if got := rewriteShellCommand(context.Background(), t.TempDir(), "git status"); got != "git status" {
				t.Fatalf("rewrite after timeout = %q, want the command unchanged", got)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("rewrite blocked for %v, want the deadline plus %v", elapsed, rtkRewriteWaitDelay)
			}
		})
	}
}

func TestRTKRewriteSkipsScripts(t *testing.T) {
	// The stand-in rewrites anything it is given, so an unchanged result is the
	// guard at work, not a passthrough exit code.
	fakeRTK(t, `if [ "$1" = rewrite ]; then printf 'rtk %s' "$2"; exit 3; fi`)
	scripts := map[string]string{
		"multi-line":      "git status\ngit log -1",
		"heredoc":         "cat <<EOF > notes.txt\nhello\nEOF",
		"loop":            "for f in *.go; do\n  echo \"$f\"\ndone",
		"carriage return": "git status\r\ngit log -1",
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			if got := rewriteShellCommand(context.Background(), t.TempDir(), script); got != script {
				t.Fatalf("script = %q, want it left untouched", got)
			}
		})
	}
}

func TestRTKRewriteKeepsSingleLineChains(t *testing.T) {
	fakeRTK(t, `if [ "$1" = rewrite ]; then printf 'rtk git status && rtk go test ./...'; exit 3; fi`)
	command := "git status && go test ./..."
	if got := rewriteShellCommand(context.Background(), t.TempDir(), command); got != "rtk git status && rtk go test ./..." {
		t.Fatalf("chain = %q, want the rewrite applied", got)
	}
}

func TestRTKRewriteRunsInCallerCwd(t *testing.T) {
	dir := t.TempDir()
	fakeRTK(t, `if [ "$1" = rewrite ]; then printf '%s' "$(pwd -P)"; exit 3; fi`)
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}

	if got := rewriteShellCommand(context.Background(), dir, "git status"); got != want {
		t.Fatalf("rewrite saw cwd %q, want %q", got, want)
	}
}

func TestRTKCommandSkipsPowerShellAndServices(t *testing.T) {
	fakeRTK(t, `if [ "$1" = rewrite ]; then printf 'rtk %s' "$2"; exit 3; fi`)
	cwd := t.TempDir()
	ctx := context.Background()
	bash := proc.Shell{Kind: proc.ShellBash, Path: "bash"}

	if got := rtkCommand(ctx, proc.Shell{Kind: proc.ShellPowerShell, Path: "pwsh"}, cwd, task.LifetimeJob, "git status"); got != "git status" {
		t.Fatalf("PowerShell command = %q, want no rtk rewrite", got)
	}
	if got := rtkCommand(ctx, bash, cwd, task.LifetimeService, "npm run dev"); got != "npm run dev" {
		t.Fatalf("service command = %q, want no rtk rewrite", got)
	}
	if got := rtkCommand(ctx, bash, cwd, task.LifetimeJob, "git status"); got != "rtk git status" {
		t.Fatalf("bash job command = %q, want the rewrite", got)
	}
}

func TestRTKEnvSwitchOn(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"off", false},
		{"1", true},
		{"true", true},
		{"yes", true},
	} {
		t.Setenv("SAN_DISABLE_RTK", tc.value)
		if got := envSwitchOn("SAN_DISABLE_RTK"); got != tc.want {
			t.Fatalf("envSwitchOn(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}
