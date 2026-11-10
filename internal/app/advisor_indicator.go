// AdvisorIndicator is the status-bar state of the advisor: whether a review is
// running, how many have finished, and what the last one found. It outlives any
// single advisorSession so the render path always has a snapshot to read, even
// when the advisor is off or being rewired.
//
// Writers split by goroutine: BeginReview and Skip run on the tea Update
// goroutine (the turn-end path), Finish runs on the review goroutine. One mutex
// serialises them, and the render path only ever reads a copied snapshot.
package app

import (
	"strings"
	"sync"
	"time"
)

// AdvisorBadgeState is the immutable view the status bar and /advisor read.
type AdvisorBadgeState struct {
	Reviewing      bool
	Reviews        int
	LastNote       string
	LastError      string
	LastSkip       string
	LastReviewedAt time.Time
}

// AdvisorIndicator is the live UI-side state for the advisor badge. Held by
// pointer on services so every goroutine mutates the same instance.
type AdvisorIndicator struct {
	mu sync.Mutex

	reviewing bool
	reviews   int
	lastNote  string
	lastErr   string
	lastSkip  string
	lastAt    time.Time
}

// NewAdvisorIndicator returns an idle indicator. It is a no-op to call.
func NewAdvisorIndicator() *AdvisorIndicator { return &AdvisorIndicator{} }

// BeginReview enters the reviewing state and clears the last skip: the reason
// a previous turn went unreviewed no longer describes what is happening.
func (a *AdvisorIndicator) BeginReview() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reviewing = true
	a.lastSkip = ""
}

// Finish records the outcome of one review. A note means the agent was advised;
// no note means the work passed. A cancelled teardown is neither: it reports no
// error and no result, so the badge keeps its previous state.
func (a *AdvisorIndicator) Finish(note string, err error) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reviewing = false
	if err != nil {
		a.lastErr = strings.TrimSpace(err.Error())
		a.lastSkip = ""
		return
	}
	a.reviews++
	a.lastAt = time.Now()
	a.lastNote = note
	a.lastErr = ""
	a.lastSkip = ""
}

// Abandon leaves the reviewing state without a result: a teardown or a session
// switch cancelled the review, so it says nothing about the work.
func (a *AdvisorIndicator) Abandon() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reviewing = false
}

// Skip records why a completed turn was not reviewed — busy (queued), a
// cancelled or failed turn, an empty delta, the advisory cap. The badge shows
// the word so a missing review is never invisible.
func (a *AdvisorIndicator) Skip(reason string) {
	if a == nil || reason == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastSkip = reason
}

// Reset starts a new conversation: no counts, no result, nothing carried over.
func (a *AdvisorIndicator) Reset() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reviewing = false
	a.reviews = 0
	a.lastNote = ""
	a.lastErr = ""
	a.lastSkip = ""
	a.lastAt = time.Time{}
}

// Snapshot copies the state for rendering.
func (a *AdvisorIndicator) Snapshot() AdvisorBadgeState {
	if a == nil {
		return AdvisorBadgeState{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return AdvisorBadgeState{
		Reviewing:      a.reviewing,
		Reviews:        a.reviews,
		LastNote:       a.lastNote,
		LastError:      a.lastErr,
		LastSkip:       a.lastSkip,
		LastReviewedAt: a.lastAt,
	}
}
