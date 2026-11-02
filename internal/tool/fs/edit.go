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

// maxFreshAnchorRows caps the edited rows echoed back after an edit. The rows
// exist so a follow-up anchored edit needs no Read; past a screenful they cost
// more context than the Read they save.
const maxFreshAnchorRows = 20

// EditTool changes a file in one of two shapes:
//
//   - anchored (preferred): file_path, remove_from, remove_to, text — replace
//     the lines spanned by two anchors from Read output. The target is named by
//     a handle instead of by copied text, so transcription slips cannot miss it
//     and a stale handle is refused by name.
//   - legacy: file_path, old_string, new_string, replace_all — the exact
//     string replacement models know from training. It stays because that
//     familiarity is worth more than any schema documentation for call
//     reliability, and because a two-line change is genuinely cheaper to write
//     this way.
type EditTool struct{}

type editReplacement struct {
	oldString  string
	newString  string
	replaceAll bool
}

// editRequest is one Edit call in either shape; exactly one of legacy and
// anchored is set.
type editRequest struct {
	filePath string
	legacy   *editReplacement
	anchored *anchorEdit
}

// editPlan is an edit computed against the file as it stands, before anything
// is written. The permission preview and the approved execution share it so
// the diff the user approves is the change that lands.
type editPlan struct {
	oldContent string
	newContent string
	anchored   bool
	count      int // legacy: replacements; anchored: lines written
	start, end int // anchored: the replaced range (1-based, inclusive)
	warnings   []string
}

func (t *EditTool) Name() string { return "Edit" }
func (t *EditTool) Description() string {
	return "Edit file contents by line anchor or exact string replacement"
}
func (t *EditTool) Icon() string { return IconEdit }

func (t *EditTool) RequiresPermission() bool { return true }

func (t *EditTool) PreparePermission(ctx context.Context, params map[string]any, cwd string) (*perm.PermissionRequest, error) {
	req, err := parseEditRequest(params)
	if err != nil {
		return nil, err
	}
	filePath := resolveEditPath(req.filePath, cwd)

	if _, err := requireObservedView(filePath); err != nil {
		return nil, &tool.ToolError{Message: err.Error()}
	}
	plan, err := planEdit(filePath, req)
	if err != nil {
		return nil, err
	}

	description := "Replace text in file"
	if plan.anchored {
		description = fmt.Sprintf("Replace lines %d-%d", plan.start, plan.end)
	}
	return &perm.PermissionRequest{
		ID:          tool.GenerateRequestID(),
		ToolName:    t.Name(),
		FilePath:    filePath,
		Description: description,
		DiffMeta:    perm.GenerateDiff(filePath, plan.oldContent, plan.newContent),
	}, nil
}

func (t *EditTool) ExecuteApproved(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()
	req, err := parseEditRequest(params)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}
	filePath := resolveEditPath(req.filePath, cwd)

	view, err := requireObservedView(filePath)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}
	plan, err := planEdit(filePath, req)
	if err != nil {
		// A stale view turns any failure ambiguous: the anchors may be old, or
		// the target may have been changed under the model. Name the staleness
		// so the recovery (re-read) is obvious.
		if view == viewStale {
			return toolresult.NewErrorResult(t.Name(),
				fmt.Sprintf("%s changed on disk after it was last read, and against its current content: %s. Read the file again", filePath, err.Error()))
		}
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	if err := atomicfile.WriteFile(filePath, []byte(plan.newContent), 0o644); err != nil {
		return toolresult.NewErrorResult(t.Name(), "failed to write file: "+err.Error())
	}
	RecordFileWritten(filePath)
	fresh := recordEditAnchors(filePath, plan.oldContent, plan.newContent)

	changes := perm.GenerateDiff(filePath, plan.oldContent, plan.newContent)
	storedDiff, truncatedDiffLines := perm.CapUnifiedDiff(changes.UnifiedDiff, maxStoredDiffLines)

	output := editSummary(filePath, plan) + fmt.Sprintf(" (+%d -%d)", changes.AddedCount, changes.RemovedCount)
	for _, warning := range plan.warnings {
		output += "\n" + warning
	}
	// On the fresh path the hint suppresses the verify-read reflex; on the
	// stale path the edit landed cleanly but the file holds other changes
	// the model has not seen.
	if view == viewStale {
		output += ". Note: the file had changed on disk since your last read — this edit applied cleanly, but the file contains other changes not in your context; Read it before edits that depend on surrounding content"
	} else {
		output += "; file state is current — no need to re-read"
	}
	if rows := formatFreshAnchors(fresh); rows != "" {
		output += "\n" + rows
	}

	hookResponse := map[string]any{
		"filePath":        filePath,
		"originalFile":    plan.oldContent,
		"structuredPatch": []any{},
		"userModified":    false,
	}
	if plan.anchored {
		hookResponse["removeFrom"] = req.anchored.removeFrom
		hookResponse["removeTo"] = req.anchored.removeTo
		hookResponse["text"] = strings.Join(req.anchored.lines, "\n")
	} else {
		hookResponse["oldString"] = req.legacy.oldString
		hookResponse["newString"] = req.legacy.newString
		hookResponse["replaceAll"] = req.legacy.replaceAll
	}

	return toolresult.ToolResult{
		Success: true,
		Output:  output,
		Details: toolresult.FileChangeDetails{
			Path:               filePath,
			EditCount:          plan.count,
			AddedLines:         changes.AddedCount,
			RemovedLines:       changes.RemovedCount,
			UnifiedDiff:        storedDiff,
			TruncatedDiffLines: truncatedDiffLines,
		},
		HookResponse: hookResponse,
		Metadata: toolresult.ResultMetadata{
			Title: t.Name(), Icon: t.Icon(), Subtitle: filePath, Duration: time.Since(start),
		},
	}
}

