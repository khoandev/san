// Package conv: status-bar components — context bar, color tiers,
// compressions badge, and the responsive segment allocator. Pure
// functions over primitives; the orchestrator (RenderModeStatus) wires
// them to env state.
package conv

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// Threshold percentages for the 4 PRD §7.2 color tiers, of the prompt budget:
// auto-compaction fires at 100%.
const (
	pctGood     = 50.0
	pctWarn     = 80.0
	pctCritical = 90.0

	// contextBarWidth is the cell count for the visual bar (PRD §7.1).
	contextBarWidth = 10
)

// contextTier classifies a context-window fill percentage into one of
// the 4 PRD §7.2 tiers. Off-by-one preserved: 80 itself falls into warn,
// only strictly-greater-than-80 is bad. ≥90 is critical.
type contextTier int

const (
	tierNone     contextTier = iota // pct unknown — denominator missing
	tierGood                        // [0, 50]    healthy
	tierWarn                        // (50, 80]   watch
	tierBad                         // (80, 90)   pressure
	tierCritical                    // [90, 100+] imminent compression
)

// classifyContextTier maps a percentage to its tier. Defensive for
// out-of-range inputs: pct < 0 returns tierGood, pct > 100 returns
// tierCritical. Renderers should still clamp for clean display.
func classifyContextTier(pct float64) contextTier {
	switch {
	case pct <= pctGood: // pct ≤ 50
		return tierGood
	case pct <= pctWarn: // 50 < pct ≤ 80
		return tierWarn
	case pct < pctCritical: // 80 < pct < 90
		return tierBad
	default: // pct ≥ 90
		return tierCritical
	}
}

// style resolves a tier to a lipgloss style composed from existing
// theme tokens (per project decision: no new theme infrastructure).
func (t contextTier) style() lipgloss.Style {
	switch t {
	case tierGood:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
	case tierWarn:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
	case tierBad:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
	case tierCritical:
		// Critical = Error + Bold. Distinct from "bad" without adding a
		// new theme token.
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error).Bold(true)
	default: // tierNone
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	}
}

// RenderContextBar renders the 10-cell bar with a percentage label:
//
//	"[██████░░░░] 71%"   normal case
//	"[----------] --"    when limit is 0 (unknown)
//
// The percentage is rounded to an integer at this layer (PRD §4.2); the
// engine itself never rounds. `used` is clamped to [0, limit] before
// computing pct so callers cannot accidentally render negatives or >100%.
func RenderContextBar(used, limit int) string {
	if limit <= 0 {
		dim := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
		return dim.Render("[" + strings.Repeat("-", contextBarWidth) + "] --")
	}
	if used < 0 {
		used = 0
	}
	pct := float64(used) / float64(limit) * 100
	if pct > 100 {
		pct = 100
	}
	filled := min(
		// round to nearest cell
		int((pct/100)*float64(contextBarWidth)+0.5), contextBarWidth)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", contextBarWidth-filled)
	style := classifyContextTier(pct).style()
	return style.Render(fmt.Sprintf("[%s] %d%%", bar, int(pct+0.5)))
}

// contextLabel renders the muted "used/limit" segment. An empty limitText
// renders the limit as "--" (unknown).
func contextLabel(usedText, limitText string) string {
	muted := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	if limitText == "" {
		return muted.Render(usedText + "/--")
	}
	return muted.Render(usedText + "/" + limitText)
}

// RenderContextLabel renders the "X/Y" segment using compact
// humanized numbers (PRD §7.4). Limit renders as "--" when unknown.
func RenderContextLabel(used, limit int) string {
	if limit <= 0 {
		return contextLabel(kit.FormatTokenCount(used), "")
	}
	return contextLabel(kit.FormatTokenCount(used), kit.FormatTokenCount(limit))
}

// compressionBadgeStyle escalates color with count (PRD §7.5):
//
//	<5     muted
//	5–9    warn
//	≥10    error
func compressionBadgeStyle(n int) lipgloss.Style {
	switch {
	case n >= 10:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
	case n >= 5:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
	default:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	}
}

