package subagent

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/session"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// A worker keeps every tool result in its own conversation for the rest of its
// turn, so an oversized read must be paged exactly as the main agent's is —
// otherwise the worker pays for the same 100 KB at every following step.
func TestWorkerToolResultsArePagedThroughTheChildSession(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	store, err := session.OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	child, err := store.Create(context.Background(), session.CreateOptions{
		AgentID:    "subagent",
		InitialCWD: dir,
		Config:     &session.EventConfig{Model: new("fake"), Mode: new("normal")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Close() })

	executor := NewExecutor(nil, dir, "fake", nil)
	if err := executor.SetSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	filter := executor.resultFilter(&preparedRun{store: store, recording: child})
	if filter == nil {
		t.Fatal("a live child session was left unfiltered")
	}

	full := strings.Repeat("界", 40_000) // 120 KB of three-byte runes
	out, err := filter(context.Background(), sdkagent.PostToolContext{
		Call:   ai.ToolCall{ID: "tc-1"},
		Result: sdkagent.TextResult(full),
	})
	if err != nil || out == nil {
		t.Fatalf("filter = (%v, %v)", out, err)
	}
	page := out.Content.Text()
	if len(page) >= len(full)/4 {
		t.Errorf("paged result is %d bytes, barely smaller than the %d it replaced", len(page), len(full))
	}
	if !strings.Contains(page, "persisted") {
		t.Errorf("the worker was not told the rest exists: %q", page[max(0, len(page)-120):])
	}
	if persisted := findBlob(t, home, child.ID(), "tc-1"); persisted != full {
		t.Error("the page points at a blob that was not written whole")
	}
}

// Without a child session there is no blob to point at, and a truncated answer
// with no way back would be worse than the bytes it saves.
func TestWorkerWithoutAChildSessionLeavesResultsAlone(t *testing.T) {
	executor := NewExecutor(nil, t.TempDir(), "fake", nil)
	if filter := executor.resultFilter(&preparedRun{}); filter != nil {
		t.Fatal("a run without a native session was filtered anyway")
	}
}

func findBlob(t *testing.T, root, sessionID, callID string) string {
	t.Helper()
	want := filepath.Join("sessions", sessionID, "blobs", "tool-result", callID)
	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || found != "" {
			return err
		}
		if rel, relErr := filepath.Rel(root, path); relErr == nil && strings.HasSuffix(filepath.ToSlash(rel), want) {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatalf("no blob at %s under %s", want, root)
	}
	data, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
