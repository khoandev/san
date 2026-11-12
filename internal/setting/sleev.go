package setting

import (
	"path/filepath"
	"strings"
)

// The Sleev gateway: one local forwarder every provider's traffic may be sent
// through.
//
// Sleev is not a provider of its own. It runs beside San on the same machine
// and forwards each request to the upstream its route names, so a routed call
// still carries the provider's own credential and San still lists the models it
// knows. The switch, the gateway's address and the harness San reports are a
// decision the user made — a setting, not something an endpoint told San about
// itself — so they live in the user's settings file.
const (
	// DefaultSleevBaseURL is the address the gateway listens on when the user
	// states none.
	DefaultSleevBaseURL = "http://127.0.0.1:17321"
	// DefaultSleevHarness is the harness San reports to the gateway when the
	// user names none. San is not one of Sleev's harnesses; "pi" is the one
	// whose base-URL model San follows, and the gateway routes on the harness
	// segment of the path.
	DefaultSleevHarness = "pi"
)

// SleevSettings stores the local gateway requests are forwarded through while
// it is enabled. There is no credential of its own: a routed request carries
// the provider's key, which is what the gateway hands to the upstream.
type SleevSettings struct {
	// Enabled routes every provider's requests through the gateway. A pointer
	// so an explicit "off" persists distinctly from "unset"; nil reads as off.
	Enabled *bool `json:"enabled,omitempty"`
	// BaseURL is the gateway's own address, e.g. http://127.0.0.1:17321.
	BaseURL string `json:"baseURL,omitempty"`
	// Harness names the client the gateway forwards for — the path segment
	// after /sleev/, and the harness whose providers must be enabled in Sleev
	// for the traffic to be optimized.
	Harness string `json:"harness,omitempty"`
}

// IsEnabled reports whether traffic is routed through the gateway.
func (s SleevSettings) IsEnabled() bool { return s.Enabled != nil && *s.Enabled }

// Normalized returns the settings with their defaults filled in: an unset
// address is the local default, its trailing slash is trimmed so the gateway
// path is never doubled, and an unset harness is the default one.
func (s SleevSettings) Normalized() SleevSettings {
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.BaseURL == "" {
		s.BaseURL = DefaultSleevBaseURL
	}
	s.Harness = strings.TrimSpace(s.Harness)
	if s.Harness == "" {
		s.Harness = DefaultSleevHarness
	}
	return s
}

// Clone deep-copies the block. A nil block stays nil, so an unset setting is
// not turned into an empty one by a clone.
func (s *SleevSettings) Clone() *SleevSettings {
	if s == nil {
		return nil
	}
	c := *s
	c.Enabled = clonePtr(s.Enabled)
	return &c
}

// mergeSleev does a field-level merge of the gateway block: the overlay's
// stated field wins, its unstated ones fall back to the base. Without this a
// save of one field would drop the others.
func mergeSleev(base, overlay *SleevSettings) *SleevSettings {
	switch {
	case base == nil:
		return overlay
	case overlay == nil:
		return base
	}
	return &SleevSettings{
		Enabled: coalescePtr(overlay.Enabled, base.Enabled),
		BaseURL: coalesce(overlay.BaseURL, base.BaseURL),
		Harness: coalesce(overlay.Harness, base.Harness),
	}
}

// UserSleev reads the gateway block from the user's settings file alone. A
// project file never answers: routing this machine's traffic through a gateway
// is the user's decision, so a cloned repository cannot turn it on. The second
// result is false when no file states the block.
func UserSleev() (SleevSettings, bool) {
	loader := NewLoader()
	if loader.userDir == "" {
		return SleevSettings{}.Normalized(), false
	}
	doc, err := loader.LoadFile(filepath.Join(loader.userDir, "settings.json"))
	if err != nil || doc.Sleev == nil {
		return SleevSettings{}.Normalized(), false
	}
	return doc.Sleev.Normalized(), true
}

// SaveSleev persists the gateway switch, address and harness at the user
// level, replacing the block wholesale: a save states all three, so an address
// cleared in the panel falls back to the default instead of keeping the old
// one. Every other setting in the file is left as it is.
func SaveSleev(enabled bool, baseURL, harness string) error {
	cfg := SleevSettings{Enabled: &enabled, BaseURL: baseURL, Harness: harness}.Normalized()
	return updateSettingsFile(ScopeUser, func(d *Data) {
		d.Sleev = &cfg
	})
}
