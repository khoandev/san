package proc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// diffCtx bounds one diff read so a hung Git cannot stall the suite.
func diffCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// gitLine runs a read-only Git command for the fixture and returns its output.
func gitLine(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestReadWorkingDiffUncommitted(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "tracked.txt", "one\n")
	statusGit(t, repo, "add", "tracked.txt")
	statusGit(t, repo, "commit", "-m", "initial")
	head := gitLine(t, repo, "rev-parse", "HEAD")
	writeStatusFile(t, repo, "tracked.txt", "one\ntwo\n")

	got, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if !strings.Contains(got.Diff, "+two") {
		t.Errorf("diff missing the added line:\n%s", got.Diff)
	}
	if got.Base != head {
		t.Errorf("base = %q, want HEAD %q", got.Base, head)
	}
	if got.ChangedFiles != 1 || got.Files[0] != "tracked.txt" {
		t.Errorf("files = %v, want [tracked.txt]", got.Files)
	}
	if got.AddedLines != 1 || got.RemovedLines != 0 {
		t.Errorf("line counts = +%d -%d, want +1 -0", got.AddedLines, got.RemovedLines)
	}
	if got.Truncated || len(got.Untracked) != 0 {
		t.Errorf("unexpected truncation or untracked files: %+v", got)
	}
	if len(got.Hash) != 64 {
		t.Errorf("hash = %q, want a sha256 hex digest", got.Hash)
	}
}

// The default base is the branch point, not the last commit: a review of
// "what would merge" must see every commit on the branch, not just the tip.
func TestReadWorkingDiffDefaultsToBaseBranch(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "base.txt", "base\n")
	statusGit(t, repo, "add", "base.txt")
	statusGit(t, repo, "commit", "-m", "base commit")
	mainOID := gitLine(t, repo, "rev-parse", "HEAD")

	statusGit(t, repo, "checkout", "-b", "feature")
	writeStatusFile(t, repo, "feature.txt", "feature\n")
	statusGit(t, repo, "add", "feature.txt")
	statusGit(t, repo, "commit", "-m", "feature commit")
	featureOID := gitLine(t, repo, "rev-parse", "HEAD")

	got, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if got.Base != mainOID {
		t.Errorf("base = %q, want main %q (not the feature tip %q)", got.Base, mainOID, featureOID)
	}
	if !strings.Contains(got.Diff, "+feature") {
		t.Errorf("diff does not include the branch commit:\n%s", got.Diff)
	}

	// An explicit base wins over the resolved one.
	explicit, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{Base: featureOID})
	if err != nil {
		t.Fatalf("read diff with explicit base: %v", err)
	}
	if explicit.Base != featureOID || explicit.ChangedFiles != 0 {
		t.Errorf("explicit base %q gave %d changed files, want the tip and none", explicit.Base, explicit.ChangedFiles)
	}

	// The repository config names a base without any code change.
	statusGit(t, repo, "config", "advisor.baseBranch", featureOID)
	configured, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read diff with configured base: %v", err)
	}
	if configured.Base != featureOID {
		t.Errorf("configured base = %q, want %q", configured.Base, featureOID)
	}
}

// A base that does not resolve must fail rather than silently reviewing some
// other change, and an option-shaped ref must never reach the Git command line.
func TestReadWorkingDiffRejectsBadBase(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "tracked.txt", "one\n")
	statusGit(t, repo, "add", "tracked.txt")
	statusGit(t, repo, "commit", "-m", "initial")

	for _, base := range []string{"no-such-branch", "--output=/tmp/advisor-injection"} {
		got, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{Base: base})
		if err == nil || !reflect.DeepEqual(got, GitDiff{}) {
			t.Errorf("base %q = %+v, err = %v; want a refusal", base, got, err)
		}
	}
	if _, err := os.Stat("/tmp/advisor-injection"); !os.IsNotExist(err) {
		t.Fatalf("an option-shaped base was executed by git: %v", err)
	}
}

func TestReadWorkingDiffUntracked(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "tracked.txt", "one\n")
	statusGit(t, repo, "add", "tracked.txt")
	statusGit(t, repo, "commit", "-m", "initial")
	writeStatusFile(t, repo, "untracked.txt", "new\n")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStatusFile(t, filepath.Join(repo, "sub"), "nested.txt", "nested\n")

	got, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	wantUntracked := []string{"sub/nested.txt", "untracked.txt"}
	if !slices.Equal(got.Untracked, wantUntracked) {
		t.Errorf("untracked = %v, want %v", got.Untracked, wantUntracked)
	}
	if got.ChangedFiles != 2 {
		t.Errorf("changed files = %d, want 2 (untracked files count)", got.ChangedFiles)
	}
	if got.Diff != "" {
		t.Errorf("untracked content must not appear in the diff:\n%s", got.Diff)
	}

	// Reading must not stage anything: the index is exactly as it was.
	status, err := ReadGitStatus(diffCtx(t), repo)
	if err != nil {
		t.Fatal(err)
	}
	if status.Staged != 0 || status.Untracked != 2 {
		t.Errorf("index touched by a read: %+v", status)
	}
}

