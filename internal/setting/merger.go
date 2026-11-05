package setting

import "maps"

// mergeSettings merges two Data, with overlay taking precedence over base.
// Slices (permission rules) are deduplicated unions. Maps are merged with overlay winning on conflicts.
func mergeSettings(base, overlay *Data) *Data {
	if base == nil {
		return overlay
	}
	if overlay == nil {
		return base
	}

	result := NewData()
	result.Permissions = mergePermissions(base.Permissions, overlay.Permissions)
	result.Model = coalesce(overlay.Model, base.Model)
	result.SubagentModel = coalesce(overlay.SubagentModel, base.SubagentModel)
	result.Theme = coalesce(overlay.Theme, base.Theme)
	result.Hooks = mergeHooks(base.Hooks, overlay.Hooks)
	result.Env = mergeMaps(base.Env, overlay.Env)
	result.EnabledPlugins = mergeMaps(base.EnabledPlugins, overlay.EnabledPlugins)
	result.DisabledTools = mergeMaps(base.DisabledTools, overlay.DisabledTools)
	result.SearchProvider = coalesce(overlay.SearchProvider, base.SearchProvider)
	result.StreamFirstChunkTimeout = coalesce(overlay.StreamFirstChunkTimeout, base.StreamFirstChunkTimeout)
	result.StreamIdleTimeout = coalesce(overlay.StreamIdleTimeout, base.StreamIdleTimeout)
	result.HookUITimeout = coalesce(overlay.HookUITimeout, base.HookUITimeout)
	result.AllowBypass = coalescePtr(overlay.AllowBypass, base.AllowBypass)
	result.AutoUpdate = coalescePtr(overlay.AutoUpdate, base.AutoUpdate)
	result.AutoMemory = coalescePtr(overlay.AutoMemory, base.AutoMemory)
	result.AutoMemoryDirectory = coalesce(overlay.AutoMemoryDirectory, base.AutoMemoryDirectory)
	result.ContextBar = coalescePtr(overlay.ContextBar, base.ContextBar)
	result.GitBar = coalescePtr(overlay.GitBar, base.GitBar)
	result.SkillCommands = coalescePtr(overlay.SkillCommands, base.SkillCommands)
	result.ThinkingDisplay = coalesce(overlay.ThinkingDisplay, base.ThinkingDisplay)
	result.BashPreviewLines = coalescePtr(overlay.BashPreviewLines, base.BashPreviewLines)
	result.AgentPreviewLines = coalescePtr(overlay.AgentPreviewLines, base.AgentPreviewLines)
	result.Persona = coalesce(overlay.Persona, base.Persona)
	result.SelfLearn = mergeSelfLearn(base.SelfLearn, overlay.SelfLearn)
	result.AutoPilot = mergeAutoPilot(base.AutoPilot, overlay.AutoPilot)
	result.Advisor = mergeAdvisor(base.Advisor, overlay.Advisor)
	result.LastOperationMode = coalesce(overlay.LastOperationMode, base.LastOperationMode)

	return result
}

// mergeAutoPilot does a field-level merge of the autopilot config: strings and
// the continuation cap coalesce (overlay's non-zero wins), ordinary steer bools
// OR (enable-anywhere wins), and tri-state default-on steers coalesce (overlay's
// explicit value wins, else base's, else nil = default on). Without this the
// entire autoPilot block is dropped on every Load and every save.
func mergeAutoPilot(base, overlay AutoPilotSettings) AutoPilotSettings {
	return AutoPilotSettings{
		Model:             coalesce(overlay.Model, base.Model),
		ThinkingEffort:    coalesce(overlay.ThinkingEffort, base.ThinkingEffort),
		SystemPrompt:      coalesce(overlay.SystemPrompt, base.SystemPrompt),
		SystemPromptFile:  coalesce(overlay.SystemPromptFile, base.SystemPromptFile),
		Mission:           coalesce(overlay.Mission, base.Mission),
		MaxContinuations:  coalesceInt(overlay.MaxContinuations, base.MaxContinuations),
		RecapAfterMinutes: coalesceInt(overlay.RecapAfterMinutes, base.RecapAfterMinutes),
		Steers: SteerSettings{
			Suggest:    coalescePtr(overlay.Steers.Suggest, base.Steers.Suggest),
			Permission: coalescePtr(overlay.Steers.Permission, base.Steers.Permission),
			BashPrompt: overlay.Steers.BashPrompt || base.Steers.BashPrompt,
			Question:   overlay.Steers.Question || base.Steers.Question,
			TurnEnd:    overlay.Steers.TurnEnd || base.Steers.TurnEnd,
		},
	}
}

