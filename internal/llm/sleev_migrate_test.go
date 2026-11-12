package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/genai-io/san/internal/setting"
)

// legacyProvidersFile is a providers.json as an install that configured the
// gateway before it moved into the settings wrote it.
const legacyProvidersFile = `{
  "connections": {"opencode-zen": {"authMethod": "api_key", "connectedAt": "2026-01-02T03:04:05Z"}},
  "current": {"modelId": "deepseek-v4.1-flash-free", "provider": "opencode-zen", "authMethod": "api_key"},
  "sleev": {"enabled": true, "baseURL": "http://127.0.0.1:17321", "harness": "pi"}
}`

// newTestHome points HOME at a fresh directory holding the given providers.json
// and returns it.
func newTestHome(t *testing.T, providers string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".san")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", dir, err)
	}
	path := filepath.Join(dir, "providers.json")
	if err := os.WriteFile(path, []byte(providers), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return home
}

// readFileMap parses a JSON file into its top-level keys.
func readFileMap(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

// The block an older install wrote to providers.json is moved to the settings
// on the first open: the switch survives a restart there, the file it came from
// keeps every other key, and its pre-migration contents are left beside it.
func TestLegacySleevBlockMovesToSettings(t *testing.T) {
	home := newTestHome(t, legacyProvidersFile)
	providersPath := filepath.Join(home, ".san", "providers.json")

	if _, err := NewStore(); err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	cfg, found := setting.UserSleev()
	if !found {
		t.Fatal("UserSleev() = not found after the migration, want the moved block")
	}
	if !cfg.IsEnabled() || cfg.BaseURL != "http://127.0.0.1:17321" || cfg.Harness != "pi" {
		t.Fatalf("UserSleev() = %#v, want the block providers.json held", cfg)
	}

	file := readFileMap(t, providersPath)
	if _, ok := file[legacySleevKey]; ok {
		t.Fatalf("providers.json still carries %q: %v", legacySleevKey, file)
	}
	for _, key := range []string{"connections", "current"} {
		if _, ok := file[key]; !ok {
			t.Fatalf("providers.json lost %q during the migration: %v", key, file)
		}
	}

	backup, err := os.ReadFile(providersPath + legacySleevBackupSuffix)
	if err != nil {
		t.Fatalf("read the backup: %v", err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal([]byte(legacyProvidersFile), &before); err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}
	if err := json.Unmarshal(backup, &after); err != nil {
		t.Fatalf("parse the backup: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("backup = %v, want the pre-migration file %v", after, before)
	}
	if _, ok := after[legacySleevKey]; !ok {
		t.Fatalf("backup = %v, want the block kept as it was", after)
	}
}

// The second open finds no block and does nothing: no new backup, no second
// copy over a value the user has since changed in the panel.
func TestLegacySleevMigrationRunsOnce(t *testing.T) {
	home := newTestHome(t, legacyProvidersFile)
	providersPath := filepath.Join(home, ".san", "providers.json")

	if _, err := NewStore(); err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	backupPath := providersPath + legacySleevBackupSuffix
	backup, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat the backup: %v", err)
	}

	// The user turns the proxy off in the panel, then the next launch opens the
	// store twice more.
	if err := setting.SaveSleev(false, "http://127.0.0.1:9999", "pi"); err != nil {
		t.Fatalf("setting.SaveSleev() error = %v", err)
	}
	for range 2 {
		if _, err := NewStore(); err != nil {
			t.Fatalf("NewStore() error = %v", err)
		}
	}

	again, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat the backup again: %v", err)
	}
	if !again.ModTime().Equal(backup.ModTime()) || again.Size() != backup.Size() {
		t.Fatalf("backup was rewritten on a later open: %v -> %v", backup.ModTime(), again.ModTime())
	}
	cfg, _ := setting.UserSleev()
	if cfg.IsEnabled() || cfg.BaseURL != "http://127.0.0.1:9999" {
		t.Fatalf("UserSleev() = %#v, want the value the panel saved", cfg)
	}
}

// A providers.json without the block is left exactly as it was: no backup, no
// rewrite, nothing to migrate.
func TestNoLegacySleevBlockLeavesTheFileAlone(t *testing.T) {
	home := newTestHome(t, `{"connections": {}}`)
	providersPath := filepath.Join(home, ".san", "providers.json")
	before, err := os.Stat(providersPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if _, err := NewStore(); err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	if _, err := os.Stat(providersPath + legacySleevBackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("a backup was written with nothing to migrate: %v", err)
	}
	after, err := os.Stat(providersPath)
	if err != nil {
		t.Fatalf("stat after open: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("providers.json was rewritten at %v, want it untouched at %v", after.ModTime(), before.ModTime())
	}
}

// A block still sitting in providers.json after the settings already state one
// is a leftover: it is dropped without overwriting the settings.
func TestLegacySleevBlockDoesNotOverrideTheSettings(t *testing.T) {
	home := newTestHome(t, legacyProvidersFile)
	providersPath := filepath.Join(home, ".san", "providers.json")

	if err := setting.SaveSleev(false, "http://127.0.0.1:7777", ""); err != nil {
		t.Fatalf("setting.SaveSleev() error = %v", err)
	}
	if _, err := NewStore(); err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	cfg, _ := setting.UserSleev()
	if cfg.IsEnabled() || cfg.BaseURL != "http://127.0.0.1:7777" {
		t.Fatalf("UserSleev() = %#v, want the value the settings already stated", cfg)
	}
	if file := readFileMap(t, providersPath); file[legacySleevKey] != nil {
		t.Fatalf("providers.json kept the leftover block: %v", file)
	}
}
