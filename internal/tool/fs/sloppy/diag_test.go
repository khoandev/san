package sloppy

import (
	"errors"
	"strings"
	"testing"
)

// TestDiagnostics pins every syntax failure to its code, the payload line it
// names, and the byte offset of the span it underlines.
func TestDiagnostics(t *testing.T) {
	tests := []struct {
		name  string
		input string
		code  string
		op    int
		line  int
		col   int
	}{
		{
			name:  "payload without a target",
			input: "just some prose\n",
			code:  codeNoTarget,
			line:  1,
		},
		{
			name:  "bare SM:EDIT",
			input: "*** SM:EDIT\n",
			code:  codeNoPath,
			line:  1,
		},
		{
			name:  "FIND before the target",
			input: "*** SM:FIND\nrun();\n",
			code:  codeNoTarget,
			line:  1,
		},
		{
			name:  "FIND without action",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\nx\n",
			code:  codeNoAction,
			op:    1,
			line:  2,
		},
		{
			name:  "AFTER without anchor",
			input: "*** SM:EDIT a.ts\n*** SM:AFTER\nx\n",
			code:  codeNoAnchor,
			op:    1,
			line:  2,
		},
		{
			name:  "second action header",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\nx\n*** SM:PUT\ny\n*** SM:PUT\nz\n",
			code:  codeDupAction,
			op:    1,
			line:  6,
		},
		{
			name:  "unknown header keyword",
			input: "*** SM:EDIT a.ts\n*** SM:FND\n",
			code:  codeHeaderWord,
			line:  2,
		},
		{
			name:  "argument on an argument-less header",
			input: "*** SM:EDIT a.ts\n*** SM:PUT all\n",
			code:  codeHeaderArg,
			line:  2,
		},
		{
			name:  "two asterisks",
			input: "*** SM:EDIT a.ts\n** SM:FIND\n",
			code:  codeHeaderShape,
			line:  2,
		},
		{
			name:  "no space after the stars",
			input: "*** SM:EDIT a.ts\n***SM:FIND\n",
			code:  codeHeaderShape,
			line:  2,
		},
		{
			name:  "unreadable quoted path",
			input: "*** SM:EDIT \"a.ts\n",
			code:  codeEditPath,
			line:  1,
		},
		{
			name:  "empty quoted path",
			input: "*** SM:EDIT \"\"\n",
			code:  codeEditPath,
			line:  1,
		},
		{
			name:  "target without operations",
			input: "*** SM:EDIT a.ts\n*** SM:EDIT b.ts\n",
			code:  codeNoOps,
			line:  1,
		},
		{
			name:  "stray close marker",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\na ⟫ b\n*** SM:PUT\nc\n",
			code:  codePatternStray,
			op:    1,
			line:  3,
			col:   2,
		},
		{
			name:  "unterminated selection",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\na ⟪b│c\n*** SM:PUT\nd\n",
			code:  codePatternOpen,
			op:    1,
			line:  3,
			col:   2,
		},
		{
			name:  "selection without divider",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\na ⟪b⟫ c\n*** SM:PUT\nd\n",
			code:  codePatternDiv,
			op:    1,
			line:  3,
			col:   2,
		},
		{
			name:  "selection with two dividers",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\na ⟪b│c│d⟫\n",
			code:  codePatternMulti,
			op:    1,
			line:  3,
			col:   2,
		},
		{
			name:  "nested selection",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\na ⟪b⟪c│d⟫\n",
			code:  codePatternNest,
			op:    1,
			line:  3,
			col:   6,
		},
		{
			// The failing line is payload line 5 even though the pattern is
			// assembled from body lines 3 and 4: chrome is dropped, the
			// reported line is the one the model wrote.
			name: "error under read-output numbering",
			input: "*** SM:EDIT a.ts\n*** SM:FIND\n" +
				"1| first\n[2 more lines in a.ts. use read to continue]\n2| a ⟪b⟫\n*** SM:PUT\nc\n",
			code: codePatternDiv,
			op:   1,
			line: 5,
			col:  5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("Parse(%q) error = %v, want a *ParseError", tt.input, err)
			}
			if perr.Code != tt.code {
				t.Errorf("ParseError.Code = %q, want %q", perr.Code, tt.code)
			}
			if perr.Op != tt.op {
				t.Errorf("ParseError.Op = %d, want %d", perr.Op, tt.op)
			}
			if perr.Line != tt.line {
				t.Errorf("ParseError.Line = %d, want %d", perr.Line, tt.line)
			}
			if perr.Col != tt.col {
				t.Errorf("ParseError.Col = %d, want %d", perr.Col, tt.col)
			}
		})
	}
}

