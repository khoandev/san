package proc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// GitStatus is a local, read-only repository snapshot. Repository is a display
// label, never a raw remote URL (which may contain credentials).
type GitStatus struct {
	Branch     string
	Repository string
	Staged     int
	Modified   int
	Untracked  int
	Conflicts  int
	Ahead      int
	Behind     int
}

// ReadGitStatus reads the worktree containing cwd, including linked worktrees
// and unborn or detached HEADs. It never contacts a remote, updates the index,
// or runs fsmonitor hooks. The caller supplies a bounded context.
// Outside a worktree, or if Git is unavailable, it returns an error.
func ReadGitStatus(ctx context.Context, cwd string) (GitStatus, error) {
	out, err := gitOutput(ctx, cwd, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return GitStatus{}, err
	}
	status := parseGitStatus(string(out))

	config, err := gitOutput(ctx, cwd, "config", "--null", "--get-regexp", `^(remote\..*\.url|branch\..*\.remote)$`)
	if err == nil {
		status.Repository = gitRepositoryLabel(string(config), status.Branch)
	}
	if status.Repository == "" {
		root, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
		if err == nil {
			status.Repository = gitDisplayText(filepath.Base(strings.TrimSpace(string(root))))
		}
	}
	return status, nil
}

// DefaultGitDiffMaxBytes bounds the diff text a diff read returns when
// DiffOptions.MaxBytes is unset: 256 KiB is roughly where a review prompt stops
// being about the change and starts being about the diff.
const DefaultGitDiffMaxBytes = 256 * 1024

// DiffOptions selects what a diff read compares against and how much text it
// returns. The zero value means "the default base, capped at the default size".
type DiffOptions struct {
	// Base is the ref to diff against. Empty resolves in this order: the
	// repository's advisor.baseBranch config, origin/HEAD, main, master, then
	// HEAD — which leaves only uncommitted changes. An explicit value is
	// resolved to a commit before it reaches the command line, so a ref that
	// looks like an option can never be interpreted as one, and a ref that does
	// not resolve is an error rather than a silent fallback: reviewing a
	// different change than the caller asked for is worse than failing.
	Base string
	// MaxBytes caps GitDiff.Diff. Zero uses DefaultGitDiffMaxBytes; a negative
	// value means no cap at all (the caller has its own bound).
	MaxBytes int
	// Paths restricts the read to these pathspecs, so a review can look at one
	// file instead of the whole worktree. Empty means every changed path. A
	// path that matches nothing is an empty diff, not an error.
	Paths []string
}

// pathspecArgs appends a pathspec restriction to a Git invocation. The "--"
// separator is what keeps a path that begins with "-" a path and not an
// option.
func pathspecArgs(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	args := make([]string, 0, len(paths)+1)
	args = append(args, "--")
	return append(args, paths...)
}

// GitDiff is a read-only diff snapshot with the metadata a review needs to
// judge size and identity without re-running Git: what it was taken against,
// which files moved, how many lines changed, and a hash that makes "have we
// already reviewed exactly this?" cheap.
type GitDiff struct {
	// Base is the commit the diff was taken against, or "" for a commit diff
	// and for an unborn repository (index against the worktree).
	Base string
	// Diff is the unified diff text, possibly truncated at MaxBytes.
	Diff string
	// Files are the changed paths, tracked changes first then untracked ones.
	Files []string
	// ChangedFiles is len(Files), kept so callers need not count.
	ChangedFiles int
	// AddedLines and RemovedLines are the totals across all changed files;
	// binary files contribute nothing to either.
	AddedLines   int
	RemovedLines int
	// Untracked lists files Git does not track. Nothing is staged here — this
	// package only reads — so their content is absent from Diff and only their
	// names are reported.
	Untracked []string
	// Hash identifies the reviewed change: a SHA-256 over the full diff text
	// (before truncation) and the sorted file list. Two reads of the same
	// change hash equal, which is what lets a re-run or a repeated merge
	// attempt skip a review it already has.
	Hash string
	// Truncated reports that Diff was cut at MaxBytes.
	Truncated bool
}

// ReadWorkingDiff reads the diff of the worktree against a base commit,
// including both staged and unstaged changes, and lists untracked files. It
// never stages, commits, checks out, or otherwise writes to the repository: a
// review must be able to look without leaving a trace. The caller supplies a
// bounded context.
func ReadWorkingDiff(ctx context.Context, cwd string, opts DiffOptions) (GitDiff, error) {
	base, err := resolveDiffBase(ctx, cwd, opts.Base)
	if err != nil {
		return GitDiff{}, err
	}
	raw, err := gitRun(ctx, cwd, diffArgs(base, false, opts.Paths)...)
	if err != nil {
		return GitDiff{}, err
	}
	stat, err := gitRun(ctx, cwd, diffArgs(base, true, opts.Paths)...)
	if err != nil {
		return GitDiff{}, err
	}
	untracked, err := gitUntracked(ctx, cwd, opts.Paths)
	if err != nil {
		return GitDiff{}, err
	}
	files, added, removed := parseNumstat(string(stat))
	files = append(files, untracked...)
	return newGitDiff(base, string(raw), files, untracked, added, removed, opts.MaxBytes), nil
}

