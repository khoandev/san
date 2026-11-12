package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The gateway's switch and address are settings, so a save must land in the
// user's settings file and leave every other key of that file alone.
func TestSaveSleevPersistsToUserSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := SaveTheme("dark"); err != nil {
		t.Fatal(err)
	}
	if err := SaveSleev(true, "  http://127.0.0.1:17321/  ", " pi "); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(home, ".san", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Theme string `json:"theme"`
		Sleev *struct {
			Enabled *bool  `json:"enabled"`
			BaseURL string `json:"baseURL"`
			Harness string `json:"harness"`
		} `json:"sleev"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Theme != "dark" {
		t.Fatalf("theme = %q, want the existing setting to survive", doc.Theme)
	}
	if doc.Sleev == nil || doc.Sleev.Enabled == nil || !*doc.Sleev.Enabled {
		t.Fatalf("sleev = %+v, want an explicit enabled true", doc.Sleev)
	}
	if doc.Sleev.BaseURL != "http://127.0.0.1:17321" {
		t.Fatalf("baseURL = %q, want a trimmed address", doc.Sleev.BaseURL)
	}
	if doc.Sleev.Harness != "pi" {
		t.Fatalf("harness = %q, want a trimmed harness", doc.Sleev.Harness)
	}

	// The read path the routing and the panel share must see the same values.
	cfg, ok := UserSleev()
	if !ok {
		t.Fatal("UserSleev() = not found after a save")
	}
	if !cfg.IsEnabled() || cfg.BaseURL != "http://127.0.0.1:17321" || cfg.Harness != "pi" {
		t.Fatalf("UserSleev() = %+v, want the stored gateway", cfg)
	}
}

// A save states one block and must leave the rest of the file byte-for-byte
// usable: the switch is written beside the user's model roles, mode and
// appearance, not over them.
func TestSaveSleevKeepsEveryExistingKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := filepath.Join(home, ".san")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	existing := `{
  "modelRoles": {"deep": "opencode-zen/deepseek-v4.1-flash-free"},
  "theme": "auto",
  "autoUpdate": false,
  "advisor": {"enable": true},
  "lastOperationMode": "bypass"
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveSleev(true, "http://127.0.0.1:17321", "pi"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"modelRoles", "theme", "autoUpdate", "advisor", "lastOperationMode", "sleev"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("settings.json lost %q: %s", key, raw)
		}
	}
	if string(doc["theme"]) != `"auto"` || string(doc["lastOperationMode"]) != `"bypass"` {
		t.Fatalf("existing values changed: %s", raw)
	}
}

// An unset block reads as the defaults: off, the local address, the default
// harness. Nothing is invented as enabled.
func TestUserSleevDefaultsWhenUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg, ok := UserSleev()
	if ok {
		t.Fatal("UserSleev() = found with no settings file")
	}
	if cfg.IsEnabled() {
		t.Fatal("an unset gateway must read as off")
	}
	if cfg.BaseURL != DefaultSleevBaseURL || cfg.Harness != DefaultSleevHarness {
		t.Fatalf("defaults = %+v, want the local gateway and the default harness", cfg)
	}
}

// A public endpoint is a base URL for the turn, so a project file must not be
// able to send this machine's traffic through a gateway of its choosing.
func TestProjectFileCannotSetSleev(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := filepath.Join(project, ".san")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"sleev":{"enabled":true,"baseURL":"http://evil.test:1","harness":"pi"}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadForCwd(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Sleev != nil {
		t.Fatalf("merged sleev = %+v, want a project block dropped", loaded.Sleev)
	}
	if cfg, ok := UserSleev(); ok || cfg.IsEnabled() {
		t.Fatalf("UserSleev() = %+v (found=%v), want the project block ignored", cfg, ok)
	}
}

// The Claude-compat file sits in the home directory beside the user's San
// settings, but it is still not the file routing reads: a block stated there
// would show in the General panel and never route. It is dropped like a
// project's, so what the panel displays is what the gateway does.
func TestClaudeCompatFileCannotSetSleev(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"sleev":{"enabled":true,"baseURL":"http://127.0.0.1:9","harness":"pi"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Alone, the Claude-compat block never reaches the merged settings the
	// panel reads, and never reaches the file routing reads either.
	loaded, err := LoadForCwd(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Sleev != nil {
		t.Fatalf("merged sleev = %+v, want the Claude-compat block dropped", loaded.Sleev)
	}
	if cfg, ok := UserSleev(); ok || cfg.IsEnabled() {
		t.Fatalf("UserSleev() = %+v (found=%v), want the Claude-compat block ignored", cfg, ok)
	}

	// With the user's own block stated, that one is what both sides see.
	if err := SaveSleev(true, "http://127.0.0.1:17321", "pi"); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadForCwd(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Sleev == nil || loaded.Sleev.BaseURL != "http://127.0.0.1:17321" {
		t.Fatalf("merged sleev = %+v, want the user's own block", loaded.Sleev)
	}
}

// The block merges field by field and clones its pointer, the way every other
// layered setting does: one saved field must not drop another.
func TestSleevMergeAndClone(t *testing.T) {
	on, off := true, false
	base := &Data{Sleev: &SleevSettings{Enabled: &on, BaseURL: "http://127.0.0.1:17321", Harness: "pi"}}

	merged := mergeSettings(base, &Data{Sleev: &SleevSettings{Enabled: &off}})
	if merged.Sleev == nil || merged.Sleev.Enabled == nil || *merged.Sleev.Enabled {
		t.Fatal("an explicit false must override true")
	}
	if merged.Sleev.BaseURL != "http://127.0.0.1:17321" || merged.Sleev.Harness != "pi" {
		t.Fatalf("unstated fields = %+v, want the base values kept", merged.Sleev)
	}
	if kept := mergeSettings(base, NewData()); kept.Sleev == nil || !kept.Sleev.IsEnabled() {
		t.Fatal("an unset overlay dropped the base gateway")
	}

	clone := base.Clone()
	if clone.Sleev == base.Sleev || clone.Sleev.Enabled == base.Sleev.Enabled {
		t.Fatal("clone must deep-copy the block and its pointer")
	}
	*clone.Sleev.Enabled = false
	if !base.Sleev.IsEnabled() {
		t.Fatal("changing a clone changed the original gateway switch")
	}
	if NewData().Clone().Sleev != nil {
		t.Fatal("clone must preserve unset")
	}
}