// editSummary is the one line the model reads first: what the call was taken to
// mean. For an anchored edit that includes the range, because a swapped pair of
// anchors is otherwise invisible in a successful result.
func editSummary(filePath string, plan editPlan) string {
	if plan.anchored {
		return fmt.Sprintf("Edited %s (lines %d-%d replaced with %d line(s))", filePath, plan.start, plan.end, plan.count)
	}
	return fmt.Sprintf("Edited %s (%d replacement(s))", filePath, plan.count)
}

// formatFreshAnchors prints the rows an edit just wrote, with their anchors.
// They are the anchors a follow-up edit of the same region needs, so printing
// them removes a Read from the common edit-then-fix loop.
func formatFreshAnchors(rows []anchorRow) string {
	if len(rows) == 0 || len(rows) > maxFreshAnchorRows {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Fresh anchors for the lines just written:\n")
	for _, row := range rows {
		fmt.Fprintf(&sb, toolresult.AnchorLineFormat, row.lineNo, row.anchor, row.text)
	}
	return sb.String()
}

// planEdit reads the file and computes the resulting content without writing.
func planEdit(filePath string, req editRequest) (editPlan, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return editPlan{}, &tool.ToolError{Message: "file not found: " + filePath}
		}
		return editPlan{}, &tool.ToolError{Message: "failed to read file: " + err.Error()}
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return editPlan{}, &tool.ToolError{Message: "failed to stat file: " + err.Error()}
	}

	plan := editPlan{oldContent: string(content)}
	bom, body, crlf, err := normalizeForEdit(plan.oldContent)
	if err != nil {
		return editPlan{}, err
	}

	if req.anchored != nil {
		result, err := applyAnchorEdit(filePath, info, body, *req.anchored)
		if err != nil {
			return editPlan{}, err
		}
		plan.newContent = restoreLineEndings(bom, result.content, crlf)
		plan.anchored = true
		plan.start, plan.end = result.start, result.end
		plan.count = result.added
		plan.warnings = append(plan.warnings, req.anchored.warnings...)
		plan.warnings = append(plan.warnings, result.warnings...)
		return plan, nil
	}

	body, count, err := replaceInContent(body, *req.legacy)
	if err != nil {
		return editPlan{}, err
	}
	plan.newContent = restoreLineEndings(bom, body, crlf)
	plan.count = count
	return plan, nil
}

