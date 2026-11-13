package conv

import (
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// plainTail memoizes the wrap of the live streaming tail. Model text arrives
// token by token, so every frame re-renders the whole block still being written
// while all but its last source line sits unchanged — and both lipgloss's wrap
// and its width measurement walk every row, so a frame costs as much as the
// block is long. Rows are therefore cached per source line: a new chunk rewraps
// only the line it lands on and reuses every finished row, whose display width
// was measured once when it was produced. That width is exactly what the gutter
// join needs, and re-measuring it per frame is what made a long tail expensive.
type plainTail struct {
	width    int      // wrap width the cache was built for
	src      string   // content the cached lines were wrapped from
	ends     []int    // byte offset in src just past each cached source line
	lineRows []int    // rows each cached source line produced
	rows     []string // wrapped rows, in order
	rowWidth []int    // display width of each cached row

	// joined memoizes the finished block: a spinner tick repaints an unchanged
	// tail, and re-joining thousands of rows for it is pure waste.
	joined       string
	joinedGutter string
	joinedOK     bool
}

// reset drops the cache, keeping the width it now applies to.
func (t *plainTail) reset(width int) { *t = plainTail{width: width} }

// reusable counts the cached source lines content still begins with. A line is
// reusable only when content matches src up to and including its trailing
// newline: the line the next chunk lands on keeps growing, and a line without a
// newline yet may still grow.
func (t *plainTail) reusable(content string) int {
	if t.src == "" {
		return 0
	}
	common := len(content)
	switch {
	case len(content) >= len(t.src) && content[:len(t.src)] == t.src:
		// The usual case: the tail only grew.
		common = len(t.src)
	default:
		i := 0
		for i < len(content) && i < len(t.src) && content[i] == t.src[i] {
			i++
		}
		common = i
	}
	reuse := 0
	for reuse < len(t.ends) {
		end := t.ends[reuse]
		start := 0
		if reuse > 0 {
			start = t.ends[reuse-1]
		}
		// Only a line that ended with a newline is closed: any later chunk
		// extends the line it lands on, so the empty line a trailing newline
		// opens is never reusable either.
		if end > common || end <= start || t.src[end-1] != '\n' {
			break
		}
		reuse++
	}
	return reuse
}

// RenderPlainTail renders the plain streaming tail: markdown-free model text,
// wrapped to width and led by gutter. The result is byte-identical to
// lipgloss.JoinHorizontal(lipgloss.Top, gutter, lipgloss.NewStyle().Width(width).Render(content))
// — which is what the tail rendered before — but rows that have not changed
// since the last frame are reused instead of wrapped and measured again. ok is
// false when the tail needs that plain composition instead (a carriage return,
// whose normalization spans lines, or an unusable width).
//
// The unit of reuse is a source line, because a line break is the only place a
// wrap is known to restart at column zero; a reply typed as one unbroken
// paragraph has no interior restart point, so it is rewrapped whole on every
// frame. Everything with line structure — prose, lists, code — pays only for
// the line the new chunk landed on.
func (r *MDRenderer) RenderPlainTail(content, gutter string, width int) (string, bool) {
	if width <= 0 || strings.IndexByte(content, '\r') >= 0 {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t := &r.tail
	if t.width != width {
		t.reset(width)
	}
	if t.joinedOK && t.joinedGutter == gutter && t.src == content {
		return t.joined, true
	}

	reuse := t.reusable(content)
	kept := 0
	for _, rows := range t.lineRows[:reuse] {
		kept += rows
	}
	t.ends = t.ends[:reuse]
	t.lineRows = t.lineRows[:reuse]
	t.rows = t.rows[:kept]
	t.rowWidth = t.rowWidth[:kept]
	offset := 0
	if reuse > 0 {
		offset = t.ends[reuse-1]
	}

	// One style for the whole pass: NewStyle allocates, and the wrap of a single
	// source line is independent of its neighbours (lipgloss wraps line by line).
	style := lipgloss.NewStyle().Width(width)
	for rest := content[offset:]; ; {
		line, after, hasNewline := strings.Cut(rest, "\n")
		added := 0
		for row := range strings.SplitSeq(style.Render(line), "\n") {
			t.rows = append(t.rows, row)
			t.rowWidth = append(t.rowWidth, xansi.StringWidth(row))
			added++
		}
		t.lineRows = append(t.lineRows, added)
		offset += len(line)
		if hasNewline {
			offset++ // the newline belongs to the line it ends
		}
		t.ends = append(t.ends, offset)
		if !hasNewline {
			break
		}
		rest = after
	}

	// JoinHorizontal pads every row of the body to the widest one, so the
	// widest row is part of the answer. Integer comparisons, not measurements.
	widest := 0
	for _, w := range t.rowWidth {
		widest = max(widest, w)
	}
	continuation := strings.Repeat(" ", xansi.StringWidth(gutter))
	var sb strings.Builder
	sb.Grow(len(t.rows) * (widest + len(continuation) + 1))
	for i, row := range t.rows {
		if i > 0 {
			sb.WriteByte('\n')
			sb.WriteString(continuation)
		} else {
			sb.WriteString(gutter)
		}
		sb.WriteString(row)
		for j := t.rowWidth[i]; j < widest; j++ {
			sb.WriteByte(' ')
		}
	}
	t.src = content
	t.joined, t.joinedGutter, t.joinedOK = sb.String(), gutter, true
	return t.joined, true
}
