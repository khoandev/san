package transcript

import (
	"context"
	"testing"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/advisor"
)

// advisorReview builds a review that passes the domain's validation, so a test
// states only what it is about.
func advisorReview(id string, trigger advisor.Trigger, target advisor.Target, status advisor.ReviewStatus, findings ...advisor.Finding) advisor.Review {
	return advisor.Review{
		ID:        id,
		Trigger:   trigger,
		Target:    target,
		Status:    status,
		DiffHash:  "hash-" + id,
		Model:     "stub-reviewer",
		Findings:  findings,
		StartedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC),
	}
}

func TestProjectAdvisorReviews(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	taskReview := advisorReview("review-1", advisor.TriggerSubagentTask,
		advisor.Target{Kind: advisor.TargetTask, ID: "task-1"}, advisor.StatusChangesRequired,
		advisor.Finding{ID: "f-1", Severity: advisor.SeverityP1, Title: "missing error handling", Status: advisor.FindingOpen},
		advisor.Finding{ID: "f-2", Severity: advisor.SeverityP3, Title: "naming", Status: advisor.FindingOpen},
	)
	mergeReview := advisorReview("review-2", advisor.TriggerBeforeMerge,
		advisor.Target{Kind: advisor.TargetDiff, Base: "main", Head: "feature"}, advisor.StatusPassed)

	tr, err := Project([]Record{
		{SessionID: "s", Time: now, Type: SessionStarted},
		{SessionID: "s", Time: now, Type: AdvisorReviewed, AdvisorReview: &taskReview},
		{SessionID: "s", Time: now.Add(time.Second), Type: AdvisorFindingUpdated, AdvisorFinding: &AdvisorFindingRecord{
			ReviewID: "review-1", FindingID: "f-1", Status: advisor.FindingAddressed, Note: "fixed in 4f2a",
		}},
		{SessionID: "s", Time: now.Add(2 * time.Second), Type: AdvisorReviewed, AdvisorReview: &mergeReview},
	})
	if err != nil {
		t.Fatalf("Project(): %v", err)
	}
	reviews := tr.State.Advisor.Reviews
	if len(reviews) != 2 {
		t.Fatalf("reviews = %d, want 2", len(reviews))
	}
	if reviews[0].ID != "review-1" || reviews[1].ID != "review-2" {
		t.Fatalf("reviews are not in the order they happened: %+v", reviews)
	}
	first := reviews[0]
	if first.Trigger != advisor.TriggerSubagentTask || first.Target.ID != "task-1" || first.DiffHash != "hash-review-1" {
		t.Errorf("review identity = %+v", first)
	}
	finding, ok := first.Finding("f-1")
	if !ok {
		t.Fatal("finding f-1 is missing")
	}
	if finding.Status != advisor.FindingAddressed || finding.Note != "fixed in 4f2a" {
		t.Errorf("f-1 = %+v, want the recorded move", finding)
	}
	if other, _ := first.Finding("f-2"); other.Status != advisor.FindingOpen {
		t.Errorf("f-2 = %+v, want it untouched", other)
	}
	if second := reviews[1]; second.Status != advisor.StatusPassed || second.Target.Head != "feature" {
		t.Errorf("second review = %+v", second)
	}
}

// A review is history: replaying its record must not duplicate it.
func TestProjectAdvisorReviewReplayIsIdempotent(t *testing.T) {
	review := advisorReview("review-1", advisor.TriggerUserRequest, advisor.Target{Kind: advisor.TargetSession}, advisor.StatusPassed)
	tr, err := Project([]Record{
		{SessionID: "s", Type: SessionStarted},
		{SessionID: "s", Type: AdvisorReviewed, AdvisorReview: &review},
		{SessionID: "s", Type: AdvisorReviewed, AdvisorReview: &review},
	})
	if err != nil {
		t.Fatalf("Project(): %v", err)
	}
	if got := len(tr.State.Advisor.Reviews); got != 1 {
		t.Fatalf("reviews = %d, want 1", got)
	}
}

// A move naming a review the log does not hold is dropped: a resume must open
// the conversation, not fail on a missing record.
func TestProjectAdvisorFindingUpdateWithoutReview(t *testing.T) {
	tr, err := Project([]Record{
		{SessionID: "s", Type: SessionStarted},
		{SessionID: "s", Type: AdvisorFindingUpdated, AdvisorFinding: &AdvisorFindingRecord{
			ReviewID: "gone", FindingID: "f-1", Status: advisor.FindingDismissed, Note: "n/a",
		}},
	})
	if err != nil {
		t.Fatalf("Project(): %v", err)
	}
	if len(tr.State.Advisor.Reviews) != 0 {
		t.Fatalf("an orphan move created history: %+v", tr.State.Advisor)
	}
}

