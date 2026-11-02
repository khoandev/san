package conv

import (
	"encoding/json"
	"strings"

	"github.com/genai-io/san/internal/tool/fs/sloppy"
	"github.com/genai-io/san/internal/tool/perm"
)

// Edit takes one anchored payload that names its own file (package fs), so the
// view layer reads the payload to show what a call asked for. Nothing here
// decides what is written: the tool and the permission preview own that.

// payloadArgs returns a call's arguments, so every view reads the payload
// through perm.PayloadText and agrees on its spelling.
func payloadArgs(input string) map[string]any {
	var args map[string]any
	if json.Unmarshal([]byte(input), &args) != nil {
		return nil
	}
	return args
}

// payloadOps returns the operations an Edit payload carries, or nil when the
// call carries no payload or it does not parse: a view of a malformed payload
// would be a guess, and the diagnostic already quotes the offending line.
func payloadOps(input string) []sloppy.Operation {
	payload := perm.PayloadText(payloadArgs(input))
	if payload == "" {
		return nil
	}
	sections, err := sloppy.Parse(payload)
	if err != nil {
		return nil
	}
	var ops []sloppy.Operation
	for _, section := range sections {
		ops = append(ops, section.Ops...)
	}
	return ops
}

// payloadQuotedText returns the current file text a payload anchors on, which
// is where it quotes a line it removes.
func payloadQuotedText(input string) string {
	var quoted []string
	for _, op := range payloadOps(input) {
		quoted = append(quoted, patternText(op.Pattern, false))
	}
	return strings.Join(quoted, "\n")
}

// payloadPreviewPair renders a payload as the text it quotes and the text it
// asks for: the pair a failed edit is compared against.
func payloadPreviewPair(input string) (old, new string, ok bool) {
	ops := payloadOps(input)
	if len(ops) == 0 {
		return "", "", false
	}
	var quoted, wanted []string
	for _, op := range ops {
		body := patternText(op.Pattern, false)
		quoted = append(quoted, body)
		switch op.Rewrite.Kind {
		case sloppy.RewriteInsert:
			wanted = append(wanted, body+"\n"+op.Rewrite.Text)
		case sloppy.RewriteInline:
			wanted = append(wanted, patternText(op.Pattern, true))
		default:
			wanted = append(wanted, op.Rewrite.Text)
		}
	}
	return strings.Join(quoted, "\n"), strings.Join(wanted, "\n"), true
}

// patternText renders a FIND body for display: "⟪old│new⟫" directives show
// their current text, or the text they rewrite it to.
func patternText(pattern sloppy.Pattern, rewritten bool) string {
	var sb strings.Builder
	last := 0
	for _, selection := range pattern.Selections {
		if selection.Start < last || selection.End > len(pattern.Body) {
			continue
		}
		sb.WriteString(pattern.Body[last:selection.Start])
		if rewritten {
			sb.WriteString(selection.New)
		} else {
			sb.WriteString(selection.Old)
		}
		last = selection.End
	}
	sb.WriteString(pattern.Body[last:])
	return sb.String()
}
