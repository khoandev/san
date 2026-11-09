// The agents.roles block: the map that assigns a model role to a built-in
// agent, its layered merge, its clone, and the fold that migrates the advisor's
// retired per-agent model slot onto it.
package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A project file may pin the agents it cares about and leave the rest to the
// user's file; the overlay wins per key, the way modelRoles merges.
func TestAgentRolesMergePerKey(t *testing.T) {
	user := &Data{Agents: AgentsSettings{Roles: map[string]string{
		AgentQuick:    "fast",
		AgentTask:     "standard",
		AgentReviewer: "deep",
	}}}
	project := &Data{Agents: AgentsSettings{Roles: map[string]string{
		AgentReviewer: "standard",
	}}}

	got := mergeSettings(user, project).Agents.Roles
	want := map[string]string{AgentQuick: "fast", AgentTask: "standard", AgentReviewer: "standard"}
	for name, role := range want {
		if got[name] != role {
			t.Errorf("agents.roles[%q] = %q, want %q", name, got[name], role)
		}
	}
	if len(got) != len(want) {
		t.Errorf("merged map = %v, want %v", got, want)
	}
	// A silent project layer must not drop the user's bindings.
	if kept := mergeSettings(user, &Data{}).Agents.Roles[AgentReviewer]; kept != "deep" {
		t.Errorf("user binding lost under an empty project layer: %q", kept)
	}
}

// Clone hands every reader its own storage, so the settings service cannot be
// mutated through a cached copy.
func TestAgentRolesCloneOwnsStorage(t *testing.T) {
	src := &Data{Agents: AgentsSettings{AutoTier: boolPtr(true), Roles: map[string]string{
		AgentAdvisor: "deep",
	}}}
	clone := src.Clone()
	clone.Agents.Roles[AgentAdvisor] = "fast"
	clone.Agents.Roles[AgentQuick] = "fast"

	if got := src.AgentRole(AgentAdvisor); got != "deep" {
		t.Fatalf("clone shared the map: source role = %q", got)
	}
	if src.AgentRole(AgentQuick) != "" {
		t.Fatal("clone wrote a key into its source's map")
	}

	// A nil map stays nil, so settings.json keeps omitting an empty block.
	encoded, err := json.Marshal(&Data{})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("empty settings encoded as %s", encoded)
	}
}

// AgentRole reads the map the way the fold writes it: case and surrounding
// space do not matter.
func TestAgentRoleLookupFoldsCase(t *testing.T) {
	data := &Data{Agents: AgentsSettings{Roles: map[string]string{" Reviewer ": "deep"}}}
	if got := data.AgentRole("reviewer"); got != "deep" {
		t.Fatalf("AgentRole(reviewer) = %q, want deep", got)
	}
	if got := data.AgentRole("REVIEWER"); got != "deep" {
		t.Fatalf("AgentRole(REVIEWER) = %q, want deep", got)
	}
	if got := data.AgentRole(AgentQuick); got != "" {
		t.Fatalf("unassigned agent = %q, want empty", got)
	}
	if got := (&Data{}).AgentRole(AgentTask); got != "" {
		t.Fatalf("empty settings = %q, want empty", got)
	}
}

// The fold normalizes both halves of an entry: retired role names collapse onto
// their rung, a redundant "@" is dropped, keys are lowercased, and an entry that
// cannot be a role is dropped rather than stored as one that never resolves.
func TestFoldAgentRolesNormalizesEntries(t *testing.T) {
	data := &Data{Agents: AgentsSettings{Roles: map[string]string{
		"Reviewer": "@slow",
		" task ":   "Standard",
		"quick":    "not a role",
	}}}
	foldSettings(data)

	if got := data.AgentRole(AgentReviewer); got != "deep" {
		t.Errorf("@slow did not fold to deep: %q", got)
	}
	if got := data.AgentRole(AgentTask); got != "standard" {
		t.Errorf("task role = %q, want standard", got)
	}
	if got := data.AgentRole(AgentQuick); got != "" {
		t.Errorf("invalid role survived the fold: %q", got)
	}
}

