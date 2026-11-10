package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrimFreeMarker(t *testing.T) {
	cases := map[string]string{
		"Beam Free":              "Beam",
		"Cat Jam Free":           "Cat Jam",
		"X (Free)":               "X",
		"X (free) Free":          "X",
		"Free Bird":              "Free Bird",
		"Freestyle":              "Freestyle",
		"DeepSeek V4.1 Flash":    "DeepSeek V4.1 Flash",
		"DeepSeek V4.1 Flash Fr": "DeepSeek V4.1 Flash Fr",
	}
	for in, want := range cases {
		if got := trimFreeMarker(in); got != want {
			t.Errorf("trimFreeMarker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalFreeIDKeepsTheIDItself(t *testing.T) {
	cases := map[string]string{
		"deepseek-v4.1-flash-free": "deepseek-v4.1-flash",
		"cat-jam:free":             "cat-jam",
		"Big-Pickle":               "big-pickle",
		"claude-opus-5":            "claude-opus-5",
	}
	for in, want := range cases {
		if got := canonicalFreeID(in); got != want {
			t.Errorf("canonicalFreeID(%q) = %q, want %q", in, got, want)
		}
	}
	if !isFreeModelID("deepseek-v4.1-flash-free") || !isFreeModelID("cat-jam:FREE") {
		t.Error("a free-tier ID was not recognised")
	}
	if isFreeModelID("claude-opus-5") || isFreeModelID("big-pickle") {
		t.Error("a billed ID was taken for a free one")
	}
}

// The name belongs to the model under the marker, and the data may file it
// under a different vendor than the one serving it.
func TestResolveModelNameFindsTheUnderlyingModel(t *testing.T) {
	data := lineups{
		opencodeVendorID: {Models: map[string]modelSpec{
			"beam": {Name: "Beam (Zen)"},
		}},
		"deepseek": {Models: map[string]modelSpec{
			"deepseek-v4.1-flash": {Name: "DeepSeek V4.1 Flash"},
		}},
	}

	if got, want := resolveModelName(data, opencodeVendorID, "deepseek-v4.1-flash-free", "DeepSeek V4.1 Flash Free"), "DeepSeek V4.1 Flash"; got != want {
		t.Errorf("cross-vendor resolve = %q, want %q", got, want)
	}
	if got, want := resolveModelName(data, opencodeVendorID, "beam-free", "Beam Free"), "Beam (Zen)"; got != want {
		t.Errorf("same-vendor resolve = %q, want %q", got, want)
	}
}

// A model the data does not know still loses the marker, and nothing else.
func TestResolveModelNameFallsBackToTrimmingTheMarker(t *testing.T) {
	if got, want := resolveModelName(lineups{}, "", "beam-free", "Beam Free"), "Beam"; got != want {
		t.Errorf("fallback resolve = %q, want %q", got, want)
	}
	// A listing that published no name at all falls back to the ID, whose
	// marker must survive: the free-tier gate matches on it.
	if got, want := resolveModelName(lineups{}, "", "flint-40b-free", ""), "flint-40b-free"; got != want {
		t.Errorf("ID fallback = %q, want %q", got, want)
	}
}

// Only a free-tier ID is resolved; every other model keeps the provider's
// name, even when the data would spell it differently.
func TestModelDisplayNameLeavesBilledModelsAlone(t *testing.T) {
	if got, want := ModelDisplayName(Anthropic, AuthAPIKey, "claude-opus-5", "Claude Opus 5"), "Claude Opus 5"; got != want {
		t.Errorf("ModelDisplayName() = %q, want %q", got, want)
	}
}

// The embedded Zen lineup is the source of the names the picker shows, so no
// entry may still carry the marker.
func TestEmbeddedFreeModelNamesCarryNoMarker(t *testing.T) {
	models := embeddedLineups("data/san.json")[opencodeVendorID].Models
	if len(models) == 0 {
		t.Fatal("the embedded Zen lineup is empty")
	}
	for id, spec := range models {
		if strings.Contains(strings.ToLower(spec.Name), "free") {
			t.Errorf("%s is named %q, which still carries the free marker", id, spec.Name)
		}
	}
}

// The name is cached on disk, so a later process answers without the data and
// a listing written before the correction still renders cleanly.
func TestModelNameCachePersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	resetModelNameCache()
	t.Cleanup(resetModelNameCache)

	if got, want := ModelDisplayName(OpenCodeZen, AuthAPIKey, "beam-free", "Beam Free"), "Beam"; got != want {
		t.Fatalf("ModelDisplayName() = %q, want %q", got, want)
	}
	if _, err := os.Stat(modelNamesPath()); err != nil {
		t.Fatalf("the resolved name was not cached: %v", err)
	}

	resetModelNameCache()
	if got, want := ModelDisplayName(OpenCodeZen, AuthAPIKey, "beam-free", "different"), "Beam"; got != want {
		t.Errorf("the cached name was not reused: %q, want %q", got, want)
	}
}

func TestCorruptModelNameCacheIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	resetModelNameCache()
	t.Cleanup(resetModelNameCache)

	path := modelNamesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := ModelDisplayName(OpenCodeZen, AuthAPIKey, "beam-free", "Beam Free"), "Beam"; got != want {
		t.Errorf("ModelDisplayName() = %q, want %q", got, want)
	}
}
