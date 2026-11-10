package kit

import (
	"reflect"
	"testing"
)

// env builds a getenv for resolveIconStyle from a fixed map, so the mapping can
// be tested without touching the process environment or a terminal.
func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestResolveIconStyle(t *testing.T) {
	noColor := map[string]string{"NO_COLOR": "1"}
	for _, tt := range []struct {
		name  string
		style string
		color bool
		env   map[string]string
		want  string
	}{
		{"unset", "", true, nil, IconStyleUnicode},
		{"auto quiet env", IconStyleAuto, true, nil, IconStyleUnicode},
		{"bogus value", "crayon", true, nil, IconStyleUnicode},
		{"explicit unicode", IconStyleUnicode, true, nil, IconStyleUnicode},
		{"explicit emoji", IconStyleEmoji, true, nil, IconStyleEmoji},
		{"explicit nerd", IconStyleNerd, true, nil, IconStyleNerd},
		{"auto on a non-tty", IconStyleAuto, false, nil, IconStylePlain},
		{"auto with NO_COLOR", IconStyleAuto, true, noColor, IconStylePlain},
		{"env picks emoji", IconStyleAuto, true, map[string]string{"SAN_ICON_STYLE": "emoji"}, IconStyleEmoji},
		{"env picks nerd", IconStyleAuto, true, map[string]string{"SAN_ICON_STYLE": "Nerd"}, IconStyleNerd},
		{"env auto is ignored", IconStyleAuto, true, map[string]string{"SAN_ICON_STYLE": "auto"}, IconStyleUnicode},
		{"SAN_NERD_FONT opts in", IconStyleAuto, true, map[string]string{"SAN_NERD_FONT": "1"}, IconStyleNerd},
		{"NF truthy opts in", IconStyleAuto, true, map[string]string{"NF": "true"}, IconStyleNerd},
		{"NF false does not", IconStyleAuto, true, map[string]string{"NF": "0"}, IconStyleUnicode},
		// An explicit choice is the user's instruction: NO_COLOR is about
		// color, so it does not override a named style.
		{"explicit survives NO_COLOR", IconStyleNerd, true, noColor, IconStyleNerd},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveIconStyle(tt.style, tt.color, env(tt.env)); got != tt.want {
				t.Fatalf("resolveIconStyle(%q) = %q, want %q", tt.style, got, tt.want)
			}
		})
	}
}

func TestInitSymbolsInstallsTable(t *testing.T) {
	t.Cleanup(func() { CurrentSymbols = unicodeSymbols })
	for _, tt := range []struct {
		style string
		want  Symbols
	}{
		{IconStyleUnicode, unicodeSymbols},
		{IconStyleEmoji, emojiSymbols},
		{IconStyleNerd, nerdSymbols},
	} {
		got := InitSymbols(tt.style)
		if got != tt.style {
			t.Fatalf("InitSymbols(%q) resolved to %q", tt.style, got)
		}
		if !reflect.DeepEqual(CurrentSymbols, tt.want) {
			t.Fatalf("InitSymbols(%q) installed %+v", tt.style, CurrentSymbols)
		}
	}
	// An unrecognized style resolves the way "auto" does rather than leaving an
	// empty table, and the return value reports what was drawn. Which table
	// "auto" picks depends on the terminal this test runs in, so compare
	// against the same resolution instead of a fixed glyph set.
	want := ResolveIconStyle("crayon")
	got := InitSymbols("crayon")
	if got != want {
		t.Fatalf("unknown style resolved to %q, want %q", got, want)
	}
	table := map[string]Symbols{
		IconStyleUnicode: unicodeSymbols,
		IconStyleEmoji:   emojiSymbols,
		IconStyleNerd:    nerdSymbols,
		IconStylePlain:   plainSymbols,
	}[got]
	if !reflect.DeepEqual(CurrentSymbols, table) {
		t.Fatalf("unknown style resolved to %q but installed %+v", got, CurrentSymbols)
	}
}

// TestEveryTableFillsEveryField is the guard that keeps Symbols closed: a new
// field left unset in one table would draw an empty cell in that style only,
// which is exactly the bug a visual check of the default misses.
func TestEveryTableFillsEveryField(t *testing.T) {
	for name, table := range map[string]Symbols{
		"unicode": unicodeSymbols,
		"emoji":   emojiSymbols,
		"nerd":    nerdSymbols,
		"plain":   plainSymbols,
	} {
		v := reflect.ValueOf(table)
		for i := range v.NumField() {
			field := v.Field(i)
			if field.Kind() == reflect.Slice {
				if field.Len() == 0 {
					t.Errorf("%s table: %s is empty", name, v.Type().Field(i).Name)
				}
				continue
			}
			if field.String() == "" {
				t.Errorf("%s table: %s is unset", name, v.Type().Field(i).Name)
			}
		}
	}
}

// TestUnicodeTableKeepsTheOldGlyphs pins the default table to the glyphs the
// TUI drew before icon styles existed: they are what every terminal without a
// preference sees, and what the golden tests compare against.
func TestUnicodeTableKeepsTheOldGlyphs(t *testing.T) {
	for _, tt := range []struct {
		name string
		got  string
		want string
	}{
		{"LiveDot", unicodeSymbols.LiveDot, "●"},
		{"ThinkingStar", unicodeSymbols.ThinkingStar, "✦"},
		{"MemoryMark", unicodeSymbols.MemoryMark, "⟡"},
		{"Success", unicodeSymbols.Success, "✓"},
		{"Error", unicodeSymbols.Error, "✗"},
		{"RadioOn", unicodeSymbols.RadioOn, "●"},
		{"RadioOff", unicodeSymbols.RadioOff, "○"},
		{"Cursor", unicodeSymbols.Cursor, "▸"},
		{"PlanBadge", unicodeSymbols.PlanBadge, "✎"},
		{"ExecuteBadge", unicodeSymbols.ExecuteBadge, "▶"},
		{"AutoAccept", unicodeSymbols.AutoAccept, "⏵⏵"},
		{"GroupMark", unicodeSymbols.GroupMark, "◆"},
		{"ToolResult", unicodeSymbols.ToolResult, "⎿"},
		{"BrandStar", unicodeSymbols.BrandStar, "✦"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if CurrentSymbols.LiveDot != unicodeSymbols.LiveDot {
		t.Fatal("CurrentSymbols must start at the Unicode table")
	}
}

func TestToolIconWidthFollowsTheTable(t *testing.T) {
	t.Cleanup(func() { CurrentSymbols = unicodeSymbols })
	for _, tt := range []struct {
		style string
		want  int
	}{
		{IconStyleUnicode, 2}, // one column of glyph plus the gap — the old constant
		{IconStyleNerd, 2},
		{IconStylePlain, 2},
		{IconStyleEmoji, 3}, // double-width emoji plus the gap
	} {
		InitSymbols(tt.style)
		if got := ToolIconWidth(); got != tt.want {
			t.Fatalf("ToolIconWidth(%s) = %d, want %d", tt.style, got, tt.want)
		}
	}
}

func TestWidthMeasuresGlyphs(t *testing.T) {
	for _, tt := range []struct {
		s    string
		want int
	}{
		{"●", 1},
		{"⏵⏵", 2},
		{"🟢", 2},
		{"abc", 3},
	} {
		if got := Width(tt.s); got != tt.want {
			t.Fatalf("Width(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}
