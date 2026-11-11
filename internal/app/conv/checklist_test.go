package conv

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

// sgrPrefix returns the opening SGR sequence a style emits, so a test can prove
// which theme color a rendered row used without hard-coding RGB values.
func sgrPrefix(style lipgloss.Style, text string) string {
	rendered := style.Render(text)
	if i := strings.IndexByte(rendered, 'm'); i >= 0 {
		return rendered[:i+1]
	}
	return ""
}

func TestChecklistDoneStrikesLabel(t *testing.T) {
	row := RenderChecklistRow(ChecklistRow{
		State:  ChecklistDone,
		Text:   "audit handlers",
		Detail: "4s",
		Width:  60,
	})

	if !strings.Contains(row, sgrStrikeOn) {
		t.Fatalf("done row has no SGR strike (%q):\n%q", sgrStrikeOn, row)
	}
	if !strings.Contains(row, sgrStrikeOff) {
		t.Fatalf("done row has no SGR strike reset (%q):\n%q", sgrStrikeOff, row)
	}
	plain := stripANSI(row)
	if !strings.Contains(plain, "audit handlers") {
		t.Fatalf("done row lost its label:\n%s", plain)
	}
	// The trailing note must stay readable: a completed row still says why or
	// when it ended.
	if reset, detail := strings.Index(row, sgrStrikeOff), strings.Index(row, "4s"); reset < 0 || detail < reset {
		t.Fatalf("detail sits inside the strike span:\n%q", row)
	}
}

func TestChecklistSkippedStrikesLabel(t *testing.T) {
	row := RenderChecklistRow(ChecklistRow{State: ChecklistSkipped, Text: "rewrite tests", Width: 40})
	if !strings.Contains(row, sgrStrikeOn) || !strings.Contains(row, sgrStrikeOff) {
		t.Fatalf("skipped row is not struck through:\n%q", row)
	}
	if !strings.Contains(stripANSI(row), "rewrite tests") {
		t.Fatalf("skipped row lost its label:\n%s", stripANSI(row))
	}
}

// Only the active row breathes; a done row must render identically on every
// frame so a finished list is a stable record.
func TestChecklistActiveBreathesAcrossFrames(t *testing.T) {
	active := func(frame int) string {
		return RenderChecklistRow(ChecklistRow{State: ChecklistActive, Frame: frame, Text: "move files", Width: 40})
	}
	first, peak := active(0), active(3)
	if first == peak {
		t.Fatalf("active row did not change across frames:\n%q", first)
	}
	if !strings.Contains(stripANSI(first), kit.CurrentSymbols.LiveDot) {
		t.Fatalf("active row does not lead with the live dot:\n%s", stripANSI(first))
	}
	if done := RenderChecklistRow(ChecklistRow{State: ChecklistDone, Text: "move files", Width: 40}); done != RenderChecklistRow(ChecklistRow{State: ChecklistDone, Text: "move files", Width: 40}) {
		t.Fatal("done row changed between renders")
	}
}

func TestChecklistBlockedAndFailedUseStatusColors(t *testing.T) {
	cases := []struct {
		name  string
		state ChecklistState
		color kit.AdaptiveColor
		glyph string
	}{
		{"blocked", ChecklistBlocked, kit.CurrentTheme.Warning, kit.CurrentSymbols.Warning},
		{"failed", ChecklistFailed, kit.CurrentTheme.Error, kit.CurrentSymbols.Error},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := RenderChecklistRow(ChecklistRow{State: tc.state, Text: "wire the API", Width: 60})
			want := sgrPrefix(lipgloss.NewStyle().Foreground(tc.color), "x")
			if !strings.Contains(row, want) {
				t.Fatalf("%s row does not use the theme color %q:\n%q", tc.name, want, row)
			}
			if plain := stripANSI(row); !strings.Contains(plain, tc.glyph+"  wire the API") {
				t.Fatalf("%s row does not lead with %q:\n%s", tc.name, tc.glyph, plain)
			}
			if strings.Contains(row, sgrStrikeOn) {
				t.Fatalf("%s row was struck through:\n%q", tc.name, row)
			}
		})
	}
}

