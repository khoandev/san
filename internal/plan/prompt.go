package plan

import (
	"fmt"
	"strings"
)

// PlanModeRules is the Plan Mode contract. It rides on the system prompt as its
// own section, so a turn costs it once instead of carrying it, and the turns
// after that inherit it through the cached prompt rather than through the
// history. Its own section is what keeps it alive across a persona swap, which
// replaces the rest of the rules part wholesale. Keep it within the byte budget
// its test enforces: every rule here is prompt paid on every planning cache
// miss.
const PlanModeRules = `You are in PLAN MODE. This mode is read-only.

Rules:
- Read, search, inspect and analyse the workspace, and ask questions.
  No writes, side effects, installs or git mutations: the host blocks them.
- What the host allows, aim here first: Read; inspection in Bash (rg/grep/find/
  ls/cat/awk, sed -n, xargs, go doc, gofmt -l, git status/log/diff/show, gh api
  GET, curl GET, brew/npm/pip list queries, tar -t, unzip -l, man/less/more,
  docker ps/inspect/logs, kubectl get/describe/logs, ps/lsof/dig/netstat); web
  search/fetch; skills; read-only MCP tools. Refused: Write, Edit, installs, go
  build/test, curl/wget writes, git/docker/kubectl mutations, any redirect into
  a file.
- Delegation is investigation only: the Agent tool with a read-only definition
  is the exception — explore covers this codebase and a dependency's
  upstream docs and APIs. Any definition that can write (task, quick) is
  refused, so name a read-only one rather than retrying with another.
- Refusals are final: read instead, narrow the pipeline, or ask.
- Gather context first: goal, scope, constraints, risks, questions, steps.
- Every step is one verifiable unit, small enough to finish in one turn:
  write them as tasks a person can check off, one outcome each, never a
  chapter heading that hides several.
- Verification scales with the step: it names the narrowest command that proves
  it — the tests of the packages it touched. The full suite belongs to the last
  step.
- The plan's steps are the user's todo list:
  never create or update tasks yourself.
- If the request is ambiguous, ask 3 to 5 focused questions before planning,
  and ask before writing the plan rather than guess.
- Reply with one fenced plan block:

` + "```plan" + `
{
  "title": "short name", "goal": "what success means",
  "inScope": ["..."], "outOfScope": ["..."], "constraints": ["..."],
  "risks": ["..."], "openQuestions": ["..."], "decisions": ["settled ones"],
  "steps": [
    { "title": "one unit of work", "detail": "how",
      "objective": "this step's outcome",
      "verification": ["narrowest command that proves it"] }
  ]
}
` + "```" + `

- Do not implement after emitting the plan; the user approves it in the
  Execution Panel.
- A changed plan comes back as one fenced plan-update block with only the
  changed fields.`

// StepOptions carries the execution policy one approved step runs under.
type StepOptions struct {
	// FastVerify scopes verification to the step: it proves itself with the
	// narrowest command covering what it touched, and the full suite is spent
	// once, on the plan's last step. Off restores "prove every criterion above"
	// on every step, which repeats the whole suite per step.
	FastVerify bool
}

// StepPrompt is the instruction for one approved step. It deliberately names
// only that step: the executor advances one verified step at a time, never a
// whole plan in one turn.
func StepPrompt(p Plan, step Step, index int, opts StepOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<plan-execution plan=%q step=%q>\n", p.Title, fmt.Sprintf("%d/%d", index+1, len(p.Steps)))
	fmt.Fprintf(&b, "Plan goal: %s\n", strings.TrimSpace(p.Goal))
	fmt.Fprintf(&b, "Current step: %s\n", step.Title)
	if step.Detail != "" {
		fmt.Fprintf(&b, "How: %s\n", step.Detail)
	}
	if step.Objective != "" {
		fmt.Fprintf(&b, "Objective: %s\n", step.Objective)
	}
	if len(step.Verification) > 0 {
		b.WriteString("Verification criteria:\n")
		for _, v := range step.Verification {
			fmt.Fprintf(&b, "- %s\n", v)
		}
	}
	if len(p.Constraints) > 0 {
		b.WriteString("Constraints:\n")
		for _, c := range p.Constraints {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	b.WriteString("\nExecute ONLY the current step. Do not start later steps.\n")
	b.WriteString(verifyRule(index == len(p.Steps)-1, opts.FastVerify))
	b.WriteString("The plan's steps are the todo list above the input: do not create or update tasks.\n")
	b.WriteString(`If the workspace no longer matches the plan, stop and report blocked.
Finish with exactly one fenced plan-step block:

` + "```plan-step" + `
{
  "status": "completed" | "blocked" | "skipped",
  "result": "what changed, in one or two sentences",
  "files": ["paths you changed"],
  "evidence": ["the command, test, or observation that proves each criterion"],
  "reason": "why it is blocked (only when blocked)"
}
` + "```" + `
</plan-execution>`)
	return b.String()
}

// verifyRule states what proves the step. Fast verify spends the full suite
// once, on the last step, so a five-step plan costs one full run instead of
// five; with the policy off every step proves every criterion, which is what
// san did before the setting existed.
func verifyRule(last, fast bool) string {
	if !fast {
		return "Verify the result against every criterion above before reporting.\n"
	}
	if last {
		return "This is the last step: run the full suite once — `make test`, plus\n" +
			"`go test ./tests/integration/...` — and report that run as evidence.\n"
	}
	return "Verify with the narrowest command that proves this step — the tests of the\n" +
		"packages you touched; do not run the full suite or integration tests here.\n"
}

// StepDisplay is the short row the transcript shows for one step prompt.
func StepDisplay(p Plan, step Step, index int) string {
	return fmt.Sprintf("▸ Plan step %d/%d — %s", index+1, len(p.Steps), step.Title)
}

// Summary renders a compact multi-line plan for notices and the panel header.
func Summary(p Plan) string {
	completed, handled, blocked, total := p.Counts()
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nGoal: %s\nProgress: %d/%d completed", p.Title, p.Goal, completed, total)
	if handled > completed {
		fmt.Fprintf(&b, " (%d handled)", handled)
	}
	if blocked > 0 {
		fmt.Fprintf(&b, " · %d blocked", blocked)
	}
	return b.String()
}

// OpenQuestionsBlock renders questions for a transcript notice.
func OpenQuestionsBlock(questions []string) string {
	if len(questions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Open questions:\n")
	for _, q := range questions {
		fmt.Fprintf(&b, "- %s\n", q)
	}
	return strings.TrimRight(b.String(), "\n")
}
