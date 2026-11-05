// Package advisor owns San's global advisor layer: the independent review that
// inspects what the main agent and its subagents produced, classifies every
// finding P0–P3, and states whether the work may be treated as finished or
// merged.
//
// The package is pure domain: severity ranking, the status a review resolves
// to, the policy that turns a setting into a block decision, and the
// session-scoped store that keeps every review. It does not talk to an LLM, run
// a subagent, or render anything — the app composes those around it. Keeping
// the rules here is what lets the same verdict govern a plan step, a finished
// subagent task and a merge without three copies of the logic.
package advisor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Severity is a finding's seriousness. The four levels are the whole scale:
// there is no P4, and an unrecognized value is a parse error rather than a
// silent downgrade.
type Severity string

const (
	// SeverityP0 is a blocker: broken core behavior, a leaked secret or
	// credential, destroyed data, a serious security hole, a required build or
	// test failing, or an out-of-scope change that risks production.
	SeverityP0 Severity = "P0"
	// SeverityP1 is critical: a real bug in an important edge case, missing
	// error handling on the main path, no test for a risky change, a design
	// that contradicts a settled decision, or a clear performance regression.
	SeverityP1 Severity = "P1"
	// SeverityP2 is major: duplication, inconsistent error handling, missing
	// logging that matters, thin edge-case tests, architecture that could be
	// better. Worth fixing; does not stop the flow by default.
	SeverityP2 Severity = "P2"
	// SeverityP3 is minor: formatting, naming, comments, small optimizations,
	// readability. Recorded, never blocking.
	SeverityP3 Severity = "P3"
)

// severities lists the scale from most to least severe, so ordering and
// iteration live in one place.
var severities = [...]Severity{SeverityP0, SeverityP1, SeverityP2, SeverityP3}

// ParseSeverity normalizes a model- or user-supplied level. Case and
// surrounding space are tolerated ("p1", " P1 "); anything else is rejected, so
// a garbled answer can never be mistaken for a blocker.
func ParseSeverity(s string) (Severity, bool) {
	switch Severity(strings.ToUpper(strings.TrimSpace(s))) {
	case SeverityP0:
		return SeverityP0, true
	case SeverityP1:
		return SeverityP1, true
	case SeverityP2:
		return SeverityP2, true
	case SeverityP3:
		return SeverityP3, true
	}
	return "", false
}

// Valid reports whether s is one of the four levels.
func (s Severity) Valid() bool {
	for _, known := range severities {
		if s == known {
			return true
		}
	}
	return false
}

// Rank orders severities from most to least severe: P0 is 0, P3 is 3. An
// unrecognized value ranks last (4) so it can never sort ahead of a real
// blocker; callers that accept external input parse it first and never see one.
func (s Severity) Rank() int {
	for i, known := range severities {
		if s == known {
			return i
		}
	}
	return len(severities)
}

// Less reports whether s is more severe than other. Ordering helpers use it so
// "highest severity" is spelled once.
func (s Severity) Less(other Severity) bool { return s.Rank() < other.Rank() }

// ReviewStatus is where a review leaves the work it inspected. Only the first
// four are verdicts; StatusError records a review that could not run, which the
// policy never derives on its own.
type ReviewStatus string

const (
	// StatusPassed means nothing worth acting on was found.
	StatusPassed ReviewStatus = "passed"
	// StatusPassedWithNotes means only P2/P3 findings were found: the work may
	// continue, and the suggestions are recorded for later.
	StatusPassedWithNotes ReviewStatus = "passed_with_notes"
	// StatusChangesRequired means a P1 was found, or a P0 under a policy that
	// does not block on it: the human decides whether to fix now.
	StatusChangesRequired ReviewStatus = "changes_required"
	// StatusBlocked means a finding at or above the blocking threshold was
	// found: the policy refuses the action the review was gating.
	StatusBlocked ReviewStatus = "blocked"
	// StatusError means the review could not run to a verdict (provider error,
	// timeout, unparseable answer). It is set by the runner, never by the
	// policy, and it is not a pass.
	StatusError ReviewStatus = "error"
)

// Trigger names what caused a review to run.
type Trigger string

const (
	// TriggerSubagentTask fires when a background subagent task finished.
	TriggerSubagentTask Trigger = "subagent_task"
	// TriggerBeforeMerge fires before a branch merge, PR/MR creation, or push
	// to the target branch.
	TriggerBeforeMerge Trigger = "before_merge"
	// TriggerPlanStep fires before a plan step is recorded as done.
	TriggerPlanStep Trigger = "plan_step"
	// TriggerLargeChange fires when a change crosses the configured size or
	// sensitive-path thresholds.
	TriggerLargeChange Trigger = "large_change"
	// TriggerUserRequest fires when the human asked for a review explicitly.
	TriggerUserRequest Trigger = "user_request"
)

