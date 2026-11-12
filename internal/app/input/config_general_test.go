package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/setting"
)

// Auto update is opt-out, so turning it off must persist an explicit false —
// a dropped field would read back as on.
func TestGeneralPanelAutoUpdateOffPersistsExplicitFalse(t *testing.T) {
	home := tempHome(t)

	p := newGeneralPanel(nil)
	p.Enter()
	if !p.baseline || p.Dirty() {
		t.Fatal("auto update should default on and start clean")
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // On → Off
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || p.saveErr != nil {
		t.Fatalf("save failed: done=%v err=%v", done, p.saveErr)
	}
	got := cmd()
	if msg, ok := got.(AutoUpdateSavedMsg); !ok || msg.On {
		t.Fatalf("got %#v, want AutoUpdateSavedMsg{On:false}", got)
	}

	raw, err := os.ReadFile(filepath.Join(home, ".san", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		AutoUpdate *bool `json:"autoUpdate"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.AutoUpdate == nil || *data.AutoUpdate {
		t.Fatalf("persisted autoUpdate = %v, want explicit false", data.AutoUpdate)
	}
}

// The proxy is a switch and an address, nothing else: no credential of its
// own, and it starts off with the local gateway already stated.
func TestGeneralPanelSleevDefaults(t *testing.T) {
	tempHome(t)

	p := newGeneralPanel(nil)
	p.Enter()

	if p.sleevBaseline {
		t.Fatal("the proxy should start off")
	}
	if p.baseURL != setting.DefaultSleevBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, setting.DefaultSleevBaseURL)
	}
	if p.Dirty() {
		t.Fatal("a fresh panel should start clean")
	}
}

// Turning the proxy on writes the switch to the user's settings file, so the
// next launch reads it back — the restart the old store lost it across — and
// reports it so the live session follows. Re-opening the panel draws the value
// the save left, from the same handle the app reloads.
func TestGeneralPanelSleevTogglePersists(t *testing.T) {
	home := tempHome(t)

	p := newGeneralPanel(nil)
	p.Enter()
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // On → Off
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Off → proxy switch
	if opt := p.options[p.cursor]; opt.kind != kindSleevToggle {
		t.Fatalf("cursor is on %v, want the proxy switch", opt.kind)
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // preview: on
	if !p.Dirty() {
		t.Fatal("a previewed switch should read as unsaved")
	}

	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || p.saveErr != nil {
		t.Fatalf("save failed: done=%v err=%v", done, p.saveErr)
	}
	msg, ok := cmd().(SleevProxySavedMsg)
	if !ok || !msg.Enabled || msg.BaseURL != setting.DefaultSleevBaseURL {
		t.Fatalf("got %#v, want the proxy on at %q", cmd(), setting.DefaultSleevBaseURL)
	}
	if p.Dirty() {
		t.Fatal("the applied switch should not still read as unsaved")
	}

	cfg, found := readStoredSleev(t, home)
	if !found || !cfg.IsEnabled() || cfg.BaseURL != setting.DefaultSleevBaseURL {
		t.Fatalf("stored sleev = %#v (found=%v), want it enabled at %q", cfg, found, setting.DefaultSleevBaseURL)
	}
	if cfg.Harness != setting.DefaultSleevHarness {
		t.Fatalf("stored harness = %q, want %q", cfg.Harness, setting.DefaultSleevHarness)
	}

	// The value survives the close-and-reopen the panel does: the settings are
	// on disk, so a second panel — a second Settings handle — reads them back.
	reopened := reloadedPanel(t)
	if !reopened.sleevBaseline || reopened.baseURL != setting.DefaultSleevBaseURL {
		t.Fatalf("reopened panel = (%v, %q), want the saved switch and address", reopened.sleevBaseline, reopened.baseURL)
	}
	if opt := reopened.options[2]; !opt.on {
		t.Fatal("reopened switch row shows off, want on")
	}
}

// The address editor takes a bare host wrong and says why; an empty field means
// the local default, which is the value the placeholder advertises.
func TestGeneralPanelSleevURLEditor(t *testing.T) {
	home := tempHome(t)

	p := newGeneralPanel(nil)
	p.Enter()
	for range 3 {
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if opt := p.options[p.cursor]; opt.kind != kindSleevURL {
		t.Fatalf("cursor is on %v, want the gateway address", opt.kind)
	}

	_, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || !p.editor.active || p.editor.kind != kindSleevURL || !p.Modal() {
		t.Fatalf("Enter on the address should open the editor, not save (done=%v editor=%+v)", done, p.editor.active)
	}

	p.editor.input.SetValue("gateway.test:17321")
	if _, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done || p.editor.err == "" {
		t.Fatalf("a scheme-less address should be refused in place (done=%v err=%q)", done, p.editor.err)
	}

	p.editor.input.SetValue("http://127.0.0.1:9999/")
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || p.editor.err != "" || p.saveErr != nil {
		t.Fatalf("save failed: done=%v editErr=%q saveErr=%v", done, p.editor.err, p.saveErr)
	}
	msg, ok := cmd().(SleevProxySavedMsg)
	if !ok || msg.BaseURL != "http://127.0.0.1:9999" {
		t.Fatalf("got %#v, want the trimmed gateway address", cmd())
	}
	if cfg, _ := readStoredSleev(t, home); cfg.BaseURL != "http://127.0.0.1:9999" {
		t.Fatalf("stored sleev = %#v, want the edited address", cfg)
	}

	// A cleared field is the default, not an error: the placeholder names it.
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.editor.input.SetValue("")
	cmd, done = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || p.editor.err != "" {
		t.Fatalf("empty address should fall back to the default: done=%v err=%q", done, p.editor.err)
	}
	if msg, ok := cmd().(SleevProxySavedMsg); !ok || msg.BaseURL != setting.DefaultSleevBaseURL {
		t.Fatalf("got %#v, want the default address", cmd())
	}
}

// The harness row uses the same editor as the address: it starts on the stored
// name, refuses a name the gateway could not route, and writes the value with
// the switch and address it did not touch.
func TestGeneralPanelSleevHarnessEditor(t *testing.T) {
	home := tempHome(t)

	p := newGeneralPanel(nil)
	p.Enter()
	for range 4 {
		p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if opt := p.options[p.cursor]; opt.kind != kindSleevHarness {
		t.Fatalf("cursor is on %v, want the harness row", opt.kind)
	}

	_, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || !p.editor.active || p.editor.kind != kindSleevHarness {
		t.Fatalf("Enter on the harness should open the editor on that row (done=%v kind=%v)", done, p.editor.kind)
	}
	if got := p.editor.input.Value(); got != setting.DefaultSleevHarness {
		t.Fatalf("editor started on %q, want the stored harness %q", got, setting.DefaultSleevHarness)
	}

	p.editor.input.SetValue("two words")
	if _, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done || p.editor.err == "" {
		t.Fatalf("a harness with a space should be refused in place (done=%v err=%q)", done, p.editor.err)
	}

	p.editor.input.SetValue(" opencode ")
	if _, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || p.editor.err != "" {
		t.Fatalf("save failed: done=%v err=%q", done, p.editor.err)
	}
	if cfg, _ := readStoredSleev(t, home); cfg.Harness != "opencode" {
		t.Fatalf("stored harness = %q, want the trimmed name", cfg.Harness)
	}

	// Re-opening the panel draws the value the save left behind, beside the
	// switch and address it kept.
	reopened := reloadedPanel(t)
	if reopened.harness != "opencode" {
		t.Fatalf("reopened harness = %q, want the saved name", reopened.harness)
	}
	if opt := reopened.options[4]; opt.desc != "opencode" {
		t.Fatalf("harness row shows %q, want the saved name", opt.desc)
	}

	// A cleared field is the default, not an error: the placeholder names it.
	for range 4 {
		reopened.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	reopened.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	reopened.editor.input.SetValue("")
	if _, done := reopened.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || reopened.editor.err != "" {
		t.Fatalf("empty harness should fall back to the default: done=%v err=%q", done, reopened.editor.err)
	}
	if cfg, _ := readStoredSleev(t, home); cfg.Harness != setting.DefaultSleevHarness {
		t.Fatalf("stored harness = %q, want %q", cfg.Harness, setting.DefaultSleevHarness)
	}
}

// reloadedPanel opens a panel on a freshly loaded settings handle, the way
// dismissing /settings and opening it again does.
func reloadedPanel(t *testing.T) *generalPanel {
	t.Helper()
	data, err := setting.Load()
	if err != nil {
		t.Fatalf("setting.Load() error = %v", err)
	}
	p := newGeneralPanel(setting.New(data))
	p.Enter()
	return p
}

// readStoredSleev reads settings.json directly, so the test checks what was
// written rather than asking the same accessor that wrote it.
func readStoredSleev(t *testing.T, home string) (setting.SleevSettings, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".san", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Sleev *setting.SleevSettings `json:"sleev"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Sleev == nil {
		return setting.SleevSettings{}.Normalized(), false
	}
	return data.Sleev.Normalized(), true
}
