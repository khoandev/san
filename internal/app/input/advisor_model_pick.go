// Advisor model sub-view: picks the provider and the model the independent
// review runs on, stored as a "vendor/model" ref so the choice stays pinned to
// its provider even when the session model changes. Leaving it unset keeps the
// session model, which is why that is the first row.
//
// The advisor tab exposes three decisions; this is the only one that needs a
// list rather than a toggle, so it owns a sub-view instead of a form row the
// cursor steps through.
package input

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// advisorModelListRows caps how many model rows the picker shows at once.
const advisorModelListRows = 12

// advisorModelPick is the picker's state: connected "vendor/model" refs, the
// provider being browsed ("" = provider list), the typed filter, and the
// selected row.
type advisorModelPick struct {
	models []string
	vendor string
	filter string
	cursor int
}

// begin resets the picker for an open, placing the cursor on the provider of
// the current ref when there is one.
func (p *advisorModelPick) begin(refs []string, current string) {
	p.models = slices.Clone(refs)
	p.vendor, p.filter, p.cursor = "", "", 0
	if vendor, _, ok := llm.ParseVendorModel(current); ok {
		p.cursor = max(slices.Index(p.choices(), string(vendor)), 0)
	}
}

// choices lists the current stage's rows: the session model plus each provider,
// then the provider's models matching the filter.
func (p *advisorModelPick) choices() []string {
	if p.vendor != "" {
		var out []string
		q := strings.ToLower(p.filter)
		for _, ref := range p.models {
			vendor, id, ok := llm.ParseVendorModel(ref)
			if ok && string(vendor) == p.vendor && strings.Contains(strings.ToLower(id), q) {
				out = append(out, id)
			}
		}
		return out
	}
	out := []string{sessionModelLabel}
	for _, ref := range p.models {
		if vendor, _, ok := llm.ParseVendorModel(ref); ok && !slices.Contains(out, string(vendor)) {
			out = append(out, string(vendor))
		}
	}
	return out
}

// handleKey applies one keypress. closed reports that the picker is done (esc at
// the top level, or a completed pick); picked reports that ref carries the
// chosen "vendor/model" ref — empty means the session model.
func (p *advisorModelPick) handleKey(msg tea.KeyMsg) (closed, picked bool, ref string) {
	choices := p.choices()
	switch msg.String() {
	case "esc":
		if p.vendor != "" {
			p.vendor, p.filter, p.cursor = "", "", 0
			return false, false, ""
		}
		return true, false, ""
	case "up":
		p.cursor = max(p.cursor-1, 0)
	case "down":
		p.cursor = min(p.cursor+1, max(len(choices)-1, 0))
	case "enter", "space":
		if len(choices) == 0 {
			return false, false, ""
		}
		pick := choices[min(p.cursor, len(choices)-1)]
		if p.vendor == "" {
			if pick == sessionModelLabel {
				return true, true, ""
			}
			p.vendor, p.filter, p.cursor = pick, "", 0
			return false, false, ""
		}
		return true, true, p.vendor + "/" + pick
	case "backspace":
		if r := []rune(p.filter); len(r) > 0 {
			p.filter = string(r[:len(r)-1])
			p.cursor = 0
		}
	default:
		if t := msg.Key().Text; p.vendor != "" && t != "" {
			p.filter += t
			p.cursor = 0
		}
	}
	return false, false, ""
}

// current names the saved ref as it appears in the current stage, so the list
// can tick it: the provider in the provider list, its model id in the model
// list, and the session-model row when nothing is pinned.
func (p *advisorModelPick) current(saved string) string {
	vendor, id, _ := llm.ParseVendorModel(saved)
	switch {
	case p.vendor != "":
		if string(vendor) != p.vendor {
			return ""
		}
		return id
	case saved == "":
		return sessionModelLabel
	default:
		return string(vendor)
	}
}

// filtering reports the model stage, where typed text narrows the list.
func (p *advisorModelPick) filtering() bool { return p.vendor != "" }

func (p *advisorModelPick) render(width int, saved string) string {
	var b strings.Builder
	switch {
	case p.vendor != "":
		b.WriteString(apLabelStyle.Render("Filter") + "  " + apValueStyle.Render(p.filter) + apCursorStyle.Render("_"))
	default:
		b.WriteString(apDescStyle.Render("Which model reviews. A cheap, fast model is usually enough; leave it on the session model to follow whatever the conversation runs on."))
	}
	b.WriteString("\n\n")

	choices := p.choices()
	current := p.current(saved)
	start := max(min(p.cursor-advisorModelListRows/2, len(choices)-advisorModelListRows), 0)
	end := min(start+advisorModelListRows, len(choices))
	for i := start; i < end; i++ {
		text := choices[i]
		if text == current {
			text += "  ✓"
		}
		mark, label := "  ", apLabelStyle.Render(text)
		if i == p.cursor {
			mark, label = apCursorStyle.Render("▸ "), apBreadcrumbSubStyle.Render(text)
		}
		b.WriteString(kit.TruncateText(mark+label, max(width-2, 20)) + "\n")
	}
	if len(p.models) == 0 {
		b.WriteString("\n" + apSummaryStyle.Render("No cached models — open /models once to load a provider's list."))
	}
	return b.String()
}

func (p *advisorModelPick) hint() string {
	if p.filtering() {
		return kit.HintLine(keycap("↑↓")+" navigate", "type to filter", keycap("enter")+" pick", keycap("esc")+" back")
	}
	return kit.HintLine(keycap("↑↓")+" navigate", keycap("enter")+" pick", keycap("esc")+" back")
}

func (p *advisorModelPick) crumb() string {
	if p.vendor != "" {
		return "Model › " + p.vendor
	}
	return "Model"
}

// advisorModelLabel is the Model row's value: the pinned ref, or the session
// model when nothing is pinned.
func advisorModelLabel(s *setting.AdvisorSettings) string {
	if s.Model.Model == "" {
		return sessionModelLabel
	}
	if s.Model.ThinkingEffort == "" {
		return s.Model.Model
	}
	return s.Model.Model + " · " + s.Model.ThinkingEffort
}
