package setting

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/proc"
)

// Loader handles loading and merging settings from multiple sources.
type Loader struct {
	userDir      string
	projectDir   string
	cwd          string // target cwd, for local overrides
	claudeCompat bool
	// untrusted keeps only the restrictive parts of project settings; see
	// IsWorkspaceTrusted.
	untrusted bool
}

// Scope selects which settings file a single-file read or write targets.
type Scope string

const (
	ScopeUser    Scope = "user"    // ~/.san
	ScopeProject Scope = "project" // <cwd>/.san
)

// dir is the settings directory for scope.
func (l *Loader) dir(scope Scope) string {
	if scope == ScopeUser {
		return l.userDir
	}
	return l.projectDir
}

// NewLoader creates a loader with default paths (~/.san, .san) and Claude compatibility enabled.
func NewLoader() *Loader {
	return NewLoaderForCwd(".")
}

// NewLoaderWithOptions creates a loader with custom options.
func NewLoaderWithOptions(userDir, projectDir string, claudeCompat bool) *Loader {
	return &Loader{
		userDir:      userDir,
		projectDir:   projectDir,
		cwd:          filepath.Dir(projectDir),
		claudeCompat: claudeCompat,
	}
}

// NewLoaderForCwd creates a loader rooted at the provided working directory.
func NewLoaderForCwd(cwd string) *Loader {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Logger().Warn("failed to determine home directory, user-level settings will be unavailable", zap.Error(err))
	}
	userDir := ""
	if homeDir != "" {
		userDir = confdir.Dir(homeDir)
	}
	cwd = absPath(cwd)
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	root := confdir.RepoRoot(cwd)
	return &Loader{
		userDir:      userDir,
		projectDir:   confdir.Dir(root),
		cwd:          absPath(cwd),
		claudeCompat: true,
		untrusted:    !IsWorkspaceTrusted(cwd),
	}
}

// Load loads and merges settings from all sources in priority order (lowest to highest):
//  1. ~/.claude/settings.json
//  2. ~/.san/settings.json
//  3. .claude/settings.json
//  4. .san/settings.json
//  5. .claude/settings.local.json
//  6. .san/settings.local.json
func (l *Loader) Load() (*Data, error) {
	homeDir, _ := os.UserHomeDir()

	// Two-phase loading: first load Claude-compat settings, then San-native.
	// For hooks, San-native settings override Claude-compat settings per event
	// to prevent incompatible hooks (e.g., Claude Code's interactive protocol)
	// from blocking San's own hooks.

	var sources []settingsSource
	if l.claudeCompat && homeDir != "" {
		sources = append(sources, settingsSource{path: filepath.Join(homeDir, ".claude", "settings.json"), claudeCompat: true})
	}
	if l.userDir != "" {
		sources = append(sources, settingsSource{path: filepath.Join(l.userDir, "settings.json")})
	}
	sources = append(sources, l.projectSettingsFiles()...)

	// Collect hooks separately: Claude-compat hooks and San-native hooks.
	// For native hooks, higher-priority sources REPLACE lower-priority sources
	// per event (project overrides user, local overrides project).
	claudeHooks := make(map[string][]Hook)
	nativeHooks := make(map[string][]Hook) // last write wins per event

	settings := NewData()
	for _, src := range sources {
		data, err := os.ReadFile(src.path)
		if err != nil {
			continue
		}
		var s Data
		if err := json.Unmarshal(data, &s); err != nil {
			log.Logger().Warn("failed to parse config file", zap.String("path", src.path), zap.Error(err))
			continue
		}
		// Fold before merging, not after: each file's retired names collapse
		// onto their rung here, so the later file's rung outranks the earlier
		// one's the way every other setting does. Folding the merged map would
		// instead let the user's retired name beat the project's new one.
		foldSettings(&s)
		if src.project && l.untrusted && !(src.local && UntrackedInRepo(src.path)) {
			s = Data{Permissions: PermissionSettings{Deny: s.Permissions.Deny, Ask: s.Permissions.Ask}}
		}

		// Extract hooks before merging — we'll merge hooks manually
		srcHooks := s.Hooks
		s.Hooks = nil
		settings = mergeSettings(settings, &s)

		// Accumulate hooks by source type.
		// Native hooks: higher-priority sources replace lower-priority per event.
		// This means project .san/settings.json can set "PermissionRequest": []
		// to disable user-level PermissionRequest hooks.
		for event, hooks := range srcHooks {
			if src.claudeCompat {
				claudeHooks[event] = append(claudeHooks[event], hooks...)
			} else {
				nativeHooks[event] = hooks
			}
		}
	}

	// Merge hooks: for each event, use native hooks if available, otherwise use Claude-compat hooks.
	// PermissionRequest hooks are NEVER inherited from Claude-compat sources because
	// Claude Code's interactive permission protocol (e.g., vibe-island-bridge) is
	// incompatible with San's TUI-based approval flow and can cause hangs.
	merged := make(map[string][]Hook)
	for event, hooks := range claudeHooks {
		if event == "PermissionRequest" {
			continue // skip — incompatible protocol
		}
		if _, hasNative := nativeHooks[event]; !hasNative {
			merged[event] = hooks
		}
	}
	for event, hooks := range nativeHooks {
		if len(hooks) > 0 {
			merged[event] = hooks
		}
		// Empty hooks array means explicitly disabled — don't add to merged
	}
	settings.Hooks = merged

	return settings, nil
}

