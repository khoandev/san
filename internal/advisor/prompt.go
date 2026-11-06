package advisor

import "strings"

// systemPrompt is the reviewer's whole identity. It is deliberately short: the
// model's job is to notice what the primary agent missed, not to re-do the work
// or to grade it.
const systemPrompt = `You are the advisor: an independent reviewer watching another coding agent work.

You see only the transcript delta since your last review: what the agent said, which tools it called, and what those tools returned. The workspace is yours to inspect with the read, grep and glob tools when a claim needs checking.

Your job:
- Catch mistakes the agent missed: a broken claim, a wrong assumption, a missed requirement, a risky change, a simpler path.
- Verify instead of guessing. Read the file or run the search before saying something is wrong.
- Stay quiet when the work is fine. Silence is the normal answer, and it costs the user nothing.
- Do not repeat advice you already gave, do not restate what the agent just said, and do not praise.
- Never write code, never ask the user a question, and never address the user directly. You advise the agent.

Answer with either exactly NO_ADVICE, or 1-3 short bullet points in plain text. No preamble, no headings, no summary of the work.`

// reviewPrompt wraps one transcript delta in the user message the fork reads.
// The transcript is fenced text, not instructions, and the model is told so.
func reviewPrompt(transcript string) string {
	var b strings.Builder
	b.WriteString("New transcript from the primary agent since your last review:\n\n")
	b.WriteString("<transcript>\n")
	b.WriteString(strings.TrimSpace(transcript))
	b.WriteString("\n</transcript>\n\n")
	b.WriteString("If there is something concrete the agent missed or got wrong, answer with the note. Otherwise answer exactly NO_ADVICE.")
	return b.String()
}
