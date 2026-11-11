package main

// embeddedLayerMap is the package → layer assignment layercheck falls back to
// when the checkout has no docs tree. This fork removed docs/ on purpose (see
// AGENTS.md), so the documentation copy the tool normally reads is absent; when
// the file is present it still wins, which keeps the tool usable on
// upstream/main.
//
// Keep it in step with docs/reference/package-map.md on upstream/main. Every new
// top-level internal/ package needs a row here — layercheck reports the ones
// that are missing rather than guessing a layer.
var embeddedLayerMap = map[string]string{
	// Entrypoints.
	"cmd/san": "cmd",

	// Application shell.
	"internal/app":         "app",
	"internal/app/conv":    "app",
	"internal/app/input":   "app",
	"internal/app/trigger": "app",
	"internal/app/kit":     "app",

	// Core contracts.
	"internal/core": "core",

	// Features: agents, orchestration, persistence.
	"internal/interaction": "feature",
	"internal/agent":       "feature",
	"internal/llm":         "feature",
	"internal/tool":        "feature",
	"internal/session":     "feature",
	"internal/transcript":  "feature",
	"internal/task":        "feature",
	"internal/todo":        "feature",
	"internal/subagent":    "feature",
	"internal/workflow":    "feature",
	"internal/broker":      "feature",
	"internal/group":       "feature",
	"internal/cron":        "feature",
	"internal/selflearn":   "feature",
	"internal/advisor":     "feature",
	"internal/plan":        "feature",
	"internal/triage":      "feature",
	"internal/judge":       "feature",

	// Features: extension surfaces.
	"internal/command":       "feature",
	"internal/skill":         "feature",
	"internal/plugin":        "feature",
	"internal/mcp":           "feature",
	"internal/mcp/transport": "feature",
	"internal/hook":          "feature",

	// Features: configuration and supporting capabilities.
	"internal/setting":   "feature",
	"internal/persona":   "feature",
	"internal/search":    "feature",
	"internal/inspector": "feature",
	"internal/reminder":  "feature",
	"internal/reviewer":  "feature",
	"internal/image":     "feature",

	// Infrastructure helpers.
	"internal/log":        "infrastructure",
	"internal/secret":     "infrastructure",
	"internal/filecache":  "infrastructure",
	"internal/markdown":   "infrastructure",
	"internal/proc":       "infrastructure",
	"internal/confdir":    "infrastructure",
	"internal/atomicfile": "infrastructure",
	"internal/autoupdate": "infrastructure",
}
