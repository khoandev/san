package sloppyedit

import (
	"strings"
	"unicode/utf8"
)

// normText is source text mapped into the matching space: whitespace is
// dropped and typographic characters fold to their ASCII spellings, so pattern
// and file may differ in indentation, spacing, and punctuation style.
//
// Both views — the matching-space text and the map from every normalized byte
// back to the source byte it came from — are built on first use. An anchor
// copied from the file matches at the literal rung without either, and folding
// a whole file is the engine's largest per-edit allocation, so it is paid only
// when the tolerant rungs run.
type normText struct {
	source string
	built  bool
	norm   string
	starts []int // starts[i] is the source byte where normalized byte i begins
}

// normalizeText prepares source for the matching space. Nothing is folded
// until the matching space or the source map is asked for.
func normalizeText(source string) normText { return normText{source: source} }

// matching is the source in the matching space.
func (n *normText) matching() string {
	n.build()
	return n.norm
}

// normalizeString is the matching-space text of source, for the callers that
// need only the text and never a source offset.
func normalizeString(source string) string {
	text, _ := foldSource(source, false)
	return text
}

// build folds the source once, keeping both views of the result.
func (n *normText) build() {
	if n.built {
		return
	}
	n.built = true
	n.norm, n.starts = foldSource(n.source, true)
}

// foldSource maps source into the matching space in one pass, returning that
// text and, when wantMap is set, the source byte every normalized byte starts
// at.
func foldSource(source string, wantMap bool) (string, []int) {
	var text strings.Builder
	text.Grow(len(source))
	var starts []int
	if wantMap {
		starts = make([]int, 0, len(source))
	}
	for offset := 0; offset < len(source); {
		r, width := utf8.DecodeRuneInString(source[offset:])
		switch {
		case isFoldedWhitespace(r):
			offset += width
		case r < utf8.RuneSelf:
			// Plain ASCII copies byte for byte, so take the whole run in one
			// write rather than one write per character.
			end := offset + width
			for end < len(source) && isPlainASCII(source[end]) {
				end++
			}
			if wantMap {
				for at := offset; at < end; at++ {
					starts = append(starts, at)
				}
			}
			text.WriteString(source[offset:end])
			offset = end
		default:
			spelling := foldRune(r)
			if wantMap {
				for range len(spelling) {
					starts = append(starts, offset)
				}
			}
			text.WriteString(spelling)
			offset += width
		}
	}
	return text.String(), starts
}

// isPlainASCII reports a byte the matching space copies verbatim.
func isPlainASCII(b byte) bool {
	return b < utf8.RuneSelf && !isFoldedWhitespace(rune(b))
}

// isFoldedWhitespace reports the characters the matching space erases: ASCII
// whitespace plus the Unicode whitespace JavaScript's trim removes.
func isFoldedWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// foldRune maps one non-ASCII character into its ASCII spelling: dash, quote,
// and not-equal variants fold to ASCII, zero-width characters vanish, and
// everything else is kept verbatim.
func foldRune(r rune) string {
	switch {
	case r >= 0x2010 && r <= 0x2015, r == 0x2212:
		return "-"
	case r >= 0x2018 && r <= 0x201B:
		return "'"
	case r >= 0x201C && r <= 0x201F:
		return "\""
	case r == 0x2260:
		return "!="
	case r == 0xBD:
		return "1/2"
	case r >= 0x200B && r <= 0x200D, r == 0x2016, r == 0x2017:
		return ""
	default:
		return string(r)
	}
}

// sourceStart maps a normalized byte offset to the source byte where that
// byte's source character starts.
func (n *normText) sourceStart(offset, fallback int) int {
	n.build()
	if offset < len(n.starts) {
		return n.starts[offset]
	}
	return fallback
}

// sourceEnd maps a normalized byte offset to the source byte where the
// previous normalized byte's source character ends. The end is the width of
// the source rune the map points at, so only the starts are stored.
func (n *normText) sourceEnd(offset, fallback int) int {
	if offset == 0 {
		return 0
	}
	n.build()
	if offset-1 >= len(n.starts) {
		return fallback
	}
	start := n.starts[offset-1]
	_, width := utf8.DecodeRuneInString(n.source[start:])
	return start + width
}

// levenshteinDistance is the edit distance over Unicode characters, with the
// shared prefix and suffix trimmed before the rolling-array dynamic program.
func levenshteinDistance(left, right string) int {
	a, b := []rune(left), []rune(right)
	if len(a) > len(b) {
		a, b = b, a
	}
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	if len(a) == 0 {
		return len(b)
	}
	previous := make([]int, len(a)+1)
	current := make([]int, len(a)+1)
	for i := range previous {
		previous[i] = i
	}
	for j := 1; j <= len(b); j++ {
		current[0] = j
		for i := 1; i <= len(a); i++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[i] = min(min(current[i-1]+1, previous[i]+1), previous[i-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(a)]
}
