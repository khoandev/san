package input

// The Advisor Panel: the snapshot it renders, the filters that narrow it, the
// finding detail, and the one rule it enforces itself — a dismissal is a
// recorded decision, so it cannot be emitted without a reason.

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/advisor"
)

// advisorKey builds the keypress a person's key produces.
func advisorKey(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func typeAdvisorText(p *AdvisorPanel, text string) {
	for _, r := range text {
		p.HandleKeypress(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// advisorTestReview is a blocked merge review with one finding at every level
// and every finding status, so a filter test can tell them apart.
func advisorTestReview() advisor.Review {
	now := time.Now()
	return advisor.Review{
		ID:         "review-1",
		Trigger:    advisor.TriggerBeforeMerge,
		Target:     advisor.Target{Kind: advisor.TargetMerge, ID: "pre-merge", Label: "git merge origin/main"},
		Status:     advisor.StatusBlocked,
		Severity:   advisor.SeverityP0,
		Summary:    "Leaks the API token in the client.",
		DiffHash:   "hash-1",
		Model:      "stub-model",
		StartedAt:  now,
		FinishedAt: now,
		Findings: []advisor.Finding{
			{
				ID: "f-1", Severity: advisor.SeverityP0, Title: "Leaks the token",
				Detail: "Logs the API key at startup.", File: "internal/api.go", Line: 42,
				Evidence: "log(token)", Impact: "Anyone with the logs can impersonate the service.",
				Suggestion: "Drop the log line.", Status: advisor.FindingOpen,
			},
			{ID: "f-2", Severity: advisor.SeverityP1, Title: "No retry test", File: "internal/api.go", Status: advisor.FindingOpen},
			{ID: "f-3", Severity: advisor.SeverityP2, Title: "Slow path", File: "internal/store.go", Status: advisor.FindingDismissed, Note: "accepted for now"},
			{ID: "f-4", Severity: advisor.SeverityP3, Title: "Naming", File: "internal/store.go", Status: advisor.FindingAddressed},
		},
	}
}

func openAdvisorPanel(view AdvisorView, width, height int) AdvisorPanel {
	p := NewAdvisorPanel()
	p.Enter(view, width, height)
	return p
}

func advisorState(reviews ...advisor.Review) advisor.State {
	return advisor.State{Reviews: reviews}
}

// An empty history is a state the panel has to render: it says so, and offers
// nothing it cannot do.
func TestAdvisorPanelEmptyState(t *testing.T) {
	p := openAdvisorPanel(AdvisorView{}, 100, 40)
	if !p.IsActive() {
		t.Fatal("Enter did not activate the panel")
	}
	view := p.Render()
	for _, want := range []string{"Advisor", "No reviews in this session yet", "/advisor diff"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty panel is missing %q:\n%s", want, view)
		}
	}
	if got := p.Actions(); len(got) != 2 || got[0] != "Re-run review" || got[1] != "Close" {
		t.Fatalf("actions = %v, want only re-run and close", got)
	}
}

// The header is the whole verdict at a glance: what ran, what it looked at,
// how it ended, the highest level, and the per-level tally.
func TestAdvisorPanelRendersTheLatestVerdict(t *testing.T) {
	review := advisorTestReview()
	p := openAdvisorPanel(AdvisorView{State: advisorState(review), Merge: advisor.MergeBlocked}, 100, 40)

	view := p.Render()
	for _, want := range []string{
		"Advisor", "blocked", "before_merge", "merge · git merge origin/main",
		"review-1", "P0 1", "P1 1", "Highest", "P0", "blocked",
		"Leaks the token", "No retry test", "FINDINGS  4 of 4",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel is missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "2 resolved") {
		t.Errorf("panel does not say how many findings were already handled:\n%s", view)
	}
	if !strings.Contains(view, "Leaks the token") {
		t.Errorf("the finding list is empty:\n%s", view)
	}
}

// A merge review carries the recommendation the policy resolved, and the panel
// states it in the policy's own words.
func TestAdvisorPanelStatesTheMergeRecommendation(t *testing.T) {
	for _, tc := range []struct {
		recommendation advisor.Recommendation
		want           string
	}{
		{advisor.SafeToMerge, "safe to merge"},
		{advisor.ReviewRequired, "review required"},
		{advisor.MergeBlocked, "blocked"},
	} {
		review := advisorTestReview()
		p := openAdvisorPanel(AdvisorView{State: advisorState(review), Merge: tc.recommendation}, 100, 40)
		if !strings.Contains(p.Render(), "Merge") || !strings.Contains(p.Render(), tc.want) {
			t.Errorf("recommendation %q is not rendered:\n%s", tc.recommendation, p.Render())
		}
	}

	// A review of something that is not a merge shows no merge verdict.
	review := advisorTestReview()
	review.Target = advisor.Target{Kind: advisor.TargetTask, ID: "task-1", Label: "task-1"}
	review.Trigger = advisor.TriggerSubagentTask
	p := openAdvisorPanel(AdvisorView{State: advisorState(review), Merge: advisor.SafeToMerge}, 100, 40)
	if strings.Contains(p.Render(), "safe to merge") {
		t.Errorf("a task review claimed a merge verdict:\n%s", p.Render())
	}
}

// Each filter narrows the list without changing the review it describes, and
// every filter has an "all" state to come back to.
func TestAdvisorPanelFilters(t *testing.T) {
	p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, 100, 40)

	if got := len(p.visible()); got != 4 {
		t.Fatalf("unfiltered findings = %d, want 4", got)
	}

	// Severity: P0 → only the leak.
	p.severity = advisor.SeverityP0
	p.invalidate()
	if got := p.visible(); len(got) != 1 || got[0].Title != "Leaks the token" {
		t.Fatalf("P0 filter = %+v, want the leak", got)
	}
	view := p.Render()
	if !strings.Contains(view, "severity P0") || strings.Contains(view, "No retry test") {
		t.Errorf("severity filter is not reflected in the list:\n%s", view)
	}

	// Status: dismissed → only the accepted finding.
	p.severity, p.status = "", advisor.FindingDismissed
	p.invalidate()
	if got := p.visible(); len(got) != 1 || got[0].Title != "Slow path" {
		t.Fatalf("dismissed filter = %+v, want the dismissed finding", got)
	}

	// File: store.go → the two findings in that file.
	p.status, p.file = "", "internal/store.go"
	p.invalidate()
	if got := len(p.visible()); got != 2 {
		t.Fatalf("file filter = %d findings, want 2", got)
	}

	// The keyboard cycles each filter, including back to all.
	p.file = ""
	p.HandleKeypress(advisorKey("s"))
	if p.severity != advisor.SeverityP0 {
		t.Errorf("s did not filter to P0: %q", p.severity)
	}
	p.HandleKeypress(advisorKey("t"))
	if p.status != advisor.FindingOpen {
		t.Errorf("t did not filter to open: %q", p.status)
	}
	p.HandleKeypress(advisorKey("f"))
	if p.file != "internal/api.go" {
		t.Errorf("f did not filter to the first file: %q", p.file)
	}
	for range 2 {
		p.HandleKeypress(advisorKey("f"))
	}
	if p.file != "" {
		t.Errorf("file filter did not cycle back to all: %q", p.file)
	}

	// A filter that matches nothing says so rather than showing an empty list.
	p.severity, p.status, p.file = advisor.SeverityP0, advisor.FindingAddressed, ""
	p.invalidate()
	if view := p.Render(); !strings.Contains(view, "No findings match these filters") {
		t.Errorf("empty filter state is unclear:\n%s", view)
	}
}

// Selecting a finding shows everything the review recorded about it, and esc
// returns to the list without closing the panel.
func TestAdvisorPanelFindingDetail(t *testing.T) {
	p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, 100, 40)
	p.HandleKeypress(advisorKey("enter"))
	if !p.detail {
		t.Fatal("enter did not open the finding detail")
	}
	view := p.Render()
	for _, want := range []string{
		"FINDING", "Leaks the token", "internal/api.go:42",
		"Logs the API key at startup.", "log(token)",
		"Anyone with the logs", "Drop the log line.", "open",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail is missing %q:\n%s", want, view)
		}
	}
	p.HandleKeypress(advisorKey("esc"))
	if p.detail {
		t.Fatal("esc did not return to the list")
	}
	if !p.IsActive() {
		t.Fatal("esc from the detail closed the whole panel")
	}
	// A second esc closes it.
	p.HandleKeypress(advisorKey("esc"))
	if p.IsActive() {
		t.Fatal("esc did not close the panel")
	}
}

