// Package sloppyedit applies parsed sloppy edit operations to one file's text.
//
// It is the apply half of the engine ported from pulseaiclub/phi: package
// sloppy parses a payload into operations, and this package locates each
// operation's anchor in the file, decides what the operation rewrites, and
// splices the result. Nothing here touches the filesystem — a caller reads the
// file, calls Apply, and writes the returned content — so the permission
// preview and the approved execution can plan the same edit twice and land the
// same bytes.
//
// # Matching
//
// An anchor is located by a ladder: byte-exact, then whitespace- and
// typography-tolerant (indentation, spacing and “smart” punctuation fold to
// their ASCII spellings), then fuzzy (same operator run, at most a few
// character edits). A pattern must match exactly once unless its header ends
// with " all"; ambiguity is an error, never a guess. Failures name the fragment
// that missed, show the file region it nearly matched, and hand back a
// copy-ready payload to resend.
//
// # Atomicity
//
// Operations anchor against the original text, so an earlier operation never
// shifts a later anchor. All operations of a section are planned before any
// splice: one failure and Apply returns the original content untouched. A
// caller that writes only on success therefore gets all-or-nothing semantics
// per file.
//
// # Deviations from the reference engine
//
// These are inherited from the upstream port and preserved on purpose: a
// dialect of the format would cost a failed round-trip per novel mistake.
//
//   - A "*** SM:PUT" replaces the whole FIND match even when the pattern
//     carries ⟪old│new⟫ selections; selections rewrite in place only when there
//     is no action header. Per-selection positional substitution is not
//     implemented.
//   - Parse-time recovery of garbled payloads (bare selections without the │
//     divider, unified-diff rewrites, ＋/－ marker lines) is rejected instead of
//     reinterpreted; package sloppy errors say so.
//   - A no-op is reported once per call; the reference escalates after three
//     identical no-ops per file and session.
package sloppyedit
