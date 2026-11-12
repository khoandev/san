package session

import (
	"fmt"
	"unicode/utf8"
)

// A tool result larger than this many bytes is kept out of the model's
// conversation and referenced instead: the full text stays in the session's
// blob directory, and only a bounded preview is sent. The threshold is a byte
// count rather than a token count because the token count is the vendor's, and
// it only ever under-reads: 100 KB is far more than any window needs inline.
const (
	ToolResultOverflowThreshold = 100_000
	ToolResultPreviewSize       = 10_000
)

// PageToolResult returns the bounded, model-facing form of a tool result too
// large for the conversation, or "" when full fits and the caller should keep
// it as it came. Both the main agent and a subagent worker page through here,
// so a worker's context is bounded the same way its parent's is.
//
// The preview cuts on a rune boundary so any vendor's tokenizer sees valid
// UTF-8. Persisting the whole content is what makes the page honest; when the
// write fails the note says the content was not kept, because a preview that
// silently dropped the rest would read to the model as the whole result.
func (s *LegacyStore) PageToolResult(sessionID, toolCallID, full string) string {
	if len(full) <= ToolResultOverflowThreshold {
		return ""
	}
	cutoff := min(ToolResultPreviewSize, len(full))
	for cutoff > 0 && !utf8.RuneStart(full[cutoff]) {
		cutoff--
	}
	preview := full[:cutoff]
	if s != nil && sessionID != "" {
		if err := s.PersistToolResult(sessionID, toolCallID, full); err == nil {
			return fmt.Sprintf("%s\n\n[Full output persisted to blobs/tool-result/%s/%s]", preview, sessionID, toolCallID)
		}
	}
	return fmt.Sprintf("%s\n\n[Output truncated from %d bytes — full content not persisted]", preview, len(full))
}
