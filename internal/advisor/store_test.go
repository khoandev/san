package advisor

import (
	"testing"
	"time"
)

func testReview(target Target, status ReviewStatus, levels ...Severity) Review {
	r := Review{Trigger: TriggerBeforeMerge, Target: target, Status: status, Findings: findings(levels...)}
	r.ApplyPolicy(Policy{BlockMerge: SeverityP0, BlockTaskCompletion: SeverityP0})
	return r
}

func TestStoreAppendKeepsHistory(t *testing.T) {
	t.Parallel()

	store := NewStore()
	target := Target{Kind: TargetDiff, Label: "working tree"}

	first, err := store.Append(testReview(target, "", SeverityP2))
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if first.ID == "" {
		t.Fatal("Append() did not assign a review id")
	}
	if first.Findings[0].ID == "" {
		t.Fatal("Append() did not assign a finding id")
	}
	if first.StartedAt.IsZero() {
		t.Fatal("Append() did not stamp a start time")
	}

	second, err := store.Append(testReview(target, "", SeverityP0))
	if err != nil {
		t.Fatalf("second Append() error = %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("two reviews share an id")
	}

	reviews := store.Reviews()
	if len(reviews) != 2 {
		t.Fatalf("Reviews() = %d, want 2 (a re-run must not replace the previous review)", len(reviews))
	}
	if reviews[0].ID != first.ID || reviews[1].ID != second.ID {
		t.Fatalf("history order changed: %q then %q", reviews[0].ID, reviews[1].ID)
	}
	if reviews[0].Status != StatusPassedWithNotes {
		t.Errorf("earlier review mutated: status = %q", reviews[0].Status)
	}
}

func TestStoreAppendRejectsIncoherentReview(t *testing.T) {
	t.Parallel()

	store := NewStore()
	if _, err := store.Append(Review{Trigger: TriggerBeforeMerge, Target: Target{Kind: "nope"}, Status: StatusPassed}); err == nil {
		t.Fatal("incoherent review accepted")
	}
	if len(store.Reviews()) != 0 {
		t.Fatal("a rejected review entered the store")
	}
	if _, err := (*Store)(nil).Append(testReview(Target{Kind: TargetDiff}, StatusPassed)); err == nil {
		t.Fatal("nil store accepted a review")
	}
}

func TestStoreLatestAndLatestFor(t *testing.T) {
	t.Parallel()

	store := NewStore()
	if _, ok := store.Latest(); ok {
		t.Fatal("empty store reported a latest review")
	}
	if _, ok := store.LatestFor(Target{Kind: TargetTask, ID: "task-1"}); ok {
		t.Fatal("empty store reported a target review")
	}

	diffReview, _ := store.Append(testReview(Target{Kind: TargetDiff}, StatusPassed))
	taskReview, _ := store.Append(testReview(Target{Kind: TargetTask, ID: "task-1"}, StatusPassed, SeverityP2))
	newerDiff, _ := store.Append(testReview(Target{Kind: TargetDiff}, StatusPassed, SeverityP3))

	latest, ok := store.Latest()
	if !ok || latest.ID != newerDiff.ID {
		t.Fatalf("Latest() = %q, want %q", latest.ID, newerDiff.ID)
	}
	got, ok := store.LatestFor(Target{Kind: TargetDiff})
	if !ok || got.ID != newerDiff.ID {
		t.Fatalf("LatestFor(diff) = %q, want %q", got.ID, newerDiff.ID)
	}
	if got, ok := store.LatestFor(Target{Kind: TargetTask, ID: "task-1"}); !ok || got.ID != taskReview.ID {
		t.Fatalf("LatestFor(task-1) = %q, want %q", got.ID, taskReview.ID)
	}
	if _, ok := store.LatestFor(Target{Kind: TargetTask, ID: "task-2"}); ok {
		t.Fatal("LatestFor reported a review for an unreviewed target")
	}
	if diffReview.ID == "" {
		t.Fatal("first diff review lost its id")
	}
}

func TestStoreReviewForMatchesDiffHash(t *testing.T) {
	t.Parallel()

	store := NewStore()
	target := Target{Kind: TargetDiff}
	older := testReview(target, StatusPassed)
	older.DiffHash = "aaa"
	if _, err := store.Append(older); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	newer := testReview(target, StatusPassed)
	newer.DiffHash = "bbb"
	if _, err := store.Append(newer); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	if got, ok := store.ReviewFor(target, "aaa"); !ok || got.DiffHash != "aaa" {
		t.Fatalf("ReviewFor(aaa) = (%q, %v), want the older review", got.DiffHash, ok)
	}
	if _, ok := store.ReviewFor(target, "ccc"); ok {
		t.Fatal("ReviewFor matched an unreviewed diff hash")
	}
	if _, ok := store.ReviewFor(Target{Kind: TargetSession}, "aaa"); ok {
		t.Fatal("ReviewFor matched a different target kind")
	}
}

func TestStoreUpdateFinding(t *testing.T) {
	t.Parallel()

	store := NewStore()
	review, err := store.Append(testReview(Target{Kind: TargetDiff}, "", SeverityP0, SeverityP3))
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	blocker := review.Findings[0]
	minor := review.Findings[1]

	if err := store.UpdateFinding(review.ID, blocker.ID, FindingDismissed, ""); err == nil {
		t.Fatal("dismissing a P0 without a reason was accepted")
	}
	if err := store.UpdateFinding(review.ID, blocker.ID, FindingDismissed, "false positive: already validated"); err != nil {
		t.Fatalf("UpdateFinding() error = %v", err)
	}

	latest, ok := store.Latest()
	if !ok {
		t.Fatal("review disappeared")
	}
	updated, ok := latest.Finding(blocker.ID)
	if !ok || updated.Status != FindingDismissed || updated.Note == "" || updated.UpdatedAt.IsZero() {
		t.Fatalf("finding not updated: %+v", updated)
	}
	if updated.Severity != SeverityP0 || updated.Title != blocker.Title {
		t.Errorf("resolution rewrote the finding's substance: %+v", updated)
	}
	if kept, _ := latest.Finding(minor.ID); kept.Status != FindingOpen {
		t.Errorf("sibling finding changed to %q", kept.Status)
	}
	// The verdict stays as reviewed: only the finding's status is mutable.
	if latest.Status != StatusBlocked {
		t.Errorf("review status changed to %q on finding update", latest.Status)
	}

	if err := store.UpdateFinding(review.ID, "missing", FindingOpen, ""); err == nil {
		t.Error("unknown finding accepted")
	}
	if err := store.UpdateFinding("missing", blocker.ID, FindingOpen, ""); err == nil {
		t.Error("unknown review accepted")
	}
	if err := (*Store)(nil).UpdateFinding(review.ID, blocker.ID, FindingOpen, ""); err == nil {
		t.Error("nil store accepted an update")
	}
}

func TestStoreOpenFindingsUsesLatestReviewPerTarget(t *testing.T) {
	t.Parallel()

	store := NewStore()
	diff := Target{Kind: TargetDiff}
	task := Target{Kind: TargetTask, ID: "task-1"}

	if _, err := store.Append(testReview(diff, "", SeverityP0)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	// Re-reviewing the same target supersedes the earlier findings.
	if _, err := store.Append(testReview(diff, "", SeverityP3)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if _, err := store.Append(testReview(task, "", SeverityP1)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	open := store.OpenFindings()
	if len(open) != 2 {
		t.Fatalf("OpenFindings() = %d, want 2 (latest diff P3 + task P1)", len(open))
	}
	if open[0].Finding.Severity != SeverityP1 {
		t.Errorf("first finding = %q, want the P1 first", open[0].Finding.Severity)
	}
	if open[1].Finding.Severity != SeverityP3 {
		t.Errorf("second finding = %q, want the superseding P3, not the superseded P0", open[1].Finding.Severity)
	}
	for _, ref := range open {
		if ref.ReviewID == "" {
			t.Error("finding reference lost its review id")
		}
	}
}

func TestStoreCountsReadsLatestReview(t *testing.T) {
	t.Parallel()

	store := NewStore()
	if got := store.Counts(); got != (Counts{}) {
		t.Fatalf("Counts() on an empty store = %+v", got)
	}
	if _, err := store.Append(testReview(Target{Kind: TargetDiff}, "", SeverityP0, SeverityP2)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if got := store.Counts(); got != (Counts{P0: 1, P2: 1}) {
		t.Fatalf("Counts() = %+v, want P0:1 P2:1", got)
	}
}

func TestStoreSnapshotIsDetached(t *testing.T) {
	t.Parallel()

	store := NewStore()
	review, err := store.Append(testReview(Target{Kind: TargetDiff, Files: []string{"a.go"}}, "", SeverityP2))
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	snap := store.Snapshot()
	snap.Reviews[0].Status = StatusBlocked
	snap.Reviews[0].Findings[0].Title = "tampered"
	snap.Reviews[0].Target.Files[0] = "tampered.go"

	latest, _ := store.Latest()
	if latest.Status != StatusPassedWithNotes || latest.Findings[0].Title == "tampered" {
		t.Fatalf("snapshot aliased live state: %+v", latest)
	}
	if latest.Target.Files[0] != "a.go" {
		t.Fatalf("snapshot aliased the target's file list: %v", latest.Target.Files)
	}
	if review.ID != latest.ID {
		t.Fatalf("store rewrote the review id")
	}
}

func TestStoreRestoreAndReset(t *testing.T) {
	t.Parallel()

	store := NewStore()
	restored := State{Reviews: []Review{
		{
			ID:        "review-1",
			Trigger:   TriggerPlanStep,
			Target:    Target{Kind: TargetStep, ID: "step-1"},
			Status:    StatusBlocked,
			Findings:  []Finding{{Severity: SeverityP0}},
			DiffHash:  "abc",
			StartedAt: time.Unix(100, 0),
		},
	}}
	store.Restore(restored)

	got, ok := store.Latest()
	if !ok {
		t.Fatal("Restore() lost the review")
	}
	if got.Findings[0].ID == "" {
		t.Error("Restore() did not assign a missing finding id")
	}
	if got.Findings[0].Status != FindingOpen {
		t.Errorf("Restore() finding status = %q, want open", got.Findings[0].Status)
	}
	if got.StartedAt != time.Unix(100, 0) {
		t.Errorf("Restore() rewrote the start time to %v", got.StartedAt)
	}

	// Mutating the restored value must not reach the store.
	restored.Reviews[0].Status = StatusPassed
	if latest, _ := store.Latest(); latest.Status != StatusBlocked {
		t.Fatalf("Restore() kept a reference to the caller's state: %q", latest.Status)
	}

	store.Reset()
	if _, ok := store.Latest(); ok {
		t.Fatal("Reset() left a review behind")
	}
	if got := store.Snapshot(); !got.Empty() {
		t.Fatalf("Snapshot() after Reset() = %+v", got)
	}
}

func TestStateEmptyAndClone(t *testing.T) {
	t.Parallel()

	var empty State
	if !empty.Empty() {
		t.Error("zero State reported non-empty")
	}
	original := State{Reviews: []Review{{
		ID:       "review-1",
		Target:   Target{Kind: TargetTask, ID: "task-1", Files: []string{"a.go"}},
		Findings: []Finding{{ID: "finding-1", Severity: SeverityP1}},
	}}}
	clone := original.Clone()
	clone.Reviews[0].Findings[0].Severity = SeverityP3
	clone.Reviews[0].Target.Files[0] = "b.go"
	if original.Reviews[0].Findings[0].Severity != SeverityP1 || original.Reviews[0].Target.Files[0] != "a.go" {
		t.Fatal("Clone() shared storage with the original")
	}
}

// The transcript folds its records through State, so the fold must be
// idempotent and must not write through the receiver's backing array.
func TestStateAppendIsAppendOnly(t *testing.T) {
	t.Parallel()

	original := State{}
	first := testReview(Target{Kind: TargetDiff}, "", SeverityP2)
	first.ID = "review-1"
	state, stored, err := original.Append(first)
	if err != nil {
		t.Fatalf("State.Append() error = %v", err)
	}
	if stored.ID != "review-1" || len(stored.Findings) != 1 || stored.Findings[0].ID == "" {
		t.Fatalf("stored review = %+v, want the normalized review", stored)
	}
	if !original.Empty() {
		t.Fatalf("Append() mutated the receiver: %+v", original)
	}

	// Replaying the same record adds nothing.
	replayed, _, err := state.Append(first)
	if err != nil {
		t.Fatalf("replayed Append() error = %v", err)
	}
	if len(replayed.Reviews) != 1 {
		t.Fatalf("replay duplicated history: %d reviews", len(replayed.Reviews))
	}

	// A second review is appended after the first, never in place of it.
	second := testReview(Target{Kind: TargetTask, ID: "task-1"}, "", SeverityP0)
	second.ID = "review-2"
	grown, _, err := state.Append(second)
	if err != nil {
		t.Fatalf("second Append() error = %v", err)
	}
	if len(grown.Reviews) != 2 || grown.Reviews[0].ID != "review-1" || grown.Reviews[1].ID != "review-2" {
		t.Fatalf("history = %+v", grown.Reviews)
	}
	if len(state.Reviews) != 1 {
		t.Fatalf("a later Append() changed the earlier state: %d reviews", len(state.Reviews))
	}
}

// A finding move returns a new state; the state a reader already holds keeps
// the finding as it was.
func TestStateUpdateFindingCopies(t *testing.T) {
	t.Parallel()

	review := testReview(Target{Kind: TargetDiff}, "", SeverityP1)
	review.ID = "review-1"
	review.Findings[0].ID = "finding-1"
	before := State{Reviews: []Review{review}}

	after, err := before.UpdateFinding("review-1", "finding-1", FindingDismissed, "false positive", time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("UpdateFinding() error = %v", err)
	}
	if got := after.Reviews[0].Findings[0]; got.Status != FindingDismissed || got.Note != "false positive" {
		t.Fatalf("finding = %+v, want the move", got)
	}
	if got := before.Reviews[0].Findings[0]; got.Status != FindingOpen {
		t.Fatalf("the earlier state changed: %+v", got)
	}
	if after.Reviews[0].Findings[0].UpdatedAt.IsZero() {
		t.Error("the move lost its timestamp")
	}

	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if _, err := before.UpdateFinding("missing", "finding-1", FindingAddressed, "", at); err == nil {
		t.Error("a missing review was accepted")
	}
	if _, err := before.UpdateFinding("review-1", "missing", FindingAddressed, "", at); err == nil {
		t.Error("a missing finding was accepted")
	}
	if _, err := before.UpdateFinding("review-1", "finding-1", FindingDismissed, "", at); err == nil {
		t.Error("dismissing a P1 without a reason was accepted")
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	t.Parallel()

	var store *Store
	if _, ok := store.Latest(); ok {
		t.Error("nil store reported a review")
	}
	if _, ok := store.ReviewFor(Target{Kind: TargetDiff}, "x"); ok {
		t.Error("nil store matched a review")
	}
	if got := store.OpenFindings(); got != nil {
		t.Errorf("nil store returned findings: %+v", got)
	}
	if got := store.Counts(); got != (Counts{}) {
		t.Errorf("nil store returned counts: %+v", got)
	}
	if got := store.Reviews(); got != nil {
		t.Errorf("nil store returned reviews: %+v", got)
	}
	if got := store.Snapshot(); !got.Empty() {
		t.Errorf("nil store returned a snapshot: %+v", got)
	}
	store.Restore(State{})
	store.Reset()
}
