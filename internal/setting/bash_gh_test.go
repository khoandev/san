package setting

import "testing"

// TestIsReadOnlyGhCommand covers the gh CLI read-only classifier.
//
// gh api is the most common planning command (querying GitHub API), so it gets
// the most thorough coverage. The other subcommands use the sub-action guard.
func TestIsReadOnlyGhCommand(t *testing.T) {
	readOnly := []string{
		// The command that triggered this change.
		"gh api repos/ekmas/neobrutalism-components/contents/apps/web/registry.json --jq '.name'",
		"gh api repos/ekmas/neobrutalism-components/git/trees/main?recursive=1 --jq '.tree[].path'",
		// gh api with explicit GET is still read-only.
		"gh api repos/owner/repo/issues --method GET",
		"gh api /repos/owner/repo --jq '.description'",
		// Listing and viewing are read-only.
		"gh repo view owner/repo",
		"gh repo list owner",
		"gh pr list",
		"gh pr view 42",
		"gh pr status",
		"gh pr diff 42",
		"gh pr checks 42",
		"gh issue list",
		"gh issue view 1",
		"gh issue status",
		"gh release list",
		"gh release view v1.0.0",
		"gh run list",
		"gh run view 123",
		"gh workflow list",
		"gh workflow view deploy.yml",
		"gh search repos neobrutalism",
		"gh search issues --label bug",
		"gh auth status",
		"gh auth token",
		"gh version",
		"gh help",
		"gh variable list",
	}
	for _, cmd := range readOnly {
		args := parseGhArgs(cmd)
		if !isReadOnlyGhCommand(args) {
			t.Errorf("isReadOnlyGhCommand(%q) = false, want true", cmd)
		}
	}
	// End-to-end via IsReadOnlyBashCommand (the entry point Plan Mode uses).
	e2eReadOnly := []string{
		"gh api repos/owner/repo/issues --jq '.[].title' 2>&1 | head -5",
		"gh api repos/owner/repo/git/trees/main?recursive=1 --jq '.tree[].path' | grep -i registry | head -40",
		"gh repo view owner/repo",
		"gh pr list | head -20",
		"gh issue list --label bug",
	}
	for _, cmd := range e2eReadOnly {
		if !IsReadOnlyBashCommand(cmd) {
			t.Errorf("IsReadOnlyBashCommand(%q) = false, want true", cmd)
		}
	}

	notReadOnly := []string{
		// Write methods on gh api.
		"gh api --method POST /repos/owner/repo/issues",
		"gh api /repos/owner/repo/issues -X POST",
		"gh api --method PATCH /repos/owner/repo/issues/1",
		"gh api --method DELETE /repos/owner/repo/issues/1",
		"gh api /graphql --input query.graphql",
		// Write sub-actions.
		"gh issue create",
		"gh issue edit 1",
		"gh issue close 1",
		"gh issue comment 1",
		"gh pr merge 42",
		"gh pr review 42",
		"gh pr ready 42",
		"gh release upload",
		"gh repo archive",
		"gh repo fork",
		"gh run rerun 123",
		"gh run cancel 123",
		"gh workflow run deploy.yml",
		"gh workflow enable deploy.yml",
		"gh variable set MY_VAR",
		"gh variable delete MY_VAR",
		"gh auth login",
		"gh auth logout",
		// Unknown subcommands are refused.
		"gh codespace create",
		"gh gist create",
	}
	for _, cmd := range notReadOnly {
		args := parseGhArgs(cmd)
		if isReadOnlyGhCommand(args) {
			t.Errorf("isReadOnlyGhCommand(%q) = true, want false", cmd)
		}
	}
}

// parseGhArgs mimics what IsReadOnlyBashCommand does: strip the leading "gh"
// and split on spaces so the unit test can call isReadOnlyGhCommand directly.
func parseGhArgs(cmd string) []string {
	args := splitArgs(cmd)
	if len(args) > 0 && args[0] == "gh" {
		return args[1:]
	}
	return args
}
