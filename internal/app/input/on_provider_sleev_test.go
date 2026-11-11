package input

import (
	"context"
	"os"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/secret"
)

// sleevTestStore returns a provider store and a secret store, both rooted in a
// fresh HOME, so the form reads and writes nothing of the developer's.
func sleevTestStore(t *testing.T) *llm.Store {
	t.Helper()
	store := newProviderTestStore(t)
	secret.ResetDefault()
	t.Cleanup(secret.ResetDefault)
	return store
}

// sleevInput indexes the form's inputs by field focus (1 is the gateway URL,
// 2 the harness, 3 the API key).
func sleevInput(m *ProviderSelector, focus int) *textinput.Model {
	return &m.sleevFormInputs[focus-1]
}

// sleevFormRow finds the gateway's row in the Providers tab.
func sleevFormRow(t *testing.T, m *ProviderSelector) providerProviderItem {
	t.Helper()
	for _, p := range m.allProviders {
		if p.Provider == llm.Sleev {
			return p
		}
	}
	t.Fatal("the gateway row is missing from the Providers tab")
	return providerProviderItem{}
}

func TestOpenSleevFormPrefillsStoredSettings(t *testing.T) {
	t.Setenv(llm.SleevAPIKeyEnvVar, "")

	t.Run("saved settings and credential", func(t *testing.T) {
		store := sleevTestStore(t)
		if err := store.SetSleev(llm.SleevConfig{Enabled: true, BaseURL: "http://192.168.1.9:17321", Harness: "hermes"}); err != nil {
			t.Fatalf("SetSleev() error = %v", err)
		}
		if err := secret.Default().Set(llm.SleevAPIKeyEnvVar, "sk-stored"); err != nil {
			t.Fatalf("secret Set() error = %v", err)
		}

		m := NewProviderSelector()
		m.store = store
		m.openSleevForm()

		if !m.sleevFormActive {
			t.Fatal("openSleevForm should activate the form")
		}
		if !m.sleevFormEnabled {
			t.Fatal("the switch should open in the stored position")
		}
		if got := sleevInput(&m, sleevFormFocusBaseURL).Value(); got != "http://192.168.1.9:17321" {
			t.Fatalf("gateway URL field = %q, want the stored address", got)
		}
		if got := sleevInput(&m, sleevFormFocusHarness).Value(); got != "hermes" {
			t.Fatalf("harness field = %q, want the stored harness", got)
		}
		if got := sleevInput(&m, sleevFormFocusAPIKey).Value(); got != "sk-stored" {
			t.Fatalf("API key field = %q, want the stored credential", got)
		}
	})

	t.Run("defaults before anything is saved", func(t *testing.T) {
		m := NewProviderSelector()
		m.store = sleevTestStore(t)
		m.openSleevForm()

		if m.sleevFormEnabled {
			t.Fatal("an unsaved gateway must open switched off")
		}
		if got := sleevInput(&m, sleevFormFocusBaseURL).Value(); got != llm.DefaultSleevBaseURL {
			t.Fatalf("gateway URL field = %q, want %q", got, llm.DefaultSleevBaseURL)
		}
		if got := sleevInput(&m, sleevFormFocusHarness).Value(); got != llm.DefaultSleevHarness {
			t.Fatalf("harness field = %q, want %q", got, llm.DefaultSleevHarness)
		}
		if got := sleevInput(&m, sleevFormFocusAPIKey).Value(); got != "" {
			t.Fatalf("API key field = %q, want empty", got)
		}
	})
}

