package advisor

import (
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"
)

// State is a session's whole advisor history: every review, oldest first. It is
// persisted in the session transcript and restored on resume, so nothing a
// review found is lost when the process restarts.
type State struct {
	Reviews []Review `json:"reviews,omitempty"`
}

// Empty reports whether the state carries no review at all.
func (s State) Empty() bool { return len(s.Reviews) == 0 }

// Clone returns a deep copy, so a snapshot handed to the UI or a transcript
// writer never aliases live state.
func (s State) Clone() State {
	out := State{Reviews: make([]Review, len(s.Reviews))}
	for i := range s.Reviews {
		out.Reviews[i] = s.Reviews[i].Clone()
	}
	return out
}

// Latest returns the most recent review.
func (s State) Latest() (Review, bool) {
	if len(s.Reviews) == 0 {
		return Review{}, false
	}
	return s.Reviews[len(s.Reviews)-1], true
}

// LatestFor returns the most recent review of the given target.
func (s State) LatestFor(target Target) (Review, bool) {
	for i := len(s.Reviews) - 1; i >= 0; i-- {
		if s.Reviews[i].Target.sameAs(target) {
			return s.Reviews[i], true
		}
	}
	return Review{}, false
}

// ReviewFor returns the most recent review of the given target whose diff hash
// matches. It answers "have we already reviewed exactly this code?", which is
// what makes a re-run cheap and a merge gate re-entrant.
func (s State) ReviewFor(target Target, diffHash string) (Review, bool) {
	for i := len(s.Reviews) - 1; i >= 0; i-- {
		r := s.Reviews[i]
		if r.Target.sameAs(target) && r.DiffHash == diffHash {
			return r, true
		}
	}
	return Review{}, false
}

// Append records a review in the history, normalizing its identity and
// validating it first. A review whose id is already present is left alone:
// history is append-only, so replaying a log adds nothing. It returns the new
// state and the review as stored, and it never mutates the receiver's backing
// array, so a state value handed to a reader cannot change under it.
func (s State) Append(review Review) (State, Review, error) {
	review = review.Clone()
	normalizeReview(&review)
	if err := review.Validate(); err != nil {
		return s, Review{}, err
	}
	for _, existing := range s.Reviews {
		if existing.ID == review.ID {
			return s, existing.Clone(), nil
		}
	}
	out := s
	out.Reviews = append(slices.Clip(s.Reviews), review)
	return out, review, nil
}

// UpdateFinding moves one finding in the history, recording why and when. It is
// the only mutation stored history allows: a review's findings, evidence and
// diff hash stay exactly as reviewed. The returned state holds a copy of the
// changed review, so an earlier snapshot does not change under its reader.
func (s State) UpdateFinding(reviewID, findingID string, status FindingStatus, note string, at time.Time) (State, error) {
	for i := range s.Reviews {
		if s.Reviews[i].ID != reviewID {
			continue
		}
		review := s.Reviews[i].Clone()
		for j := range review.Findings {
			finding := &review.Findings[j]
			if finding.ID != findingID {
				continue
			}
			if err := finding.Resolve(status, note); err != nil {
				return s, err
			}
			if !at.IsZero() {
				finding.UpdatedAt = at
			}
			out := s
			out.Reviews = slices.Clone(s.Reviews)
			out.Reviews[i] = review
			return out, nil
		}
		return s, fmt.Errorf("no finding %q in review %q", findingID, reviewID)
	}
	return s, fmt.Errorf("no review %q", reviewID)
}

// Store owns one session's advisor state. The UI goroutine reads it, the
// background review runner appends to it, and the transcript writer snapshots
// it, so every exported method takes the lock and returns copies.
type Store struct {
	mu    sync.RWMutex
	state State
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{} }

// Snapshot returns a detached copy of the whole state.
func (s *Store) Snapshot() State {
	if s == nil {
		return State{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Clone()
}

// Restore replaces the state, as a session resume does. Reviews keep their
// identity; anything missing is normalized rather than dropped, because the
// history is the record of what was reviewed.
func (s *Store) Restore(state State) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state.Clone()
	for i := range s.state.Reviews {
		normalizeReview(&s.state.Reviews[i])
	}
}

// Reset clears the state for a fresh conversation.
func (s *Store) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = State{}
}

