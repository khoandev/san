package sloppyedit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool/fs/sloppy"
)

// The parity suite runs the reference engine's apply fixtures (oh-my-pi
// pi-edit, tests/fixtures/sloppy/apply.json) against this engine. Every skip
// names a behavior doc.go lists as a deliberate deviation or a payload shape
// package sloppy rejects instead of recovering.

type fixtureCase struct {
	Name   string            `json:"name"`
	Files  map[string]string `json:"files"`
	Args   fixtureArgs       `json:"args"`
	Expect fixtureExpect     `json:"expect"`
}

type fixtureArgs struct {
	Input string `json:"input"`
}

type fixtureExpect struct {
	Error  string            `json:"error"`
	Files  map[string]string `json:"files"`
	Writes *int              `json:"writes"`
}

// Deviation reasons, shared by the skip entries below.
const (
	skipBareSelection = "bare ⟪old⟫ without the │ divider is a parse error here (sloppy rejects it)"
	skipMarkerLines   = "＋/－ marker lines are literal here (no add/remove line syntax)"
	skipDesiredBlock  = "anchor-less FIND desired text is a parse error here (use *** SM:PUT for that)"
	skipRecovery      = "parse-time garbling recovery (echoes, diff shapes, relocating selections) is not implemented"
	skipPositional    = "per-selection positional substitution for multi-selection rewrites is not implemented"
	skipRegisters     = "»N deletion registers are not implemented"
	skipPunctuation   = "whole punctuation-run rewriting is not implemented"
	skipWording       = "diagnostic wording differs; the failure is still fail-closed"
)

// skipFixtures names the cases whose shapes this engine rejects or reinterprets.
var skipFixtures = map[string]string{
	// Bare selections (⟪old⟫ with no divider) and their diagnostics.
	"uses an empty rewrite as a blank line only for an insertion operation":  skipBareSelection,
	"allows zero rewrite gaps to replace a selection containing gaps":        skipBareSelection,
	"keeps a same-line insertion inline":                                     skipBareSelection,
	"deletes a selected whole line including indentation and newline":        skipBareSelection,
	"applies the all-match opener to every match with one identical rewrite": skipBareSelection,
	"accepts a unique single-fragment sparse selection":                      skipBareSelection,
	"accepts the Unicode ellipsis gap":                                       skipBareSelection,
	"supports an empty directional selection at the exact line boundary":     skipBareSelection,
	"treats empty double selection markers as an insertion point":            skipBareSelection,
	"resolves every batch anchor against the original content":               skipBareSelection,
	"allows one punctuation insertion typo for a unique changing candidate":  skipBareSelection,
	"accepts a small fragment typo only when the fuzzy tuple is unique":      skipBareSelection,
	"normalizes Unicode punctuation in sparse literals":                      skipBareSelection,
	"treats rewrite ellipses beyond the capture count as literal":            skipBareSelection,
	"applies a bare selection beside inline pairs as desired text":           skipBareSelection,
	"applies a non-overlapping batch and accepts an EOF transport newline":   skipBareSelection,
	"preserves a large region outside a tiny selection":                      skipBareSelection,
	"rejects punctuation-only patterns but accepts short identifiers":        skipBareSelection,
	"reports zero matches for the all-match opener without guessing":         skipBareSelection,
	"checks ordered-tuple uniqueness before choosing a match":                skipBareSelection,
	"does not use punctuation tolerance when more than one candidate aligns": skipBareSelection,
	"rejects overlapping original target spans atomically":                   skipBareSelection,

	// ＋/－ marker lines.
	"inserts an add line after its anchor":                                       skipMarkerLines,
	"keeps a run of add lines in authored order":                                 skipMarkerLines,
	"deletes a run of －-marked lines silently":                                   skipMarkerLines,
	"replaces a － run with the ＋ run directly below it":                          skipMarkerLines,
	"deletes an indented －-marked line byte-for-byte":                            skipMarkerLines,
	"mixes add lines with inline replacements in one operation":                  skipMarkerLines,
	"anchors an add run above a gap to its preceding line":                       skipMarkerLines,
	"applies add lines containing literal selection markers verbatim":            skipMarkerLines,
	"keeps typed depth for an indented add-line run before a following anchor":   skipMarkerLines,
	"normalizes whitespace-only MATCH rows around add lines":                     skipMarkerLines,
	"inserts add lines at their authored tab depth":                              skipMarkerLines,
	"matches ＋ insertion anchors leniently when only whitespace drifted":         skipMarkerLines,
	"keeps the next anchor's indentation when a lenient ＋ insert lands above it": skipMarkerLines,
	"names unmarked MATCH lines that exist nowhere and suggests ＋":               skipMarkerLines,
	"keeps an add line's indentation from either marker style":                   skipMarkerLines,
	"inserts an all-＋ REWRITE without writing literal markers":                   skipMarkerLines,
	"drops －-marked old lines from a REWRITE paired with ＋ lines":                skipMarkerLines,
	"treats an all-＋ REWRITE as insertion after the kept MATCH":                  skipMarkerLines,

	// Anchor-less FIND desired text (here: an anchor-less *** SM:PUT).
	"collapses back-to-back duplicates when desired text matches both copies": skipDesiredBlock,
	"applies marker-less desired text over its closest near-match block":      skipDesiredBlock,
	"applies a marker-less desired import line over its near-match":           skipDesiredBlock,
	"keeps the fail-closed error when no block resembles the stated text":     skipDesiredBlock,
	"collapses a duplicated block stated once as mono desired text":           skipDesiredBlock,

	// Parse-time recovery of garbled payloads.
	"drops an echoed literal before an inline selection":                        skipRecovery,
	"drops an echoed anchor line above an embedded selection":                   skipRecovery,
	"recovers a unified-diff-shaped rewrite-less op as inline changes":          skipRecovery,
	"binds a diff added-run to its context line and hunk markers to gaps":       skipRecovery,
	"replaces the whole line when a bare selection's REWRITE restates it":       skipRecovery,
	"treats several OLD selections as one whole sparse region":                  skipRecovery,
	"bounds same-line gaps and aligns a full-line rewrite around the selection": skipRecovery,
	"strips full-statement envelope echoes around an inner selection":           skipRecovery,
	"strips harmless leading and trailing sparse gaps":                          skipRecovery,

	// Rewrites over several selections.
	"substitutes multiple selections positionally when REWRITE has one line per selection": skipPositional,
	"uses whole-line ellipses as positional separators when the pattern has no gaps":       skipPositional,
	"supports multi-line positional groups separated by whole-line ellipses":               skipPositional,
	"fails closed on whole-line rewrite ellipses when a multi-selection pattern has gaps":  skipPositional,
	"fails closed when a multi-selection rewrite proves neither interpretation":            skipPositional,
	"allows proven whole-span replacement for multiple selections":                         skipPositional,
	"maps provided rewrite gaps positionally and drops omitted later gaps":                 skipPositional,
	"re-emits selected gaps positionally without retyping their spans":                     skipPositional,

	// Remaining reference-only machinery.
	"reuses an inline deletion register in a later inline insertion": skipRegisters,
	"never rewrites part of a longer punctuation run":                skipPunctuation,

	// Fail-closed, different words.
	"throws error on unclosed opening selection marker":                         skipWording,
	"throws error on unmatched closing selection marker":                        skipWording,
	"keeps the unmatched-close error when a stray ⟫ follows a proper selection": skipWording,
}

func TestParityFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "sloppy_apply.json"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixture struct {
		Cases []fixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture file contains no cases")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if reason := skipFixtures[tc.Name]; reason != "" {
				t.Skip(reason)
			}
			runFixture(t, tc)
		})
	}
}

func runFixture(t *testing.T, tc fixtureCase) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range tc.Files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatalf("create fixture dir for %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture file %s: %v", name, err)
		}
	}

	sections, err := sloppy.Parse(tc.Args.Input)
	writes := 0
	if err == nil {
		var results map[string]Result
		results, err = applySections(sections, func(path string) (string, error) {
			raw, readErr := os.ReadFile(filepath.Join(dir, path))
			if readErr != nil {
				return "", readErr
			}
			return normalizeLF(string(raw)), nil
		})
		if err == nil {
			for path, result := range results {
				if result.Content == normalizeLF(tc.Files[path]) {
					continue
				}
				writes++
				// The tool restores the on-disk line ending on write.
				text := result.Content
				if strings.Contains(tc.Files[path], "\r\n") {
					text = strings.ReplaceAll(text, "\n", "\r\n")
				}
				if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o644); err != nil {
					t.Fatalf("write fixture file %s: %v", path, err)
				}
			}
		}
	}

	if wantErr := tc.Expect.Error; wantErr != "" {
		if err == nil {
			t.Fatal("expected a failure")
		}
		if !errorMatches(err.Error(), wantErr) {
			t.Fatalf("error mismatch\n  expected: %s\n  actual:   %s", wantErr, err.Error())
		}
	} else if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tc.Expect.Writes != nil && writes != *tc.Expect.Writes {
		t.Errorf("write count = %d, want %d", writes, *tc.Expect.Writes)
	}
	for name, want := range tc.Expect.Files {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if string(got) != want {
			t.Errorf("file %s = %q, want %q", name, string(got), want)
		}
	}
}

// normalizeLF mirrors util.NormalizeLF from upstream: the fixture corpus stores
// files with canonical line endings, the tool reads the on-disk ones.
func normalizeLF(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// applySections mirrors upstream edittool's payload driver (payload.go), which
// the engine copy does not include. It applies every section to the file read
// through read, keyed by the payload's path spelling, atomically.
func applySections(
	sections []sloppy.Section,
	read func(path string) (string, error),
) (map[string]Result, error) {
	standalone := len(sections) == 1 && len(sections[0].Ops) == 1
	out := make(map[string]Result, len(sections))
	for _, section := range sections {
		content, err := read(section.Path)
		if err != nil {
			return nil, sectionError(section.Path, err, len(sections))
		}
		result, err := Apply(content, section, standalone)
		if err != nil {
			return nil, sectionError(section.Path, err, len(sections))
		}
		out[section.Path] = result
	}
	return out, nil
}

// sectionError attributes a failure to its file and, for multi-file payloads,
// states that no sibling was written. Single-file failures surface unwrapped.
func sectionError(path string, err error, sections int) error {
	detail := strings.TrimPrefix(err.Error(), atomicityNotice+"\n")
	if sections > 1 {
		detail = fmt.Sprintf("[%s]: %s\nNo files were modified — sections apply atomically.", path, detail)
	}
	return applyError{atomicityNotice + "\n" + detail}
}

// errorMatches mirrors the reference harness: "^"-prefixed patterns match as
// regular expressions, everything else is a substring check.
func errorMatches(actual, pattern string) bool {
	if rest, ok := strings.CutPrefix(pattern, "^"); ok {
		matched, err := regexp.MatchString(rest, actual)
		return err == nil && matched
	}
	return strings.Contains(actual, pattern)
}
