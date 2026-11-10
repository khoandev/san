package kit

import (
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

// The icon styles the TUI can draw. The settings schema owns the stored values
// (see setting.IconStyle*); this package owns what each one draws. kit is a leaf
// and must not import setting, so the literals are repeated here with the
// schema as the source of truth for the set.
const (
	// IconStyleAuto picks a style from the environment (see ResolveIconStyle).
	IconStyleAuto = "auto"
	// IconStyleUnicode draws the default symbol set: the glyphs the TUI has
	// always used (●, ✦, ✓, …). It is the zero-value default.
	IconStyleUnicode = "unicode"
	// IconStyleEmoji draws colorful emoji. Every emoji is double-width, so any
	// row that pads by a glyph's width must measure it instead of assuming one
	// column.
	IconStyleEmoji = "emoji"
	// IconStyleNerd draws Nerd Font private-use glyphs; a terminal without a
	// patched font renders tofu boxes instead.
	IconStyleNerd = "nerd"
	// IconStylePlain is never stored or chosen by the user: it is the ASCII
	// set used when the output cannot carry either colors or the assumed
	// glyph repertoire (see ResolveIconStyle).
	IconStylePlain = "plain"
)

// Symbols is the closed set of semantic glyphs the TUI draws. It is closed on
// purpose: punctuation and framing characters — the separators "·" and "—", the
// ellipsis "…", the arrows "→" "←" "↑" "↓", the rules "│" "─", the focus bar
// "▎", the context meter's block glyphs "█" "░" and "[✓]" inside rendered
// markdown — are not icons. They carry no meaning that would change between
// styles, so they stay literal at their call sites and never enter a table
// here. Adding a field is a deliberate act: every table below must gain a
// value, in the same change.
type Symbols struct {
	// LiveDot is the in-flight indicator — breathing while work runs, solid
	// once it settles (see LiveDot).
	LiveDot string
	// ThinkingStar leads a reasoning block and the waiting row (see LiveStar).
	ThinkingStar string
	// MemoryMark identifies a memory being read or supplied as context.
	MemoryMark string
	// Success and Error mark a settled result, one line or one block.
	Success string
	Error   string
	// Warning marks a condition that wants attention but is not a failure.
	Warning string
	// RadioOn and RadioOff are the persisted-choice markers of a radio group.
	RadioOn  string
	RadioOff string
	// Cursor marks the hovered row of a picker that has no FocusBar column.
	Cursor string
	// PlanBadge and ExecuteBadge lead the plan-mode status badge.
	PlanBadge    string
	ExecuteBadge string
	// AutoAccept leads the "accept edits", autopilot and YOLO mode indicator.
	AutoAccept string
	// GroupMark leads the session's group identity ("◆ host/self").
	GroupMark string
	// ToolResult leads a tool's result trailer line ("⎿ …").
	ToolResult string
	// BrandStar is the star inside the "< SAN ✦ />" wordmark.
	BrandStar string
	// SpinnerFrames is the animation of a generic waiting spinner; it is the
	// LiveDot repeated, so every live indicator moves in step.
	SpinnerFrames []string
}

// The Unicode table is the default and must keep the glyphs the TUI has always
// drawn: it is what every terminal without a chosen preference sees, and what
// the golden tests compare against.
var unicodeSymbols = Symbols{
	LiveDot:       "●",
	ThinkingStar:  ThinkingMark,
	MemoryMark:    MemoryMark,
	Success:       "✓",
	Error:         "✗",
	Warning:       "⚡",
	RadioOn:       "●",
	RadioOff:      "○",
	Cursor:        "▸",
	PlanBadge:     "✎",
	ExecuteBadge:  "▶",
	AutoAccept:    "⏵⏵",
	GroupMark:     "◆",
	ToolResult:    "⎿",
	BrandStar:     "✦",
	SpinnerFrames: []string{"●"},
}

// The emoji table trades the quiet symbols for colorful, double-width ones.
// Emoji ignore the foreground color a row applies, which is why the monochrome
// style stays the default.
var emojiSymbols = Symbols{
	LiveDot:       "🟢",
	ThinkingStar:  "✨",
	MemoryMark:    "🧠",
	Success:       "✅",
	Error:         "❌",
	Warning:       "⚠️",
	RadioOn:       "🔘",
	RadioOff:      "⚪",
	Cursor:        "👉",
	PlanBadge:     "📝",
	ExecuteBadge:  "▶️",
	AutoAccept:    "⏩",
	GroupMark:     "👥",
	ToolResult:    "📎",
	BrandStar:     "✨",
	SpinnerFrames: []string{"🟢"},
}

// The Nerd Font table uses the private-use glyphs a patched font maps back to
// Font Awesome and friends. A terminal without such a font draws a box.
var nerdSymbols = Symbols{
	LiveDot:       "\uf111", // nf-fa-circle
	ThinkingStar:  "\uf005", // nf-fa-star
	MemoryMark:    "\uf1c0", // nf-fa-database
	Success:       "\uf00c", // nf-fa-check
	Error:         "\uf00d", // nf-fa-times
	Warning:       "\uf071", // nf-fa-exclamation-triangle
	RadioOn:       "\uf192", // nf-fa-dot-circle-o
	RadioOff:      "\uf10c", // nf-fa-circle-o
	Cursor:        "\uf0da", // nf-fa-caret-right
	PlanBadge:     "\uf040", // nf-fa-pencil
	ExecuteBadge:  "\uf04b", // nf-fa-play
	AutoAccept:    "\uf04e", // nf-fa-forward
	GroupMark:     "\uf0c0", // nf-fa-users
	ToolResult:    "\uf149", // nf-fa-level-down
	BrandStar:     "\uf005", // nf-fa-star
	SpinnerFrames: []string{"\uf111"},
}

// The ASCII table is the fallback for output that cannot carry the assumed
// glyphs. It is deliberately narrow: one ASCII character wherever the Unicode
// table has one, so column math that measured the Unicode cell still holds.
var plainSymbols = Symbols{
	LiveDot:       "*",
	ThinkingStar:  "*",
	MemoryMark:    "%",
	Success:       "v",
	Error:         "x",
	Warning:       "!",
	RadioOn:       "(*)",
	RadioOff:      "( )",
	Cursor:        ">",
	PlanBadge:     "P",
	ExecuteBadge:  ">",
	AutoAccept:    ">>",
	GroupMark:     "#",
	ToolResult:    "\\",
	BrandStar:     "*",
	SpinnerFrames: []string{"*"},
}

// CurrentSymbols is the glyph table every render path reads. It starts at the
// Unicode table so a process that never calls InitSymbols (a test, a CLI
// subcommand that renders a line) draws exactly what it did before the icon
// style existed.
var CurrentSymbols = unicodeSymbols

// InitSymbols resolves style and installs its table as CurrentSymbols. Call it
// once at startup, and again whenever the user picks a different style. It is
// cheap and does no terminal I/O.
func InitSymbols(style string) string {
	resolved := ResolveIconStyle(style)
	switch resolved {
	case IconStyleEmoji:
		CurrentSymbols = emojiSymbols
	case IconStyleNerd:
		CurrentSymbols = nerdSymbols
	case IconStylePlain:
		CurrentSymbols = plainSymbols
	default:
		CurrentSymbols = unicodeSymbols
	}
	return resolved
}

// ResolveIconStyle maps a stored style to the style that will actually be
// drawn. It is the only place that maps "auto" to a table:
//
//   - An explicit unicode, emoji or nerd choice is honored as-is: the user
//     overrode the guess, and NO_COLOR is a statement about color, not glyphs.
//   - "auto" (or anything unrecognized) consults the environment: a
//     non-color/non-TTY stdout, or NO_COLOR, falls back to plain ASCII; else
//     SAN_ICON_STYLE (unicode, emoji, nerd) picks a table, SAN_NERD_FONT or NF
//     being truthy opts into Nerd Font, and everything else stays Unicode.
//
// A terminal cannot be asked which font it renders, so the Nerd Font arm is an
// opt-in signal rather than a probe; guessing wrong draws tofu.
func ResolveIconStyle(style string) string {
	return resolveIconStyle(style, SupportsColor(), os.Getenv)
}

// resolveIconStyle is ResolveIconStyle with its environment injected, so the
// mapping is testable without a terminal.
func resolveIconStyle(style string, color bool, getenv func(string) string) string {
	switch style {
	case IconStyleUnicode, IconStyleEmoji, IconStyleNerd:
		return style
	}
	if !color || getenv("NO_COLOR") != "" {
		return IconStylePlain
	}
	if env := normalizeIconStyle(getenv("SAN_ICON_STYLE")); env != "" {
		return env
	}
	if truthy(getenv("SAN_NERD_FONT")) || truthy(getenv("NF")) {
		return IconStyleNerd
	}
	return IconStyleUnicode
}

// normalizeIconStyle returns the concrete style an environment variable names,
// or "" when it names none of them (including "auto", which cannot recurse).
func normalizeIconStyle(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case IconStyleUnicode, IconStyleEmoji, IconStyleNerd:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// SupportsColor reports whether stdout can carry styled output: a terminal with
// NO_COLOR unset. It is the one answer the UI uses for "may I draw glyphs and
// color here", shared by the splash and the symbol resolution.
func SupportsColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Width is the display width of s in terminal columns. Every row that aligns
// by glyph width must use it rather than len or a constant: an emoji occupies
// two columns, a Nerd Font glyph one, and a string of either several.
func Width(s string) int {
	return lipgloss.Width(s)
}

// ToolIconWidth is the column budget a tool row reserves for its icon and the
// gap after it. It follows the live symbol table instead of a constant, so a
// double-width emoji icon does not overrun the label beside it.
func ToolIconWidth() int {
	return max(1, Width(CurrentSymbols.LiveDot)) + 1
}