func TestChecklistGlyphsFollowSymbolTable(t *testing.T) {
	cases := map[ChecklistState]string{
		ChecklistPending: kit.CurrentSymbols.RadioOff,
		ChecklistActive:  kit.CurrentSymbols.LiveDot,
		ChecklistDone:    kit.CurrentSymbols.Success,
		ChecklistSkipped: "-",
		ChecklistBlocked: kit.CurrentSymbols.Warning,
		ChecklistFailed:  kit.CurrentSymbols.Error,
		ChecklistStalled: kit.CurrentSymbols.LiveDot,
	}
	for state, want := range cases {
		if got := stripANSI(state.Glyph(0)); got != want {
			t.Errorf("state %d glyph = %q, want %q", state, got, want)
		}
	}
}

// Every row stays inside its column budget, whatever the label and note cost.
func TestRenderChecklistRowFitsWidth(t *testing.T) {
	states := []ChecklistState{
		ChecklistPending, ChecklistActive, ChecklistDone, ChecklistSkipped,
		ChecklistBlocked, ChecklistFailed, ChecklistStalled,
	}
	longText := "refactor the authentication middleware và giữ tương thích 旧客户端"
	rows := []ChecklistRow{
		{State: ChecklistDone, Text: longText, Detail: "a very long trailing note that should yield", Width: 80},
		{State: ChecklistBlocked, Tag: "#12", TagWidth: 4, Text: longText, Detail: "the API moved", Width: 40},
		{State: ChecklistPending, Tag: "1.", Text: longText, Width: 12},
		{State: ChecklistActive, Text: longText, Width: 3},
	}
	for _, base := range rows {
		for _, state := range states {
			row := base
			row.State = state
			for _, width := range []int{1, 2, 4, 7, 12, 20, 40, 80} {
				row.Width = width
				got := RenderChecklistRow(row)
				if w := lipgloss.Width(got); w > width {
					t.Fatalf("state %d width %d rendered %d columns:\n%q", state, width, w, got)
				}
				if lines := strings.Count(got, "\n"); lines != 0 {
					t.Fatalf("state %d width %d rendered %d newlines", state, width, lines)
				}
				if on, off := strings.Count(got, sgrStrikeOn), strings.Count(got, sgrStrikeOff); on != off {
					t.Fatalf("state %d width %d left an unbalanced strike (%d on, %d off):\n%q", state, width, on, off, got)
				}
			}
		}
	}
}

// The label is cut before it is styled, so the strike wraps the truncated text
// and no escape sequence is left dangling by the cut.
func TestRenderChecklistRowTruncatesBeforeStyling(t *testing.T) {
	row := RenderChecklistRow(ChecklistRow{
		State: ChecklistDone,
		Text:  strings.Repeat("wide ", 20),
		Width: 30,
	})
	plain := stripANSI(row)
	if !strings.HasSuffix(plain, "…") {
		t.Fatalf("long label was not truncated:\n%s", plain)
	}
	if got := strings.Count(row, sgrStrikeOn); got != 1 {
		t.Fatalf("truncated row has %d strike-on sequences, want 1:\n%q", got, row)
	}
	if ellipsis, reset := strings.Index(row, "…"), strings.Index(row, sgrStrikeOff); ellipsis < 0 || reset < ellipsis {
		t.Fatalf("strike does not wrap the truncated label:\n%q", row)
	}
}

func TestRenderChecklistRowAlignsTags(t *testing.T) {
	short := stripANSI(RenderChecklistRow(ChecklistRow{Tag: "#1", TagWidth: 4, Text: "first", Width: 40}))
	long := stripANSI(RenderChecklistRow(ChecklistRow{Tag: "#10", TagWidth: 4, Text: "second", Width: 40}))

	column := func(line, label string) int {
		i := strings.Index(line, label)
		if i < 0 {
			t.Fatalf("label %q missing from %q", label, line)
		}
		return lipgloss.Width(line[:i])
	}
	if a, b := column(short, "first"), column(long, "second"); a != b {
		t.Fatalf("labels start at columns %d and %d:\n%q\n%q", a, b, short, long)
	}
}

// The note yields to the label: a short row still names its work in full, and
// even a cramped one keeps more than a glyph.
func TestRenderChecklistRowKeepsLabelWhenNarrow(t *testing.T) {
	row := func(width int) string {
		return stripANSI(RenderChecklistRow(ChecklistRow{
			State:  ChecklistBlocked,
			Text:   "wire the API",
			Detail: "waiting on the upstream migration",
			Width:  width,
		}))
	}
	if plain := row(40); !strings.Contains(plain, "wire the API") {
		t.Fatalf("row lost its label to the note:\n%s", plain)
	}
	if plain := row(20); !strings.Contains(plain, "wire") {
		t.Fatalf("cramped row lost its label to the note:\n%s", plain)
	}
}
