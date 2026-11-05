package advisor

import (
	"testing"
)

func TestParseSeverity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want Severity
		ok   bool
	}{
		{"P0", SeverityP0, true},
		{"p1", SeverityP1, true},
		{" P2 ", SeverityP2, true},
		{"p3", SeverityP3, true},
		{"", "", false},
		{"P4", "", false},
		{"blocker", "", false},
		{"0", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseSeverity(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseSeverity(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSeverityRankOrdersP0First(t *testing.T) {
	t.Parallel()

	if !SeverityP0.Less(SeverityP1) || !SeverityP1.Less(SeverityP2) || !SeverityP2.Less(SeverityP3) {
		t.Fatal("severity scale is not ordered P0 < P1 < P2 < P3")
	}
	if SeverityP0.Less(SeverityP0) {
		t.Error("a severity must not be less than itself")
	}
	// An unknown level sorts after every real one so it can never read as a
	// blocker.
	if got := Severity("P9").Rank(); got <= SeverityP3.Rank() {
		t.Errorf("unknown severity rank = %d, want greater than P3's %d", got, SeverityP3.Rank())
	}
	if Severity("P9").Valid() {
		t.Error("unknown severity reported valid")
	}
}

func TestReviewHighestIgnoresResolvedFindings(t *testing.T) {
	t.Parallel()

	r := Review{Findings: []Finding{
		{Severity: SeverityP0, Status: FindingDismissed},
		{Severity: SeverityP2, Status: FindingOpen},
		{Severity: SeverityP3, Status: FindingDeferred},
	}}
	if got := r.Highest(); got != SeverityP2 {
		t.Fatalf("Highest() = %q, want P2 (the dismissed P0 must not count)", got)
	}

	r.Findings = append(r.Findings, Finding{Severity: SeverityP0, Status: FindingOpen})
	if got := r.Highest(); got != SeverityP0 {
		t.Fatalf("Highest() = %q, want P0", got)
	}

	if got := (Review{}).Highest(); got != "" {
		t.Fatalf("Highest() of an empty review = %q, want empty", got)
	}
}

func TestReviewCounts(t *testing.T) {
	t.Parallel()

	r := Review{Findings: []Finding{
		{Severity: SeverityP0, Status: FindingOpen},
		{Severity: SeverityP0, Status: FindingDismissed},
		{Severity: SeverityP1, Status: FindingAddressed},
		{Severity: SeverityP3, Status: FindingOpen},
	}}
	all := r.Counts()
	if all != (Counts{P0: 2, P1: 1, P3: 1}) {
		t.Errorf("Counts() = %+v, want P0:2 P1:1 P3:1", all)
	}
	open := r.UnresolvedCounts()
	if open != (Counts{P0: 1, P3: 1}) {
		t.Errorf("UnresolvedCounts() = %+v, want P0:1 P3:1", open)
	}
	if all.Total() != 4 {
		t.Errorf("Total() = %d, want 4", all.Total())
	}
	if got := all.Blocking(SeverityP1); got != 3 {
		t.Errorf("Blocking(P1) = %d, want 3", got)
	}
	if got := all.Blocking(Severity("")); got != 0 {
		t.Errorf("Blocking(\"\") = %d, want 0", got)
	}
}

func TestReviewOpenFindingsSortedBySeverity(t *testing.T) {
	t.Parallel()

	r := Review{Findings: []Finding{
		{Title: "z", Severity: SeverityP3, Status: FindingOpen},
		{Title: "a", Severity: SeverityP0, Status: FindingOpen},
		{Title: "gone", Severity: SeverityP0, Status: FindingAddressed},
		{Title: "m", Severity: SeverityP1, Status: FindingDeferred, File: "b.go"},
		{Title: "m", Severity: SeverityP1, Status: FindingOpen, File: "a.go"},
	}}
	open := r.OpenFindings()
	if len(open) != 4 {
		t.Fatalf("OpenFindings() returned %d findings, want 4", len(open))
	}
	if open[0].Title != "a" || open[1].File != "a.go" || open[2].File != "b.go" || open[3].Title != "z" {
		t.Fatalf("unexpected order: %+v", []string{open[0].Title, open[1].File, open[2].File, open[3].Title})
	}
}

func TestReviewValidate(t *testing.T) {
	t.Parallel()

	valid := Review{
		Trigger: TriggerBeforeMerge,
		Target:  Target{Kind: TargetDiff},
		Status:  StatusPassed,
		Findings: []Finding{
			{Severity: SeverityP2, Status: FindingOpen},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}

	broken := []Review{
		{Trigger: "", Target: Target{Kind: TargetDiff}, Status: StatusPassed},
		{Trigger: TriggerBeforeMerge, Target: Target{Kind: "nonsense"}, Status: StatusPassed},
		{Trigger: TriggerBeforeMerge, Target: Target{Kind: TargetDiff}, Status: "great"},
		{Trigger: TriggerBeforeMerge, Target: Target{Kind: TargetDiff}, Status: StatusPassed, Severity: "P7"},
		{Trigger: TriggerBeforeMerge, Target: Target{Kind: TargetDiff}, Status: StatusPassed,
			Findings: []Finding{{Severity: "", Status: FindingOpen}}},
		{Trigger: TriggerBeforeMerge, Target: Target{Kind: TargetDiff}, Status: StatusPassed,
			Findings: []Finding{{Severity: SeverityP0, Status: "later"}}},
	}
	for i, r := range broken {
		if err := r.Validate(); err == nil {
			t.Errorf("case %d: incoherent review accepted", i)
		}
	}
}

func TestFindingResolveRequiresReasonForDismissedBlocker(t *testing.T) {
	t.Parallel()

	for _, sev := range []Severity{SeverityP0, SeverityP1} {
		f := Finding{Severity: sev, Status: FindingOpen}
		if err := f.Resolve(FindingDismissed, "  "); err == nil {
			t.Errorf("dismissing a %s finding without a reason was accepted", sev)
		}
		if err := f.Resolve(FindingDismissed, "false positive: guarded upstream"); err != nil {
			t.Errorf("dismissing a %s finding with a reason failed: %v", sev, err)
		}
		if f.Status != FindingDismissed || f.Note == "" {
			t.Errorf("finding not resolved: %+v", f)
		}
	}

	f := Finding{Severity: SeverityP3, Status: FindingOpen}
	if err := f.Resolve(FindingDismissed, ""); err != nil {
		t.Errorf("dismissing a P3 without a reason failed: %v", err)
	}
	if err := f.Resolve("nonsense", ""); err == nil {
		t.Error("unknown status accepted")
	}
}

func TestTargetSameAsIgnoresFiles(t *testing.T) {
	t.Parallel()

	a := Target{Kind: TargetTask, ID: "task-1", Files: []string{"a.go"}}
	b := Target{Kind: TargetTask, ID: "task-1", Files: []string{"a.go", "b.go"}}
	c := Target{Kind: TargetStep, ID: "task-1"}
	if !a.sameAs(b) {
		t.Error("same target kind and id compared unequal")
	}
	if a.sameAs(c) {
		t.Error("different target kinds compared equal")
	}
}