// advisor.model used to be the only place the advisor's model lived. A value
// written as "@role" names a routing slot, so it moves onto agents.roles; a
// concrete model reference is a pin and is left where it is.
func TestFoldAgentRolesMigratesAdvisorModel(t *testing.T) {
	t.Run("role reference moves", func(t *testing.T) {
		data := &Data{Advisor: AdvisorSettings{Model: "@fast"}}
		foldSettings(data)
		if got := data.AgentRole(AgentAdvisor); got != "fast" {
			t.Fatalf("agents.roles.advisor = %q, want fast", got)
		}
		if data.Advisor.Model != "" {
			t.Fatalf("advisor.model = %q, want it cleared", data.Advisor.Model)
		}
	})

	t.Run("explicit assignment wins", func(t *testing.T) {
		data := &Data{
			Advisor: AdvisorSettings{Model: "@fast"},
			Agents:  AgentsSettings{Roles: map[string]string{AgentAdvisor: "deep"}},
		}
		foldSettings(data)
		if got := data.AgentRole(AgentAdvisor); got != "deep" {
			t.Fatalf("agents.roles.advisor = %q, want the explicit deep", got)
		}
		if data.Advisor.Model != "" {
			t.Fatalf("advisor.model = %q, want it cleared", data.Advisor.Model)
		}
	})

	t.Run("concrete model stays pinned", func(t *testing.T) {
		data := &Data{Advisor: AdvisorSettings{Model: "anthropic/claude-sonnet-4-5"}}
		foldSettings(data)
		if got := data.AgentRole(AgentAdvisor); got != "" {
			t.Fatalf("agents.roles.advisor = %q, want none", got)
		}
		if data.Advisor.Model != "anthropic/claude-sonnet-4-5" {
			t.Fatalf("advisor.model = %q, want the pin kept", data.Advisor.Model)
		}
	})

	t.Run("bare role name is not a role reference", func(t *testing.T) {
		// A bare id is a model id everywhere else — llm.ParseRoleAlias needs
		// the "@" — so it must not be mistaken for a role here.
		data := &Data{Advisor: AdvisorSettings{Model: "fast"}}
		foldSettings(data)
		if got := data.AgentRole(AgentAdvisor); got != "" {
			t.Fatalf("agents.roles.advisor = %q, want none", got)
		}
	})
}

// A file written before agents.roles existed keeps working: loading folds the
// advisor's "@role" pin onto the new block, and the advisor keeps the model the
// role resolves to.
func TestLoadFoldsAdvisorModelOntoAgentRoles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)

	userFile := userSettingsFile(home)
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte(`{"advisor":{"enable":true,"model":"@fast"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := TrustWorkspace(work); err != nil {
		t.Fatal(err)
	}

	loaded, err := NewLoaderForCwd(work).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.AgentRole(AgentAdvisor); got != "fast" {
		t.Fatalf("agents.roles.advisor = %q, want fast", got)
	}
	if loaded.Advisor.Model != "" {
		t.Fatalf("advisor.model = %q, want it folded away", loaded.Advisor.Model)
	}
	if !loaded.Advisor.Enabled() {
		t.Fatal("folding the model must not change the on/off switch")
	}
}

// The JSON shape the block is documented as:
// {"agents":{"roles":{"advisor":"deep","quick":"fast"}}}.
func TestAgentRolesJSONShape(t *testing.T) {
	var data Data
	if err := json.Unmarshal([]byte(`{"agents":{"roles":{"advisor":"deep","quick":"fast"}}}`), &data); err != nil {
		t.Fatal(err)
	}
	if got := data.AgentRole(AgentAdvisor); got != "deep" {
		t.Fatalf("decoded advisor role = %q", got)
	}
	if got := data.AgentRole(AgentQuick); got != "fast" {
		t.Fatalf("decoded quick role = %q", got)
	}

	encoded, err := json.Marshal(Data{Agents: AgentsSettings{Roles: map[string]string{AgentAdvisor: "deep"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"agents":{"roles":{"advisor":"deep"}}}` {
		t.Fatalf("encoded settings = %s", encoded)
	}
}