type settingsSource struct {
	path         string
	claudeCompat bool
	project      bool // from the working directory, not the user's home
	local        bool // a settings.local.json
}

// projectSettingsFiles lists the project's settings files, lowest priority
// first: .claude/settings.json, .san/settings.json and their .local twins.
func (l *Loader) projectSettingsFiles() []settingsSource {
	var files []settingsSource
	for _, local := range []bool{false, true} {
		name := "settings.json"
		dir := l.projectDir
		if local {
			name = "settings.local.json"
			dir = confdir.Dir(l.cwd)
		}
		if l.claudeCompat {
			files = append(files, settingsSource{path: filepath.Join(filepath.Dir(dir), ".claude", name), claudeCompat: true, project: true, local: local})
		}
		files = append(files, settingsSource{path: filepath.Join(dir, name), project: true, local: local})
	}
	// A linked worktree also reads the main checkout's local files, last, since
	// that is where grants are saved.
	if shared := confdir.LocalDir(l.cwd); shared != confdir.Dir(absPath(l.cwd)) {
		if l.claudeCompat {
			files = append(files, settingsSource{path: filepath.Join(filepath.Dir(shared), ".claude", "settings.local.json"), claudeCompat: true, project: true, local: true})
		}
		files = append(files, settingsSource{path: filepath.Join(shared, "settings.local.json"), project: true, local: true})
	}
	return files
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// LoadFile loads settings from a specific file.
func (l *Loader) LoadFile(path string) (*Data, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Data
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	foldSettings(&s)
	return &s, nil
}

// SaveToUser saves settings to the user-level settings file, merging with existing.
func (l *Loader) SaveToUser(settings *Data) error {
	return l.saveToFile(filepath.Join(l.userDir, "settings.json"), settings)
}

func (l *Loader) saveToFile(path string, settings *Data) error {
	existing := NewData()
	if err := atomicfile.ReadJSON(path, existing); err != nil {
		return err
	}
	merged := mergeSettings(existing, settings)
	foldSettings(merged)
	return atomicfile.WriteJSON(path, merged, 0o644)
}

var (
	loadedSettings   *Data
	loadedSettingsMu sync.Mutex
)

// invalidateLoaded drops the cached settings so the next Load reads disk.
func invalidateLoaded() {
	loadedSettingsMu.Lock()
	loadedSettings = nil
	loadedSettingsMu.Unlock()
}

// Load loads settings using the default loader (cached after first call).
func Load() (*Data, error) {
	loadedSettingsMu.Lock()
	defer loadedSettingsMu.Unlock()
	if loadedSettings != nil {
		return loadedSettings, nil
	}
	s, err := NewLoader().Load()
	if err != nil {
		return nil, err
	}
	loadedSettings = s
	return s, nil
}

// LoadForCwd loads settings for the provided working directory without using
// the process-global cache. This is used when the session cwd changes.
func LoadForCwd(cwd string) (*Data, error) {
	return NewLoaderForCwd(cwd).Load()
}

// defaultData returns default settings without loading from disk.
func defaultData() *Data {
	return NewData()
}

// UpdateDisabledToolsAt updates disabled tools at user level (true) or project
// level (false). It replaces the whole disabledTools block rather than routing
// through the merging save: the caller passes the complete desired map for the
// level, and merging would OR it with the file's previous keys so a re-enable
// (a removed key) or a default-disabled tool toggled back off could never
// persist. Cross-level layering still ORs on Load by design.
func UpdateDisabledToolsAt(disabledTools map[string]bool, scope Scope) error {
	return updateSettingsFile(scope, func(d *Data) {
		d.DisabledTools = disabledTools
	})
}

// updateSettingsFile reads the settings file at the requested level (true =
// user-wide, false = project-local), lets mutate replace a single block, and
// writes it back — leaving every other setting in the file untouched. It
// deliberately does NOT route through SaveToUser (which merges):
// that merger ORs the boolean fields, so reusing it would OR the new config
// with the file's own previous value and a true→false toggle (e.g. disabling an
// arm from /config) could never be persisted. Replacing the block lets the
// off-toggle stick. (Cross-level layering still ORs on Load by design —
// disabling at a lower-priority level cannot override an enable at a
// higher-priority one.)
func updateSettingsFile(scope Scope, mutate func(*Data)) error {
	loader := NewLoader()
	path := filepath.Join(loader.dir(scope), "settings.json")

	existing := NewData()
	if err := atomicfile.ReadJSON(path, existing); err != nil {
		return err
	}
	// Fold before the mutator, not only after it: a mutator that replaces a
	// block must see the file's legacy spellings already migrated, or replacing
	// advisor would drop a model pin the fold would have moved onto
	// agents.roles. The fold after the write stays, for whatever the mutator
	// writes in a retired spelling.
	foldSettings(existing)
	mutate(existing)
	// Every write folds too, so a legacy key left in the file by an older san
	// is migrated by any save instead of waiting for a hand edit.
	foldSettings(existing)
	if err := atomicfile.WriteJSON(path, existing, 0o644); err != nil {
		return err
	}

	invalidateLoaded()
	return nil
}

// UpdateSelfLearnAt persists the L1 self-learning config at the requested
// ModelRoleName normalizes and validates a role name for modelRoles. Names are
// one namespace, not two spellings of one namespace, so the returned name is
// lowercase; a name that cannot be a role is an error the caller shows inline
// rather than a silent binding that never resolves. A retired per-agent name is
// stored under the rung that answers its work, so a write never puts a dead key
// back into the file.
func ModelRoleName(name string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if !ValidModelRoleName(canonical) {
		return "", fmt.Errorf("invalid role name %q: use lowercase letters, digits and hyphens, starting with a letter", name)
	}
	return core.CanonicalModelRole(canonical), nil
}

// ValidateModelRole checks one binding: the role name and its model reference.
// The reference is validated as non-empty only — whether "@slow" or
// "vendor/model" resolves depends on the connected providers, and an
// unresolvable reference falls back to the session model rather than failing.
func ValidateModelRole(name, ref string) error {
	if _, err := ModelRoleName(name); err != nil {
		return err
	}
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("role %q: a model reference is required (use %q for the session model)", name, ModelInheritRef)
	}
	return nil
}

