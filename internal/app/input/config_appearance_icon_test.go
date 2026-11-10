package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/setting"
)

// parkOnIconStyle moves the cursor to the row for style, failing when the
// group is missing.
func parkOnIconStyle(t *testing.T, p *appearancePanel, style string) {
	t.Helper()
	target := -1
	for i, opt := range p.options {
		if opt.kind == kindIconStyle && opt.iconStyle == style {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("no %q row in the appearance options", style)
	}
	p.cursor = target
}

// TestAppearanceIconGroupExists confirms every style has a row and that the
// unknown/unset value parks on the auto row.
func TestAppearanceIconGroupExists(t *testing.T) {
	for _, style := range []string{
		setting.IconStyleAuto,
		setting.IconStyleUnicode,
		setting.IconStyleEmoji,
		setting.IconStyleNerd,
	} {
		idx := indexOfIconStyle(style)
		opts := appearanceOptions()
		if idx < 0 || idx >= len(opts) || opts[idx].kind != kindIconStyle || opts[idx].iconStyle != style {
			t.Fatalf("indexOfIconStyle(%q) = %d, not that row", style, idx)
		}
	}
	if got := appearanceOptions()[indexOfIconStyle("bogus")].iconStyle; got != setting.IconStyleAuto {
		t.Fatalf("unknown style parked on %q, want the auto row", got)
	}

	// The panel reads the persisted style, and the auto row says what the
	// terminal currently resolves to instead of leaving the user to guess.
	p := newAppearancePanel(setting.New(&setting.Data{IconStyle: setting.IconStyleEmoji}))
	p.Enter()
	if p.iconBaseline != setting.IconStyleEmoji {
		t.Fatalf("iconBaseline = %q, want emoji", p.iconBaseline)
	}
	parkOnIconStyle(t, p, setting.IconStyleEmoji)
	if p.Dirty() {
		t.Fatal("the row matching the saved style should not be dirty")
	}
	parkOnIconStyle(t, p, setting.IconStyleNerd)
	if !p.Dirty() {
		t.Fatal("a row diverging from the saved style should be dirty")
	}
}

// TestAppearanceIconStyleAppliesAndSaves confirms Enter installs the symbol
// table live, persists the style to the user settings file, advances the
// baseline, and emits IconStyleSavedMsg.
func TestAppearanceIconStyleAppliesAndSaves(t *testing.T) {
	t.Cleanup(func() { kit.InitSymbols(setting.IconStyleUnicode) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	p := newAppearancePanel(nil)
	p.Enter() // nil settings → auto
	parkOnIconStyle(t, p, setting.IconStyleNerd)

	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatalf("enter should dismiss the popup (done=true), saveErr=%v", p.saveErr)
	}
	if cmd == nil {
		t.Fatal("expected an IconStyleSavedMsg command")
	}
	msg, ok := cmd().(IconStyleSavedMsg)
	if !ok || msg.Style != setting.IconStyleNerd {
		t.Fatalf("expected IconStyleSavedMsg{nerd}, got %#v", cmd())
	}
	// Applied live: the table follows the saved choice, not just the popup.
	if got := kit.CurrentSymbols.LiveDot; got != "\uf111" { // nf-fa-circle
		t.Fatalf("the nerd table was not installed: LiveDot = %q", got)
	}
	if p.iconBaseline != setting.IconStyleNerd {
		t.Fatalf("iconBaseline = %q, want nerd", p.iconBaseline)
	}
	parkOnIconStyle(t, p, setting.IconStyleNerd)
	if p.Dirty() {
		t.Fatal("the applied row should be the new baseline")
	}

	raw, err := os.ReadFile(filepath.Join(home, ".san", "settings.json"))
	if err != nil {
		t.Fatalf("settings file not written: %v", err)
	}
	var data struct {
		IconStyle string `json:"iconStyle"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("settings file not valid JSON: %v\n%s", err, raw)
	}
	if data.IconStyle != setting.IconStyleNerd {
		t.Fatalf("persisted iconStyle = %q, want nerd\n%s", data.IconStyle, raw)
	}
	// The reload the app performs must see the same choice.
	controller := setting.New(setting.NewData())
	if err := controller.Reload(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := controller.IconStyleKind(); got != setting.IconStyleNerd {
		t.Fatalf("reloaded icon style = %q, want nerd", got)
	}
}

// TestAppearanceIconStyleSaveFailureReverts confirms a failed persist keeps the
// popup open, surfaces the error, and puts the previously installed table back
// so the UI never shows a style a restart would not.
func TestAppearanceIconStyleSaveFailureReverts(t *testing.T) {
	t.Cleanup(func() { kit.InitSymbols(setting.IconStyleUnicode) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Block the write: a regular file where the .san dir must be makes the
	// loader's MkdirAll fail.
	if err := os.WriteFile(filepath.Join(home, ".san"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newAppearancePanel(nil)
	p.Enter()                    // baseline auto
	before := kit.CurrentSymbols // the Unicode table this process started with
	parkOnIconStyle(t, p, setting.IconStyleEmoji)
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || cmd != nil {
		t.Fatalf("a failed save must keep the popup open (done=%v, cmd=%#v)", done, cmd)
	}
	if p.saveErr == nil {
		t.Fatal("a failed save should set saveErr")
	}
	if p.iconBaseline != setting.IconStyleAuto {
		t.Fatalf("iconBaseline should be untouched on failure, got %q", p.iconBaseline)
	}
	if !reflect.DeepEqual(kit.CurrentSymbols, before) {
		t.Fatalf("the failed style must be reverted, got %+v", kit.CurrentSymbols)
	}
}
