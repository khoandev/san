package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/tool/toolresult"
)

const IconEdit = "✏️"

// maxStoredDiffLines caps the unified diff persisted with a result so a huge
// rewrite doesn't bloat the session transcript; the UI shows the cap notice.
const maxStoredDiffLines = 400

// EditTool changes a file through a sloppy anchored patch.
//
// The call carries one payload: a "*** SM:EDIT path" header, then operations.
// Each operation quotes current file text under "*** SM:FIND" and either
// replaces it under "*** SM:PUT", inserts after it under "*** SM:AFTER", or
// rewrites fragments in place with "⟪old│new⟫" selections inside the anchor.
// "…" elides the runs the anchor does not restate.
//
// It replaced san's earlier shapes — line anchors (remove_from/remove_to) and
// exact string replacement (old_string/new_string) — because both made the
// model reproduce text it had already read: the anchor shape forced a whole
// replacement range to be retyped, and the exact shape broke on any
// transcription slip, indentation included. A sloppy patch quotes only the
// lines that identify the site, tolerates whitespace and typographic drift,
// and still fails closed: an anchor that is absent or ambiguous applies
// nothing.
type EditTool struct{}

// editPlan is an edit computed against the file as it stands, before anything
// is written. The permission preview and the approved execution share it so
// the diff the user approves is the change that lands.
type editPlan struct {
	filePath   string
	payload    string
	oldContent string
	newContent string
	ops        int      // operations the payload applied
	notes      []string // per-operation notes for the model
	staleView  bool     // the file changed on disk after the model last read it
}

func (t *EditTool) Name() string { return "Edit" }
func (t *EditTool) Description() string {
	return "Edit file contents with an anchored sloppy patch"
}
func (t *EditTool) Icon() string { return IconEdit }

func (t *EditTool) RequiresPermission() bool { return true }

func (t *EditTool) PreparePermission(ctx context.Context, params map[string]any, cwd string) (*perm.PermissionRequest, error) {
	plan, err := planEdit(params, cwd)
	if err != nil {
		return nil, err
	}
	return &perm.PermissionRequest{
		ID:          tool.GenerateRequestID(),
		ToolName:    t.Name(),
		FilePath:    plan.filePath,
		Description: fmt.Sprintf("Apply %d anchored edit(s)", plan.ops),
		DiffMeta:    perm.GenerateDiff(plan.filePath, plan.oldContent, plan.newContent),
	}, nil
}

func (t *EditTool) ExecuteApproved(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()
	plan, err := planEdit(params, cwd)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	if err := atomicfile.WriteFile(plan.filePath, []byte(plan.newContent), 0o644); err != nil {
		return toolresult.NewErrorResult(t.Name(), "failed to write file: "+err.Error())
	}
	RecordFileWritten(plan.filePath)

	changes := perm.GenerateDiff(plan.filePath, plan.oldContent, plan.newContent)
	storedDiff, truncatedDiffLines := perm.CapUnifiedDiff(changes.UnifiedDiff, maxStoredDiffLines)

	output := editSummary(plan) + fmt.Sprintf(" (+%d -%d)", changes.AddedCount, changes.RemovedCount)
	for _, note := range plan.notes {
		output += "\n" + note
	}
	// On the fresh path the hint suppresses the verify-read reflex; on the
	// stale path the edit landed cleanly but the file holds other changes
	// the model has not seen.
	if plan.staleView {
		output += ". Note: the file had changed on disk since your last read — this edit applied cleanly, but the file contains other changes not in your context; Read it before edits that depend on surrounding content"
	} else {
		output += "; file state is current — no need to re-read"
	}

	hookResponse := map[string]any{
		"filePath":        plan.filePath,
		"originalFile":    plan.oldContent,
		"structuredPatch": []any{},
		"userModified":    false,
		"payload":         plan.payload,
	}

	return toolresult.ToolResult{
		Success: true,
		Output:  output,
		Details: toolresult.FileChangeDetails{
			Path:               plan.filePath,
			EditCount:          plan.ops,
			AddedLines:         changes.AddedCount,
			RemovedLines:       changes.RemovedCount,
			UnifiedDiff:        storedDiff,
			TruncatedDiffLines: truncatedDiffLines,
		},
		HookResponse: hookResponse,
		Metadata: toolresult.ResultMetadata{
			Title: t.Name(), Icon: t.Icon(), Subtitle: plan.filePath, Duration: time.Since(start),
		},
	}
}

