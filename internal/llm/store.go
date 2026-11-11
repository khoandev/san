package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
)

// What the user connected, and what they chose.
//
// The store is one JSON file — providers.json in San's config directory —
// holding the decisions a session should not have to ask about again: which
// providers are connected and how, which model is current, and the per-model
// preferences (token limits, thinking effort) that go with it. Everything a
// provider merely told San about itself is cached rather than decided; that
// half lives in modelcache.go.
//
// Several Stores are open at once — the app has one, the /models picker opens
// its own — so a write lands on disk and the others catch up via Reload.

// ConnectionInfo records that a provider is connected, and by which method.
type ConnectionInfo struct {
	AuthMethod  AuthMethod `json:"authMethod"`
	ConnectedAt time.Time  `json:"connectedAt"`
}

// CurrentModelInfo names the model San is inferring through. The provider and
// auth method travel with the ID because the same ID can be served by several
// of them, with different windows and different pricing.
type CurrentModelInfo struct {
	ModelID    string     `json:"modelId"`
	Provider   ProviderID `json:"provider"`
	AuthMethod AuthMethod `json:"authMethod"`
}

// The user-defined OpenAI-compatible provider.
//
// There is one of it, and its ID is fixed: a user-chosen name would add rename
// bookkeeping without distinguishing anything. The two names below are what
// identify it — in the store, in the secret store, and in the Providers tab —
// and they live here beside the config they key rather than in whichever
// package happens to open the connection.
const (
	// CustomProvider is the provider name the user-defined endpoint is
	// registered and stored under.
	CustomProvider ProviderID = "custom"
	// CustomAPIKeyEnvVar is where its credential is kept.
	CustomAPIKeyEnvVar = "SAN_CUSTOM_API_KEY"
)

// The Sleev gateway.
//
// Sleev is not a provider of its own: it is a local proxy every configured
// provider's traffic can be routed through, so the address below is a
// destination rather than a source of models. Its configuration lives here
// beside the custom provider's for the same reason — it is a decision the user
// made, not something an endpoint told San about itself.
const (
	// DefaultSleevBaseURL is the address the gateway listens on when the user
	// states none.
	DefaultSleevBaseURL = "http://127.0.0.1:17321"
	// DefaultSleevHarness is the harness name the gateway is told it is
	// serving, when the user names no other.
	DefaultSleevHarness = "pi"
)

// SleevConfig stores the local proxy requests are forwarded through while it is
// enabled. There is no credential of its own: a routed request carries the
// provider's key, which is what the gateway hands to the upstream.
type SleevConfig struct {
	// Enabled routes every provider's requests through the gateway. Opt-in:
	// false is the zero value.
	Enabled bool `json:"enabled"`
	// BaseURL is the gateway's own address, e.g. http://127.0.0.1:17321.
	BaseURL string `json:"baseURL,omitempty"`
	// Harness names the client the gateway forwards for, sent as the
	// sleev-harness header.
	Harness string `json:"harness,omitempty"`
}

// IsEnabled reports whether traffic is routed through the gateway. Nil-receiver
// safe, for callers that hold a config the user never saved.
func (c *SleevConfig) IsEnabled() bool { return c != nil && c.Enabled }

// Normalized returns the config with its defaults filled in: an unset address
// is the local default, and trailing slashes are trimmed so the gateway path is
// never doubled. Harness likewise falls back to its default.
func (c SleevConfig) Normalized() SleevConfig {
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.BaseURL == "" {
		c.BaseURL = DefaultSleevBaseURL
	}
	c.Harness = strings.TrimSpace(c.Harness)
	if c.Harness == "" {
		c.Harness = DefaultSleevHarness
	}
	return c
}

// CustomProviderConfig stores the user-defined OpenAI-compatible provider added
// via the /models Providers tab. The API key is not kept here — it lives in the
// secret store under CustomAPIKeyEnvVar.
type CustomProviderConfig struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseURL"`
}