// ModelInheritRef is the reference that means "the session model", the same
// spelling a subagent's model field uses. Empty means the same on read; the
// setter requires one of the two spellings so clearing a role is explicit.
const ModelInheritRef = "inherit"

// UpdateModelRolesAt replaces the whole modelRoles block in scope's settings
// file, leaving every other setting in the file untouched. It replaces rather
// than merges so unbinding a role (a key removed from the map) actually
// persists at this level; cross-level layering still merges per key on Load, so
// a project may bind some roles and inherit the user's for the rest. Names are
// canonicalized to lowercase and every entry is validated before anything
// touches disk.
func UpdateModelRolesAt(roles map[string]string, scope Scope) error {
	canonical := make(map[string]string, len(roles))
	for name, ref := range roles {
		if err := ValidateModelRole(name, ref); err != nil {
			return err
		}
		key, _ := ModelRoleName(name)
		canonical[key] = strings.TrimSpace(ref)
	}
	return updateSettingsFile(scope, func(d *Data) {
		d.ModelRoles = canonical
	})
}

// SetModelRole binds one role to a model reference at scope, keeping every
// other binding in the file. A reference that is empty or "inherit" is stored
// as "inherit": the role follows the session model again, and the explicit
// spelling says so in the file instead of leaving a bare key.
func SetModelRole(name, ref string, scope Scope) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = ModelInheritRef
	}
	if err := ValidateModelRole(name, ref); err != nil {
		return err
	}
	key, _ := ModelRoleName(name)
	return updateSettingsFile(scope, func(d *Data) {
		if d.ModelRoles == nil {
			d.ModelRoles = make(map[string]string)
		}
		clearRoleKey(d.ModelRoles, key)
		d.ModelRoles[key] = ref
	})
}