// Compaction rewrites the conversation, never the advisor history.
func TestProjectAdvisorReviewsSurviveCompaction(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	review := advisorReview("review-1", advisor.TriggerPlanStep,
		advisor.Target{Kind: advisor.TargetStep, ID: "step-2"}, advisor.StatusPassedWithNotes,
		advisor.Finding{ID: "f-1", Severity: advisor.SeverityP2, Title: "duplication", Status: advisor.FindingOpen})
	tr, err := Project([]Record{
		{SessionID: "s", Time: now, Type: SessionStarted},
		{SessionID: "s", Time: now, Type: MessageAppended, Message: &ai.Message{ID: "m1", Role: "user", Content: ai.Content{ai.TextBlock("start")}}},
		{SessionID: "s", Time: now, Type: AdvisorReviewed, AdvisorReview: &review},
		{SessionID: "s", Time: now, Type: SessionCompacted, Compaction: &Compaction{MessageID: "m1"}},
		{SessionID: "s", Time: now, Type: ConversationReplaced, Messages: []ai.Message{
			{ID: "m1", Role: "user", Content: ai.Content{ai.TextBlock("summary")}},
			{ID: "m2", Role: "assistant", Content: ai.Content{ai.TextBlock("next")}},
		}},
	})
	if err != nil {
		t.Fatalf("Project(): %v", err)
	}
	if len(tr.Messages) != 2 {
		t.Fatalf("resume kept %d messages, want the post-compaction chain", len(tr.Messages))
	}
	reviews := tr.State.Advisor.Reviews
	if len(reviews) != 1 || reviews[0].ID != "review-1" {
		t.Fatalf("compaction dropped the advisor history: %+v", reviews)
	}
	if got := reviews[0].UnresolvedCounts(); got.P2 != 1 {
		t.Errorf("findings after compaction = %+v, want the P2 still open", got)
	}
}

// The whole point of the records: write a session, read it back from a fresh
// store, and get the same history.
func TestAdvisorReviewsRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewStore(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if err := store.Start(ctx, StartCommand{SessionID: "s", Time: now}); err != nil {
		t.Fatal(err)
	}
	review := advisorReview("review-1", advisor.TriggerBeforeMerge,
		advisor.Target{Kind: advisor.TargetDiff, Base: "main", Head: "feature", Files: []string{"internal/api.go"}},
		advisor.StatusBlocked,
		advisor.Finding{ID: "f-1", Severity: advisor.SeverityP0, Title: "leaks a token", File: "internal/api.go", Line: 42, Status: advisor.FindingOpen},
		advisor.Finding{ID: "f-2", Severity: advisor.SeverityP2, Title: "duplication", Status: advisor.FindingOpen},
	)
	review.Severity = advisor.SeverityP0
	appendRecord(t, store, Record{SessionID: "s", Time: now, Type: AdvisorReviewed, AdvisorReview: &review})
	appendRecord(t, store, Record{SessionID: "s", Time: now, Type: AdvisorFindingUpdated, AdvisorFinding: &AdvisorFindingRecord{
		ReviewID: "review-1", FindingID: "f-2", Status: advisor.FindingDeferred, Note: "follow-up task", UpdatedAt: now,
	}})
	settings := `{"enable":true,"triggers":{"beforeMerge":true}}`
	appendRecord(t, store, Record{SessionID: "s", Time: now, Type: SessionStatePatched, State: &StatePatch{AdvisorSettings: &settings}})

	// A second store reads the log cold, as a resume does.
	resumed, err := NewStore(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := resumed.Load(ctx, "s")
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.State.AdvisorSettings != settings {
		t.Errorf("advisor settings = %q, want the session override", loaded.State.AdvisorSettings)
	}
	reviews := loaded.State.Advisor.Reviews
	if len(reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(reviews))
	}
	got := reviews[0]
	if got.ID != review.ID || got.Trigger != review.Trigger || got.Status != review.Status || got.Severity != advisor.SeverityP0 {
		t.Errorf("review = %+v", got)
	}
	if got.DiffHash != review.DiffHash || got.Model != review.Model || got.StartedAt.IsZero() {
		t.Errorf("review identity lost: %+v", got)
	}
	if got.Target.Base != "main" || got.Target.Head != "feature" || len(got.Target.Files) != 1 {
		t.Errorf("target lost: %+v", got.Target)
	}
	if len(got.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(got.Findings))
	}
	// Findings are ordered most severe first, as the store orders them.
	if got.Findings[0].Severity != advisor.SeverityP0 || got.Findings[0].Line != 42 {
		t.Errorf("first finding = %+v", got.Findings[0])
	}
	if got.Findings[1].Status != advisor.FindingDeferred || got.Findings[1].Note != "follow-up task" {
		t.Errorf("deferred finding = %+v, want the recorded move", got.Findings[1])
	}
	if got.Findings[1].UpdatedAt.IsZero() {
		t.Error("the move lost its timestamp")
	}
}

