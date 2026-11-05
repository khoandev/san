// The Advisor Panel's app side: /advisor's command, the snapshot the panel
// renders, and the handlers behind each action.
//
// Everything here runs on the UI goroutine, because the panel, the prompt and
// the task tracker are all UI state. The one thing that leaves this file is a
// review request: it goes through the same bounded background runner every other
// trigger uses, and its verdict comes back as a notice and a repaint.
package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/transcript"
)

// advisorUsage is the one line that lists every target /advisor can review.
const advisorUsage = "Usage: /advisor [diff | commit <sha> | task <id> | step <id> | session | file <path>]"

// advisorCommand is /advisor. With no argument it opens the findings panel;
// with a target it reviews exactly that thing — the working tree, a commit, a
// finished task, a plan step, the session, or one file — and the panel shows
// the verdict when it lands.
//
// The runner is normally built when the agent runtime starts, so a command
// asked for before the first turn would find none. wireAdvisor is idempotent,
// and skipping it while a runner exists keeps a live review from being torn
// down by a status read.
func (m *model) advisorCommand(args string) (string, tea.Cmd) {
	cfg := m.advisorConfig()
	if !cfg.Enabled() {
		return "Advisor is off. Set advisor.enable in settings to review changes.", nil
	}
	if m.services.Advisor.session == nil {
		m.wireAdvisor()
	}

	args = strings.TrimSpace(args)
	kind, rest, _ := strings.Cut(args, " ")
	kind = strings.ToLower(kind)
	switch kind {
	case "":
		m.showAdvisorPanel()
		return "", nil
	case "status":
		m.showAdvisorPanel()
		return m.advisorStatusLine(), nil
	case "review", "diff":
		line, cmd := m.startTargetReview("diff", "")
		m.refreshAdvisorPanel()
		return line, cmd
	case "commit", "task", "step", "session", "file":
		line, cmd := m.startTargetReview(kind, strings.TrimSpace(rest))
		m.refreshAdvisorPanel()
		return line, cmd
	default:
		return advisorUsage, nil
	}
}

// startTargetReview resolves one /advisor target, reads exactly that change and
// hands it to the background runner. The read is bounded and synchronous — the
// person asked for it — but the review itself never blocks the UI.
func (m *model) startTargetReview(kind, arg string) (string, tea.Cmd) {
	if m.services.Advisor.session == nil {
		return "Advisor has no model to review with.", nil
	}
	target, problem := m.advisorTarget(kind, arg)
	if problem != "" {
		return problem, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), advisorDiffTimeout)
	defer cancel()
	diff, err := m.readAdvisorDiff(ctx, target)
	if err != nil {
		return "Advisor could not read a diff: " + err.Error(), nil
	}
	if strings.TrimSpace(diff.Diff) == "" && len(diff.Files) == 0 {
		return "Nothing to review for " + advisorTargetLabel(target) + ".", nil
	}
	started := m.requestAdvisorReview(advisor.ReviewInput{
		Trigger:       advisor.TriggerUserRequest,
		Target:        target,
		Diff:          diff.Diff,
		DiffHash:      diff.Hash,
		Files:         diff.Files,
		DiffTruncated: diff.Truncated,
	})
	if !started {
		return "Advisor did not start a review: one is already running, or this diff was just reviewed.", nil
	}
	return "Advisor is reviewing " + advisorTargetLabel(target) + "…", nil
}

// advisorTarget turns a subcommand into the target it names. The second return
// is a usage problem, never a partial target: a review is not started on a
// guess about what the person meant.
func (m *model) advisorTarget(kind, arg string) (advisor.Target, string) {
	switch kind {
	case "diff", "review":
		return advisor.Target{Kind: advisor.TargetDiff, Label: "working tree"}, ""
	case "session":
		id := m.sessionID()
		return advisor.Target{Kind: advisor.TargetSession, ID: id, Label: "session"}, ""
	case "commit":
		if arg == "" {
			return advisor.Target{}, "Usage: /advisor commit <sha>"
		}
		return advisor.Target{Kind: advisor.TargetCommit, ID: arg, Label: "commit " + arg}, ""
	case "task":
		if arg == "" {
			return advisor.Target{}, "Usage: /advisor task <id>"
		}
		return advisor.Target{Kind: advisor.TargetTask, ID: arg, Label: "task " + arg}, ""
	case "step":
		if arg == "" {
			return advisor.Target{}, "Usage: /advisor step <id>"
		}
		return advisor.Target{Kind: advisor.TargetStep, ID: arg, Label: m.advisorStepLabel(arg)}, ""
	case "file":
		path, problem := m.advisorFilePath(arg)
		if problem != "" {
			return advisor.Target{}, problem
		}
		return advisor.Target{Kind: advisor.TargetFile, ID: path, Label: path}, ""
	}
	return advisor.Target{}, advisorUsage
}