// ClearModelRole removes one role's binding at scope, returning it to the
// session model. Other bindings and other settings in the file are untouched;
// clearing a role that is not bound is not an error. The role's effort goes
// with it: an effort without a model of its own would silently apply to
// whatever the role inherits next.
func ClearModelRole(name string, scope Scope) error {
	key, err := ModelRoleName(name)
	if err != nil {
		return err
	}
	return updateSettingsFile(scope, func(d *Data) {
		clearRoleKey(d.ModelRoles, key)
		clearRoleKey(d.ModelRoleEfforts, key)
	})
}

// ModelRoleEffortName normalizes an effort label for modelRoleEfforts. Labels
// are provider-native ("low", "high", "xhigh") and case is folded, so a
// hand-edited "High" still means high. An unknown-but-well-formed label is
// kept: a vendor that introduces a rung must work without a san release, and
// the resolver validates it against the model it is sent to.
func ModelRoleEffortName(effort string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(effort))
	if canonical == "" {
		return "", fmt.Errorf("invalid effort %q: use a provider effort label such as low, medium or high", effort)
	}
	for _, r := range canonical {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", fmt.Errorf("invalid effort %q: use lowercase letters, digits and hyphens", effort)
		}
	}
	return canonical, nil
}

// ValidateModelRoleEffort checks one effort binding: the role name and the
// label. The label is validated by shape only — the rungs a model offers come
// from the live catalog, and the resolver falls back to that model's default
// when the label is not on its ladder.
func ValidateModelRoleEffort(name, effort string) error {
	if _, err := ModelRoleName(name); err != nil {
		return err
	}
	_, err := ModelRoleEffortName(effort)
	return err
}

// SetModelRoleEffort binds one role to a reasoning effort at scope, keeping
// every other binding in the file. The role does not need a model binding of
// its own: an inherited role may still ask for a different depth. An effort of
// "" or a label equal to "default" clears the binding, which is how the UI
// says "use the model's own default".
func SetModelRoleEffort(name, effort string, scope Scope) error {
	effort = strings.TrimSpace(effort)
	if effort == "" || strings.EqualFold(effort, ModelEffortDefaultRef) {
		return ClearModelRoleEffort(name, scope)
	}
	if err := ValidateModelRoleEffort(name, effort); err != nil {
		return err
	}
	key, _ := ModelRoleName(name)
	canonical, _ := ModelRoleEffortName(effort)
	return updateSettingsFile(scope, func(d *Data) {
		if d.ModelRoleEfforts == nil {
			d.ModelRoleEfforts = make(map[string]string)
		}
		clearRoleKey(d.ModelRoleEfforts, key)
		d.ModelRoleEfforts[key] = canonical
	})
}

// ModelEffortDefaultRef is the effort spelling the pickers offer for "no
// binding": the role answers at whatever rung the model itself defaults to.
const ModelEffortDefaultRef = "default"