// The panel has to fit a small terminal and a wide one: the same content, no
// panic, and the actions still reachable.
func TestAdvisorPanelRendersAtEveryWidth(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120, 200} {
		for _, height := range []int{16, 30, 50} {
			p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, width, height)
			view := p.Render()
			if strings.TrimSpace(view) == "" {
				t.Fatalf("%dx%d rendered nothing", width, height)
			}
			if !strings.Contains(view, "Advisor") || !strings.Contains(view, "Close") {
				t.Fatalf("%dx%d lost the header or the actions:\n%s", width, height, view)
			}
			p.HandleKeypress(advisorKey("enter"))
			if strings.TrimSpace(p.Render()) == "" {
				t.Fatalf("%dx%d rendered an empty detail", width, height)
			}
		}
	}
}

// Dismissing is a decision, so the panel asks for the reason and refuses to
// emit anything while it is empty.
func TestAdvisorPanelDismissRequiresAReason(t *testing.T) {
	p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, 100, 40)

	if cmd := p.HandleKeypress(advisorKey("d")); cmd != nil {
		t.Fatal("dismiss emitted an action before the reason was asked for")
	}
	if !p.dismissing {
		t.Fatal("dismiss did not open the reason editor")
	}
	if !strings.Contains(p.Render(), "reason is required") {
		t.Errorf("the reason prompt is missing:\n%s", p.Render())
	}

	if cmd := p.HandleKeypress(advisorKey("ctrl+s")); cmd != nil {
		t.Fatal("an empty reason was accepted")
	}
	if !p.dismissing {
		t.Fatal("the empty reason closed the editor")
	}
	if !strings.Contains(p.Render(), "A reason is required to dismiss") {
		t.Errorf("the refusal is not explained:\n%s", p.Render())
	}

	typeAdvisorText(&p, "accepted risk, the token is redacted upstream")
	cmd := p.HandleKeypress(advisorKey("ctrl+s"))
	if cmd == nil {
		t.Fatal("a dismissal with a reason emitted nothing")
	}
	msg, ok := cmd().(AdvisorActionMsg)
	if !ok {
		t.Fatalf("dismissal emitted %T", cmd())
	}
	if msg.Action != AdvisorActionDismiss || msg.ReviewID != "review-1" || msg.FindingID != "f-1" {
		t.Fatalf("dismissal = %+v, want the selected finding", msg)
	}
	if !strings.Contains(msg.Note, "accepted risk") {
		t.Errorf("dismissal note = %q, want the typed reason", msg.Note)
	}
	if p.dismissing {
		t.Error("the editor stayed open after the decision")
	}
}

