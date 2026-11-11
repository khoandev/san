package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/llm"
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
	if p.baseURL != llm.DefaultSleevBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, llm.DefaultSleevBaseURL)
	}
	if p.Dirty() {
		t.Fatal("a fresh panel should start clean")
	}
}

// Turning the proxy on writes the switch beside the providers it affects, so
// the next launch reads it back, and reports it so the live session follows.
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
	if !ok || !msg.Enabled || msg.BaseURL != llm.DefaultSleevBaseURL {
		t.Fatalf("got %#v, want the proxy on at %q", cmd(), llm.DefaultSleevBaseURL)
	}
	if p.Dirty() {
		t.Fatal("the applied switch should not still read as unsaved")
	}

	cfg := readStoredSleev(t, home)
	if cfg == nil || !cfg.Enabled || cfg.BaseURL != llm.DefaultSleevBaseURL {
		t.Fatalf("stored sleev = %#v, want it enabled at %q", cfg, llm.DefaultSleevBaseURL)
	}
	if cfg.Harness != llm.DefaultSleevHarness {
		t.Fatalf("stored harness = %q, want %q", cfg.Harness, llm.DefaultSleevHarness)
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
	if done || !p.urlEditing || !p.Modal() {
		t.Fatalf("Enter on the address should open the editor, not save (done=%v editing=%v)", done, p.urlEditing)
	}

	p.urlInput.SetValue("gateway.test:17321")
	if _, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done || p.urlErr == "" {
		t.Fatalf("a scheme-less address should be refused in place (done=%v err=%q)", done, p.urlErr)
	}

	p.urlInput.SetValue("http://127.0.0.1:9999/")
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || p.urlErr != "" || p.saveErr != nil {
		t.Fatalf("save failed: done=%v urlErr=%q saveErr=%v", done, p.urlErr, p.saveErr)
	}
	msg, ok := cmd().(SleevProxySavedMsg)
	if !ok || msg.BaseURL != "http://127.0.0.1:9999" {
		t.Fatalf("got %#v, want the trimmed gateway address", cmd())
	}
	if cfg := readStoredSleev(t, home); cfg == nil || cfg.BaseURL != "http://127.0.0.1:9999" {
		t.Fatalf("stored sleev = %#v, want the edited address", cfg)
	}

	// A cleared field is the default, not an error: the placeholder names it.
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.urlInput.SetValue("")
	cmd, done = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || p.urlErr != "" {
		t.Fatalf("empty address should fall back to the default: done=%v urlErr=%q", done, p.urlErr)
	}
	if msg, ok := cmd().(SleevProxySavedMsg); !ok || msg.BaseURL != llm.DefaultSleevBaseURL {
		t.Fatalf("got %#v, want the default address", cmd())
	}
}

// readStoredSleev reads providers.json directly, so the test checks what was
// written rather than asking the same accessor that wrote it.
func readStoredSleev(t *testing.T, home string) *llm.SleevConfig {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".san", "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Sleev *llm.SleevConfig `json:"sleev"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data.Sleev
}