// RenderCompressionsBadge returns the "compacted ×N" badge or "" when n ≤ 0.
// Plain text (no emoji) keeps the status line aligned across terminals; color
// escalates with the count per compressionBadgeStyle.
func RenderCompressionsBadge(n int) string {
	if n <= 0 {
		return ""
	}
	return compressionBadgeStyle(n).Render(fmt.Sprintf("compacted ×%d", n))
}

// statusSegment is one keep-or-drop unit of the status line's right cluster.
// Under width pressure whole segments are dropped, never truncated mid-text.
type statusSegment struct {
	text     string // already-rendered (styled) text
	priority int    // 1 = most important (dropped last); larger drops first
}

// fitStatusSegments returns the segments that fit within maxWidth when joined
// by a separator of sepWidth visible columns, preserving their caller-supplied
// order. Under width pressure it drops the highest-priority-number segments
// first; survivors keep the original order so the layout still reads naturally.
func fitStatusSegments(segments []statusSegment, maxWidth, sepWidth int) []string {
	if maxWidth <= 0 || len(segments) == 0 {
		return nil
	}

	// Decide keep/drop in priority order (most important first) without
	// disturbing the original order the survivors are emitted in.
	byPriority := make([]int, len(segments))
	for i := range byPriority {
		byPriority[i] = i
	}
	sort.SliceStable(byPriority, func(a, b int) bool {
		return segments[byPriority[a]].priority < segments[byPriority[b]].priority
	})

	keep := make([]bool, len(segments))
	remaining := maxWidth
	kept := 0
	for _, i := range byPriority {
		need := lipgloss.Width(segments[i].text)
		if kept > 0 {
			need += sepWidth // every segment after the first costs a separator
		}
		if need > remaining {
			continue
		}
		keep[i] = true
		remaining -= need
		kept++
	}

	out := make([]string, 0, kept)
	for i, seg := range segments {
		if keep[i] {
			out = append(out, seg.text)
		}
	}
	return out
}

// OperationModeParams holds the parameters needed for rendering mode status.
type OperationModeParams struct {
	Mode              setting.OperationMode
	ContextTokens     int // how much of the window the conversation fills: last prompt plus reply
	ContextWindow     int // the model's context window, what the label counts against; 0 = unknown
	ContextBudget     int // prompt size at which auto-compaction fires; 0 = unknown
	ModelName         string
	StatusMessage     string
	SubscriptionUsage string // account limits; optional, yields first on narrow screens
	ConversationCost  llm.CostTotal
	Compressions      int  // session compact count, drives the "compacted ×N" badge
	ShowContextBar    bool // render the visual [██████░░░░] 71% bar (opt-in)
	Width             int
	JudgeApprovals    int      // judge approvals this session, shown next to the mode
	JudgeEscalations  int      // judge escalations to the user this session
	AutopilotThinking bool     // the copilot is mid-decision — show "thinking…" on the mode indicator
	Group             string   // this session's group, "" outside one
	GroupSelf         string   // this session's member name in it
	GroupHold         bool     // this member holds the others' messages
	ModeHint          bool     // show "(shift+tab)": briefly, after start or a switch
	GroupWaiting      int      // member messages waiting for the person's next input
	GroupApproval     []string // other members whose turn waits on their user's approval
	// PlanMode is the Plan Mode badge: "plan" while the session is read-only,
	// "execute" while it runs an approved plan, "" otherwise. The operation
	// mode cannot express either posture.
	PlanMode string
	// Advisor is the advisor's badge: off renders nothing, otherwise which
	// model reviews, whether a review is running, and what the last one found.
	Advisor AdvisorBadge
	// AgentUsage is the per-role inference request count: role name and the
	// number of requests it spent this session. Empty renders nothing.
	AgentUsage []AgentUsageCount
}

// AgentUsageCount is one agent role's accumulated request count on the status
// line. The name is the role or definition name; the renderer owns the glyph.
type AgentUsageCount struct {
	Name  string
	Count int
}