// ReadCommitDiff reads one commit's diff, with its message header as review
// context. DiffOptions.Base is ignored: the commit is the subject. An unknown
// sha is an error, never an empty diff.
func ReadCommitDiff(ctx context.Context, cwd, sha string, opts DiffOptions) (GitDiff, error) {
	if strings.TrimSpace(sha) == "" {
		return GitDiff{}, fmt.Errorf("commit sha is required")
	}
	oid, ok := gitCommit(ctx, cwd, sha)
	if !ok {
		return GitDiff{}, fmt.Errorf("no commit %q in this repository", sha)
	}
	raw, err := gitRun(ctx, cwd, showArgs(oid, false)...)
	if err != nil {
		return GitDiff{}, err
	}
	stat, err := gitRun(ctx, cwd, showArgs(oid, true)...)
	if err != nil {
		return GitDiff{}, err
	}
	files, added, removed := parseNumstat(string(stat))
	return newGitDiff("", string(raw), files, nil, added, removed, opts.MaxBytes), nil
}

// diffArgs builds the read-only diff invocation. External diff drivers and
// textconv filters are disabled because both run repository-configured
// programs: a review must not execute code it was asked to inspect.
func diffArgs(base string, numstat bool, paths []string) []string {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "-U3"}
	if numstat {
		args = append(args, "--numstat")
	}
	if base != "" {
		args = append(args, base)
	}
	return append(args, pathspecArgs(paths)...)
}

// showArgs builds the read-only commit-diff invocation, with the same disabled
// execution paths as diffArgs.
func showArgs(oid string, numstat bool) []string {
	args := []string{"show", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "-U3"}
	if numstat {
		return append(args, "--numstat", "--format=", oid)
	}
	return append(args, "--format=medium", oid)
}

// resolveDiffBase picks the commit the worktree is compared against. An unborn
// repository has no commit at all and resolves to "", which diffs the index
// against the worktree.
func resolveDiffBase(ctx context.Context, cwd, requested string) (string, error) {
	if requested != "" {
		if oid, ok := gitCommit(ctx, cwd, requested); ok {
			return oid, nil
		}
		return "", fmt.Errorf("base ref %q is not a commit in this repository", requested)
	}
	var candidates []string
	if out, err := gitRun(ctx, cwd, "config", "--get", "advisor.baseBranch"); err == nil {
		if configured := strings.TrimSpace(string(out)); configured != "" {
			candidates = append(candidates, configured)
		}
	}
	candidates = append(candidates, "origin/HEAD", "main", "master", "HEAD")
	for _, candidate := range candidates {
		if oid, ok := gitCommit(ctx, cwd, candidate); ok {
			return oid, nil
		}
	}
	return "", nil
}

// gitCommit resolves a ref to a full commit id. A ref beginning with "-" is
// refused outright: it would be read as an option by the git command line, and
// the caller could not know what the review then looked at.
func gitCommit(ctx context.Context, cwd, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", false
	}
	out, err := gitRun(ctx, cwd, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", false
	}
	oid := strings.TrimSpace(string(out))
	return oid, oid != ""
}

// gitUntracked lists the untracked files in the worktree. It reads the status
// rather than adding anything to the index, so the listing is side-effect free.
func gitUntracked(ctx context.Context, cwd string, paths []string) ([]string, error) {
	args := append([]string{"status", "--porcelain=v2", "-z", "--untracked-files=all"}, pathspecArgs(paths)...)
	out, err := gitRun(ctx, cwd, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		switch {
		case strings.HasPrefix(record, "? "):
			files = append(files, gitDisplayText(strings.TrimPrefix(record, "? ")))
		case strings.HasPrefix(record, "2 "):
			// A rename/copy carries its original path as the next record, which
			// is not a status record even if the filename begins with "? ".
			i++
		}
	}
	return files, nil
}

// parseNumstat reads `git diff --numstat` output. Binary files report "-" for
// both counts, which contributes nothing to the line totals.
func parseNumstat(out string) (files []string, added, removed int) {
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		if n, err := strconv.Atoi(fields[0]); err == nil {
			added += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			removed += n
		}
		if name := gitDisplayText(fields[2]); name != "" {
			files = append(files, name)
		}
	}
	return files, added, removed
}

// newGitDiff assembles the snapshot, applying the size cap and hashing the
// untruncated text so identity does not depend on how much was returned.
func newGitDiff(base, diff string, files, untracked []string, added, removed, maxBytes int) GitDiff {
	if maxBytes == 0 {
		maxBytes = DefaultGitDiffMaxBytes
	}
	text, truncated := truncateDiffText(diff, maxBytes)
	return GitDiff{
		Base:         base,
		Diff:         text,
		Files:        files,
		ChangedFiles: len(files),
		AddedLines:   added,
		RemovedLines: removed,
		Untracked:    untracked,
		Hash:         diffHash(diff, files),
		Truncated:    truncated,
	}
}

