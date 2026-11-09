// configForm is the config zone both settings panels are built on: /evolve's
// self-learning form and /settings' advisor form. It edits a working snapshot
// of one settings struct against a baseline, validates inline, and saves to one
// of the scopes it offers — the user file, the project file, or (for the
// advisor) this session only.
//
// The owning panel supplies its rows via rowsFn and the type-specific parts —
// where the working copy starts, how it is validated, compared, persisted and
// announced — as constructor fields. The snap, scope selector, cursor/edit
// machinery and Save action are shared here; a panel renders additional zones
// (the Strategy editor, the Learned drill-in) around the form by composing
// rather than extending it.
package input

import (
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/setting"
)

// ConfigSavedMsg is emitted on a successful self-learning Save so the app can
// show a transient confirmation. SavedSelfLearn carries the snapshot that was
// just written so the consumer can compare it against the post-Reload
// effective state and detect cross-level overrides (settings merger ORs
// Enabled across user+project).
type ConfigSavedMsg struct {
	Scope          setting.Scope
	SavedSelfLearn setting.SelfLearnSettings
}

// configScope is where a saved config goes: one of the two settings files, or
// — for a config the session can own — this conversation only. It is not
// setting.Scope because a session has no file to write.
type configScope string

const (
	scopeUser    configScope = configScope(setting.ScopeUser)
	scopeProject configScope = configScope(setting.ScopeProject)
	scopeSession configScope = "session"
)

// configForm is the shared form. T is the settings struct the panel edits.
type configForm[T any] struct {
	// workspace returns the live cwd + settings service; read at Enter so a
	// project reload is picked up. Nil (tests) seeds a zero snapshot.
	workspace func() (string, *setting.Settings)

	// rowsFn returns the panel-specific rows.
	rowsFn func() []configRow[T]

	// seed places the working copy: where this config starts in a settings
	// snapshot. Nil starts every edit from T's zero value.
	seed func(*setting.Data) T
	// equal compares two working copies for the unsaved-edits indicator. Nil
	// compares with ==, which only compiles for a comparable T.
	equal func(T, T) bool
	// validate rejects an illegal working copy before anything is written.
	validate func(T) error
	// persist writes the copy at the chosen scope. A scope with no file (the
	// session) writes nothing here; the message carries it instead.
	persist func(T, configScope) error
	// saved builds the message that hands the written copy to the app.
	saved func(T, configScope) tea.Msg
	// scopes are the save targets the ←→ control cycles through, in display
	// order. Empty means user and project.
	scopes []configScope
	// visible is how many body rows the card can show. The form windows its
	// rows around the cursor past that count, so a long settings list stays
	// navigable on a short terminal. Zero shows every row.
	visible int

	// snap is the working buffer; Save writes it to the chosen scope.
	snap T
	// baseline captures snap as it was at Enter() time so the form can flag
	// unsaved edits in the top-right corner.
	baseline T
	scope    configScope

	cursor        int
	editing       bool
	editingBuffer string
}

// scopesOrDefault lists the save targets the scope control shows.
func (f *configForm[T]) scopesOrDefault() []configScope {
	if len(f.scopes) == 0 {
		return []configScope{scopeUser, scopeProject}
	}
	return f.scopes
}

func (f *configForm[T]) Enter() {
	f.editing = false
	f.editingBuffer = ""
	var zero T
	f.snap = zero
	if f.workspace != nil && f.seed != nil {
		if _, settings := f.workspace(); settings != nil {
			if data := settings.Snapshot(); data != nil {
				f.snap = f.seed(data)
			}
		}
	}
	f.baseline = f.snap
	f.scope = f.scopesOrDefault()[0]
	f.cursor = findEditable(f.rowsFn(), 0, +1, 0)
}

// Dirty reports whether the working snapshot diverges from the baseline — the
// shell uses it to pin the "● unsaved" indicator.
func (f *configForm[T]) Dirty() bool {
	if f.equal == nil {
		return any(f.snap) != any(f.baseline)
	}
	return !f.equal(f.snap, f.baseline)
}

