package conv

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// plainTailReference is the composition RenderPlainTail replaced, kept here so
// the fast path is held to the bytes it must produce.
func plainTailReference(content, gutter string, width int) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, gutter, lipgloss.NewStyle().Width(width).Render(content))
}

// testTailGutter is the gutter the live tail is drawn with: the turn's first
// content carries the marker, later blocks a blank continuation.
func testTailGutter() string { return contentGutter(true) }

func TestRenderPlainTailMatchesPlainComposition(t *testing.T) {
	tails := []string{
		"one line",
		"a\nb\nc",
		"a\n\nb",
		"trailing newline\n",
		"double trailing\n\n",
		strings.Repeat("word ", 60),
		"short\n" + strings.Repeat("word ", 40) + "\nshort",
		"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```",
		"日本語のテキストがここにあります。とても長い行です。" + strings.Repeat("これは折り返しのテストです。", 6),
		"supercalifragilisticexpialidocious" + strings.Repeat("x", 140),
		"   leading spaces\n\ttab\there",
		"trailing spaces   \n   ",
		"line with \x1b[31mred\x1b[0m color",
		strings.Repeat("  ", 3) + "\n\n\n" + strings.Repeat("tail ", 50),
	}
	gutters := []string{testTailGutter(), continuationGutter}
	for _, width := range []int{40, 61, 96} {
		for _, gutter := range gutters {
			for _, tail := range tails {
				md := NewMDRenderer(width + streamWrapReserve)
				got, ok := md.RenderPlainTail(tail, gutter, width)
				if !ok {
					t.Fatalf("width %d: tail %q refused the fast path", width, tail)
				}
				if want := plainTailReference(tail, gutter, width); got != want {
					t.Fatalf("width %d gutter %q: tail %q\n got %q\nwant %q", width, gutter, tail, got, want)
				}
			}
		}
	}
}

// TestRenderPlainTailGrowsWithTheStream feeds the same tail one rune at a time,
// the shape a streaming reply has, and checks every intermediate frame against
// the plain composition. Reuse may never change the answer.
func TestRenderPlainTailGrowsWithTheStream(t *testing.T) {
	tails := []string{
		strings.Repeat("A paragraph of the answer that keeps arriving, with `code` in it.\n\n", 6) +
			"and the final paragraph that never ends until the model stops",
		"```go\n" + strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) error {\n", 12) + "```",
		"日本語の長い段落が少しずつ届きます。" + strings.Repeat("折り返しも試します。", 8),
	}
	for _, tail := range tails {
		width := 72
		gutter := testTailGutter()
		md := NewMDRenderer(width + streamWrapReserve)
		runes := []rune(tail)
		for i := 1; i <= len(runes); i += 3 {
			chunk := string(runes[:i])
			got, ok := md.RenderPlainTail(chunk, gutter, width)
			if !ok {
				t.Fatalf("frame %d refused the fast path", i)
			}
			if want := plainTailReference(chunk, gutter, width); got != want {
				t.Fatalf("frame %d of %q\n got %q\nwant %q", i, tail, got, want)
			}
		}
	}
}

// TestRenderPlainTailRewrapsOnlyTheGrownLine pins the point of the cache: a tail
// that grew by one word must not be wrapped and measured again.
func TestRenderPlainTailRewrapsOnlyTheGrownLine(t *testing.T) {
	width := 72
	gutter := testTailGutter()
	// One appended word costs the same on a tail of 200 settled lines as on a
	// tail of 3: only the line the chunk landed on is touched.
	growCost := func(lines int) float64 {
		md := NewMDRenderer(width + streamWrapReserve)
		body := strings.Repeat("a line of the answer that has already settled\n", lines)
		if _, ok := md.RenderPlainTail(body, gutter, width); !ok {
			t.Fatal("fast path refused")
		}
		return testing.AllocsPerRun(20, func() {
			md.tail.joinedOK = false // the re-join is the frame's own cost, not the cache's
			if _, ok := md.RenderPlainTail(body+"more", gutter, width); !ok {
				t.Fatal("fast path refused")
			}
		})
	}
	short, long := growCost(3), growCost(200)
	if long > short+3 {
		t.Fatalf("growing a 200-line tail costs %.1f allocs against %.1f for a 3-line tail: settled rows are wrapped again", long, short)
	}

	// And the same tail twice is served without joining its rows again.
	md := NewMDRenderer(width + streamWrapReserve)
	body := strings.Repeat("a line of the answer that has already settled\n", 200)
	if _, ok := md.RenderPlainTail(body, gutter, width); !ok {
		t.Fatal("fast path refused")
	}
	repeat := testing.AllocsPerRun(20, func() {
		if _, ok := md.RenderPlainTail(body, gutter, width); !ok {
			t.Fatal("fast path refused")
		}
	})
	if repeat > 0 {
		t.Fatalf("an unchanged tail costs %.1f allocs, want the memoized block", repeat)
	}
}

func BenchmarkRenderPlainTailGrow(b *testing.B) {
	width := 72
	gutter := testTailGutter()
	md := NewMDRenderer(width + streamWrapReserve)
	body := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) error {\n", 400)
	md.RenderPlainTail(body, gutter, width)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		md.RenderPlainTail(body+"one more token", gutter, width)
	}
}

// BenchmarkPlainTailWhole is the reference composition the fast path replaced,
// at the same size, so the two numbers mean the same thing.
func BenchmarkPlainTailWhole(b *testing.B) {
	width := 72
	gutter := testTailGutter()
	body := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) error {\n", 400)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		plainTailReference(body+"one more token", gutter, width)
	}
}
