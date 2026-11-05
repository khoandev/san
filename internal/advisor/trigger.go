package advisor

// This file is the advisor's trigger layer. It answers the questions the
// runner asks before spending a model call — "is this command a merge?", "is
// this change big or sensitive enough to review on its own?", "did we already
// review exactly this code?" — and packs the context one review needs into the
// engine's request.
//
// Nothing here reads the filesystem or calls a model: the caller supplies the
// diff it already read (proc.ReadWorkingDiff) and the settings it resolved, so
// the rules stay testable without a repository, a provider or a session.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/setting"
)

// ---------------------------------------------------------------------------
// Merge detection
// ---------------------------------------------------------------------------

// baseBranches are the branch names a push lands on when nothing else is
// configured. They stay merge targets even when a base branch is set: a push
// that reached main or master unreviewed is exactly what the gate exists to
// stop, whatever the session called its base.
var baseBranches = []string{"main", "master"}

// MergeCommand reports whether a shell command line performs an action the
// pre-merge review gates, and names the form that matched ("git merge",
// "git push origin main", "gh pr create"). It returns "" for a command line
// that integrates nothing.
//
// The command is split by the same AST parse every permission and safety rule
// uses (setting.NormalizedBashCommands), so a merge hidden behind `cd x &&`,
// `;` or a pipeline is still seen, while `echo 'git merge'` is not a merge.
//
// baseBranch names the branch a merge lands on; "" accepts main and master.
// Both are matched even when a base branch is configured. A `git pull` is not
// gated: it brings remote work into the current branch rather than publishing
// the current branch to the base.
func MergeCommand(cmd, baseBranch string) string {
	for _, sub := range setting.NormalizedBashCommands(cmd) {
		if form := mergeForm(sub, baseBranch); form != "" {
			return form
		}
	}
	return ""
}

// IsMergeCommand reports whether cmd performs a merge-like action. It is the
// form for callers that have no base branch to hand.
func IsMergeCommand(cmd string) bool { return MergeCommand(cmd, "") != "" }

// mergeForm answers for one sub-command of a command line.
func mergeForm(sub, baseBranch string) string {
	name, args := splitNormalized(sub)
	switch name {
	case "git":
		return gitMergeForm(args, baseBranch)
	case "gh", "glab":
		return forgeMergeForm(name, args)
	}
	return ""
}

// splitNormalized splits a normalized sub-command ("git:push origin main") back
// into its executable and arguments. The arguments are already shell-free, so
// plain field splitting is exact.
func splitNormalized(sub string) (string, []string) {
	name, rest, ok := strings.Cut(sub, ":")
	if !ok {
		return name, nil
	}
	return name, strings.Fields(rest)
}

// gitMergeForm answers for one git invocation.
func gitMergeForm(args []string, baseBranch string) string {
	sub, rest := gitSubcommand(args)
	switch sub {
	case "merge":
		// Aborting or quitting a merge integrates nothing.
		if hasFlag(rest, "--abort", "--quit") {
			return ""
		}
		return "git merge"
	case "rebase":
		// A rebase rewrites the branch it runs on, which is the integration
		// this gate is about; only the control verbs integrate nothing.
		if hasFlag(rest, "--abort", "--quit", "--skip", "--edit-todo", "--show-current-patch") {
			return ""
		}
		return "git rebase"
	case "push":
		return gitPushForm(rest, baseBranch)
	}
	return ""
}

// gitSubcommand returns git's subcommand and the arguments after it, skipping
// the global options, including the ones that take a separate value, so
// `git -C repo merge main` reads as a merge. Unlike the permission gate's
// parser, an unrecognized option is skipped rather than treated as fatal: this
// answers "could this merge?", where a false negative skips the review that
// matters most and a false positive costs one review.
func gitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return "", nil
		case gitGlobalOptionTakesValue(arg):
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			return arg, args[i+1:]
		}
	}
	return "", nil
}

// gitGlobalOptionTakesValue reports whether a git option consumes the next
// argument, so the scan does not mistake that value for the subcommand.
func gitGlobalOptionTakesValue(arg string) bool {
	switch arg {
	case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--exec-path", "--super-prefix":
		return true
	}
	return false
}

// gitPushForm answers for one `git push`, gating only a push that can land on
// the base branch.
func gitPushForm(args []string, baseBranch string) string {
	var positional []string
	options := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case options && arg == "--":
			options = false
		case options && (arg == "-n" || arg == "--dry-run"):
			// A dry run publishes nothing.
			return ""
		case options && (arg == "-d" || arg == "--delete" || arg == "--prune"):
			// Deleting a ref lands no change on a branch.
			return ""
		case options && pushOptionTakesValue(arg):
			i++
		case options && strings.HasPrefix(arg, "-"):
			// Any other flag (--force, -u, --tags, --all) still pushes.
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) < 2 {
		// No refspec: the push publishes whatever branch HEAD is on, which the
		// command line does not name. Counted, because a review that cannot
		// prove the target is not the base is cheaper than a merge nobody
		// looked at.
		return "git push"
	}
	remote, refspecs := positional[0], positional[1:]
	for _, refspec := range refspecs {
		if pushesToBase(refspec, baseBranch) {
			return "git push " + remote + " " + refspec
		}
	}
	return ""
}

