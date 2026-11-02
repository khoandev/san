package sloppyedit

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool/fs/sloppy"
)

// applyOne runs a single-file payload against content.
func applyOne(t *testing.T, content, payload string) (Result, error) {
	t.Helper()
	sections, err := sloppy.Parse(payload)
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("payload parsed into %d sections, want 1", len(sections))
	}
	return Apply(content, sections[0], true)
}

func mustApply(t *testing.T, content, payload string) Result {
	t.Helper()
	result, err := applyOne(t, content, payload)
	if err != nil {
		t.Fatalf("apply payload: %v", err)
	}
	return result
}

// requireContains fails the test when got does not contain want.
func requireContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%q does not contain %q", got, want)
	}
}

func TestAppliesFindPutPair(t *testing.T) {
	result := mustApply(t,
		"const timeout = 1000;\nstart();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nconst timeout = 1000;\n*** SM:PUT\nconst timeout = 5000;\n")
	if want := "const timeout = 5000;\nstart();\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestEmptyPutDeletesTheMatch(t *testing.T) {
	result := mustApply(t,
		"a = 1;\nrun();\nb = 2;\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nrun();\n*** SM:PUT\n")
	if want := "a = 1;\nb = 2;\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
	if len(result.Notes) != 1 {
		t.Fatalf("notes = %q, want 1 note", result.Notes)
	}
	requireContains(t, result.Notes[0], "deleted")
}

func TestGapsCaptureAndReplayThroughPut(t *testing.T) {
	result := mustApply(t,
		"function legacy(a) {\n  stage(a);\n  commit(a);\n}\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nfunction legacy(a) {\n…\n}\n*** SM:PUT\nfunction modern(a) {\n…\n}\n")
	if want := "function modern(a) {\n  stage(a);\n  commit(a);\n}\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}

	sparse := mustApply(t,
		"function loadUser(id){\n\tconst user = legacyStore.read(id);\n\treturn user;\n}\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nfunction loadUser(…){\n\tconst user = legacyStore.read(…);\n…\n"+
			"*** SM:PUT\nfunction loadUser(…){\n\tconst user = await database.users.read(…);\n"+
			"\tif (!user) throw new MissingUserError(id);\n…\n")
	if want := "function loadUser(id){\n\tconst user = await database.users.read(id);\n" +
		"\tif (!user) throw new MissingUserError(id);\n\treturn user;\n}\n"; sparse.Content != want {
		t.Errorf("content = %q, want %q", sparse.Content, want)
	}
}

