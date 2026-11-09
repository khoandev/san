package subagent

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/markdown"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// bundledAgents holds the default agent definitions compiled into the binary, so
// a fresh install has a usable set before the user writes one. They are the
// lowest priority source: any file under ~/.san/agents, .san/agents or a plugin
// with the same name replaces one, and agents.json can switch one off.
//
//go:embed agents/*.md
var bundledAgents embed.FS

// bundledAgentDir is the folder name inside bundledAgents.
const bundledAgentDir = "agents"

// PluginAgentPath represents a plugin agent path with namespace metadata.
type PluginAgentPath struct {
	Path      string
	Namespace string
}

// agentSearchPath represents an agent search location with optional namespace.
type agentSearchPath struct {
	path      string
	namespace string // Default namespace for agents in this path (from plugin)
}

// additionalAgentPaths stores plugin agent paths.
var (
	additionalAgentPaths   []agentSearchPath
	additionalAgentPathsMu sync.Mutex
)

// AddPluginAgentPath adds a plugin agent path to be searched.
func AddPluginAgentPath(path, namespace string) {
	additionalAgentPathsMu.Lock()
	defer additionalAgentPathsMu.Unlock()
	additionalAgentPaths = append(additionalAgentPaths, agentSearchPath{
		path:      path,
		namespace: namespace,
	})
}

// ClearPluginAgentPaths clears all plugin agent paths.
func ClearPluginAgentPaths() {
	additionalAgentPathsMu.Lock()
	defer additionalAgentPathsMu.Unlock()
	additionalAgentPaths = nil
}

// Load creates an independent catalog without consulting global plugin paths.
func Load(cwd string, plugins []PluginAgentPath) (*Registry, error) {
	r := NewRegistry()
	if err := r.InitStores(cwd); err != nil {
		return nil, err
	}
	r.loadAgents(cwd, plugins)
	return r, nil
}

// LoadAgents loads agent definitions from standard locations.
// Note: .claude/plugins/ loading is removed - plugins are handled by the plugin system.
// Priority when the same agent name appears in several locations:
//  1. .san/agents/*.md (project level, preferred)
//  2. ~/.san/agents/*.md (user level, preferred)
//  3. .claude/agents/*.md (project level, Claude Code compatible)
//  4. ~/.claude/agents/*.md (user level, Claude Code compatible)
//  5. Plugin agent paths
//
// Registry.Register overwrites by name, so sources load lowest-priority
// first — the highest-priority definition lands last and wins.
func LoadAgents(cwd string) {
	additionalAgentPathsMu.Lock()
	plugins := make([]PluginAgentPath, len(additionalAgentPaths))
	for i, p := range additionalAgentPaths {
		plugins[i] = PluginAgentPath{Path: p.path, Namespace: p.namespace}
	}
	additionalAgentPathsMu.Unlock()
	defaultRegistry.loadAgents(cwd, plugins)
}

func (r *Registry) loadAgents(cwd string, plugins []PluginAgentPath) {
	homeDir, _ := os.UserHomeDir()

	// Bundled definitions first: Register overwrites by name, so loading them
	// ahead of every file source makes them the fallback a user definition or a
	// plugin agent overrides.
	r.loadBundled()

	priorityOrdered := []agentSearchPath{
		{path: filepath.Join(confdir.Dir(cwd), "agents")},
		{path: filepath.Join(confdir.Dir(homeDir), "agents")},
		{path: filepath.Join(cwd, ".claude", "agents")},
		{path: filepath.Join(homeDir, ".claude", "agents")},
	}
	for _, p := range plugins {
		priorityOrdered = append(priorityOrdered, agentSearchPath{path: p.Path, namespace: p.Namespace})
	}
	for _, sp := range slices.Backward(priorityOrdered) {
		r.loadDir(sp.path, sp.namespace)
	}
}

// loadDir accepts a directory of .md files or a direct file path.
func (r *Registry) loadDir(path string, namespace string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}

	// If path is a file, load it directly
	if !info.IsDir() {
		if strings.HasSuffix(path, ".md") {
			r.loadFile(path, namespace)
		}
		return
	}

	// Path is a directory, scan for .md files
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		filePath := filepath.Join(path, name)
		r.loadFile(filePath, namespace)
	}
}

func (r *Registry) loadFile(filePath string, namespace string) {
	config, err := parseAgentFile(filePath)
	if err != nil {
		log.Logger().Debug("Failed to parse agent file",
			zap.String("path", filePath),
			zap.Error(err))
		return
	}

	if config != nil {
		if namespace != "" && !strings.Contains(config.Name, ":") {
			config.Name = namespace + ":" + config.Name
			config.Source = "plugin"
		}

		r.Register(config)
		log.Logger().Info("Loaded agent",
			zap.String("name", config.Name),
			zap.String("source", filePath))
	}
}

// frontmatterAliases are alternate key spellings accepted in agent
// frontmatter: `tools` is Claude Code's key, the hyphenated forms are the
// spellings docs/guides/writing-a-subagent.md documents, and `permissionMode`
// is what a hand-written or exported definition tends to use. Canonical keys on
// AgentConfig win when both are present.
type frontmatterAliases struct {
	Tools               ToolList       `yaml:"tools"`
	AllowedTools        ToolList       `yaml:"allowed-tools"`
	PermissionMode      PermissionMode `yaml:"permission-mode"`
	PermissionModeCamel PermissionMode `yaml:"permissionMode"`
	MaxSteps            int            `yaml:"max_steps"`
}

