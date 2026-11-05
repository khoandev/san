package input

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/task"
)

// A finished task a review refused must not read as a plain completion: the
// list row and the detail both say so, and both still name the review.
func TestTaskViewerShowsNeedsReview(t *testing.T) {
	var v TaskViewer
	v.Show([]TaskViewItem{{
		ID: "t1", Subject: "Audit the auth path", Status: task.StatusCompleted,
		NeedsReview: "P0", ReviewID: "review-abc123",
	}}, 100, 24)

	plain := ansi.Strip(v.Render())
	if !strings.Contains(plain, "completed · needs review P0") {
		t.Fatalf("list row hides the refusal:\n%s", plain)
	}

	v.HandleKeypress(taskKey("enter"))
	detail := ansi.Strip(v.Render())
	if !strings.Contains(detail, "Review: review-abc123 (P0)") {
		t.Fatalf("detail does not name the review:\n%s", detail)
	}
}

// A task no review objected to keeps the plain status: the flag is an event,
// not decoration on every finished row.
func TestTaskViewerOmitsReviewWhenClean(t *testing.T) {
	var v TaskViewer
	v.Show([]TaskViewItem{{ID: "t1", Subject: "Audit the auth path", Status: task.StatusCompleted}}, 100, 24)
	plain := ansi.Strip(v.Render())
	if strings.Contains(plain, "needs review") {
		t.Fatalf("a clean completion claims a review:\n%s", plain)
	}
}
