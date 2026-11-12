package workflow

import (
	"strings"
	"testing"
)

// withMarks installs m as the mark source for one test and restores the default
// literals afterwards. The source is package state, so no test using it may run
// in parallel.
func withMarks(t *testing.T, m Marks) {
	t.Helper()
	SetMarkSource(func() Marks { return m })
	t.Cleanup(func() { SetMarkSource(nil) })
}

func TestDefaultMarksStandWithoutASource(t *testing.T) {
	SetMarkSource(nil)
	for _, tt := range []struct {
		status Status
		want   string
	}{
		{StatusRunning, "●"},
		{StatusPending, "○"},
		{StatusSucceeded, "✓"},
		{StatusFailed, "✗"},
		{StatusSkipped, "↷"},
		{StatusOmitted, "⊘"},
	} {
		if got := statusGlyph(tt.status); got != tt.want {
			t.Fatalf("statusGlyph(%s) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// TestMarkSourceDrivesEveryStatusGlyph is the guard that a status the caller did
// not override still draws its default: the installed source names only what the
// TUI can draw itself, and the workflow-only marks stay put.
func TestMarkSourceDrivesEveryStatusGlyph(t *testing.T) {
	withMarks(t, Marks{Running: "•", Pending: "◦", Succeeded: "+", Failed: "x"})
	for _, tt := range []struct {
		status Status
		want   string
	}{
		{StatusRunning, "•"},
		{StatusPending, "◦"},
		{StatusSucceeded, "+"},
		{StatusFailed, "x"},
		{StatusSkipped, "↷"},
		{StatusOmitted, "⊘"},
	} {
		if got := statusGlyph(tt.status); got != tt.want {
			t.Fatalf("statusGlyph(%s) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// TestCompactViewDrawsTheInstalledMarks covers the graph, which is the part of
// the live view the TUI does not post-process: without the source it would keep
// drawing the old, larger marks beside the conversation's new ones.
func TestCompactViewDrawsTheInstalledMarks(t *testing.T) {
	w, err := Parse("---\nname: audit\n---\n```mermaid\nflowchart LR\n  map --> bugs & risk & lean --> report\n```\n\n## map\nmode: explore\n\nMap\n\n## bugs\nmode: explore\n\nBugs\n\n## risk\nmode: explore\n\nRisk\n\n## lean\nmode: explore\n\nLean\n\n## report\nmode: explore\n\nReport\n")
	if err != nil {
		t.Fatal(err)
	}
	withMarks(t, Marks{Running: "•", Pending: "◦", Succeeded: "+"})
	got := w.CompactProgressView(map[string]Status{"map": StatusSucceeded, "bugs": StatusRunning}, 100)
	for _, want := range []string{"map + ─", "bugs •", "risk ◦"} {
		if !strings.Contains(got, want) {
			t.Fatalf("graph does not draw %q:\n%s", want, got)
		}
	}
}

// TestActivityStreamDrawsTheInstalledRunningMark keeps the activity stream on
// the same marks as the graph, so one live view never mixes two sizes.
func TestActivityStreamDrawsTheInstalledRunningMark(t *testing.T) {
	withMarks(t, Marks{Running: "•", Succeeded: "+", Failed: "x"})
	w := &Workflow{}
	for _, tt := range []struct {
		event ActivityEvent
		want  string
	}{
		{ActivityEvent{Node: "map", ToolID: "t1", Text: "Bash(ls)"}, "• Bash(ls)"},
		{ActivityEvent{Node: "map", ToolID: "t1", Text: "Bash(ls)", ToolFinished: true}, "+ Bash(ls)"},
		{ActivityEvent{Node: "map", ToolID: "t1", Text: "Bash(ls)", ToolFinished: true, ToolFailed: true}, "x Bash(ls)"},
	} {
		got := w.ActivityStreamView([]ActivityEvent{tt.event}, 80)
		if !strings.Contains(got, tt.want) {
			t.Fatalf("activity stream does not draw %q:\n%s", tt.want, got)
		}
	}
}