// pushOptionTakesValue reports whether a push option consumes the next
// argument.
func pushOptionTakesValue(arg string) bool {
	switch arg {
	case "-o", "--push-option", "--repo", "--receive-pack", "--exec":
		return true
	}
	return false
}

// pushesToBase reports whether a refspec publishes onto the base branch. Both
// `main` and `HEAD:main` name the base; `HEAD` alone is counted because the
// branch behind it is not on the command line.
func pushesToBase(refspec, baseBranch string) bool {
	_, dst, hasDst := strings.Cut(refspec, ":")
	if !hasDst {
		return matchesBaseBranch(refspec, baseBranch)
	}
	return matchesBaseBranch(dst, baseBranch)
}

// matchesBaseBranch reports whether a ref names a branch a merge would land on.
func matchesBaseBranch(ref, baseBranch string) bool {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "+")
	ref = strings.TrimPrefix(ref, "refs/heads/")
	switch ref {
	case "":
		return false
	case "HEAD", "@":
		// The branch behind HEAD is not on the command line; assume the worst.
		return true
	}
	if baseBranch != "" && ref == strings.TrimPrefix(baseBranch, "refs/heads/") {
		return true
	}
	for _, branch := range baseBranches {
		if ref == branch {
			return true
		}
	}
	return false
}

// forgeGroups maps a forge CLI to the command group that carries its change
// requests: `gh pr …`, `glab mr …`.
var forgeGroups = map[string]string{"gh": "pr", "glab": "mr"}

// forgeActions are the verbs that publish a branch for merge or land it.
var forgeActions = map[string]bool{"create": true, "merge": true}

// forgeMergeForm answers for `gh` and `glab`: opening or merging a pull/merge
// request is the forge-side equivalent of the merge this gate protects.
func forgeMergeForm(name string, args []string) string {
	group, ok := forgeGroups[name]
	if !ok {
		return ""
	}
	for i, arg := range args {
		if arg != group {
			continue
		}
		for _, next := range args[i+1:] {
			if strings.HasPrefix(next, "-") {
				continue
			}
			if forgeActions[next] {
				return name + " " + group + " " + next
			}
			break
		}
		break
	}
	return ""
}

// hasFlag reports whether any of the flags appears in args.
func hasFlag(args []string, flags ...string) bool {
	for _, arg := range args {
		for _, flag := range flags {
			if arg == flag {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Change-size and sensitive-path triggers
// ---------------------------------------------------------------------------

// TriggerForDiff decides whether a change should be reviewed on its own,
// because it is large or because it touches a sensitive path, and explains the
// decision in words the UI can show. It returns "" when no automatic review is
// warranted — including when the advisor is off or the change is empty.
//
// The size arms are inert unless the large-change trigger is on, and a
// threshold of 0 disables its arm, so a session that sets neither threshold
// never reviews for size. The sensitive-path arm is independent of the size
// knobs: a one-line change to an auth file is the change most worth a review.
func TriggerForDiff(cfg setting.AdvisorSettings, diff proc.GitDiff) (Trigger, string) {
	if !cfg.Enabled() {
		return "", ""
	}
	files := changedFiles(diff)
	if len(files) == 0 && strings.TrimSpace(diff.Diff) == "" {
		return "", ""
	}

	var reasons []string
	if cfg.SensitivePaths() {
		if hits := SensitivePaths(cfg, files); len(hits) > 0 {
			reasons = append(reasons, "touches sensitive path "+strings.Join(hits, ", "))
		}
	}
	if cfg.OnLargeChanges() {
		if reason := largeChangeReason(cfg, diff, len(files)); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		return "", ""
	}
	return TriggerLargeChange, strings.Join(reasons, "; ")
}

// SensitivePaths returns the changed paths that match a configured sensitive
// file or directory pattern, in the order they were given. It is exported so a
// caller can show why a review ran without re-deriving the match.
func SensitivePaths(cfg setting.AdvisorSettings, files []string) []string {
	patterns := cfg.Thresholds.SensitiveFilePatterns
	dirs := cfg.Thresholds.SensitiveDirPatterns
	if len(patterns) == 0 && len(dirs) == 0 {
		return nil
	}
	var hits []string
	for _, file := range files {
		if isSensitivePath(file, patterns, dirs) {
			hits = append(hits, file)
		}
	}
	return hits
}

// isSensitivePath reports whether one changed path matches any configured
// pattern. File patterns go through the settings glob matcher, so "**/.env" and
// "*.pem" mean here what they mean in a permission rule. Directory patterns
// match the path or any directory above it, so a bare "internal/auth" covers
// everything under it.
func isSensitivePath(file string, patterns, dirs []string) bool {
	clean := cleanPath(file)
	if clean == "" {
		return false
	}
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) != "" && setting.MatchPathGlob(pattern, clean) {
			return true
		}
	}
	for _, dir := range dirs {
		scope := strings.Trim(cleanPath(dir), "/")
		if scope == "" {
			continue
		}
		if clean == scope || strings.HasPrefix(clean, scope+"/") || setting.MatchPathGlob(dir, clean) {
			return true
		}
	}
	return false
}

// cleanPath normalizes a repository-relative path for matching: slashes only,
// no leading "./" or "/".
func cleanPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "./")
	return strings.TrimPrefix(p, "/")
}

