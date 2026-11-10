package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/confdir"
)

// What a model is called, as opposed to what it is.
//
// OpenCode Zen's free tier spells a model's ID with a -free (or :free) suffix
// — "deepseek-v4.1-flash-free" — while the name people know belongs to the
// model without it. Dropping the suffix from the ID and looking the model up
// in San's data returns the real name; a name a listing still carries the
// marker on is trimmed as well.
//
// The ID itself is never changed: the free-tier gate filters on it. Only the
// display name moves.
//
// The answer is kept in memory and on disk, because it is asked for on every
// render and must survive a model listing that a previous San wrote before the
// data was corrected.

// freeModelIDSuffixes are the spellings the free tier appends to an ID.
var freeModelIDSuffixes = []string{":free", "-free"}

// freeModelNameSuffixes are the markers a display name may still carry.
var freeModelNameSuffixes = []string{" (free)", " free"}

// canonicalFreeID drops the free-tier suffix from a model ID.
func canonicalFreeID(id string) string {
	out := strings.ToLower(strings.TrimSpace(id))
	for _, suffix := range freeModelIDSuffixes {
		out = strings.TrimSuffix(out, suffix)
	}
	return out
}

// isFreeModelID reports whether an ID belongs to the free tier: the only IDs
// whose name is resolved through the data.
func isFreeModelID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, suffix := range freeModelIDSuffixes {
		if strings.HasSuffix(id, suffix) {
			return true
		}
	}
	return false
}

// trimFreeMarker removes a trailing free-tier marker from a display name.
// "Freestyle" and "Free Bird" are untouched: only a suffix is a marker.
func trimFreeMarker(name string) string {
	for {
		lowered := strings.ToLower(name)
		trimmed := name
		for _, suffix := range freeModelNameSuffixes {
			if strings.HasSuffix(lowered, suffix) {
				trimmed = strings.TrimRight(name[:len(name)-len(suffix)], " ")
				break
			}
		}
		if trimmed == name {
			return name
		}
		name = trimmed
	}
}

// modelVendorIDs maps San's provider IDs onto the catalog vendor a model's
// data is keyed by, where the two spellings differ.
var modelVendorIDs = map[ProviderID]string{
	OpenCodeZen: opencodeVendorID,
	MinMax:      "minimax",
}

// modelVendorID returns the data key a provider's models are filed under.
func modelVendorID(provider ProviderID) string {
	if vendor, ok := modelVendorIDs[provider]; ok {
		return vendor
	}
	return string(provider)
}

// lookupModelName finds the data's name for a canonical ID. The provider's own
// vendor is searched first, then every other in lexical order, so the answer
// does not depend on map iteration.
func lookupModelName(data lineups, vendorID, canonical string) string {
	if spec, ok := data[vendorID].find(canonical); ok && spec.Name != "" {
		return spec.Name
	}
	names := make([]string, 0, len(data))
	for id := range data {
		if id != vendorID {
			names = append(names, id)
		}
	}
	sort.Strings(names)
	for _, id := range names {
		if spec, ok := data[id].find(canonical); ok && spec.Name != "" {
			return spec.Name
		}
	}
	return ""
}

// resolveModelName is the resolution without the caches: the underlying
// model's name by canonical ID, else the fallback with the marker trimmed.
func resolveModelName(data lineups, vendorID, modelID, fallback string) string {
	canonical := canonicalFreeID(modelID)
	if canonical != strings.ToLower(strings.TrimSpace(modelID)) {
		if name := lookupModelName(data, vendorID, canonical); name != "" {
			return trimFreeMarker(name)
		}
	}
	if fallback == "" {
		fallback = modelID
	}
	return trimFreeMarker(fallback)
}

// modelInfoDisplayName is the name a ModelInfo already carries, in the order
// the picker reads it.
func modelInfoDisplayName(info ModelInfo) string {
	switch {
	case info.DisplayName != "":
		return info.DisplayName
	case info.Name != "":
		return info.Name
	default:
		return info.ID
	}
}