func (a frontmatterAliases) applyTo(config *AgentConfig) {
	if config.AllowTools == nil {
		if a.AllowedTools != nil {
			config.AllowTools = a.AllowedTools
		} else if a.Tools != nil {
			config.AllowTools = a.Tools
		}
	}
	if config.PermissionMode == "" {
		if a.PermissionMode != "" {
			config.PermissionMode = a.PermissionMode
		} else if a.PermissionModeCamel != "" {
			config.PermissionMode = a.PermissionModeCamel
		}
	}
	if config.MaxSteps <= 0 && a.MaxSteps > 0 {
		config.MaxSteps = a.MaxSteps
	}
}

// parseAgentFile parses an AGENT.md file with YAML frontmatter. The body stays
// on disk: SourceFile records it and GetSystemPrompt loads it on first use, so a
// definition whose charter is never needed costs nothing.
func parseAgentFile(filePath string) (*AgentConfig, error) {
	frontmatter, _, err := markdown.ParseFrontmatterFile(filePath)
	if err != nil {
		return nil, err
	}
	config, err := parseAgentFrontmatter(frontmatter, strings.TrimSuffix(filepath.Base(filePath), ".md"))
	if err != nil || config == nil {
		return config, err
	}

	config.SourceFile = filePath
	if config.Source == "" {
		homeDir, _ := os.UserHomeDir()
		switch {
		case strings.HasPrefix(filePath, filepath.Join(confdir.Dir(homeDir), "agents")),
			strings.HasPrefix(filePath, filepath.Join(homeDir, ".claude", "agents")):
			config.Source = "user"
		default:
			config.Source = "project"
		}
	}

	return config, nil
}

// parseAgentBytes parses an agent definition that lives in the binary rather
// than on disk. The body IS the charter, and there is no file to read later, so
// it is set here and sealed: systemPromptOnce is spent up front, which means a
// later SourceFile assignment can never replace the embedded prompt.
func parseAgentBytes(fileName string, data []byte) (*AgentConfig, error) {
	frontmatter, body, err := markdown.ParseFrontmatter(data)
	if err != nil {
		return nil, err
	}
	config, err := parseAgentFrontmatter(frontmatter, strings.TrimSuffix(fileName, ".md"))
	if err != nil || config == nil {
		return config, err
	}

	config.SystemPrompt = body
	config.systemPromptOnce.Do(func() {})
	config.Source = "built-in"

	return config, nil
}

// parseAgentFrontmatter builds an AgentConfig from an agent file's YAML
// frontmatter, applying the documented aliases and the built-in defaults.
// fallbackName names the definition when the frontmatter omits a name: the
// file's base name on disk, or the embedded file's name. An empty frontmatter
// is not an error — it is a markdown file that is not an agent definition, and
// nil is returned so the caller skips it.
func parseAgentFrontmatter(frontmatter, fallbackName string) (*AgentConfig, error) {
	if frontmatter == "" {
		return nil, nil
	}

	var config AgentConfig
	if err := yaml.Unmarshal([]byte(frontmatter), &config); err != nil {
		return nil, err
	}
	var aliases frontmatterAliases
	if err := yaml.Unmarshal([]byte(frontmatter), &aliases); err == nil {
		aliases.applyTo(&config)
	}

	config.PermissionMode = NormalizePermissionMode(string(config.PermissionMode))
	// A tier the catalog cannot name is a claim the delegating model cannot act
	// on, so a definition that declares one declares a real rung.
	config.Tier = NormalizeAgentTier(string(config.Tier))
	if !ValidAgentTier(config.Tier) {
		return nil, fmt.Errorf("invalid tier %q: use fast, standard or deep", config.Tier)
	}

	if config.Name == "" {
		config.Name = fallbackName
	}
	if config.Model == "" {
		config.Model = "inherit"
	}
	if config.MaxSteps <= 0 {
		config.MaxSteps = defaultMaxSteps
	}
	if config.PermissionMode == "" {
		config.PermissionMode = PermissionDefault
	}

	return &config, nil
}

// loadBundled registers the embedded agent definitions. It runs before every
// file source in loadAgents, so a same-named file or plugin agent overrides one;
// agents.json still decides whether a bundled agent is enabled.
func (r *Registry) loadBundled() {
	entries, err := fs.ReadDir(bundledAgents, bundledAgentDir)
	if err != nil {
		log.Logger().Warn("bundled agent definitions unavailable", zap.Error(err))
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := bundledAgentDir + "/" + entry.Name()
		data, err := bundledAgents.ReadFile(path)
		if err != nil {
			log.Logger().Debug("Failed to read bundled agent definition", zap.String("path", path), zap.Error(err))
			continue
		}
		config, err := parseAgentBytes(entry.Name(), data)
		if err != nil {
			log.Logger().Warn("Failed to parse bundled agent definition",
				zap.String("path", path), zap.Error(err))
			continue
		}
		if config != nil {
			r.Register(config)
		}
	}
}

// LoadAgentSystemPrompt loads just the system prompt (body) from an agent file.
func LoadAgentSystemPrompt(filePath string) string {
	_, body, err := markdown.ParseFrontmatterFile(filePath)
	if err != nil {
		log.Logger().Debug("Failed to read agent file for system prompt",
			zap.String("path", filePath),
			zap.Error(err))
		return ""
	}
	return body
}
