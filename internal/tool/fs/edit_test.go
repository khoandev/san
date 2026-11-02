package fs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/tool/toolresult"
)

// The tests below exercise Edit through its one payload shape: a "*** SM:EDIT"
// header, then anchored operations. Anything that reaches for the deleted
// remove_from/to or old_string/new_string arguments must fail on shape alone,
// which TestEditRejectsMissingPayload pins.

// editOp renders one FIND/PUT operation for a file.
func editOp(filePath, find, put string) string {
	return "*** SM:EDIT " + filePath + "\n*** SM:FIND\n" + find + "\n*** SM:PUT\n" + put + "\n"
}

// editAllOp renders one FIND/PUT operation whose header asks for every match.
func editAllOp(filePath, find, put string) string {
	return "*** SM:EDIT " + filePath + " all\n*** SM:FIND\n" + find + "\n*** SM:PUT\n" + put + "\n"
}

// runEdit sends a payload the way the model does.
func runEdit(payload, cwd string) toolresult.ToolResult {
	return (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{"payload": payload}, cwd)
}

func editOnce(filePath, find, put, cwd string) toolresult.ToolResult {
	return runEdit(editOp(filePath, find, put), cwd)
}

func TestFileToolsKeepSymlinkAndPermissions(t *testing.T) {
	for _, name := range []string{"Write", "Edit"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target.txt")
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "link.txt")
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			readForEdit(t, link, dir)
			var result toolresult.ToolResult
			if name == "Write" {
				result = (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
					"file_path": link, "content": "replacement", "mode": 0o777,
				}, dir)
			} else {
				result = editOnce(link, "original", "replacement", dir)
			}
			if !result.Success {
				t.Fatal(result.Error)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "replacement" {
				t.Fatalf("target = %q, err = %v", got, err)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("symlink replaced: info = %v, err = %v", info, err)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("permissions = %v, want 0600", info.Mode().Perm())
			}
		})
	}
}

func TestEditAmbiguousAnchorOffersAll(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "rename.go")
	content := "count := 1\nprint(count)\nreturn count\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// An anchor matching three places is refused: the model must narrow it or
	// say it means every match. The error carries the payload that does the
	// latter, so the retry is a copy rather than a rewrite.
	res := editOnce(filePath, "count", "total", tmpDir)
	if res.Success {
		t.Fatalf("ambiguous anchor must not apply, got: %+v", res)
	}
	for _, want := range []string{"ambiguous", "*** SM:EDIT " + filePath + " all", "No operations were applied"} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("ambiguity error should carry %q, got:\n%s", want, res.Error)
		}
	}
	if got, _ := os.ReadFile(filePath); string(got) != content {
		t.Fatalf("ambiguous payload changed the file to %q", got)
	}

	result := runEdit(editAllOp(filePath, "count", "total"), tmpDir)
	if !result.Success {
		t.Fatalf("all-match edit failed: %s", result.Error)
	}
	details, ok := result.Details.(toolresult.FileChangeDetails)
	if !ok || details.EditCount != 1 || details.AddedLines != 3 || details.RemovedLines != 3 {
		t.Fatalf("Edit details = %#v", result.Details)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "total := 1\nprint(total)\nreturn total\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditRejectsNoOp(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "noop.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// A rewrite that lands the same bytes is a mistake worth saying out loud:
	// it usually means the model misremembered what it was changing.
	res := editOnce(filePath, "hello", "hello", tmpDir)
	if res.Success || !strings.Contains(res.Error, "makes no change") {
		t.Fatalf("a no-op edit must be rejected, got: %+v", res)
	}
}

func TestEditToleratesWhitespaceDrift(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "code.go")
	// The file's lines carry trailing spaces the anchor will not restate: the
	// matching ladder folds whitespace before giving up.
	content := "func main() {  \n\tprintln(\"hi\")\t\n}\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	res := editOnce(filePath, "func main() {\n\tprintln(\"hi\")\n}", "func main() {\n\tprintln(\"bye\")\n}", tmpDir)
	if !res.Success {
		t.Fatalf("whitespace-tolerant match should apply, got: %s", res.Error)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "func main() {\n\tprintln(\"bye\")\n}\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditMissNamesClosestRegion(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "code.go")
	content := "func main() {\n\tif ok {\n\t\tprintln(\"hi\")\n\t}\n}\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// A missing anchor is a miss, never a guess; the error locates the region
	// it nearly matched and shows the file's own bytes.
	res := editOnce(filePath, "func handler() {\n\treturn nil\n}", "func handler() {\n\treturn err\n}", tmpDir)
	if res.Success {
		t.Fatal("a missing anchor must not apply")
	}
	for _, want := range []string{"did not match", "No operations were applied", "func main() {"} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("miss error should carry %q, got:\n%s", want, res.Error)
		}
	}
	if got, _ := os.ReadFile(filePath); string(got) != content {
		t.Fatalf("failed edit changed the file to %q", got)
	}
}

