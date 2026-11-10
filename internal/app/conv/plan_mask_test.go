package conv

import (
	"strings"
	"testing"
)

// The plan protocol arrives as fenced JSON in the agent's prose. The panel is
// where a person reads the plan, so the transcript must never draw the block —
// neither while it streams, nor once it is committed to scrollback.

func closedPlanBlock() string {
	return "```plan\n" +
		`{"title":"Split auth","goal":"one package","steps":[{"title":"a"},{"title":"b"}]}` +
		"\n```\n"
}

// A message that is nothing but a plan block still renders one row: the
// summary line replaces the block, so the transcript shows that a plan landed
// without a bullet dangling over JSON.
func TestRenderAssistantMessageHidesPlanBlock(t *testing.T) {
	out := stripANSI(RenderAssistantMessage(AssistantParams{Content: closedPlanBlock(), Width: 80}))
	if strings.Contains(out, "```") || strings.Contains(out, "{") {
		t.Fatalf("the plan JSON reached the transcript: %q", out)
	}
	if !strings.Contains(out, "● ▸ plan ready") {
		t.Fatalf("block-only message = %q, want one summary row under the bullet", out)
	}
}

func TestRenderAssistantMessageSummarizesPlanBlock(t *testing.T) {
	out := stripANSI(RenderAssistantMessage(AssistantParams{
		Content: "Here is the plan.\n\n" + closedPlanBlock(),
		Width:   80,
	}))
	if strings.Contains(out, "```") || strings.Contains(out, `"steps"`) {
		t.Fatalf("the plan JSON reached the transcript:\n%s", out)
	}
	if !strings.Contains(out, "Here is the plan.") {
		t.Fatalf("the prose around the block was dropped:\n%s", out)
	}
	if got := strings.Count(out, "▸ plan ready"); got != 1 {
		t.Fatalf("summary lines = %d, want exactly one:\n%s", got, out)
	}
}

// While the block streams, the open fence must stay hidden: drawing its JSON
// and then replacing it with a summary line would flicker row to row.
func TestRenderAssistantMessageHidesStreamingPlanBlock(t *testing.T) {
	out := stripANSI(RenderAssistantMessage(AssistantParams{
		StreamActive: true,
		IsLast:       true,
		Content:      "```plan\n{\"title\":\"Split auth\",\n",
		Width:        80,
	}))
	if strings.Contains(out, "```") || strings.Contains(out, "{") || strings.Contains(out, "plan ready") {
		t.Fatalf("the streaming plan block leaked into the live tail: %q", out)
	}
}

func TestRenderCommittedContentBlockHidesPlanBlock(t *testing.T) {
	if out := stripANSI(RenderCommittedContentBlock(closedPlanBlock(), true, nil)); !strings.Contains(out, "▸ plan ready") || strings.Contains(out, "{") {
		t.Fatalf("a committed plan-only block = %q, want the summary row only", out)
	}

	out := stripANSI(RenderCommittedContentBlock("Done reading.\n\n"+closedPlanBlock(), true, nil))
	if strings.Contains(out, "```") {
		t.Fatalf("committed scrollback shows the plan JSON:\n%s", out)
	}
	if !strings.Contains(out, "Done reading.") || !strings.Contains(out, "▸ plan ready") {
		t.Fatalf("committed row lost its prose or its summary:\n%s", out)
	}
}

// Scrollback always renders with a markdown renderer, so the masked text has
// to be the text that reaches glamour — this is the path a live transcript
// actually takes.
func TestRenderCommittedContentBlockMasksThroughMarkdown(t *testing.T) {
	md := NewMDRenderer(80)
	out := stripANSI(RenderCommittedContentBlock("Done reading.\n\n"+closedPlanBlock(), true, md))
	if strings.Contains(out, "```") || strings.Contains(out, "{") {
		t.Fatalf("markdown rendering leaked the plan JSON into scrollback:\n%s", out)
	}
	if got := strings.Count(out, "▸ plan ready"); got != 1 {
		t.Fatalf("summary lines = %d, want exactly one:\n%s", got, out)
	}
	if !strings.Contains(out, "Done reading.") {
		t.Fatalf("the prose around the block was dropped:\n%s", out)
	}
}

func TestRenderCommittedContentBlockLeavesOtherCodeAlone(t *testing.T) {
	content := "```go\nfunc main() {}\n```\n"
	out := stripANSI(RenderCommittedContentBlock(content, true, nil))
	if !strings.Contains(out, "func main() {}") {
		t.Fatalf("a non-plan code block was masked away:\n%s", out)
	}
}
