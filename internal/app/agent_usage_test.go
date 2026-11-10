package app

import "testing"

// The status bar reads Snapshot in display order, so the order is part of the
// contract, not an implementation detail.
func TestAgentUsageSnapshotOrdersRoles(t *testing.T) {
	u := NewAgentUsage()
	u.Add("custom-b", 2)
	u.Add("task", 4)
	u.Add("advisor", 1)
	u.Add("custom-a", 3)
	u.Add("quick", 5)
	u.Add("reviewer", 6)
	u.Add("explore", 7)

	got := u.Snapshot()
	want := []AgentUsageEntry{
		{Name: "advisor", Count: 1},
		{Name: "quick", Count: 5},
		{Name: "explore", Count: 7},
		{Name: "task", Count: 4},
		{Name: "reviewer", Count: 6},
		{Name: "custom-a", Count: 3},
		{Name: "custom-b", Count: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("snapshot[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Two call sites naming the same role must land on one entry, and a run that
// spent no inference must not create one.
func TestAgentUsageMergesNamesAndIgnoresEmptyRuns(t *testing.T) {
	u := NewAgentUsage()
	u.Add("quick", 2)
	u.Add("quick", 3)
	u.Add("  ", 9)
	u.Add("explore", 0)
	u.Add("task", -1)

	got := u.Snapshot()
	if len(got) != 1 || got[0].Name != "quick" || got[0].Count != 5 {
		t.Fatalf("snapshot = %+v, want one quick entry of 5", got)
	}
}

// A nil counter is what a bare test model carries; it must stay inert rather
// than panic a render or a background run.
func TestAgentUsageNilIsInert(t *testing.T) {
	var u *AgentUsage
	u.Add("quick", 3)
	if got := u.Snapshot(); got != nil {
		t.Fatalf("nil snapshot = %+v, want nil", got)
	}
	u.Reset()
}

// Reset starts a new conversation: old counts are gone and the counter is
// usable again.
func TestAgentUsageReset(t *testing.T) {
	u := NewAgentUsage()
	u.Add("quick", 2)
	u.Reset()
	if got := u.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot after reset = %+v, want empty", got)
	}
	u.Add("task", 1)
	if got := u.Snapshot(); len(got) != 1 || got[0].Name != "task" {
		t.Fatalf("snapshot after reset+add = %+v", got)
	}
}

// Snapshot returns a copy: a background run adding during a render must not
// mutate the slice the render holds.
func TestAgentUsageSnapshotIsACopy(t *testing.T) {
	u := NewAgentUsage()
	u.Add("quick", 1)
	snap := u.Snapshot()
	u.Add("quick", 1)
	if snap[0].Count != 1 {
		t.Fatalf("held snapshot mutated to %d", snap[0].Count)
	}
}
