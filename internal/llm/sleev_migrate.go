// One-time move of the Sleev gateway block out of providers.json.
//
// The block used to live beside the connections, which made a route every
// provider takes look like a provider of its own — and, worse, put a decision
// the user made in a file the app rewrites on every model change. It now lives
// in settings.json. Installs that already have the old block are moved once: the
// block is copied to the settings, the file it came from is kept beside it as a
// backup, and the key is dropped so nothing writes it twice.
package llm

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/setting"
)

// legacySleevKey is the providers.json key the gateway block used to hold.
const legacySleevKey = "sleev"

// legacySleevBackupSuffix is appended to providers.json when the old block is
// moved out of it, so the pre-migration file is still at hand.
const legacySleevBackupSuffix = ".bak"

// migrateLegacySleev moves a gateway block found in providers.json into the
// user's settings file, and removes it from providers.json.
//
// The block is read as raw JSON rather than through storeData: the struct no
// longer has the field, so decoding into it would drop the block before it
// could be moved. A missing file, a file without the key, or a block the
// settings already state all leave the file untouched — the second load after a
// migration does no work and writes no second backup.
func migrateLegacySleev(providersPath string) error {
	raw, err := os.ReadFile(providersPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var file map[string]json.RawMessage
	if err := json.Unmarshal(raw, &file); err != nil {
		// Not JSON this build understands: leave it to whoever reads it next
		// rather than replacing a file with what was parsed from broken input.
		return nil
	}
	block, ok := file[legacySleevKey]
	if !ok {
		return nil
	}

	var legacy struct {
		Enabled bool   `json:"enabled"`
		BaseURL string `json:"baseURL"`
		Harness string `json:"harness"`
	}
	if err := json.Unmarshal(block, &legacy); err != nil {
		return err
	}

	// The settings are the source of truth: a block still sitting here after a
	// save in the panel is a leftover, and copying it back would undo that save.
	if _, stated := setting.UserSleev(); !stated {
		if err := setting.SaveSleev(legacy.Enabled, legacy.BaseURL, legacy.Harness); err != nil {
			return err
		}
	}

	if err := atomicfile.Write(providersPath+legacySleevBackupSuffix, raw, 0o644); err != nil {
		return err
	}
	delete(file, legacySleevKey)
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(providersPath, append(out, '\n'), 0o644)
}

// migrateLegacySleevLogged runs the migration and reports a failure without
// failing the open: a store San can read is more useful than one it refuses,
// and the routing that needs the gateway reads the settings directly.
func migrateLegacySleevLogged(providersPath string) {
	if err := migrateLegacySleev(providersPath); err != nil {
		log.Logger().Warn("could not move the sleev block into settings.json: " + err.Error())
	}
}