// storeData is providers.json, as written.
type storeData struct {
	Connections     map[string]ConnectionInfo `json:"connections"`               // key: provider
	Models          map[string]modelCache     `json:"models"`                    // key: provider:authMethod
	Current         *CurrentModelInfo         `json:"current"`                   // current model with provider info
	SearchProvider  *string                   `json:"searchProvider,omitempty"`  // search provider name (exa, serper, brave)
	ContextLimits   map[string]contextLimit   `json:"tokenLimits,omitempty"`     // key: modelID
	ThinkingEfforts map[string]string         `json:"thinkingEfforts,omitempty"` // key: modelID; value: provider-native effort label
	CustomProvider  *CustomProviderConfig     `json:"customProvider,omitempty"`  // user-defined OpenAI-compatible provider
	Sleev           *SleevConfig              `json:"sleev,omitempty"`           // local gateway every provider may be routed through
}

// Store is providers.json, open. Every accessor locks; the data is unexported
// so nothing can read it half-written.
type Store struct {
	mu   sync.RWMutex
	path string
	data storeData
}

// NewStore opens the store, creating the config directory if it is missing. A
// file that is not there yet is not an error — it is a first run.
func NewStore() (*Store, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configDir := confdir.Dir(homeDir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}

	store := &Store{
		path: filepath.Join(configDir, "providers.json"),
		data: storeData{
			Connections: make(map[string]ConnectionInfo),
			Models:      make(map[string]modelCache),
		},
	}

	if err := store.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	return store, nil
}

// load reads providers.json into memory.
func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read provider store %s: %w", s.path, err)
	}

	if err := json.Unmarshal(data, &s.data); err != nil {
		return fmt.Errorf("parse provider store: %w", err)
	}

	s.initMaps()
	return nil
}

// Reload re-reads the store from disk, refreshing this instance's in-memory
// caches with data written by another Store instance.
//
// The provider selector operates on its own Store (a separate NewStore), so the
// model metadata it caches — display names and context-window limits — and the
// current-model choice it persists land on disk but not in the shared app-level
// Store the status bar reads. Reloading after a model switch lets the status bar
// pick up the new model's name and limit instead of falling back to the raw ID
// and "--". A missing file is not an error: nothing has been persisted yet.
func (s *Store) Reload() error {
	if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// initMaps fills in the maps an older file — or a hand-edited one — may not
// have, so every writer can assign without checking first.
func (s *Store) initMaps() {
	if s.data.Connections == nil {
		s.data.Connections = make(map[string]ConnectionInfo)
	}
	if s.data.Models == nil {
		s.data.Models = make(map[string]modelCache)
	}
	if s.data.ContextLimits == nil {
		s.data.ContextLimits = make(map[string]contextLimit)
	}
	if s.data.ThinkingEfforts == nil {
		s.data.ThinkingEfforts = make(map[string]string)
	}
}

// save writes the store back, atomically. Callers hold the lock.
func (s *Store) save() error {
	return atomicfile.WriteJSON(s.path, s.data, 0o644)
}

// Connect records that a provider is connected by this auth method.
func (s *Store) Connect(provider ProviderID, authMethod AuthMethod) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Connections[string(provider)] = ConnectionInfo{
		AuthMethod:  authMethod,
		ConnectedAt: time.Now(),
	}

	return s.save()
}

// IsConnected reports whether a provider is connected by this auth method.
func (s *Store) IsConnected(provider ProviderID, authMethod AuthMethod) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conn, ok := s.data.Connections[string(provider)]
	if !ok {
		return false
	}
	return conn.AuthMethod == authMethod
}

// Connection returns a provider's connection, if it has one.
func (s *Store) Connection(provider ProviderID) (ConnectionInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conn, ok := s.data.Connections[string(provider)]
	return conn, ok
}

