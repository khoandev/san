// The agents.roles block: the map that assigns a model role to a built-in
// agent, its layered merge, its clone, and the fold that moves the advisor's
// retired model slots onto the "advisor" model role.
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
		AgentTask: "standard",
	}}}
	clone := src.Clone()
	clone.Agents.Roles[AgentTask] = "deep"
	clone.Agents.Roles[AgentQuick] = "fast"

	if got := src.AgentRole(AgentTask); got != "standard" {
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

// The advisor's model used to live in two retired slots. Both move onto the one
// place a role's model is configured now, modelRoles.advisor, and both are
// cleared so a save stops writing them.
func TestFoldAdvisorRoleMigratesRetiredSlots(t *testing.T) {
	t.Run("role reference moves", func(t *testing.T) {
		data := &Data{Advisor: AdvisorSettings{Model: "@fast"}}
		foldSettings(data)
		if got := data.ModelRole(AgentAdvisor); got != "@fast" {
			t.Fatalf("modelRoles.advisor = %q, want @fast", got)
		}
		if data.Advisor.Model != "" {
			t.Fatalf("advisor.model = %q, want it cleared", data.Advisor.Model)
		}
	})

	t.Run("concrete model moves", func(t *testing.T) {
		data := &Data{Advisor: AdvisorSettings{Model: "anthropic/claude-sonnet-4-5"}}
		foldSettings(data)
		if got := data.ModelRole(AgentAdvisor); got != "anthropic/claude-sonnet-4-5" {
			t.Fatalf("modelRoles.advisor = %q, want the pin", got)
		}
		if data.Advisor.Model != "" {
			t.Fatalf("advisor.model = %q, want it cleared", data.Advisor.Model)
		}
	})

	t.Run("agents.roles.advisor becomes a role reference", func(t *testing.T) {
		data := &Data{Agents: AgentsSettings{Roles: map[string]string{AgentAdvisor: "slow"}}}
		foldSettings(data)
		if got := data.ModelRole(AgentAdvisor); got != "@deep" {
			t.Fatalf("modelRoles.advisor = %q, want @deep (slow folded)", got)
		}
		if _, retired := data.Agents.Roles[AgentAdvisor]; retired {
			t.Fatalf("agents.roles.advisor survived the fold: %v", data.Agents.Roles)
		}
	})

	t.Run("agents.roles.advisor wins over advisor.model", func(t *testing.T) {
		// The role assignment superseded the pin, exactly as the old panel
		// wrote it.
		data := &Data{
			Advisor: AdvisorSettings{Model: "anthropic/claude-sonnet-4-5"},
			Agents:  AgentsSettings{Roles: map[string]string{AgentAdvisor: "fast"}},
		}
		foldSettings(data)
		if got := data.ModelRole(AgentAdvisor); got != "@fast" {
			t.Fatalf("modelRoles.advisor = %q, want @fast", got)
		}
	})

	t.Run("an explicit binding wins", func(t *testing.T) {
		data := &Data{
			ModelRoles: map[string]string{AgentAdvisor: "openai/gpt-5"},
			Advisor:    AdvisorSettings{Model: "@fast"},
			Agents:     AgentsSettings{Roles: map[string]string{AgentAdvisor: "deep"}},
		}
		foldSettings(data)
		if got := data.ModelRole(AgentAdvisor); got != "openai/gpt-5" {
			t.Fatalf("modelRoles.advisor = %q, want the explicit binding", got)
		}
		if data.Advisor.Model != "" {
			t.Fatalf("advisor.model = %q, want it cleared", data.Advisor.Model)
		}
	})
}

// A file written before the advisor had a role of its own keeps working:
// loading folds its retired slots onto modelRoles.advisor, so the review keeps
// answering on the model it answered on before.
func TestLoadFoldsRetiredAdvisorSlots(t *testing.T) {
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
	if got := loaded.ModelRole(AgentAdvisor); got != "@fast" {
		t.Fatalf("modelRoles.advisor = %q, want @fast", got)
	}
	if loaded.Advisor.Model != "" {
		t.Fatalf("advisor.model = %q, want it folded away", loaded.Advisor.Model)
	}
	if !loaded.Advisor.Enabled() {
		t.Fatal("folding the model must not change the on/off switch")
	}
}

// The JSON shape the blocks are documented as: a roles map of agents and a
// standalone advisor switch.
func TestAgentRolesJSONShape(t *testing.T) {
	var data Data
	if err := json.Unmarshal([]byte(`{"agents":{"roles":{"quick":"fast"}},"advisor":{"enable":true}}`), &data); err != nil {
		t.Fatal(err)
	}
	if got := data.AgentRole(AgentQuick); got != "fast" {
		t.Fatalf("decoded quick role = %q", got)
	}
	if !data.Advisor.Enabled() {
		t.Fatal("decoded advisor switch = off")
	}

	encoded, err := json.Marshal(Data{Agents: AgentsSettings{Roles: map[string]string{AgentQuick: "fast"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"agents":{"roles":{"quick":"fast"}}}` {
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

// One save writes the agent roles, folds the role names it writes, and leaves
// every other key in the file alone — including the advisor switch, which this
// write does not own.
func TestUpdateAgentRolesAtWritesTheBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	file := writeUserSettingsFile(t, home, `{"theme":"dark","agents":{"autoTier":false},"advisor":{"enable":true},"modelRoles":{"fast":"openai/gpt-5-mini"}}`)
	err := UpdateAgentRolesAt(
		map[string]string{" Reviewer ": "@slow", AgentQuick: "fast"},
		ScopeUser,
	)
	if err != nil {
		t.Fatalf("UpdateAgentRolesAt: %v", err)
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
		t.Error("the write cleared the advisor's switch")
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

// A project-scope write lands in the workspace's .san directory and leaves the
// user's file as it was.
func TestUpdateAgentRolesAtWritesProjectScope(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)

	userFile := writeUserSettingsFile(t, home, `{"agents":{"roles":{"quick":"fast"}}}`)
	if err := TrustWorkspace(work); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAgentRolesAt(map[string]string{AgentReviewer: "standard"}, ScopeProject); err != nil {
		t.Fatalf("UpdateAgentRolesAt: %v", err)
	}

	project := readSettingsFile(t, filepath.Join(".san", "settings.json"))
	if got := project.AgentRole(AgentReviewer); got != "standard" {
		t.Errorf("project agents.roles.reviewer = %q, want standard", got)
	}
	user := readSettingsFile(t, userFile)
	if got := user.AgentRole(AgentQuick); got != "fast" {
		t.Errorf("user agents.roles.quick = %q, want it untouched", got)
	}
}

// A write never stores a role that cannot resolve, and nothing reaches disk when
// validation fails.
func TestUpdateAgentRolesAtRejectsInvalidRole(t *testing.T) {
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
		if err := UpdateAgentRolesAt(roles, ScopeUser); err == nil {
			t.Errorf("%s: UpdateAgentRolesAt accepted %v", name, roles)
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
	if got := settings.AgentRole(AgentReviewer); got != "" {
		t.Fatalf("unset reviewer role = %q, want empty", got)
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

// The advisor's switch is its own block: writing it replaces the block without
// touching the roles map or the retired model slot's migration.
func TestUpdateAdvisorAtWritesOnlyTheSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	file := writeUserSettingsFile(t, home, `{"agents":{"roles":{"quick":"fast"}},"modelRoles":{"fast":"openai/gpt-5-mini"}}`)
	if err := UpdateAdvisorAt(AdvisorSettings{Enable: boolPtr(true)}, ScopeUser); err != nil {
		t.Fatalf("UpdateAdvisorAt: %v", err)
	}

	data := readSettingsFile(t, file)
	if !data.Advisor.Enabled() {
		t.Fatal("the switch did not reach the file")
	}
	if got := data.AgentRole(AgentQuick); got != "fast" {
		t.Errorf("agents.roles.quick = %q, want it untouched", got)
	}
	if got := data.ModelRole("fast"); got != "openai/gpt-5-mini" {
		t.Errorf("modelRoles.fast = %q, want it untouched", got)
	}
	if err := UpdateAdvisorAt(AdvisorSettings{Enable: boolPtr(false)}, ScopeUser); err != nil {
		t.Fatalf("UpdateAdvisorAt: %v", err)
	}
	if data := readSettingsFile(t, file); data.Advisor.Enabled() {
		t.Fatal("an explicit off did not persist")
	}
}
