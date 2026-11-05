package plan

import (
	"strings"
	"testing"
)

func textPlan() Plan {
	p := Plan{
		Title:         "Split auth",
		Goal:          "one package owns credentials",
		InScope:       []string{"internal/auth"},
		OutOfScope:    []string{"the CLI"},
		Constraints:   []string{"no new deps"},
		Risks:         []string{"flaky tests"},
		OpenQuestions: []string{"keep the legacy cookie?"},
		Steps: []Step{
			{Title: "read the code", Objective: "map callers", Verification: []string{"rg auth", "go build ./..."}},
			{Title: "move files"},
		},
	}
	p.Normalize()
	p.AddDecision("keep the helper")
	return p
}

// The Edit plan editor round-trips a plan without losing what the panel shows.
func TestTextRoundTrip(t *testing.T) {
	original := textPlan()
	parsed, err := ParseText(FormatText(original))
	if err != nil {
		t.Fatalf("ParseText(FormatText()) failed: %v", err)
	}
	if parsed.Title != original.Title || parsed.Goal != original.Goal {
		t.Fatalf("title/goal = %q / %q", parsed.Title, parsed.Goal)
	}
	if len(parsed.InScope) != 1 || parsed.InScope[0] != "internal/auth" {
		t.Fatalf("in-scope = %v", parsed.InScope)
	}
	if len(parsed.OutOfScope) != 1 || parsed.OutOfScope[0] != "the CLI" {
		t.Fatalf("out-of-scope = %v", parsed.OutOfScope)
	}
	if len(parsed.Decisions) != 1 || parsed.Decisions[0].Text != "keep the helper" {
		t.Fatalf("decisions = %v", parsed.Decisions)
	}
	if len(parsed.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(parsed.Steps))
	}
	first := parsed.Steps[0]
	if first.Title != "read the code" || first.Objective != "map callers" {
		t.Fatalf("step 1 = %+v", first)
	}
	if len(first.Verification) != 2 || first.Verification[1] != "go build ./..." {
		t.Fatalf("step 1 verification = %v", first.Verification)
	}
}

// Formatting collapses a multi-line item so one item is always one line, and
// parsing refuses what it cannot represent instead of dropping it.
func TestTextRejectsWhatItCannotRepresent(t *testing.T) {
	if _, err := ParseText("title: x\ngoal: y\nstray line\n\nsteps:\n- a\n"); err == nil {
		t.Fatal("a stray line was accepted")
	}
	if _, err := ParseText("title: x\nwibble:\n- a\n"); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if _, err := ParseText("title: no steps\n"); err == nil {
		t.Fatal("a plan without steps was accepted")
	}

	p := textPlan()
	p.Steps[0].Objective = "map\ncallers"
	if text := FormatText(p); strings.Contains(text, "map\ncallers") {
		t.Fatalf("a newline survived into one item:\n%s", text)
	}
}

// MergeEdit folds an edit back onto the plan it replaces: identity, step IDs,
// and the progress journal survive, and the steps restart as pending.
func TestMergeEditKeepsIdentity(t *testing.T) {
	base := textPlan()
	base.Progress = []LogEntry{{Kind: "draft", Message: "Plan ready for review"}}

	edited, err := ParseText("title: Renamed\ngoal: newer\n\nsteps:\n- one\n- two\n- three\n")
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeEdit(base, edited)
	if merged.ID != base.ID {
		t.Fatalf("ID = %q, want %q", merged.ID, base.ID)
	}
	if merged.Title != "Renamed" || len(merged.Steps) != 3 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged.Steps[0].ID != base.Steps[0].ID {
		t.Fatal("step 1 lost its ID")
	}
	if merged.Steps[2].ID == "" {
		t.Fatal("the new step has no ID")
	}
	if len(merged.Progress) != 1 {
		t.Fatalf("progress = %+v", merged.Progress)
	}
	if merged.Steps[0].Status != StepPending {
		t.Fatalf("status = %q, want pending", merged.Steps[0].Status)
	}
}
