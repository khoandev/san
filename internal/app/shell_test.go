package app

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/proc"
)

func TestShellFallbackWarning(t *testing.T) {
	if os.Getenv("SAN_TEST_SHELL_WARNING") == "1" {
		warnShellFallback()
		fmt.Print(proc.DefaultShellName())
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows ignores the Unix SHELL preference")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	type shellCase struct {
		preference, want string
		reason           string // expected fallback diagnostic; "" means silence
	}
	cases := []shellCase{
		{bash, "bash", ""},
		{"/usr/bin/tcsh", "bash", "is unsupported; falling back to bash"},
		// Fish is a shell san speaks, so a missing one is unavailable rather
		// than unsupported — a distinction the diagnostic must keep.
		{"/not-installed/fish", "bash", "is unavailable"},
	}
	if zsh, err := exec.LookPath("zsh"); err == nil {
		cases = append(cases, shellCase{zsh, "zsh", ""})
	}
	if fish, err := exec.LookPath("fish"); err == nil {
		cases = append(cases, shellCase{fish, "fish", ""})
	}
	for _, tc := range cases {
		t.Run(tc.preference, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestShellFallbackWarning$")
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "SHELL=") && !strings.HasPrefix(env, "SAN_SHELL=") && !strings.HasPrefix(env, "SAN_TEST_SHELL_WARNING=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "SHELL="+tc.preference, "SAN_SHELL=", "SAN_TEST_SHELL_WARNING=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil || !strings.HasPrefix(string(out), tc.want) {
				t.Fatalf("selected shell = %q, %v (%s)", out, err, stderr.String())
			}
			got := stderr.String()
			if tc.reason == "" {
				if got != "" {
					t.Fatalf("unexpected fallback diagnostic = %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.reason) {
				t.Fatalf("fallback diagnostic = %q, want %q", got, tc.reason)
			}
		})
	}
}
