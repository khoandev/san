package llm

import (
	"regexp"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// The gateway's free tier is gated on wire shape rather than on a credential,
// so these are the checks that decide whether a request is answered at all.

func TestOpenCodeSessionTokenHasTheGateShapeAndIsStable(t *testing.T) {
	pattern := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

	first := opencodeSessionToken("conv-1")
	if !pattern.MatchString(first) {
		t.Fatalf("token %q does not match the gate's shape", first)
	}
	if second := opencodeSessionToken("conv-1"); second != first {
		t.Errorf("the same conversation minted two tokens: %q then %q — every turn would miss the cache", first, second)
	}
	if other := opencodeSessionToken("conv-2"); other == first {
		t.Errorf("two conversations share one token: %q", other)
	}

	canonical := "ses_0123456789abABCDEFGHIJKLMN"
	if got := opencodeSessionToken(canonical); got != canonical {
		t.Errorf("a canonical token was rewritten: %q, want %q", got, canonical)
	}
}

// The head is clocked, not digest-derived: Zen decodes it as a client-side
// timestamp and refuses a value that reads as a foreign client.
func TestOpenCodeSessionHeadsAreClocked(t *testing.T) {
	heads := make(map[string]bool)
	for _, id := range []string{"a", "b", "c", "d"} {
		heads[opencodeSessionToken(id)[4:16]] = true
	}
	if len(heads) != 4 {
		t.Fatalf("four sessions produced %d distinct heads: %v", len(heads), heads)
	}
}

func TestOpenCodeFreeModelMatchesTheLineup(t *testing.T) {
	for _, id := range []string{"big-pickle", "beam-free", "deepseek-v4.1-flash-free"} {
		if !opencodeFreeModel(id) {
			t.Errorf("%q was not recognised as free", id)
		}
	}
	for _, id := range []string{"claude-opus-5", "deepseek-v4.1-flash", "free-tier", ""} {
		if opencodeFreeModel(id) {
			t.Errorf("%q was taken for a free model", id)
		}
	}
}

func TestOpenCodeShapeRequestAddsTheEnvBlockAndGateToolsOnce(t *testing.T) {
	system := "base"
	var tools []core.ToolSchema
	opencodeShapeRequest("big-pickle", "conv-1", "/tmp/project", &system, &tools)

	want := "base\n<env>\n  Working directory: /tmp/project\n</env>"
	if system != want {
		t.Fatalf("system = %q, want %q", system, want)
	}
	for _, name := range opencodeGateTools {
		if count := countTool(tools, name); count != 1 {
			t.Errorf("tool %q appears %d times, want 1", name, count)
		}
	}

	// A second pass — a retried inference — must change neither.
	opencodeShapeRequest("big-pickle", "conv-1", "/tmp/elsewhere", &system, &tools)
	if system != want {
		t.Errorf("the env block moved on a second shape: %q", system)
	}
	for _, name := range opencodeGateTools {
		if count := countTool(tools, name); count != 1 {
			t.Errorf("tool %q appears %d times after a second shape, want 1", name, count)
		}
	}
}

func TestOpenCodeShapeRequestLeavesABilledModelAlone(t *testing.T) {
	system := "base"
	tools := []core.ToolSchema{{Name: "Read"}}
	opencodeShapeRequest("claude-opus-5", "conv-1", "/tmp/project", &system, &tools)

	if system != "base" {
		t.Errorf("a billed model's prompt was rewritten: %q", system)
	}
	if len(tools) != 1 {
		t.Errorf("a billed model's roster was padded: %+v", tools)
	}
}

func TestOpenCodeShapeRequestKeepsAnExistingRoster(t *testing.T) {
	system := "<env>\n  Working directory: /already\n</env>"
	tools := []core.ToolSchema{
		{Name: "bash"}, {Name: "edit"}, {Name: "glob"}, {Name: "grep"}, {Name: "read"},
	}
	opencodeShapeRequest("beam-free", "", "/tmp/ignored", &system, &tools)

	if system != "<env>\n  Working directory: /already\n</env>" {
		t.Errorf("the existing env block was replaced: %q", system)
	}
	if len(tools) != 5 {
		t.Errorf("a complete roster was padded to %d entries", len(tools))
	}
}

func TestOpenCodeSessionHeadersForAFreeModel(t *testing.T) {
	headers := opencodeSessionHeaders("big-pickle", "conv-1")
	if got := headers["User-Agent"]; !strings.HasPrefix(got, opencodeUserAgent) {
		t.Errorf("User-Agent = %q, want it to lead with %q", got, opencodeUserAgent)
	}
	if got := headers["x-opencode-client"]; got != "cli" {
		t.Errorf("x-opencode-client = %q, want cli", got)
	}
	if got := headers["x-opencode-session"]; !opencodeSessionPattern.MatchString(got) {
		t.Errorf("x-opencode-session = %q, want the canonical shape", got)
	}

	// Traffic outside any conversation still carries the header, on this
	// installation's identity.
	if got := opencodeSessionHeaders("beam-free", "")["x-opencode-session"]; !opencodeSessionPattern.MatchString(got) {
		t.Errorf("a conversation-less session header = %q, want the canonical shape", got)
	}
}

func countTool(tools []core.ToolSchema, name string) int {
	n := 0
	for _, schema := range tools {
		if schema.Name == name {
			n++
		}
	}
	return n
}