// ResolveAuthMethod returns the model's own auth method, falling back to its
// provider's stored connection when the model doesn't carry one. Provider-scoped
// cache lookups (token limits, reasoning) key on provider+auth, so they resolve
// the auth this way to avoid missing the cache for a model selected without an
// explicit method.
func (s *Store) ResolveAuthMethod(current *CurrentModelInfo) AuthMethod {
	if current == nil {
		return ""
	}
	if current.AuthMethod != "" {
		return current.AuthMethod
	}
	return s.ConnectionAuthMethod(current.Provider)
}

// ConnectionAuthMethod returns the auth method of a provider's active
// connection, or "" when it has none. Nil-receiver safe, for callers that hold
// a provider but no model (llm.Client resolving its own context window).
func (s *Store) ConnectionAuthMethod(provider ProviderID) AuthMethod {
	if s == nil {
		return ""
	}
	if conn, ok := s.Connection(provider); ok {
		return conn.AuthMethod
	}
	return ""
}

// Connections returns a copy of every connection, keyed by provider.
func (s *Store) Connections() map[string]ConnectionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]ConnectionInfo, len(s.data.Connections))
	maps.Copy(result, s.data.Connections)
	return result
}

// Disconnect removes the connection for a provider.
func (s *Store) Disconnect(provider ProviderID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data.Connections, string(provider))
	return s.save()
}

// ClearCurrentModel clears the current model selection.
func (s *Store) ClearCurrentModel() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Current = nil
	return s.save()
}

// SetCurrentModel records the model San infers through, and where to reach it.
func (s *Store) SetCurrentModel(modelID string, provider ProviderID, authMethod AuthMethod) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Current = &CurrentModelInfo{
		ModelID:    modelID,
		Provider:   provider,
		AuthMethod: authMethod,
	}
	return s.save()
}

// CurrentModel returns the model San infers through, or nil if none is set.
func (s *Store) CurrentModel() *CurrentModelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.data.Current
}

// SearchProvider returns the chosen web-search backend, or "" for the default.
func (s *Store) SearchProvider() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.data.SearchProvider == nil {
		return "" // Will use default (exa)
	}
	return *s.data.SearchProvider
}

// SetSearchProvider records the web-search backend to use.
func (s *Store) SetSearchProvider(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.SearchProvider = &name
	return s.save()
}

// ThinkingEffort returns the persisted thinking effort for modelID,
// or "" when no preference has been saved (fall back to provider default).
func (s *Store) ThinkingEffort(modelID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.ThinkingEfforts[modelID]
}

// SetThinkingEffort saves the thinking effort for modelID.
// Passing "" deletes the entry so future loads fall back to the provider default.
func (s *Store) SetThinkingEffort(modelID, effort string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initMaps()
	if effort == "" {
		delete(s.data.ThinkingEfforts, modelID)
	} else {
		s.data.ThinkingEfforts[modelID] = effort
	}
	return s.save()
}

// CustomProvider returns the stored custom provider config, or nil when the
// user hasn't defined one. Returns a copy so callers can't mutate the store.
func (s *Store) CustomProvider() *CustomProviderConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.data.CustomProvider == nil {
		return nil
	}
	cfg := *s.data.CustomProvider
	return &cfg
}

// SetCustomProvider saves the custom provider config.
func (s *Store) SetCustomProvider(cfg CustomProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.CustomProvider = &cfg
	return s.save()
}

// Sleev returns the gateway config, or nil when the user never configured one.
// The copy is normalized, so a hand-edited file with an empty address still
// says where the gateway is. Returns a copy so callers can't mutate the store.
func (s *Store) Sleev() *SleevConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.data.Sleev == nil {
		return nil
	}
	cfg := s.data.Sleev.Normalized()
	return &cfg
}

// SetSleev saves the gateway config, normalizing it first so what is written
// back states its defaults rather than relying on the reader to supply them.
func (s *Store) SetSleev(cfg SleevConfig) error {
	cfg = cfg.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Sleev = &cfg
	return s.save()
}
