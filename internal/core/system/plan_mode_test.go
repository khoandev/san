package system

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// TestPlanModeSectionSurvivesAPersonaSwap is the promise the section exists
// for: a persona swap replaces the identity / behavior / rules parts wholesale,
// and Plan Mode's contract is not one of them.
func TestPlanModeSectionSurvivesAPersonaSwap(t *testing.T) {
	const rules = "You are in PLAN MODE. This mode is read-only."
	sys := Build(core.ScopeMain)
	SetPlanModeRules(sys, rules)

	if got := sys.Prompt(); strings.Count(got, "PLAN MODE") != 1 {
		t.Fatalf("prompt carries %d plan-mode blocks, want 1", strings.Count(got, "PLAN MODE"))
	}
	rulesAt := strings.Index(sys.Prompt(), "</rules>")
	planAt := strings.Index(sys.Prompt(), "<plan-mode>")
	if rulesAt < 0 || planAt < rulesAt {
		t.Fatal("the plan-mode block must render under the rules it narrows")
	}

	SwapPersona(sys, Persona{Rules: "custom rules"})
	prompt := sys.Prompt()
	if !strings.Contains(prompt, "custom rules") {
		t.Fatal("the persona's rules part did not land")
	}
	if !strings.Contains(prompt, rules) {
		t.Fatal("a persona swap dropped the Plan Mode contract")
	}
}

// TestSetPlanModeRulesEmptyDropsTheSection is the leaving half: an empty text
// takes the block off instead of leaving an empty envelope behind.
func TestSetPlanModeRulesEmptyDropsTheSection(t *testing.T) {
	sys := Build(core.ScopeMain)
	SetPlanModeRules(sys, "read-only")
	SetPlanModeRules(sys, "  ")

	prompt := sys.Prompt()
	if strings.Contains(prompt, "plan-mode") || strings.Contains(prompt, "read-only") {
		t.Fatal("an empty contract left the section on the prompt")
	}
	names := make([]string, 0, len(sys.Sections()))
	for _, sec := range sys.Sections() {
		names = append(names, sec.Name)
	}
	if strings.Contains(strings.Join(names, ","), "plan-mode") {
		t.Fatalf("section still registered: %v", names)
	}
}
