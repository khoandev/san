package advisor

// This file is the merge gate: the advisor check that runs inside the
// permission path, before a merge-like shell command executes.
//
// The gate is synchronous on purpose. A review that finishes after the merge
// has described a decision that already happened; the verdict has to exist
// before the command runs. Its cost is bounded — one diff read, one review, one
// timeout — and it is paid only by a call whose command line integrates a
// branch. Every other tool call goes straight to the permission rules, without
// reading a diff or touching a model.
//
// Nothing here calls a model, reads git or writes the session: the caller
// supplies the diff reader, the review runner, the reuse lookup and the
// recorder, which keeps the rules testable without a repository, a provider or
// a transcript.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/tool"
)

const (
	// defaultMergeGateTimeout bounds one gate run when the config sets none.
	// It is a backstop: the review runner carries its own deadline, and this
	// one also covers a diff read that hangs before the runner is reached.
	defaultMergeGateTimeout = 2 * time.Minute
	// mergeTargetID is the one merge target a session reviews: whether a diff
	// may be integrated. The merge form is the label, not the identity — `git
	// merge` and `git push origin main` on the same tree want one review — and
	// the diff hash is what tells two candidate merges apart.
	mergeTargetID = "pre-merge"
	// mergeReasonFindings caps how many finding titles a reason names, so the
	// message stays readable in a tool-error row.
	mergeReasonFindings = 3
	// mergeGoal is what the reviewer is told the merge is for. A merge has no
	// acceptance criteria of its own: the diff against base is the subject.
	mergeGoal = "integrate this branch into its base branch without breaking it"
)

// MergeVerdict is what the gate concluded about a shell command.
type MergeVerdict int

const (
	// MergeNotGated means the gate did not judge this call: the advisor is
	// off, the tool is not a shell, or the command integrates nothing. The
	// caller's own permission path decides, exactly as it would with no
	// advisor.
	MergeNotGated MergeVerdict = iota
	// MergePermit means the diff was reviewed and the command may run.
	MergePermit
	// MergeConfirm means findings the policy does not settle on its own still
	// need the human's blessing before the command runs. The gate does not ask:
	// the caller owns the prompt, so a review never talks to the TUI from a
	// background goroutine.
	MergeConfirm
	// MergeReject means the review refused the merge. The reason names the
	// review and what it found, so the model and the user read the same
	// explanation.
	MergeReject
)

// String renders the verdict for logs and test failures.
func (v MergeVerdict) String() string {
	switch v {
	case MergeNotGated:
		return "not-gated"
	case MergePermit:
		return "permit"
	case MergeConfirm:
		return "confirm"
	case MergeReject:
		return "reject"
	}
	return fmt.Sprintf("MergeVerdict(%d)", int(v))
}

// MergeOutcome is the gate's answer and the reason to show.
type MergeOutcome struct {
	Verdict MergeVerdict
	// Reason explains the verdict in one line: the review id, the severity and
	// the finding titles. It reaches the model as the tool error on a reject
	// and the user as the prompt text on a confirm.
	Reason string
	// Review is the review the verdict came from, or the error review when no
	// verdict could be reached. The zero Review means the gate did not run.
	Review Review
}

// MergeGateConfig carries what the gate needs and nothing it can derive. The
// two function fields that read settings are functions so the gate never
// imports the settings layer and never caches a policy the user has changed.
type MergeGateConfig struct {
	// Enabled reports whether the gate runs, read per call so turning the
	// trigger off takes effect on the next command. Nil means enabled.
	Enabled func() bool
	// Policy returns the block policy, read per call for the same reason. Nil
	// blocks nothing: a merge gate with no policy only records what it found.
	Policy func() Policy
	// BaseBranch names the branch a merge lands on; "" accepts main and
	// master. It is the branch whose refspec a push is matched against.
	BaseBranch string
	// Timeout bounds one gate run end to end. Zero uses
	// defaultMergeGateTimeout.
	Timeout time.Duration
	// ReadDiff reads the whole diff against base — every commit on the branch
	// plus the uncommitted worktree, not the last commit alone. Required: a
	// gate that cannot see the change cannot review it.
	ReadDiff func(ctx context.Context) (proc.GitDiff, error)
	// Review runs one review synchronously and records it: the store, the
	// transcript and the notice are the caller's, because this package owns no
	// session. Required. It returns an error when no verdict was reached, and
	// the gate then records a StatusError review through Record so the panel
	// can explain the missing verdict.
	Review func(ctx context.Context, in ReviewInput) (Review, error)
	// Reuse answers "have we already reviewed exactly this diff?", so a second
	// merge attempt on unchanged code costs nothing. Nil means never reuse.
	Reuse func(target Target, diffHash string) (Review, bool)
	// Record stores a review the gate built itself. Nil skips recording.
	Record func(Review)
}