// truncateDiffText cuts the diff at a line boundary at or below maxBytes, so a
// truncated diff still ends on a complete line. A negative maxBytes means no
// cap.
func truncateDiffText(diff string, maxBytes int) (string, bool) {
	if maxBytes < 0 || len(diff) <= maxBytes {
		return diff, false
	}
	cut := maxBytes
	if i := strings.LastIndexByte(diff[:cut], '\n'); i >= 0 {
		cut = i + 1
	}
	return diff[:cut], true
}

// diffHash is the change's identity: the diff text plus the sorted file list.
// The file list is part of it so an untracked file appearing (whose content the
// diff cannot show) still changes the hash.
func diffHash(diff string, files []string) string {
	sorted := slices.Clone(files)
	slices.Sort(sorted)
	h := sha256.New()
	h.Write([]byte(diff))
	h.Write([]byte{0})
	for _, file := range sorted {
		h.Write([]byte(file))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// gitRun is gitOutput with a readable error: Git reports why it refused on
// stderr, and "exit status 1" alone tells a reviewer nothing.
func gitRun(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	out, err := gitOutput(ctx, cwd, args...)
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if message := strings.TrimSpace(string(exitErr.Stderr)); message != "" {
			return out, fmt.Errorf("git %s: %s", args[0], message)
		}
	}
	return out, fmt.Errorf("git %s: %w", args[0], err)
}

func gitOutput(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	args = append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	// Clean filters may start children that keep output pipes open after Git exits.
	DetachSession(cmd)
	cmd.Cancel = func() error { return TerminateGroup(cmd, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	// An inherited Git command environment must not redirect this read to a
	// different repository or index than the UI's current working directory.
	cmd.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
			return true
		}
		return false
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := StartGroup(cmd); err != nil {
		return nil, err
	}
	err := cmd.Wait()
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitErr.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

func parseGitStatus(output string) GitStatus {
	var status GitStatus
	var oid string
	records := strings.Split(output, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		switch {
		case strings.HasPrefix(record, "# branch.head "):
			status.Branch = gitDisplayText(strings.TrimPrefix(record, "# branch.head "))
		case strings.HasPrefix(record, "# branch.oid "):
			oid = strings.TrimPrefix(record, "# branch.oid ")
		case strings.HasPrefix(record, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(record, "# branch.ab "))
			if len(fields) == 2 {
				status.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				status.Behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
		case strings.HasPrefix(record, "1 "), strings.HasPrefix(record, "2 "):
			if len(record) >= 4 {
				if record[2] != '.' {
					status.Staged++
				}
				if record[3] != '.' {
					status.Modified++
				}
			}
			if record[0] == '2' {
				// A rename/copy has a second NUL-delimited original path. It is
				// not a status record, even if the filename begins with "? ".
				i++
			}
		case strings.HasPrefix(record, "? "):
			status.Untracked++
		case strings.HasPrefix(record, "u "):
			status.Conflicts++
		}
	}
	if status.Branch == "(detached)" && oid != "" && oid != "(initial)" {
		status.Branch = oid[:min(7, len(oid))] + " (detached)"
	}
	return status
}

func gitRepositoryLabel(config, branch string) string {
	remotes := make(map[string]string)
	trackedRemote := ""
	for record := range strings.SplitSeq(config, "\x00") {
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			continue
		}
		if key == "branch."+branch+".remote" {
			trackedRemote = value
		}
		if name, ok := strings.CutPrefix(key, "remote."); ok {
			if name, ok = strings.CutSuffix(name, ".url"); ok && remotes[name] == "" {
				remotes[name] = value
			}
		}
	}
	if label := gitRemoteLabel(remotes[trackedRemote]); label != "" {
		return label
	}
	if label := gitRemoteLabel(remotes["origin"]); label != "" {
		return label
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if label := gitRemoteLabel(remotes[name]); label != "" {
			return label
		}
	}
	return ""
}

func gitRemoteLabel(remote string) string {
	if remote == "" || (!strings.Contains(remote, "://") && strings.Contains(remote, "::")) {
		// Remote-helper syntax may contain commands or credentials, not a
		// repository URL. Never render that opaque value.
		return ""
	}
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		if u.Scheme == "file" {
			return gitDisplayText(strings.TrimSuffix(filepath.Base(u.Path), ".git"))
		}
		if u.Hostname() != "" {
			// Deliberately omit userinfo, port, query and fragment.
			return gitDisplayText(u.Hostname() + "/" + strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"))
		}
		return ""
	}
	// SCP-style SSH remotes, e.g. git@github.com:owner/repo.git.
	if userHost, path, ok := strings.Cut(remote, ":"); ok && !strings.ContainsAny(userHost, `/\\`) && len(userHost) > 1 {
		_, host, hasUser := strings.Cut(userHost, "@")
		if !hasUser {
			host = userHost
		}
		return gitDisplayText(host + "/" + strings.TrimSuffix(strings.Trim(path, "/"), ".git"))
	}
	// Local remotes can contain private directory names; show only the basename.
	return gitDisplayText(strings.TrimSuffix(filepath.Base(strings.ReplaceAll(remote, `\`, "/")), ".git"))
}

func gitDisplayText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}
