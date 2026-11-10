package plan

import (
	"fmt"
	"strings"
)

// The agent emits the plan protocol as fenced JSON blocks — ```plan,
// ```plan-update and ```plan-step (see parse.go). Those blocks are transport,
// not message: the host reads them into the plan store and the Execution Panel
// shows the result. Rendered as they arrive, they reach the transcript as a
// wall of JSON in the middle of the agent's prose, so the display layer asks
// for a masked copy instead of rendering the raw text.
//
// MaskBlocks lives beside the parser on purpose: one file owns what a plan
// fence looks like, and the renderer only decides where to call it.

// summaryText bounds the text a one-line summary carries. The renderer that
// draws the summary has no width to pass down here, so the bound is fixed.
const summaryText = 60

// MaskBlocks returns text as it should be displayed: every plan fence is
// replaced by one short line, and the JSON never reaches the screen.
//
// Three cases keep the transcript honest:
//
//   - a closed block that parses becomes one summary line;
//   - a closed block that does not parse is kept verbatim. The app answers a
//     bad block with a "Plan rejected" notice and opens no panel, so the raw
//     block is the only copy of what the agent produced;
//   - an unterminated block is dropped with no summary. That is a block still
//     streaming: half a document cannot be parsed, and a line that changed
//     shape as tokens arrived would flicker.
//
// Anything that is not a plan fence is left exactly as it came in, including
// a fenced code block that happens to quote plan syntax.
func MaskBlocks(text string) string {
	if !strings.Contains(text, "```") && !strings.Contains(text, "~~~") {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	var out strings.Builder
	for i := 0; i < len(lines); i++ {
		marker, run, info, ok := fenceOpener(lines[i])
		if !ok {
			out.WriteString(lines[i])
			continue
		}
		if !isPlanFence(info) {
			// Some other code block: copy it through its closing fence, so a
			// plan-looking line quoted inside it stays content.
			out.WriteString(lines[i])
			i = copyFence(lines, i+1, &out, marker, run)
			continue
		}
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if closesFence(lines[j], marker, run) {
				end = j
				break
			}
		}
		if end < 0 {
			// A block that never closed is the streaming tail. Whatever
			// follows it in this snapshot cannot be displayed either.
			return out.String()
		}
		summary, ok := planSummary(info, strings.Join(lines[i+1:end], ""))
		if !ok {
			for j := i; j <= end; j++ {
				out.WriteString(lines[j])
			}
			i = end
			continue
		}
		out.WriteString(summary)
		if strings.HasSuffix(lines[end], "\n") {
			out.WriteString("\n")
		}
		i = end
	}
	return out.String()
}

// copyFence writes lines from start through the closing fence of marker/run
// and returns the index of the last line it wrote. An unterminated block is
// copied to the end of the text.
func copyFence(lines []string, start int, out *strings.Builder, marker byte, run int) int {
	for j := start; j < len(lines); j++ {
		out.WriteString(lines[j])
		if closesFence(lines[j], marker, run) {
			return j
		}
	}
	return len(lines) - 1
}

// isPlanFence reports whether a fence's info string opens one of the plan
// protocol blocks.
func isPlanFence(info string) bool {
	switch {
	case strings.EqualFold(info, PlanBlockFence),
		strings.EqualFold(info, UpdateBlockFence),
		strings.EqualFold(info, StepBlockFence):
		return true
	}
	return false
}

// planSummary renders one display line for a closed plan fence. ok is false
// when the block cannot be parsed, which leaves the caller to keep the block
// as the agent sent it.
func planSummary(info, body string) (string, bool) {
	text := fenced(info, body)
	switch {
	case strings.EqualFold(info, PlanBlockFence):
		produced, found, err := ParsePlanBlock(text)
		if err != nil || !found {
			return "", false
		}
		return fmt.Sprintf("▸ plan ready — %s · %d steps", shorten(produced.Title), len(produced.Steps)), true
	case strings.EqualFold(info, UpdateBlockFence):
		if _, found, err := ParseUpdateBlock(text); err != nil || !found {
			return "", false
		}
		return "▸ plan updated", true
	case strings.EqualFold(info, StepBlockFence):
		outcome, found, err := ParseStepBlock(text)
		if err != nil || !found {
			return "", false
		}
		switch outcome.Status {
		case StepCompleted:
			return withDetail("✓ step completed", outcome.Result), true
		case StepBlocked:
			return withDetail("! step blocked", outcome.Reason), true
		case StepSkipped:
			return "- step skipped", true
		}
		return "▸ step report", true
	}
	return "", false
}

// fenced rebuilds the text the parsers expect from a body that is already
// unwrapped. Reusing the parsers keeps the summary in step with the protocol
// instead of growing a second reader of the same JSON.
func fenced(info, body string) string {
	return "```" + info + "\n" + body + "\n```\n"
}

// withDetail appends a shortened detail to a summary head, and nothing when the
// block carried no detail to show.
func withDetail(head, detail string) string {
	if strings.TrimSpace(detail) == "" {
		return head
	}
	return head + " — " + shorten(detail)
}

// shorten collapses a value to one bounded line: a summary is a row in the
// transcript, not a place to read the report.
func shorten(s string) string {
	plain := oneLine(s)
	runes := []rune(plain)
	if len(runes) <= summaryText {
		return plain
	}
	return strings.TrimRight(string(runes[:summaryText-1]), " ") + "…"
}

// fenceOpener reports the marker of a line that opens or closes a fenced code
// block: the fence character, its run length, and the info string after it.
func fenceOpener(line string) (marker byte, run int, info string, ok bool) {
	s := strings.TrimSpace(line)
	if len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return 0, 0, "", false
	}
	marker = s[0]
	for run < len(s) && s[run] == marker {
		run++
	}
	if run < 3 {
		return 0, 0, "", false
	}
	return marker, run, strings.TrimSpace(s[run:]), true
}

// closesFence reports whether line closes a fence opened with marker: the same
// character, at least as long, and carrying no info string.
func closesFence(line string, marker byte, run int) bool {
	m, r, info, ok := fenceOpener(line)
	return ok && m == marker && r >= run && info == ""
}