// ClearModelRoleEffort removes one role's effort binding at scope, returning
// it to the model's own default. Clearing a role that has no effort bound is
// not an error, and the role's model binding is left untouched.
func ClearModelRoleEffort(name string, scope Scope) error {
	key, err := ModelRoleName(name)
	if err != nil {
		return err
	}
	return updateSettingsFile(scope, func(d *Data) {
		clearRoleKey(d.ModelRoleEfforts, key)
	})
}

// UpdateSelfLearnAt persists the L1 self-learning config at the requested
// settings level, syncing the Evolve tool toggle with the enabled capabilities.
// Returns Validate's error verbatim if the new config is illegal (§3.1) so the
// caller can surface it inline before touching disk.
func UpdateSelfLearnAt(cfg SelfLearnSettings, scope Scope) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return updateSettingsFile(scope, func(d *Data) {
		d.SelfLearn = cfg
		if d.DisabledTools == nil {
			d.DisabledTools = make(map[string]bool)
		}
		d.DisabledTools["Evolve"] = !cfg.Skills.Active()
	})
}

// UpdateAutoPilotAt persists the autopilot config at the requested settings
// level, rewriting only the autoPilot block.
func UpdateAutoPilotAt(cfg AutoPilotSettings, scope Scope) error {
	return updateSettingsFile(scope, func(d *Data) { d.AutoPilot = cfg })
}

// UpdateAdvisorAt persists the advisor config at the requested settings level,
// rewriting only the advisor block. It replaces the block rather than merging
// into it so a knob turned back to its default (an explicit false becoming
// absent, a raised bound coming back down) actually persists at this level;
// cross-level layering still applies on Load. Validate runs first, so a bad
// mode or grouping is reported before anything touches disk.
func UpdateAdvisorAt(cfg AdvisorSettings, scope Scope) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return updateSettingsFile(scope, func(d *Data) { d.Advisor = cfg })
}

// ValidateAgentRoles checks an agents.roles map before it is written: every
// value must be a role name, with the "@" an agent definition's model field
// carries allowed but not required, and every key must name an agent. An agent
// whose role is unknown is rejected here rather than stored as a slot that
// silently resolves nothing; an agent name is only required to be non-empty,
// because a hand-edited file may route a plugin agent whose name the built-ins
// do not describe.
func ValidateAgentRoles(roles map[string]string) error {
	for name, value := range roles {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("agents.roles: an agent name is required")
		}
		role := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "@"))
		if _, err := ModelRoleName(role); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
	}
	return nil
}

// UpdateAgentsAt persists the agents tab's settings at the requested level in
// one read-modify-write: the agents.roles bindings and the advisor block, so a
// save that turns the advisor on and moves an agent's role cannot land half
// applied. It rewrites only those two blocks, leaving every other setting in
// the file untouched, and it replaces agent roles wholesale at this level the
// way UpdateModelRolesAt does — a role removed from the map must stop being
// assigned, which a merge could not express. Cross-level layering still merges
// per key on Load, so a project may pin the agents it cares about.
//
// It takes the roles map rather than an AgentsSettings: the tab does not own
// agents.autoTier, and a signature that accepted the whole block would let a
// caller believe it had saved that switch. updateSettingsFile folds the file
// before this mutator runs, so a legacy advisor.model has already moved onto
// agents.roles.advisor by the time the block is replaced; a caller passing the
// effective bindings (as the panel does) therefore keeps the assignment.
func UpdateAgentsAt(roles map[string]string, advisor AdvisorSettings, scope Scope) error {
	if err := ValidateAgentRoles(roles); err != nil {
		return err
	}
	if err := advisor.Validate(); err != nil {
		return err
	}
	// Fold before writing, so the file holds canonical role names — a retired
	// name lands on the rung that answers its work, the same way a modelRoles
	// write folds.
	folded := foldAgentRoleMap(roles)
	return updateSettingsFile(scope, func(d *Data) {
		// A saved advisor role supersedes a legacy concrete pin: the pin can no
		// longer decide what answers, so keeping it would only mislead a reader
		// of the file. With no advisor role assigned and no model passed, the
		// file's own pin stands — the tab assigns roles and has no control for
		// a concrete model, so its absence is "no opinion", not "clear it".
		if _, assigned := folded[AgentAdvisor]; assigned {
			advisor.Model = ""
		} else if strings.TrimSpace(advisor.Model) == "" {
			advisor.Model = d.Advisor.Model
		}
		d.Agents.Roles = folded
		d.Advisor = advisor
	})
}