// A pathspec is how /advisor file reviews one path: the read must return only
// that path's change, tracked and untracked alike, and must not let a
// path-shaped option reach Git.
func TestReadWorkingDiffScopesToPaths(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "kept.txt", "one\n")
	writeStatusFile(t, repo, "other.txt", "one\n")
	statusGit(t, repo, "add", "kept.txt", "other.txt")
	statusGit(t, repo, "commit", "-m", "initial")
	writeStatusFile(t, repo, "kept.txt", "one\ntwo\n")
	writeStatusFile(t, repo, "other.txt", "one\nchanged\n")
	writeStatusFile(t, repo, "untracked.txt", "new\n")

	got, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{Paths: []string{"kept.txt"}})
	if err != nil {
		t.Fatalf("read scoped diff: %v", err)
	}
	if !strings.Contains(got.Diff, "kept.txt") || strings.Contains(got.Diff, "other.txt") {
		t.Errorf("scoped diff covers the wrong files:\n%s", got.Diff)
	}
	if !slices.Equal(got.Files, []string{"kept.txt"}) {
		t.Errorf("files = %v, want only the scoped path", got.Files)
	}
	if len(got.Untracked) != 0 {
		t.Errorf("untracked = %v, want none outside the pathspec", got.Untracked)
	}

	// The whole read is available when no path is given.
	all, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if all.ChangedFiles <= got.ChangedFiles {
		t.Errorf("unscoped read saw %d files, want more than the scoped %d", all.ChangedFiles, got.ChangedFiles)
	}

	// A path that matches nothing is an empty diff, not an option.
	empty, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{Paths: []string{"--no-such-file"}})
	if err != nil {
		t.Fatalf("path-shaped argument reached Git as an option: %v", err)
	}
	if empty.ChangedFiles != 0 || empty.Diff != "" {
		t.Errorf("unmatched path = %+v, want an empty diff", empty)
	}
}

func TestReadWorkingDiffEmptyAndStable(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "tracked.txt", "one\n")
	statusGit(t, repo, "add", "tracked.txt")
	statusGit(t, repo, "commit", "-m", "initial")

	first, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if first.Diff != "" || first.ChangedFiles != 0 || first.AddedLines != 0 || first.RemovedLines != 0 {
		t.Errorf("clean worktree = %+v, want an empty diff", first)
	}
	if len(first.Hash) != 64 {
		t.Errorf("hash = %q, want a digest even for an empty diff", first.Hash)
	}

	second, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash {
		t.Errorf("the same change hashed differently: %q vs %q", first.Hash, second.Hash)
	}
}

// The cap must bound what a review is given without changing the change's
// identity: the hash covers the full text.
func TestReadWorkingDiffTruncates(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "big.txt", "one\n")
	statusGit(t, repo, "add", "big.txt")
	statusGit(t, repo, "commit", "-m", "initial")
	var content strings.Builder
	for i := range 500 {
		content.WriteString("line ")
		content.WriteString(strings.Repeat("x", i%40))
		content.WriteString("\n")
	}
	writeStatusFile(t, repo, "big.txt", content.String())

	full, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if full.Truncated {
		t.Fatal("the default cap should not cut a 500-line diff")
	}

	capped, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{MaxBytes: 200})
	if err != nil {
		t.Fatalf("read capped diff: %v", err)
	}
	if !capped.Truncated {
		t.Error("Truncated = false, want true at a 200-byte cap")
	}
	if len(capped.Diff) > 200 {
		t.Errorf("capped diff is %d bytes, want at most 200", len(capped.Diff))
	}
	if !strings.HasSuffix(capped.Diff, "\n") {
		t.Error("a truncated diff should end on a complete line")
	}
	if capped.Hash != full.Hash {
		t.Errorf("truncation changed the hash: %q vs %q", capped.Hash, full.Hash)
	}
	if capped.AddedLines != full.AddedLines || capped.ChangedFiles != full.ChangedFiles {
		t.Errorf("truncation changed the metadata: %+v vs %+v", capped, full)
	}

	uncapped, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{MaxBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if uncapped.Truncated || len(uncapped.Diff) != len(full.Diff) {
		t.Errorf("a negative cap should mean no cap, got %d bytes", len(uncapped.Diff))
	}
}

// A binary file yields a "Binary files differ" placeholder: no panic, no
// half-decoded content, and no invented line counts.
func TestReadWorkingDiffBinary(t *testing.T) {
	repo := initStatusRepo(t)
	binary := []byte{0x00, 0x01, 0x02, 0x00, 0xff}
	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	statusGit(t, repo, "add", "blob.bin")
	statusGit(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), append(binary, 0x00, 0x09), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read binary diff: %v", err)
	}
	if !strings.Contains(got.Diff, "Binary files") {
		t.Errorf("binary diff = %q, want a binary placeholder", got.Diff)
	}
	if got.ChangedFiles != 1 || got.AddedLines != 0 || got.RemovedLines != 0 {
		t.Errorf("binary metadata = %+v, want one file and no line counts", got)
	}
}