// HandleKey handles one keypress. activated names the rowEntry the user
// pressed enter on ("" otherwise) so the owning panel can open the matching
// sub-view; done=true asks the shell to dismiss the popup (after Save).
func (f *configForm[T]) HandleKey(msg tea.KeyMsg) (cmd tea.Cmd, done bool, activated string) {
	if f.editing {
		return f.handleEditingKey(msg), false, ""
	}
	rows := f.rowsFn()
	if f.cursor >= len(rows) {
		f.cursor = findEditable(rows, 0, +1, 0)
	}
	switch msg.String() {
	case "up", "k":
		f.cursor = findEditable(rows, f.cursor-1, -1, f.cursor)
	case "down", "j":
		f.cursor = findEditable(rows, f.cursor+1, +1, f.cursor)
	case "left", "right":
		// ←→ cycles the save target; Tab is reserved by the shell for switching
		// tabs.
		f.scope = f.nextScope(msg.String() == "right")
	case "space":
		// Space toggles the checkbox on a plain bool row or the checkbox half
		// of a bool+int row; on a choice row it steps to the next option.
		switch r := rows[f.cursor]; r.kind {
		case rowBool, rowBoolInt:
			if r.toggle != nil {
				r.toggle(&f.snap)
			}
		case rowChoice:
			r.cycle(&f.snap)
		}
	case "enter":
		row := rows[f.cursor]
		switch row.kind {
		case rowBool:
			if row.toggle != nil {
				row.toggle(&f.snap)
			}
		case rowInt, rowBoolInt:
			// Enter edits the number; Space toggles the checkbox (bool+int).
			f.editing = true
			f.editingBuffer = strconv.Itoa(row.intGetter(&f.snap))
		case rowText:
			f.editing = true
			f.editingBuffer = row.strGetter(&f.snap)
		case rowChoice:
			row.cycle(&f.snap)
		case rowEntry:
			// The form doesn't own the sub-view; tell the panel to open it.
			return nil, false, row.entryID
		case rowSave:
			cmd, done = f.save()
			return cmd, done, ""
		}
	}
	return nil, false, ""
}

// nextScope steps the save target one place through the scope list, wrapping at
// either end so ←→ never dead-ends.
func (f *configForm[T]) nextScope(forward bool) configScope {
	scopes := f.scopesOrDefault()
	at := 0
	for i, s := range scopes {
		if s == f.scope {
			at = i
			break
		}
	}
	step := 1
	if !forward {
		step = len(scopes) - 1
	}
	return scopes[(at+step)%len(scopes)]
}

// save validates the working snapshot, writes it to the chosen scope, and
// emits the panel's saved message with done=true. A failed validation or write
// keeps the popup open (the inline error already shows why).
func (f *configForm[T]) save() (tea.Cmd, bool) {
	if f.validate != nil {
		if err := f.validate(f.snap); err != nil {
			return nil, false
		}
	}
	if f.persist != nil {
		if err := f.persist(f.snap, f.scope); err != nil {
			return nil, false
		}
	}
	scope := f.scope
	saved := f.snap
	if f.saved == nil {
		return nil, true
	}
	return func() tea.Msg { return f.saved(saved, scope) }, true
}

func (f *configForm[T]) handleEditingKey(msg tea.KeyMsg) tea.Cmd {
	row := f.rowsFn()[f.cursor]
	switch msg.String() {
	case "esc":
		f.editing = false
		f.editingBuffer = ""
	case "enter":
		f.commitEdit(row)
		f.editing = false
		f.editingBuffer = ""
	case "backspace":
		if n := len(f.editingBuffer); n > 0 {
			// Trim a whole final rune so multi-byte paths delete cleanly.
			f.editingBuffer = trimLastRune(f.editingBuffer)
		}
	default:
		f.appendEditText(row, msg.Key().Text)
	}
	return nil
}