// RenderAgentUsage renders the per-role counts as "role count" pairs joined by
// the status line's separator, in the order the caller supplies. Roles that ran
// nothing are omitted, and an empty result means the status line carries no
// usage segment at all.
//
// No icon precedes a role: the past glyph map needed one mark per agent, three
// of them outside the symbol registry, and the names read clearer than a legend
// the reader has to learn.
func RenderAgentUsage(entries []AgentUsageCount) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if entry.Count <= 0 || name == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d", name, entry.Count))
	}
	if len(parts) == 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Render(strings.Join(parts, " · "))
}

// AdvisorBadge is the advisor's status-bar state. The zero value renders
// nothing, which is what a disabled or unconfigured advisor looks like.
type AdvisorBadge struct {
	Enabled   bool   // the advisor is on for this session
	HasModel  bool   // a review model is resolved; false with Enabled shows why nothing runs
	Reviewing bool   // a review is in flight
	Reviews   int    // reviews finished in this conversation
	LastNote  string // the last note injected, "" when the last review found nothing
	LastError string // the last review's failure
	LastSkip  string // why the last completed turn was not reviewed
}

// RenderModeStatus renders the combined mode status line. It is always one row:
// both halves drop their least important pieces rather than running past Width.
func RenderModeStatus(params OperationModeParams) string {
	left := renderLeftCluster(params)
	if params.Width <= 0 {
		return left
	}
	// The cluster gets what the left run leaves, minus the gap, so it drops its
	// least important segments instead of running off the edge.
	cluster := params
	cluster.Width = params.Width - lipgloss.Width(left) - 3
	right := renderStatusCluster(cluster)
	if right == "" {
		return left
	}

	gap := max(2, params.Width-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return left + strings.Repeat(" ", gap) + right
}

// renderLeftCluster composes the operation mode, the optional shift+tab hint,
// the Plan Mode badge and the group into one left-aligned run. Each piece is a
// statusSegment: under width pressure the group goes first, then the plan badge,
// then the mode — the last of which is the row's reason to exist. The mode
// indicator carries the row's two-space indent, so survivors that do not start
// with it get the same indent added.
func renderLeftCluster(params OperationModeParams) string {
	mode, hint := modeIndicator(params.Mode, params.JudgeApprovals, params.JudgeEscalations, params.AutopilotThinking)
	if params.ModeHint {
		mode += hint
	}

	segments := make([]statusSegment, 0, 3)
	if mode != "" {
		segments = append(segments, statusSegment{text: mode, priority: 1})
	}
	if badge := renderPlanBadge(params.PlanMode); badge != "" {
		segments = append(segments, statusSegment{text: badge, priority: 2})
	}
	if group := renderGroupStatus(params); group != "" {
		segments = append(segments, statusSegment{text: group, priority: 3})
	}
	if len(segments) == 0 {
		return ""
	}

	sep := " · "
	survivors := make([]string, 0, len(segments))
	if params.Width > 0 {
		// Two columns are always spent on the row indent, whether the mode
		// indicator supplies it or the fallback below adds it.
		fitted := fitStatusSegments(segments, max(1, params.Width-2), lipgloss.Width(sep))
		for _, text := range fitted {
			survivors = append(survivors, text)
		}
	} else {
		for _, seg := range segments {
			survivors = append(survivors, seg.text)
		}
	}
	if len(survivors) == 0 {
		return ""
	}
	out := strings.Join(survivors, sep)
	if !strings.HasPrefix(out, "  ") {
		out = "  " + out
	}
	return out
}

// renderPlanBadge returns the Plan Mode badge alone — "plan" while the session
// is read-only, "execute" while it runs an approved plan, "" otherwise.
func renderPlanBadge(planMode string) string {
	var icon, label string
	var clr kit.AdaptiveColor
	switch planMode {
	case "plan":
		icon, label, clr = kit.CurrentSymbols.PlanBadge, " plan", kit.CurrentTheme.Focus
	case "execute":
		icon, label, clr = kit.CurrentSymbols.ExecuteBadge, " execute", kit.CurrentTheme.Success
	default:
		return ""
	}
	return lipgloss.NewStyle().Foreground(clr).Render(icon + label)
}

// renderAdvisorBadge renders the advisor's state as one short label. A badged
// review is never silent: running, noted, checked, failed and skipped each read
// differently, and an enabled advisor with no model names the missing piece.
func renderAdvisorBadge(b AdvisorBadge) string {
	if !b.Enabled {
		return ""
	}
	var label string
	var clr kit.AdaptiveColor
	switch {
	case !b.HasModel:
		label, clr = "advisor · no model", kit.CurrentTheme.Muted
	case b.Reviewing:
		label, clr = "advisor · reviewing", kit.CurrentTheme.Focus
	case b.LastError != "":
		label, clr = "advisor · error", kit.CurrentTheme.Warning
	case b.LastNote != "":
		label, clr = "advisor · noted", kit.CurrentTheme.Success
	case b.Reviews > 0:
		label, clr = "advisor · checked", kit.CurrentTheme.Muted
	case b.LastSkip != "":
		label, clr = "advisor · "+b.LastSkip, kit.CurrentTheme.Muted
	default:
		label, clr = "advisor", kit.CurrentTheme.Muted
	}
	return lipgloss.NewStyle().Foreground(clr).Render(label)
}

// renderGroupStatus reads "◇ shop/api", "· hold" when it holds messages, then,
// in amber, whatever waits on the person: held messages, which scroll out of
// sight, and members stuck on approval.
func renderGroupStatus(p OperationModeParams) string {
	if p.Group == "" {
		return ""
	}
	parts := []string{fmt.Sprintf("%s %s/%s", kit.CurrentSymbols.GroupMark, p.Group, p.GroupSelf)}
	if p.GroupHold {
		parts = append(parts, "hold")
	}
	quiet := len(parts) // what is said before anything waits on the person
	if p.GroupWaiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting", p.GroupWaiting))
	}
	switch n := len(p.GroupApproval); {
	case n == 1:
		parts = append(parts, p.GroupApproval[0]+" needs approval")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%s +%d need approval", p.GroupApproval[0], n-1))
	}
	color := kit.CurrentTheme.Muted
	if len(parts) > quiet {
		color = kit.CurrentTheme.Warning
	}
	return lipgloss.NewStyle().Foreground(color).Render(strings.Join(parts, " · "))
}

