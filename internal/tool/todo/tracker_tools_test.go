package todo

import (
	"context"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/todo"
)

func useTestTrackerStore(t *testing.T) *todo.Store {
	t.Helper()
	store := todo.NewStore()
	if err := store.SetStorageDir(t.TempDir()); err != nil {
		t.Fatalf("SetStorageDir(): %v", err)
	}
	todo.SetDefault(store)
	t.Cleanup(func() { todo.SetDefault(store) })
	return store
}

func TestTrackerGetTool_ShowsOwnerAndOpenBlockers(t *testing.T) {
	store := useTestTrackerStore(t)

	blocker := store.Create("Blocker", "finish first", "blocking", nil)
	blocked := store.Create("Blocked", "waits on blocker", "waiting", nil)
	if err := store.Update(blocked.ID, todo.WithOwner("Explore"), todo.WithAddBlockedBy([]string{blocker.ID})); err != nil {
		t.Fatalf("Update(blocked): %v", err)
	}

	result := (&TrackerGetTool{}).Execute(context.Background(), map[string]any{
		"taskId": blocked.ID,
	}, "")

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "Owner: Explore") {
		t.Fatalf("expected owner in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "Blocked by (open): "+blocker.ID) {
		t.Fatalf("expected open blocker in output, got %q", result.Output)
	}
}

func TestTrackerGetTool_WithoutTaskIDListsAll(t *testing.T) {
	store := useTestTrackerStore(t)

	done := store.Create("First", "already done", "doing first", nil)
	if err := store.Update(done.ID, todo.WithStatus(todo.StatusCompleted)); err != nil {
		t.Fatalf("Update(done): %v", err)
	}
	pending := store.Create("Second", "still open", "doing second", nil)
	if err := store.Update(pending.ID, todo.WithOwner("Explore")); err != nil {
		t.Fatalf("Update(pending): %v", err)
	}

	result := (&TrackerGetTool{}).Execute(context.Background(), map[string]any{}, "")

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	// Compact overview: one line per task with id/status/owner, no description.
	for _, want := range []string{
		"#" + done.ID + " [" + todo.StatusCompleted + "]",
		"#" + pending.ID + " [" + todo.StatusPending + "] owner:Explore",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("expected output to contain %q, got %q", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "already done") {
		t.Fatalf("list mode should omit descriptions, got %q", result.Output)
	}
	if result.Metadata.Subtitle != "1/2 done" {
		t.Fatalf("subtitle = %q, want %q", result.Metadata.Subtitle, "1/2 done")
	}
}

func TestTrackerUpdateTool_ParsesJSONBlockedByAndPersistsFields(t *testing.T) {
	store := useTestTrackerStore(t)

	blocker := store.Create("Blocker", "must finish first", "blocking", nil)
	task := store.Create("Implement", "write tests", "writing", nil)

	result := (&TrackerUpdateTool{}).Execute(context.Background(), map[string]any{
		"taskId":       task.ID,
		"status":       todo.StatusInProgress,
		"owner":        "Plan",
		"description":  "write more tests",
		"addBlockedBy": `["` + blocker.ID + `"]`,
	}, "")

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.Metadata.Subtitle != "#"+task.ID+" "+todo.StatusInProgress {
		t.Fatalf("unexpected subtitle %q", result.Metadata.Subtitle)
	}

	updated, ok := store.Get(task.ID)
	if !ok {
		t.Fatal("expected updated task to exist")
	}
	if updated.Status != todo.StatusInProgress {
		t.Fatalf("status = %q, want %q", updated.Status, todo.StatusInProgress)
	}
	if updated.Owner != "Plan" {
		t.Fatalf("owner = %q, want %q", updated.Owner, "Plan")
	}
	if updated.Description != "write more tests" {
		t.Fatalf("description = %q, want %q", updated.Description, "write more tests")
	}
	if len(updated.BlockedBy) != 1 || updated.BlockedBy[0] != blocker.ID {
		t.Fatalf("blockedBy = %#v, want [%q]", updated.BlockedBy, blocker.ID)
	}
}

// The model reads the schema, not the system prompt, at the moment it decides
// whether to reach for a task tool. The descriptions therefore carry the
// discipline: two steps or two files is enough to make a list (the old "3+"
// threshold let ordinary multi-file work go untracked), one item is in progress
// at a time, and completed needs evidence.
func TestTrackerSchemasDemandTodoDiscipline(t *testing.T) {
	create := (&TrackerCreateTool{}).Schema().Description
	for _, want := range []string{
		"2+ distinct steps",
		"2+ files",
		"More than one deliverable",
		"exactly one task in_progress",
		"before the first edit",
		"is not evidence",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("TaskCreate description should contain %q", want)
		}
	}
	if strings.Contains(create, "3+ distinct steps") {
		t.Error("TaskCreate still asks for 3+ steps, which leaves two-step work untracked")
	}

	update := (&TrackerUpdateTool{}).Schema().Description
	for _, want := range []string{
		"exactly one task in_progress",
		"done and verified",
		"a read of the change",
		"not on \"should work\"",
	} {
		if !strings.Contains(update, want) {
			t.Errorf("TaskUpdate description should contain %q", want)
		}
	}
}
