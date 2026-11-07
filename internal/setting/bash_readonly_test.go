package setting

import "testing"

// TestReadOnlyBashArgumentGuards covers the commands whose read-only use rests
// on their arguments rather than their name: xargs dispatches, sed can write or
// execute through its own program, go is safe per subcommand, and a few
// commands take an output file as their second operand.
func TestReadOnlyBashArgumentGuards(t *testing.T) {
	readOnly := []string{
		// The survey command planning actually runs: find, xargs, sort, tail.
		"find internal/setting -type f -name '*.go' | xargs wc -l | sort -n | tail -60",
		"find . -name '*.go' | xargs -0 -r grep -l foo",
		"find . -name '*.go' | xargs", // no command: xargs runs echo
		"ls | xargs -n 1 basename",
		"sed -n 120,175p internal/llm/registry.go",
		`sed -n '/func Parse/,/^}/p' internal/llm/registry.go`,
		"sed 's/foo/bar/g' a.go",
		"sed -rn 's/a/b/p' a.go",
		"sed -e 's/a/b/' -e '2d' a.go",
		"sed -n '1,5p' wfile.go", // a file whose name starts with w still reads
		"cat a.go | sed 's/x/y/' | head -5",
		`sed -n '$!N;s/\n//' f.go`,
		"go doc fmt.Println",
		"go env",
		"go version",
		"gofmt -l internal",
		"uniq -c in.txt | sort -rn",
		"xxd a.bin",
		"yq '.name' go.mod",
		"printenv PATH",
		"if [ -f go.mod ]; then echo yes; fi",
	}
	for _, cmd := range readOnly {
		if !IsReadOnlyBashCommand(cmd) {
			t.Errorf("IsReadOnlyBashCommand(%q) = false, want true", cmd)
		}
	}

	notReadOnly := []string{
		// xargs runs whatever it is handed; an unknown option could shift
		// which word that is, so it fails closed.
		"find . -name '*.go' | xargs rm",
		"find . -name '*.go' | xargs -n 1 rm -f",
		"find . -name '*.go' | xargs -i rm",
		"find . -type f | xargs -Z rm",
		"ls | xargs sh -c 'rm -rf /tmp/x'",
		"ls | xargs ./x/grep foo",
		"find . | xargs sort -o out.txt",
		// sed writes in place, runs a script file, or writes/executes from its
		// program.
		"sed -i '' 's/a/b/' f.go",
		"sed -Ei 's/a/b/' f.go",
		"sed --in-place=.bak 's/a/b/' f.go",
		"sed -f script.sed f.go",
		"sed 's/a/b/w out.txt' f.go",
		"sed -n 'w /tmp/x' f.go",
		"sed -n 'W /tmp/x' f.go",
		"sed -n '1e touch /tmp/x' f.go",
		"sed 's/a/b/e' f.go",
		// go: compilation, downloads, and execution are not inspection.
		"go build ./...",
		"go test ./...",
		"go run ./cmd/san",
		"go mod tidy",
		"go env -w GOFLAGS=-mod=mod",
		"go env -u GOFLAGS",
		// A second operand is an output file for these two.
		"uniq in.txt out.txt",
		"xxd a.bin out.hex",
		// gofmt -w rewrites in place; yq -i does too.
		"gofmt -w internal/x.go",
		"yq -i '.a=1' f.yaml",
	}
	for _, cmd := range notReadOnly {
		if IsReadOnlyBashCommand(cmd) {
			t.Errorf("IsReadOnlyBashCommand(%q) = true, want false", cmd)
		}
	}
}

func TestSedProgramIsReadOnly(t *testing.T) {
	cases := map[string]bool{
		"120,175p":           true,
		"1,5d":               true,
		"/func Parse/,/^}/p": true,
		"s/foo/bar/g":        true,
		"s|a/b|c/d|":         true,
		"s/[/]/x/":           true,
		"$p":                 true,
		"/x/!p":              true,
		"y/abc/xyz/":         true,
		"1~3p":               true,
		":top;N;$!btop":      true,
		"s/a/b/;p":           true,
		"1{p;p}":             true,
		"":                   true,
		"w /tmp/x":           false,
		"W /tmp/x":           false,
		"1e touch /tmp/x":    false,
		"s/a/b/w out.txt":    false,
		"s/a/b/e":            false,
		"r /etc/passwd":      false,
		"Q":                  true,  // quits without printing
		"s/a/b":              false, // unterminated
		"1,2p;s/a/b/x":       false, // unknown s flag
	}
	for program, want := range cases {
		if got := sedProgramIsReadOnly(program); got != want {
			t.Errorf("sedProgramIsReadOnly(%q) = %v, want %v", program, got, want)
		}
	}
}

func TestXargsInnerCommandFailsClosed(t *testing.T) {
	cases := map[string]bool{
		"":                  true, // no command: echo
		"-0 -r grep -l x":   true,
		"-n1 wc -l":         true,
		"-I{} cat {}":       true,
		"--max-args=1 wc":   true,
		"--unknown wc":      false,
		"-Z wc":             false,
		"-i rm":             false,
		"rm -rf /tmp/x":     false,
		"-n 1 sed -i s/a//": false,
	}
	for args, want := range cases {
		inner, ok := xargsInnerCommand(splitArgs(args))
		if !ok {
			if want {
				t.Errorf("xargsInnerCommand(%q) rejected, want %v", args, inner)
			}
			continue
		}
		if got := isReadOnlyXargsCommand(splitArgs(args)); got != want {
			t.Errorf("isReadOnlyXargsCommand(%q) = %v, want %v", args, got, want)
		}
	}
}