// commitEdit writes the edit buffer back to the snapshot: clamped int for
// rowInt, trimmed free text for rowText.
func (f *configForm[T]) commitEdit(row configRow[T]) {
	switch row.kind {
	case rowInt, rowBoolInt:
		if v, err := strconv.Atoi(f.editingBuffer); err == nil {
			v = min(max(v, row.intMin), row.intMax)
			row.intSetter(&f.snap, v)
		}
	case rowText:
		row.strSetter(&f.snap, strings.TrimSpace(f.editingBuffer))
	}
}

// appendEditText appends typed input: digits only (capped at 4) for the numeric
// rows, any printable text (capped) for rowText.
func (f *configForm[T]) appendEditText(row configRow[T], t string) {
	switch row.kind {
	case rowInt, rowBoolInt:
		if len(t) == 1 && t[0] >= '0' && t[0] <= '9' && len(f.editingBuffer) < 4 {
			f.editingBuffer += t
		}
	case rowText:
		if t != "" && len(f.editingBuffer) < 512 {
			f.editingBuffer += t
		}
	}
}

// trimLastRune drops the final UTF-8 rune of s.
func trimLastRune(s string) string {
	r := []rune(s)
	return string(r[:len(r)-1])
}

// HintLine renders the bottom hint as monospace keycaps.
func (f *configForm[T]) HintLine() string {
	return keycap("↑↓") + " navigate  " +
		keycap("space") + " toggle  " +
		keycap("enter") + " edit/save  " +
		keycap("←→") + " scope"
}

// Render draws the config-zone body. The arm's identity comes from the tab
// strip above, so there is no section header or rail here — just the scope
// control and a flat, calmly-spaced list of rows.
func (f *configForm[T]) Render(width int) string {
	rows := f.rowsFn()
	var validationErr error
	if f.validate != nil {
		validationErr = f.validate(f.snap)
	}

	var b strings.Builder
	b.WriteString(f.renderScopeControl())
	start, end := f.window(rows)
	if start > 0 || end < len(rows) {
		b.WriteString(selflearnFaintStyle.Render(
			"  " + strconv.Itoa(start+1) + "–" + strconv.Itoa(end) + " of " + strconv.Itoa(len(rows))))
	}
	b.WriteString("\n\n")

	for i := start; i < end; i++ {
		row := rows[i]
		switch row.kind {
		case rowSubHeader:
			// Align the label with the entry-row labels (which sit past a
			// 2-col cursor gutter) so the section headers read as one level.
			indentPad := strings.Repeat(" ", contentCol(row.indent))
			b.WriteString(indentPad + selflearnSubHeaderStyle.Render(strings.ToUpper(row.label)))
			b.WriteString("\n") // breathing room under the sub-header
		case rowSpacer:
			// blank line
		case rowSave:
			b.WriteString(f.renderSaveRow(i, validationErr))
		case rowBool:
			b.WriteString(f.renderBoolRow(i, row, width))
		case rowInt:
			b.WriteString(f.renderIntRow(i, row, width))
		case rowBoolInt:
			b.WriteString(f.renderBoolIntRow(i, row))
		case rowText:
			b.WriteString(f.renderTextRow(i, row, width))
		case rowChoice:
			b.WriteString(f.renderChoiceRow(i, row, width))
		case rowEntry:
			b.WriteString(f.renderEntryRow(i, row, width))
		}
		b.WriteString("\n")
	}

	if validationErr != nil {
		b.WriteString("\n")
		b.WriteString(selflearnErrorStyle.Render("⚠ " + validationErr.Error()))
		b.WriteString("\n")
	}
	return b.String()
}

// window is the run of rows the card can show, kept around the cursor. Two rows
// go to the scope control and its breathing space; the rest is the list.
func (f *configForm[T]) window(rows []configRow[T]) (start, end int) {
	end = len(rows)
	if f.visible <= 0 {
		return 0, end
	}
	avail := max(f.visible-2, 3)
	if len(rows) <= avail {
		return 0, len(rows)
	}
	start = min(max(f.cursor-avail/2, 0), len(rows)-avail)
	return start, start + avail
}