// ApplyPersonaOverlay merges a persona's settings.json overlay on top of the
// resolved base settings, as the highest file-level layer. Maps merge per key
// (a persona can even re-enable a tool a lower layer disabled, via
// "disabledTools": {"X": false}); permission allow/deny/ask are deduplicated
// unions, so a persona can tighten but never loosen them. A nil overlay
// returns base unchanged.
//
// The overlay's own Persona selector is ignored: a persona cannot re-select the
// active persona (which would be circular).
func ApplyPersonaOverlay(base, overlay *Data) *Data {
	if overlay == nil {
		return base
	}
	ov := overlay.Clone()
	ov.Persona = ""
	return mergeSettings(base, ov)
}

// mergeAdvisor does a field-level merge of the advisor config. Every on/off knob
// is tri-state and coalesces — the overlay's explicit value wins, else the
// base's, else nil keeps the documented default — so a project or a session can
// turn a default-on trigger off and have it stick. Bounds coalesce (a non-zero
// overlay wins) and the sensitive-path patterns are a deduplicated union,
// matching permissions: a project can widen what counts as sensitive but never
// narrow the global list. Without this the whole advisor block is dropped on
// every Load and every save.
func mergeAdvisor(base, overlay AdvisorSettings) AdvisorSettings {
	return AdvisorSettings{
		Enable:          coalescePtr(overlay.Enable, base.Enable),
		RunInBackground: coalescePtr(overlay.RunInBackground, base.RunInBackground),
		Triggers: AdvisorTriggers{
			AfterSubagentTask: coalescePtr(overlay.Triggers.AfterSubagentTask, base.Triggers.AfterSubagentTask),
			BeforeMerge:       coalescePtr(overlay.Triggers.BeforeMerge, base.Triggers.BeforeMerge),
			BeforePlanStep:    coalescePtr(overlay.Triggers.BeforePlanStep, base.Triggers.BeforePlanStep),
			OnLargeChanges:    coalescePtr(overlay.Triggers.OnLargeChanges, base.Triggers.OnLargeChanges),
			SensitivePaths:    coalescePtr(overlay.Triggers.SensitivePaths, base.Triggers.SensitivePaths),
		},
		Blocks: AdvisorBlocks{
			MergeOnP0:               coalescePtr(overlay.Blocks.MergeOnP0, base.Blocks.MergeOnP0),
			MergeOnP1:               coalescePtr(overlay.Blocks.MergeOnP1, base.Blocks.MergeOnP1),
			TaskCompletionOnP0:      coalescePtr(overlay.Blocks.TaskCompletionOnP0, base.Blocks.TaskCompletionOnP0),
			PlanStepCompletionOnP0:  coalescePtr(overlay.Blocks.PlanStepCompletionOnP0, base.Blocks.PlanStepCompletionOnP0),
			RequireConfirmationOnP1: coalescePtr(overlay.Blocks.RequireConfirmationOnP1, base.Blocks.RequireConfirmationOnP1),
			AllowProceedWithRisk:    coalescePtr(overlay.Blocks.AllowProceedWithRisk, base.Blocks.AllowProceedWithRisk),
		},
		Thresholds: AdvisorThresholds{
			MinChangedFiles:       coalesceInt(overlay.Thresholds.MinChangedFiles, base.Thresholds.MinChangedFiles),
			MinChangedLines:       coalesceInt(overlay.Thresholds.MinChangedLines, base.Thresholds.MinChangedLines),
			SensitiveFilePatterns: mergeStringSlices(base.Thresholds.SensitiveFilePatterns, overlay.Thresholds.SensitiveFilePatterns),
			SensitiveDirPatterns:  mergeStringSlices(base.Thresholds.SensitiveDirPatterns, overlay.Thresholds.SensitiveDirPatterns),
			MaxDiffBytes:          coalesceInt(overlay.Thresholds.MaxDiffBytes, base.Thresholds.MaxDiffBytes),
			CooldownSeconds:       coalesceInt(overlay.Thresholds.CooldownSeconds, base.Thresholds.CooldownSeconds),
		},
		Review: AdvisorReview{
			Mode:         coalesce(overlay.Review.Mode, base.Review.Mode),
			CustomPrompt: coalesce(overlay.Review.CustomPrompt, base.Review.CustomPrompt),
		},
		Model: AdvisorModel{
			Model:          coalesce(overlay.Model.Model, base.Model.Model),
			ThinkingEffort: coalesce(overlay.Model.ThinkingEffort, base.Model.ThinkingEffort),
			MaxTokens:      coalesceInt(overlay.Model.MaxTokens, base.Model.MaxTokens),
			TimeoutSeconds: coalesceInt(overlay.Model.TimeoutSeconds, base.Model.TimeoutSeconds),
			MaxFindings:    coalesceInt(overlay.Model.MaxFindings, base.Model.MaxFindings),
		},
		Display: AdvisorDisplay{
			StatusBar: coalescePtr(overlay.Display.StatusBar, base.Display.StatusBar),
			HideP3:    coalescePtr(overlay.Display.HideP3, base.Display.HideP3),
			GroupBy:   coalesce(overlay.Display.GroupBy, base.Display.GroupBy),
		},
	}
}

