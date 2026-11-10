package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/kit"
)

// The splash is one row: brand mark, project and model, separated by the same
// " · " the status line uses.
func TestWelcomeIsOneSeparatedRow(t *testing.T) {
	got := ansi.Strip(renderWelcome(welcomeInfo{CWD: "/tmp/san", Model: "opus"}))
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("splash must be one row: %q", got)
	}
	if !strings.HasSuffix(got, "< SAN "+kit.CurrentSymbols.BrandStar+" /> · san · opus") {
		t.Fatalf("splash = %q", got)
	}
}

// Plain output (a pipe, or NO_COLOR) must not inherit a glyph table chosen for
// an interactive terminal: the emoji star is double-width and nerd glyphs can
// be tofu, so the ASCII fallback is the only safe mark there.
func TestPlainWelcomeIgnoresTheLiveSymbolTable(t *testing.T) {
	t.Cleanup(func() { kit.InitSymbols(kit.IconStyleUnicode) })
	kit.InitSymbols(kit.IconStyleEmoji)

	got := plainWelcome(welcomeInfo{CWD: "/tmp/san", Model: "opus"})
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("plain splash must be one row: %q", got)
	}
	plainStar := kit.SymbolsFor(kit.IconStylePlain).BrandStar
	if !strings.Contains(got, "< SAN "+plainStar+" />") {
		t.Fatalf("plain splash is missing the ASCII star: %q", got)
	}
	if emojiStar := kit.SymbolsFor(kit.IconStyleEmoji).BrandStar; strings.Contains(got, emojiStar) {
		t.Fatalf("plain splash used the emoji star: %q", got)
	}
}
