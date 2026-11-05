// The advisor badge: one compact statement of the review layer's latest
// verdict, shown beside the mode and plan badges and silent when there is
// nothing to say.
package conv

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/setting"
)

// The four verdicts each read as their own word, none of them collapse into a
// generic "reviewed".
func TestAdvisorBadgeVerdicts(t *testing.T) {
	cases := []struct {
		status advisor.ReviewStatus
		want   string
	}{
		{advisor.StatusPassed, "advisor · passed"},
		{advisor.StatusPassedWithNotes, "advisor · passed · notes"},
		{advisor.StatusChangesRequired, "advisor · changes required"},
		{advisor.StatusBlocked, "advisor · blocked"},
		{advisor.StatusError, "advisor · error"},
	}
	for _, c := range cases {
		got := stripANSI(RenderModeStatus(OperationModeParams{
			ModelName: "m", Width: 120,
			Advisor: AdvisorBadge{Show: true, Status: c.status},
		}))
		if !strings.Contains(got, c.want) {
			t.Errorf("status %s rendered %q, want %q", c.status, got, c.want)
		}
	}
}

// A running review is the newer news: it replaces the stale verdict, and it
// does not report counts that belong to the review it is about to supersede.
func TestAdvisorBadgeRunningWins(t *testing.T) {
	got := stripANSI(RenderModeStatus(OperationModeParams{
		ModelName: "m", Width: 120,
		Advisor: AdvisorBadge{Show: true, Running: true, Status: advisor.StatusBlocked, P0: 3},
	}))
	if !strings.Contains(got, "advisor · reviewing") {
		t.Fatalf("badge = %q, want the running phase", got)
	}
	if strings.Contains(got, "P0") {
		t.Fatalf("badge = %q, want no counts while a review is in flight", got)
	}
}

// P0 and P1 are what can still stop something; P2/P3 are notes and never reach
// the status line, which is also why hiding P3 in the panel changes nothing
// here.
func TestAdvisorBadgeCountsOnlyBlockingLevels(t *testing.T) {
	got := stripANSI(RenderModeStatus(OperationModeParams{
		ModelName: "m", Width: 120,
		Advisor: AdvisorBadge{Show: true, Status: advisor.StatusChangesRequired, P0: 1, P1: 2},
	}))
	if !strings.Contains(got, "P0 1 · P1 2") {
		t.Fatalf("badge = %q, want both blocking counts", got)
	}
	// A review whose only open findings are P2/P3 renders the verdict alone.
	clean := stripANSI(RenderModeStatus(OperationModeParams{
		ModelName: "m", Width: 120,
		Advisor: AdvisorBadge{Show: true, Status: advisor.StatusPassedWithNotes},
	}))
	if strings.Contains(clean, "P2") || strings.Contains(clean, "P3") {
		t.Fatalf("badge = %q, want no minor counts", clean)
	}
}

// Nothing to report — advisor off, badge turned off in settings, or no review
// yet — means the line says nothing at all.
func TestAdvisorBadgeHiddenWhenNotShown(t *testing.T) {
	got := stripANSI(RenderModeStatus(OperationModeParams{ModelName: "m", Width: 120}))
	if strings.Contains(got, "advisor") {
		t.Fatalf("status line = %q, want no advisor badge", got)
	}
}

// The badge sits on the left, beside the mode indicator, where "how is this
// session running?" is answered.
func TestAdvisorBadgeSitsBesideTheMode(t *testing.T) {
	got := stripANSI(RenderModeStatus(OperationModeParams{
		Mode: setting.ModeAutoAccept, ModelName: "m", Width: 120,
		Advisor: AdvisorBadge{Show: true, Status: advisor.StatusPassed},
	}))
	accept := strings.Index(got, "accept edits")
	badge := strings.Index(got, "advisor · passed")
	if accept < 0 || badge < 0 || badge < accept {
		t.Fatalf("status line = %q, want the advisor badge after the mode indicator", got)
	}
}
