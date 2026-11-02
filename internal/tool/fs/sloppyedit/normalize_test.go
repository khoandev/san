package sloppyedit

import "testing"

func TestNormalizeTextDropsWhitespaceAndFoldsPunctuation(t *testing.T) {
	got := normalizeText("\tconst  x = “a” ≠ b;\n")
	if want := `constx="a"!=b;`; got.text != want {
		t.Errorf("normalized text = %q, want %q", got.text, want)
	}
}

func TestNormalizeTextMapsNormalizedBytesBackToSource(t *testing.T) {
	source := "a\u00a0😀 b"
	got := normalizeText(source)

	if want := "a😀b"; got.text != want {
		t.Fatalf("normalized text = %q, want %q", got.text, want)
	}
	if start := got.sourceStart(0, -1); start != 0 {
		t.Errorf("sourceStart(0) = %d, want 0", start)
	}
	if end := got.sourceEnd(1, -1); end != 1 {
		t.Errorf("sourceEnd(1) = %d, want 1", end)
	}
	// The emoji occupies 4 source bytes but folds to itself byte-for-byte.
	emojiStart := got.sourceStart(1, -1)
	if emojiStart != 3 {
		t.Errorf("sourceStart(1) = %d, want 3", emojiStart)
	}
	if end := got.sourceEnd(5, -1); end != 7 {
		t.Errorf("sourceEnd(5) = %d, want 7", end)
	}
	if want := "😀"; source[emojiStart:got.sourceEnd(5, -1)] != want {
		t.Errorf("source slice = %q, want %q", source[emojiStart:got.sourceEnd(5, -1)], want)
	}
	if start := got.sourceStart(5, -1); start != 8 {
		t.Errorf("sourceStart(5) = %d, want 8", start)
	}
}

func TestNormalizeTextKeepsNonASCIICharactersVerbatim(t *testing.T) {
	got := normalizeText("中文 = 1")
	if want := "中文=1"; got.text != want {
		t.Errorf("normalized text = %q, want %q", got.text, want)
	}
	if len(got.starts) != len(got.text) {
		t.Fatalf("starts has %d entries, want %d (one per normalized byte)", len(got.starts), len(got.text))
	}
	for i, start := range got.starts {
		if start >= got.ends[i] {
			t.Errorf("starts[%d] = %d, want < ends[%d] = %d", i, start, i, got.ends[i])
		}
	}
}

func TestSourceOffsetsFallBackAtEnds(t *testing.T) {
	got := normalizeText("ab")
	if start := got.sourceStart(len(got.text), 42); start != 42 {
		t.Errorf("sourceStart past the end = %d, want fallback 42", start)
	}
	if end := got.sourceEnd(0, 42); end != 0 {
		t.Errorf("sourceEnd(0) = %d, want 0", end)
	}
	if end := got.sourceEnd(len(got.text), 42); end != 2 {
		t.Errorf("sourceEnd past the end = %d, want 2", end)
	}
}

func TestLevenshteinDistance(t *testing.T) {
	cases := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{name: "identical", left: "kitten", right: "kitten", want: 0},
		{name: "classic", left: "kitten", right: "sitting", want: 3},
		{name: "empty against text", left: "", right: "abc", want: 3},
		{name: "astral counts once", left: "😀a", right: "😀b", want: 1},
		{name: "shared edges trimmed", left: "prefix-middle-suffix", right: "prefix-middl-suffix", want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := levenshteinDistance(tc.left, tc.right); got != tc.want {
				t.Errorf("levenshteinDistance(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
			}
			if got := levenshteinDistance(tc.right, tc.left); got != tc.want {
				t.Errorf("levenshteinDistance(%q, %q) = %d, want %d", tc.right, tc.left, got, tc.want)
			}
		})
	}
}