// renderStatusCluster composes the status line's right-hand cluster, in
// display order: model name, the advisor badge, the per-role request counts,
// optional transient status message, the numeric "X/Y" context label, the
// optional visual context bar, the optional compressions badge, and the
// optional cost. Each piece is a statusSegment with a drop priority;
// fitStatusSegments drops the least important first when the terminal is too
// narrow to hold them all.
//
// The advisor badge lives here rather than beside the mode because it can grow
// ("advisor · 3 waiting · 2 need approval"): on the right it shares the one
// drop-priority budget instead of pushing the mode off a narrow line.
func renderStatusCluster(p OperationModeParams) string {
	// With no model there is nothing to annotate: a lone context label or
	// advisor badge on the right would read as a stray fragment. Keep the old
	// silence unless the caller has something real to say.
	if p.ModelName == "" && p.StatusMessage == "" && !p.Advisor.Enabled && RenderAgentUsage(p.AgentUsage) == "" {
		return ""
	}
	muted := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	sep := muted.Render(" · ")

	// Priority 1 = most important (dropped last). The model name is dropped
	// last; usage and the advisor are reference material, so they give way
	// first — the model and the context fill are what the line is for.
	var segments []statusSegment
	if p.ModelName != "" {
		segments = append(segments, statusSegment{text: muted.Render(p.ModelName), priority: 1})
	}
	if badge := renderAdvisorBadge(p.Advisor); badge != "" {
		segments = append(segments, statusSegment{text: badge, priority: 6})
	}
	if usage := RenderAgentUsage(p.AgentUsage); usage != "" {
		segments = append(segments, statusSegment{text: usage, priority: 5})
	}
	if p.StatusMessage != "" {
		segments = append(segments, statusSegment{text: muted.Render(p.StatusMessage), priority: 2})
	}

	// The numeric label always renders — it falls back to "X/--" when the
	// limit is unknown, so the slot stays visible instead of silently hiding.
	segments = append(segments, statusSegment{
		text:     RenderContextLabel(p.ContextTokens, p.ContextWindow),
		priority: 4,
	})

	// The visual bar is opt-in (off by default). When shown it also carries
	// the auto-compact hint as a near-full warning.
	if p.ShowContextBar {
		bar := RenderContextBar(p.ContextTokens, p.ContextWindow)
		if p.ContextBudget > 0 {
			if hint := compactStatusHint(float64(p.ContextTokens) / float64(p.ContextBudget) * 100); hint != "" {
				bar += sep + muted.Render(hint)
			}
		}
		segments = append(segments, statusSegment{text: bar, priority: 5})
	}

	if badge := RenderCompressionsBadge(p.Compressions); badge != "" {
		segments = append(segments, statusSegment{text: badge, priority: 6})
	}
	if !p.ConversationCost.IsZero() {
		segments = append(segments, statusSegment{text: muted.Render(kit.FormatCostTotal(p.ConversationCost)), priority: 7})
	}

	if p.SubscriptionUsage != "" {
		segments = append(segments, statusSegment{text: muted.Render(p.SubscriptionUsage), priority: 8})
	}
	if len(segments) == 0 {
		return ""
	}
	survivors := fitStatusSegments(segments, p.Width, lipgloss.Width(sep))
	return strings.Join(survivors, sep)
}

