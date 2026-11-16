package tool

import "testing"

// TestNormalizeReadPath keeps San's Read tool working from the wire schema the
// Zen free tier requires, which spells the path argument `filePath`.
func TestNormalizeReadPath(t *testing.T) {
	input := map[string]any{"filePath": "/tmp/a"}
	normalizeReadPath("Read", input)
	if got := input["file_path"]; got != "/tmp/a" {
		t.Fatalf("file_path = %v, want the wire's filePath", got)
	}
	if got := input["filePath"]; got != "/tmp/a" {
		t.Fatalf("the wire argument was lost: %v", input)
	}

	input = map[string]any{"filePath": "/tmp/a"}
	normalizeReadPath("Write", input)
	if _, ok := input["file_path"]; ok {
		t.Fatalf("a non-Read tool was rewritten: %v", input)
	}

	input = map[string]any{"filePath": "/wire", "file_path": "/san"}
	normalizeReadPath("Read", input)
	if got := input["file_path"]; got != "/san" {
		t.Fatalf("an explicit file_path was overwritten: %v", input)
	}
}
