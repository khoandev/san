package plan

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PlanBlockFence is the info string the agent must use for a complete plan.
const PlanBlockFence = "plan"

// StepBlockFence is the info string for a step outcome.
const StepBlockFence = "plan-step"

// UpdateBlockFence is the info string for an incremental plan update.
const UpdateBlockFence = "plan-update"

// planWire is the agent-facing shape: decisions are plain sentences, while the
// stored Plan keeps each with the time it was settled.
type planWire struct {
	Title         string   `json:"title"`
	Goal          string   `json:"goal"`
	InScope       []string `json:"inScope"`
	OutOfScope    []string `json:"outOfScope"`
	Constraints   []string `json:"constraints"`
	Risks         []string `json:"risks"`
	OpenQuestions []string `json:"openQuestions"`
	Decisions     []string `json:"decisions"`
	Steps         []Step   `json:"steps"`
}

// ParsePlanBlock finds the last ```plan fenced JSON block in text and
// normalizes it. found is false when the agent produced no block at all, which
// is not an error — it may still be asking a clarifying question.
func ParsePlanBlock(text string) (Plan, bool, error) {
	body, found := lastFence(text, PlanBlockFence)
	if !found {
		return Plan{}, false, nil
	}
	var wire planWire
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		return Plan{}, true, fmt.Errorf("plan block is not valid JSON: %w", err)
	}
	if strings.TrimSpace(wire.Title) == "" {
		return Plan{}, true, fmt.Errorf("plan block has no title")
	}
	p := Plan{
		Title:         wire.Title,
		Goal:          wire.Goal,
		InScope:       wire.InScope,
		OutOfScope:    wire.OutOfScope,
		Constraints:   wire.Constraints,
		Risks:         wire.Risks,
		OpenQuestions: wire.OpenQuestions,
		Steps:         wire.Steps,
		Status:        StatusPendingApproval,
	}
	for _, d := range wire.Decisions {
		p.AddDecision(d)
	}
	p.Normalize()
	return p, true, nil
}

// Update is the incremental form of a plan: new information folds into the
// existing draft instead of replacing it wholesale.
type Update struct {
	Title         *string  `json:"title,omitempty"`
	Goal          *string  `json:"goal,omitempty"`
	InScope       []string `json:"inScope,omitempty"`
	OutOfScope    []string `json:"outOfScope,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
	Risks         []string `json:"risks,omitempty"`
	OpenQuestions []string `json:"openQuestions,omitempty"`
	Decisions     []string `json:"decisions,omitempty"`
	Steps         []Step   `json:"steps,omitempty"`
}

// ParseUpdateBlock finds the last ```plan-update block.
func ParseUpdateBlock(text string) (Update, bool, error) {
	body, found := lastFence(text, UpdateBlockFence)
	if !found {
		return Update{}, false, nil
	}
	var u Update
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		return Update{}, true, fmt.Errorf("plan-update block is not valid JSON: %w", err)
	}
	return u, true, nil
}

// Merge folds u into p. Lists of settled facts append; scope lists replace;
// steps replace when the update carries any.
func Merge(p *Plan, u Update) {
	if p == nil {
		return
	}
	if u.Title != nil {
		p.Title = strings.TrimSpace(*u.Title)
	}
	if u.Goal != nil {
		p.Goal = strings.TrimSpace(*u.Goal)
	}
	if u.InScope != nil {
		p.InScope = cloneStrings(u.InScope)
	}
	if u.OutOfScope != nil {
		p.OutOfScope = cloneStrings(u.OutOfScope)
	}
	if u.Constraints != nil {
		p.Constraints = cloneStrings(u.Constraints)
	}
	if u.Risks != nil {
		p.Risks = append(p.Risks, u.Risks...)
	}
	if u.OpenQuestions != nil {
		p.OpenQuestions = append(p.OpenQuestions, u.OpenQuestions...)
	}
	if u.Steps != nil {
		p.Steps = make([]Step, len(u.Steps))
		for i := range u.Steps {
			step := u.Steps[i]
			step.Normalize()
			p.Steps[i] = step
		}
	}
	for _, d := range u.Decisions {
		p.AddDecision(d)
	}
	p.Normalize()
	p.UpdatedAt = time.Now()
}

// StepOutcome is the parsed value of a ```plan-step block.
type StepOutcome struct {
	Result StepResult
	Found  bool
}

// ParseStepBlock finds the last ```plan-step block in text.
func ParseStepBlock(text string) (StepResult, bool, error) {
	body, found := lastFence(text, StepBlockFence)
	if !found {
		return StepResult{}, false, nil
	}
	var r StepResult
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return StepResult{}, true, fmt.Errorf("plan-step block is not valid JSON: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(string(r.Status))) {
	case "completed", "complete", "done", "success":
		r.Status = StepCompleted
	case "blocked", "failed", "failure", "error":
		r.Status = StepBlocked
	case "skipped", "skip":
		r.Status = StepSkipped
	default:
		r.Status = StepBlocked
		if strings.TrimSpace(r.Reason) == "" {
			r.Reason = fmt.Sprintf("unknown step status %q", r.Status)
		}
	}
	return r, true, nil
}

// lastFence returns the body of the last fenced code block whose info string
// matches fence. Indented closing fences are accepted; an unterminated block
// is ignored.
func lastFence(text, fence string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	open := -1
	body := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if open < 0 {
			if info, ok := fenceInfo(trimmed); ok && strings.EqualFold(info, fence) {
				open = i
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			body = strings.Join(lines[open+1:i], "\n")
			open = -1
		}
	}
	if open >= 0 {
		return "", false
	}
	return body, body != ""
}

// fenceInfo returns the info string of a line that opens a ``` fence.
func fenceInfo(line string) (string, bool) {
	if !strings.HasPrefix(line, "```") {
		return "", false
	}
	info := strings.TrimSpace(strings.TrimPrefix(line, "```"))
	if info == "" {
		return "", false
	}
	return info, true
}