// MergeGate holds one gate's configuration. It is stateless otherwise, so the
// permission path may call Check from any goroutine.
type MergeGate struct {
	cfg MergeGateConfig
}

// NewMergeGate returns a gate over cfg. A config without a diff reader or a
// review runner still answers — with a rejection under a blocking policy — so
// a misconfigured gate never silently permits a merge.
func NewMergeGate(cfg MergeGateConfig) *MergeGate { return &MergeGate{cfg: cfg} }

// Check answers whether a tool call may run. A call that is not a shell command
// performing an integration action returns MergeNotGated without reading a
// diff, calling a model or waiting on anything.
//
// When it is gated, the whole diff against base is reviewed once, reused from
// history when the same hash was already reviewed, and resolved against the
// policy: a blocking finding rejects, a finding that needs the human confirms,
// and a clean (or merely noted) diff permits.
//
// A review that cannot run fails open with a warning, because an unavailable
// reviewer is not evidence of a bad change — unless the merge policy blocks on
// P0, where "we could not check" must not read as "we checked". That is the
// same posture as every other gate in the permission path: no answer, no merge,
// once the user has asked for merges to be blocked.
func (g *MergeGate) Check(ctx context.Context, name string, input map[string]any) MergeOutcome {
	if g == nil {
		return MergeOutcome{}
	}
	if g.cfg.Enabled != nil && !g.cfg.Enabled() {
		return MergeOutcome{}
	}
	command := shellCommand(name, input)
	if command == "" {
		return MergeOutcome{}
	}
	form := MergeCommand(command, g.cfg.BaseBranch)
	if form == "" {
		return MergeOutcome{}
	}

	target := Target{Kind: TargetMerge, ID: mergeTargetID, Label: form}
	if g.cfg.ReadDiff == nil || g.cfg.Review == nil {
		return g.failure(target, "", fmt.Errorf("the advisor has no review runner"))
	}

	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()

	diff, err := g.cfg.ReadDiff(ctx)
	if err != nil {
		return g.failure(target, "", fmt.Errorf("reading the diff against base: %w", err))
	}
	target = mergeTarget(form, diff)

	// A repeated merge attempt on unchanged code is the same review: the diff
	// hash is the identity, so a retry after a refusal costs nothing and
	// cannot drift into a different verdict.
	if g.cfg.Reuse != nil {
		if reuse, ok := g.cfg.Reuse(target, diff.Hash); ok && reuse.Status != StatusError {
			return g.verdict(reuse)
		}
	}

	review, err := g.cfg.Review(ctx, ReviewInput{
		Trigger:       TriggerBeforeMerge,
		Target:        target,
		Diff:          diff.Diff,
		DiffHash:      diff.Hash,
		Files:         diff.Files,
		DiffTruncated: diff.Truncated,
		Goal:          mergeGoal,
	})
	if err != nil {
		return g.failure(target, diff.Hash, err)
	}
	return g.verdict(review)
}

// verdict resolves a review's outstanding findings against the merge policy.
//
// It reads the findings, never the status the review resolved to on its own:
// a review run for a task under a lenient policy still has to stop a merge
// under a strict one, and a finding dismissed since then no longer blocks.
func (g *MergeGate) verdict(review Review) MergeOutcome {
	policy := g.policy()
	severity := review.Highest()
	switch {
	case policy.Blocks(ActionMerge, severity):
		if policy.AllowProceedWithRisk {
			// The block stands unless the human accepts the risk; that is a
			// prompt, not a permit and not a refusal.
			return MergeOutcome{Verdict: MergeConfirm, Review: review,
				Reason: mergeReason("advisor found a blocking issue in this merge", review, severity)}
		}
		return MergeOutcome{Verdict: MergeReject, Review: review,
			Reason: mergeReason("advisor blocked this merge", review, severity)}
	case policy.NeedsConfirmation(severity):
		return MergeOutcome{Verdict: MergeConfirm, Review: review,
			Reason: mergeReason("advisor reviewed this merge and wants your call", review, severity)}
	default:
		return MergeOutcome{Verdict: MergePermit, Review: review,
			Reason: mergeReason("advisor reviewed the whole diff against base", review, severity)}
	}
}