// Valid reports whether t is a known trigger. The store accepts any trigger so
// a future one does not need a migration, but UIs and prompts use this to
// decide whether they can label it.
func (t Trigger) Valid() bool {
	switch t {
	case TriggerSubagentTask, TriggerBeforeMerge, TriggerPlanStep, TriggerLargeChange, TriggerUserRequest:
		return true
	}
	return false
}

// TargetKind names what a review looked at.
type TargetKind string

const (
	// TargetDiff reviews a diff range.
	TargetDiff TargetKind = "diff"
	// TargetTask reviews a subagent task: its diff plus its result.
	TargetTask TargetKind = "task"
	// TargetStep reviews one plan step.
	TargetStep TargetKind = "step"
	// TargetCommit reviews a single commit.
	TargetCommit TargetKind = "commit"
	// TargetSession reviews everything the session changed.
	TargetSession TargetKind = "session"
	// TargetFile reviews one file or a path scope.
	TargetFile TargetKind = "file"
)

// Valid reports whether k is a known target kind.
func (k TargetKind) Valid() bool {
	switch k {
	case TargetDiff, TargetTask, TargetStep, TargetCommit, TargetSession, TargetFile:
		return true
	}
	return false
}

// Target identifies what a review looked at and how to link back to it. It is
// stored with the review so the UI can jump to the task, step, commit or diff
// range without re-deriving it from prose.
type Target struct {
	Kind TargetKind `json:"kind"`
	// ID is the kind-specific identity: a task id, a plan step id, a commit
	// sha, or a file path. Empty for a whole-diff or whole-session review.
	ID string `json:"id,omitempty"`
	// Label is the human-facing description ("task 3f2a · go vet fix").
	Label string `json:"label,omitempty"`
	// Base and Head name the diff range the review covered, as git refs or
	// hashes. Both empty when the review did not cover a range.
	Base string `json:"base,omitempty"`
	Head string `json:"head,omitempty"`
	// Files lists the changed paths the review was told about.
	Files []string `json:"files,omitempty"`
}

// Clone returns a deep copy so a snapshot never aliases live state.
func (t Target) Clone() Target {
	out := t
	out.Files = append([]string(nil), t.Files...)
	return out
}

// Empty reports whether the target carries no identity at all.
func (t Target) Empty() bool { return t.Kind == "" && t.ID == "" }

// sameAs reports whether two targets address the same thing. Files are not
// compared: a re-review of the same task or step is the same target even if the
// changed-path list grew.
func (t Target) sameAs(other Target) bool {
	return t.Kind == other.Kind && t.ID == other.ID
}

// FindingStatus is a finding's lifecycle. Dismissed and addressed findings stop
// counting against the work; deferred ones do not, because "later" is not a fix
// and must not become a way past a blocker.
type FindingStatus string

const (
	FindingOpen      FindingStatus = "open"
	FindingAddressed FindingStatus = "addressed"
	FindingDismissed FindingStatus = "dismissed"
	FindingDeferred  FindingStatus = "deferred"
)

// Valid reports whether s is a known finding status.
func (s FindingStatus) Valid() bool {
	switch s {
	case FindingOpen, FindingAddressed, FindingDismissed, FindingDeferred:
		return true
	}
	return false
}

// Unresolved reports whether the finding still counts against the work.
func (s FindingStatus) Unresolved() bool {
	return s == FindingOpen || s == FindingDeferred || s == ""
}

// Finding is one issue the advisor raised, with the evidence it relied on and
// the fix it suggests.
type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	// File and Line locate the issue; Line is 0 when the finding is about a
	// whole file or the change as a whole.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Evidence quotes the diff line or check output the finding rests on, so a
	// reader can confirm it without re-running the review.
	Evidence string `json:"evidence,omitempty"`
	// Impact is the expected consequence if the finding ships.
	Impact string `json:"impact,omitempty"`
	// Suggestion is the proposed fix, in the reviewer's words.
	Suggestion string        `json:"suggestion,omitempty"`
	Status     FindingStatus `json:"status"`
	// Note records why the finding was dismissed or deferred. Required for a
	// dismissed P0/P1, which is a decision the session keeps.
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// Clone returns a deep copy of the finding.
func (f Finding) Clone() Finding { return f }

// Unresolved reports whether the finding still counts against the work.
func (f Finding) Unresolved() bool { return f.Status.Unresolved() }

