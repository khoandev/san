package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readData parses one settings file so a test asserts on what actually landed
// on disk rather than on the writer's intent.
func readData(t *testing.T, path string) *Data {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var d Data
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return &d
}

// TestMergeModelRolesPerKey pins the layering rule: a role either file binds
// keeps its binding, and only a key both files set is decided by the overlay.
// A whole-block coalesce would instead have made a project file that binds one
// role erase every role the user file binds.
func TestMergeModelRolesPerKey(t *testing.T) {
	base := NewData()
	base.ModelRoles = map[string]string{"smol": "user-smol", "plan": "user-plan"}
	overlay := NewData()
	overlay.ModelRoles = map[string]string{"smol": "project-smol", "task": "project-task"}

	got := mergeSettings(base, overlay).ModelRoles
	want := map[string]string{"smol": "project-smol", "plan": "user-plan", "task": "project-task"}
	if len(got) != len(want) {
		t.Fatalf("modelRoles = %v, want %v", got, want)
	}
	for name, ref := range want {
		if got[name] != ref {
			t.Fatalf("modelRoles[%q] = %q, want %q (all: %v)", name, got[name], ref, got)
		}
	}

	// An overlay that says nothing about roles leaves the base's bindings.
	if ref := mergeSettings(base, NewData()).ModelRoles["plan"]; ref != "user-plan" {
		t.Fatalf("empty overlay erased role: %q", ref)
	}
}

// TestSettingsModelRolesAccessors covers the read paths: the accessor hands out
// a copy, and both lookups fold the case of a hand-edited key.
func TestSettingsModelRolesAccessors(t *testing.T) {
	d := NewData()
	d.ModelRoles = map[string]string{"smol": "anthropic/claude-haiku-4-5", "Slow": " openai/gpt-5 "}
	s := New(d)

	roles := s.ModelRoles()
	if roles["smol"] != "anthropic/claude-haiku-4-5" {
		t.Fatalf("ModelRoles() = %v", roles)
	}
	roles["smol"] = "changed"
	if s.ModelRole("smol") != "anthropic/claude-haiku-4-5" {
		t.Fatal("ModelRoles() shares storage with live settings")
	}
	if ref := s.ModelRole("slow"); ref != "openai/gpt-5" {
		t.Fatalf("ModelRole folded lookup = %q, want trimmed openai/gpt-5", ref)
	}
	if ref := s.ModelRole("  SLOW "); ref != "openai/gpt-5" {
		t.Fatalf("ModelRole with padding = %q", ref)
	}
	if ref := s.ModelRole("unbound"); ref != "" {
		t.Fatalf("unassigned role = %q, want empty", ref)
	}
	if New(nil).ModelRole("smol") != "" || New(nil).ModelRoles() != nil {
		t.Fatal("nil settings must read as no bindings")
	}
}

// TestSetModelRolePersistsAndPreservesOtherSettings guards the save path: the
// binding lands, an unrelated block in the same file survives, and a second
// role does not displace the first.
func TestSetModelRolePersistsAndPreservesOtherSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	file := userSettingsFile(home)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"model":"claude-x","theme":"dark","modelRoles":{"plan":"openai/gpt-5"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetModelRole("smol", "anthropic/claude-haiku-4-5", ScopeUser); err != nil {
		t.Fatalf("set: %v", err)
	}
	d := readData(t, file)
	if d.Model != "claude-x" || d.Theme != "dark" {
		t.Fatalf("unrelated settings clobbered: model=%q theme=%q", d.Model, d.Theme)
	}
	if d.ModelRoles["smol"] != "anthropic/claude-haiku-4-5" || d.ModelRoles["plan"] != "openai/gpt-5" {
		t.Fatalf("modelRoles = %v", d.ModelRoles)
	}

	// Rebinding the same role replaces its reference instead of adding a second
	// entry under another spelling.
	if err := SetModelRole("SMOL", "inherit", ScopeUser); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	d = readData(t, file)
	if len(d.ModelRoles) != 2 || d.ModelRoles["smol"] != "inherit" {
		t.Fatalf("after rebind modelRoles = %v, want smol=inherit and plan kept", d.ModelRoles)
	}

	if err := ClearModelRole("plan", ScopeUser); err != nil {
		t.Fatalf("clear: %v", err)
	}
	d = readData(t, file)
	if _, ok := d.ModelRoles["plan"]; ok || d.ModelRoles["smol"] != "inherit" {
		t.Fatalf("after clear modelRoles = %v", d.ModelRoles)
	}
}

// TestSetModelRoleEmptyRefMeansInherit pins the explicit spelling: an empty
// reference is stored as "inherit" rather than dropped, so a role taken back to
// the session model still says so in the file.
func TestSetModelRoleEmptyRefMeansInherit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := SetModelRole("task", "   ", ScopeUser); err != nil {
		t.Fatalf("set: %v", err)
	}
	if ref := readData(t, userSettingsFile(home)).ModelRoles["task"]; ref != ModelInheritRef {
		t.Fatalf("task = %q, want %q", ref, ModelInheritRef)
	}
}

