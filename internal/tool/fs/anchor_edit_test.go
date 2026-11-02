package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool/toolresult"
)

// anchorRowPattern parses one Read row: line number, anchor, separator, text.
var anchorRowPattern = regexp.MustCompile(`^\s*(\d+) ([a-z]{5})│(.*)$`)

// parseAnchorRows extracts line number → anchor from model-facing text. Anchors
// only ever reach a model through that text, so tests read them from there
// rather than from the store.
func parseAnchorRows(text string) map[int]string {
	anchors := map[int]string{}
	for _, line := range strings.Split(text, "\n") {
		m := anchorRowPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var lineNo int
		fmt.Sscanf(m[1], "%d", &lineNo)
		anchors[lineNo] = m[2]
	}
	return anchors
}

// readRows reads a file and returns the anchors its rows carried.
func readRows(t *testing.T, filePath, cwd string, params map[string]any) (map[int]string, string) {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["file_path"] = filePath
	res := (&ReadTool{}).Execute(context.Background(), params, cwd)
	if !res.Success {
		t.Fatalf("Read failed: %s", res.Error)
	}
	out := res.FormatForLLM()
	anchors := parseAnchorRows(out)
	if len(anchors) != len(res.Lines) {
		t.Fatalf("read %d rows but parsed %d anchors from:\n%s", len(res.Lines), len(anchors), out)
	}
	return anchors, out
}

func runAnchorEdit(cwd, filePath, from, to, text string) toolresult.ToolResult {
	return (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path":   filePath,
		"remove_from": from,
		"remove_to":   to,
		"text":        text,
	}, cwd)
}

func TestReadRowsCarryDistinctAnchors(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "rows.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, out := readRows(t, filePath, dir, nil)
	if len(anchors) != 3 {
		t.Fatalf("expected 3 anchored rows, got:\n%s", out)
	}
	if !strings.Contains(out, "    1 "+anchors[1]+"│alpha") {
		t.Fatalf("row format is not \"line number, anchor, │, content\":\n%s", out)
	}
	seen := map[string]int{}
	for lineNo, anchor := range anchors {
		if len(anchor) != AnchorLen {
			t.Fatalf("line %d anchor %q is %d chars", lineNo, anchor, len(anchor))
		}
		if other, dup := seen[anchor]; dup {
			t.Fatalf("lines %d and %d share anchor %q", other, lineNo, anchor)
		}
		seen[anchor] = lineNo
	}
}

func TestReadRepeatsTheSameAnchorsForUnchangedLines(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "stable.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, _ := readRows(t, filePath, dir, nil)
	again, _ := readRows(t, filePath, dir, map[string]any{"offset": 1})
	if len(first) != len(again) {
		t.Fatalf("second read changed the row count: %v vs %v", first, again)
	}
	for lineNo, anchor := range first {
		if again[lineNo] != anchor {
			t.Fatalf("line %d anchor changed between reads: %q then %q", lineNo, anchor, again[lineNo])
		}
	}
}

