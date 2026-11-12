package task

import (
	"context"
	"testing"
)

func TestAgentTaskReportsItsModel(t *testing.T) {
	worker := NewAgentTask("w1", "task", "Fix build", context.Background(), nil, "")
	if got := worker.GetStatus().Model; got != "" {
		t.Fatalf("a launch with no model reported %q", got)
	}
	worker.SetModel("test-model")
	if got := worker.GetStatus().Model; got != "test-model" {
		t.Fatalf("model = %q, want test-model", got)
	}
	// Repeating the same model is not a second launch fact.
	worker.SetModel("test-model")
	if got := worker.GetStatus().Model; got != "test-model" {
		t.Fatalf("model = %q after a repeat", got)
	}
}