func compactStatusHint(percent float64) string {
	switch {
	case percent > pctWarn:
		return "auto-compact near"
	default:
		return ""
	}
}

// RenderOperationModeIndicator returns the mode status indicator for auto-accept, autopilot, or YOLO mode.
func RenderOperationModeIndicator(mode setting.OperationMode, judgeApprovals, judgeEscalations int, autopilotThinking bool) string {
	label, hint := modeIndicator(mode, judgeApprovals, judgeEscalations, autopilotThinking)
	return label + hint
}

// modeIndicator returns the mode label and, apart, its shift+tab hint, which
// the status line shows only for a moment.
func modeIndicator(mode setting.OperationMode, judgeApprovals, judgeEscalations int, autopilotThinking bool) (label, hint string) {
	var icon string
	var clr kit.AdaptiveColor

	switch mode {
	case setting.ModeAutoAccept:
		icon = kit.CurrentSymbols.AutoAccept
		label = " accept edits"
		clr = kit.CurrentTheme.Success
	case setting.ModeAutoPilot:
		icon = kit.CurrentSymbols.AutoAccept
		label = " autopilot"
		clr = kit.CurrentTheme.Warning
	case setting.ModeBypassPermissions:
		icon = kit.CurrentSymbols.AutoAccept
		label = " YOLO"
		clr = kit.CurrentTheme.Yolo
	default:
		return "", ""
	}

	if mode == setting.ModeAutoPilot {
		if autopilotThinking {
			// The copilot is deciding — surface it here instead of a transcript line.
			label = " autopilot · thinking…"
		} else {
			if judgeApprovals > 0 {
				label += fmt.Sprintf(" · %d approved", judgeApprovals)
			}
			if judgeEscalations > 0 {
				label += fmt.Sprintf(" · %d escalated", judgeEscalations)
			}
		}
	}

	style := lipgloss.NewStyle().Foreground(clr)
	return "  " + style.Render(icon+label), lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Render(" (shift+tab)")
}

// ModelStatusLabel composes the status line's model segment: the model name
// with its thinking effort in parentheses, e.g. "Opus 5 (high)". Effort is a
// property of the model, so it rides along with the name instead of claiming
// its own segment — one shape for every provider.
func ModelStatusLabel(modelName, effort string) string {
	if effort == "" || effort == "off" || effort == "none" {
		return modelName
	}
	return modelName + " (" + effort + ")"
}
