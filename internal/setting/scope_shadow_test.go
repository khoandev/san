package setting

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScopeUserPersonaWriteReportsProjectShadow pins the scope contract: a
// write that a project pin would silently shadow has to say so instead of
// landing somewhere it cannot win. Today the caller writes project scope on the
// user's behalf, which is how a personal choice ends up in the repository.
func TestScopeUserPersonaWriteReportsProjectShadow(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)

	projectFile := filepath.Join(work, ".san", "settings.json")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(`{"persona":"project-pin"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SavePersonaAt(work, "mine", ScopeUser); err == nil {
		t.Fatal("user-scope persona write was silently shadowed by the project pin")
	}
	if got := PersonaAt(work, ScopeProject); got != "project-pin" {
		t.Fatalf("project pin changed to %q", got)
	}
}
