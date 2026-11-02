package fs

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/fs/sloppy"
	"github.com/genai-io/san/internal/tool/fs/sloppyedit"
)

// Edit's payload parser and apply stage.
//
// The format is the "sloppy" anchored patch: a file section
// ("*** SM:EDIT path") carrying operations, each anchored by a FIND body and
// rewritten by a PUT body, inserted after it by an AFTER body, or rewritten in
// place by "⟪old│new⟫" selections. "…" elides unchanged runs inside an anchor,
// so the model restates only the lines that identify the site.
//
// The parser (internal/tool/fs/sloppy) is a byte-compatible port of
// pulseaiclub/phi's package of the same name; the apply stage
// (internal/tool/fs/sloppyedit) is the port of its engine. Keeping the two in
// step with upstream is deliberate: the format is documented for models, and a
// dialect only san parses would cost a failed round-trip per novel mistake.

// sloppyEdit is one parsed payload, pinned to the file its header names.
type sloppyEdit struct {
	payload string
	section sloppy.Section
}

// sloppyResult is a payload applied to a file's text.
type sloppyResult struct {
	content string
	ops     int
	notes   []string
}

// parseSloppyEdit parses the payload and pins it to one file. Parsing is pure,
// so the permission preview and the approved execution see the same sections.
func parseSloppyEdit(payload string) (sloppyEdit, error) {
	sections, err := sloppy.Parse(payload)
	if err != nil {
		return sloppyEdit{}, &tool.ToolError{Message: err.Error()}
	}
	switch len(sections) {
	case 0:
		return sloppyEdit{}, &tool.ToolError{Message: `[E_BAD_SHAPE] the payload edits nothing: start it with "*** SM:EDIT relative/path" and give each operation a "*** SM:FIND" header`}
	case 1:
		return sloppyEdit{payload: payload, section: sections[0]}, nil
	default:
		paths := make([]string, len(sections))
		for i, section := range sections {
			paths[i] = section.Path
		}
		return sloppyEdit{}, &tool.ToolError{Message: fmt.Sprintf(
			"[E_MULTI_FILE] this payload edits %d files (%s). One Edit call reviews one file, so every section applies or none does — send one payload per file",
			len(sections), strings.Join(paths, ", "))}
	}
}

// applySloppyEdit runs the payload against the file text. Operations anchor on
// the original content, so earlier operations never shift later anchors, and
// any failure leaves the file untouched.
func applySloppyEdit(content string, edit sloppyEdit) (sloppyResult, error) {
	result, err := sloppyedit.Apply(content, edit.section, len(edit.section.Ops) == 1)
	if err != nil {
		return sloppyResult{}, &tool.ToolError{Message: err.Error()}
	}
	return sloppyResult{content: result.Content, ops: len(edit.section.Ops), notes: result.Notes}, nil
}

// sloppyTarget resolves the file the payload edits. A file_path argument is not
// part of the shape — the header names the file — but a call that carries one
// must not disagree with the header, or the approval the user reads would
// describe one file while the patch rewrote another.
func sloppyTarget(params map[string]any, edit sloppyEdit, cwd string) (string, error) {
	named := resolveEditPath(edit.section.Path, cwd)
	stated := tool.GetString(params, "file_path")
	if stated == "" {
		return named, nil
	}
	if resolved := resolveEditPath(stated, cwd); resolved != named {
		return "", &tool.ToolError{Message: fmt.Sprintf(
			`[E_BAD_SHAPE] file_path %q and the payload's "*** SM:EDIT %s" name different files. Drop file_path — the payload names the file it edits — or make them the same`,
			stated, edit.section.Path)}
	}
	return named, nil
}