// largeChangeReason reports why a change counts as large, or "" when it does
// not. Only the arms whose threshold is set can fire.
func largeChangeReason(cfg setting.AdvisorSettings, diff proc.GitDiff, files int) string {
	lines := diff.AddedLines + diff.RemovedLines
	minFiles := cfg.Thresholds.MinChangedFiles
	minLines := cfg.Thresholds.MinChangedLines
	byFiles := minFiles > 0 && files >= minFiles
	byLines := minLines > 0 && lines >= minLines
	switch {
	case byFiles && byLines:
		return fmt.Sprintf("%d files and %d lines changed (thresholds %d and %d)", files, lines, minFiles, minLines)
	case byFiles:
		return fmt.Sprintf("%d files changed (threshold %d)", files, minFiles)
	case byLines:
		return fmt.Sprintf("%d lines changed (threshold %d)", lines, minLines)
	}
	return ""
}

// changedFiles returns the paths a diff read reported, falling back to the
// paths named in the diff text.
func changedFiles(diff proc.GitDiff) []string {
	if files := uniqueStrings(trimmed(diff.Files)); len(files) > 0 {
		return files
	}
	return diffFiles(diff.Diff)
}

// ---------------------------------------------------------------------------
// Cooldown
// ---------------------------------------------------------------------------

// Cooldown stops a repeated trigger from paying for the same review twice. It
// remembers, per target, the diff hash that was last reviewed and when: the
// same hash inside the window is suppressed, a changed hash always runs.
//
// The key is the target's identity rather than the full (target, hash) triple,
// which keeps the map bounded by the number of targets instead of the number of
// edits. The behaviour is the same — a cooldown suppresses a duplicate review,
// never a review of different code — and a revert back to an earlier hash runs
// again, which is the safe direction: the code moved since that review.
type Cooldown struct {
	mu   sync.Mutex
	last map[cooldownKey]cooldownEntry
	now  func() time.Time
}

type cooldownKey struct {
	kind TargetKind
	id   string
}

type cooldownEntry struct {
	hash string
	at   time.Time
}

// NewCooldown returns an empty tracker.
func NewCooldown() *Cooldown {
	return &Cooldown{last: make(map[cooldownKey]cooldownEntry), now: time.Now}
}

// Due reports whether a review of this target and diff may run now. A nil
// tracker is due for everything, so wiring may leave it unset, and a window of
// zero or less disables the cooldown entirely.
//
// Due does not record anything: Mark does, so a review that failed to start —
// a provider error, a cancelled context — does not silence the retry.
func (c *Cooldown) Due(target Target, diffHash string, window time.Duration) bool {
	if c == nil || window <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.last[cooldownKey{kind: target.Kind, id: target.ID}]
	if !ok || entry.hash != diffHash {
		return true
	}
	return !c.clock().Before(entry.at.Add(window))
}

// clock reads the tracker's time source, defaulting to the wall clock so a
// zero-value Cooldown works as well as one from NewCooldown.
func (c *Cooldown) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// Mark records that a review of this target and diff is starting now, opening
// its cooldown window.
func (c *Cooldown) Mark(target Target, diffHash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = make(map[cooldownKey]cooldownEntry)
	}
	c.last[cooldownKey{kind: target.Kind, id: target.ID}] = cooldownEntry{hash: diffHash, at: c.clock()}
}

