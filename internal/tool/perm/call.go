package perm

import (
	"strings"

	"github.com/genai-io/san/internal/tool/fs/sloppy"
)

// The helpers here answer "which file does this call touch" for the tools that
// change one. Edit is the odd one: it names its target inside the payload
// header rather than in a file_path argument, so every caller that scopes,
// previews or rules on a call has to read the path the same way the tool does.
// Keeping that reading in one place is what stops a permission rule from
// scoping a call by an argument the call never carried.

// PayloadParamNames are the spellings an Edit call may use for its payload, in
// priority order. "payload" is the documented one; "patch" and "input" are the
// aliases the reference engine accepts too, so a call that reaches for a more
// familiar name is not rejected on shape alone.
var PayloadParamNames = []string{"payload", "patch", "input"}

// PayloadText returns the anchored payload an Edit call carries, or "" when it
// carries none. A whitespace-only value counts as absent so an empty alias
// beside a real one does not shadow it.
func PayloadText(args map[string]any) string {
	for _, name := range PayloadParamNames {
		if value, _ := args[name].(string); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// CallFile returns the file a call targets, as the tool will resolve it. Read
// and Write name it in file_path; Edit names it in its payload's
// "*** SM:EDIT" header. A payload that does not parse names nothing, so a
// malformed call falls back to whatever the call did state.
func CallFile(toolName string, args map[string]any) string {
	if toolName == "Edit" {
		if paths := sloppy.TargetPaths(PayloadText(args)); len(paths) > 0 {
			return paths[0]
		}
	}
	path, _ := args["file_path"].(string)
	return path
}
