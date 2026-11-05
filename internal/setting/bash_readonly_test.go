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