func TestValidateSleevForm(t *testing.T) {
	t.Setenv(llm.SleevAPIKeyEnvVar, "")

	tests := []struct {
		name    string
		enabled bool
		baseURL string
		harness string
		apiKey  string
		wantErr string
	}{
		{name: "missing URL", baseURL: "", apiKey: "sk-test", wantErr: "gateway URL is required"},
		{name: "URL without scheme", baseURL: "127.0.0.1:17321", apiKey: "sk-test", wantErr: "https:// or http://"},
		{name: "missing API key", baseURL: "http://127.0.0.1:17321", apiKey: "", wantErr: "API key is required"},
		{name: "blank fields normalize", enabled: true, baseURL: "  http://127.0.0.1:17321/  ", harness: "  ", apiKey: "sk-test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewProviderSelector()
			m.store = sleevTestStore(t)
			m.openSleevForm()
			m.sleevFormEnabled = tt.enabled
			sleevInput(&m, sleevFormFocusBaseURL).SetValue(tt.baseURL)
			sleevInput(&m, sleevFormFocusHarness).SetValue(tt.harness)
			sleevInput(&m, sleevFormFocusAPIKey).SetValue(tt.apiKey)

			cfg, apiKey, err := m.validateSleevForm()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("validateSleevForm() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateSleevForm() error = %v", err)
			}
			if !cfg.Enabled {
				t.Fatal("the saved config should carry the switch position")
			}
			if cfg.BaseURL != "http://127.0.0.1:17321" {
				t.Fatalf("baseURL = %q, want spaces and trailing slash trimmed", cfg.BaseURL)
			}
			if cfg.Harness != llm.DefaultSleevHarness {
				t.Fatalf("harness = %q, want %q", cfg.Harness, llm.DefaultSleevHarness)
			}
			if apiKey != "sk-test" {
				t.Fatalf("apiKey = %q, want the typed credential", apiKey)
			}
		})
	}
}

// TestSubmitSleevFormSavesWithoutConnecting pins what the gateway's form does
// and does not do: it writes the credential and the setting, and nothing else.
// No provider is connected, no model list is fetched, and the command it
// returns only reports the outcome.
func TestSubmitSleevFormSavesWithoutConnecting(t *testing.T) {
	t.Setenv(llm.SleevAPIKeyEnvVar, "")
	t.Cleanup(func() { _ = os.Unsetenv(llm.SleevAPIKeyEnvVar) })

	store := sleevTestStore(t)
	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabProviders
	m.store = store
	m.openSleevForm()

	// A row already on screen, so the repaint after saving has somewhere to go.
	m.allProviders = []providerProviderItem{{
		Provider:    llm.Sleev,
		DisplayName: "Sleev",
		AuthMethods: []providerAuthMethodItem{{
			Provider:   llm.Sleev,
			AuthMethod: llm.AuthAPIKey,
			Status:     llm.StatusNotConfigured,
			EnvVars:    []string{llm.SleevAPIKeyEnvVar},
		}},
	}}
	m.rebuildVisibleItems()

	m.sleevFormEnabled = true
	sleevInput(&m, sleevFormFocusBaseURL).SetValue("http://127.0.0.1:17321/")
	sleevInput(&m, sleevFormFocusHarness).SetValue("pi")
	sleevInput(&m, sleevFormFocusAPIKey).SetValue("sk-gateway")

	cmd := m.submitSleevForm()
	if cmd == nil {
		t.Fatal("a successful submit should report its outcome")
	}
	if m.sleevFormActive {
		t.Fatal("the form should close after a successful submit")
	}
	if m.sleevFormErr != "" {
		t.Fatalf("sleevFormErr = %q, want none", m.sleevFormErr)
	}

	// The command is a status report, not a connect or refresh: running it must
	// produce sleevSavedMsg, and it must not produce a connect result.
	if _, ok := connectResultFromCmd(cmd); ok {
		t.Fatal("saving the gateway must not connect a provider or refresh models")
	}
	msg, ok := cmd().(sleevSavedMsg)
	if !ok {
		t.Fatalf("submit command = %T, want sleevSavedMsg", cmd())
	}
	if !msg.Enabled {
		t.Fatal("sleevSavedMsg should report the gateway switched on")
	}

	// Credential: secret store and process environment.
	if got := secret.Default().Get(llm.SleevAPIKeyEnvVar); got != "sk-gateway" {
		t.Fatalf("stored credential = %q, want sk-gateway", got)
	}
	if got := os.Getenv(llm.SleevAPIKeyEnvVar); got != "sk-gateway" {
		t.Fatalf("env credential = %q, want sk-gateway", got)
	}

	// Setting: the in-memory store, and the file a new process will read.
	cfg := store.Sleev()
	if cfg == nil || !cfg.Enabled || cfg.BaseURL != "http://127.0.0.1:17321" || cfg.Harness != "pi" {
		t.Fatalf("store.Sleev() = %+v, want enabled at the trimmed address", cfg)
	}
	reopened, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if persisted := reopened.Sleev(); persisted == nil || !persisted.Enabled {
		t.Fatalf("providers.json did not keep the enabled gateway: %+v", persisted)
	}

	// Nothing connected behind it, and no models fetched on its behalf.
	if connections := store.Connections(); len(connections) != 0 {
		t.Fatalf("connections = %v, want none: the gateway is a setting", connections)
	}
	if cached := store.CachedModelsByProvider(); len(cached) != 0 {
		t.Fatalf("cached models = %v, want none: nothing was fetched", cached)
	}

	// The row repaints from the setting, not from a reload.
	row := sleevFormRow(t, &m)
	if got := row.AuthMethods[0].Status; got != llm.StatusConnected {
		t.Fatalf("row status = %v, want connected while enabled", got)
	}
}