// Esc leaves the reason editor without emitting a decision.
func TestAdvisorPanelDismissCanBeCancelled(t *testing.T) {
	p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, 100, 40)
	p.HandleKeypress(advisorKey("d"))
	typeAdvisorText(&p, "never mind")
	if cmd := p.HandleKeypress(advisorKey("esc")); cmd != nil {
		t.Fatal("cancelling a dismissal emitted an action")
	}
	if p.dismissing || p.statusLine != "" {
		t.Fatalf("cancel left the editor open (%v) or a status (%q)", p.dismissing, p.statusLine)
	}
	if !strings.Contains(p.Render(), "Leaks the token") {
		t.Error("the panel did not return to the finding list")
	}
}

// Every finding action emits the message the app routes, and each one names the
// review and finding it applies to.
func TestAdvisorPanelActionsEmitMessages(t *testing.T) {
	for _, tc := range []struct {
		key    string
		action AdvisorAction
	}{
		{"x", AdvisorActionFix},
		{"e", AdvisorActionDefer},
		{"u", AdvisorActionFollowUp},
	} {
		p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, 100, 40)
		cmd := p.HandleKeypress(advisorKey(tc.key))
		if cmd == nil {
			t.Fatalf("%s emitted nothing", tc.key)
		}
		msg, ok := cmd().(AdvisorActionMsg)
		if !ok || msg.Action != tc.action {
			t.Fatalf("%s emitted %+v, want %s", tc.key, msg, tc.action)
		}
		if msg.ReviewID != "review-1" || msg.FindingID != "f-1" {
			t.Errorf("%s = %+v, want review-1/f-1", tc.key, msg)
		}
	}

	// Re-run does not need a finding, and close never leaves the panel active.
	p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, 100, 40)
	cmd := p.HandleKeypress(advisorKey("r"))
	if cmd == nil {
		t.Fatal("r emitted nothing")
	}
	if msg, ok := cmd().(AdvisorActionMsg); !ok || msg.Action != AdvisorActionRerun || msg.ReviewID != "review-1" {
		t.Fatalf("r emitted %+v, want a re-run of review-1", msg)
	}
	if cmd := p.HandleKeypress(advisorKey("esc")); cmd != nil {
		t.Fatal("close emitted an action")
	}
	if p.IsActive() {
		t.Fatal("close left the panel open")
	}
}

