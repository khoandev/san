package input

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func rateLimitDialog() *RateLimitModel {
	m := NewRateLimit()
	m.Show(RateLimitRequest{
		Title:  "Provider rate limit",
		Detail: "rate_limit (http 429): slow down",
		Hint:   7 * time.Second,
	}, 80, 24)
	return &m
}

// keyMsg is one keypress as a terminal delivers it: named keys carry no text.
func keyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: rune(name[0]), Text: name}
}

// answer presses one key and reports the dialog's answer, or nil when the key
// changed the selection instead of answering.
func answer(t *testing.T, m *RateLimitModel, key string) *RateLimitChoiceMsg {
	t.Helper()
	cmd := m.HandleKeypress(keyMsg(key))
	if cmd == nil {
		return nil
	}
	msg, ok := cmd().(RateLimitChoiceMsg)
	if !ok {
		t.Fatalf("key %q answered with %T, want RateLimitChoiceMsg", key, cmd())
	}
	return &msg
}

// pressKey sends a special key with no text, the way the terminal delivers it.
func pressKey(m *RateLimitModel, key tea.KeyPressMsg) {
	m.HandleKeypress(key)
}

// The dialog says what failed, how long the provider asked to wait, and offers
// both answers. Nothing about the failure is left to the transcript.
func TestRateLimitDialogShowsTheFailureAndItsWait(t *testing.T) {
	m := rateLimitDialog()
	frame := ansi.Strip(m.Render())
	for _, want := range []string{
		"Provider rate limit",
		"rate_limit (http 429): slow down",
		"The provider asked to wait 7s before retrying.",
		"Close",
		"Restart",
		"choose",
		"restart",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("dialog is missing %q:\n%s", want, frame)
		}
	}
}

// A provider that named no wait gets no invented number: the dialog says only
// that nothing was said.
func TestRateLimitDialogInventsNoWait(t *testing.T) {
	m := NewRateLimit()
	m.Show(RateLimitRequest{Title: "Provider overloaded", Detail: "overloaded"}, 80, 24)

	frame := ansi.Strip(m.Render())
	if !strings.Contains(frame, "The provider named no wait before retrying.") {
		t.Fatalf("dialog invented a wait:\n%s", frame)
	}
}

// Enter is the reflex and must not spend an inference on an unread error: the
// cursor starts on Close, and both direct keys answer without moving it.
func TestRateLimitDialogKeysAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		move    func(m *RateLimitModel)
		key     string
		restart bool
	}{
		{"enter closes", nil, "enter", false},
		{"right then enter restarts", func(m *RateLimitModel) { pressKey(m, keyRight) }, "enter", true},
		{"tab then enter restarts", func(m *RateLimitModel) { pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab}) }, "enter", true},
		{"left returns to close", func(m *RateLimitModel) {
			pressKey(m, keyRight)
			pressKey(m, keyLeft)
		}, "enter", false},
		{"esc closes", nil, "esc", false},
		{"c closes", nil, "c", false},
		{"r restarts", nil, "r", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := rateLimitDialog()
			if tc.move != nil {
				tc.move(m)
			}

			key := tc.key
			got := answer(t, m, key)
			if got == nil {
				t.Fatalf("%q did not answer", key)
			}
			if got.Restart != tc.restart {
				t.Fatalf("%q answered Restart=%v, want %v", key, got.Restart, tc.restart)
			}
			if m.IsActive() {
				t.Fatal("the dialog stayed open after answering")
			}
			if m.HandleKeypress(keyEnter) != nil {
				t.Fatal("a closed dialog answered a second time")
			}
		})
	}
}

// The dialog is a docked panel, not a full screen: whatever the provider said
// is cut to the terminal instead of wrapping the frame apart.
func TestRateLimitDialogFitsTheTerminal(t *testing.T) {
	m := NewRateLimit()
	m.Show(RateLimitRequest{
		Title:  "Provider rate limit",
		Detail: strings.Repeat("rate_limit (http 429): slow down. ", 12),
		Hint:   90 * time.Second,
	}, 60, 24)

	for _, line := range strings.Split(ansi.Strip(m.Render()), "\n") {
		if width := lipgloss.Width(line); width > 60 {
			t.Fatalf("line of %d columns overflows the 60-column terminal: %q", width, line)
		}
	}
}
