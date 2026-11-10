package plan

import (
	"strings"
	"testing"
)

// Text without a plan fence must come back byte for byte: the mask runs on
// every assistant message, so anything it touched that it did not own would
// rewrite the transcript.
func TestMaskBlocksLeavesOtherContentAlone(t *testing.T) {
	for _, text := range []string{
		"",
		"plain prose\nwith a second line\n",
		"```go\nfunc main() {}\n```\n",
		"```json\n{\"not\":\"a plan\"}\n```\n",
		"~~~\nno info string\n~~~\n",
		// A code block quoting plan syntax is content, not a plan block.
		"````markdown\n```plan\n{\"title\":\"quoted\"}\n```\n````\n",
		// An unterminated non-plan block is copied as it is.
		"```go\nx := 1\n",
		"a table | b\n--- | ---\n1 | 2\n",
		"trailing prose without a newline",
		"```planning\nnot the plan protocol\n```\n",
	} {
		if got := MaskBlocks(text); got != text {
			t.Fatalf("MaskBlocks changed unrelated text:\n in: %q\nout: %q", text, got)
		}
	}
}

func TestMaskBlocksReplacesClosedPlanBlock(t *testing.T) {
	text := "Before.\n\n```plan\n" +
		`{"title":"Split auth","goal":"one package","steps":[{"title":"a"},{"title":"b"}]}` +
		"\n```\n\nAfter.\n"
	got := MaskBlocks(text)
	if strings.Contains(got, "```") || strings.Contains(got, `"title"`) {
		t.Fatalf("plan JSON survived the mask:\n%s", got)
	}
	if !strings.HasSuffix(got, "After.\n") {
		t.Fatalf("prose after the block was dropped:\n%q", got)
	}
	for _, want := range []string{"Before.", "After.", "▸ plan ready — Split auth · 2 steps"} {
		if !strings.Contains(got, want) {
			t.Fatalf("masked text is missing %q:\n%s", want, got)
		}
	}
}

// A block that has not closed yet is a block still streaming. It must vanish
// without a summary line, or the row would change shape as tokens arrive.
func TestMaskBlocksDropsUnterminatedBlockWithoutSummary(t *testing.T) {
	text := "Intro.\n\n```plan\n{\"title\":\"Split auth\",\n"
	got := MaskBlocks(text)
	if got != "Intro.\n\n" {
		t.Fatalf("unterminated block = %q, want the prose only", got)
	}
	if strings.Contains(got, "▸") {
		t.Fatalf("an unterminated block must not render a summary:\n%s", got)
	}
}

// A block the parser rejects stays readable: the app only answers it with a
// "Plan rejected" notice, so the raw JSON is the one copy the person can read.
func TestMaskBlocksKeepsUnparsableBlocks(t *testing.T) {
	for _, text := range []string{
		"```plan\n{\"title\": \n```\n",
		// Parses as JSON but the plan is not usable: same rule.
		"```plan\n{\"steps\":[{\"title\":\"a\"}]}\n```\n",
		"```plan-step\n{\"status\":\"completed\"\n```\n",
	} {
		if got := MaskBlocks(text); got != text {
			t.Fatalf("unparsable block was not kept:\n in: %q\nout: %q", text, got)
		}
	}
}

func TestMaskBlocksSummarizesPlanUpdatesAndSteps(t *testing.T) {
	text := "```plan-update\n{\"goal\":\"one package\"}\n```\n" +
		"```plan-step\n{\"status\":\"completed\",\"result\":\"moved files\"}\n```\n" +
		"```plan-step\n{\"status\":\"skipped\"}\n```\n" +
		"```plan-step\n{\"status\":\"blocked\",\"reason\":\"tests fail\"}\n```\n"
	want := "▸ plan updated\n" +
		"✓ step completed — moved files\n" +
		"- step skipped\n" +
		"! step blocked — tests fail\n"
	if got := MaskBlocks(text); got != want {
		t.Fatalf("masked blocks =\n%q\nwant\n%q", got, want)
	}
}

// Summaries are one line by construction: a long result is shortened, never
// wrapped, so the mask cannot add rows the renderer did not budget for.
func TestMaskBlocksShortensLongDetails(t *testing.T) {
	long := strings.Repeat("word ", 40)
	text := "```plan-step\n{\"status\":\"completed\",\"result\":\"" + strings.TrimSpace(long) + "\"}\n```\n"
	got := strings.TrimRight(MaskBlocks(text), "\n")
	if strings.Contains(got, "\n") {
		t.Fatalf("summary wrapped onto more than one line: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("a shortened summary should end in an ellipsis: %q", got)
	}
	if runes := []rune(got); len(runes) > len("✓ step completed — ")+summaryText {
		t.Fatalf("summary is longer than its bound: %d runes", len(runes))
	}
}

// The mask must not swallow prose that follows a completed block.
func TestMaskBlocksKeepsProseAroundBlocks(t *testing.T) {
	text := "line one\n```plan-step\n{\"status\":\"completed\"}\n```\nline two\n"
	got := MaskBlocks(text)
	want := "line one\n✓ step completed\nline two\n"
	if got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}