// renderScopeControl is a segmented selector with the active segment as a
// filled pill. The "scope" label is dropped — the visible segments (user /
// project / session) are self-explanatory.
func (f *configForm[T]) renderScopeControl() string {
	sep := selflearnMutedStyle.Render(" · ")
	segments := make([]string, 0, len(f.scopesOrDefault()))
	for _, scope := range f.scopesOrDefault() {
		if f.scope == scope {
			segments = append(segments, selflearnScopeActiveStyle.Render(string(scope)))
			continue
		}
		segments = append(segments, selflearnScopeIdleStyle.Render(string(scope)))
	}
	return strings.Join(segments, sep)
}

// keycap renders a key label as a bg-filled pill so it doesn't read as
// a checkbox. The fill is the kit's neutral search-input gray so the
// keycap feels like a physical key cap, not a [ ] toggle.
func keycap(s string) string {
	return selflearnKeycapStyle.Render(" " + capitalizeKeyLabel(s) + " ")
}

// capitalizeKeyLabel title-cases each part of a key combo so hints read as
// proper key names: "ctrl+r" → "Ctrl+R", "enter" → "Enter". Arrow glyphs
// (↑↓, ←→) and other non-letters pass through unchanged.
func capitalizeKeyLabel(s string) string {
	if len([]rune(s)) == 1 {
		return s // a bare letter key: "D" would read as shift+d
	}
	parts := strings.Split(s, "+")
	for i, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		r[0] = unicode.ToUpper(r[0])
		parts[i] = string(r)
	}
	return strings.Join(parts, "+")
}

// ── Row rendering ───────────────────────────────────────────────────────

// Layout columns. Every row's "content" (bracket for bool, value for int)
// starts at a column derived from its indent (indentStep cols per level);
// the cursor caret sits cursorWidth cols before that. Section headers go
// at indent 0 (col 0). Rows directly under a section: indent 1 (col 4).
// Sub-sections under those: header at indent 1, rows at indent 2 (col 8).
const (
	indentStep  = 4
	cursorWidth = 2
)

// contentCol returns the column where the row's leftmost rendered content
// (bracket / value / save button) starts, for the given indent.
func contentCol(indent int) int { return indent * indentStep }

func (f *configForm[T]) cursorMark(i int) string {
	if i == f.cursor {
		return selflearnCursorStyle.Render("▸ ")
	}
	return strings.Repeat(" ", cursorWidth)
}

// cursorPad returns the leading whitespace + cursor caret so the next
// glyph lands exactly at contentCol(indent).
func (f *configForm[T]) cursorPad(i, indent int) string {
	at := max(contentCol(indent)-cursorWidth, 0)
	return strings.Repeat(" ", at) + f.cursorMark(i)
}

func (f *configForm[T]) renderBoolRow(i int, row configRow[T], width int) string {
	mark := "[ ]"
	if row.boolGetter(&f.snap) {
		mark = selflearnCheckStyle.Render("[✓]")
	}
	line := f.cursorPad(i, row.indent) + mark + " " + row.label
	return noteSuffix(line, row.note, width)
}

// noteSuffix appends a row's muted note when the card is wide enough to hold
// it. A sentence clipped mid-word explains less than no sentence at all, so at
// the narrowest width a settings panel gets the row keeps its control and drops
// the note.
func noteSuffix(line, note string, width int) string {
	if note == "" {
		return line
	}
	const gap = "  "
	if lipgloss.Width(line)+lipgloss.Width(gap)+lipgloss.Width(note) > width {
		return line
	}
	return line + gap + selflearnMutedStyle.Render(note)
}