// With nothing selected there is nothing to fix, dismiss or defer, so those
// actions are not offered.
func TestAdvisorPanelOffersNoFindingActionsWithoutFindings(t *testing.T) {
	review := advisorTestReview()
	review.Findings = nil
	p := openAdvisorPanel(AdvisorView{State: advisorState(review)}, 100, 40)
	actions := p.Actions()
	if len(actions) != 2 || actions[0] != "Re-run review" || actions[1] != "Close" {
		t.Fatalf("actions = %v, want only re-run and close", actions)
	}
	if view := p.Render(); !strings.Contains(view, "This review found nothing") {
		t.Errorf("a clean review is not stated:\n%s", view)
	}
}

// A refresh after an action keeps the person on the review and finding they
// were reading, so a dismissal does not move the panel under them.
func TestAdvisorPanelRefreshKeepsTheSelection(t *testing.T) {
	review := advisorTestReview()
	p := openAdvisorPanel(AdvisorView{State: advisorState(review)}, 100, 40)
	p.HandleKeypress(advisorKey("down")) // select f-2
	selected, ok := p.selected()
	if !ok || selected.ID != "f-2" {
		t.Fatalf("selected = %+v, want f-2", selected)
	}

	updated := review.Clone()
	updated.Findings[1].Status = advisor.FindingDismissed
	updated.Findings[1].Note = "accepted"
	p.Refresh(AdvisorView{State: advisorState(updated)})
	again, ok := p.selected()
	if !ok || again.ID != "f-2" || again.Status != advisor.FindingDismissed {
		t.Fatalf("after refresh = %+v, want the same finding dismissed", again)
	}
	if !strings.Contains(p.Render(), "f-2") && !strings.Contains(p.Render(), "No retry test") {
		t.Error("the refresh lost the review")
	}

	// A new review of the same target replaces the displayed verdict: the
	// panel follows the history's latest review once its own is superseded.
	next := advisorTestReview()
	next.ID = "review-2"
	p.Refresh(AdvisorView{State: advisorState(next)})
	if p.review.ID != "review-2" {
		t.Fatalf("panel shows %q, want the new review", p.review.ID)
	}
}

// A pasted reason lands in the editor, not in the prompt behind the panel.
func TestAdvisorPanelPasteGoesToTheReason(t *testing.T) {
	p := openAdvisorPanel(AdvisorView{State: advisorState(advisorTestReview())}, 100, 40)
	p.HandleKeypress(advisorKey("d"))
	p.HandlePaste("pasted reason\nwith a second line")
	cmd := p.HandleKeypress(advisorKey("ctrl+s"))
	if cmd == nil {
		t.Fatal("the pasted reason was not accepted")
	}
	msg, _ := cmd().(AdvisorActionMsg)
	if !strings.Contains(msg.Note, "pasted reason") || !strings.Contains(msg.Note, "second line") {
		t.Fatalf("note = %q, want the pasted text", msg.Note)
	}
}
