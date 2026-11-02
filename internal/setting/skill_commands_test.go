package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestShowSkillCommands(t *testing.T) {
	on, off := true, false
	for _, tt := range []struct {
		name string
		data *Data
		want bool
	}{
		{"nil", nil, true}, // opt-out: no settings at all still lists skills
		{"unset", NewData(), true},
		{"on", &Data{SkillCommands: &on}, true},
		{"off", &Data{SkillCommands: &off}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.data.ShowSkillCommands(); got != tt.want {
				t.Fatalf("ShowSkillCommands = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkillCommandsMergeAndClone(t *testing.T) {
	on, off := true, false
	base := &Data{SkillCommands: &off}
	if mergeSettings(base, NewData()).ShowSkillCommands() {
		t.Fatal("an unset overlay dropped the base preference")
	}
	if got := mergeSettings(base, &Data{SkillCommands: &on}); got.SkillCommands == nil || !got.ShowSkillCommands() {
		t.Fatal("an explicit true must override false")
	}
	if mergeSettings(&Data{SkillCommands: &on}, base).ShowSkillCommands() {
		t.Fatal("an explicit false must override true")
	}
	clone := base.Clone()
	if clone.ShowSkillCommands() || clone.SkillCommands == base.SkillCommands {
		t.Fatal("clone must preserve and deep-copy the skill-commands pointer")
	}
	*clone.SkillCommands = true
	if base.ShowSkillCommands() {
		t.Fatal("changing a clone changed the original skill-commands preference")
	}
	if NewData().Clone().SkillCommands != nil {
		t.Fatal("clone must preserve unset")
	}
}

func TestSaveSkillCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := SaveTheme("dark"); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} {
		if err := SaveSkillCommands(on); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(home, ".san", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var got Data
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.SkillCommands == nil || got.ShowSkillCommands() != on || got.Theme != "dark" {
			t.Fatalf("saved on=%v: SkillCommands=%v, theme=%q", on, got.SkillCommands, got.Theme)
		}
		// The independent controller reload must retain the choice.
		controller := New(NewData())
		if err := controller.Reload(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if controller.ShowSkillCommands() != on {
			t.Fatalf("reloaded on=%v as %v", on, controller.ShowSkillCommands())
		}
	}
}
