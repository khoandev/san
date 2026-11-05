package app

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/broker"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/todo"
)

// notesAdvisorAnswer is a review that found only notes: nothing that stops a
// finished task from reading as done.
const notesAdvisorAnswer = `{"summary":"Small stuff.","findings":[{"severity":"P2","title":"Rename the helper","detail":"The name lies about what it does."}]}`

// newTaskReviewModel is the smallest model that carries a finished subagent task
// through the lifecycle hook into a review: an enabled advisor over a git
// workspace, a tracker store and a task manager of its own.
func newTaskReviewModel(t *testing.T, answer string) *model {
	t.Helper()
	m := newAdvisorTestModel(t, answer)
	m.advisorTasks = make(chan task.TaskInfo, 8)
	m.services.Tracker = todo.NewStore()
	m.services.Task = task.NewManager()
	// The review reads a working diff, which needs a repository; the stub answer
	// ignores what the diff says.
	if out, err := exec.Command("git", "init", "--quiet", m.env.CWD).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	m.wireAdvisor()
	return m
}

// wireTaskHook routes subagent completions into the model and undoes the global
// handler afterwards, so one test's hook cannot fire in another.
func wireTaskHook(t *testing.T, m *model) {
	t.Helper()
	m.wireTaskLifecycle(nil)
	t.Cleanup(func() { task.SetLifecycleHandler(nil) })
}

// completeAgentTask runs one subagent task to completion and returns the
// message its completion queued for the UI loop, if one was queued.
func completeAgentTask(t *testing.T, m *model, id, description string) (task.TaskInfo, bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker := m.services.Task.CreateAgentTask(id, "Backend", description, ctx, cancel)
	worker.Complete(nil)
	select {
	case info := <-m.advisorTasks:
		return info, true
	case <-time.After(5 * time.Second):
		return task.TaskInfo{}, false
	}
}

// taskItem returns the tracker item mirroring a background task.
func taskItem(t *testing.T, m *model, taskID string) *todo.Item {
	t.Helper()
	for _, item := range m.services.Tracker.List() {
		if todo.BackgroundTaskID(item) == taskID {
			return item
		}
	}
	t.Fatalf("no tracker item mirrors task %q", taskID)
	return nil
}