// advisorStepLabel names a step by its title when the plan still holds it, so
// the review reads "step · wire the gate" rather than an opaque id.
func (m *model) advisorStepLabel(id string) string {
	if active, ok := m.plan.Active(); ok {
		for _, step := range active.Steps {
			if step.ID == id && strings.TrimSpace(step.Title) != "" {
				return step.Title
			}
		}
	}
	return id
}

// advisorFilePath keeps a file target inside the workspace and in the form Git
// reads: a path relative to the working directory. An absolute path outside it
// would review something the session's tools cannot see.
func (m *model) advisorFilePath(arg string) (string, string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "Usage: /advisor file <path>"
	}
	path := arg
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(m.env.CWD, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", "The path must be inside the workspace."
		}
		path = rel
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || strings.HasPrefix(path, "..") {
		return "", "The path must be inside the workspace."
	}
	return path, ""
}

// readAdvisorDiff reads the change a target names. A commit reads its own diff;
// a file scopes the working diff to that pathspec; every other target reads the
// whole working tree against base.
func (m *model) readAdvisorDiff(ctx context.Context, target advisor.Target) (proc.GitDiff, error) {
	opts := proc.DiffOptions{MaxBytes: m.advisorConfig().ResolvedMaxDiffBytes()}
	switch target.Kind {
	case advisor.TargetCommit:
		return proc.ReadCommitDiff(ctx, m.env.CWD, target.ID, opts)
	case advisor.TargetFile:
		opts.Paths = []string{target.ID}
	}
	return proc.ReadWorkingDiff(ctx, m.env.CWD, opts)
}

// advisorTargetLabel names a target in a sentence.
func advisorTargetLabel(target advisor.Target) string {
	if label := strings.TrimSpace(target.Label); label != "" {
		return label
	}
	if id := strings.TrimSpace(target.ID); id != "" {
		return id
	}
	return string(target.Kind)
}

// ── Panel snapshot ──────────────────────────────────────────────────────

// showAdvisorPanel opens the panel on the session's review history.
func (m *model) showAdvisorPanel() {
	m.userInput.Advisor.Enter(m.advisorView(), m.env.Width, m.env.Height)
}

// refreshAdvisorPanel repaints an open panel; a closed one stays closed.
func (m *model) refreshAdvisorPanel() {
	if !m.userInput.Advisor.IsActive() {
		return
	}
	m.userInput.Advisor.Refresh(m.advisorView())
}

// advisorView is the snapshot the panel renders: the history, the live status,
// and the merge recommendation resolved against the session's policy.
func (m *model) advisorView() input.AdvisorView {
	snap := m.services.Advisor.Indicator.Snapshot()
	view := input.AdvisorView{
		State:   m.services.Advisor.Store.Snapshot(),
		Running: snap.Running,
		Err:     snap.Err,
	}
	if latest, ok := view.State.Latest(); ok && latest.Target.Kind == advisor.TargetMerge {
		view.Merge = advisor.Recommend(latest, advisorPolicy(m.advisorConfig()))
	}
	return view
}

// ── Actions ─────────────────────────────────────────────────────────────

// handleAdvisorAction applies one panel action. Every arm is idempotent where
// it can be: a repeated dismissal names the same finding and records the same
// decision, and a re-run either finds the diff unchanged or appends a review.
func (m *model) handleAdvisorAction(msg input.AdvisorActionMsg) tea.Cmd {
	switch msg.Action {
	case input.AdvisorActionClose:
		m.userInput.Advisor.Close()
		return nil
	case input.AdvisorActionFix:
		return m.fixAdvisorFinding(msg.ReviewID, msg.FindingID)
	case input.AdvisorActionDismiss:
		return m.resolveAdvisorFinding(msg.ReviewID, msg.FindingID, advisor.FindingDismissed, msg.Note)
	case input.AdvisorActionDefer:
		return m.resolveAdvisorFinding(msg.ReviewID, msg.FindingID, advisor.FindingDeferred, msg.Note)
	case input.AdvisorActionFollowUp:
		return m.followUpAdvisorFinding(msg.ReviewID, msg.FindingID)
	case input.AdvisorActionRerun:
		return m.rerunAdvisorReview(msg.ReviewID)
	}
	return nil
}

