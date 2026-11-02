package fs

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// The anchored shape of Edit: replace the lines spanned by two anchors with the
// given text. It exists because the two ways exact-string editing fails are
// both transcription problems — a model retyping a line it already saw —
// whereas an anchor is copied as five letters and a line number can never be
// off by one, because the number is not what the edit names.
//
// The payload mirrors the one the hashline extension family uses so a model
// that has seen that schema can call this tool without relearning it:
//
//	{file_path, remove_from: "abcde", remove_to: "abcfg", text: "new line\n…"}
//
// text is a string whose line breaks separate lines, or an array of strings,
// one per line; an empty text deletes the range. A single anchor in both
// remove_from and remove_to replaces one line.
type anchorEdit struct {
	removeFrom string
	removeTo   string
	lines      []string
	warnings   []string
}

// anchoredEditResult is an anchored edit applied to a file's text.
type anchoredEditResult struct {
	content  string
	start    int // 1-based first replaced line
	end      int // 1-based last replaced line
	added    int // lines written in place of the range
	warnings []string
}

// parseAnchorEdit validates the anchored payload. Shape errors are reported
// before any file is touched, with the field name that is wrong.
func parseAnchorEdit(params map[string]any) (anchorEdit, error) {
	from := tool.GetString(params, "remove_from")
	to := tool.GetString(params, "remove_to")
	if from == "" || to == "" {
		return anchorEdit{}, &tool.ToolError{Message: `[E_BAD_SHAPE] the anchored edit needs "remove_from" and "remove_to": the 5-letter anchors of the first and last line to replace (use the same anchor twice for one line)`}
	}

	rawText, ok := params["text"]
	if !ok {
		return anchorEdit{}, &tool.ToolError{Message: `[E_BAD_SHAPE] the anchored edit needs "text": the exact replacement text (use "" to delete the range)`}
	}
	lines, err := payloadLines(rawText)
	if err != nil {
		return anchorEdit{}, err
	}

	edit := anchorEdit{lines: lines}
	var warning string
	if edit.removeFrom, warning, err = normalizeAnchorRef(from, "remove_from"); err != nil {
		return anchorEdit{}, err
	}
	if warning != "" {
		edit.warnings = append(edit.warnings, warning)
	}
	if edit.removeTo, warning, err = normalizeAnchorRef(to, "remove_to"); err != nil {
		return anchorEdit{}, err
	}
	if warning != "" {
		edit.warnings = append(edit.warnings, warning)
	}
	for _, line := range edit.lines {
		if strings.ContainsRune(line, 0) {
			return anchorEdit{}, &tool.ToolError{Message: "[E_BAD_SHAPE] the replacement text contains a NUL byte; a text file cannot hold one. Remove it and retry"}
		}
	}
	return edit, nil
}

// payloadLines turns the edit's text into replacement lines. A trailing line
// break sets the newline of the last line rather than adding a blank line, so
// a model that writes "a\nb\n" gets two lines and not three — the convention
// every editor output follows.
func payloadLines(raw any) ([]string, error) {
	switch value := raw.(type) {
	case string:
		if value == "" {
			return nil, nil
		}
		lines := strings.Split(value, "\n")
		if strings.HasSuffix(value, "\n") {
			lines = lines[:len(lines)-1]
		}
		return lines, nil
	case []any:
		var lines []string
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, &tool.ToolError{Message: `[E_BAD_SHAPE] "text" must be a string or an array of strings, one entry per line`}
			}
			lines = append(lines, strings.Split(text, "\n")...)
		}
		return lines, nil
	default:
		return nil, &tool.ToolError{Message: `[E_BAD_SHAPE] "text" must be a string holding the exact replacement text, or an array of strings with one entry per line`}
	}
}