// A finished subagent task whose review finds a P0 is no longer a clean
// completion: the tracker item keeps its status but carries the flag and the
// review id, /tasks shows the refusal, the user gets a short notice naming the
// task, and the ordinary completion notice still reaches main.
func TestCompletedTaskWithP0NeedsReview(t *testing.T) {
	m := newTaskReviewModel(t, blockingAdvisorAnswer)
	wireTaskHook(t, m)
	completions := make(chan broker.Message, 4)
	broker.Register(broker.Main, func(msg broker.Message) bool {
		completions <- msg
		return true
	})
	t.Cleanup(func() { broker.Unregister(broker.Main) })

	info, queued := completeAgentTask(t, m, "t-p0", "Audit the auth path")
	if !queued {
		t.Fatal("the completion hook queued no review")
	}

	// The task's own notice is untouched: the user learns the task ended
	// whether or not the advisor is on.
	select {
	case msg := <-completions:
		if msg.TaskCompletion == nil || msg.TaskCompletion.TaskID != "t-p0" {
			t.Fatalf("completion notice = %+v, want the finished task", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the completion notice never reached main")
	}

	if cmd := m.onAdvisorTask(info); cmd == nil {
		t.Fatal("the review listener was not re-armed")
	}

	verdict := waitAdvisorNotice(t, m.mainNotices)
	if !strings.Contains(verdict.Display, string(advisor.StatusBlocked)) {
		t.Fatalf("verdict notice = %q, want the blocked verdict", verdict.Display)
	}
	flagged := waitAdvisorNotice(t, m.mainNotices)
	if !strings.Contains(flagged.Display, "needs review") || !strings.Contains(flagged.Display, string(advisor.SeverityP0)) {
		t.Fatalf("flag notice = %q, want the P0 refusal", flagged.Display)
	}
	if !strings.Contains(flagged.Display, "Backend: Audit the auth path") {
		t.Errorf("flag notice = %q, want the task it names", flagged.Display)
	}

	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 1 {
		t.Fatalf("history = %d reviews, want 1", len(reviews))
	}
	review := reviews[0]
	if review.Trigger != advisor.TriggerSubagentTask || review.Target.Kind != advisor.TargetTask || review.Target.ID != "t-p0" {
		t.Errorf("review = %+v, want it aimed at the finished task", review)
	}

	item := taskItem(t, m, "t-p0")
	severity, reviewID, ok := todo.NeedsReview(item)
	if !ok {
		t.Fatalf("the tracker item carries no review flag: %+v", item.Metadata)
	}
	if severity != string(advisor.SeverityP0) || reviewID != review.ID {
		t.Errorf("flag = %q by %q, want P0 by %q", severity, reviewID, review.ID)
	}
	// The work did finish; the flag changes how it reads, not what happened.
	if item.Status != todo.StatusCompleted {
		t.Errorf("status = %q, want the item to stay completed", item.Status)
	}

	// /tasks is the place the refusal is visible after the row folds away.
	var shown bool
	for _, snapshot := range m.taskSnapshots() {
		if snapshot.ID != "t-p0" {
			continue
		}
		shown = snapshot.NeedsReview == string(advisor.SeverityP0) && snapshot.ReviewID == review.ID
	}
	if !shown {
		t.Errorf("task snapshots did not carry the review flag: %+v", m.taskSnapshots())
	}
}

// Notes alone are recorded and nothing else changes: the task keeps its status,
// its tracker item stays clean, and the only notice is the review verdict.
func TestTaskReviewWithNotesLeavesTheTaskClean(t *testing.T) {
	m := newTaskReviewModel(t, notesAdvisorAnswer)
	wireTaskHook(t, m)

	info, queued := completeAgentTask(t, m, "t-p2", "Tidy the retry helper")
	if !queued {
		t.Fatal("the completion hook queued no review")
	}
	m.onAdvisorTask(info)

	verdict := waitAdvisorNotice(t, m.mainNotices)
	if !strings.Contains(verdict.Display, string(advisor.StatusPassedWithNotes)) {
		t.Fatalf("verdict notice = %q, want the notes verdict", verdict.Display)
	}
	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 1 {
		t.Fatalf("history = %d reviews, want the finding recorded once", len(reviews))
	}
	if got := len(reviews[0].Findings); got != 1 {
		t.Fatalf("findings = %d, want the P2 recorded", got)
	}
	if review := reviews[0]; review.Status == advisor.StatusBlocked || review.Severity != advisor.SeverityP2 {
		t.Errorf("review = %+v, want a P2 that blocks nothing", review)
	}

	item := taskItem(t, m, "t-p2")
	if severity, reviewID, ok := todo.NeedsReview(item); ok {
		t.Errorf("a notes-only review flagged the task: %q by %q", severity, reviewID)
	}
	if item.Status != todo.StatusCompleted {
		t.Errorf("status = %q, want it unchanged", item.Status)
	}
	// No second notice: a clean completion stays quiet.
	select {
	case n := <-m.mainNotices:
		t.Fatalf("a notes-only review published an extra notice: %+v", n)
	case <-time.After(100 * time.Millisecond):
	}
}

// Turning the trigger off costs nothing: no runner, no review, no flag, no
// notice. A finished bash task is not the advisor's business either.
func TestTaskReviewSkippedWhenTheTriggerIsOff(t *testing.T) {
	m := newTaskReviewModel(t, blockingAdvisorAnswer)
	off := false
	m.env.Advisor.Triggers.AfterSubagentTask = &off
	m.wireAdvisor()
	wireTaskHook(t, m)

	info, queued := completeAgentTask(t, m, "t-off", "Audit the auth path")
	if !queued {
		t.Fatal("the completion hook queued no review")
	}
	if m.onAdvisorTask(info) == nil {
		t.Fatal("the review listener was not re-armed")
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 0 {
		t.Fatalf("history = %d reviews, want none while the trigger is off", got)
	}
	if snap := m.services.Advisor.Indicator.Snapshot(); snap.Running {
		t.Error("a skipped review left the status bar spinning")
	}
	select {
	case n := <-m.mainNotices:
		t.Fatalf("a skipped review published a notice: %+v", n)
	default:
	}
	if item := taskItem(t, m, "t-off"); item.Status != todo.StatusCompleted {
		t.Errorf("status = %q, want the untouched completion", item.Status)
	}
}

func TestTaskReviewIgnoresBashWorkers(t *testing.T) {
	m := newTaskReviewModel(t, blockingAdvisorAnswer)
	m.requestTaskReview(task.TaskInfo{ID: "b1", Type: task.TaskTypeBash, Status: task.StatusCompleted})
	m.requestTaskReview(task.TaskInfo{ID: "t-run", Type: task.TaskTypeAgent, Status: task.StatusRunning})
	select {
	case info := <-m.advisorTasks:
		t.Fatalf("a review was queued for %+v, want only finished subagent tasks", info)
	case <-time.After(50 * time.Millisecond):
	}
}