// mutedNote wraps a panel's own trailing note to the card width, so a pointer
// at settings.json survives a narrow terminal instead of running past the edge.
func mutedNote(text string, width int) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && lipgloss.Width(line)+1+lipgloss.Width(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line == "" {
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return selflearnMutedStyle.Render(strings.Join(lines, "\n"))
}

// renderBoolIntRow draws a checkbox with an inline editable cadence on one line,
// e.g. "[✓] Create new skills   every (10) iterations". Space toggles the box,
// Enter edits the number. The cadence is dimmed while the checkbox is off.
func (f *configForm[T]) renderBoolIntRow(i int, row configRow[T]) string {
	on := row.boolGetter(&f.snap)
	mark := "[ ]"
	if on {
		mark = selflearnCheckStyle.Render("[✓]")
	}
	value := strconv.Itoa(row.intGetter(&f.snap))
	if f.editing && i == f.cursor {
		value = f.editingBuffer + "_"
	}
	cadence := selflearnMutedStyle.Render(row.intLead+" ") + valueChip(value)
	if row.unit != "" {
		cadence += " " + selflearnMutedStyle.Render(row.unit)
	}
	if !on {
		cadence = selflearnFaintStyle.Render(row.intLead + " (" + value + ") " + row.unit)
	}
	return f.cursorPad(i, row.indent) + mark + " " + row.label + "   " + cadence
}

// renderChoiceRow draws a rowChoice as "label   ‹value›": the chevrons say the
// value steps, which space and enter do. It shows the resolved value, so an
// unset field reads as the default it behaves as.
func (f *configForm[T]) renderChoiceRow(i int, row configRow[T], width int) string {
	labelStart := contentCol(row.indent) + 4 // align with bool-row labels
	leftPad := strings.Repeat(" ", labelStart-cursorWidth)
	chips := selflearnChipBracketStyle.Render("‹") +
		selflearnValueStyle.Render(row.getChoice(&f.snap)) +
		selflearnChipBracketStyle.Render("›")
	line := leftPad + f.cursorMark(i) + row.label + "   " + chips
	return noteSuffix(line, row.note, width)
}

// renderEntryRow draws a drill-in section entry: "▸ STRATEGY  how it guides … built-in".
// The label is an uppercase muted-bold header (the same level as a sub-header),
// with a muted description and a right-aligned value hint; enter-to-open is in
// the bottom hint.
//
// A narrow card gives up the description before the summary, and then clips the
// summary's tail: a wrapped settings row explains less than a clipped hint, and
// the summary is the value the user came to read.
func (f *configForm[T]) renderEntryRow(i int, row configRow[T], width int) string {
	left := f.cursorPad(i, row.indent) + selflearnSubHeaderStyle.Render(strings.ToUpper(row.label))
	summary := ""
	if row.summary != nil {
		summary = row.summary(&f.snap)
	}
	if row.desc != "" {
		withDesc := left + "  " + selflearnMutedStyle.Render(row.desc)
		if summary == "" || lipgloss.Width(withDesc)+lipgloss.Width(summary)+3 <= width {
			left = withDesc
		}
	}
	if summary == "" {
		return left
	}
	// Leave a 2-col margin before the card edge so the rail-prefixed row never
	// spills the summary onto a wrapped line, plus one column of gap.
	avail := width - lipgloss.Width(left) - 3
	if avail < 6 {
		return left
	}
	shown := kit.TruncateText(summary, avail)
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(shown)-2, 1)
	return left + strings.Repeat(" ", gap) + selflearnEntrySummaryStyle.Render(shown)
}

func (f *configForm[T]) renderIntRow(i int, row configRow[T], _ int) string {
	value := strconv.Itoa(row.intGetter(&f.snap))
	if f.editing && i == f.cursor {
		value = f.editingBuffer + "_"
	}
	// Label-first phrase: "Run every (10) user turns" reads as a sentence.
	// The label sits one bracket-width past where a bool row's "[" goes,
	// so labels align with bool-row labels at the same indent.
	labelStart := contentCol(row.indent) + 4 // 4 = bracket width "[ ] "
	leftPad := strings.Repeat(" ", labelStart-cursorWidth)
	line := leftPad + f.cursorMark(i) + row.label + " " + valueChip(value)
	if row.unit != "" {
		line += " " + selflearnMutedStyle.Render(row.unit)
	}
	if row.footnote != nil {
		fn := row.footnote(row.intGetter(&f.snap))
		line += selflearnMutedStyle.Render("  ~  " + fn)
	}
	return line
}