func TestSubmitSleevFormRejectsInvalidField(t *testing.T) {
	t.Setenv(llm.SleevAPIKeyEnvVar, "")

	m := NewProviderSelector()
	m.store = sleevTestStore(t)
	m.openSleevForm()
	m.sleevFormEnabled = true
	sleevInput(&m, sleevFormFocusBaseURL).SetValue("127.0.0.1:17321")
	sleevInput(&m, sleevFormFocusAPIKey).SetValue("sk-test")

	if cmd := m.submitSleevForm(); cmd != nil {
		t.Fatal("an invalid form should not produce a command")
	}
	if !m.sleevFormActive {
		t.Fatal("an invalid form should stay open")
	}
	if !strings.Contains(m.sleevFormErr, "https:// or http://") {
		t.Fatalf("sleevFormErr = %q, want the scheme complaint", m.sleevFormErr)
	}
	if m.store.Sleev() != nil {
		t.Fatal("nothing should be written when validation fails")
	}
	if got := secret.Default().Get(llm.SleevAPIKeyEnvVar); got != "" {
		t.Fatalf("stored credential = %q, want none written", got)
	}
}

func TestSleevFormKeyRouting(t *testing.T) {
	t.Setenv(llm.SleevAPIKeyEnvVar, "")

	m := NewProviderSelector()
	m.store = sleevTestStore(t)
	m.openSleevForm()

	// The switch opens focused, and Space flips it without typing anything.
	if m.sleevFormFocus != sleevFormFocusEnabled {
		t.Fatalf("focus on open = %d, want the switch", m.sleevFormFocus)
	}
	m.handleSleevFormKey(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !m.sleevFormEnabled {
		t.Fatal("Space on the switch should turn it on")
	}
	m.handleSleevFormKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.sleevFormEnabled {
		t.Fatal("Right on the switch should turn it off")
	}

	// Tab walks the fields in display order and wraps at the end.
	for want := sleevFormFocusBaseURL; want < sleevFormFieldCount; want++ {
		m.handleSleevFormKey(tea.KeyPressMsg{Code: tea.KeyTab})
		if m.sleevFormFocus != want {
			t.Fatalf("focus after Tab = %d, want %d", m.sleevFormFocus, want)
		}
	}
	m.handleSleevFormKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.sleevFormFocus != sleevFormFocusEnabled {
		t.Fatalf("focus after wrapping = %d, want the switch", m.sleevFormFocus)
	}

	// Arrows only act on the switch: in a text field they move the cursor.
	m.handleSleevFormKey(tea.KeyPressMsg{Code: tea.KeyTab})
	sleevInput(&m, sleevFormFocusBaseURL).SetValue("")
	m.handleSleevFormKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m.handleSleevFormKey(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if got := sleevInput(&m, sleevFormFocusBaseURL).Value(); got != "h " {
		t.Fatalf("typed gateway URL = %q, want the keys typed into the field", got)
	}
	if m.sleevFormEnabled {
		t.Fatal("Space in a text field must not flip the switch")
	}

	// Esc closes the form without writing.
	m.handleSleevFormKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.sleevFormActive {
		t.Fatal("Esc should close the form")
	}
}

func TestGoBackClosesSleevForm(t *testing.T) {
	m := NewProviderSelector()
	m.store = newProviderTestStore(t)
	m.openSleevForm()

	if !m.GoBack() {
		t.Fatal("GoBack should consume the event while the form is open")
	}
	if m.sleevFormActive {
		t.Fatal("GoBack should close the form")
	}
}

func TestSelectProviderOpensSleevForm(t *testing.T) {
	store := newProviderTestStore(t)
	if err := store.SetSleev(llm.SleevConfig{Enabled: true, BaseURL: llm.DefaultSleevBaseURL, Harness: llm.DefaultSleevHarness}); err != nil {
		t.Fatalf("SetSleev() error = %v", err)
	}

	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabProviders
	m.store = store
	m.allProviders = []providerProviderItem{{
		Provider:    llm.Sleev,
		DisplayName: "Sleev",
		Connected:   true,
		AuthMethods: []providerAuthMethodItem{{
			Provider:   llm.Sleev,
			AuthMethod: llm.AuthAPIKey,
			Status:     llm.StatusConnected,
			EnvVars:    []string{llm.SleevAPIKeyEnvVar},
		}},
	}}
	m.rebuildVisibleItems()
	m.selectedIdx = 0

	// The row is "connected" while enabled, and Enter still opens the form
	// rather than refreshing a model list the gateway does not have.
	if cmd := m.selectProvider(m.visibleItems[0]); cmd != nil {
		t.Fatal("Enter on the gateway row should not return a command")
	}
	if !m.sleevFormActive {
		t.Fatal("Enter on the gateway row should open its form")
	}
	if m.credForm.active {
		t.Fatal("the gateway should not open the single API-key input")
	}
}

// TestSleevSavedRebuildsSessionProvider pins the switch taking effect without a
// restart: the session's provider was configured against the old address, so
// saving the setting rebuilds it from the store that was just written.
func TestSleevSavedRebuildsSessionProvider(t *testing.T) {
	store := sleevTestStore(t)
	id := llm.ProviderID("sleev-rebuild-test")
	built := 0
	llm.Register(llm.Meta{Provider: id, AuthMethod: llm.AuthAPIKey}, func(context.Context) (llm.Provider, error) {
		built++
		return &connectFailProvider{}, nil
	})
	t.Cleanup(func() { llm.Unregister(id, llm.AuthAPIKey) })

	if err := store.SetCurrentModel("test-model", id, llm.AuthAPIKey); err != nil {
		t.Fatalf("SetCurrentModel() error = %v", err)
	}

	state := ProviderState{Selector: NewProviderSelector()}
	state.Selector.store = store

	previous := llm.Default().Provider()
	t.Cleanup(func() { llm.Default().SetProvider(previous) })

	var switched llm.Provider
	calls := 0
	deps := OverlayDeps{SwitchProvider: func(p llm.Provider, commit func() error) error {
		calls++
		switched = p
		return commit()
	}}

	cmd, handled := UpdateProvider(deps, &state, sleevSavedMsg{Enabled: true})
	if !handled {
		t.Fatal("UpdateProvider should handle sleevSavedMsg")
	}
	if cmd == nil {
		t.Fatal("a saved setting should report its outcome")
	}
	if built != 1 {
		t.Fatalf("GetProvider() calls = %d, want one rebuild of the session provider", built)
	}
	if calls != 1 {
		t.Fatalf("SwitchProvider() calls = %d, want 1", calls)
	}
	if switched == nil {
		t.Fatal("SwitchProvider should receive the rebuilt provider")
	}
	if got := llm.Default().Provider(); got != switched {
		t.Fatalf("default connection provider = %v, want the rebuilt one", got)
	}
	if got := state.StatusMessage; !strings.Contains(got, "Sleev:") {
		t.Fatalf("StatusMessage = %q, want the gateway outcome", got)
	}
}

// TestSleevSavedWithoutCurrentModelClearsProvider covers the other half: an
// unsaved model leaves nothing to rebuild, so the default connection is cleared
// for the next turn to resolve instead of holding a stale provider.
func TestSleevSavedWithoutCurrentModelClearsProvider(t *testing.T) {
	store := sleevTestStore(t)
	state := ProviderState{Selector: NewProviderSelector()}
	state.Selector.store = store

	previous := llm.Default().Provider()
	llm.Default().SetProvider(&connectFailProvider{})
	t.Cleanup(func() { llm.Default().SetProvider(previous) })

	calls := 0
	deps := OverlayDeps{SwitchProvider: func(llm.Provider, func() error) error {
		calls++
		return nil
	}}

	cmd, handled := UpdateProvider(deps, &state, sleevSavedMsg{Enabled: false})
	if !handled {
		t.Fatal("UpdateProvider should handle sleevSavedMsg")
	}
	if cmd == nil {
		t.Fatal("a saved setting should report its outcome")
	}
	if calls != 0 {
		t.Fatalf("SwitchProvider() calls = %d, want none without a current model", calls)
	}
	if got := llm.Default().Provider(); got != nil {
		t.Fatalf("default connection provider = %v, want nil so the next turn resolves", got)
	}
	if got := state.StatusMessage; !strings.Contains(got, "Sleev:") {
		t.Fatalf("StatusMessage = %q, want the gateway outcome", got)
	}
}

// TestProviderDataSleevRowStatus pins the row's three states: the switch
// decides connected, and the stored credential only decides whether an idle
// gateway reads as available.
func TestProviderDataSleevRowStatus(t *testing.T) {
	t.Setenv(llm.SleevAPIKeyEnvVar, "")

	tests := []struct {
		name    string
		enabled bool
		key     string
		want    llm.Status
	}{
		{name: "enabled", enabled: true, key: "sk-stored", want: llm.StatusConnected},
		{name: "enabled without a key", enabled: true, want: llm.StatusConnected},
		{name: "key but switched off", enabled: false, key: "sk-stored", want: llm.StatusAvailable},
		{name: "nothing configured", enabled: false, want: llm.StatusNotConfigured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := sleevTestStore(t)
			if err := store.SetSleev(llm.SleevConfig{Enabled: tt.enabled}); err != nil {
				t.Fatalf("SetSleev() error = %v", err)
			}
			if tt.key != "" {
				if err := secret.Default().Set(llm.SleevAPIKeyEnvVar, tt.key); err != nil {
					t.Fatalf("secret Set() error = %v", err)
				}
			}

			m := NewProviderSelector()
			m.active = true
			m.activeTab = providerTabProviders
			if _, err := m.Enter(t.Context(), 100, 40); err != nil {
				t.Fatalf("Enter() error = %v", err)
			}

			row := sleevFormRow(t, &m)
			if len(row.AuthMethods) != 1 {
				t.Fatalf("auth methods = %d, want the gateway's single method", len(row.AuthMethods))
			}
			if got := row.AuthMethods[0].Status; got != tt.want {
				t.Fatalf("row status = %v, want %v", got, tt.want)
			}
			if got := row.Connected; got != (tt.want == llm.StatusConnected) {
				t.Fatalf("row Connected = %v for status %v", got, tt.want)
			}

			// The switch also survives a reopen: the form prefills from the
			// setting Enter just loaded.
			m.openSleevForm()
			if m.sleevFormEnabled != tt.enabled {
				t.Fatalf("form switch = %v, want %v", m.sleevFormEnabled, tt.enabled)
			}
		})
	}
}
