package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The benchmarks below exist to keep the two Edit shapes honest against each
// other. Anchored editing is the default, so it must not be the slower path: a
// model that reaches for it should not pay for the safety it buys.

const benchLineCount = 2000

func benchContent(lines int) string {
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&sb, "func handler%04d(ctx context.Context, req *Request) (*Response, error) {\n", i)
	}
	return sb.String()
}

// benchAnchorRows reads a file and returns line number → anchor without going
// through a regexp: a benchmark helper must not dominate what it measures.
func benchAnchorRows(b *testing.B, filePath, dir string) map[int]string {
	b.Helper()
	res := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, dir)
	if !res.Success {
		b.Fatalf("Read failed: %s", res.Error)
	}
	anchors := make(map[int]string, len(res.Lines))
	for _, line := range res.Lines {
		anchors[line.LineNo] = line.Hash
	}
	if len(anchors) != benchLineCount {
		b.Fatalf("read %d rows, want %d", len(anchors), benchLineCount)
	}
	return anchors
}

// benchmarkWarmEdit times one Edit call against a file the model has already
// read: the model's steady state, where it edits line after line without
// re-reading between calls.
func benchmarkWarmEdit(b *testing.B, anchored bool) {
	b.Helper()
	dir := b.TempDir()
	filePath := filepath.Join(dir, "bench.go")
	if err := os.WriteFile(filePath, []byte(benchContent(benchLineCount)), 0o644); err != nil {
		b.Fatal(err)
	}
	ResetFileViews()
	anchors := benchAnchorRows(b, filePath, dir)
	const target = "func handler1000(ctx context.Context, req *Request) (*Response, error) { // edited"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		line := 1 + i%(benchLineCount/2)
		var res = struct {
			ok  bool
			err string
		}{}
		if anchored {
			r := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
				"file_path":   filePath,
				"remove_from": anchors[line],
				"remove_to":   anchors[line],
				"text":        target,
			}, dir)
			res.ok, res.err = r.Success, r.Error
		} else {
			r := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
				"file_path":  filePath,
				"old_string": fmt.Sprintf("func handler%04d(ctx context.Context, req *Request) (*Response, error) {", line-1),
				"new_string": target,
			}, dir)
			res.ok, res.err = r.Success, r.Error
		}
		if !res.ok {
			b.Fatalf("edit failed: %s", res.err)
		}
	}
}

func BenchmarkEditAnchored(b *testing.B) { benchmarkWarmEdit(b, true) }
func BenchmarkEditLegacy(b *testing.B)   { benchmarkWarmEdit(b, false) }

// BenchmarkReadThenEdit times the whole per-change cycle a model pays for when
// it must look before it changes: Read the file, then edit one line in it.
func BenchmarkReadThenEdit(b *testing.B) {
	for _, anchored := range []bool{true, false} {
		name := "legacy"
		if anchored {
			name = "anchored"
		}
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			filePath := filepath.Join(dir, "bench.go")
			content := benchContent(benchLineCount)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
					b.Fatal(err)
				}
				ResetFileViews()
				b.StartTimer()

				read := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, dir)
				if !read.Success {
					b.Fatalf("Read failed: %s", read.Error)
				}
				params := map[string]any{"file_path": filePath}
				if anchored {
					anchors := make(map[int]string, len(read.Lines))
					for _, line := range read.Lines {
						anchors[line.LineNo] = line.Hash
					}
					params["remove_from"] = anchors[1000]
					params["remove_to"] = anchors[1000]
					params["text"] = "func handler1000() { // edited"
				} else {
					params["old_string"] = "func handler1000(ctx context.Context, req *Request) (*Response, error) {"
					params["new_string"] = "func handler1000() { // edited"
				}
				if res := (&EditTool{}).ExecuteApproved(context.Background(), params, dir); !res.Success {
					b.Fatalf("edit failed: %s", res.Error)
				}
			}
		})
	}
}

// TestEditRequestPayloadSize pins the property that motivates anchored editing
// in the first place: the call a model has to emit does not grow with the text
// it replaces. Legacy editing must quote every removed line; anchored editing
// quotes two 5-letter handles.
func TestEditRequestPayloadSize(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "payload.go")
	content := benchContent(200)
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	read := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, dir)
	if !read.Success {
		t.Fatalf("Read failed: %s", read.Error)
	}
	anchors := make(map[int]string, len(read.Lines))
	for _, line := range read.Lines {
		anchors[line.LineNo] = line.Hash
	}

	const start = 50
	// Two sizes: one a model writes by hand, one it would not. The anchored
	// call is the same size for both while the legacy call grows with the range
	// — that difference, not client CPU, is what the model pays for.
	lines := splitFileLines(content)
	for _, rangeLines := range []int{5, 40} {
		oldText := strings.Join(lines[start-1:start-1+rangeLines], "\n") + "\n"
		legacy := len(fmt.Sprintf(`{"file_path":%q,"old_string":%q,"new_string":%q}`, filePath, oldText, "replacement"))
		anchored := len(fmt.Sprintf(`{"file_path":%q,"remove_from":%q,"remove_to":%q,"text":%q}`, filePath, anchors[start], anchors[start+rangeLines-1], "replacement"))

		if anchored >= legacy/2 {
			t.Fatalf("anchored payload %d bytes should be far below the legacy %d bytes for a %d-line replacement", anchored, legacy, rangeLines)
		}
		t.Logf("replacing %d lines: anchored request %d bytes, legacy request %d bytes (%.1fx)", rangeLines, anchored, legacy, float64(legacy)/float64(anchored))
	}

	// The anchored call must actually be accepted for the range.
	const rangeLines = 5
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path":   filePath,
		"remove_from": anchors[start],
		"remove_to":   anchors[start+rangeLines-1],
		"text":        "replacement",
	}, dir)
	if !res.Success {
		t.Fatalf("anchored edit failed: %s", res.Error)
	}
}