// An unborn repository has no commit to diff against; the read must still
// succeed and report the untracked work instead of failing.
func TestReadWorkingDiffUnbornRepository(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "first.txt", "hello\n")

	got, err := ReadWorkingDiff(diffCtx(t), repo, DiffOptions{})
	if err != nil {
		t.Fatalf("read diff in an unborn repository: %v", err)
	}
	if got.Base != "" {
		t.Errorf("base = %q, want empty for an unborn repository", got.Base)
	}
	if !slices.Equal(got.Untracked, []string{"first.txt"}) {
		t.Errorf("untracked = %v, want [first.txt]", got.Untracked)
	}
}

func TestReadWorkingDiffUnavailable(t *testing.T) {
	t.Run("outside repository", func(t *testing.T) {
		got, err := ReadWorkingDiff(diffCtx(t), t.TempDir(), DiffOptions{})
		if err == nil || !reflect.DeepEqual(got, GitDiff{}) {
			t.Fatalf("non-repository = %+v, err = %v", got, err)
		}
		if !strings.Contains(err.Error(), "git diff") {
			t.Errorf("error should name the git command, got %v", err)
		}
	})
	t.Run("missing git", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		got, err := ReadWorkingDiff(diffCtx(t), t.TempDir(), DiffOptions{})
		if err == nil || !reflect.DeepEqual(got, GitDiff{}) {
			t.Fatalf("missing git = %+v, err = %v", got, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := ReadWorkingDiff(ctx, t.TempDir(), DiffOptions{}); err == nil || !reflect.DeepEqual(got, GitDiff{}) {
			t.Fatalf("cancelled read = %+v, err = %v", got, err)
		}
	})
}

func TestReadCommitDiff(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "tracked.txt", "one\n")
	statusGit(t, repo, "add", "tracked.txt")
	statusGit(t, repo, "commit", "-m", "initial")
	writeStatusFile(t, repo, "tracked.txt", "one\ntwo\n")
	statusGit(t, repo, "add", "tracked.txt")
	statusGit(t, repo, "commit", "-m", "add the second line")
	sha := gitLine(t, repo, "rev-parse", "HEAD")

	got, err := ReadCommitDiff(diffCtx(t), repo, sha, DiffOptions{})
	if err != nil {
		t.Fatalf("read commit diff: %v", err)
	}
	if !strings.Contains(got.Diff, "add the second line") {
		t.Errorf("commit diff should carry the message header:\n%s", got.Diff)
	}
	if !strings.Contains(got.Diff, "+two") {
		t.Errorf("commit diff missing the change:\n%s", got.Diff)
	}
	if got.Base != "" || got.ChangedFiles != 1 || got.AddedLines != 1 || got.RemovedLines != 0 {
		t.Errorf("commit metadata = %+v, want one file and one added line", got)
	}
	if len(got.Hash) != 64 || got.Truncated {
		t.Errorf("commit diff hash/truncation = %q/%v", got.Hash, got.Truncated)
	}

	// The short sha resolves to the same review.
	short, err := ReadCommitDiff(diffCtx(t), repo, sha[:8], DiffOptions{})
	if err != nil {
		t.Fatalf("read short sha: %v", err)
	}
	if short.Hash != got.Hash {
		t.Errorf("short sha hashed differently: %q vs %q", short.Hash, got.Hash)
	}
}

func TestReadCommitDiffUnknown(t *testing.T) {
	repo := initStatusRepo(t)
	writeStatusFile(t, repo, "tracked.txt", "one\n")
	statusGit(t, repo, "add", "tracked.txt")
	statusGit(t, repo, "commit", "-m", "initial")

	for _, sha := range []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "  "} {
		got, err := ReadCommitDiff(diffCtx(t), repo, sha, DiffOptions{})
		if err == nil || !reflect.DeepEqual(got, GitDiff{}) {
			t.Fatalf("sha %q = %+v, err = %v; want a clear error", sha, got, err)
		}
		if !strings.Contains(err.Error(), "commit") {
			t.Errorf("error should name the missing commit, got %v", err)
		}
	}
}

func TestDiffHashIdentity(t *testing.T) {
	base := diffHash("diff text", []string{"a.go", "b.go"})
	if base != diffHash("diff text", []string{"b.go", "a.go"}) {
		t.Error("file order must not change the hash")
	}
	if base == diffHash("other text", []string{"a.go", "b.go"}) {
		t.Error("different diff text must change the hash")
	}
	if base == diffHash("diff text", []string{"a.go"}) {
		t.Error("a different file list must change the hash")
	}
}
