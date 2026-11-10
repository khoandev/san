package advisor

import (
	"strings"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"

	"github.com/genai-io/san/internal/core"

	// Register the built-in tools so ReadOnlyTools filters a live registry, the
	// same wiring the app uses.
	_ "github.com/genai-io/san/internal/tool/register"
)

// The caller counts the advisor's requests, so a review must report the steps
// the fork actually spent.
func TestAdviseReportsInferenceSteps(t *testing.T) {
	driver := aitest.New(aitest.Says("- the retry loop never resets its backoff"))
	engine := NewEngine(EngineConfig{
		Client: func([]core.Message) (*ai.Client, error) { return driver.Client(), nil },
		CWD:    t.TempDir(),
	})

	note, steps, err := engine.Advise(t.Context(), "User: implement the retry loop")
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if !strings.Contains(note, "backoff") {
		t.Fatalf("note = %q", note)
	}
	if steps != 1 {
		t.Fatalf("steps = %d, want 1 for a single-inference review", steps)
	}
}

func TestParseNote(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty answer is silence", "  \n\t ", ""},
		{"no advice", "NO_ADVICE", ""},
		{"no advice with punctuation", "  no_advice.  ", ""},
		{"no advice with trailing lines", "NO_ADVICE\n\nnothing to add", ""},
		{"a note is kept", "- the retry loop never resets its backoff", "- the retry loop never resets its backoff"},
		{"a fenced note is unwrapped", "```text\n- the test file is not compiled\n```", "- the test file is not compiled"},
		{"advice is kept even when it opens with a word", "Consider the empty case.", "Consider the empty case."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseNote(tt.content); got != tt.want {
				t.Fatalf("parseNote(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// A note is injected into the main conversation's context window, so a runaway
// review must not be able to exceed the cap it is documented to respect.
func TestParseNoteCapsLength(t *testing.T) {
	note := parseNote(strings.Repeat("á", maxNoteRunes+10))
	if runes := []rune(note); len(runes) != maxNoteRunes+1 || runes[len(runes)-1] != '…' {
		t.Fatalf("note length = %d runes, want %d plus an ellipsis", len(runes), maxNoteRunes+1)
	}
}

// The gate is the whole safety story of a background review: it runs without a
// person watching, so a write tool must never reach it.
func TestReadOnlyToolsExcludesWriters(t *testing.T) {
	tools := ReadOnlyTools(t.TempDir())
	if tools == nil || len(tools.All()) == 0 {
		t.Fatal("no read tools were registered")
	}
	gate := readOnlyGate(tools)
	for _, tool := range tools.All() {
		ok, _ := gate(t.Context(), tool.Schema().Name, nil)
		if !ok {
			t.Fatalf("%s is in the tool set but the gate rejects it", tool.Schema().Name)
		}
	}
	for _, name := range []string{"Edit", "Write", "Bash", "Task"} {
		if ok, reason := gate(t.Context(), name, nil); ok || reason == "" {
			t.Fatalf("%s passed the read-only gate (reason %q)", name, reason)
		}
	}
}

func TestAdviseWithoutClientFails(t *testing.T) {
	if _, _, err := NewEngine(EngineConfig{}).Advise(t.Context(), "User: hi"); err == nil {
		t.Fatal("an engine without a client reported a review")
	}
	var nilEngine *Engine
	if _, _, err := nilEngine.Advise(t.Context(), "User: hi"); err == nil {
		t.Fatal("a nil engine reported a review")
	}
}