// splitArgs splits a test case on spaces; the cases avoid quoted operands.
func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// TestReadOnlyGhCommand covers the gh CLI read-only classifier.
//
// gh api is the most common planning command (querying GitHub API), so it gets
// the most thorough coverage. The other subcommands use the sub-action guard.
func TestReadOnlyGhCommand(t *testing.T) {
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

// TestReadOnlyCommonCommands verifies that frequently used inspection commands
// (curl, wget, brew, npm/yarn/pnpm, pip, tar, unzip, docker, kubectl, and
// the flat-list additions) are allowed in Plan Mode when used as readers and
// rejected when used as writers.
func TestReadOnlyCommonCommands(t *testing.T) {
	readOnly := []string{
		// flat-list additions
		"awk '{print $1}' go.mod",
		"awk -F: '{print $2}' /etc/hosts",
		"man git",
		"less go.mod",
		"more README.md",
		"ps aux",
		"ps -ef",
		"pgrep san",
		"lsof -i :8080",
		"env",
		"locale",
		"arch",
		"sw_vers",
		"uptime",
		"cal",
		"bc",
		"expr 1 + 1",
		"type go",
		"zcat archive.gz",
		"ping -c 1 google.com",
		"traceroute google.com",
		"nslookup google.com",
		"dig google.com",
		"host google.com",
		"whois google.com",
		"netstat -an",
		"ss -tlnp",
		"ifconfig",
		"ip addr",
		"open README.md",
		"pbpaste",
		// curl (GET-only reads)
		"curl https://api.github.com/repos/owner/repo",
		"curl -s https://registry.npmjs.org/react/latest",
		"curl -H 'Accept: application/json' https://api.example.com/v1/users",
		"curl -o - https://example.com/data.json",
		"curl --output - https://example.com/data.json",
		"curl -X GET https://api.example.com",
		"curl --request GET https://api.example.com",
		"curl -sL https://example.com | head -20",
		// wget (stdout only)
		"wget -q -O - https://example.com/data.json",
		"wget --quiet --output-document=- https://api.example.com",
		// brew (inspect only)
		"brew list",
		"brew info go",
		"brew search neovim",
		"brew deps go",
		"brew outdated",
		"brew --version",
		"brew doctor",
		"brew config",
		// npm / yarn / pnpm / bun (list/info only)
		"npm list",
		"npm info react",
		"npm view lodash version",
		"npm outdated",
		"npm --version",
		"yarn list",
		"yarn info react",
		"yarn outdated",
		"yarn --version",
		"pnpm list",
		"pnpm info react",
		"pnpm --version",
		"bun --version",
		// pip / pip3 (inspect only)
		"pip list",
		"pip show requests",
		"pip freeze",
		"pip --version",
		"pip3 list",
		"pip3 show numpy",
		// tar (list only)
		"tar -tf archive.tar.gz",
		"tar -tvf archive.tar",
		"tar --list -f archive.tar.gz",
		// unzip (list only)
		"unzip -l archive.zip",
		"unzip -v archive.zip",
		// docker (inspect only)
		"docker ps",
		"docker ps -a",
		"docker images",
		"docker inspect mycontainer",
		"docker logs mycontainer",
		"docker version",
		"docker info",
		"docker container ls",
		"docker image ls",
		"docker network ls",
		"docker system df",
		// kubectl (read only)
		"kubectl get pods",
		"kubectl get pods -n kube-system",
		"kubectl describe pod mypod",
		"kubectl logs mypod",
		"kubectl version",
		"kubectl api-resources",
		"kubectl config view",
		"kubectl config current-context",
		"kubectl auth can-i create pods",
		"k get pods",
	}
	for _, cmd := range readOnly {
		if !IsReadOnlyBashCommand(cmd) {
			t.Errorf("IsReadOnlyBashCommand(%q) = false, want true", cmd)
		}
	}

	notReadOnly := []string{
		// curl writes
		"curl -X POST https://api.example.com/items",
		"curl --request PUT https://api.example.com/items/1",
		"curl --data-raw 'body' https://api.example.com",
		"curl -F 'file=@upload.txt' https://api.example.com",
		"curl -T myfile.txt https://api.example.com/upload",
		"curl --json '{}' https://api.example.com",
		"curl -o output.html https://example.com",
		"curl -O https://example.com/file.zip",
		// wget writes
		"wget https://example.com/file.zip",
		"wget -O output.html https://example.com",
		"wget -P /tmp https://example.com/file.zip",
		"wget -o wget.log https://example.com",
		"wget -r https://example.com",
		// brew writes
		"brew install go",
		"brew uninstall node",
		"brew upgrade",
		"brew update",
		"brew tap homebrew/cask",
		// npm writes
		"npm install react",
		"npm uninstall lodash",
		"npm run build",
		"npm publish",
		"npm config set registry https://registry.npmjs.org",
		// pip writes
		"pip install requests",
		"pip uninstall requests",
		// tar writes
		"tar -czf archive.tar.gz .",
		"tar -xzf archive.tar.gz",
		"tar -rf archive.tar newfile.txt",
		// unzip writes (extract)
		"unzip archive.zip",
		"unzip archive.zip -d /tmp",
		// docker writes
		"docker run ubuntu",
		"docker exec mycontainer bash",
		"docker rm mycontainer",
		"docker build .",
		"docker pull ubuntu",
		"docker network rm mynetwork",
		"docker system prune",
		// kubectl writes
		"kubectl apply -f deployment.yaml",
		"kubectl delete pod mypod",
		"kubectl create deployment myapp --image=nginx",
		"kubectl config set-context myctx",
		"kubectl config use-context myctx",
		"kubectl auth reconcile -f rbac.yaml",
	}
	for _, cmd := range notReadOnly {
		if IsReadOnlyBashCommand(cmd) {
			t.Errorf("IsReadOnlyBashCommand(%q) = true, want false", cmd)
		}
	}
}