// recordEditAnchors moves the file's anchor snapshot onto the content just
// written and returns the rows of the changed range. Lines outside the range
// keep their anchors (shifted), so an edit does not invalidate handles the
// model holds for the rest of the file.
func recordEditAnchors(filePath, oldContent, newContent string) []anchorRow {
	info, err := os.Stat(filePath)
	if err != nil {
		forgetAnchors(filePath)
		return nil
	}
	// Anchors number the lines Read shows, so both sides are compared in the
	// normalized form Read scans: a CRLF file's rows carry no trailing CR.
	_, oldBody, _, oldErr := normalizeForEdit(oldContent)
	_, newBody, _, newErr := normalizeForEdit(newContent)
	if oldErr != nil || newErr != nil {
		forgetAnchors(filePath)
		return nil
	}
	first, last, count, ok := changedLineRange(oldBody, newBody)
	if !ok {
		return nil
	}
	lines := splitFileLines(newBody)
	if first < 1 || first-1+count > len(lines) {
		forgetAnchors(filePath)
		return nil
	}
	texts := lines[first-1 : first-1+count]
	anchors := mintAnchors(count)
	recordAnchorEdit(filePath, info, first, last, anchors, texts)

	rows := make([]anchorRow, count)
	for i := range rows {
		rows[i] = anchorRow{lineNo: first + i, text: texts[i], anchor: anchors[i]}
	}
	return rows
}

// parseEditRequest picks the shape the call uses and validates it. The two
// shapes are mutually exclusive: a call carrying fields from both is a model
// hedging, and guessing which half it meant would edit the wrong thing.
func parseEditRequest(params map[string]any) (editRequest, error) {
	filePath, err := tool.RequireString(params, "file_path")
	if err != nil {
		return editRequest{}, err
	}

	_, hasFrom := params["remove_from"]
	_, hasTo := params["remove_to"]
	_, hasText := params["text"]
	_, hasOld := params["old_string"]
	_, hasNew := params["new_string"]

	if hasFrom || hasTo || hasText {
		if hasOld || hasNew {
			return editRequest{}, &tool.ToolError{Message: `[E_BAD_SHAPE] Edit takes one shape per call: anchored {remove_from, remove_to, text} or legacy {old_string, new_string, replace_all}, not both`}
		}
		anchored, err := parseAnchorEdit(params)
		if err != nil {
			return editRequest{}, err
		}
		return editRequest{filePath: filePath, anchored: &anchored}, nil
	}

	oldString, ok := params["old_string"].(string)
	if !ok || oldString == "" {
		return editRequest{}, &tool.ToolError{Message: `old_string must be a non-empty string, or pass remove_from/remove_to anchors from Read output`}
	}
	newString, ok := params["new_string"].(string)
	if !ok {
		return editRequest{}, &tool.ToolError{Message: "new_string is required (use an empty string to delete old_string)"}
	}
	oldString = normalizeLineEndings(oldString)
	newString = normalizeLineEndings(newString)
	if newString == oldString {
		return editRequest{}, &tool.ToolError{Message: "new_string must be different from old_string"}
	}
	return editRequest{
		filePath: filePath,
		legacy: &editReplacement{
			oldString:  oldString,
			newString:  newString,
			replaceAll: tool.GetBool(params, "replace_all"),
		},
	}, nil
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

// applyEdit performs a legacy replacement on content, preserving a BOM and
// Windows line endings. It returns the new content and how many occurrences
// were replaced.
func applyEdit(content string, edit editReplacement) (string, int, error) {
	bom, body, crlf, err := normalizeForEdit(content)
	if err != nil {
		return "", 0, err
	}
	body, count, err := replaceInContent(body, edit)
	if err != nil {
		return "", 0, err
	}
	return restoreLineEndings(bom, body, crlf), count, nil
}

func replaceInContent(content string, edit editReplacement) (string, int, error) {
	occurrences := strings.Count(content, edit.oldString)
	if edit.replaceAll && occurrences > 0 {
		return strings.ReplaceAll(content, edit.oldString, edit.newString), occurrences, nil
	}

	switch occurrences {
	case 1:
		return strings.Replace(content, edit.oldString, edit.newString, 1), 1, nil
	case 0:
		// Zero exact matches usually means a whitespace transcription slip;
		// the tolerant ladder recovers or diagnoses it. This also serves
		// replace_all — a unique tolerant match is the only occurrence.
		match, err := resolveTolerantMatch(content, edit.oldString)
		if err != nil {
			return "", 0, err
		}
		return content[:match.start] + edit.newString + content[match.end:], 1, nil
	default:
		return "", 0, fmt.Errorf("old_string matches %d locations; add surrounding context to make it unique, or set replace_all to change every occurrence", occurrences)
	}
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
