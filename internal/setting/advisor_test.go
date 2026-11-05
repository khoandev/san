package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// boolPtr is the tri-state helper these tests need: every advisor on/off knob
// is a *bool precisely so an explicit false is distinguishable from absent.
func boolPtr(b bool) *bool { return &b }

// The advisor block loads under the "advisor" JSON key; the Go field is the
// same name. An unknown sibling key is ignored, as for every other block.
func TestAdvisorSettingsKey(t *testing.T) {
	var d Data
	if err := json.Unmarshal([]byte(`{"advisor":{"enable":true,"review":{"mode":"deep"}},"advisorX":{"enable":true}}`), &d); err != nil {
		t.Fatalf("unmarshal advisor: %v", err)
	}
	if !d.Advisor.Enabled() {
		t.Error("advisor.enable:true did not enable the advisor")
	}
	if d.Advisor.Mode() != AdvisorModeDeep {
		t.Errorf("review mode = %q, want deep", d.Advisor.Mode())
	}
}

// The zero config must read as the documented product defaults, not as a pile
// of zero values: opt-in enable, default-on triggers, default-on merge block at
// P0, bounded resources.
func TestAdvisorDefaults(t *testing.T) {
	a := AdvisorSettings{}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Enabled", a.Enabled(), false},
		{"RunsInBackground", a.RunsInBackground(), true},
		{"AfterSubagentTask", a.AfterSubagentTask(), true},
		{"BeforeMerge", a.BeforeMerge(), true},
		{"BeforePlanStep", a.BeforePlanStep(), false},
		{"OnLargeChanges", a.OnLargeChanges(), false},
		{"SensitivePaths", a.SensitivePaths(), true},
		{"MergeThreshold", a.MergeThreshold(), "P0"},
		{"TaskThreshold", a.TaskThreshold(), "P0"},
		{"PlanStepThreshold", a.PlanStepThreshold(), "P0"},
		{"ConfirmThreshold", a.ConfirmThreshold(), "P1"},
		{"AllowProceedWithRisk", a.AllowProceedWithRisk(), false},
		{"Mode", a.Mode(), AdvisorModeStandard},
		{"ResolvedMaxTokens", a.ResolvedMaxTokens(), AdvisorDefaultMaxTokens},
		{"ResolvedTimeout", a.ResolvedTimeout(), time.Duration(AdvisorDefaultTimeoutSeconds) * time.Second},
		{"ResolvedMaxFindings", a.ResolvedMaxFindings(), AdvisorDefaultMaxFindings},
		{"ResolvedMaxDiffBytes", a.ResolvedMaxDiffBytes(), AdvisorDefaultMaxDiffBytes},
		{"ResolvedCooldown", a.ResolvedCooldown(), time.Duration(AdvisorDefaultCooldownSeconds) * time.Second},
		{"ShowStatusBar", a.ShowStatusBar(), true},
		{"HidesP3", a.HidesP3(), false},
		{"Grouping", a.Grouping(), AdvisorDefaultGrouping},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !a.IsZero() {
		t.Error("zero advisor config should report IsZero")
	}
	if err := a.Validate(); err != nil {
		t.Errorf("zero config should validate, got %v", err)
	}
}