// ModelDisplayName returns the name to show for a model. It is a no-op for
// every model outside the free tier; a free-tier ID resolves to the underlying
// model's name through San's data, falling back to the marker-trimmed name.
//
// The answer is cached per provider and auth method, on disk as well as in
// memory: the picker asks on every render, and a model listing written before
// the data was corrected must still display cleanly.
func ModelDisplayName(provider ProviderID, auth AuthMethod, modelID, fallback string) string {
	if !isFreeModelID(modelID) {
		return fallback
	}
	key := providerKey(provider, auth) + "|" + modelID
	if name, ok := cachedModelName(key); ok {
		return name
	}
	name := resolveModelName(loadedModelData(), modelVendorID(provider), modelID, fallback)
	rememberModelName(key, name)
	return name
}

// CleanModelDisplayName is ModelDisplayName for a caller that knows only the
// model and the name — the store's read-side helpers, which scan listings
// cached under every provider. The lookup prefers the canonical model over the
// provider's own vendor list, which is the right answer for a shared ID.
func CleanModelDisplayName(modelID, fallback string) string {
	if !isFreeModelID(modelID) {
		return fallback
	}
	key := "|" + modelID
	if name, ok := cachedModelName(key); ok {
		return name
	}
	name := resolveModelName(loadedModelData(), "", modelID, fallback)
	rememberModelName(key, name)
	return name
}

// ---------------------------------------------------------------------------
// The on-disk cache
// ---------------------------------------------------------------------------

// modelNamesVersion is bumped when the file's meaning changes; an older or
// newer version decodes as empty rather than as a wrong answer.
const modelNamesVersion = 1

// modelNamesFile is the file's shape.
type modelNamesFile struct {
	Version int               `json:"version"`
	Names   map[string]string `json:"names"`
}

// modelNames is the process-wide cache. loaded separates "read, and the file
// said nothing" from "not read yet", so an absent file is not re-read on every
// lookup.
var modelNames = struct {
	mu     sync.Mutex
	loaded bool
	names  map[string]string
}{}

// modelNamesPath is where the resolved names live.
func modelNamesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(confdir.Dir(home), "cache", "model-names.json")
}

func cachedModelName(key string) (string, bool) {
	modelNames.mu.Lock()
	defer modelNames.mu.Unlock()
	names := loadModelNamesLocked()
	name, ok := names[key]
	return name, ok
}

// rememberModelName records one answer and persists it. A write failure costs
// the next process a second resolution, not the current one: names resolve
// identically from the data every time.
func rememberModelName(key, name string) {
	modelNames.mu.Lock()
	defer modelNames.mu.Unlock()
	names := loadModelNamesLocked()
	if names[key] == name {
		return
	}
	names[key] = name
	writeModelNamesLocked()
}

// loadModelNamesLocked reads the cache once. A missing or unreadable file is
// an empty cache, never an error: the names are derivable, and a corrupt file
// must not cost a model its listing.
func loadModelNamesLocked() map[string]string {
	if modelNames.loaded {
		return modelNames.names
	}
	modelNames.loaded = true
	modelNames.names = make(map[string]string)

	path := modelNamesPath()
	if path == "" {
		return modelNames.names
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return modelNames.names
	}
	var file modelNamesFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != modelNamesVersion {
		return modelNames.names
	}
	for key, name := range file.Names {
		if key != "" && name != "" {
			modelNames.names[key] = name
		}
	}
	return modelNames.names
}

// writeModelNamesLocked replaces the file in one rename, so a reader never
// sees half of it. Callers hold the cache lock.
func writeModelNamesLocked() {
	path := modelNamesPath()
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(modelNamesFile{Version: modelNamesVersion, Names: modelNames.names}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".model-names-*.json")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

// resetModelNameCache forgets the in-memory cache, for tests that move HOME.
func resetModelNameCache() {
	modelNames.mu.Lock()
	defer modelNames.mu.Unlock()
	modelNames.loaded = false
	modelNames.names = nil
}
