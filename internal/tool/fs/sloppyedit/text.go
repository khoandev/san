package sloppyedit

import "unicode/utf8"

// truncateRunes shortens s to at most n runes, appending suffix when it cuts.
// Diagnostics quote file fragments, so the cut counts runes and never splits a
// multi-byte character.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