// An explicit false must survive JSON, Clone and the settings merge. This is
// the regression tri-state pointers exist for: a default-on trigger turned off
// in a project must stay off after the next Load.
func TestAdvisorExplicitOffSurvivesMerge(t *testing.T) {
	var d Data
	raw := `{"advisor":{"enable":true,"runInBackground":false,
		"triggers":{"beforeMerge":false,"sensitivePaths":false,"beforePlanStep":true},
		"blocks":{"mergeOnP0":false,"allowProceedWithRisk":true},
		"display":{"statusBar":false,"hideP3":true}}}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for name, a := range map[string]AdvisorSettings{
		"parsed": d.Advisor,
		"clone":  d.Clone().Advisor,
		"merged": mergeSettings(NewData(), &d).Advisor,
	} {
		if a.RunsInBackground() {
			t.Errorf("%s: explicit runInBackground:false was lost", name)
		}
		if a.BeforeMerge() {
			t.Errorf("%s: explicit beforeMerge:false was lost", name)
		}
		if a.SensitivePaths() {
			t.Errorf("%s: explicit sensitivePaths:false was lost", name)
		}
		if !a.BeforePlanStep() {
			t.Errorf("%s: explicit beforePlanStep:true was lost", name)
		}
		if a.MergeThreshold() != "" {
			t.Errorf("%s: mergeOnP0:false should leave merges advisory, got threshold %q", name, a.MergeThreshold())
		}
		if !a.AllowProceedWithRisk() {
			t.Errorf("%s: allowProceedWithRisk:true was lost", name)
		}
		if a.ShowStatusBar() {
			t.Errorf("%s: statusBar:false was lost", name)
		}
		if !a.HidesP3() {
			t.Errorf("%s: hideP3:true was lost", name)
		}
	}
}

// Merge is field-level for every knob in every group: the overlay's explicit
// value wins, group by group, and only the sensitive-path pattern lists union.
func TestMergeAdvisorFieldLevel(t *testing.T) {
	base := AdvisorSettings{
		Enable:          boolPtr(false),
		RunInBackground: boolPtr(false),
		Triggers: AdvisorTriggers{
			AfterSubagentTask: boolPtr(false),
			BeforeMerge:       boolPtr(false),
			BeforePlanStep:    boolPtr(false),
			OnLargeChanges:    boolPtr(false),
			SensitivePaths:    boolPtr(false),
		},
		Blocks: AdvisorBlocks{
			MergeOnP0:               boolPtr(false),
			MergeOnP1:               boolPtr(false),
			TaskCompletionOnP0:      boolPtr(false),
			PlanStepCompletionOnP0:  boolPtr(false),
			RequireConfirmationOnP1: boolPtr(false),
			AllowProceedWithRisk:    boolPtr(false),
		},
		Thresholds: AdvisorThresholds{
			MinChangedFiles:       1,
			MinChangedLines:       10,
			SensitiveFilePatterns: []string{"base/file", "shared"},
			SensitiveDirPatterns:  []string{"base-dir"},
			MaxDiffBytes:          1000,
			CooldownSeconds:       5,
		},
		Review: AdvisorReview{Mode: AdvisorModeFast, CustomPrompt: "base prompt"},
		Model: AdvisorModel{
			Model:          "base-model",
			ThinkingEffort: "low",
			MaxTokens:      100,
			TimeoutSeconds: 10,
			MaxFindings:    2,
		},
		Display: AdvisorDisplay{StatusBar: boolPtr(false), HideP3: boolPtr(false), GroupBy: AdvisorGroupByFile},
	}
	overlay := AdvisorSettings{
		Enable:          boolPtr(true),
		RunInBackground: boolPtr(true),
		Triggers: AdvisorTriggers{
			AfterSubagentTask: boolPtr(true),
			BeforeMerge:       boolPtr(true),
			BeforePlanStep:    boolPtr(true),
			OnLargeChanges:    boolPtr(true),
			SensitivePaths:    boolPtr(true),
		},
		Blocks: AdvisorBlocks{
			MergeOnP0:               boolPtr(true),
			MergeOnP1:               boolPtr(true),
			TaskCompletionOnP0:      boolPtr(true),
			PlanStepCompletionOnP0:  boolPtr(true),
			RequireConfirmationOnP1: boolPtr(true),
			AllowProceedWithRisk:    boolPtr(true),
		},
		Thresholds: AdvisorThresholds{
			MinChangedFiles:       2,
			MinChangedLines:       20,
			SensitiveFilePatterns: []string{"overlay/file", "shared"},
			SensitiveDirPatterns:  []string{"overlay-dir"},
			MaxDiffBytes:          2000,
			CooldownSeconds:       10,
		},
		Review: AdvisorReview{Mode: AdvisorModeDeep, CustomPrompt: "overlay prompt"},
		Model: AdvisorModel{
			Model:          "overlay-model",
			ThinkingEffort: "high",
			MaxTokens:      200,
			TimeoutSeconds: 20,
			MaxFindings:    3,
		},
		Display: AdvisorDisplay{StatusBar: boolPtr(true), HideP3: boolPtr(true), GroupBy: AdvisorGroupByTask},
	}

	got := mergeSettings(&Data{Advisor: base}, &Data{Advisor: overlay}).Advisor

	want := overlay
	want.Thresholds.SensitiveFilePatterns = []string{"base/file", "shared", "overlay/file"}
	want.Thresholds.SensitiveDirPatterns = []string{"base-dir", "overlay-dir"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged advisor config mismatch\n got: %+v\nwant: %+v", got, want)
	}
	// The merge must not alias the overlay's slices: a later edit of either
	// side would otherwise leak into the other layer.
	got.Thresholds.SensitiveFilePatterns[0] = "mutated"
	if overlay.Thresholds.SensitiveFilePatterns[0] != "overlay/file" {
		t.Error("merge aliased the overlay's sensitive-file patterns")
	}

	// And with an empty overlay every base field survives — the regression that
	// made a whole block read back as zero after a Load.
	kept := mergeSettings(&Data{Advisor: base}, &Data{}).Advisor
	if !reflect.DeepEqual(kept, base) {
		t.Fatalf("empty overlay dropped base config\n got: %+v\nwant: %+v", kept, base)
	}
}

// A project may widen the sensitive-path lists but never narrow the global
// ones: the union is what makes a project-level opt-out impossible.
func TestMergeAdvisorPatternsUnion(t *testing.T) {
	base := AdvisorSettings{Thresholds: AdvisorThresholds{
		SensitiveFilePatterns: []string{"**/.env", "**/*.pem"},
		SensitiveDirPatterns:  []string{"migrations/"},
	}}
	overlay := AdvisorSettings{Thresholds: AdvisorThresholds{
		SensitiveFilePatterns: []string{"**/*.pem", "secrets/**"},
		SensitiveDirPatterns:  []string{"infra/"},
	}}

	got := mergeAdvisor(base, overlay)
	if want := []string{"**/.env", "**/*.pem", "secrets/**"}; !slices.Equal(got.Thresholds.SensitiveFilePatterns, want) {
		t.Errorf("file patterns = %v, want %v", got.Thresholds.SensitiveFilePatterns, want)
	}
	if want := []string{"migrations/", "infra/"}; !slices.Equal(got.Thresholds.SensitiveDirPatterns, want) {
		t.Errorf("dir patterns = %v, want %v", got.Thresholds.SensitiveDirPatterns, want)
	}
}

// Load is where the layering actually matters: a project's advisor block must
// override the user's group by group, and the patterns must union.
func TestLoadAdvisorProjectOverlayWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	repo := t.TempDir()

	writeFile(t, userSettingsFile(home), `{
  "advisor": {
    "enable": true,
    "runInBackground": true,
    "triggers": {"afterSubagentTask": true, "beforeMerge": false, "beforePlanStep": false},
    "blocks": {"mergeOnP0": true, "mergeOnP1": false, "allowProceedWithRisk": false},
    "thresholds": {"minChangedFiles": 3, "sensitiveFilePatterns": ["**/.env"], "maxDiffBytes": 1024, "cooldownSeconds": 30},
    "review": {"mode": "fast", "customPrompt": "user criteria"},
    "model": {"model": "user-model", "maxTokens": 1000, "timeoutSeconds": 60, "maxFindings": 5},
    "display": {"statusBar": true, "hideP3": false, "groupBy": "file"}
  }
}`)
	writeFile(t, filepath.Join(repo, ".san", "settings.json"), `{
  "advisor": {
    "runInBackground": false,
    "triggers": {"afterSubagentTask": false, "beforeMerge": true, "beforePlanStep": true},
    "blocks": {"mergeOnP0": false, "mergeOnP1": true, "allowProceedWithRisk": true},
    "thresholds": {"minChangedFiles": 7, "sensitiveFilePatterns": ["secrets/**"], "maxDiffBytes": 2048, "cooldownSeconds": 90},
    "review": {"mode": "deep", "customPrompt": "project criteria"},
    "model": {"model": "project-model", "maxTokens": 2000, "timeoutSeconds": 120, "maxFindings": 8},
    "display": {"statusBar": false, "hideP3": true, "groupBy": "task"}
  }
}`)

	d, err := NewLoaderWithOptions(filepath.Join(home, ".san"), filepath.Join(repo, ".san"), false).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	a := d.Advisor

	if !a.Enabled() {
		t.Error("enable:true from the user layer was dropped")
	}
	if a.RunsInBackground() {
		t.Error("project runInBackground:false did not win")
	}
	if a.AfterSubagentTask() {
		t.Error("project afterSubagentTask:false did not win")
	}
	if !a.BeforeMerge() || !a.BeforePlanStep() {
		t.Error("project beforeMerge/beforePlanStep:true did not win")
	}
	if a.MergeThreshold() != "P1" {
		t.Errorf("merge threshold = %q, want P1 from the project layer", a.MergeThreshold())
	}
	if !a.AllowProceedWithRisk() {
		t.Error("project allowProceedWithRisk:true did not win")
	}
	if a.Thresholds.MinChangedFiles != 7 || a.ResolvedMaxDiffBytes() != 2048 || a.ResolvedCooldown() != 90*time.Second {
		t.Errorf("project thresholds did not win: %+v", a.Thresholds)
	}
	if a.Mode() != AdvisorModeDeep || a.Review.CustomPrompt != "project criteria" {
		t.Errorf("project review block did not win: %+v", a.Review)
	}
	if a.Model.Model != "project-model" || a.ResolvedMaxTokens() != 2000 ||
		a.ResolvedTimeout() != 120*time.Second || a.ResolvedMaxFindings() != 8 {
		t.Errorf("project model block did not win: %+v", a.Model)
	}
	if a.ShowStatusBar() || !a.HidesP3() || a.Grouping() != AdvisorGroupByTask {
		t.Errorf("project display block did not win: %+v", a.Display)
	}
	if want := []string{"**/.env", "secrets/**"}; !slices.Equal(a.Thresholds.SensitiveFilePatterns, want) {
		t.Errorf("sensitive file patterns = %v, want the user+project union %v", a.Thresholds.SensitiveFilePatterns, want)
	}
}

// UpdateAdvisorAt replaces the block at one level, so a knob turned back to its
// default persists instead of being OR-ed with the file's previous value.
func TestUpdateAdvisorAtPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	file := userSettingsFile(home)

	read := func() AdvisorSettings {
		t.Helper()
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read user settings: %v", err)
		}
		var d Data
		if err := json.Unmarshal(data, &d); err != nil {
			t.Fatalf("unmarshal user settings: %v", err)
		}
		return d.Advisor
	}

	on := AdvisorSettings{
		Enable:   boolPtr(true),
		Triggers: AdvisorTriggers{BeforeMerge: boolPtr(false)},
		Model:    AdvisorModel{Model: "cheap-model", MaxFindings: 5},
	}
	if err := UpdateAdvisorAt(on, ScopeUser); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got := read()
	if !got.Enabled() || got.BeforeMerge() || got.Model.Model != "cheap-model" || got.ResolvedMaxFindings() != 5 {
		t.Fatalf("stored advisor config lost a knob: %+v", got)
	}

	// Turning the advisor back off must clear the block, not merge into it.
	if err := UpdateAdvisorAt(AdvisorSettings{}, ScopeUser); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := read(); !got.IsZero() {
		t.Fatalf("disable was dropped (replace-block regression): %+v", got)
	}

	// An illegal config is reported before anything touches disk.
	if err := UpdateAdvisorAt(AdvisorSettings{Review: AdvisorReview{Mode: "thorough"}}, ScopeUser); err == nil {
		t.Fatal("an unknown review mode should be rejected")
	}
	if got := read(); !got.IsZero() {
		t.Fatalf("a rejected config still reached disk: %+v", got)
	}
}

// Settings.Advisor() hands out a detached copy: a caller mutating the returned
// An untouched advisor config must not be written to settings.json — omitzero
// asks IsZero — while a configured one round-trips through the same file.
func TestAdvisorSettingsJSONRoundTrip(t *testing.T) {
	zero, err := json.Marshal(NewData())
	if err != nil {
		t.Fatalf("marshal empty data: %v", err)
	}
	if strings.Contains(string(zero), `"advisor"`) {
		t.Errorf("an unconfigured advisor block should be omitted, got %s", zero)
	}

	src := NewData()
	src.Advisor = AdvisorSettings{
		Enable:          boolPtr(true),
		RunInBackground: boolPtr(false),
		Triggers:        AdvisorTriggers{BeforeMerge: boolPtr(false), BeforePlanStep: boolPtr(true)},
		Blocks:          AdvisorBlocks{MergeOnP1: boolPtr(true), AllowProceedWithRisk: boolPtr(true)},
		Thresholds: AdvisorThresholds{
			MinChangedFiles:       4,
			SensitiveFilePatterns: []string{"**/.env"},
			CooldownSeconds:       120,
		},
		Review:  AdvisorReview{Mode: AdvisorModeDeep, CustomPrompt: "check migrations"},
		Model:   AdvisorModel{Model: "anthropic/claude-haiku-4-5", ThinkingEffort: "low", MaxFindings: 6},
		Display: AdvisorDisplay{HideP3: boolPtr(true), GroupBy: AdvisorGroupByFile},
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal advisor: %v", err)
	}

	var back Data
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal advisor: %v", err)
	}
	if !back.Advisor.Equal(src.Advisor) {
		t.Fatalf("advisor did not round-trip\n got: %+v\nwant: %+v", back.Advisor, src.Advisor)
	}
	if back.Advisor.Triggers.BeforeMerge == nil || *back.Advisor.Triggers.BeforeMerge {
		t.Error("explicit beforeMerge:false did not survive the round-trip")
	}
}

// config must not reach into the live settings.
func TestSettingsAdvisorIsDetached(t *testing.T) {
	d := NewData()
	d.Advisor = AdvisorSettings{
		Enable:     boolPtr(true),
		Thresholds: AdvisorThresholds{SensitiveFilePatterns: []string{"**/.env"}},
	}
	s := New(d)

	a := s.Advisor()
	if !a.Enabled() {
		t.Fatal("Advisor() lost enable")
	}
	*a.Enable = false
	a.Thresholds.SensitiveFilePatterns[0] = "mutated"

	live := s.Advisor()
	if !live.Enabled() || live.Thresholds.SensitiveFilePatterns[0] != "**/.env" {
		t.Fatal("Advisor() aliased the live settings")
	}
}

// Clone must deep-copy the tri-state pointers and the pattern lists.
func TestAdvisorSettingsCloneIsDeep(t *testing.T) {
	orig := AdvisorSettings{
		Enable:     boolPtr(true),
		Triggers:   AdvisorTriggers{BeforeMerge: boolPtr(false)},
		Thresholds: AdvisorThresholds{SensitiveFilePatterns: []string{"a"}, SensitiveDirPatterns: []string{"b"}},
		Display:    AdvisorDisplay{HideP3: boolPtr(false)},
	}
	clone := orig.Clone()
	*clone.Enable = false
	*clone.Triggers.BeforeMerge = true
	*clone.Display.HideP3 = true
	clone.Thresholds.SensitiveFilePatterns[0] = "x"
	clone.Thresholds.SensitiveDirPatterns[0] = "y"

	if !orig.Enabled() || orig.BeforeMerge() || orig.HidesP3() {
		t.Error("Clone shared a tri-state pointer with the original")
	}
	if orig.Thresholds.SensitiveFilePatterns[0] != "a" || orig.Thresholds.SensitiveDirPatterns[0] != "b" {
		t.Error("Clone shared a pattern slice with the original")
	}
}

// Equal compares what the advisor would do, so nil and an explicit default read
// as equal — that is what keeps the Settings panel from flagging phantom edits.
func TestAdvisorSettingsEqualNormalizesDefaults(t *testing.T) {
	var zero AdvisorSettings
	explicitDefaults := AdvisorSettings{
		Enable:          boolPtr(false),
		RunInBackground: boolPtr(true),
		Triggers: AdvisorTriggers{
			AfterSubagentTask: boolPtr(true),
			BeforeMerge:       boolPtr(true),
			SensitivePaths:    boolPtr(true),
		},
		Blocks:  AdvisorBlocks{MergeOnP0: boolPtr(true), RequireConfirmationOnP1: boolPtr(true)},
		Display: AdvisorDisplay{StatusBar: boolPtr(true), HideP3: boolPtr(false)},
	}
	if !zero.Equal(explicitDefaults) {
		t.Error("nil knobs and their explicit defaults should compare equal")
	}
	if zero.Equal(AdvisorSettings{Enable: boolPtr(true)}) {
		t.Error("enabling the advisor must read as a change")
	}
	if zero.Equal(AdvisorSettings{Thresholds: AdvisorThresholds{CooldownSeconds: 120}}) {
		t.Error("a raised cooldown must read as a change")
	}
}

// Bounds are clamped, so a hand-edited settings file cannot ask for a review
// that never finishes or a panel flooded with findings.
func TestAdvisorResolvedBoundsClamp(t *testing.T) {
	a := AdvisorSettings{
		Thresholds: AdvisorThresholds{MaxDiffBytes: 1 << 30, CooldownSeconds: 1 << 20},
		Model:      AdvisorModel{MaxTokens: 1 << 20, TimeoutSeconds: 1 << 20, MaxFindings: 1 << 20},
	}
	if got := a.ResolvedMaxTokens(); got != AdvisorMaxTokensLimit {
		t.Errorf("max tokens = %d, want the %d cap", got, AdvisorMaxTokensLimit)
	}
	if got := a.ResolvedTimeout(); got != time.Duration(AdvisorMaxTimeoutSeconds)*time.Second {
		t.Errorf("timeout = %v, want the %ds cap", got, AdvisorMaxTimeoutSeconds)
	}
	if got := a.ResolvedMaxFindings(); got != AdvisorMaxFindingsLimit {
		t.Errorf("max findings = %d, want the %d cap", got, AdvisorMaxFindingsLimit)
	}
	if got := a.ResolvedMaxDiffBytes(); got != AdvisorMaxDiffBytesLimit {
		t.Errorf("max diff bytes = %d, want the %d cap", got, AdvisorMaxDiffBytesLimit)
	}
	if got := a.ResolvedCooldown(); got != time.Duration(AdvisorMaxCooldownSecondsLimit)*time.Second {
		t.Errorf("cooldown = %v, want the %ds cap", got, AdvisorMaxCooldownSecondsLimit)
	}
}

// Validate rejects what cannot mean anything: an unknown mode or grouping and a
// negative bound. Anything a user could reasonably write passes.
func TestAdvisorSettingsValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  AdvisorSettings
		ok   bool
	}{
		{"zero", AdvisorSettings{}, true},
		{"fast", AdvisorSettings{Review: AdvisorReview{Mode: AdvisorModeFast}}, true},
		{"deep", AdvisorSettings{Review: AdvisorReview{Mode: AdvisorModeDeep}}, true},
		{"bad mode", AdvisorSettings{Review: AdvisorReview{Mode: "thorough"}}, false},
		{"bad grouping", AdvisorSettings{Display: AdvisorDisplay{GroupBy: "color"}}, false},
		{"negative lines", AdvisorSettings{Thresholds: AdvisorThresholds{MinChangedLines: -1}}, false},
		{"negative timeout", AdvisorSettings{Model: AdvisorModel{TimeoutSeconds: -5}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}