// TestDiagnosticReportShape pins the rendered report: the payload line, a
// caret under the span, and one help line. A caret keeps its column under a
// tab-indented line.
func TestDiagnosticReportShape(t *testing.T) {
	_, err := Parse("*** SM:EDIT a.ts\n*** SM:FIND\n\ta ⟪b⟫ c\n*** SM:PUT\nd\n")
	if err == nil {
		t.Fatal("Parse() error = nil, want a *ParseError")
	}
	want := "error[SM203]: selection has no ⟪old│new⟫ divider (operation 1)\n" +
		" --> payload line 3:4\n" +
		"  |\n" +
		"3 | \ta ⟪b⟫ c\n" +
		"  | \t  ^^^ expected one │ between the current and the new text\n" +
		"help: write ⟪old│new⟫; to state whole replacement lines — or to keep a literal │ — " +
		"use a *** SM:PUT block instead."
	if err.Error() != want {
		t.Errorf("Parse() error =\n%s\nwant\n%s", err.Error(), want)
	}
}

// TestDiagnosticReportSuggestion checks the "did you mean" hint and the wide
// gutter a two-digit payload line needs.
func TestDiagnosticReportSuggestion(t *testing.T) {
	input := "*** SM:EDIT a.ts\n" + strings.Repeat("*** SM:FIND\nx\n*** SM:PUT\ny\n", 2) + "*** SM:FND\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("Parse() error = nil, want a *ParseError")
	}
	want := "error[SM106]: unknown header keyword \"FND\"\n" +
		"  --> payload line 10:1\n" +
		"   |\n" +
		"10 | *** SM:FND\n" +
		"   | ^^^^^^^^^^ not a header keyword\n" +
		"help: Did you mean *** SM:FIND? headers are *** SM:EDIT, *** SM:FIND, *** SM:PUT and *** SM:AFTER."
	if err.Error() != want {
		t.Errorf("Parse() error =\n%s\nwant\n%s", err.Error(), want)
	}
}

// TestDiagnosticReportWithoutSuggestion keeps the help line useful when no
// keyword is close enough to guess.
func TestDiagnosticReportWithoutSuggestion(t *testing.T) {
	_, err := Parse("*** SM:EDIT a.ts\n*** SM:UPDATE x\n")
	if err == nil {
		t.Fatal("Parse() error = nil, want a *ParseError")
	}
	want := "error[SM106]: unknown header keyword \"UPDATE\"\n" +
		" --> payload line 2:1\n" +
		"  |\n" +
		"2 | *** SM:UPDATE x\n" +
		"  | ^^^^^^^^^^^^^^^ not a header keyword\n" +
		"help: headers are *** SM:EDIT, *** SM:FIND, *** SM:PUT and *** SM:AFTER."
	if err.Error() != want {
		t.Errorf("Parse() error =\n%s\nwant\n%s", err.Error(), want)
	}
}

// TestHeaderAttemptsStayContent pins the shapes that remain content: an
// *** SM:AFTER body passes every line through, and prose is not a header
// attempt.
func TestHeaderAttemptsStayContent(t *testing.T) {
	sections, err := Parse("*** SM:EDIT a.ts\n*** SM:FIND\nx\n*** SM:AFTER\n*** SM:FND\n* SM:PUT\n")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("len(sections) = %d, want 1", len(sections))
	}
	if len(sections[0].Ops) != 1 {
		t.Fatalf("len(sections[0].Ops) = %d, want 1", len(sections[0].Ops))
	}
	if got, want := sections[0].Ops[0].Rewrite.Text, "*** SM:FND\n* SM:PUT"; got != want {
		t.Errorf("sections[0].Ops[0].Rewrite.Text = %q, want %q", got, want)
	}

	for _, line := range []string{"* SM:FIND", "**bold**", "SM:FIND", "***", "*** not a header", "*** SM:EDIT"} {
		if _, ok := badHeader(line); ok {
			t.Errorf("line %q must stay content, got a header diagnostic", line)
		}
	}
}

// TestClosestHeader covers the suggestion threshold.
func TestClosestHeader(t *testing.T) {
	if got, want := closestHeader("FND"), "FIND"; got != want {
		t.Errorf("closestHeader(%q) = %q, want %q", "FND", got, want)
	}
	if got, want := closestHeader("POT"), "PUT"; got != want {
		t.Errorf("closestHeader(%q) = %q, want %q", "POT", got, want)
	}
	if got, want := closestHeader("after"), "AFTER"; got != want {
		t.Errorf("closestHeader(%q) = %q, want %q", "after", got, want)
	}
	if got, want := closestHeader("Edti"), "EDIT"; got != want {
		t.Errorf("closestHeader(%q) = %q, want %q", "Edti", got, want)
	}
	if got := closestHeader("UPDATE"); got != "" {
		t.Errorf("closestHeader(%q) = %q, want no suggestion", "UPDATE", got)
	}
	if got := closestHeader("ZZZ"); got != "" {
		t.Errorf("closestHeader(%q) = %q, want no suggestion", "ZZZ", got)
	}
}