// Remaining reports how long until the same target and diff is due again, so a
// caller can explain why nothing ran. Zero means "now".
func (c *Cooldown) Remaining(target Target, diffHash string, window time.Duration) time.Duration {
	if c == nil || window <= 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.last[cooldownKey{kind: target.Kind, id: target.ID}]
	if !ok || entry.hash != diffHash {
		return 0
	}
	remaining := entry.at.Add(window).Sub(c.clock())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Reset forgets every entry, as a new session does.
func (c *Cooldown) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = make(map[cooldownKey]cooldownEntry)
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

// ReviewInput is everything the caller knows about the work to review: what
// triggered the review, what it covers, and the context it is judged against.
// It is the raw material BuildRequest bounds and the engine runs.
type ReviewInput struct {
	// Trigger names why the review is running.
	Trigger Trigger
	// Target identifies the reviewed object.
	Target Target
	// Diff is the unified diff under review, as read by proc.ReadWorkingDiff
	// or proc.ReadCommitDiff.
	Diff string
	// DiffHash is the hash that came with the diff. BuildRequest never invents
	// one: a different hash for the same change would defeat both the cooldown
	// and the reuse check.
	DiffHash string
	// Files are the changed paths, when the caller has them.
	Files []string
	// DiffTruncated reports that the diff read already cut the text for size.
	DiffTruncated bool
	// Goal is what the work was supposed to accomplish.
	Goal string
	// Acceptance lists the criteria the work is judged against.
	Acceptance []string
	// Checks records test, lint, typecheck and build results.
	Checks []string
	// Decisions records choices already settled in the session.
	Decisions []string
	// Constraints records known limits and risks.
	Constraints []string
	// CustomPrompt adds criteria for this review only; the configured one is
	// used when this is empty.
	CustomPrompt string
}

// BuildRequest turns a review input into the engine's request, applying the
// configured bounds: the diff is capped at the configured size, the changed
// paths are derived from the diff when the caller did not supply them, and the
// configured depth, custom criteria and finding cap travel with the request.
//
// A diff cut here is flagged, so the reviewer knows a missing hunk is a
// truncation and not missing work.
func BuildRequest(cfg setting.AdvisorSettings, in ReviewInput) Request {
	diff, truncated := truncateDiff(in.Diff, cfg.ResolvedMaxDiffBytes())
	files := uniqueStrings(trimmed(in.Files))
	if len(files) == 0 {
		files = diffFiles(diff)
	}
	target := in.Target.Clone()
	if len(target.Files) == 0 {
		target.Files = files
	}
	return Request{
		Trigger:       in.Trigger,
		Target:        target,
		Diff:          diff,
		DiffHash:      in.DiffHash,
		Files:         files,
		DiffTruncated: in.DiffTruncated || truncated,
		Goal:          strings.TrimSpace(in.Goal),
		Acceptance:    trimmed(in.Acceptance),
		Checks:        trimmed(in.Checks),
		Decisions:     trimmed(in.Decisions),
		Constraints:   trimmed(in.Constraints),
		CustomPrompt:  firstNonEmpty(in.CustomPrompt, cfg.Review.CustomPrompt),
		Mode:          cfg.Mode(),
		MaxFindings:   cfg.ResolvedMaxFindings(),
	}
}

// truncateDiff caps diff text at maxBytes on a line boundary, never inside a
// UTF-8 sequence, and reports whether it cut anything. maxBytes <= 0 means no
// cap.
func truncateDiff(diff string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(diff) <= maxBytes {
		return diff, false
	}
	cut := diff[:maxBytes]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i+1]
	}
	// A byte cut can land mid-rune; the trailing partial sequence is at most
	// three bytes, so this loop never runs long.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

// diffFiles lists the paths a unified diff touches, for a request that arrived
// without a file list.
func diffFiles(diff string) []string {
	var files []string
	seen := make(map[string]bool)
	for line := range strings.SplitSeq(diff, "\n") {
		rest, ok := strings.CutPrefix(line, "diff --git ")
		if !ok {
			continue
		}
		path := diffHeaderPath(rest)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}

// diffHeaderPath extracts the path from a "diff --git" header. Diffs are read
// with renames disabled, so the header is "a/<path> b/<path>" with both halves
// equal: their length is exact, which recovers a path containing spaces that a
// search for the " b/" separator would split in the wrong place. A header that
// does not fit that shape (a hand-written diff, a rename) falls back to the
// separator.
func diffHeaderPath(rest string) string {
	body, ok := strings.CutPrefix(rest, "a/")
	if !ok {
		return ""
	}
	const separator = " b/"
	if n := (len(body) - len(separator)) / 2; n > 0 && len(body) == 2*n+len(separator) {
		if head := body[:n]; body[n:] == separator+head {
			return cleanPath(head)
		}
	}
	if i := strings.LastIndex(body, separator); i > 0 {
		return cleanPath(body[i+len(separator):])
	}
	return ""
}

// uniqueStrings drops duplicates and blanks while keeping the first-seen order,
// so a file list does not repeat a path the diff named twice.
func uniqueStrings(values []string) []string {
	var out []string
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
