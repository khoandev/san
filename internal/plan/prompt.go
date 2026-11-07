package plan

import (
	"fmt"
	"strings"
)

// PlanModeRules is appended to every user request while Plan Mode is active.
// The behavior rules are duplicated here (rather than only in a system
// prompt) so they survive persona overrides, compaction, and provider swaps.
const PlanModeRules = `You are in PLAN MODE. This mode is read-only.

Rules:
- You may read files, search, inspect the workspace, analyse the architecture,
  and ask the user clarifying questions. Nothing else.
- Do NOT modify or create files, do NOT run commands with side effects, do NOT
  install packages, do NOT change git state, and do NOT call any tool that
  writes to the workspace. The host blocks these calls; a rejected call is not
  permission to try another route.
- What the host does allow, so aim here first: Read; inspection commands in
  Bash including rg/grep/find/ls/cat/awk, sed -n, xargs, go doc, gofmt -l,
  read-only git (status/log/diff/show), gh api (GET only) and gh repo/pr/
  issue/search, curl (GET — no -d/--data/--json/-X POST), brew list/info/
  search, npm/yarn/pnpm list/info/outdated, pip list/show, tar -t (list),
  unzip -l, docker ps/inspect/logs, kubectl get/describe/logs, ps/lsof/dig/
  nslookup/ping/netstat and other network-inspection commands, man/less/more;
  web search and fetch; skill instructions; read-only MCP tools.
  Refused: Write, Edit, package installs, go build/test, curl with a body or
  write method, wget without -O -, brew install/upgrade, npm install/run,
  docker run/exec/rm, kubectl apply/delete, and any redirect into a file.
- Delegation is the one exception, and it is for investigation only: call the
  Agent tool with a read-only definition — explore for this codebase, librarian
  for a dependency's upstream docs and APIs. Use it when the reading would
  flood your context; the worker reports findings, it never edits. Any
  definition that can write (task, designer, or a user's own edit agent) is
  refused, so name a read-only definition rather than retrying with another.
- When a call is refused, do not retry it in another shape. Read the file, run
  a narrower pipeline, or ask the user — the refusal means the workspace cannot
  change here, not that another phrasing will pass.
- Gather enough context first. Identify the goal, in-scope and out-of-scope
  work, technical constraints, risks, open questions, and the ordered steps.
- If the request is ambiguous, ask 3 to 5 focused questions before planning.
- When the plan is ready, reply with exactly one fenced block whose info string
  is plan, containing JSON shaped like this:

` + "```plan" + `
{
  "title": "short name",
  "goal": "what success means",
  "inScope": ["..."],
  "outOfScope": ["..."],
  "constraints": ["..."],
  "risks": ["..."],
  "openQuestions": ["..."],
  "decisions": ["choices already settled"],
  "steps": [
    {
      "title": "one ordered unit of work",
      "detail": "how it will be done",
      "objective": "the concrete outcome of this step",
      "verification": ["how the step will be proven"]
    }
  ]
}
` + "```" + `

- Do not start implementing after emitting the plan block. The user reviews and
  approves it in the Execution Panel.
- When new information changes the plan, reply with one fenced plan-update
  block containing only the changed fields (title, goal, inScope, outOfScope,
  constraints, risks, openQuestions, decisions, steps).`

// PlanRequest wraps a user request with the Plan Mode protocol.
func PlanRequest(request string) string {
	request = strings.TrimSpace(request)
	if request == "" {
		return "<plan-mode>\n" + PlanModeRules + "\n</plan-mode>"
	}
	return "<plan-mode>\n" + PlanModeRules + "\n\nUser request:\n" + request + "\n</plan-mode>"
}

// StepPrompt is the instruction for one approved step. It deliberately names
// only that step: the executor advances one verified step at a time, never a
// whole plan in one turn.
func StepPrompt(p Plan, step Step, index int) string {
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
	b.WriteString(`
Execute ONLY the current step. Do not start later steps.
Verify the result against every criterion above before reporting.
If the workspace no longer matches the plan, stop and report blocked.
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