// resolveAdvisorFinding records a decision on one finding: the store moves it,
// the transcript keeps the move beside the review, and the panel repaints. The
// domain requires a reason for a dismissed P0/P1; the panel requires one for
// every dismissal, so a decision it emits always carries one.
func (m *model) resolveAdvisorFinding(reviewID, findingID string, status advisor.FindingStatus, note string) tea.Cmd {
	note = strings.TrimSpace(note)
	if status == advisor.FindingDismissed && note == "" {
		m.conv.AddNotice("Advisor: a reason is required before a finding is dismissed.")
		return tea.Batch(m.CommitMessages()...)
	}
	review, finding, ok := m.advisorFinding(reviewID, findingID)
	if !ok {
		m.conv.AddNotice("Advisor: that finding is no longer in the review history.")
		return tea.Batch(m.CommitMessages()...)
	}
	if err := m.services.Advisor.Store.UpdateFinding(reviewID, findingID, status, note); err != nil {
		m.conv.AddNotice("Advisor: " + err.Error())
		return tea.Batch(m.CommitMessages()...)
	}
	m.persistAdvisorFinding(reviewID, findingID, status, note)
	m.refreshAdvisorPanel()
	m.conv.AddNotice(advisorDecisionLine(review, finding, status, note))
	return tea.Batch(m.CommitMessages()...)
}

// persistAdvisorFinding appends the finding's move to the transcript. It is its
// own record, so the review it belongs to stays exactly as it was reviewed.
func (m *model) persistAdvisorFinding(reviewID, findingID string, status advisor.FindingStatus, note string) {
	sess := m.services.Session
	if sess == nil {
		return
	}
	if err := sess.Append(context.Background(), transcript.Record{
		Type: transcript.AdvisorFindingUpdated,
		AdvisorFinding: &transcript.AdvisorFindingRecord{
			ReviewID: reviewID, FindingID: findingID, Status: status, Note: note,
		},
	}); err != nil {
		log.Logger().Warn("advisor finding decision not persisted", zap.Error(err))
	}
}

// fixAdvisorFinding puts the finding in the prompt, where the main agent can
// act on it. The person still sends the turn: the panel never starts work on
// its own, and the prompt is editable before it goes.
func (m *model) fixAdvisorFinding(reviewID, findingID string) tea.Cmd {
	review, finding, ok := m.advisorFinding(reviewID, findingID)
	if !ok {
		m.conv.AddNotice("Advisor: that finding is no longer in the review history.")
		return tea.Batch(m.CommitMessages()...)
	}
	m.userInput.Advisor.Close()
	m.userInput.Textarea.SetValue(advisorFixPrompt(review, finding))
	m.conv.AddNotice("The finding is in the prompt. Edit it if needed, then press Enter.")
	return tea.Batch(m.CommitMessages()...)
}

// advisorFixPrompt is what the main agent reads: the finding, where it lives,
// the evidence behind it and the fix the reviewer proposed.
func advisorFixPrompt(review advisor.Review, finding advisor.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fix this finding from advisor review %s, then run the checks again.\n\n", review.ID)
	fmt.Fprintf(&b, "[%s] %s", finding.Severity, finding.Title)
	if finding.File != "" {
		b.WriteString(" (" + finding.File)
		if finding.Line > 0 {
			fmt.Fprintf(&b, ":%d", finding.Line)
		}
		b.WriteString(")")
	}
	b.WriteString("\n")
	for _, line := range []struct{ label, value string }{
		{"", finding.Detail},
		{"Evidence: ", finding.Evidence},
		{"Impact: ", finding.Impact},
		{"Suggested fix: ", finding.Suggestion},
	} {
		value := strings.TrimSpace(line.value)
		if value == "" {
			continue
		}
		b.WriteString("\n" + line.label + value + "\n")
	}
	b.WriteString("\nReport what you changed, and keep the rest of the review's findings in mind.")
	return b.String()
}

// followUpAdvisorFinding turns a finding into a tracker item, so a deferred
// issue has an owner instead of only a note.
func (m *model) followUpAdvisorFinding(reviewID, findingID string) tea.Cmd {
	review, finding, ok := m.advisorFinding(reviewID, findingID)
	if !ok {
		m.conv.AddNotice("Advisor: that finding is no longer in the review history.")
		return tea.Batch(m.CommitMessages()...)
	}
	if m.services.Tracker == nil {
		m.conv.AddNotice("Advisor: no task list is available in this session.")
		return tea.Batch(m.CommitMessages()...)
	}
	subject := fmt.Sprintf("%s: %s", finding.Severity, finding.Title)
	item := m.services.Tracker.Create(subject, advisorFindingDescription(review, finding), "Addressing the advisor's finding", map[string]any{
		"advisor_review_id":  review.ID,
		"advisor_finding_id": finding.ID,
		"advisor_severity":   string(finding.Severity),
	})
	if err := m.appendSessionState(); err != nil {
		log.Logger().Warn("advisor follow-up not persisted", zap.Error(err))
	}
	m.refreshAdvisorPanel()
	if item != nil {
		m.conv.AddNotice(fmt.Sprintf("Follow-up added to tasks: %s (%s)", subject, item.ID))
	}
	return tea.Batch(m.CommitMessages()...)
}