// renderTextRow draws a free-text row ("Storage path  <value>") whose value is
// tail-anchored so the meaningful end of a long path stays visible; an empty,
// non-editing value shows the muted placeholder instead.
func (f *configForm[T]) renderTextRow(i int, row configRow[T], width int) string {
	labelStart := contentCol(row.indent) + 4 // align with bool-row labels
	leftPad := strings.Repeat(" ", labelStart-cursorWidth)
	head := leftPad + f.cursorMark(i) + row.label + " "

	editing := f.editing && i == f.cursor
	value := row.strGetter(&f.snap)
	if editing {
		value = f.editingBuffer
	}

	avail := max(width-lipgloss.Width(head)-1, 8)
	if value == "" && !editing {
		return head + selflearnMutedStyle.Render(kit.TruncateKeepEnd(row.placeholder, avail))
	}
	shown := kit.TruncateKeepEnd(value, avail-1) // room for the edit caret
	if editing {
		shown += "_"
	}
	return head + selflearnValueStyle.Render(shown)
}

// valueChip wraps a numeric value in chip-style brackets so it reads as
// an editable input: muted parens around an accent-bold value.
func valueChip(value string) string {
	return selflearnChipBracketStyle.Render("(") +
		selflearnValueStyle.Render(value) +
		selflearnChipBracketStyle.Render(")")
}

func (f *configForm[T]) renderSaveRow(i int, validationErr error) string {
	style := selflearnSaveButtonStyle
	if validationErr != nil {
		style = selflearnSaveButtonDisabledStyle
	}
	btn := style.Render("Save")
	tail := selflearnMutedStyle.Render("  or " + keycap("esc") + selflearnMutedStyle.Render(" to discard"))
	return f.cursorPad(i, 1) + btn + tail
}

// ── Row kinds and layout ────────────────────────────────────────────────

// rowKind discriminates the rendered row types: bool toggle, int with a
// clamped range, the save action, two header levels, a blank spacer, and
// the advanced-action hint.
type rowKind int

const (
	rowBool rowKind = iota
	rowInt
	rowBoolInt // a checkbox with an inline editable number (Create + its cadence)
	rowText    // inline-edited free-text value (e.g. the memory storage path)
	rowChoice  // one of a fixed list of values, stepped with space/enter
	rowEntry   // opens a panel-owned sub-view (e.g. the Steering Prompt editor)
	rowSave
	rowSubHeader // sub-section title (e.g. "Learn by")
	rowSpacer    // blank line
)

// configRow is one renderable row over the form's settings struct T. Fields
// unused by the row's kind stay zero.
type configRow[T any] struct {
	kind       rowKind
	label      string
	toggle     func(*T)
	boolGetter func(*T) bool
	intGetter  func(*T) int
	intSetter  func(*T, int)
	intMin     int
	intMax     int
	unit       string           // for rowInt/rowBoolInt — muted suffix after the value (e.g. "iterations", "KB")
	intLead    string           // for rowBoolInt — muted word before the value chip (e.g. "every")
	footnote   func(int) string // for rowInt — optional muted inline footnote after the label
	note       string           // for rowBool — optional muted inline hint after the label
	indent     int
	// choices/getChoice/setChoice back a rowChoice: the fixed values, which one
	// the config currently resolves to, and where a step writes the next. The
	// getter answers with the resolved value, so an unset field displays — and
	// cycles from — the default it actually behaves as.
	choices   []string
	getChoice func(*T) string
	setChoice func(*T, string)
	// entryID identifies a rowEntry to its owning panel; desc/summary render
	// the muted description and the right-aligned value hint (e.g. "built-in").
	entryID string
	desc    string
	summary func(*T) string
	// strGetter/strSetter back a rowText; placeholder shows when the value is
	// empty (e.g. "default (project store)").
	strGetter   func(*T) string
	strSetter   func(*T, string)
	placeholder string
}

