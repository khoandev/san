package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLayerMap(t *testing.T) {
	md := `
| Path | Layer | Responsibility |
| --- | --- | --- |
| ` + "`cmd/san`" + ` | ` + "`cmd`" + ` | Main CLI binary. |
| ` + "`internal/app`" + ` | ` + "`app`" + ` | TUI shell. |
| ` + "`internal/core`" + ` | ` + "`core`" + ` | Shared contracts. |
| ` + "`internal/tool`" + ` | ` + "`feature`" + ` | Built-in tools. |
| ` + "`internal/log`" + ` | ` + "`infrastructure`" + ` | Logging. |
`

	got, err := parseLayerMap(md)
	if err != nil {
		t.Fatalf("parseLayerMap() error = %v", err)
	}
	want := map[string]string{
		"cmd/san":       "cmd",
		"internal/app":  "app",
		"internal/core": "core",
		"internal/tool": "feature",
		"internal/log":  "infrastructure",
	}
	for pkg, layer := range want {
		if got[pkg] != layer {
			t.Fatalf("layer for %s = %q, want %q", pkg, got[pkg], layer)
		}
	}
}

func TestParseLayerMapRejectsUnknownLayer(t *testing.T) {
	_, err := parseLayerMap("| `internal/app` | `ui` | TUI shell. |")
	if err == nil {
		t.Fatal("parseLayerMap() error = nil, want error")
	}
}

// The embedded assignment is what keeps the lint gate working in a checkout
// with no docs tree, so it has to stand on its own: known layers only, and a row
// for every package layercheck has been taught about.
func TestEmbeddedLayerMapIsUsable(t *testing.T) {
	if len(embeddedLayerMap) == 0 {
		t.Fatal("embeddedLayerMap is empty")
	}
	for pkg, layer := range embeddedLayerMap {
		if _, ok := rank[layer]; !ok {
			t.Errorf("package %s has unknown layer %q", pkg, layer)
		}
		if !strings.HasPrefix(pkg, "internal/") && !strings.HasPrefix(pkg, "cmd/") {
			t.Errorf("package %s is outside the checked scope", pkg)
		}
	}
	for _, pkg := range []string{
		"cmd/san", "internal/app", "internal/core", "internal/tool", "internal/log",
		"internal/judge", "internal/triage",
	} {
		if _, ok := embeddedLayerMap[pkg]; !ok {
			t.Errorf("package %s has no layer", pkg)
		}
	}
}

// A missing map file is the fork's normal case, not an error.
func TestLoadLayerMapFallsBackToEmbedded(t *testing.T) {
	layerOf, err := loadLayerMap(filepath.Join(t.TempDir(), "package-map.md"))
	if err != nil {
		t.Fatalf("loadLayerMap() error = %v", err)
	}
	if layerOf["internal/app"] != embeddedLayerMap["internal/app"] {
		t.Fatalf("fallback layer for internal/app = %q, want the embedded one", layerOf["internal/app"])
	}
}

// A present file still wins, so upstream's documented assignment stays the
// source of truth wherever it exists.
func TestLoadLayerMapPrefersTheDocumentedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package-map.md")
	md := "| Path | Layer |\n| --- | --- |\n| `internal/app` | `core` |\n"
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	layerOf, err := loadLayerMap(path)
	if err != nil {
		t.Fatalf("loadLayerMap() error = %v", err)
	}
	if layerOf["internal/app"] != "core" {
		t.Fatalf("layer for internal/app = %q, want the documented \"core\"", layerOf["internal/app"])
	}
}

func TestLookupLayerInheritsNearestAncestor(t *testing.T) {
	layerOf := map[string]string{
		"internal/app": "app",
	}
	rel, layer, ok := lookupLayer(layerOf, repoModule+"/internal/app/input")
	if !ok {
		t.Fatal("lookupLayer() ok = false, want true")
	}
	if rel != "internal/app/input" || layer != "app" {
		t.Fatalf("lookupLayer() = (%q, %q), want (internal/app/input, app)", rel, layer)
	}
}
