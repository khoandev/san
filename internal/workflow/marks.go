package workflow

// Marks are the status marks the compact views draw: one glyph per node status,
// in the styles the graph, the activity stream and the run summary all use.
// Skipped and Omitted have no counterpart in the TUI's icon table and are
// workflow's own.
type Marks struct {
	Running   string
	Pending   string
	Succeeded string
	Failed    string
	Skipped   string
	Omitted   string
}

// defaultMarks is what a view draws when no mark source is installed: the
// glyphs these views have always emitted, which the TUI's own marker
// normalization also matches literally. It keeps a process that never installs
// a source — a test, a CLI path — drawing exactly what it drew before.
var defaultMarks = Marks{
	Running:   "●",
	Pending:   "○",
	Succeeded: "✓",
	Failed:    "✗",
	Skipped:   "↷",
	Omitted:   "⊘",
}

// markSource, when set, supplies the live marks. A function rather than a table
// so a style change takes effect without a second install: the views read the
// source on every render.
var markSource func() Marks

// SetMarkSource installs the function the compact views read their status marks
// from. The TUI calls it once at startup so the workflow graph, the activity
// stream and the conversation around them draw the same icon style; nil
// restores the default literals. A source that leaves a field empty keeps the
// default for that status, so it only has to name the marks it changes.
func SetMarkSource(fn func() Marks) {
	markSource = fn
}

// currentMarks returns the marks to draw now, defaults filled in for whatever
// the source leaves empty.
func currentMarks() Marks {
	marks := defaultMarks
	if markSource == nil {
		return marks
	}
	got := markSource()
	for _, f := range [...]struct {
		dst *string
		src string
	}{
		{&marks.Running, got.Running},
		{&marks.Pending, got.Pending},
		{&marks.Succeeded, got.Succeeded},
		{&marks.Failed, got.Failed},
		{&marks.Skipped, got.Skipped},
		{&marks.Omitted, got.Omitted},
	} {
		if f.src != "" {
			*f.dst = f.src
		}
	}
	return marks
}