// cycle steps a choice row to its next option, wrapping at the end.
func (r configRow[T]) cycle(s *T) {
	if len(r.choices) == 0 || r.getChoice == nil || r.setChoice == nil {
		return
	}
	at := 0
	current := r.getChoice(s)
	for i, c := range r.choices {
		if c == current {
			at = i
			break
		}
	}
	r.setChoice(s, r.choices[(at+1)%len(r.choices)])
}

// editable reports whether the row can hold the cursor. Derived from kind:
// the actionable kinds (toggle, edit-in-place, text, choice, sub-view entry,
// save) are editable; section headers, sub-headers, and spacers are not.
func (r configRow[T]) editable() bool {
	switch r.kind {
	case rowBool, rowInt, rowBoolInt, rowText, rowChoice, rowEntry, rowSave:
		return true
	default:
		return false
	}
}

// findEditable walks rows from start in direction step (+1 forward,
// −1 backward) until it hits an editable row. Returns fallback when
// no editable row is found in that direction — start ≥ len(rows) for
// the first-row lookup, the cursor itself for next/prev so navigation
// past the last actionable row stays put.
func findEditable[T any](rows []configRow[T], start, step, fallback int) int {
	for i := start; i >= 0 && i < len(rows); i += step {
		if rows[i].editable() {
			return i
		}
	}
	return fallback
}

// ── Styles ──────────────────────────────────────────────────────────────

// Palette: teal (Focus) is the single accent, reserved for "focus / on" — the
// cursor caret and a checked box. Everything structural is grayscale
// (Text → Muted → TextDim → Faint). Error red is the only other hue, kept for
// the rare validation message.
var (
	// Headers stay calm muted-bold caps — headers are soft signposts, not
	// shouting accents.
	selflearnSubHeaderStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Bold(true)

	selflearnMutedStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	selflearnErrorStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)

	// The one accent: cursor caret and checked box.
	selflearnCursorStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Focus).Bold(true)
	selflearnCheckStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Focus)

	// Editable numeric value inside a "(…)" chip: plain bold text — the chip
	// brackets carry the "editable" affordance, no color needed.
	selflearnValueStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text).Bold(true)

	// Two-segment scope control: the active segment is a neutral filled pill,
	// the inactive one a flat dim label.
	selflearnScopeActiveStyle = lipgloss.NewStyle().
					Background(kit.SearchBg).
					Foreground(kit.CurrentTheme.Text).
					Bold(true).
					Padding(0, 1)
	selflearnScopeIdleStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.TextDim).
				Padding(0, 1)

	// Keycap pill — neutral gray bg + bold text so it reads as a physical key.
	selflearnKeycapStyle = lipgloss.NewStyle().
				Background(kit.SearchBg).
				Foreground(kit.CurrentTheme.Text).
				Bold(true)

	// Faint wrapper for dimmed content (e.g. an unchecked action's cadence).
	selflearnFaintStyle = lipgloss.NewStyle().Faint(true)

	// Save button — a neutral filled pill; the teal cursor caret marks focus.
	// Dimmed when the snapshot fails validation.
	selflearnSaveButtonStyle = lipgloss.NewStyle().
					Background(kit.SearchBg).
					Foreground(kit.CurrentTheme.Text).
					Bold(true).
					Padding(0, 2)
	selflearnSaveButtonDisabledStyle = lipgloss.NewStyle().
						Background(kit.SearchBg).
						Foreground(kit.CurrentTheme.TextDim).
						Padding(0, 2)

	// Chip-style brackets for editable values: "(10)" with dim parens.
	selflearnChipBracketStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)

	// Right-aligned value hint on a sub-view entry row ("built-in" / "custom").
	selflearnEntrySummaryStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
)