func TestEditRequiresReadFirst(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "unread.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := editOnce(filePath, "hello", "goodbye", tmpDir)
	if res.Success || !strings.Contains(res.Error, "has not been read in this session") {
		t.Fatalf("edit without read must be rejected, got: %+v", res)
	}
}

func TestEditStaleViewSoftApply(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "stale.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// External modification after the read. The edit target still matches
	// exactly and uniquely, so it applies — with a warning, not a block.
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := editOnce(filePath, "beta", "BETA", tmpDir)
	if !res.Success {
		t.Fatalf("clean match on a stale view should apply, got: %s", res.Error)
	}
	if !strings.Contains(res.Output, "applied cleanly") || !strings.Contains(res.Output, "changed on disk") {
		t.Fatalf("stale apply should carry the warning note, got: %s", res.Output)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditStaleViewMismatchNamesStaleness(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "stale2.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	if err := os.WriteFile(filePath, []byte("something else entirely\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := editOnce(filePath, "hello", "goodbye", tmpDir)
	if res.Success || !strings.Contains(res.Error, "changed on disk after it was last read") || !strings.Contains(res.Error, "Read the file again") {
		t.Fatalf("stale mismatch should name the staleness and the recovery, got: %+v", res)
	}

	// Re-reading clears the staleness.
	readForEdit(t, filePath, tmpDir)
	if res := editOnce(filePath, "something else", "anything", tmpDir); !res.Success {
		t.Fatalf("edit after re-read should succeed, got: %s", res.Error)
	}
}

func TestResetFileViewsForgetsObservations(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "reset.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// /clear and session switches reset the views: the new conversation has
	// no Read results, so the gate must demand a fresh Read.
	ResetFileViews()
	res := editOnce(filePath, "hello", "goodbye", tmpDir)
	if res.Success || !strings.Contains(res.Error, "has not been read in this session") {
		t.Fatalf("edit after view reset must require a fresh read, got: %+v", res)
	}
}

func TestEditAfterOwnWriteNeedsNoRead(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "hello.txt")

	// The exact flow from live testing: Write a new file, then Edit it
	// repeatedly — no Read anywhere. The tool's own results are the view.
	written := (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "123",
	}, tmpDir)
	if !written.Success {
		t.Fatalf("write failed: %s", written.Error)
	}
	res := editOnce(filePath, "123", "23", tmpDir)
	if !res.Success {
		t.Fatalf("edit after own Write should need no Read, got: %s", res.Error)
	}
	if !strings.Contains(res.Output, "no need to re-read") {
		t.Fatalf("fresh edit result should suppress the verify-read reflex, got: %s", res.Output)
	}
	if res := editOnce(filePath, "23", "5", tmpDir); !res.Success {
		t.Fatalf("edit after own Edit should need no Read, got: %s", res.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "5" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditKeepsOwnWriteFresh(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "chain.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure the second edit happens with a different mtime than the read;
	// only the tool's own view refresh keeps it current.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filePath, old, old); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	if res := editOnce(filePath, "one", "1", tmpDir); !res.Success || strings.Contains(res.Output, "applied cleanly") {
		t.Fatalf("first edit should be a plain fresh apply: %+v", res)
	}
	if res := editOnce(filePath, "two", "2", tmpDir); !res.Success || strings.Contains(res.Output, "applied cleanly") {
		t.Fatalf("second edit after own write should stay current, got: %+v", res)
	}
}

// TestEditRejectsMissingPayload keeps the deleted shapes dead: a call carrying
// anything but a payload fails on shape, so the old arguments cannot linger as
// a silent fallback.
func TestEditRejectsMissingPayload(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "legacy.txt")
	if err := os.WriteFile(filePath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	for name, params := range map[string]map[string]any{
		"old strings": {"file_path": filePath, "old_string": "old", "new_string": "new"},
		"line range":  {"file_path": filePath, "remove_from": "abcde", "remove_to": "abcde", "text": "new"},
		"empty":       {},
	} {
		res := (&EditTool{}).ExecuteApproved(context.Background(), params, tmpDir)
		if res.Success || !strings.Contains(res.Error, "[E_BAD_SHAPE]") {
			t.Fatalf("%s: the removed shape must fail on shape, got: %+v", name, res)
		}
	}
}

func TestEditRejectsMultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		readForEdit(t, filepath.Join(tmpDir, name), tmpDir)
	}

	payload := editOp("a.txt", "one", "1") + editOp("b.txt", "one", "1")
	res := runEdit(payload, tmpDir)
	if res.Success || !strings.Contains(res.Error, "[E_MULTI_FILE]") {
		t.Fatalf("a multi-file payload must be split, got: %+v", res)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if got, _ := os.ReadFile(filepath.Join(tmpDir, name)); string(got) != "one\n" {
			t.Fatalf("%s changed to %q", name, got)
		}
	}
}

