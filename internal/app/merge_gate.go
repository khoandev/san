// The merge gate's app wiring: the advisor's pre-merge review as the permission
// path sees it.
//
// The gate lives on AdvisorServices so wireAdvisor can rebuild it with the
// runner and teardownAdvisor can drop it. Everything it reaches at call time —
// the store, the notice channel, the indicator — is safe from the agent
// goroutine, which is where the permission check runs.
package app

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/proc"
)

// Errors the gate's runner reports as "no verdict". Each one is handled by the
// gate's own policy: a warning when merges are advisory, a refusal when the user
// asked for merges to be blocked.
var (
	errAdvisorUnavailable = errors.New("the advisor is not running")
	errAdvisorBusy        = errors.New("the advisor is already reviewing another change")
	errAdvisorNoVerdict   = errors.New("the advisor produced no verdict")
)

// newMergeGate builds the gate the permission path consults before a merge-like
// command runs. It is called from wireAdvisor, on the UI goroutine, with the
// runner it belongs to; the returned gate reads only atomics, the store and the
// notice channel afterwards, so the agent goroutine may call it.
func (m *model) newMergeGate(s *advisorSession) *advisor.MergeGate {
	if s == nil {
		return nil
	}
	// Capture what cannot change under the gate: the session's workspace,
	// configuration and policy are fixed for the runner's life, and a settings
	// or model change rebuilds the runner (advisorSignature) rather than
	// mutating them.
	store, cwd, cfg, policy := m.services.Advisor.Store, s.cwd, s.cfg, s.policy
	return advisor.NewMergeGate(advisor.MergeGateConfig{
		// Liveness is read per call, so a torn-down session stops gating
		// immediately. The trigger comes from the configuration the runner was
		// wired with, like the rest of the advisor: a new session re-wires both
		// the runner and this gate.
		Enabled: func() bool { return s.live.Load() && cfg.BeforeMerge() },
		Policy:  func() advisor.Policy { return policy },
		Timeout: cfg.ResolvedTimeout(),
		ReadDiff: func(ctx context.Context) (proc.GitDiff, error) {
			// The whole diff against base — every commit on the branch plus the
			// uncommitted worktree — not the last commit alone.
			return proc.ReadWorkingDiff(ctx, cwd, proc.DiffOptions{MaxBytes: cfg.ResolvedMaxDiffBytes()})
		},
		Reuse: func(target advisor.Target, diffHash string) (advisor.Review, bool) {
			return store.ReviewFor(target, diffHash)
		},
		Review: func(ctx context.Context, in advisor.ReviewInput) (advisor.Review, error) {
			return m.reviewMerge(ctx, s, in)
		},
		Record: m.recordMergeReview,
	})
}

// mergeGateResolver hands the agent the per-call resolver for the gate. It reads
// the atomic pointer, so the agent goroutine never touches UI state, and a
// session re-wired between calls is seen on the next one.
func (m *model) mergeGateResolver() agent.MergeGateFunc {
	return func() *advisor.MergeGate { return m.services.Advisor.mergeGate() }
}

// reviewMerge runs one pre-merge review and returns the stored verdict. It is
// the synchronous path: the permission check is holding the merge command, so
// nothing may run in the background here.
//
// It claims the run slot but not the cooldown. The gate has already reused a
// review by diff hash, so a second run means the code changed — exactly what
// must be reviewed again rather than suppressed by a cooldown window.
func (m *model) reviewMerge(ctx context.Context, s *advisorSession, in advisor.ReviewInput) (advisor.Review, error) {
	if s == nil || !s.live.Load() {
		return advisor.Review{}, errAdvisorUnavailable
	}
	if !s.busy.CompareAndSwap(false, true) {
		return advisor.Review{}, errAdvisorBusy
	}
	stored, forkID, ok := m.runClaimedReview(ctx, s, advisor.BuildRequest(s.cfg, in))
	if !ok {
		return advisor.Review{}, errAdvisorNoVerdict
	}
	m.publishAdvisorVerdict(stored, forkID)
	return stored, nil
}

// recordMergeReview stores a review the gate built itself: the error review that
// explains why no verdict exists. A review the runner produced is already stored
// and announced by runClaimedReview, so the gate records only the failures and
// never doubles a verdict.
//
// Every call is a mutex or a channel send, so this runs on the agent goroutine
// with no UI state touched.
func (m *model) recordMergeReview(review advisor.Review) {
	stored, err := m.services.Advisor.Store.Append(review)
	if err != nil {
		log.Logger().Warn("advisor merge review rejected", zap.Error(err))
		return
	}
	cause := errors.New(stored.Error)
	m.services.Advisor.Indicator.Fail(cause)
	m.persistAdvisorReview(stored)
	log.Logger().Warn("advisor could not review a merge",
		zap.String("review", stored.ID), zap.String("error", stored.Error))
	m.notifyMain(mainNotice{Display: advisorFailureLine(cause)})
}