// Append records a review. Reviews are append-only: an existing review is never
// replaced, so a re-run adds history instead of overwriting the verdict it
// supersedes. Missing review and finding ids are assigned here, and the review
// is validated before it enters the store.
func (s *Store) Append(review Review) (Review, error) {
	if s == nil {
		return Review{}, fmt.Errorf("advisor store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next, stored, err := s.state.Append(review)
	if err != nil {
		return Review{}, err
	}
	s.state = next
	return stored, nil
}

// UpdateFinding moves one finding to a new status, recording why. It is the
// only mutation allowed on stored history: the review's findings, evidence and
// diff hash stay exactly as reviewed.
func (s *Store) UpdateFinding(reviewID, findingID string, status FindingStatus, note string) error {
	if s == nil {
		return fmt.Errorf("advisor store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := s.state.UpdateFinding(reviewID, findingID, status, note, time.Now())
	if err != nil {
		return err
	}
	s.state = next
	return nil
}

// Reviews returns every review, oldest first.
func (s *Store) Reviews() []Review {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Review, len(s.state.Reviews))
	for i := range s.state.Reviews {
		out[i] = s.state.Reviews[i].Clone()
	}
	return out
}

// Latest returns the most recent review.
func (s *Store) Latest() (Review, bool) {
	if s == nil {
		return Review{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Latest()
}

// LatestFor returns the most recent review of a target.
func (s *Store) LatestFor(target Target) (Review, bool) {
	if s == nil {
		return Review{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.LatestFor(target)
}

// ReviewFor returns the most recent review of a target with a matching diff
// hash — the reuse check that keeps a re-run or a repeated merge attempt from
// paying for the same review twice.
func (s *Store) ReviewFor(target Target, diffHash string) (Review, bool) {
	if s == nil {
		return Review{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.ReviewFor(target, diffHash)
}

// FindingRef pairs a finding with the review that raised it, for callers that
// render or act on findings across reviews.
type FindingRef struct {
	ReviewID string
	Finding  Finding
}

// OpenFindings returns the unresolved findings of the latest review of each
// target, most severe first. Superseded reviews are skipped on purpose: once a
// target has been reviewed again, its older findings are history, and counting
// them would keep a fixed issue visible forever.
func (s *Store) OpenFindings() []FindingRef {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool, len(s.state.Reviews))
	var out []FindingRef
	for i := len(s.state.Reviews) - 1; i >= 0; i-- {
		review := s.state.Reviews[i]
		key := string(review.Target.Kind) + "\x00" + review.Target.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, f := range review.OpenFindings() {
			out = append(out, FindingRef{ReviewID: review.ID, Finding: f})
		}
	}
	sortFindingRefs(out)
	return out
}

// Counts returns the unresolved finding tally of the most recent review — what
// sortFindingRefs orders findings across reviews the same way a single review
// orders them, so the panel's list is stable regardless of append order.
func sortFindingRefs(refs []FindingRef) {
	sort.SliceStable(refs, func(i, j int) bool {
		a, b := refs[i].Finding, refs[j].Finding
		if a.Severity != b.Severity {
			return a.Severity.Less(b.Severity)
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return refs[i].ReviewID < refs[j].ReviewID
	})
}

// Counts returns the unresolved finding tally of the most recent review — what
// the status bar shows for the session.
func (s *Store) Counts() Counts {
	if s == nil {
		return Counts{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	review, ok := s.state.Latest()
	if !ok {
		return Counts{}
	}
	return review.UnresolvedCounts()
}

// normalizeReview fills the identity and defaults a review needs to be stored:
// ids, start time, finding ids, and an open status for a finding that arrived
// without one. It never invents a verdict — a review without a status fails
// validation instead.
func normalizeReview(r *Review) {
	if r.ID == "" {
		r.ID = NewID("review")
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	for i := range r.Findings {
		if r.Findings[i].ID == "" {
			r.Findings[i].ID = NewID("finding")
		}
		if r.Findings[i].Status == "" {
			r.Findings[i].Status = FindingOpen
		}
	}
	sortFindings(r.Findings)
}
