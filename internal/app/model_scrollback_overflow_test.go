package app

import (
	"strings"
	"testing"
)

// TestFlushVisibleOverflowSkipsRowsThatFitAboveTheComposer pins the fast path
// in flushVisibleOverflow: rows that fit above even the shortest bottom block
// stay in the live frame instead of being handed to history.
func TestFlushVisibleOverflowSkipsRowsThatFitAboveTheComposer(t *testing.T) {
	m := fixedComposerModel(80, 24)
	m.queueScrollbackPrint(strings.Join([]string{"row one", "row two", "row three"}, "\n"), 0)
	if len(m.flush.pendingPrints) != 0 {
		t.Fatal("rows that fit the frame were printed into history")
	}
	if got := len(m.flush.visible); got != 3 {
		t.Fatalf("retained rows = %d, want the 3 that still fit", got)
	}
}

// TestFlushVisibleOverflowMeasuresWrappedRows is the other half of that skip:
// the bound is over wrapped terminal rows, not retained lines, so one long line
// still overflows a short terminal.
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
