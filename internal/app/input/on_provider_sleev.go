// Provider selector: the Sleev gateway's settings form.
//
// The gateway is not a vendor: turning it on routes every other provider
// through it, and turning it off puts them back on their own addresses. So this
// form writes one setting and no connection — no key is checked against
// anything, no model list is fetched, and the Providers tab keeps listing the
// providers behind it exactly as before.
package input

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/secret"
)

// The form's rows, in display order. The first is the on/off switch, which is
// drawn rather than typed into; the rest are text inputs.
const (
	sleevFormFocusEnabled = iota
	sleevFormFocusBaseURL
	sleevFormFocusHarness
	sleevFormFocusAPIKey
	sleevFormFieldCount
)

// sleevFormInputCount is how many typed rows the form has — base URL, harness,
// API key — in the order they are focused, so focus-1 indexes them.
const sleevFormInputCount = sleevFormFieldCount - 1

// sleevSavedMsg reports that the gateway's settings were written, so the status
// bar can confirm it without waiting on a model reload that never comes.
type sleevSavedMsg struct {
	Enabled bool
}

// isSleevProvider reports whether name is the gateway's fixed name.
func (s *ProviderSelector) isSleevProvider(name llm.ProviderID) bool {
	return name == llm.Sleev
}

// loadSleevConfig returns the stored gateway settings, with the defaults the
// store would apply filled in. Nil only when the store itself is missing.
func (s *ProviderSelector) loadSleevConfig() (llm.SleevConfig, bool) {
	if s.store == nil {
		return llm.SleevConfig{}, false
	}
	cfg := s.store.Sleev()
	if cfg == nil {
		return llm.SleevConfig{}.Normalized(), true
	}
	return cfg.Normalized(), true
}

// sleevRowStatus is the state the gateway's row reports. It is the setting that
// decides — enabled reads as connected, because that is what it means for every
// provider — and the credential only decides whether an idle gateway is ready.
func (s *ProviderSelector) sleevRowStatus() llm.Status {
	cfg, ok := s.loadSleevConfig()
	if ok && cfg.Enabled {
		return llm.StatusConnected
	}
	if secret.Resolve(llm.SleevAPIKeyEnvVar) != "" {
		return llm.StatusAvailable
	}
	return llm.StatusNotConfigured
}

// openSleevForm initializes the inline form, prefilled from the stored setting
// and the stored credential.
func (s *ProviderSelector) openSleevForm() {
	cfg, _ := s.loadSleevConfig()

	urlInput := textinput.New()
	urlInput.Placeholder = llm.DefaultSleevBaseURL
	urlInput.SetValue(cfg.BaseURL)
	urlInput.CharLimit = 256
	urlInput.SetWidth(40)

	harnessInput := textinput.New()
	harnessInput.Placeholder = llm.DefaultSleevHarness
	harnessInput.SetValue(cfg.Harness)
	harnessInput.CharLimit = 64
	harnessInput.SetWidth(40)

	keyInput := textinput.New()
	keyInput.Placeholder = llm.SleevAPIKeyEnvVar
	keyInput.SetValue(secret.Resolve(llm.SleevAPIKeyEnvVar))
	keyInput.CharLimit = 256
	keyInput.SetWidth(40)
	keyInput.EchoMode = textinput.EchoPassword

	s.closeCredentialForm()
	s.closeCustomForm()
	s.closeOllamaForm()
	s.sleevFormInputs = [sleevFormInputCount]textinput.Model{urlInput, harnessInput, keyInput}
	s.sleevFormEnabled = cfg.Enabled
	s.sleevFormErr = ""
	s.sleevFormActive = true
	s.sleevFormFocus = sleevFormFocusEnabled
}

func (s *ProviderSelector) closeSleevForm() {
	s.sleevFormActive = false
	s.sleevFormErr = ""
	s.sleevFormFocus = sleevFormFocusEnabled
}

// sleevFormInputAt returns the text input the focus is on, or nil while the
// on/off switch has it: that row is toggled, not typed into.
func (s *ProviderSelector) sleevFormInputAt() *textinput.Model {
	if s.sleevFormFocus <= sleevFormFocusEnabled || s.sleevFormFocus > sleevFormFieldCount-1 {
		return nil
	}
	return &s.sleevFormInputs[s.sleevFormFocus-1]
}

