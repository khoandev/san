package fs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool/toolresult"
)

func fishTool(t *testing.T) *ShellTool {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native fish is supported on Unix")
	}
	path, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	return &ShellTool{shell: proc.Shell{Kind: proc.ShellFish, Path: path}}
}

// The directory is recorded by a fish_exit handler installed through -C, so an
// explicit exit must still leave the final directory behind and keep its status.
func TestFishTracksDirectoryAndExitStatus(t *testing.T) {
	shell := fishTool(t)
	cwd := t.TempDir()
	child := filepath.Join(cwd, "child dir")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, exit := range []int{0, 7} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			result := shell.ExecuteApproved(context.Background(), map[string]any{
				"command": fmt.Sprintf("cd 'child dir'; set -q version; or exit 99; printf 'fish\\n'; exit %d", exit),
			}, cwd)
			if result.Success != (exit == 0) || result.Output != "fish\n" {
				t.Fatalf("unexpected fish result: %+v", result)
			}
			if exit != 0 && result.Error != "exit code 7" {
				t.Fatalf("exit error = %q", result.Error)
			}
			got, err := filepath.EvalSymlinks(result.HookResponse.(map[string]any)["cwd"].(string))
			want, wantErr := filepath.EvalSymlinks(child)
			if err != nil || wantErr != nil || got != want {
				t.Fatalf("tracked cwd = %q, want %q (%v, %v)", got, want, err, wantErr)
			}
		})
	}
}

// fish -c reads config.fish, so --no-config is what keeps san's runs
// reproducible; the inherited environment must still arrive intact.
func TestFishSkipsUserStartupAndPreservesEnvironment(t *testing.T) {
	shell := fishTool(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "fish", "config.fish")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("set -gx SAN_FISH_STARTUP loaded\nprintf 'unexpected startup output\\n'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithShellEnv(context.Background(), []string{"XDG_CONFIG_HOME=" + dir, "SAN_FISH_STARTUP=unset", "SAN_FISH_INHERITED=kept"})
	result := shell.ExecuteApproved(ctx, map[string]any{
		"command": `if set -q SAN_FISH_STARTUP; echo $SAN_FISH_STARTUP; else; echo unset; end; echo $SAN_FISH_INHERITED`,
	}, dir)
	if !result.Success || result.Output != "unset\nkept\n" {
		t.Fatalf("startup/environment contract changed: %+v", result)
	}
}

func TestBackgroundFishUsesSelectedInterpreter(t *testing.T) {
	shell := fishTool(t)
	manager := task.NewManager()
	t.Cleanup(func() { manager.Cancel(); manager.Join() })
	ctx := task.WithManager(context.Background(), manager)
	result := shell.ExecuteApproved(ctx, map[string]any{
		"command":        `set -q version; or exit 99; printf 'fish-background\n'`,
		"execution_mode": shellBackgroundJob,
	}, t.TempDir())
	if !result.Success {
		t.Fatal(result.Error)
	}
	id := result.Details.(toolresult.BashDetails).TaskID
	job, ok := manager.Get(id)
	if !ok || !job.WaitForCompletion(5*time.Second) {
		t.Fatal("background fish did not finish")
	}
	if info := job.GetStatus(); info.Status != task.StatusCompleted || job.GetOutput() != "fish-background\n" {
		t.Fatalf("background fish failed: %+v, %q", info, job.GetOutput())
	}
}