// UpdateLastOperationMode persists the user-wide mode restored when a new
// session starts.
func UpdateLastOperationMode(mode OperationMode) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.LastOperationMode = mode.PersistenceName() })
}

// AutoPilotPresetDir is the folder where /autopilot Export saves named configs
// and Import reads them — a shared, non-session space for reusable presets.
func AutoPilotPresetDir() string {
	return filepath.Join(NewLoader().userDir, "autopilot")
}

// ExportAutoPilot writes cfg as a named preset (<presetDir>/<name>.json),
// returning the path. The name is reduced to a bare filename and must be
// non-empty.
func ExportAutoPilot(name string, cfg AutoPilotSettings) (string, error) {
	base := sanitizePresetName(name)
	if base == "" {
		return "", fmt.Errorf("preset name required")
	}
	dir := AutoPilotPresetDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, base+".json")
	// A preset is a template: where one run stood means nothing to the next.
	cfg.MissionState = ""
	if err := atomicfile.WriteJSON(path, cfg, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ListAutoPilotPresets returns the saved preset names (without extension),
// sorted. A missing directory yields an empty list, not an error.
func ListAutoPilotPresets() ([]string, error) {
	entries, err := os.ReadDir(AutoPilotPresetDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names, nil
}

// ImportAutoPilot reads a named preset back into an AutoPilotSettings.
func ImportAutoPilot(name string) (AutoPilotSettings, error) {
	var cfg AutoPilotSettings
	base := sanitizePresetName(name)
	if base == "" {
		return cfg, fmt.Errorf("preset name required")
	}
	data, err := os.ReadFile(filepath.Join(AutoPilotPresetDir(), base+".json"))
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

// sanitizePresetName reduces a user-entered name to a safe bare filename so a
// preset can never escape the preset directory.
func sanitizePresetName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".json")
	name = filepath.Base(name) // strip any path components
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// defaultDisabledTools are tools that ship disabled: rarely-used features
// whose schemas should not cost every conversation context. The /tool panel
// enables one by writing an explicit "false" entry, which overrides the
// default here.
var defaultDisabledTools = map[string]bool{
	"Cron": true,
	// Task tracker: the plan panel and its tools ship off by default. The
	// workflow guidance lives in the tools' own schemas, so re-enabling them
	// from the /tool panel brings the guidance back with them.
	"TaskCreate": true,
	"TaskGet":    true,
	"TaskUpdate": true,
	// Stopping a background agent is uncommon and destructive. It remains
	// available through /tool when a user wants the model to manage workers.
	"AgentStop": true,
	// Workflow runs a graph of subagents as one background task. It is opt-in:
	// its schema is the longest of any tool and lists every saved workflow, and
	// a model that has it will reach for it where one Agent call would do.
	"Workflow": true,
	// Self-learning is opt-in; saving /evolve capabilities enables its trigger.
	"Evolve": true,
}

// IsDefaultDisabledTool reports whether the tool ships disabled. The /tool
// panel uses this to write an explicit enable entry instead of deleting the
// key (deletion would fall back to the default).
func IsDefaultDisabledTool(name string) bool {
	return defaultDisabledTools[name] || name == secondShellTool()
}

// secondShellTool names the shell tool offered beside the default one — Bash,
// on a Windows that has Git Bash — which ships disabled. Empty when there is
// none.
func secondShellTool() string {
	if second, ok := proc.SecondShell(); ok {
		return second.Kind.ToolName()
	}
	return ""
}

// WithDefaultDisabledTools overlays the factory defaults onto explicit
// settings entries: an explicit entry (true or false) always wins; absent
// keys fall back to the default.
func WithDefaultDisabledTools(explicit map[string]bool) map[string]bool {
	result := make(map[string]bool, len(explicit)+len(defaultDisabledTools)+1)
	maps.Copy(result, defaultDisabledTools)
	if second := secondShellTool(); second != "" {
		result[second] = true
	}
	maps.Copy(result, explicit)
	for name, disabled := range result {
		if !disabled {
			delete(result, name)
		}
	}
	return result
}

// DisabledToolsAt returns disabled tools from a single settings file (not merged).
func DisabledToolsAt(scope Scope) map[string]bool {
	loader := NewLoader()
	path := filepath.Join(loader.dir(scope), "settings.json")
	s, err := loader.LoadFile(path)
	if err != nil || s.DisabledTools == nil {
		return make(map[string]bool)
	}
	result := make(map[string]bool, len(s.DisabledTools))
	maps.Copy(result, s.DisabledTools)
	return result
}

// PersonaAt returns the persona pinned in a single settings file (not merged).
func PersonaAt(cwd string, scope Scope) string {
	loader := NewLoader()
	if cwd != "" {
		loader = NewLoaderForCwd(cwd)
	}
	dir := loader.dir(scope)
	if dir == "" {
		return ""
	}
	s, err := loader.LoadFile(filepath.Join(dir, "settings.json"))
	if err != nil || s == nil {
		return ""
	}
	return s.Persona
}

// ModelRolesAt returns the role bindings in a single settings file, not the
// merged view. The /models Roles tab uses it to mark a row whose assignment
// comes from the project scope, which outranks the user's own.
func ModelRolesAt(cwd string, scope Scope) map[string]string {
	loader := NewLoader()
	if cwd != "" {
		loader = NewLoaderForCwd(cwd)
	}
	dir := loader.dir(scope)
	if dir == "" {
		return nil
	}
	s, err := loader.LoadFile(filepath.Join(dir, "settings.json"))
	if err != nil || s == nil || len(s.ModelRoles) == 0 {
		return nil
	}
	roles := make(map[string]string, len(s.ModelRoles))
	for name, ref := range s.ModelRoles {
		if key, err := ModelRoleName(name); err == nil {
			roles[key] = strings.TrimSpace(ref)
		}
	}
	return roles
}

// ModelRoleEffortsAt returns the role→effort bindings in a single settings
// file, not the merged view — the mirror of ModelRolesAt, so the /models Roles
// tab can mark a row whose depth comes from the project scope. Names and labels
// are canonicalized; an entry that cannot name a role is skipped rather than
// failing the whole read.
func ModelRoleEffortsAt(cwd string, scope Scope) map[string]string {
	loader := NewLoader()
	if cwd != "" {
		loader = NewLoaderForCwd(cwd)
	}
	dir := loader.dir(scope)
	if dir == "" {
		return nil
	}
	s, err := loader.LoadFile(filepath.Join(dir, "settings.json"))
	if err != nil || s == nil || len(s.ModelRoleEfforts) == 0 {
		return nil
	}
	efforts := make(map[string]string, len(s.ModelRoleEfforts))
	for name, effort := range s.ModelRoleEfforts {
		key, err := ModelRoleName(name)
		if err != nil {
			continue
		}
		label, err := ModelRoleEffortName(effort)
		if err != nil {
			continue
		}
		efforts[key] = label
	}
	return efforts
}

// LoadTheme returns the configured theme string, or "" if none is set.
func LoadTheme() string {
	s, err := Load()
	if err != nil || s == nil {
		return ""
	}
	return s.Theme
}

// SaveTheme persists the chosen theme to ~/.san/settings.json.
func SaveTheme(t string) error {
	if err := NewLoader().SaveToUser(&Data{Theme: t}); err != nil {
		return err
	}
	invalidateLoaded()
	return nil
}

// SaveIconStyle persists the TUI icon-style choice to ~/.san/settings.json,
// replacing the field. Callers pass one of the IconStyle* constants; an
// unrecognized value resolves to the default on read (see IconStyleKind).
// Where the style is a per-user display preference, the choice is written to
// the user file only — a project overlay never sets it.
func SaveIconStyle(style string) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.IconStyle = style })
}

