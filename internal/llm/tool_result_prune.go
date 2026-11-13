package llm

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// A stale tool result is one the request has moved past: the turn reasoned
// over it several steps ago, and its text is paid for again on every inference
// that follows. The newest results travel whole, because they are the working
// set the model is still reading; anything older travels as a bounded preview
// plus one line saying how much was elided and how to get it back.
//
// The trim happens on the request copy, never on the conversation: the session
// keeps every result verbatim, so scrolling back, resuming, and compaction all
// still read what the tool actually said.
const (
	// RecentToolResults is how many of the newest results may travel verbatim.
	RecentToolResults = 8
	// recentWindowBytes bounds what that window may occupy. A count alone
	// cannot bound a prompt when one result is 200 KB: eight reads of a long
	// file are half a megabyte, so the working set is budgeted in bytes too.
	recentWindowBytes = 64_000
	// staleResultFloor is the size below which a stale result is left alone: a
	// preview would save little and might cut the one line that mattered.
	staleResultFloor = 2000
	// stalePreviewBytes is how much of a stale result's head is kept.
	stalePreviewBytes = 600
)

// staleNote closes every preview. It states the elision, not merely truncation,
// so the model reads it as "ask again" rather than "that was all".
const staleNote = "[... %s elided: this result is older than the last %d tool results, which travel in full. Run the tool again if you need it.]"

// PrepareRequest is what one inference sends: the conversation adapted to what
// the model can read and to what it still needs to see.
func (l *Client) PrepareRequest(messages []core.Message) []core.Message {
	return l.RequestMessages(PruneStaleToolResults(messages))
}

// PruneStaleToolResults replaces every tool result outside the recent window
// with a preview of its head. The window is the newest RecentToolResults,
// counted from the end while they fit in recentWindowBytes; the newest result
// travels whole however big it is, because it is the one the next step reads.
//
// Position and size decide the window; content never does. A result that was
// pruned once stays pruned, so the request prefix is stable from one turn to
// the next — which is what a prompt cache keys on.
//
// The input is never modified. When nothing is stale the same slice comes back
// and nothing is allocated.
func PruneStaleToolResults(messages []core.Message) []core.Message {
	stale := staleResults(messages)
	if stale == 0 {
		return messages
	}

	out := make([]core.Message, len(messages))
	copy(out, messages)
	index := 0
	for i := range out {
		var content ai.Content
		for j, block := range out[i].Content {
			if block.Type != ai.BlockToolResult || block.ToolResult == nil {
				continue
			}
			index++
			if index > stale || !worthPruning(*block.ToolResult) {
				continue
			}
			if content == nil {
				content = make(ai.Content, len(out[i].Content))
				copy(content, out[i].Content)
				out[i].Content = content
			}
			result := *block.ToolResult
			result.Content = ai.TextContent(stalePreview(result.Content))
			content[j].ToolResult = &result
		}
	}
	return out
}

// staleResults is how many of the earliest results fall outside the recent
// window. It walks the conversation backwards so the window is measured from
// the newest result, the one a turn is reasoning over right now.
func staleResults(messages []core.Message) int {
	total := 0
	for _, m := range messages {
		for _, block := range m.Content {
			if block.Type == ai.BlockToolResult && block.ToolResult != nil {
				total++
			}
		}
	}
	kept, size := 0, 0
	for i := len(messages) - 1; i >= 0; i-- {
		content := messages[i].Content
		for j := len(content) - 1; j >= 0; j-- {
			block := content[j]
			if block.Type != ai.BlockToolResult || block.ToolResult == nil {
				continue
			}
			bytes := resultBytes(block.ToolResult.Content)
			// The newest result is kept whatever it weighs: the model has not
			// read it yet, and a preview would be the whole of what it sees.
			if kept == RecentToolResults || (kept > 0 && size+bytes > recentWindowBytes) {
				return total - kept
			}
			kept++
			size += bytes
		}
	}
	return total - kept
}

// resultBytes is what a result weighs in the request: its text, plus the
// pictures it carries, which are paid for whatever they read as.
func resultBytes(content ai.Content) int {
	size := 0
	for _, block := range content {
		switch block.Type {
		case ai.BlockText:
			size += len(block.Text)
		case ai.BlockImage:
			if block.Image != nil {
				size += len(block.Image.Data)
			}
		}
	}
	return size
}

// worthPruning reports whether the result is big enough that a preview is a
// real saving. An image counts whatever its text: the picture is what costs.
func worthPruning(result ai.ToolResult) bool {
	text, images := measure(result.Content)
	return images > 0 || text > staleResultFloor
}

// measure is the size of a result without joining it: building the text just
// to learn how long it is would allocate the very copy the trim exists to
// avoid paying for.
func measure(content ai.Content) (text, images int) {
	for _, block := range content {
		switch block.Type {
		case ai.BlockText:
			text += len(block.Text)
		case ai.BlockImage:
			images++
		}
	}
	return text, images
}

// stalePreview keeps the head of the result — a file's first lines, a command's
// opening output — and names how much it dropped. The head is cut on a rune
// boundary so any vendor's tokenizer reads valid UTF-8.
func stalePreview(content ai.Content) string {
	size, images := measure(content)
	var buf strings.Builder
	for _, block := range content {
		if block.Type == ai.BlockText && buf.Len() < stalePreviewBytes {
			buf.WriteString(block.Text)
		}
	}
	head := buf.String()
	if len(head) > stalePreviewBytes {
		cut := stalePreviewBytes
		for cut > 0 && !utf8.RuneStart(head[cut]) {
			cut--
		}
		head = head[:cut]
	}
	elided := fmt.Sprintf("%d bytes", size-len(head))
	switch {
	case images == 1 && elided == "0 bytes":
		elided = "1 image"
	case images == 1:
		elided = fmt.Sprintf("%s and 1 image", elided)
	case images > 1 && elided == "0 bytes":
		elided = fmt.Sprintf("%d images", images)
	case images > 1:
		elided = fmt.Sprintf("%s and %d images", elided, images)
	}
	note := fmt.Sprintf(staleNote, elided, RecentToolResults)
	if head == "" {
		return note
	}
	return head + "\n\n" + note
}
