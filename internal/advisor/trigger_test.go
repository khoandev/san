package advisor

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/setting"
)

func testBool(v bool) *bool { return &v }

// enabledAdvisor is the smallest configuration that lets an automatic trigger
// fire, so a test states only the knob it is about.
func enabledAdvisor() setting.AdvisorSettings {
	return setting.AdvisorSettings{Enable: testBool(true)}
}

func TestIsMergeCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  string
		want bool
	}{
		{"git merge", "git merge main", true},
		{"git merge no-ff", "git merge --no-ff feature/retry-budget", true},
		{"merge behind cd", "cd repo && git merge origin/main", true},
		{"merge after fetch", "git fetch origin; git merge origin/main", true},
		{"git rebase", "git rebase main", true},
		{"rebase continue", "git rebase --continue", true},
		{"rebase abort", "git rebase --abort", false},
		{"merge abort", "git merge --abort", false},
		{"git with -C", "git -C /repo merge main", true},
		{"gh pr create", "gh pr create --fill", true},
		{"gh pr merge", "gh pr merge 42 --squash", true},
		{"glab mr create", "glab mr create --title retry", true},
		{"git push to main", "git push origin main", true},
		{"git push -u to main", "git push -u origin HEAD:main", true},
		{"git push force to main", "git push --force origin main", true},
		{"git push bare", "git push", true},
		{"git push feature branch", "git push origin feature/retry-budget", false},
		{"git push dry run", "git push --dry-run origin main", false},
		{"git push delete", "git push --delete origin main", false},
		{"git log", "git log --oneline -5", false},
		{"git fetch", "git fetch origin", false},
		{"git status", "git status --porcelain", false},
		{"echo is not a merge", "echo 'git merge'", false},
		{"gh pr list", "gh pr list", false},
		{"gh issue create", "gh issue create --title bug", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMergeCommand(tc.cmd); got != tc.want {
				t.Errorf("IsMergeCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// The matched form names what was stopped, so a block message can say it.
func TestMergeCommandNamesTheForm(t *testing.T) {
	for _, tc := range []struct{ cmd, want string }{
		{"git merge main", "git merge"},
		{"git push origin main", "git push origin main"},
		{"gh pr create", "gh pr create"},
		{"glab mr merge", "glab mr merge"},
	} {
		if got := MergeCommand(tc.cmd, ""); got != tc.want {
			t.Errorf("MergeCommand(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

// A configured base branch adds a target; it does not remove main and master.
func TestMergeCommandConfiguredBase(t *testing.T) {
	if got := MergeCommand("git push origin release", "release"); got == "" {
		t.Error("a push to the configured base branch was not a merge")
	}
	if got := MergeCommand("git push origin release", ""); got != "" {
		t.Errorf("a push to an unconfigured branch matched %q", got)
	}
	if got := MergeCommand("git push origin main", "release"); got == "" {
		t.Error("a push to main stopped being a merge once a base branch was set")
	}
}

// diffOf builds the GitDiff a proc read would return for the given paths.
func diffOf(files []string, added, removed int) proc.GitDiff {
	var text strings.Builder
	for _, file := range files {
		text.WriteString("diff --git a/" + file + " b/" + file + "\n+changed\n")
	}
	return proc.GitDiff{
		Diff:         text.String(),
		Files:        files,
		ChangedFiles: len(files),
		AddedLines:   added,
		RemovedLines: removed,
		Hash:         "hash-" + strings.Join(files, ","),
	}
}

func TestTriggerForDiff(t *testing.T) {
	large := enabledAdvisor()
	large.Triggers.OnLargeChanges = testBool(true)
	large.Thresholds.MinChangedFiles = 3
	large.Thresholds.MinChangedLines = 100

	sensitive := enabledAdvisor()
	sensitive.Thresholds.SensitiveFilePatterns = []string{"**/.env", "**/*.pem"}
	sensitive.Thresholds.SensitiveDirPatterns = []string{"internal/auth", "migrations/**"}

	off := setting.AdvisorSettings{Enable: testBool(false)}

	for _, tc := range []struct {
		name      string
		cfg       setting.AdvisorSettings
		diff      proc.GitDiff
		want      bool
		wantWords []string
	}{
		{
			name: "advisor off never triggers",
			cfg:  off,
			diff: diffOf([]string{"a.go", "b.go", "c.go", "config/.env"}, 500, 500),
		},
		{
			name: "small ordinary change",
			cfg:  large,
			diff: diffOf([]string{"internal/app/model.go"}, 10, 2),
		},
		{
			name:      "file threshold",
			cfg:       large,
			diff:      diffOf([]string{"a.go", "b.go", "c.go"}, 5, 5),
			want:      true,
			wantWords: []string{"3 files changed", "threshold 3"},
		},
		{
			name:      "line threshold",
			cfg:       large,
			diff:      diffOf([]string{"a.go"}, 80, 40),
			want:      true,
			wantWords: []string{"120 lines changed", "threshold 100"},
		},
		{
			name: "thresholds unset never fire",
			cfg: func() setting.AdvisorSettings {
				cfg := enabledAdvisor()
				cfg.Triggers.OnLargeChanges = testBool(true)
				return cfg
			}(),
			diff: diffOf([]string{"a.go", "b.go", "c.go"}, 500, 500),
		},
		{
			name: "large-change trigger off",
			cfg: func() setting.AdvisorSettings {
				cfg := enabledAdvisor()
				cfg.Thresholds.MinChangedFiles = 3
				return cfg
			}(),
			diff: diffOf([]string{"a.go", "b.go", "c.go"}, 500, 500),
		},
		{
			name:      "sensitive file pattern",
			cfg:       sensitive,
			diff:      diffOf([]string{"deploy/.env"}, 1, 0),
			want:      true,
			wantWords: []string{"touches sensitive path", "deploy/.env"},
		},
		{
			name:      "sensitive directory",
			cfg:       sensitive,
			diff:      diffOf([]string{"internal/auth/token.go"}, 2, 1),
			want:      true,
			wantWords: []string{"internal/auth/token.go"},
		},
		{
			name:      "sensitive dir glob",
			cfg:       sensitive,
			diff:      diffOf([]string{"migrations/0007_add_index.sql"}, 4, 0),
			want:      true,
			wantWords: []string{"migrations/0007_add_index.sql"},
		},
		{
			name: "sensitive trigger off",
			cfg: func() setting.AdvisorSettings {
				cfg := sensitive
				cfg.Triggers.SensitivePaths = testBool(false)
				return cfg
			}(),
			diff: diffOf([]string{"deploy/.env"}, 1, 0),
		},
		{
			name: "empty change",
			cfg:  large,
			diff: proc.GitDiff{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trigger, reason := TriggerForDiff(tc.cfg, tc.diff)
			if got := trigger != ""; got != tc.want {
				t.Fatalf("TriggerForDiff = %q (%q), want triggered=%v", trigger, reason, tc.want)
			}
			if !tc.want {
				return
			}
			if trigger != TriggerLargeChange {
				t.Errorf("trigger = %q, want %q", trigger, TriggerLargeChange)
			}
			for _, word := range tc.wantWords {
				if !strings.Contains(reason, word) {
					t.Errorf("reason %q does not mention %q", reason, word)
				}
			}
		})
	}
}

// The paths a review was triggered by can be named on their own, so a panel can
// show why it ran.
func TestSensitivePaths(t *testing.T) {
	cfg := enabledAdvisor()
	cfg.Thresholds.SensitiveFilePatterns = []string{"**/.env"}
	cfg.Thresholds.SensitiveDirPatterns = []string{"internal/auth"}
	files := []string{"deploy/.env", "internal/app/model.go", "internal/auth/token.go", "internal/authz/policy.go"}
	got := SensitivePaths(cfg, files)
	want := []string{"deploy/.env", "internal/auth/token.go"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("SensitivePaths = %v, want %v", got, want)
	}
	if got := SensitivePaths(setting.AdvisorSettings{}, files); got != nil {
		t.Errorf("no patterns matched %v", got)
	}
}

// The cooldown suppresses a duplicate review of unchanged code and never a
// review of different code.
func TestCooldown(t *testing.T) {
	target := Target{Kind: TargetDiff, ID: "working"}
	window := 60 * time.Second
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	c := NewCooldown()
	c.now = func() time.Time { return now }

	if !c.Due(target, "hash-a", window) {
		t.Fatal("a first review was not due")
	}
	c.Mark(target, "hash-a")

	if c.Due(target, "hash-a", window) {
		t.Error("the same diff was due again inside the cooldown")
	}
	if remaining := c.Remaining(target, "hash-a", window); remaining != window {
		t.Errorf("Remaining = %s, want %s", remaining, window)
	}

	// A changed hash always runs, whatever the window says.
	if !c.Due(target, "hash-b", window) {
		t.Error("a changed diff was suppressed by the cooldown")
	}
	c.Mark(target, "hash-b")
	if c.Due(target, "hash-b", window) {
		t.Error("the new diff was due again inside the cooldown")
	}

	// Once the window passes, the same diff is due again.
	now = now.Add(window)
	if !c.Due(target, "hash-b", window) {
		t.Error("the cooldown never expired")
	}
	if remaining := c.Remaining(target, "hash-b", window); remaining != 0 {
		t.Errorf("Remaining after expiry = %s, want 0", remaining)
	}

	// Another target has its own window.
	other := Target{Kind: TargetTask, ID: "task-1"}
	if !c.Due(other, "hash-b", window) {
		t.Error("a different target inherited another target's cooldown")
	}
}

func TestCooldownDisabled(t *testing.T) {
	target := Target{Kind: TargetDiff, ID: "working"}
	c := NewCooldown()
	c.Mark(target, "hash-a")
	if !c.Due(target, "hash-a", 0) {
		t.Error("a zero window suppressed a review")
	}

	var nilTracker *Cooldown
	if !nilTracker.Due(target, "hash-a", time.Hour) {
		t.Error("a nil tracker suppressed a review")
	}
	nilTracker.Mark(target, "hash-a") // must not panic
	if remaining := nilTracker.Remaining(target, "hash-a", time.Hour); remaining != 0 {
		t.Errorf("nil Remaining = %s, want 0", remaining)
	}

	c.Reset()
	if !c.Due(target, "hash-a", time.Hour) {
		t.Error("Reset did not forget the entry")
	}
}

// BuildRequest carries every section of the review context and caps the diff.
func TestBuildRequestCarriesContext(t *testing.T) {
	cfg := enabledAdvisor()
	cfg.Review.CustomPrompt = "Check the retry budget math."
	cfg.Model.MaxFindings = 5
	cfg.Review.Mode = setting.AdvisorModeDeep

	diff := "diff --git a/internal/api.go b/internal/api.go\n+token := os.Getenv(\"TOKEN\")\n" +
		"diff --git a/internal/old.go b/internal/old.go\n-removed\n"
	req := BuildRequest(cfg, ReviewInput{
		Trigger:       TriggerBeforeMerge,
		Target:        Target{Kind: TargetDiff, Base: "main", Head: "feature"},
		Diff:          diff,
		DiffHash:      "hash-1",
		Goal:          "Add the retry budget",
		Acceptance:    []string{"retries stop after 3 attempts", " "},
		Checks:        []string{"go test ./... ok", "go vet ./... ok"},
		Decisions:     []string{"budgets live in settings, not in code"},
		Constraints:   []string{"no new dependencies"},
		DiffTruncated: false,
	})

	if req.Trigger != TriggerBeforeMerge || req.DiffHash != "hash-1" {
		t.Errorf("request identity = %+v", req)
	}
	if req.Diff != diff {
		t.Error("a small diff was altered")
	}
	if req.DiffTruncated {
		t.Error("a small diff was reported truncated")
	}
	if got, want := strings.Join(req.Files, ","), "internal/api.go,internal/old.go"; got != want {
		t.Errorf("files = %q, want %q (derived from the diff)", got, want)
	}
	if got, want := strings.Join(req.Target.Files, ","), "internal/api.go,internal/old.go"; got != want {
		t.Errorf("target files = %q, want %q", got, want)
	}
	if req.Goal != "Add the retry budget" {
		t.Errorf("goal = %q", req.Goal)
	}
	if len(req.Acceptance) != 1 || req.Acceptance[0] != "retries stop after 3 attempts" {
		t.Errorf("acceptance = %v, want the blank entry dropped", req.Acceptance)
	}
	if len(req.Checks) != 2 || len(req.Decisions) != 1 || len(req.Constraints) != 1 {
		t.Errorf("context lists = %v / %v / %v", req.Checks, req.Decisions, req.Constraints)
	}
	if req.CustomPrompt != "Check the retry budget math." {
		t.Errorf("custom prompt = %q, want the configured one", req.CustomPrompt)
	}
	if req.Mode != setting.AdvisorModeDeep {
		t.Errorf("mode = %q, want the configured depth", req.Mode)
	}
	if req.MaxFindings != 5 {
		t.Errorf("max findings = %d, want 5", req.MaxFindings)
	}
}

// A per-review custom prompt wins over the configured one.
func TestBuildRequestCustomPromptOverride(t *testing.T) {
	cfg := enabledAdvisor()
	cfg.Review.CustomPrompt = "configured"
	req := BuildRequest(cfg, ReviewInput{Trigger: TriggerUserRequest, Target: Target{Kind: TargetFile, ID: "a.go"}, CustomPrompt: "this review only"})
	if req.CustomPrompt != "this review only" {
		t.Errorf("custom prompt = %q, want the per-review one", req.CustomPrompt)
	}
}

// A diff over the configured cap is cut on a line boundary and flagged, so the
// reviewer knows a missing hunk is truncation rather than missing work.
func TestBuildRequestCapsDiff(t *testing.T) {
	cfg := enabledAdvisor()
	cfg.Thresholds.MaxDiffBytes = 64

	var diff strings.Builder
	for i := 0; i < 20; i++ {
		diff.WriteString("+line " + strings.Repeat("x", 10) + "\n")
	}
	req := BuildRequest(cfg, ReviewInput{
		Trigger: TriggerBeforeMerge,
		Target:  Target{Kind: TargetDiff},
		Diff:    diff.String(),
	})
	if !req.DiffTruncated {
		t.Error("an over-cap diff was not flagged truncated")
	}
	if len(req.Diff) > 64 {
		t.Errorf("diff length = %d, want <= 64", len(req.Diff))
	}
	if !strings.HasSuffix(req.Diff, "\n") {
		t.Errorf("diff was cut mid-line: %q", req.Diff[len(req.Diff)-20:])
	}
	if !strings.HasPrefix(diff.String(), req.Diff) {
		t.Error("the cut diff is not a prefix of the original")
	}
}

// A read that already truncated the text keeps its flag even when the text fits
// the review's own cap.
func TestBuildRequestKeepsCallerTruncation(t *testing.T) {
	req := BuildRequest(enabledAdvisor(), ReviewInput{
		Trigger:       TriggerBeforeMerge,
		Target:        Target{Kind: TargetDiff},
		Diff:          "diff --git a/x b/x\n+y\n",
		DiffTruncated: true,
	})
	if !req.DiffTruncated {
		t.Error("the caller's truncation flag was dropped")
	}
}

// A cut must not leave half a rune in the prompt. The newline search usually
// saves it; a single long line has no newline to land on, so the byte cut has
// to back off to a rune boundary itself.
func TestTruncateDiffKeepsValidUTF8(t *testing.T) {
	diff := strings.Repeat("é", 10) // 20 bytes, one line, no newline at all
	cut, truncated := truncateDiff(diff, 7)
	if !truncated {
		t.Fatal("nothing was cut")
	}
	if !utf8.ValidString(cut) {
		t.Fatalf("cut text is not valid UTF-8: %q", cut)
	}
	if len(cut) != 6 {
		t.Errorf("cut length = %d, want the 3 whole runes that fit in 7 bytes", len(cut))
	}
	if cut != diff[:len(cut)] {
		t.Error("the cut is not a prefix of the original")
	}
}

// A path containing spaces survives the diff-header parse: both halves of the
// header are equal, so the split is exact rather than a search for " b/".
func TestDiffFilesReadsHeaders(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/internal/api.go b/internal/api.go",
		"--- a/internal/api.go",
		"+++ b/internal/api.go",
		"diff --git a/docs/my notes.md b/docs/my notes.md",
		"+++ b/docs/my notes.md",
		"diff --git a/internal/api.go b/internal/api.go",
	}, "\n")
	got := diffFiles(diff)
	want := []string{"internal/api.go", "docs/my notes.md"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("diffFiles = %v, want %v", got, want)
	}
	if got := diffFiles("not a diff at all\n"); got != nil {
		t.Errorf("diffFiles on prose = %v, want nothing", got)
	}
}
