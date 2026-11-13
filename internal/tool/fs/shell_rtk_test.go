//go:build !windows

package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/task"
)

// installRTKOnPath puts a fake rtk first on PATH so a rewritten command such as
// "rtk echo rtk-ran" resolves. PATH is prepended, never replaced: proc resolves
// bash once per process, so hiding PATH here would break every later test.
// The fake logs each real invocation to $RTK_LOG, which is how a test tells the
// rewritten command actually ran.
func installRTKOnPath(t *testing.T) string {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  rewrite) printf 'rtk echo rtk-ran'; exit 3 ;;\n" +
		"  echo) shift; echo \"rtk-ran:$*\"; echo \"rtk-ran:$*\" >> \"$RTK_LOG\" ;;\n" +
		"esac\n"
	path := filepath.Join(binDir, "rtk")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rtk: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "rtk.log")
	t.Setenv("RTK_LOG", log)
	return log
}

func TestRTKShellToolRunsCommands(t *testing.T) {
	tool := bashTool(t) // resolve bash before PATH gains the fake rtk
	log := installRTKOnPath(t)
	cwd := t.TempDir()

	params := map[string]any{"command": "git status", "description": "check status"}
	result := tool.ExecuteApproved(context.Background(), params, cwd)
	if !result.Success {
		t.Fatalf("ExecuteApproved() failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "rtk-ran:rtk-ran") {
		t.Fatalf("output = %q, want the rewritten command's output", result.Output)
	}
	if got := readFileOrEmpty(log); !strings.Contains(got, "rtk-ran") {
		t.Fatalf("rtk log = %q, want the rewritten command to have run", got)
	}
	if params["command"] != "git status" {
		t.Fatalf("model argument = %v, want it left as written", params["command"])
	}

	request, err := tool.PreparePermission(context.Background(), params, cwd)
	if err != nil {
		t.Fatalf("PreparePermission() error: %v", err)
	}
	if request.BashMeta == nil || request.BashMeta.Command != "git status" {
		t.Fatalf("approval preview = %#v, want the command the model wrote", request.BashMeta)
	}
}

func TestRTKBackgroundTaskKeepsTheModelCommand(t *testing.T) {
	tool := bashTool(t)
	log := installRTKOnPath(t)
	cwd := t.TempDir()

	selected, err := tool.resolve()
	if err != nil {
		t.Fatal(err)
	}
	result := tool.executeBackground(context.Background(), selected, "git status", "check status", cwd, 5*time.Second, task.LifetimeJob)
	if !result.Success {
		t.Fatalf("executeBackground() failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "Command: git status") {
		t.Fatalf("result = %q, want the command the model wrote", result.Output)
	}

	meta, ok := result.HookResponse.(map[string]any)["backgroundTask"].(map[string]any)
	if !ok {
		t.Fatalf("hook response = %#v, want a background task", result.HookResponse)
	}
	id := meta["taskId"].(string)
	background, ok := task.Default().Get(id)
	if !ok {
		t.Fatalf("missing task %s", id)
	}
	defer task.Default().Remove(id)
	defer task.Default().Kill(id)
	if got := background.GetStatus().Command; got != "git status" {
		t.Fatalf("registry command = %q, want the command the model wrote", got)
	}

	// The process itself ran the rewrite.
	waitForLog(t, log, "rtk-ran:rtk-ran")
}

func TestRTKServiceCommandIsNotRewritten(t *testing.T) {
	tool := bashTool(t)
	log := installRTKOnPath(t)

	selected, err := tool.resolve()
	if err != nil {
		t.Fatal(err)
	}
	result := tool.executeBackground(context.Background(), selected, "rtk echo service-raw", "dev server", t.TempDir(), 0, task.LifetimeService)
	if !result.Success {
		t.Fatalf("executeBackground() failed: %s", result.Error)
	}
	meta := result.HookResponse.(map[string]any)["backgroundTask"].(map[string]any)
	id := meta["taskId"].(string)
	defer task.Default().Remove(id)
	defer task.Default().Kill(id)

	// A rewritten service would have run "rtk echo rtk-ran" and logged that.
	waitForLog(t, log, "rtk-ran:service-raw")
	if got := readFileOrEmpty(log); strings.Contains(got, "rtk-ran:rtk-ran") {
		t.Fatalf("rtk log = %q, want the service left unrewritten", got)
	}
}

// waitForLog polls until the fake rtk has logged want, so a test never races
// the background process it just started.
func waitForLog(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(readFileOrEmpty(path), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("rtk log = %q, want %q", readFileOrEmpty(path), want)
}

func readFileOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