// Twenty reviews write twenty records: history grows by one line per review,
// never by a copy of everything before it.
func TestAdvisorReviewsDoNotDuplicateHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewStore(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Start(ctx, StartCommand{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		review := advisorReview("review-"+string(rune('a'+i)), advisor.TriggerUserRequest,
			advisor.Target{Kind: advisor.TargetDiff}, advisor.StatusPassed)
		appendRecord(t, store, Record{SessionID: "s", Type: AdvisorReviewed, AdvisorReview: &review})
	}

	records := readRawRecordMaps(t, dir, "s")
	reviewed := 0
	for _, rec := range records {
		if rec["type"] != AdvisorReviewed {
			continue
		}
		reviewed++
		payload, ok := rec["advisorReview"].(map[string]any)
		if !ok {
			t.Fatalf("advisor.reviewed payload is not a review: %v", rec["advisorReview"])
		}
		if _, has := payload["reviews"]; has {
			t.Fatal("an advisor.reviewed record carries the whole history, not one review")
		}
		if payload["id"] == nil {
			t.Fatalf("advisor.reviewed record lost the review id: %v", payload)
		}
	}
	if reviewed != 20 {
		t.Fatalf("advisor.reviewed records = %d, want 20", reviewed)
	}
	if len(records) != 21 {
		t.Fatalf("transcript records = %d, want 21 (one start, twenty reviews)", len(records))
	}
}

// A review is history, not listing data: a session that ran one must still show
// up in the picker, and the picker's activity time must follow the review.
func TestAdvisorReviewsDoNotHideTheSessionFromThePicker(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if err := store.Start(ctx, StartCommand{SessionID: "s", Time: start}); err != nil {
		t.Fatal(err)
	}
	review := advisorReview("review-1", advisor.TriggerBeforeMerge,
		advisor.Target{Kind: advisor.TargetDiff}, advisor.StatusChangesRequired,
		advisor.Finding{ID: "f-1", Severity: advisor.SeverityP1, Title: "no retry", Status: advisor.FindingOpen})
	reviewedAt := start.Add(time.Minute)
	appendRecord(t, store, Record{SessionID: "s", Time: reviewedAt, Type: AdvisorReviewed, AdvisorReview: &review})

	items, err := store.List(ctx, "p", ListOptions{})
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(items) != 1 || items[0].ID != "s" {
		t.Fatalf("a session with an advisor review is missing from the picker: %+v", items)
	}
	if !items[0].UpdatedAt.Equal(reviewedAt) {
		t.Errorf("updatedAt = %v, want the review's time %v", items[0].UpdatedAt, reviewedAt)
	}

	// A finding decision moves the session's activity time too, without adding
	// anything the picker shows.
	decidedAt := reviewedAt.Add(time.Minute)
	appendRecord(t, store, Record{SessionID: "s", Time: decidedAt, Type: AdvisorFindingUpdated, AdvisorFinding: &AdvisorFindingRecord{
		ReviewID: review.ID, FindingID: "f-1", Status: advisor.FindingDeferred, Note: "later", UpdatedAt: decidedAt,
	}})
	items, err = store.List(ctx, "p", ListOptions{})
	if err != nil {
		t.Fatalf("List() after a decision: %v", err)
	}
	if len(items) != 1 || !items[0].UpdatedAt.Equal(decidedAt) {
		t.Fatalf("picker after a finding decision = %+v, want the decision's time", items)
	}

	// The listing is cheap, not lossy: loading the session still replays the
	// review and the decision.
	loaded, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if len(loaded.State.Advisor.Reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(loaded.State.Advisor.Reviews))
	}
	finding, ok := loaded.State.Advisor.Reviews[0].Finding("f-1")
	if !ok || finding.Status != advisor.FindingDeferred || finding.Note != "later" {
		t.Fatalf("finding = %+v, want the recorded decision", finding)
	}
}

func appendRecord(t *testing.T, store *Store, rec Record) {
	t.Helper()
	if err := store.Append(context.Background(), rec); err != nil {
		t.Fatalf("Append(%s): %v", rec.Type, err)
	}
}