// writeUserSettingsFile puts raw JSON in the user settings file and returns its
// path, so a test reads back exactly what a write left behind.
func writeUserSettingsFile(t *testing.T, home, raw string) string {
	t.Helper()
	file := userSettingsFile(home)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// readSettingsFile decodes a settings file the way another san would.
func readSettingsFile(t *testing.T, path string) Data {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var data Data
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, raw)
	}
	return data
}

// One save writes the agent roles and the advisor block together, folds the
// role names it writes, and leaves every other key in the file alone.
func TestUpdateAgentsAtWritesBothBlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	file := writeUserSettingsFile(t, home, `{"theme":"dark","agents":{"autoTier":false},"modelRoles":{"fast":"openai/gpt-5-mini"}}`)
	on := true
	err := UpdateAgentsAt(
		map[string]string{" Reviewer ": "@slow", AgentQuick: "fast"},
		AdvisorSettings{Enable: &on},
		ScopeUser,
	)
	if err != nil {
		t.Fatalf("UpdateAgentsAt: %v", err)
	}

	data := readSettingsFile(t, file)
	if got := data.AgentRole(AgentReviewer); got != "deep" {
		t.Errorf("agents.roles.reviewer = %q, want the folded deep", got)
	}
	if _, stray := data.Agents.Roles[" Reviewer "]; stray {
		t.Error("an unfolded key reached the file")
	}
	if got := data.AgentRole(AgentQuick); got != "fast" {
		t.Errorf("agents.roles.quick = %q, want fast", got)
	}
	if !data.Advisor.Enabled() {
		t.Error("advisor enable did not reach the file")
	}
	// Every other setting survives: the root keys, the autoTier switch the tab
	// does not own, and the model roles it does not edit.
	if data.Theme != "dark" {
		t.Errorf("theme = %q, want it untouched", data.Theme)
	}
	if data.Agents.AutoTier == nil || data.Agents.AutoTierOn() {
		t.Error("agents.autoTier was cleared or flipped by the save")
	}
	if got := data.ModelRole("fast"); got != "openai/gpt-5-mini" {
		t.Errorf("modelRoles.fast = %q, want it untouched", got)
	}
}

// A project-scope save lands in the workspace's .san directory and leaves the
// user's file as it was.
func TestUpdateAgentsAtWritesProjectScope(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)

	userFile := writeUserSettingsFile(t, home, `{"agents":{"roles":{"advisor":"deep"}}}`)
	if err := TrustWorkspace(work); err != nil {
		t.Fatal(err)
	}
	err := UpdateAgentsAt(
		map[string]string{AgentReviewer: "standard"},
		AdvisorSettings{},
		ScopeProject,
	)
	if err != nil {
		t.Fatalf("UpdateAgentsAt: %v", err)
	}

	project := readSettingsFile(t, filepath.Join(".san", "settings.json"))
	if got := project.AgentRole(AgentReviewer); got != "standard" {
		t.Errorf("project agents.roles.reviewer = %q, want standard", got)
	}
	if got := project.AgentRole(AgentAdvisor); got != "" {
		t.Errorf("project agents.roles.advisor = %q, want none", got)
	}
	user := readSettingsFile(t, userFile)
	if got := user.AgentRole(AgentAdvisor); got != "deep" {
		t.Errorf("user agents.roles.advisor = %q, want it untouched", got)
	}
}

// A save never writes a role that cannot resolve, and nothing reaches disk when
// validation fails.
func TestUpdateAgentsAtRejectsInvalidRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	file := writeUserSettingsFile(t, home, `{"theme":"dark"}`)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]map[string]string{
		"not a role": {AgentQuick: "not a role"},
		"empty name": {" ": "fast"},
		"empty role": {AgentTask: ""},
	}
	for name, roles := range cases {
		if err := UpdateAgentsAt(roles, AdvisorSettings{}, ScopeUser); err == nil {
			t.Errorf("%s: UpdateAgentsAt accepted %v", name, roles)
		}
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("a rejected save still wrote the file:\n%s", after)
	}
}

