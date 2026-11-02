package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool/fs/sloppy"
	"github.com/genai-io/san/internal/tool/fs/sloppyedit"
)

// The benchmarks below keep Edit's cost honest: a payload is parsed, matched
// against the file, applied, diffed and written, and the first three of those
// must stay negligible next to the file I/O the tool cannot avoid.

const benchLineCount = 2000

// benchFile is the size of a small file: a typical edit target, where the
// engine's whole-file normalization is cheap.
const benchSmallFile = 200

// benchContent builds a Go-shaped file with one function in the middle, so a
// benchmark patch can anchor on its signature and its closing brace.
func benchContent(body int) string { return benchFileOf(body, benchLineCount) }

// benchFileOf builds a file of about `total` lines around a function body. The
// engine normalizes the whole file on every payload, so file size — not body
// size — is what its cost follows.
func benchFileOf(body, total int) string {
	var sb strings.Builder
	filler := (total - body - 2) / 2
	for i := 0; i < filler; i++ {
		fmt.Fprintf(&sb, "var value%04d = compute(%d)\n", i, i)
	}
	sb.WriteString("func target(ctx context.Context) error {\n")
	for i := 0; i < body; i++ {
		fmt.Fprintf(&sb, "\tvalue%04d := compute(%d)\n", i, i)
	}
	sb.WriteString("}\n")
	for i := 0; i < filler; i++ {
		fmt.Fprintf(&sb, "var tail%04d = compute(%d)\n", i, i)
	}
	return sb.String()
}

// benchEditPayload is the payload a model sends to rename target and keep its
// body: two anchored lines and one elided run, whatever the body length.
func benchEditPayload(filePath string, body int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*** SM:EDIT %s\n", filePath)
	sb.WriteString("*** SM:FIND\nfunc target(ctx context.Context) error {\n…\n}\n")
	sb.WriteString("*** SM:PUT\nfunc target(ctx context.Context) error { // renamed\n…\n}\n")
	return sb.String()
}

// benchPrepared returns a temp file with the given body length inside a file of
// about `total` lines, read once so Edit's "requires a current view" gate is
// satisfied.
func benchPrepared(b *testing.B, body, total int) (string, string) {
	b.Helper()
	dir := b.TempDir()
	filePath := filepath.Join(dir, "bench.go")
	if err := os.WriteFile(filePath, []byte(benchFileOf(body, total)), 0o644); err != nil {
		b.Fatal(err)
	}
	ResetFileViews()
	read := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, dir)
	if !read.Success {
		b.Fatalf("Read failed: %s", read.Error)
	}
	return dir, filePath
}

// benchmarkEdit times one Edit call against a file the model has already read:
// the model's steady state, where it edits without re-reading between calls.
func benchmarkEdit(b *testing.B, body, total int) {
	dir, filePath := benchPrepared(b, body, total)
	payload := benchEditPayload(filePath, body)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// The first call renames the function and later calls find the same
		// anchor line, so the measurement is the match/apply path, not a
		// growing file.
		if res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{"payload": payload}, dir); !res.Success {
			b.Fatalf("edit failed: %s", res.Error)
		}
	}
}

func BenchmarkEditPayload(b *testing.B)          { benchmarkEdit(b, 40, benchLineCount) }
func BenchmarkEditPayloadLargeBody(b *testing.B) { benchmarkEdit(b, 400, benchLineCount) }
func BenchmarkEditPayloadSmallFile(b *testing.B) { benchmarkEdit(b, 40, benchSmallFile) }

// BenchmarkApplyOnly isolates the engine from the tool: parse, match, apply and
// splice, without reading, diffing or writing the file. It is the part of an
// edit a payload shape can make faster or slower, so it is the number to watch
// when the matching ladder changes.
func BenchmarkApplyOnly(b *testing.B) {
	content := benchContent(400)
	sections, err := sloppy.Parse(benchEditPayload("bench.go", 400))
	if err != nil {
		b.Fatalf("parse failed: %v", err)
	}
	if len(sections) != 1 {
		b.Fatalf("got %d sections, want 1", len(sections))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := sloppyedit.Apply(content, sections[0], false); err != nil {
			b.Fatalf("apply failed: %v", err)
		}
	}
}

// BenchmarkReadThenEdit times the whole per-change cycle a model pays for when
// it must look before it changes: Read the file, then edit one line in it.
func BenchmarkReadThenEdit(b *testing.B) {
	dir := b.TempDir()
	filePath := filepath.Join(dir, "bench.go")
	content := benchContent(40)
	payload := benchEditPayload(filePath, 40)
	b.ReportAllocs()
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
		if res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{"payload": payload}, dir); !res.Success {
			b.Fatalf("edit failed: %s", res.Error)
		}
	}
}

// TestEditPayloadSize pins the property that motivates an anchored patch: the
// call a model must emit does not grow with the text it keeps. Restating a
// 40-line function costs 40 lines twice (anchor and replacement); anchoring its
// signature and closing brace costs the same call at any body length.
func TestEditPayloadSize(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "payload.go")
	if err := os.WriteFile(filePath, []byte(benchContent(40)), 0o644); err != nil {
		t.Fatal(err)
	}
	read := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, dir)
	if !read.Success {
		t.Fatalf("Read failed: %s", read.Error)
	}

	elided := len(benchEditPayload(filePath, 40))
	full := len(benchFullQuotePayload(filePath, 40))
	if elided >= full/2 {
		t.Fatalf("elided payload %d bytes should be far below the full-quote payload %d bytes", elided, full)
	}
	t.Logf("keeping a 42-line function: anchored payload %d bytes, full quote %d bytes (%.1fx)", elided, full, float64(full)/float64(elided))

	// Both payloads must actually apply.
	for name, payload := range map[string]string{"anchored": benchEditPayload(filePath, 40), "full": benchFullQuotePayload(filePath, 40)} {
		if name == "full" {
			if err := os.WriteFile(filePath, []byte(benchContent(40)), 0o644); err != nil {
				t.Fatal(err)
			}
			if read := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, dir); !read.Success {
				t.Fatalf("Read failed: %s", read.Error)
			}
		}
		if res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{"payload": payload}, dir); !res.Success {
			t.Fatalf("%s payload failed: %s", name, res.Error)
		}
	}
}

// benchFullQuotePayload restates the whole function in both the anchor and the
// replacement, which is what a model had to do before anchors existed.
func benchFullQuotePayload(filePath string, body int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*** SM:EDIT %s\n", filePath)
	sb.WriteString("*** SM:FIND\nfunc target(ctx context.Context) error {\n")
	for i := 0; i < body; i++ {
		fmt.Fprintf(&sb, "\tvalue%04d := compute(%d)\n", i, i)
	}
	sb.WriteString("}\n*** SM:PUT\nfunc target(ctx context.Context) error { // renamed\n")
	for i := 0; i < body; i++ {
		fmt.Fprintf(&sb, "\tvalue%04d := compute(%d)\n", i, i)
	}
	sb.WriteString("}\n")
	return sb.String()
}