func TestEditRejectsDisagreeingFilePin(t *testing.T) {
	tmpDir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		readForEdit(t, filepath.Join(tmpDir, name), tmpDir)
	}

	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": "b.txt",
		"payload":   editOp("a.txt", "one", "1"),
	}, tmpDir)
	if res.Success || !strings.Contains(res.Error, "name different files") {
		t.Fatalf("a file_path disagreeing with the header must be refused, got: %+v", res)
	}
}

func TestEditSyntaxErrorIsDiagnostic(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "diag.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// A malformed payload is answered with the parser's diagnostic — code,
	// payload location and fix — rather than a matching failure.
	res := runEdit("*** SM:EDIT\n*** SM:FIND\nhello\n*** SM:PUT\nbye\n", tmpDir)
	if res.Success {
		t.Fatalf("a payload without a path must fail, got: %+v", res)
	}
	for _, want := range []string{"error[SM1", "help:", "path"} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("diagnostic should carry %q, got:\n%s", want, res.Error)
		}
	}
}

func TestEditAppliesPayloadAtomically(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "atomic.txt")
	content := "alpha\nbeta\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// The first operation matches, the second does not: nothing lands. A
	// partial apply would leave the model's view and the file disagreeing.
	payload := "*** SM:EDIT " + filePath + "\n*** SM:FIND\nalpha\n*** SM:PUT\nALPHA\n" +
		"*** SM:FIND\nmissing line\n*** SM:PUT\nwhatever\n"
	res := runEdit(payload, tmpDir)
	if res.Success || !strings.Contains(res.Error, "No operations were applied") {
		t.Fatalf("a partial payload must apply nothing, got: %+v", res)
	}
	if got, _ := os.ReadFile(filePath); string(got) != content {
		t.Fatalf("file content = %q, want it untouched", got)
	}
}

func TestEditAfterAndSelectionShapes(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "shapes.go")
	content := "func main() {\n\told := 1\n}\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// ⟪old│new⟫ rewrites a fragment in place with no action header.
	res := runEdit("*** SM:EDIT "+filePath+"\n*** SM:FIND\n\t⟪old := 1│name := 1⟫\n", tmpDir)
	if !res.Success {
		t.Fatalf("selection rewrite failed: %s", res.Error)
	}
	// *** SM:AFTER keeps the anchor and inserts the body after it.
	res = runEdit("*** SM:EDIT "+filePath+"\n*** SM:FIND\n\tname := 1\n*** SM:AFTER\n\tname++\n", tmpDir)
	if !res.Success {
		t.Fatalf("after insert failed: %s", res.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "func main() {\n\tname := 1\n\tname++\n}\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditTailorsPreviewToPayload(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "preview.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	payload := editOp(filePath, "alpha", "ALPHA")
	request, err := (&EditTool{}).PreparePermission(context.Background(), map[string]any{"payload": payload}, tmpDir)
	if err != nil {
		t.Fatalf("PreparePermission failed: %v", err)
	}
	if request.FilePath != filePath {
		t.Fatalf("preview file path = %q, want %q", request.FilePath, filePath)
	}
	if request.Description != "Apply 1 anchored edit(s)" {
		t.Fatalf("preview description = %q", request.Description)
	}
	if !strings.Contains(request.DiffMeta.UnifiedDiff, "+ALPHA") {
		t.Fatalf("preview diff should show the change, got:\n%s", request.DiffMeta.UnifiedDiff)
	}

	// The preview and the apply must plan the same edit: the diff the user
	// approves is the change that lands.
	res := runEdit(payload, tmpDir)
	if !res.Success {
		t.Fatalf("approved edit failed: %s", res.Error)
	}
	details, ok := res.Details.(toolresult.FileChangeDetails)
	if !ok || details.UnifiedDiff != request.DiffMeta.UnifiedDiff {
		t.Fatalf("applied diff %q differs from the previewed %q", details.UnifiedDiff, request.DiffMeta.UnifiedDiff)
	}
}

func TestWriteOverwriteRequiresCurrentView(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "target.txt")
	if err := os.WriteFile(filePath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	write := func() toolresult.ToolResult {
		return (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
			"file_path": filePath,
			"content":   "replaced\n",
		}, tmpDir)
	}

	if res := write(); res.Success || !strings.Contains(res.Error, "has not been read in this session") {
		t.Fatalf("overwrite without read must be rejected, got: %+v", res)
	}

	readForEdit(t, filePath, tmpDir)
	res := write()
	if !res.Success {
		t.Fatalf("overwrite after read should succeed, got: %s", res.Error)
	}
	if !strings.Contains(res.Output, "use Edit for modifications") {
		t.Fatalf("overwrite result should nudge toward Edit, got: %s", res.Output)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "replaced\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestWriteNewFileNeedsNoRead(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "fresh.txt")

	res := (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "hello\n",
	}, tmpDir)
	if !res.Success {
		t.Fatalf("creating a new file must not require a read, got: %s", res.Error)
	}
	if strings.Contains(res.Output, "use Edit for modifications") {
		t.Fatalf("creating a new file should not carry the overwrite note, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "no need to re-read") {
		t.Fatalf("create result should suppress the verify-read reflex, got: %s", res.Output)
	}
}