// The settings service answers the same question the panel asks, through the
// clone it hands every reader.
func TestSettingsAgentRoleAndAgentRoles(t *testing.T) {
	settings := New(NewData())
	if got := settings.AgentRole(AgentAdvisor); got != "" {
		t.Fatalf("unset advisor role = %q, want empty", got)
	}
	if got := settings.AgentRoles(); got != nil {
		t.Fatalf("unset map = %v, want nil", got)
	}

	data := NewData()
	data.Agents.Roles[AgentReviewer] = "deep"
	settings = New(data)
	if got := settings.AgentRole("REVIEWER"); got != "deep" {
		t.Fatalf("AgentRole(REVIEWER) = %q, want deep", got)
	}
	roles := settings.AgentRoles()
	roles[AgentReviewer] = "fast"
	if got := settings.AgentRole(AgentReviewer); got != "deep" {
		t.Fatalf("AgentRoles handed out live storage: role = %q", got)
	}
}

// A legacy advisor.model written as "@role" survives the tab's save: the fold
// moves it onto agents.roles.advisor before the block is replaced, so a save
// that ships the effective bindings keeps the assignment instead of dropping it.
func TestUpdateAgentsAtKeepsFoldedAdvisorRole(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)

	file := writeUserSettingsFile(t, home, `{"advisor":{"enable":true,"model":"@deep"}}`)
	if err := TrustWorkspace(work); err != nil {
		t.Fatal(err)
	}
	effective, err := NewLoaderForCwd(work).Load()
	if err != nil {
		t.Fatal(err)
	}
	// The panel seeds its form from the effective settings, so this is the map
	// a save ships — the assignment the fold produced, not the legacy key.
	if err := UpdateAgentsAt(effective.Agents.Roles, effective.Advisor, ScopeUser); err != nil {
		t.Fatalf("UpdateAgentsAt: %v", err)
	}

	data := readSettingsFile(t, file)
	if got := data.AgentRole(AgentAdvisor); got != "deep" {
		t.Fatalf("agents.roles.advisor = %q, want the fold's deep", got)
	}
	if data.Advisor.Model != "" {
		t.Fatalf("advisor.model = %q, want the folded key gone", data.Advisor.Model)
	}
	if !data.Advisor.Enabled() {
		t.Fatal("the save turned the advisor off")
	}
}

// A concrete model pin the tab has no control for is not silently cleared by a
// save that only assigns roles, and it is dropped once a role is assigned
// (which supersedes it).
func TestUpdateAgentsAtKeepsLegacyModelPin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	file := writeUserSettingsFile(t, home, `{"advisor":{"enable":true,"model":"anthropic/claude-sonnet-4-5"}}`)
	if err := UpdateAgentsAt(map[string]string{AgentQuick: "fast"}, AdvisorSettings{Enable: boolPtr(true)}, ScopeUser); err != nil {
		t.Fatalf("UpdateAgentsAt: %v", err)
	}
	data := readSettingsFile(t, file)
	if data.Advisor.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("advisor.model = %q, want the pin kept", data.Advisor.Model)
	}

	if err := UpdateAgentsAt(map[string]string{AgentAdvisor: "deep"}, AdvisorSettings{Enable: boolPtr(true)}, ScopeUser); err != nil {
		t.Fatalf("UpdateAgentsAt: %v", err)
	}
	data = readSettingsFile(t, file)
	if data.Advisor.Model != "" {
		t.Fatalf("advisor.model = %q, want it cleared once a role is assigned", data.Advisor.Model)
	}
	if got := data.AgentRole(AgentAdvisor); got != "deep" {
		t.Fatalf("agents.roles.advisor = %q, want deep", got)
	}
}