// Review is one advisor run. Reviews are immutable history: a re-run appends a
// new Review, and a finding's status is the only part that changes in place.
type Review struct {
	ID      string       `json:"id"`
	Trigger Trigger      `json:"trigger"`
	Target  Target       `json:"target"`
	Status  ReviewStatus `json:"status"`
	// Severity is the highest level found, empty when the review found
	// nothing. It is derived from the findings, never taken from the model.
	Severity Severity `json:"severity,omitempty"`
	// Summary is the reviewer's one-paragraph conclusion.
	Summary  string    `json:"summary,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
	// DiffHash identifies the reviewed content. Two reviews of one target with
	// different hashes reviewed different code; the same hash means a re-run is
	// redundant until something changes.
	DiffHash string `json:"diffHash,omitempty"`
	// Model names the model that produced the review; empty for a policy-only
	// verdict (e.g. a review that never reached the model).
	Model string `json:"model,omitempty"`
	// Error explains a StatusError review.
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// Clone returns a deep copy, so a snapshot handed to the UI or a transcript
// writer never aliases live state.
func (r Review) Clone() Review {
	out := r
	out.Target = r.Target.Clone()
	out.Findings = make([]Finding, len(r.Findings))
	copy(out.Findings, r.Findings)
	return out
}

// Highest returns the most severe level among the review's unresolved
// findings, or "" when none remain.
func (r Review) Highest() Severity {
	best := Severity("")
	for _, f := range r.Findings {
		if !f.Unresolved() {
			continue
		}
		if best == "" || f.Severity.Less(best) {
			best = f.Severity
		}
	}
	return best
}

// Counts returns the number of findings per level. Every finding is counted,
// resolved or not: the tally describes the review, while Highest and the policy
// describe what is still outstanding.
func (r Review) Counts() Counts {
	var c Counts
	for _, f := range r.Findings {
		c.add(f.Severity)
	}
	return c
}

// UnresolvedCounts returns the number of unresolved findings per level.
func (r Review) UnresolvedCounts() Counts {
	var c Counts
	for _, f := range r.Findings {
		if f.Unresolved() {
			c.add(f.Severity)
		}
	}
	return c
}

// OpenFindings returns the unresolved findings, most severe first, so callers
// that render or act on them share one order.
func (r Review) OpenFindings() []Finding {
	out := make([]Finding, 0, len(r.Findings))
	for _, f := range r.Findings {
		if f.Unresolved() {
			out = append(out, f)
		}
	}
	sortFindings(out)
	return out
}

// Finding returns the finding with the given id.
func (r Review) Finding(id string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.ID == id {
			return f, true
		}
	}
	return Finding{}, false
}

// sortFindings orders findings by severity, then file, then line, then title —
// a stable, readable order that does not depend on the model's output order.
func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Severity != b.Severity {
			return a.Severity.Less(b.Severity)
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Title < b.Title
	})
}

// Counts is a finding tally per level.
type Counts struct {
	P0 int `json:"p0"`
	P1 int `json:"p1"`
	P2 int `json:"p2"`
	P3 int `json:"p3"`
}

func (c *Counts) add(s Severity) {
	switch s {
	case SeverityP0:
		c.P0++
	case SeverityP1:
		c.P1++
	case SeverityP2:
		c.P2++
	case SeverityP3:
		c.P3++
	}
}

// Total is the number of findings counted.
func (c Counts) Total() int { return c.P0 + c.P1 + c.P2 + c.P3 }

// Blocking is the number of findings at or above the given level.
func (c Counts) Blocking(from Severity) int {
	switch from {
	case SeverityP0:
		return c.P0
	case SeverityP1:
		return c.P0 + c.P1
	case SeverityP2:
		return c.P0 + c.P1 + c.P2
	case SeverityP3:
		return c.Total()
	}
	return 0
}

// NewID returns a fresh prefixed identifier. Review and finding ids are opaque
// strings: the transcript, the UI and the session decisions all reference them,
// so they must be stable and collision-free rather than sequential.
func NewID(prefix string) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// Validate reports whether a review is coherent enough to store. It is the
// boundary check for reviews built outside this package (a parsed model answer,
// a restored transcript record): an unknown severity, status or trigger is
// rejected rather than rendered.
func (r Review) Validate() error {
	if strings.TrimSpace(string(r.Trigger)) == "" {
		return fmt.Errorf("review trigger is required")
	}
	if !r.Target.Kind.Valid() {
		return fmt.Errorf("unknown target kind %q", r.Target.Kind)
	}
	switch r.Status {
	case StatusPassed, StatusPassedWithNotes, StatusChangesRequired, StatusBlocked, StatusError:
	default:
		return fmt.Errorf("unknown review status %q", r.Status)
	}
	if r.Severity != "" && !r.Severity.Valid() {
		return fmt.Errorf("unknown severity %q", r.Severity)
	}
	for i, f := range r.Findings {
		if !f.Severity.Valid() {
			return fmt.Errorf("finding %d has unknown severity %q", i+1, f.Severity)
		}
		if f.Status != "" && !f.Status.Valid() {
			return fmt.Errorf("finding %d has unknown status %q", i+1, f.Status)
		}
	}
	return nil
}