// editSummary is the one line the model reads first: what the call was taken to
// mean. The file is named after the payload's own header, because that header —
// not an argument — decided which file was edited.
func editSummary(plan editPlan) string {
	return fmt.Sprintf("Edited %s (%d anchored operation(s) applied)", plan.filePath, plan.ops)
}

// planEdit parses the payload, resolves its file, and computes the resulting
// content without writing. The permission preview and the approved execution
// both go through here, so what the user approves is what lands.
func planEdit(params map[string]any, cwd string) (editPlan, error) {
	payload := perm.PayloadText(params)
	if payload == "" {
		return editPlan{}, &tool.ToolError{Message: `[E_BAD_SHAPE] Edit takes one "payload": start it with "*** SM:EDIT relative/path", then give each operation a "*** SM:FIND" anchor and a "*** SM:PUT" or "*** SM:AFTER" body (or "⟪old│new⟫" selections inside the anchor)`}
	}
	edit, err := parseSloppyEdit(payload)
	if err != nil {
		return editPlan{}, err
	}
	filePath, err := sloppyTarget(params, edit, cwd)
	if err != nil {
		return editPlan{}, err
	}

	// A patch quotes text the model read; editing a file it has not seen — or
	// whose view changed under it — would anchor against lines it cannot know.
	view, err := requireObservedView(filePath)
	if err != nil {
		return editPlan{}, &tool.ToolError{Message: err.Error()}
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return editPlan{}, &tool.ToolError{Message: "file not found: " + filePath}
		}
		return editPlan{}, &tool.ToolError{Message: "failed to read file: " + err.Error()}
	}

	plan := editPlan{filePath: filePath, oldContent: string(content), payload: payload, staleView: view == viewStale}
	bom, body, crlf, err := normalizeForEdit(plan.oldContent)
	if err != nil {
		return editPlan{}, err
	}
	result, err := applySloppyEdit(body, edit)
	if err != nil {
		// A stale view turns any failure ambiguous: the anchor may quote text
		// that changed under the model. Name the staleness so the recovery
		// (re-read, then re-anchor) is obvious.
		if plan.staleView {
			return editPlan{}, &tool.ToolError{Message: fmt.Sprintf(
				"%s changed on disk after it was last read, and this payload failed against its current content: %s. Read the file again",
				filePath, err.Error())}
		}
		return editPlan{}, err
	}
	plan.newContent = restoreLineEndings(bom, result.content, crlf)
	plan.ops = result.ops
	plan.notes = result.notes
	return plan, nil
}

// normalizeForEdit strips the BOM and normalizes to LF, reporting the file's
// own conventions so the write can restore them. Mixed endings are refused:
// rewriting a file would silently pick one convention for every line.
func normalizeForEdit(content string) (bom, body string, crlf bool, err error) {
	if after, ok := strings.CutPrefix(content, "\ufeff"); ok {
		bom, content = "\ufeff", after
	}
	crlf = strings.Contains(content, "\r\n")
	if crlf && strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
		return "", "", false, fmt.Errorf("file has mixed line endings; normalize it before editing")
	}
	return bom, normalizeLineEndings(content), crlf, nil
}

func restoreLineEndings(bom, body string, crlf bool) string {
	if crlf {
		body = strings.ReplaceAll(body, "\n", "\r\n")
	}
	return bom + body
}

func normalizeLineEndings(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func resolveEditPath(filePath, cwd string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(cwd, filePath)
}

func (t *EditTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.ExecuteApproved(ctx, params, cwd)
}

func init() {
	tool.Register(&EditTool{})
}