// mergeSelfLearn does a field-level merge of the L1 configuration: integers
// coalesce (non-zero wins), bools OR (deny-anywhere or enable-anywhere
// wins, matching the safety bias of layered config). Without this the
// entire selfLearn block is dropped on every Load and every save.
func mergeSelfLearn(base, overlay SelfLearnSettings) SelfLearnSettings {
	return SelfLearnSettings{
		Skills: SelfLearnSkills{
			DenyCreate: overlay.Skills.DenyCreate || base.Skills.DenyCreate,
			DenyUpdate: overlay.Skills.DenyUpdate || base.Skills.DenyUpdate,
			DenyDelete: overlay.Skills.DenyDelete || base.Skills.DenyDelete,
		},
		Strategy: coalesce(overlay.Strategy, base.Strategy),
	}
}

func coalesceInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func mergePermissions(base, overlay PermissionSettings) PermissionSettings {
	return PermissionSettings{
		DefaultMode: coalesce(overlay.DefaultMode, base.DefaultMode),
		Allow:       mergeStringSlices(base.Allow, overlay.Allow),
		Deny:        mergeStringSlices(base.Deny, overlay.Deny),
		Ask:         mergeStringSlices(base.Ask, overlay.Ask),
	}
}

func mergeStringSlices(base, overlay []string) []string {
	seen := make(map[string]bool, len(base)+len(overlay))
	result := make([]string, 0, len(base)+len(overlay))
	for _, s := range append(base, overlay...) {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// mergeHooks merges hook configurations, appending overlay hooks to base hooks per event.
func mergeHooks(base, overlay map[string][]Hook) map[string][]Hook {
	result := make(map[string][]Hook, len(base)+len(overlay))
	for k, v := range base {
		result[k] = append([]Hook{}, v...)
	}
	for k, v := range overlay {
		result[k] = append(result[k], v...)
	}
	return result
}

// coalesce returns the first non-empty string.
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// coalescePtr picks the overlay's value when explicitly set. An explicit false
// or 0 must survive — that is a real choice, not an absent value, which is the
// whole reason these fields are pointers.
func coalescePtr[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

// mergeMaps merges two maps with overlay taking precedence over base.
func mergeMaps[V any](base, overlay map[string]V) map[string]V {
	result := make(map[string]V, len(base)+len(overlay))
	maps.Copy(result, base)
	maps.Copy(result, overlay)
	return result
}
