package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/todo"
)

// benchmarkFrameModel is a sized TUI with three live workers, the state in
// which the per-frame paths are most expensive.
func benchmarkFrameModel(b testing.TB, height int, workers int) *model {
	b.Helper()
	m := fixedComposerModel(100, height)
	m.conv.ShowTasks = true // otherwise the strip draws plan steps only
	m.services.Task = task.NewManager()
	for i := range workers {
		worker := task.NewAgentTask(string(rune('a'+i)), "reviewer", "Tracked work", context.Background(), nil, "")
		worker.AppendOutput([]byte(strings.Repeat("worker output line\n", 40)))
		m.services.Task.RegisterTask(worker)
		item := m.services.Tracker.Create("Tracked work", "", "", map[string]any{"background_task_id": worker.GetID()})
		if err := m.services.Tracker.Update(item.ID, todo.WithStatus(todo.StatusInProgress)); err != nil {
			b.Fatal(err)
		}
	}
	return m
}

// flushVisibleOverflow runs on every Update, and to answer "did a settled row
// fall off the frame" it renders the bottom block and the active content a
// second time. No row-count gate can replace that render (see its doc comment),
// so these numbers are the standing cost of the duplicate frame; the branch
// that does hand rows over is benchmarked in model_scrollback_overflow_test.go.
func BenchmarkFlushVisibleOverflowIdle(b *testing.B) {
	m := benchmarkFrameModel(b, 40, 3)
	m.flush.visible = scrollbackPhysicalLines(strings.Repeat("more settled rows\n", 20), 100)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.flushVisibleOverflow()
	}
}

func BenchmarkFlushVisibleOverflowStreaming(b *testing.B) {
	m := benchmarkFrameModel(b, 140, 3)
	m.flush.visible = scrollbackPhysicalLines(strings.Repeat("more settled rows\n", 20), 100)
	m.conv.Stream.Active = true
	m.conv.Stream.StartedAt = time.Now()
	m.conv.Stream.Inferring = true
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.flushVisibleOverflow()
	}
}

// BenchmarkRenderTrackerListWithRetainedOutput is the strip above the composer
// drawn for workers that have each retained a full output buffer. It runs twice
// per frame, and its row for a worker reads three fields off that task.
func BenchmarkRenderTrackerListWithRetainedOutput(b *testing.B) {
	m := benchmarkFrameModel(b, 40, 3)
	output := strings.Repeat("worker output line\n", 512*1024/19)
	for _, worker := range m.services.Task.List() {
		worker.AppendOutput([]byte(output))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.renderTrackerList()
	}
}

// BenchmarkFrameRender measures one full frame, the unit the duplicate render
// inside flushVisibleOverflow is paid against.
func BenchmarkFrameRender(b *testing.B) {
	m := benchmarkFrameModel(b, 40, 3)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.viewString()
	}
}