// TestUpdateModelRolesAtReplacesBlock confirms a removed key is really removed:
// routing through the merging save would keep the old binding alive forever.
func TestUpdateModelRolesAtReplacesBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	file := userSettingsFile(home)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"modelRoles":{"smol":"old-smol","plan":"old-plan"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateModelRolesAt(map[string]string{"Plan": "openai/gpt-5"}, ScopeUser); err != nil {
		t.Fatalf("update: %v", err)
	}
	d := readData(t, file)
	if _, ok := d.ModelRoles["smol"]; ok {
		t.Fatalf("removed role survived: %v", d.ModelRoles)
	}
	if d.ModelRoles["plan"] != "openai/gpt-5" {
		t.Fatalf("plan = %q, want canonical lowercase key", d.ModelRoles["plan"])
	}
}

// TestUpdateModelRolesAtProjectScope writes at the project level to prove the
// scope argument reaches that file, and the user-level file stays untouched.
func TestUpdateModelRolesAtProjectScope(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)

	projectFile := filepath.Join(work, ".san", "settings.json")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(`{"theme":"light"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetModelRole("designer", "anthropic/claude-opus-4-7", ScopeProject); err != nil {
		t.Fatalf("set: %v", err)
	}
	d := readData(t, projectFile)
	if d.ModelRoles["designer"] != "anthropic/claude-opus-4-7" || d.Theme != "light" {
		t.Fatalf("project file = %+v", d)
	}
	if _, err := os.Stat(userSettingsFile(home)); err == nil {
		t.Fatal("project-scoped write touched the user settings file")
	}
}

// TestModelRolesAtReadsOneScope pins the read the /models Roles tab marks rows
// with: the project's own bindings, canonicalized, and nothing else. A merged
// read would make every inherited role look project-assigned.
func TestModelRolesAtReadsOneScope(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)

	userFile := userSettingsFile(home)
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte(`{"modelRoles":{"smol":"user-smol"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(work, ".san", "settings.json")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(`{"modelRoles":{"Slow":"project-slow","plan":"project-plan"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	project := ModelRolesAt(work, ScopeProject)
	if len(project) != 2 || project["slow"] != "project-slow" || project["plan"] != "project-plan" {
		t.Fatalf("project roles = %v, want the file's bindings keyed canonically", project)
	}
	if _, inherited := project["smol"]; inherited {
		t.Fatalf("project roles include the user's smol: %v", project)
	}

	user := ModelRolesAt(work, ScopeUser)
	if len(user) != 1 || user["smol"] != "user-smol" {
		t.Fatalf("user roles = %v", user)
	}

	// A directory with no settings of its own says so rather than erroring.
	if roles := ModelRolesAt(t.TempDir(), ScopeProject); len(roles) != 0 {
		t.Fatalf("empty project roles = %v", roles)
	}
}

// TestModelRoleValidation rejects a name that could never resolve and a bare
// key with no reference, before anything reaches disk.
func TestModelRoleValidation(t *testing.T) {
	for _, name := range []string{"", "  ", "smol role", "-smol", "9smol", "smol_x", "smol!"} {
		if _, err := ModelRoleName(name); err == nil {
			t.Errorf("ModelRoleName(%q) accepted an invalid name", name)
		}
	}
	// Case and padding are folded rather than rejected: the stored key is what
	// has to be lowercase, and a hand-typed "Slow" names the same role.
	for name, want := range map[string]string{"smol": "smol", " Designer ": "designer", "SMOL": "smol", "code-review-2": "code-review-2"} {
		if got, err := ModelRoleName(name); err != nil || got != want {
			t.Errorf("ModelRoleName(%q) = %q, %v", name, got, err)
		}
	}
	if err := ValidateModelRole("smol", "  "); err == nil {
		t.Error("ValidateModelRole accepted an empty reference")
	}
	if err := ValidateModelRole("smol", ModelInheritRef); err != nil {
		t.Errorf("ValidateModelRole rejected inherit: %v", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	file := userSettingsFile(home)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"modelRoles":{"smol":"kept"}}`
	if err := os.WriteFile(file, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateModelRolesAt(map[string]string{"smol": "new", "Bad Name": "x"}, ScopeUser); err == nil {
		t.Fatal("UpdateModelRolesAt accepted an invalid role name")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != before {
		t.Fatalf("a rejected update touched the file: %s", data)
	}
	if err := SetModelRole("smol", "", ScopeUser); err != nil {
		t.Fatalf("empty reference must mean inherit, got %v", err)
	}
	if ref := readData(t, file).ModelRoles["smol"]; ref != ModelInheritRef {
		t.Fatalf("smol = %q, want %q", ref, ModelInheritRef)
	}
}

// TestModelRolesJSONRoundTrip keeps the on-disk spelling stable: hand-written
// settings.json is the supported way to bind roles outside a panel.
func TestModelRolesJSONRoundTrip(t *testing.T) {
	var d Data
	if err := json.Unmarshal([]byte(`{"modelRoles":{"smol":"openai/gpt-5-mini","slow":"inherit"}}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.ModelRoles["smol"] != "openai/gpt-5-mini" || d.ModelRoles["slow"] != "inherit" {
		t.Fatalf("decoded modelRoles = %v", d.ModelRoles)
	}
	if d.Clone().ModelRoles["smol"] != "openai/gpt-5-mini" {
		t.Fatal("clone lost modelRoles")
	}
	out, err := json.Marshal(&Data{ModelRoles: d.ModelRoles})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"modelRoles":{"slow":"inherit","smol":"openai/gpt-5-mini"}}` {
		t.Fatalf("encoded as %s", out)
	}
	// An empty map is omitted, so a file with no bindings stays clean.
	if out, err := json.Marshal(NewData()); err != nil || string(out) != `{}` {
		t.Fatalf("empty Data encoded as %s, %v", out, err)
	}
}