func TestMidLineGapStaysLineBounded(t *testing.T) {
	_, err := applyOne(t, "call(first,\nsecond);\nother();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\ncall(…second);\n*** SM:PUT\ncall(…third);\n")
	if err == nil {
		t.Fatal("a mid-line gap must not swallow the line break")
	}

	result := mustApply(t,
		"call(first, second);\nother();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\ncall(…second);\n*** SM:PUT\ncall(…third);\n")
	if want := "call(first, third);\nother();\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestEdgeGapsReplayAsNothingAroundAnInnerCapture(t *testing.T) {
	result := mustApply(t,
		"before();\nfunction legacy(a) {\n  stage(a);\n}\nafter();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\n…\nfunction legacy(a) {\n…\n}\n…\n"+
			"*** SM:PUT\n…\nfunction modern(a) {\n…\n}\n…\n")
	if want := "before();\nfunction modern(a) {\n  stage(a);\n}\nafter();\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestWholeLineEdgeGapKeepsItsJoiningNewline(t *testing.T) {
	result := mustApply(t,
		"before();\nconst value = old;\nafter();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nconst value = old;\n…\n*** SM:PUT\nconst value = new;\n")
	if want := "before();\nconst value = new;\nafter();\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestAllRewritesEveryMatch(t *testing.T) {
	result := mustApply(t,
		"logger.debug(\nlogger.debug(\nkeep();\n",
		"*** SM:EDIT a.ts all\n*** SM:FIND\nlogger.debug(\n*** SM:PUT\nlogger.trace(\n")
	if want := "logger.trace(\nlogger.trace(\nkeep();\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestAllWithZeroMatchesFails(t *testing.T) {
	_, err := applyOne(t, "keep();\n",
		"*** SM:EDIT a.ts all\n*** SM:FIND\nlogger.debug(\n*** SM:PUT\nlogger.trace(\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "found 0 matches")
	requireContains(t, err.Error(), atomicityNotice)
}

func TestAmbiguousMatchFailsWithCopyReadyRetries(t *testing.T) {
	_, err := applyOne(t, "item\nitem\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\nitem\n*** SM:AFTER\nitem\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "is ambiguous")
	requireContains(t, err.Error(), "*** SM:EDIT a.txt all")
}

func TestWhitespaceEquivalentOutcomesAreUnambiguous(t *testing.T) {
	result := mustApply(t, "open() {\n  work(unit);\n\n  work(unit);\n}\n",
		"*** SM:EDIT a.ts\n⟪  work(unit);\n│⟫\n")
	if want := "open() {\n  work(unit);\n}\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestNormalizedMatchToleratesIndentationDrift(t *testing.T) {
	result := mustApply(t, "function run() {\n\tsecond();\t\n}\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\nfunction run() {\n    second();\n*** SM:AFTER\n\tthird();\n")
	if want := "function run() {\n\tsecond();\t\n\tthird();\n}\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestFuzzyMatchAcceptsASmallTypo(t *testing.T) {
	result := mustApply(t, "const runner = computeValue(1);\nkeep();\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\nconst runner = computeValue(1);\n*** SM:PUT\nconst runner = compute(2);\n")
	if want := "const runner = compute(2);\nkeep();\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestNeverFuzzyMatchesDifferentOperatorSequences(t *testing.T) {
	_, err := applyOne(t, "const shouldRun = left || right;\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nleft && right\n*** SM:PUT\nleft ?? right\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "did not match a.ts")
}

func TestNoMatchErrorGroundsTheMiss(t *testing.T) {
	_, err := applyOne(t, "const RUNNER = compute(1);\nkeep();\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\nconst RUNNER = gone(9);\n*** SM:PUT\nconst RUNNER = compute(2);\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "did not match a.txt")
	requireContains(t, err.Error(), "Current file content near the closest match")
	requireContains(t, err.Error(), "Copy-ready corrected operation:")
	requireContains(t, err.Error(), "const RUNNER = compute(1);")
}

func TestAfterInsertsAndKeepsEndOfFileConvention(t *testing.T) {
	cases := []struct {
		name     string
		before   string
		expected string
	}{
		{name: "trailing newline", before: "anchor\nnext\n", expected: "anchor\nadded\nnext\n"},
		{name: "no trailing newline", before: "anchor", expected: "anchor\nadded"},
		{name: "last line", before: "anchor\n", expected: "anchor\nadded\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := mustApply(t, tc.before,
				"*** SM:EDIT a.txt\n*** SM:FIND\nanchor\n*** SM:AFTER\nadded\n")
			if result.Content != tc.expected {
				t.Errorf("content = %q, want %q", result.Content, tc.expected)
			}
		})
	}
}

func TestAfterBodyIsLiteral(t *testing.T) {
	result := mustApply(t, "anchor\n\nnext\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\nanchor\n*** SM:AFTER\n\n\t+literal <a> & \"b\"\n…\n\n")
	if want := "anchor\n\n\t+literal <a> & \"b\"\n…\n\nnext\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestSelectionWithGapsCapturesAndReplays(t *testing.T) {
	result := mustApply(t, "const value = oldCall(options);\nreport(value);\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nconst value = ⟪oldCall(…)│newCall(…) ?? fallback⟫;\nreport(value)\n")
	if want := "const value = newCall(options) ?? fallback;\nreport(value);\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestInlineSelectionsRewriteOnlyTheSelectedText(t *testing.T) {
	result := mustApply(t,
		"const timeout = readConfig().timeout ?? 1000;\nrun(timeout);\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\ntimeout = …⟪1000│5000⟫…\nrun(timeout)\n")
	if want := "const timeout = readConfig().timeout ?? 5000;\nrun(timeout);\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestInlineSelectionsApplyToEachNamedSelection(t *testing.T) {
	result := mustApply(t,
		"const timeout = 1000;\nconst retries = 3;\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nconst timeout = ⟪1000│5000⟫;\nconst retries = ⟪3│5⟫;\n")
	if want := "const timeout = 5000;\nconst retries = 5;\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestInlineInsertionAndDeletion(t *testing.T) {
	inserted := mustApply(t, "run(x);\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nrun(⟪│y⟫x);\n")
	if want := "run(yx);\n"; inserted.Content != want {
		t.Errorf("content = %q, want %q", inserted.Content, want)
	}

	deleted := mustApply(t, "run(x);\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nrun(⟪x│⟫);\n")
	if want := "run();\n"; deleted.Content != want {
		t.Errorf("content = %q, want %q", deleted.Content, want)
	}
}

func TestGapOnlySelectionReplacesTheCapturedRegion(t *testing.T) {
	result := mustApply(t, "loadUser( id , opts );\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nloadUser(⟪…│id⟫);\n")
	if want := "loadUser(id);\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestLineInsertionLandsOnItsOwnLine(t *testing.T) {
	result := mustApply(t, "function displayName(user) {\n  return user.name;\n}\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\n  return user.name;\n⟪│  return user?.name ?? fallback;⟫\n")
	if want := "function displayName(user) {\n  return user.name;\n  return user?.name ?? fallback;\n}\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestBatchAnchorsResolveAgainstOriginalContent(t *testing.T) {
	result := mustApply(t, "one\ntwo\nthree\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\none\n*** SM:PUT\none changed\n"+
			"*** SM:FIND\ntwo\n*** SM:AFTER\ninserted\n"+
			"*** SM:FIND\nthree\n*** SM:PUT\nthree changed\n")
	if want := "one changed\ntwo\ninserted\nthree changed\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
}

func TestOverlappingEditsFailAtomically(t *testing.T) {
	_, err := applyOne(t, "alpha beta\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\nalpha\n*** SM:PUT\nfirst\n*** SM:FIND\nalpha beta\n*** SM:PUT\nsecond\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "overlapping original spans")
}

func TestNoOpErrorNamesTheOperation(t *testing.T) {
	_, err := applyOne(t, "run();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nrun();\n*** SM:PUT\nrun();\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "Operation 1 makes no change to a.ts.")
}

func TestDesiredTextIsANoOpWhenItAlreadyHolds(t *testing.T) {
	result := mustApply(t, "const a = 1;\n",
		"*** SM:EDIT a.ts\n*** SM:PUT\nconst a = 1;\n")
	if want := "const a = 1;\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
	if len(result.Notes) != 1 {
		t.Fatalf("notes = %q, want 1 note", result.Notes)
	}
	requireContains(t, result.Notes[0], "already matches the file")
}

func TestDesiredTextUpgradesTheClosestBlock(t *testing.T) {
	result := mustApply(t,
		"function helper() {\n  return 1;\n}\nkeep();\n",
		"*** SM:EDIT a.ts\n*** SM:PUT\nfunction helper() {\n  return 2;\n}\n")
	if want := "function helper() {\n  return 2;\n}\nkeep();\n"; result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}
	if len(result.Notes) != 1 {
		t.Fatalf("notes = %q, want 1 note", result.Notes)
	}
	requireContains(t, result.Notes[0], "closest matching block")
}

func TestDesiredTextWithoutResemblingBlockFails(t *testing.T) {
	_, err := applyOne(t, "unrelated();\n",
		"*** SM:EDIT a.ts\n*** SM:PUT\nfunction helper() {\n  return 2;\n}\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "no block in a.ts resembles it")
}

func TestPutWithSelectionMarkersIsRejected(t *testing.T) {
	_, err := applyOne(t, "run();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nrun();\n*** SM:PUT\n⟪run│go⟫();\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "has selection markers in *** SM:PUT")
}

func TestWholeLineGapWithoutCaptureIsRejected(t *testing.T) {
	_, err := applyOne(t, "run();\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\nrun();\n*** SM:PUT\n…\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "whole-line … with no *** SM:FIND gap")
}

func TestTooGenericPatternIsRejected(t *testing.T) {
	_, err := applyOne(t, "if (ready) { run(); }\n",
		"*** SM:EDIT a.ts\n*** SM:FIND\n⟪};│⟫\n*** SM:PUT\n}\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), "too generic")
}

func TestFailureLeavesContentUntouched(t *testing.T) {
	_, err := applyOne(t, "alpha\nalpha\n",
		"*** SM:EDIT a.txt\n*** SM:FIND\nalpha\n*** SM:PUT\nbeta\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireContains(t, err.Error(), atomicityNotice)
}