func (s *ProviderSelector) handleSleevFormKey(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc":
		s.closeSleevForm()
		return nil
	case "tab", "down":
		s.focusSleevFormField((s.sleevFormFocus + 1) % sleevFormFieldCount)
		return nil
	case "shift+tab", "up":
		s.focusSleevFormField((s.sleevFormFocus + sleevFormFieldCount - 1) % sleevFormFieldCount)
		return nil
	case "enter":
		return s.submitSleevForm()
	case "left", "right", "space":
		// The switch is the only row arrows and Space act on; in a text field
		// they move the cursor and type as usual.
		if s.sleevFormFocus == sleevFormFocusEnabled {
			s.sleevFormEnabled = !s.sleevFormEnabled
			return nil
		}
	}

	input := s.sleevFormInputAt()
	if input == nil {
		return nil
	}
	var cmd tea.Cmd
	*input, cmd = input.Update(key)
	return cmd
}

func (s *ProviderSelector) focusSleevFormField(i int) {
	for j := range s.sleevFormInputs {
		s.sleevFormInputs[j].Blur()
	}
	s.sleevFormFocus = i
	if input := s.sleevFormInputAt(); input != nil {
		input.Focus()
	}
}

// validateSleevForm normalizes and validates the fields. The address has to be
// one a request can be sent to, and an empty credential is "keep the stored
// one" — which only fails when nothing is stored, because a gateway reached
// without a key is a legitimate setup but an unauthenticated one is not what an
// empty form means.
func (s *ProviderSelector) validateSleevForm() (llm.SleevConfig, string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(s.sleevFormInputs[sleevFormFocusBaseURL-1].Value()), "/")
	if baseURL == "" {
		return llm.SleevConfig{}, "", fmt.Errorf("gateway URL is required")
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return llm.SleevConfig{}, "", fmt.Errorf("gateway URL must start with https:// or http://")
	}

	apiKey := strings.TrimSpace(s.sleevFormInputs[sleevFormFocusAPIKey-1].Value())
	if apiKey == "" && secret.Resolve(llm.SleevAPIKeyEnvVar) == "" {
		return llm.SleevConfig{}, "", fmt.Errorf("API key is required")
	}

	harness := strings.TrimSpace(s.sleevFormInputs[sleevFormFocusHarness-1].Value())
	cfg := llm.SleevConfig{Enabled: s.sleevFormEnabled, BaseURL: baseURL, Harness: harness}.Normalized()
	return cfg, apiKey, nil
}

// submitSleevForm persists the form — the credential to the secret store, the
// settings to the provider store — and repaints the row from what was written.
//
// Nothing is fetched and nothing is connected: the gateway serves no models of
// its own, and the providers already connected behind it are unaffected until
// their next request.
func (s *ProviderSelector) submitSleevForm() tea.Cmd {
	cfg, apiKey, err := s.validateSleevForm()
	if err != nil {
		s.sleevFormErr = err.Error()
		return nil
	}

	if apiKey != "" {
		if store := secret.Default(); store != nil {
			if err := store.Set(llm.SleevAPIKeyEnvVar, apiKey); err != nil {
				s.sleevFormErr = err.Error()
				return nil
			}
		}
		os.Setenv(llm.SleevAPIKeyEnvVar, apiKey)
	}

	if s.store == nil {
		s.sleevFormErr = "provider store is not loaded"
		return nil
	}
	if err := s.store.SetSleev(cfg); err != nil {
		s.sleevFormErr = err.Error()
		return nil
	}
	s.closeSleevForm()

	// Repaint the row the form was opened from, so "on" is visible without a
	// reload that would refetch every other provider's models.
	status := s.sleevRowStatus()
	for i := range s.allProviders {
		if s.allProviders[i].Provider != llm.Sleev {
			continue
		}
		for j := range s.allProviders[i].AuthMethods {
			s.allProviders[i].AuthMethods[j].Status = status
		}
	}
	s.rebuildVisibleItems()

	enabled := cfg.Enabled
	return func() tea.Msg { return sleevSavedMsg{Enabled: enabled} }
}