func TestAnchorEditReplacesRange(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "range.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, _ := readRows(t, filePath, dir, nil)
	res := runAnchorEdit(dir, filePath, anchors[2], anchors[3], "BETA\nGAMMA\nEXTRA")
	if !res.Success {
		t.Fatalf("anchored edit failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "lines 2-3 replaced with 3 line(s)") {
		t.Fatalf("result should name the range it replaced, got: %s", res.Output)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\nBETA\nGAMMA\nEXTRA\ndelta\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestAnchorEditSingleLineAndDeletion(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "single.txt")
	if err := os.WriteFile(filePath, []byte("keep1\nchange\nkeep2\nremove me\ntail\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, _ := readRows(t, filePath, dir, nil)
	if res := runAnchorEdit(dir, filePath, anchors[2], anchors[2], "changed"); !res.Success {
		t.Fatalf("single-line anchored edit failed: %s", res.Error)
	}
	if res := runAnchorEdit(dir, filePath, anchors[4], anchors[4], ""); !res.Success {
		t.Fatalf("anchored deletion failed: %s", res.Error)
	}

	got, _ := os.ReadFile(filePath)
	if string(got) != "keep1\nchanged\nkeep2\ntail\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestAnchorEditServesFreshAnchorsForChainedEdits(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "chain.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, _ := readRows(t, filePath, dir, nil)
	res := runAnchorEdit(dir, filePath, anchors[2], anchors[2], "TWO")
	if !res.Success {
		t.Fatalf("first edit failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Fresh anchors for the lines just written") {
		t.Fatalf("edit should hand back anchors for the lines it wrote, got: %s", res.Output)
	}

	// The freshly served row is anchored at line 2 and must be editable without
	// a Read.
	fresh := parseAnchorRows(res.Output)[2]
	if fresh == "" {
		t.Fatalf("could not parse the fresh anchor from: %s", res.Output)
	}
	if second := runAnchorEdit(dir, filePath, fresh, fresh, "TWO AGAIN"); !second.Success {
		t.Fatalf("chained anchored edit should not need a Read, got: %s", second.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "one\nTWO AGAIN\nthree\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestAnchorEditKeepsAnchorsOutsideTheRange(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "spread.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, _ := readRows(t, filePath, dir, nil)
	if res := runAnchorEdit(dir, filePath, anchors[2], anchors[2], "TWO\nINSERTED"); !res.Success {
		t.Fatalf("insert failed: %s", res.Error)
	}
	// "five" moved down a line but its anchor still names it: an edit of a part
	// of the file does not invalidate handles for the rest.
	res := runAnchorEdit(dir, filePath, anchors[5], anchors[5], "FIVE")
	if !res.Success {
		t.Fatalf("edit after a shift should still resolve, got: %s", res.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "one\nTWO\nINSERTED\nthree\nfour\nFIVE\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestAnchorEditRejectsStaleAnchor(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, _ := readRows(t, filePath, dir, nil)
	if err := os.WriteFile(filePath, []byte("rewritten\nby\nsomeone\nelse\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runAnchorEdit(dir, filePath, anchors[2], anchors[2], "TWO")
	if res.Success {
		t.Fatal("an anchor minted before the file changed must not apply")
	}
	if !strings.Contains(res.Error, "E_STALE_ANCHOR") || !strings.Contains(res.Error, "Read "+filePath+" again") {
		t.Fatalf("stale anchor error should name the anchor and the recovery, got: %s", res.Error)
	}
}

func TestAnchorEditRefusesLineChangedUnderTheSameStamp(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "shadow.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	anchors, _ := readRows(t, filePath, dir, nil)

	// A same-length rewrite with the mtime put back leaves mtime and size
	// untouched: the served row's fingerprint is the only thing that can tell
	// the model is looking at text that no longer exists.
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("ONE\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filePath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	res := runAnchorEdit(dir, filePath, anchors[1], anchors[1], "uno")
	if res.Success {
		t.Fatal("an edit of a line that changed since it was shown must be refused")
	}
	if !strings.Contains(res.Error, "E_RANGE_STALE") {
		t.Fatalf("expected E_RANGE_STALE, got: %s", res.Error)
	}

	// The rejection carries the current rows, so the retry needs no Read.
	fresh := parseAnchorRows(res.Error)[1]
	if fresh == "" {
		t.Fatalf("stale range error should serve the current rows, got: %s", res.Error)
	}
	if retry := runAnchorEdit(dir, filePath, fresh, fresh, "uno"); !retry.Success {
		t.Fatalf("retry with the anchors from the error should apply, got: %s", retry.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "uno\ntwo\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestAnchorEditRefusesLineTooLongToShow(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "long.txt")
	longLine := strings.Repeat("x", maxLineLen+10)
	if err := os.WriteFile(filePath, []byte("short\n"+longLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, out := readRows(t, filePath, dir, nil)
	if !strings.Contains(out, lineTruncationMarker) {
		t.Fatalf("expected the long line to be cut with the marker, got:\n%s", out)
	}
	res := runAnchorEdit(dir, filePath, anchors[1], anchors[2], "replacement")
	if res.Success {
		t.Fatal("a line Read could not show in full must not be replaced blind")
	}
	if !strings.Contains(res.Error, "E_LINE_TOO_LONG") {
		t.Fatalf("expected E_LINE_TOO_LONG, got: %s", res.Error)
	}
}

func TestAnchorEditStripsPastedRow(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "paste.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, out := readRows(t, filePath, dir, nil)
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, anchors[2]) {
			row = strings.TrimRight(line, " ")
		}
	}
	res := runAnchorEdit(dir, filePath, row, row, "TWO")
	if !res.Success {
		t.Fatalf("a pasted Read row should be repaired, got: %s", res.Error)
	}
	if !strings.Contains(res.Output, "W_BAD_REF") {
		t.Fatalf("the repair should be reported, got: %s", res.Output)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "one\nTWO\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestAnchorEditReversedRange(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "reversed.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, _ := readRows(t, filePath, dir, nil)
	res := runAnchorEdit(dir, filePath, anchors[3], anchors[1], "X")
	if !res.Success {
		t.Fatalf("reversed anchors should be understood, got: %s", res.Error)
	}
	if !strings.Contains(res.Output, "H_RANGE_REVERSED") {
		t.Fatalf("the reversal should be reported, got: %s", res.Output)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "X\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditRejectsMixedShapes(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "mixed.txt")
	if err := os.WriteFile(filePath, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	anchors, _ := readRows(t, filePath, dir, nil)

	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path":   filePath,
		"remove_from": anchors[1],
		"remove_to":   anchors[1],
		"text":        "uno",
		"old_string":  "one",
		"new_string":  "uno",
	}, dir)
	if res.Success || !strings.Contains(res.Error, "one shape per call") {
		t.Fatalf("a call carrying both shapes must be refused, got: %+v", res)
	}
}

func TestAnchorEditRejectsBadAnchor(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(filePath, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readRows(t, filePath, dir, nil)

	for _, ref := range []string{"qwerty", "12", "QWER T"} {
		res := runAnchorEdit(dir, filePath, ref, ref, "x")
		if res.Success || !strings.Contains(res.Error, "E_BAD_REF") {
			t.Fatalf("anchor %q should be refused with E_BAD_REF, got: %+v", ref, res)
		}
	}
	// An omitted anchor is a shape error, not a malformed reference.
	if res := runAnchorEdit(dir, filePath, "", "", "x"); res.Success || !strings.Contains(res.Error, "E_BAD_SHAPE") {
		t.Fatalf("a missing anchor should be refused with E_BAD_SHAPE, got: %+v", res)
	}
}

func TestResetFileViewsDropsAnchors(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "reset.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	anchors, _ := readRows(t, filePath, dir, nil)

	ResetFileViews()
	// The next Read mints a new generation of anchors; the ones the previous
	// conversation was given must not resolve against it.
	readRows(t, filePath, dir, nil)

	res := runAnchorEdit(dir, filePath, anchors[1], anchors[1], "uno")
	if res.Success || !strings.Contains(res.Error, "E_STALE_ANCHOR") {
		t.Fatalf("anchors must not survive a conversation reset, got: %+v", res)
	}
}

func TestAnchorEditPreservesCrlfAndBom(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "crlf.txt")
	if err := os.WriteFile(filePath, []byte("\ufeffone\r\ntwo\r\nthree\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	anchors, _ := readRows(t, filePath, dir, nil)
	if res := runAnchorEdit(dir, filePath, anchors[2], anchors[2], "TWO"); !res.Success {
		t.Fatalf("anchored edit failed: %s", res.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "\ufeffone\r\nTWO\r\nthree\r\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestChangedLineRange(t *testing.T) {
	cases := []struct {
		name         string
		old, new     string
		first, last  int
		replacements int
		ok           bool
	}{
		{name: "identical", old: "a\nb\n", new: "a\nb\n", ok: false},
		{name: "one line", old: "a\nb\nc\n", new: "a\nB\nc\n", first: 2, last: 2, replacements: 1, ok: true},
		{name: "insert", old: "a\nc\n", new: "a\nb\nc\n", first: 2, last: 1, replacements: 1, ok: true},
		{name: "delete", old: "a\nb\nc\n", new: "a\nc\n", first: 2, last: 2, replacements: 0, ok: true},
		{name: "from empty", old: "", new: "a\nb\n", first: 1, last: 0, replacements: 2, ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, last, replacements, ok := changedLineRange(tc.old, tc.new)
			if ok != tc.ok || first != tc.first || last != tc.last || replacements != tc.replacements {
				t.Fatalf("changedLineRange(%q, %q) = (%d, %d, %d, %v), want (%d, %d, %d, %v)",
					tc.old, tc.new, first, last, replacements, ok, tc.first, tc.last, tc.replacements, tc.ok)
			}
		})
	}
}
