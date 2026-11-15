package conv

import (
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/app/kit"
)

// RetryTickInterval is how often the retry row recounts the wait it shows. One
// second matches the clock the row speaks in: seconds, not frames.
const RetryTickInterval = time.Second

// RetryTickMsg asks the retry row to redraw itself. It carries nothing: the
// wait it counts down is computed from the failure it was told about.
type RetryTickMsg struct{}

// retryTick schedules the next recount of the row.
func retryTick() tea.Cmd {
	return tea.Tick(RetryTickInterval, func(time.Time) tea.Msg { return RetryTickMsg{} })
}

// RetryWait is the live row between a failed inference attempt and the one that
// replaces it: the provider's own wording, its wait hint, and the attempt about
// to run. Display-only — the failure a dead turn leaves is the persisted
// notice, never this row, which is why it disappears the moment anything else
// starts happening.
type RetryWait struct {
	active  bool
	detail  string        // the provider's wording, e.g. "rate_limit (http 429): slow down"
	hint    time.Duration // Retry-After, when the provider gave one
	attempt int           // the attempt that failed
	since   time.Time     // when that attempt was reported failed
}

// Begin records a failed attempt the provider itself says is worth repeating.
// False for an error a retry cannot fix — an auth failure or a bad request —
// since the row would promise something that will not happen.
func (r *RetryWait) Begin(err error, attempt int, now time.Time) bool {
	if !ai.IsRetryable(err) {
		return false
	}
	r.active = true
	r.detail = err.Error()
	r.hint = ai.RetryAfter(err)
	r.attempt = attempt
	r.since = now
	return true
}

// Clear ends the row: the next attempt started, the turn ended, or the agent
// stopped with nothing left to wait for.
func (r *RetryWait) Clear() { *r = RetryWait{} }

// Active reports whether the row is owed a line right now.
func (r RetryWait) Active() bool { return r.active }

// Line renders the row for one terminal width, or "" when nothing is pending.
// now is the caller's clock so the remaining wait counts down without the row
// holding a timer of its own.
func (r RetryWait) Line(width int, now time.Time) string {
	if !r.active {
		return ""
	}
	text := r.detail + " · retrying"
	if wait := r.remaining(now); wait > 0 {
		text = r.detail + " · retrying in " + kit.WaitText(wait)
	}
	if r.attempt > 0 {
		text += " (attempt " + strconv.Itoa(r.attempt+1) + ")"
	}
	line := kit.CurrentSymbols.Warning + " " + kit.TruncateText(text, max(1, width))
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning).Render(line)
}

// remaining is the wait still owed: the provider's hint minus the time already
// spent waiting on it. A hint already spent reads as none owed.
func (r RetryWait) remaining(now time.Time) time.Duration {
	if r.hint <= 0 || r.since.IsZero() || !now.After(r.since) {
		return r.hint
	}
	return r.hint - now.Sub(r.since)
}
