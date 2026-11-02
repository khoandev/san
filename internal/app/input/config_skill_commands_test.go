package input

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/setting"
)

func TestAppearanceSkillCommandsToggle(t *testing.T) {
	p := newAppearancePanel(nil)
	p.Enter()
	if !p.skillCommandsBaseline {
		t.Fatal("skill commands must default to listed")
	}
	rows := 0
	for i, opt := range p.options {
		if opt.kind != kindSkillCommands {
			continue
		}
		rows++
		p.cursor = i
		if p.Dirty() || !opt.barOn {
			t.Fatal("SKILL COMMANDS must start On and clean")
		}
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
		if !p.Dirty() || p.options[i].barOn {
			t.Fatal("left should preview Off and mark the preference dirty")
		}
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
		if p.options[i].barOn {
			t.Fatal("left of Off must stay Off")
		}
		if !strings.Contains(ansi.Strip(p.HintLine()), "←→") {
			t.Fatal("the toggle must advertise its Off/On keys")
		}
	}
	if rows != 1 || strings.Count(ansi.Strip(p.Render(100, 40)), "SLASH MENU") != 1 {
		t.Fatal("Appearance must offer exactly one slash-menu toggle")
	}
}

func TestAppearanceSkillCommandsReadsBaseline(t *testing.T) {
	off := false
	p := newAppearancePanel(setting.New(&setting.Data{SkillCommands: &off}))
	p.Enter()
	if p.skillCommandsBaseline {
		t.Fatal("panel did not read the saved skill-commands preference")
	}
	for _, opt := range p.options {
		if opt.kind == kindSkillCommands && (opt.barOn || !p.isCurrent(opt)) {
			t.Fatal("toggle does not match the saved preference")
		}
	}
}

func TestAppearanceSkillCommandsSavesAndEmits(t *testing.T) {
	tempHome(t)
	p := newAppearancePanel(nil)
	p.Enter()
	for _, on := range []bool{false, true} {
		for i, opt := range p.options {
			if opt.kind == kindSkillCommands {
				p.cursor = i
			}
		}
		key := tea.KeyLeft
		if on {
			key = tea.KeyRight
		}
		p.HandleKey(tea.KeyPressMsg{Code: key})
		cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !done || cmd == nil || p.saveErr != nil || p.skillCommandsBaseline != on || p.Dirty() {
			t.Fatalf("on=%v: done=%v, err=%v, baseline=%v", on, done, p.saveErr, p.skillCommandsBaseline)
		}
		msg, ok := cmd().(SkillCommandsSavedMsg)
		if !ok || msg.On != on {
			t.Fatalf("on=%v: wrong saved message %#v", on, msg)
		}
		controller := setting.New(setting.NewData())
		if err := controller.Reload(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if controller.ShowSkillCommands() != on {
			t.Fatalf("on=%v did not persist", on)
		}
	}
}
