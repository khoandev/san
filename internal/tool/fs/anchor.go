package fs

import (
	"strings"
	"sync"
	"unicode"
)

// Line anchors are short, opaque handles attached to one line of one file.
// Read prints them beside the line (`12 qwert│content`); Edit targets a range
// by its first and last anchor instead of by copied text. Two failures that
// dominate exact-string editing disappear: a model that mistypes indentation
// while copying a line no longer misses its target, and an edit aimed at a line
// that changed since it was read is refused by name instead of landing somewhere
// plausible.
//
// Anchors are minted from one per-session counter, never reused. An anchor
// therefore names exactly one file and line, and a stale anchor resolves to
// nothing — the recovery (Read again) is unambiguous — instead of to whatever
// line now occupies that position.
const (
	// AnchorLen is the number of characters in an anchor.
	AnchorLen = 5
)

// anchorAlphabet is a fixed permutation of a-z. Two properties matter: every
// anchor is plain lowercase letters (no digits, so it can never be confused
// with a line number), and consecutive anchors differ in their last characters
// (`qwert`, `qwerw`) but read as unrelated words rather than as one long run a
// model might conflate.
const anchorAlphabet = "qwertyuiopasdfghjklzxcvbnm"

const anchorSpace = 26 * 26 * 26 * 26 * 26

// anchorBlocklist holds five-letter anchors that are never minted. A generated
// word sitting beside a line of code reads as an editorial comment; skipping
// the few that are actual words costs nothing because anchors are handed out
// by a counter, not derived from content.
var anchorBlocklist = map[string]struct{}{
	"asses": {}, "bitch": {}, "boobs": {}, "chink": {}, "cocks": {},
	"coons": {}, "cunts": {}, "dicks": {}, "dildo": {}, "dykes": {},
	"fagot": {}, "fucks": {}, "kikes": {}, "nigga": {}, "penis": {},
	"pussy": {}, "raped": {}, "rapes": {}, "shits": {}, "sluts": {},
	"spics": {}, "tards": {}, "titty": {}, "whore": {},
}

// anchorAt renders the n-th anchor. n must be below anchorSpace.
func anchorAt(n int) string {
	var b [AnchorLen]byte
	for i := AnchorLen - 1; i >= 0; i-- {
		b[i] = anchorAlphabet[n%26]
		n /= 26
	}
	return string(b[:])
}

// anchorSeq is the session's mint cursor. It restarts with the conversation
// (ResetFileViews) so a new session never inherits another conversation's
// handles, and it only ever grows so a minted anchor is never handed out twice.
var (
	anchorSeqMu sync.Mutex
	anchorSeq   int
)

// mintAnchor hands out the next anchor. Callers hold anchorsMu; the separate
// mutex only guards the cursor against a caller that does not.
func mintAnchor() string {
	anchorSeqMu.Lock()
	defer anchorSeqMu.Unlock()
	for {
		anchor := anchorAt(anchorSeq)
		anchorSeq++
		if _, blocked := anchorBlocklist[anchor]; blocked {
			continue
		}
		return anchor
	}
}

// validAnchor reports whether s is a well-formed anchor: exactly AnchorLen
// lowercase letters. Anything else is a copy mistake worth naming.
func validAnchor(s string) bool {
	if len(s) != AnchorLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// canonicalLine normalizes a line for fingerprinting: a trailing CR is a line
// ending, and trailing whitespace is invisible in Read output, so neither
// counts as a change when deciding whether a stored anchor still describes the
// line it was minted for.
func canonicalLine(line string) string {
	line = strings.ReplaceAll(line, "\r", "")
	return strings.TrimRightFunc(line, unicode.IsSpace)
}

// lineChecksum fingerprints one line for the anchor snapshot. It is not a
// security primitive: it only answers "does the line behind this anchor still
// read the same", where a collision would mean an edit lands on a line whose
// text an earlier Read showed.
func lineChecksum(line string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	line = canonicalLine(line)
	h := uint64(offset64)
	for i := 0; i < len(line); i++ {
		h ^= uint64(line[i])
		h *= prime64
	}
	return h
}

// anchorRow is one line Read is about to show. editable is false for a row the
// model cannot faithfully reproduce — a line cut short by the length cap — so
// it is printed with an anchor but never recorded as served. anchor is filled
// in when the row is one an edit just wrote and is being echoed back.
type anchorRow struct {
	lineNo   int
	text     string
	anchor   string
	editable bool
}
