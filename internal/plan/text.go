package plan

import (
	"fmt"
	"strings"
)

// The Execution Panel's "Edit plan" button edits the plan as text, in memory.
// A plan is never written to a file of its own, so the editor cannot be an
// external one: the panel owns a textarea seeded by FormatText, and ParseText
// turns the result back into a Plan. The format is one field per heading and
// one line per item, which keeps both directions total:
//
//	title: split the auth package
//	goal: one package owns credential handling
//
//	in-scope:
//	- internal/auth
//	out-of-scope:
//	- the CLI
//
//	steps:
//	- Split the package | move files into internal/auth | go build ./... ; go test ./internal/auth
//
// A step line is "title | objective | verification"; the objective and the
// verification criteria are optional, and criteria are separated by ";".
func FormatText(p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "title: %s\n", oneLine(p.Title))
	fmt.Fprintf(&b, "goal: %s\n", oneLine(p.Goal))
	writeSection(&b, "in-scope", p.InScope)
	writeSection(&b, "out-of-scope", p.OutOfScope)
	writeSection(&b, "constraints", p.Constraints)
	writeSection(&b, "risks", p.Risks)
	writeSection(&b, "open-questions", p.OpenQuestions)
	writeSection(&b, "decisions", decisionTexts(p.Decisions))
	b.WriteString("\nsteps:\n")
	for _, step := range p.Steps {
		fmt.Fprintf(&b, "- %s | %s | %s\n",
			oneLine(step.Title), oneLine(step.Objective), oneLine(strings.Join(step.Verification, "; ")))
	}
	return b.String()
}

// ParseText is FormatText's inverse. It refuses what it cannot represent
// rather than silently dropping the line, because an edit the user cannot see
// is worse than an error under the editor.
func ParseText(text string) (Plan, error) {
	var p Plan
	section := ""
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // blank separator or a comment the editor tolerates
		}
		if item, ok := cutListItem(line); ok {
			if err := appendItem(&p, section, item); err != nil {
				return Plan{}, err
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Plan{}, fmt.Errorf("line %q is neither a heading nor a list item", line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "title":
			p.Title, section = value, ""
		case "goal":
			p.Goal, section = value, ""
		case "in-scope", "in scope", "scope":
			section = "in-scope"
		case "out-of-scope", "out of scope":
			section = "out-of-scope"
		case "constraints":
			section = "constraints"
		case "risks":
			section = "risks"
		case "open-questions", "open questions":
			section = "open-questions"
		case "decisions":
			section = "decisions"
		case "steps":
			section = "steps"
		default:
			return Plan{}, fmt.Errorf("unknown field %q", key)
		}
		// "in-scope: internal/auth" — a heading carrying its first item.
		if value != "" && section != "" {
			if err := appendItem(&p, section, value); err != nil {
				return Plan{}, err
			}
		}
	}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	p.Normalize()
	return p, nil
}

// MergeEdit folds an edited plan back onto the plan it replaces: the identity,
// the progress journal, and the step IDs survive, so the edit changes what the
// plan says without pretending it is a different plan.
func MergeEdit(base, edited Plan) Plan {
	out := edited
	out.ID = base.ID
	out.CreatedAt = base.CreatedAt
	out.Progress = append([]LogEntry(nil), base.Progress...)
	for i := range out.Steps {
		out.Steps[i].Status = StepPending
		if i < len(base.Steps) {
			out.Steps[i].ID = base.Steps[i].ID
		}
	}
	out.Normalize()
	return out
}

func cutListItem(line string) (string, bool) {
	for _, marker := range []string{"- ", "* "} {
		if strings.HasPrefix(line, marker) {
			return strings.TrimSpace(strings.TrimPrefix(line, marker)), true
		}
	}
	return "", false
}

func appendItem(p *Plan, section, item string) error {
	switch section {
	case "in-scope":
		p.InScope = append(p.InScope, item)
	case "out-of-scope":
		p.OutOfScope = append(p.OutOfScope, item)
	case "constraints":
		p.Constraints = append(p.Constraints, item)
	case "risks":
		p.Risks = append(p.Risks, item)
	case "open-questions":
		p.OpenQuestions = append(p.OpenQuestions, item)
	case "decisions":
		p.AddDecision(item)
	case "steps":
		p.Steps = append(p.Steps, parseStepLine(item))
	case "":
		return fmt.Errorf("list item %q is outside any section", item)
	}
	return nil
}

func parseStepLine(item string) Step {
	parts := strings.Split(item, "|")
	step := Step{Title: strings.TrimSpace(parts[0])}
	if len(parts) > 1 {
		step.Objective = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		for _, criterion := range strings.Split(parts[2], ";") {
			if criterion = strings.TrimSpace(criterion); criterion != "" {
				step.Verification = append(step.Verification, criterion)
			}
		}
	}
	return step
}

func writeSection(b *strings.Builder, name string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", name)
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", oneLine(item))
	}
}

func decisionTexts(decisions []Decision) []string {
	out := make([]string, 0, len(decisions))
	for _, d := range decisions {
		if text := oneLine(d.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// oneLine keeps one item on one line so the format stays parseable; a newline
// inside an item is a paste, not structure.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
