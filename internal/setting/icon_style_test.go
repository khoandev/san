package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIconStyleKind(t *testing.T) {
	for _, tt := range []struct {
		name string
		data *Data
		want string
	}{
		{"nil", nil, DefaultIconStyle},
		{"unset", NewData(), DefaultIconStyle},
		{"auto", &Data{IconStyle: IconStyleAuto}, IconStyleAuto},
		{"unicode", &Data{IconStyle: IconStyleUnicode}, IconStyleUnicode},
		{"emoji", &Data{IconStyle: IconStyleEmoji}, IconStyleEmoji},
		{"nerd", &Data{IconStyle: IconStyleNerd}, IconStyleNerd},
		{"unknown", &Data{IconStyle: "crayon"}, DefaultIconStyle},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.data.IconStyleKind(); got != tt.want {
				t.Fatalf("IconStyleKind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIconStyleMergeAndClone(t *testing.T) {
	base := &Data{IconStyle: IconStyleEmoji}
	if got := mergeSettings(base, NewData()).IconStyleKind(); got != IconStyleEmoji {
		t.Fatalf("an unset overlay dropped the base style: got %q", got)
	}
	if got := mergeSettings(NewData(), base).IconStyleKind(); got != IconStyleEmoji {
		t.Fatalf("the overlay did not win: got %q", got)
	}
	if got := mergeSettings(base, &Data{IconStyle: IconStyleNerd}).IconStyleKind(); got != IconStyleNerd {
		t.Fatalf("an explicit overlay must override the base: got %q", got)
	}
	clone := base.Clone()
	if clone.IconStyle != IconStyleEmoji {
		t.Fatalf("clone dropped the style: got %q", clone.IconStyle)
	}
	if NewData().Clone().IconStyle != "" {
		t.Fatal("clone must preserve unset")
	}
}

func TestSaveIconStyle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := SaveTheme("dark"); err != nil {
		t.Fatal(err)
	}
	for _, style := range []string{IconStyleUnicode, IconStyleEmoji, IconStyleNerd, IconStyleAuto} {
		if err := SaveIconStyle(style); err != nil {
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
		if got.IconStyle != style || got.IconStyleKind() != style || got.Theme != "dark" {
			t.Fatalf("saved %q: IconStyle=%q, theme=%q", style, got.IconStyle, got.Theme)
		}
		// The independent controller reload must retain the choice.
		controller := New(NewData())
		if err := controller.Reload(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if controller.IconStyleKind() != style {
			t.Fatalf("reloaded %q as %q", style, controller.IconStyleKind())
		}
	}
}
