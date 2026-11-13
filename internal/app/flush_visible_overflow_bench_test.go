package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
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

// streamingFrameModel is the frame the person waits on: a long reply arriving
// chunk by chunk, redrawn on every token batch, with the blocks that already
// completed already handed to scrollback (see FlushStreamingBlocks). Only the
// block still being written stays live, which is what the app actually draws.
func streamingFrameModel(b testing.TB, settled, tail string) (*model, *core.ChatMessage) {
	b.Helper()
	m := benchmarkFrameModel(b, 40, 3)
	m.conv.Stream.Active = true
	m.conv.Stream.StartedAt = time.Now()
	m.conv.Stream.Inferring = true
	m.conv.Messages = append(m.conv.Messages, core.ChatMessage{
		ID: "stream-1", Role: core.ChatAssistant, Content: settled + tail, ContentCommittedLen: len(settled),
	})
	m.conv.Stream.MessageID = "stream-1"
	return m, &m.conv.Messages[len(m.conv.Messages)-1]
}

// BenchmarkFrameRenderStreamingReply grows the trailing paragraph by a word per
// frame, the shape a streamed answer has. A frame that re-wraps the settled part
// of the block pays for it here.
func BenchmarkFrameRenderStreamingReply(b *testing.B) {
	settled := strings.Repeat("A paragraph of the answer that keeps arriving, with `code` in it.\n\n", 60)
	m, row := streamingFrameModel(b, settled, "The final paragraph that keeps arriving, word by word, until it ends.")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		row.Content += " more"
		m.viewString()
	}
}

// BenchmarkFrameRenderStreamingLongBlock is the same frame while a long fenced
// block streams: the tail is one block with hundreds of rows, and it stays live
// until the model closes the fence.
func BenchmarkFrameRenderStreamingLongBlock(b *testing.B) {
	settled := strings.Repeat("A paragraph of the answer that keeps arriving.\n\n", 40)
	tail := "```go\n" + strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) error {\n", 320)
	m, row := streamingFrameModel(b, settled, tail)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		row.Content += "x"
		m.viewString()
	}
}
