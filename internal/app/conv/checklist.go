package conv

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

// SGR strike-on and strike-off. lipgloss v2 styles a struck string one rune at
// a time and closes every rune with SGR 0, so its output never carries the
// explicit 29 (strike off). A checklist row colors its label once and wraps
// that span in this pair instead: the strike then covers the whole label,
// whitespace included, and ends with the attribute it names.
const (
	sgrStrikeOn  = "\x1b[9m"
	sgrStrikeOff = "\x1b[29m"
)

// ChecklistState is how one row of a todo list or a plan-step checklist reads.
// It is the single place that decides a row's glyph, color and strike, so the
// tracker above the input and the Execution Panel cannot drift apart.
type ChecklistState int

const (
	// ChecklistPending is work nothing has started.
	ChecklistPending ChecklistState = iota
	// ChecklistActive is the row being worked on right now.
	ChecklistActive
	// ChecklistDone is work that finished.
	ChecklistDone
	// ChecklistSkipped is work deliberately passed over or stopped.
	ChecklistSkipped
	// ChecklistBlocked is work waiting on the person.
	ChecklistBlocked
	// ChecklistFailed is work that ended in an error.
	ChecklistFailed
	// ChecklistStalled is work marked in progress with nothing executing it.
	ChecklistStalled
)

// retired reports whether a state closes its row. Retired labels are struck
// through, so finished work stays on screen as a record without competing with
// the work still moving.
func (s ChecklistState) retired() bool {
	return s == ChecklistDone || s == ChecklistSkipped
}

// Glyph returns the status mark the state draws. Only the active state moves:
// it breathes on the shared frame tick, while every other state holds still, so
// a row never claims motion it does not have. Stalled reuses the live dot as a
// solid mark because its work did start — nothing is advancing it.
func (s ChecklistState) Glyph(frame int) string {
	switch s {
	case ChecklistActive:
		return kit.LiveDot(frame)
	case ChecklistDone:
		return kit.CurrentSymbols.Success
	case ChecklistSkipped:
		// A dash is punctuation, not an icon (see kit.Symbols), so it stays
		// literal: skipped work reads as closed rather than as a second done
		// check a skimming eye might trust.
		return "-"
	case ChecklistBlocked:
		return kit.CurrentSymbols.Warning
	case ChecklistFailed:
		return kit.CurrentSymbols.Error
	case ChecklistStalled:
		return kit.CurrentSymbols.LiveDot
	default:
		return kit.CurrentSymbols.RadioOff
	}
}

// style returns the label style a state draws. It is unexported on purpose: a
// caller reaching for it directly would get the color but silently lose the
// strike a retired state owes its label, so rows go through Render instead.
func (s ChecklistState) style() lipgloss.Style {
	switch s {
	case ChecklistActive:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Accent).Bold(true)
	case ChecklistDone:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
	case ChecklistSkipped:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	case ChecklistBlocked:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
	case ChecklistFailed:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
	default:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	}
}

// Render colors a label and, for a retired state, strikes it through. Pass
// already-truncated text: the strike is a wrapper, so styling first and cutting
// afterwards would leave a dangling escape sequence behind.
func (s ChecklistState) Render(text string) string {
	return s.render(text, kit.AdaptiveColor{})
}

// render is Render with an optional color override. A zero tint keeps the
// state's palette; a set one (an owning agent's color) wins over the color but
// never over the strike a retired state owes its label.
func (s ChecklistState) render(text string, tint kit.AdaptiveColor) string {
	if text == "" {
		return ""
	}
	style := s.style()
	if tint != (kit.AdaptiveColor{}) {
		style = style.Foreground(tint)
	}
	styled := style.Render(text)
	if !s.retired() {
		return styled
	}
	return sgrStrikeOn + styled + sgrStrikeOff
}

// ChecklistRow is one line of a checklist: a status glyph, an optional ordinal
// or id, the label, and an optional note.
type ChecklistRow struct {
	// State drives the row's glyph, color and strike.
	State ChecklistState
	// Frame advances the active glyph's breath; other states ignore it.
	Frame int
	// Indent leads the row. Empty means two spaces.
	Indent string
	// Tag is the ordinal or id shown before the label ("1.", "#4"). Empty
	// draws no tag.
	Tag string
	// TagWidth pads Tag to this display width so sibling rows align their
	// labels. Zero leaves the tag as written.
	TagWidth int
	// Text is the label. It is truncated before it is styled.
	Text string
	// Detail is an optional trailing note; it is muted and never struck, so a
	// completed row still shows why or when it ended.
	Detail string
	// Width is the total column budget. Values below one draw one column.
	Width int
	// Tint overrides the label (and glyph) color for a row owned by a colored
	// background agent. The zero value keeps the state's palette.
	Tint kit.AdaptiveColor
}

// minLabelColumns is the room a row keeps for its label before it spends
// columns on the trailing note.
const minLabelColumns = 8

// checklistDetailStyle is the one tone for a row's trailing note, so a finished
// label's strike never reaches it.
var checklistDetailStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)

// RenderChecklistRow renders one row and never exceeds Width columns. The label
// is truncated before it is styled, so a cut never lands inside an escape
// sequence and the strike covers exactly the text that survived.
func RenderChecklistRow(row ChecklistRow) string {
	width := max(1, row.Width)
	indent := row.Indent
	if indent == "" {
		indent = "  "
	}
	tag := row.Tag
	if tag != "" {
		if pad := row.TagWidth - lipgloss.Width(tag); pad > 0 {
			tag += strings.Repeat(" ", pad)
		}
	}
	glyph := row.State.Glyph(row.Frame)
	if row.State != ChecklistActive {
		// The active glyph already carries its breath color; tinting it again
		// would freeze the animation.
		glyphStyle := row.State.style()
		if row.Tint != (kit.AdaptiveColor{}) {
			glyphStyle = glyphStyle.Foreground(row.Tint)
		}
		glyph = glyphStyle.Render(glyph)
	}
	prefix := indent + glyph + "  "
	if tag != "" {
		prefix += tag + "  "
	}
	prefixWidth := lipgloss.Width(prefix)

	// One row means one line: a label or note that arrived with a newline (a
	// model-written reason, say) is flattened before it is measured.
	text := strings.Join(strings.Fields(row.Text), " ")
	note := strings.Join(strings.Fields(row.Detail), " ")

	// The note yields to the label: a row with a long reason still names the
	// work it is about, keeping minLabelColumns plus the two-space gap.
	detail := ""
	if note != "" {
		budget := min(width-prefixWidth-minLabelColumns-2, max(1, width/2))
		if budget > 0 {
			detail = "  " + checklistDetailStyle.Render(kit.TruncateText(note, budget))
		}
	}

	if budget := width - prefixWidth - lipgloss.Width(detail); budget > 0 {
		text = kit.TruncateText(text, budget)
	} else {
		text = ""
	}
	// The final cut is a safety net for a budget narrower than the prefix; it
	// only ever shortens already-composed output.
	return kit.TruncateText(prefix+row.State.render(text, row.Tint)+detail, width)
}