// failure resolves a review that could not run. It records the error as a
// StatusError review — never as a pass — so the panel shows why no verdict
// exists, then fails open or closed according to the merge policy.
func (g *MergeGate) failure(target Target, diffHash string, cause error) MergeOutcome {
	review := Review{
		ID:        NewID("review"),
		Trigger:   TriggerBeforeMerge,
		Target:    target.Clone(),
		Status:    StatusError,
		DiffHash:  diffHash,
		Error:     cause.Error(),
		StartedAt: time.Now(),
	}
	review.FinishedAt = review.StartedAt
	if g.cfg.Record != nil {
		g.cfg.Record(review)
	}
	if g.policy().Blocks(ActionMerge, SeverityP0) {
		return MergeOutcome{Verdict: MergeReject, Review: review,
			Reason: fmt.Sprintf("advisor could not review this merge (%v) and merges are blocked until a review passes · review %s", cause, review.ID)}
	}
	return MergeOutcome{Verdict: MergePermit, Review: review,
		Reason: fmt.Sprintf("advisor could not review this merge (%v); allowing it with a warning · review %s", cause, review.ID)}
}

// policy reads the live policy, tolerating an unset source.
func (g *MergeGate) policy() Policy {
	if g.cfg.Policy == nil {
		return Policy{}
	}
	return g.cfg.Policy()
}

// timeout reads the configured bound, defaulting so a gate run can never be
// unbounded.
func (g *MergeGate) timeout() time.Duration {
	if g.cfg.Timeout > 0 {
		return g.cfg.Timeout
	}
	return defaultMergeGateTimeout
}

// mergeTarget names what a merge review covers. Base is the commit the diff was
// taken against, so a resumed session can tell what the review saw.
func mergeTarget(form string, diff proc.GitDiff) Target {
	target := Target{Kind: TargetMerge, ID: mergeTargetID, Label: form, Base: diff.Base, Head: "HEAD"}
	if len(diff.Files) > 0 {
		target.Files = append([]string(nil), diff.Files...)
	}
	return target
}

// shellCommand returns the command line of a shell tool call, or "" for any
// other tool. Only a shell can integrate a branch, so every other call takes
// the cheap path.
func shellCommand(name string, input map[string]any) string {
	if !tool.IsShellTool(name) || input == nil {
		return ""
	}
	command, _ := input["command"].(string)
	return strings.TrimSpace(command)
}

// mergeReason is the one line a reject, a confirm and a permit each carry: what
// the advisor decided, which review decided it, and what it found. The review
// id makes the verdict traceable to the history the panel shows; the titles
// make the tool error actionable without a second lookup.
func mergeReason(prefix string, review Review, severity Severity) string {
	var b strings.Builder
	b.WriteString(prefix)
	if review.ID != "" {
		b.WriteString(" · review " + review.ID)
	}
	if severity != "" {
		b.WriteString(" · " + string(severity))
	}
	if titles := mergeFindingTitles(review); titles != "" {
		b.WriteString(" · " + titles)
	}
	if summary := firstLine(review.Summary); summary != "" {
		b.WriteString(" · " + summary)
	}
	if len(review.OpenFindings()) > 0 {
		b.WriteString(" · open /advisor for the findings")
	}
	return b.String()
}

// mergeFindingTitles quotes the most severe open findings, up to the cap, so the
// reason says what is wrong in the reviewer's own words.
func mergeFindingTitles(review Review) string {
	open := review.OpenFindings()
	if len(open) == 0 {
		return ""
	}
	if len(open) > mergeReasonFindings {
		open = open[:mergeReasonFindings]
	}
	parts := make([]string, 0, len(open))
	for _, f := range open {
		title := strings.TrimSpace(f.Title)
		if title == "" {
			continue
		}
		parts = append(parts, string(f.Severity)+": "+title)
	}
	titles := strings.Join(parts, "; ")
	if len(review.OpenFindings()) > len(open) {
		titles += fmt.Sprintf(" (+%d more)", len(review.OpenFindings())-len(open))
	}
	return titles
}
