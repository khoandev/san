package todo

import "testing"

// The metadata a projected step carries reads back through the accessors, and a
// cleared reason disappears instead of lingering on the row.
func TestPlanMetadataRoundTrip(t *testing.T) {
	metadata := PlanMetadata("plan-1", "step-1", "blocked", "the API moved")
	item := &Item{Metadata: metadata}

	if got := PlanID(item); got != "plan-1" {
		t.Fatalf("PlanID = %q", got)
	}
	if got := PlanStepID(item); got != "step-1" {
		t.Fatalf("PlanStepID = %q", got)
	}
	if got := PlanStepStatus(item); got != "blocked" {
		t.Fatalf("PlanStepStatus = %q", got)
	}
	if got := PlanStepBlockReason(item); got != "the API moved" {
		t.Fatalf("PlanStepBlockReason = %q", got)
	}

	// WithMetadata treats nil as a delete, which is how a step that moved on
	// stops carrying the reason it was blocked for.
	if err := (&Store{items: map[string]*Item{"1": item}}).Update("1", WithMetadata(PlanMetadata("plan-1", "step-1", "completed", ""))); err != nil {
		t.Fatal(err)
	}
	if got := PlanStepBlockReason(item); got != "" {
		t.Fatalf("stale block reason = %q", got)
	}
	if got := PlanStepStatus(item); got != "completed" {
		t.Fatalf("PlanStepStatus = %q after update", got)
	}
}

// An ordinary todo carries none of the plan fields.
func TestPlanAccessorsOnPlainItem(t *testing.T) {
	item := &Item{Subject: "Audit handlers"}
	if PlanID(item) != "" || PlanStepID(item) != "" || PlanStepStatus(item) != "" || PlanStepBlockReason(item) != "" {
		t.Fatalf("plain item answered a plan question: %+v", item.Metadata)
	}
	if PlanID(nil) != "" {
		t.Fatal("nil item must answer empty")
	}
}