// SaveContextBar persists the context-usage bar on/off choice to
// ~/.san/settings.json. The value is written as an explicit pointer so an
// "off" choice overrides any inherited "on" instead of being dropped as a
// zero value (see Data.ContextBar).
func SaveContextBar(on bool) error {
	if err := NewLoader().SaveToUser(&Data{ContextBar: &on}); err != nil {
		return err
	}
	invalidateLoaded()
	return nil
}

// SaveGitBar persists the optional Git status row to ~/.san/settings.json.
// An explicit false is retained so switching the bar off survives a reload.
func SaveGitBar(on bool) error {
	if err := NewLoader().SaveToUser(&Data{GitBar: &on}); err != nil {
		return err
	}
	invalidateLoaded()
	return nil
}

// SaveSkillCommands persists whether skills are listed in the "/" command menu
// to ~/.san/settings.json, as an explicit value so hiding them sticks. The
// setting is a menu preference: a hidden skill still runs when typed.
func SaveSkillCommands(on bool) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.SkillCommands = &on })
}

// SaveThinkingDisplay persists the reasoning-display choice to
// ~/.san/settings.json, replacing the field. Callers pass one of the
// ThinkingDisplay* constants; an unrecognized value resolves to the default on
// read (see ThinkingDisplayMode).
func SaveThinkingDisplay(mode string) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.ThinkingDisplay = mode })
}

