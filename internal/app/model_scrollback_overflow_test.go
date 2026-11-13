package app

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// TestFlushVisibleOverflowKeepsRowsThatFit is the cheap half of the rule: three
// short rows above a composer that occupies five stay in the live frame.
func TestFlushVisibleOverflowKeepsRowsThatFit(t *testing.T) {
	m := fixedComposerModel(80, 24)
	m.queueScrollbackPrint(strings.Join([]string{"row one", "row two", "row three"}, "\n"), 0)
	if len(m.flush.pendingPrints) != 0 {
		t.Fatal("rows that fit the frame were printed into history")
	}
	if got := len(m.flush.visible); got != 3 {
		t.Fatalf("retained rows = %d, want the 3 that still fit", got)
	}
}

// TestFlushVisibleOverflowHandsEveryRowOverWhenTheFrameIsFull pins the half a
// row-count gate gets wrong: when the bottom block and the active tail fill the
// terminal, keep is zero and every retained row leaves — a handful of short
// rows included. Skipping the measurement because the retained rows look short
// would strand them in a frame that cannot draw them.
func TestFlushVisibleOverflowHandsEveryRowOverWhenTheFrameIsFull(t *testing.T) {
	m := fixedComposerModel(80, 24)
	m.conv.Stream.Active = true
	for range 40 {
		m.conv.Append(core.ChatMessage{Role: core.ChatAssistant, Content: "streaming row"})
	}
	m.queueScrollbackPrint(strings.Join([]string{"row one", "row two", "row three"}, "\n"), 0)
	if len(m.flush.pendingPrints) == 0 {
		t.Fatal("rows stranded above a full frame were never handed to history")
	}
	if len(m.flush.visible) != 0 {
		t.Fatalf("retained rows = %d, want 0 when the frame has no room", len(m.flush.visible))
	}
}

// TestFlushVisibleOverflowMeasuresWrappedRows covers the other direction: the
// measurement is over wrapped terminal rows, so one long line overflows a short
// terminal without the retained line count ever growing.
func TestFlushVisibleOverflowMeasuresWrappedRows(t *testing.T) {
	m := fixedComposerModel(80, 24)
	m.queueScrollbackPrint(strings.Repeat("wide ", 4_000), 0)
	if len(m.flush.pendingPrints) == 0 {
		t.Fatal("a line wrapping past the terminal height stayed in the frame")
	}
	if len(m.flush.visible)*80 > 24*80 {
		t.Fatalf("retained rows still exceed the terminal: %d lines", len(m.flush.visible))
	}
}

// BenchmarkFlushVisibleOverflowHandingRows is the same call on the frames that
// do overflow, the path that still renders the bottom block to measure it.
func BenchmarkFlushVisibleOverflowHandingRows(b *testing.B) {
	m := benchmarkFrameModel(b, 24, 3)
	m.flush.visible = scrollbackPhysicalLines(strings.Repeat("settled row that must leave\n", 60), 100)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.flushVisibleOverflow()
		if len(m.flush.visible) == 0 {
			b.StopTimer()
			m.flush.visible = scrollbackPhysicalLines(strings.Repeat("settled row that must leave\n", 60), 100)
			m.flush.pendingPrints = nil
			b.StartTimer()
		}
	}
}