// normalizeAnchorRef reduces what the model put in remove_from/remove_to to a
// bare anchor. The shapes it accepts are the ways a real call drifts: a whole
// Read row pasted back, a diff line's "+" or "-" prefix kept, the anchor
// wrapped in quotes, an annotated "(line 12)" left on, or uppercase letters.
// Each repair is echoed as a warning so the model learns the expected shape.
//
// The scan is hand-rolled rather than regexp-based: this runs on every anchored
// edit, twice per anchor (preview and execution), and three regexp matches per
// anchor dominated the edit's CPU profile.
func normalizeAnchorRef(raw, field string) (anchor string, warning string, err error) {
	ref := strings.TrimSpace(raw)
	if len(ref) >= 2 && (ref[0] == '"' || ref[0] == '\'') && ref[len(ref)-1] == ref[0] {
		ref = strings.TrimSpace(ref[1 : len(ref)-1])
	}

	if cut := strings.Index(ref, toolresult.AnchorSep); cut >= 0 {
		row := strings.TrimSpace(ref[:cut])
		switch {
		case strings.HasPrefix(row, "+"), strings.HasPrefix(row, "-"):
			marker := row[:1]
			ref = strings.TrimSpace(row[1:])
			if marker == "+" {
				warning = fmt.Sprintf("[W_BAD_REF] stripped the diff \"+\" marker from %s", field)
			} else {
				warning = fmt.Sprintf("[W_BAD_REF] stripped the leading \"-\" marker from %s", field)
			}
		case strings.ContainsAny(row, " \t"):
			ref = row[strings.LastIndexAny(row, " \t")+1:]
			warning = fmt.Sprintf("[W_BAD_REF] stripped the line number and content from %s — pass the anchor alone", field)
		default:
			ref = row
			warning = fmt.Sprintf("[W_BAD_REF] stripped the anchor row's content from %s — pass the anchor alone", field)
		}
	} else if cut := strings.IndexByte(ref, '('); cut > 0 {
		if suffix := strings.TrimSpace(ref[cut:]); strings.HasPrefix(suffix, "(line") {
			ref = strings.TrimSpace(ref[:cut])
			warning = fmt.Sprintf("[W_BAD_REF] stripped the \"(line N)\" suffix from %s", field)
		}
	}

	lower := strings.ToLower(ref)
	if validAnchor(lower) {
		if lower != ref {
			warning = fmt.Sprintf("[W_ANCHOR_CASE] %s %q was lowercased to %q", field, ref, lower)
		}
		return lower, warning, nil
	}
	return "", "", &tool.ToolError{Message: anchorRefMessage(ref, field)}
}

// anchorRefMessage explains the specific mistake in a rejected anchor rather
// than restating the format: the recovery differs by shape.
func anchorRefMessage(ref, field string) string {
	switch {
	case ref == "":
		return fmt.Sprintf("[E_BAD_REF] %s is empty. Pass the 5 letters before \"│\" in Read output", field)
	case strings.Contains(ref, "│"):
		return fmt.Sprintf("[E_BAD_REF] %s %q is a whole row. Pass only its 5-letter anchor — drop the line number, \"│\", and the line text", field, clipText(ref))
	case strings.Contains(ref, "\n"):
		return fmt.Sprintf("[E_BAD_REF] %s spans %d lines. It takes one anchor: the first anchor in remove_from, the last in remove_to", field, strings.Count(ref, "\n")+1)
	case ref[0] >= '0' && ref[0] <= '9':
		return fmt.Sprintf("[E_BAD_REF] %s %q starts with a line number. Pass the anchor alone, not the line it points at", field, clipText(ref))
	default:
		return fmt.Sprintf("[E_BAD_REF] %s %q is not an anchor. Anchors are exactly 5 lowercase letters copied from Read output", field, clipText(ref))
	}
}