// SaveBashPreviewLines persists the shell output preview's line count to
// ~/.san/settings.json, explicitly, so 0 (no preview) survives a reload.
func SaveBashPreviewLines(n int) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.BashPreviewLines = &n })
}

// SaveAgentPreviewLines persists the Agent activity preview's line count to
// ~/.san/settings.json, explicitly, so 0 (no preview) survives a reload.
func SaveAgentPreviewLines(n int) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.AgentPreviewLines = &n })
}

// SaveAutoUpdate persists whether background auto-update is on to
// ~/.san/settings.json, as an explicit value so turning it off sticks.
func SaveAutoUpdate(on bool) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.AutoUpdate = &on })
}

// SaveAutoMemory persists the auto-memory switch at scope: ~/.san (every
// project) or the project's .san (this project, which wins).
func SaveAutoMemory(on bool, scope Scope) error {
	return updateSettingsFile(scope, func(d *Data) { d.AutoMemory = &on })
}

// AutoMemoryAt returns the autoMemory value in one settings file; nil when
// that file leaves it unset.
func AutoMemoryAt(scope Scope) *bool {
	loader := NewLoader()
	s, err := loader.LoadFile(filepath.Join(loader.dir(scope), "settings.json"))
	if err != nil {
		return nil
	}
	return s.AutoMemory
}

// SaveAllowBypass persists whether YOLO mode (bypassPermissions) is reachable
// to ~/.san/settings.json. It replaces the field rather than merging it: the
// setting is opt-out, so locking the gate means persisting an explicit false,
// which is exactly the toggle updateSettingsFile exists to make stick.
func SaveAllowBypass(on bool) error {
	return updateSettingsFile(ScopeUser, func(d *Data) { d.AllowBypass = &on })
}

// SavePersonaAt persists the chosen persona name in scope's settings file. An
// empty name clears the field.
// It bypasses mergeSettings so an empty value can actually clear it on disk.
//
// Scope matters: a project-scoped persona should save project-level so the
// selection lives with the persona (and doesn't leak to other projects, where
// it wouldn't resolve); user-scoped personas save user-level to become the
// default everywhere.
func SavePersonaAt(cwd, name string, scope Scope) error {
	loader := NewLoader()
	if cwd != "" {
		loader = NewLoaderForCwd(cwd)
	}
	dir := loader.dir(scope)
	if dir == "" {
		return os.ErrNotExist
	}
	path := filepath.Join(dir, "settings.json")

	existing := NewData()
	if err := atomicfile.ReadJSON(path, existing); err != nil {
		return err
	}
	existing.Persona = name

	if err := atomicfile.WriteJSON(path, existing, 0o644); err != nil {
		return err
	}

	invalidateLoaded()
	return nil
}