// advisorFindingDescription is the tracker item's body: the finding's own
// words, so the item stands on its own outside the panel.
func advisorFindingDescription(review advisor.Review, finding advisor.Finding) string {
	var b strings.Builder
	if finding.File != "" {
		b.WriteString(finding.File)
		if finding.Line > 0 {
			fmt.Fprintf(&b, ":%d", finding.Line)
		}
		b.WriteString("\n")
	}
	for _, value := range []string{finding.Detail, finding.Evidence, finding.Suggestion} {
		if value = strings.TrimSpace(value); value != "" {
			b.WriteString("\n" + value + "\n")
		}
	}
	b.WriteString("\nFrom advisor review " + review.ID + " (" + string(review.Trigger) + ").")
	return b.String()
}

// rerunAdvisorReview reviews a target again after the code changed. The diff
// hash is the comparison: unchanged code keeps the review it already has, and
// changed code appends a new review instead of replacing the old one, so the
// history still shows what was reviewed before the fix.
func (m *model) rerunAdvisorReview(reviewID string) tea.Cmd {
	review, ok := m.advisorReviewByID(reviewID)
	if !ok {
		m.conv.AddNotice("Advisor: that review is no longer in the session.")
		return tea.Batch(m.CommitMessages()...)
	}
	if m.services.Advisor.session == nil {
		m.wireAdvisor()
	}
	if m.services.Advisor.session == nil {
		m.conv.AddNotice("Advisor has no model to review with.")
		return tea.Batch(m.CommitMessages()...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), advisorDiffTimeout)
	defer cancel()
	diff, err := m.readAdvisorDiff(ctx, review.Target)
	if err != nil {
		m.conv.AddNotice("Advisor could not read a diff: " + err.Error())
		return tea.Batch(m.CommitMessages()...)
	}
	if review.DiffHash != "" && diff.Hash == review.DiffHash {
		m.refreshAdvisorPanel()
		m.conv.AddNotice("Advisor: nothing changed since " + review.ID + ", so its verdict still stands.")
		return tea.Batch(m.CommitMessages()...)
	}
	started := m.requestAdvisorReview(advisor.ReviewInput{
		Trigger:       advisor.TriggerUserRequest,
		Target:        review.Target.Clone(),
		Diff:          diff.Diff,
		DiffHash:      diff.Hash,
		Files:         diff.Files,
		DiffTruncated: diff.Truncated,
	})
	if !started {
		m.conv.AddNotice("Advisor did not start a review: one is already running, or this diff was just reviewed.")
		return tea.Batch(m.CommitMessages()...)
	}
	m.refreshAdvisorPanel()
	m.conv.AddNotice("Advisor is reviewing " + advisorTargetLabel(review.Target) + " again…")
	return tea.Batch(m.CommitMessages()...)
}

// advisorReviewByID finds a review in the session's history.
func (m *model) advisorReviewByID(id string) (advisor.Review, bool) {
	for _, review := range m.services.Advisor.Store.Reviews() {
		if review.ID == id {
			return review, true
		}
	}
	return advisor.Review{}, false
}

// advisorFinding finds one finding beside the review that raised it.
func (m *model) advisorFinding(reviewID, findingID string) (advisor.Review, advisor.Finding, bool) {
	review, ok := m.advisorReviewByID(reviewID)
	if !ok {
		return advisor.Review{}, advisor.Finding{}, false
	}
	finding, ok := review.Finding(findingID)
	return review, finding, ok
}

// advisorDecisionLine is the notice a decision prints: which finding moved,
// where it was, and the reason the person gave.
func advisorDecisionLine(review advisor.Review, finding advisor.Finding, status advisor.FindingStatus, note string) string {
	line := fmt.Sprintf("advisor · %s %s finding %q", status, finding.Severity, finding.Title)
	if finding.File != "" {
		location := finding.File
		if finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", finding.File, finding.Line)
		}
		line += " (" + location + ")"
	}
	if note != "" {
		line += " · " + note
	}
	if review.ID != "" {
		line += " · review " + review.ID
	}
	return line
}