func clipText(s string) string {
	const max = 48
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// applyAnchorEdit resolves both anchors against the file state the model saw
// and returns the edited text. Nothing is written here, so the same call serves
// the permission preview and the approved execution.
func applyAnchorEdit(filePath string, info os.FileInfo, content string, edit anchorEdit) (anchoredEditResult, error) {
	lines := splitFileLines(content)
	var warnings []string

	// One lock hold resolves both anchors and validates the range: an edit is
	// on the hot path of every change the agent makes.
	resolved, current := resolveAnchoredRange(filePath, info, lines, edit.removeFrom, edit.removeTo, len(edit.lines) == 0)
	if !current {
		return anchoredEditResult{}, staleAnchorError(filePath, edit.removeFrom)
	}
	if resolved.missing != "" {
		return anchoredEditResult{}, staleAnchorError(filePath, resolved.missing)
	}
	start, end := resolved.start, resolved.end
	if resolved.reversed {
		warnings = append(warnings, fmt.Sprintf("[H_RANGE_REVERSED] remove_from came after remove_to; lines %d-%d were replaced in document order", start, end))
	}

	unchanged := len(edit.lines) == end-start+1
	if unchanged {
		for i, line := range edit.lines {
			if lines[start-1+i] != line {
				unchanged = false
				break
			}
		}
	}
	if unchanged {
		return anchoredEditResult{}, &tool.ToolError{Message: fmt.Sprintf("the replacement is identical to lines %d-%d of %s; nothing to change", start, end, filePath)}
	}

	// Read cuts a line at maxLineLen, so a range holding one was never shown in
	// full. Replacing it by anchor would overwrite text the model never saw;
	// the honest answer is to refuse and name the way out.
	for line := start; line <= end; line++ {
		if utf8.RuneCountInString(lines[line-1]) > maxLineLen {
			return anchoredEditResult{}, &tool.ToolError{Message: fmt.Sprintf(
				"[E_LINE_TOO_LONG] line %d of %s is longer than Read's %d-character cap, so its content was never shown in full. Edit cannot replace a line it cannot show; split the line first, or replace the whole file with Write",
				line, filePath, maxLineLen)}
		}
	}

	if len(resolved.stale) > 0 {
		return anchoredEditResult{}, rangeStaleError(filePath, info, lines, start, end, resolved.stale)
	}

	out := make([]string, 0, len(lines)-(end-start+1)+len(edit.lines))
	out = append(out, lines[:start-1]...)
	out = append(out, edit.lines...)
	out = append(out, lines[end:]...)

	edited := strings.Join(out, "\n")
	// A file that ended with a newline keeps ending with one; a file that did
	// not (a hand-written config, a Makefile fragment) is not given one.
	if edited != "" && strings.HasSuffix(content, "\n") {
		edited += "\n"
	}
	return anchoredEditResult{
		content:  edited,
		start:    start,
		end:      end,
		added:    len(edit.lines),
		warnings: warnings,
	}, nil
}

// staleAnchorError names the anchor that no longer resolves and the one action
// that fixes it. It covers both causes — the file changed on disk, or the
// anchor belongs to another file — because they need the same response.
func staleAnchorError(filePath, anchor string) error {
	return &tool.ToolError{Message: fmt.Sprintf(
		"[E_STALE_ANCHOR] anchor %q is stale for %s: the file changed since that anchor was served, or the anchor belongs to another file. Read %s again for fresh anchors",
		anchor, filePath, filePath)}
}

// rangeStaleError shows the range as it is now, with anchors, so the retry can
// proceed without a Read: the model asked to replace lines it never saw (or
// that changed under it), and the rows are exactly what it was missing.
func rangeStaleError(filePath string, info os.FileInfo, lines []string, start, end int, stale []int) error {
	anchors := serveRange(filePath, info, lines, start, end)
	const maxRows = 40
	var sb strings.Builder
	fmt.Fprintf(&sb, "[E_RANGE_STALE] %d of the %d line(s) in %d-%d of %s were never shown or changed after they were shown. The range now reads:\n",
		len(stale), end-start+1, start, end, filePath)
	for i := 0; i < len(anchors) && i < maxRows; i++ {
		fmt.Fprintf(&sb, toolresult.AnchorLineFormat, start+i, anchors[i], lines[start-1+i])
	}
	if len(anchors) > maxRows {
		fmt.Fprintf(&sb, "... (%d more lines; Read the file for the rest)\n", len(anchors)-maxRows)
	}
	sb.WriteString("Retry with the anchors above — this error already served them, so no Read is needed.")
	return &tool.ToolError{Message: sb.String()}
}

// splitFileLines splits text into the lines Read numbers: one entry per
// newline-terminated line plus a final unterminated one, and no entry for the
// empty remainder after a trailing newline. Keeping this identical to Read's
// numbering is what makes an anchor's line number meaningful.
func splitFileLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
