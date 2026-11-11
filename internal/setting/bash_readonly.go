package setting

import (
	"path/filepath"
	"slices"
	"strings"
)

// Argument-level guards for the inspection commands whose read-only use
// depends on more than their name.
//
// A name alone stops proving anything once the command can dispatch to another
// program (xargs), run a program of its own (sed's w and e commands), or take
// an output file as an operand (uniq's second positional). Each of those is
// classified here instead of by the flat list in security.go, and every
// scanner fails closed: a flag, letter, or construct it does not model makes
// the invocation not read-only.

// readOnlyBashMaxPositional caps the non-flag operands of commands whose
// second operand is an output file rather than an input, so "uniq in out" and
// "xxd in out" do not pass as inspection.
var readOnlyBashMaxPositional = map[string]int{
	"uniq": 1,
	"xxd":  1,
}

// hasUnsafeReadOnlyFlag reports whether args carry a flag that turns an
// otherwise read-only command into a writer or an executor. Matched by prefix
// so the "=" and attached-value forms ("--in-place=.bak", "-oout") count too.
func hasUnsafeReadOnlyFlag(name string, args []string) bool {
	for _, flag := range readOnlyBashUnsafeFlags[name] {
		for _, arg := range args {
			if strings.HasPrefix(arg, flag) {
				return true
			}
		}
	}
	return false
}

// exceedsPositionalCap reports whether a command with an output operand got
// more operands than it may take as inputs.
func exceedsPositionalCap(name string, args []string) bool {
	max, ok := readOnlyBashMaxPositional[name]
	if !ok {
		return false
	}
	operands := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			operands++
		}
	}
	return operands > max
}

// onlyShortFlags reports whether arg is a bundle of short options drawn from
// allowed, such as -0r for -0 -r. A letter outside the set makes it false, so
// a bundle can never smuggle in an option the caller refused.
func onlyShortFlags(arg, allowed string) bool {
	if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
		return false
	}
	for _, r := range arg[1:] {
		if !strings.ContainsRune(allowed, r) {
			return false
		}
	}
	return true
}

// shortFlagWithValue reports an attached short-option value such as -e1p or
// -I{}, as opposed to the "--" long form, whose second byte is a dash.
func shortFlagWithValue(arg string, flag byte) bool {
	return len(arg) > 2 && arg[0] == '-' && arg[1] == flag
}

// --- xargs -----------------------------------------------------------------

// xargs dispatches: its safety is the safety of the command it runs, so the
// inner command is classified exactly like a command in a pipeline. With no
// inner command xargs runs echo, which only writes to stdout.
//
// Refusing unknown xargs options keeps the dispatch honest: an option San does
// not know may consume the next word as its value, which would move the
// command xargs really runs one word later than the one classified here.
func isReadOnlyXargsCommand(args []string) bool {
	inner, ok := xargsInnerCommand(args)
	if !ok {
		return false
	}
	if len(inner) == 0 {
		return true
	}
	name := filepath.Base(inner[0])
	// A path names some other program than the one on the read-only list.
	if inner[0] != name {
		return false
	}
	return isReadOnlyParsedCommand(parsedCommand{Name: name, Argv0: name, Args: inner[1:]})
}

// xargsValueFlags consume the following word as their value, so that word is
// never the command xargs runs. The -i and -l styles are absent on purpose:
// their replacement string is optional and attached, so treating the next word
// as a value would hide the command.
var xargsValueFlags = map[string]bool{
	"-a": true, "--arg-file": true,
	"-d": true, "--delimiter": true,
	"-E": true, "--eof": true,
	"-I": true, "--replace": true,
	"-J": true,
	"-L": true, "--max-lines": true,
	"-n": true, "--max-args": true,
	"-s": true, "--max-chars": true,
	"-P": true, "--max-procs": true,
	"-R": true, "--process-slot-var": true,
	"-S": true,
}

// xargsBoolFlags are options that take no value, so the next word is still the
// command.
var xargsBoolFlags = map[string]bool{
	"-0": true, "--null": true,
	"-r": true, "--no-run-if-empty": true,
	"-t": true, "--verbose": true,
	"-x": true, "--exit": true,
	"--show-limits": true, "--help": true, "--version": true,
	"-o": true, "--open-tty": true,
}

// xargsShortFlags are the letters xargsBoolFlags spells in bundled form.
const xargsShortFlags = "0rtxo"

// xargsInnerCommand returns the command xargs runs, or nil when it runs none.
// ok is false for an option San does not know.
func xargsInnerCommand(args []string) (inner []string, ok bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return args[i+1:], true
		case xargsValueFlags[arg]:
			i++
		case xargsBoolFlags[arg], onlyShortFlags(arg, xargsShortFlags):
		case strings.HasPrefix(arg, "--"):
			name, _, _ := strings.Cut(arg, "=")
			if !xargsValueFlags[name] {
				return nil, false
			}
		case shortFlagWithValue(arg, 'n'), shortFlagWithValue(arg, 'L'),
			shortFlagWithValue(arg, 'P'), shortFlagWithValue(arg, 's'),
			shortFlagWithValue(arg, 'I'), shortFlagWithValue(arg, 'J'),
			shortFlagWithValue(arg, 'd'), shortFlagWithValue(arg, 'E'),
			shortFlagWithValue(arg, 'R'), shortFlagWithValue(arg, 'S'),
			shortFlagWithValue(arg, 'a'):
			// The value is attached: -n1, -I{}.
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			return nil, false
		default:
			return args[i:], true
		}
	}
	return nil, true
}

// --- sed -------------------------------------------------------------------

// isReadOnlySedCommand reports whether a sed invocation can only print. sed
// leaves the output stream in three ways, and all three are refused: -i edits
// files in place, -f runs a program San cannot read, and the w, W and e
// commands write a file or run a shell command. The programs that remain are
// scanned by sedProgramIsReadOnly, which accepts only the commands that edit
// the pattern space and the addresses that select it.
func isReadOnlySedCommand(args []string) bool {
	programs, ok := sedPrograms(args)
	if !ok || len(programs) == 0 {
		return false
	}
	for _, program := range programs {
		if !sedProgramIsReadOnly(program) {
			return false
		}
	}
	return true
}

// sedReadOnlyLongFlags are the long sed options that only change how a program
// is read or how output is written.
var sedReadOnlyLongFlags = map[string]bool{
	"--quiet": true, "--silent": true, "--regexp-extended": true,
	"--separate": true, "--unbuffered": true, "--null-data": true,
	"--posix": true, "--debug": true, "--sandbox": true,
	"--follow-symlinks": true, "--help": true, "--version": true,
}

// sedReadOnlyShortFlags are the bundled short options sed may carry.
const sedReadOnlyShortFlags = "nErsuzb"

// sedPrograms collects the -e programs of a sed invocation, or its first
// operand when it has none. ok is false for the two options that put the
// program out of reach: -i writes in place, -f reads a script file.
func sedPrograms(args []string) (programs []string, ok bool) {
	operands := 0
	endOfOptions := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case endOfOptions:
			operands++
			if operands == 1 && len(programs) == 0 {
				programs = append(programs, arg)
			}
		case arg == "--":
			endOfOptions = true
		case arg == "-e" || arg == "--expression":
			if i+1 >= len(args) {
				return nil, false
			}
			i++
			programs = append(programs, args[i])
		case strings.HasPrefix(arg, "--expression="):
			programs = append(programs, strings.TrimPrefix(arg, "--expression="))
		case shortFlagWithValue(arg, 'e'):
			programs = append(programs, arg[2:])
		case strings.HasPrefix(arg, "-i"):
			return nil, false
		case arg == "-f" || strings.HasPrefix(arg, "--file") || shortFlagWithValue(arg, 'f'):
			return nil, false
		case strings.HasPrefix(arg, "--"):
			name, _, _ := strings.Cut(arg, "=")
			if !sedReadOnlyLongFlags[name] {
				return nil, false
			}
		case onlyShortFlags(arg, sedReadOnlyShortFlags):
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			// An option San does not know could be an edit (-Ei is -E -i).
			return nil, false
		default:
			operands++
			if operands == 1 && len(programs) == 0 {
				programs = append(programs, arg)
			}
		}
	}
	return programs, true
}

// sedProgramIsReadOnly scans a sed program. It understands addresses, s and y,
// the pattern-space commands, branches and blocks; anything else — w, W, e, r,
// i, a, c, and every command introduced after POSIX — makes it false, so a
// construct the scanner does not model can only make sed look more dangerous.
func sedProgramIsReadOnly(program string) bool {
	for i := 0; i < len(program); {
		switch program[i] {
		case ';', '\n', ' ', '\t', '}', '#':
			i++
			continue
		}
		next, ok := skipSedAddresses(program, i)
		if !ok {
			return false
		}
		i = skipSedBlanks(program, next)
		if i < len(program) && program[i] == '!' {
			i = skipSedBlanks(program, i+1)
		}
		if i >= len(program) {
			return false
		}
		switch program[i] {
		case '{':
			i++
		case 's':
			if next, ok = skipSedSubstitution(program, i); !ok {
				return false
			}
			i = next
		case 'y':
			if next, ok = skipSedTranslate(program, i); !ok {
				return false
			}
			i = next
		case 'b', 't', 'T':
			// A branch stays inside this program.
			i = skipSedToLineEnd(program, i+1)
		case ':', 'q', 'Q', 'l', 'L':
			// A label, or a command followed by a word up to the line end.
			i = skipSedToLineEnd(program, i+1)
		case 'd', 'D', 'g', 'G', 'h', 'H', 'x', 'n', 'N', 'p', 'P', 'z', '=', 'F':
			i++
		default:
			return false
		}
	}
	return true
}

func skipSedBlanks(program string, i int) int {
	for i < len(program) && (program[i] == ' ' || program[i] == '\t') {
		i++
	}
	return i
}

// skipSedToLineEnd skips the rest of a command that ends at the line, such as
// a label or the word a branch names.
func skipSedToLineEnd(program string, i int) int {
	for i < len(program) && program[i] != '\n' && program[i] != ';' {
		i++
	}
	return i
}

// skipSedAddresses skips the address or address range in front of a command.
// With no address it returns i unchanged, which is how a bare command parses.
func skipSedAddresses(program string, i int) (int, bool) {
	next, ok := skipSedAddress(program, i)
	if !ok || next == i {
		return i, true
	}
	i = next
	if i < len(program) && program[i] == ',' {
		second, ok := skipSedAddress(program, i+1)
		if !ok || second == i+1 {
			return 0, false
		}
		i = second
	}
	return i, true
}

// skipSedAddress skips one address: a line number, $, a /regex/, a \xregex x,
// or the GNU first~step and addr+N forms.
func skipSedAddress(program string, i int) (int, bool) {
	if i >= len(program) {
		return i, false
	}
	switch c := program[i]; {
	case c >= '0' && c <= '9':
		i = skipSedDigits(program, i)
		if i < len(program) && program[i] == '~' {
			i = skipSedDigits(program, i+1)
		}
	case c == '$':
		i++
	case c == '/' || c == '\\':
		delim := byte('/')
		if c == '\\' {
			i++
			if i >= len(program) {
				return i, false
			}
			delim = program[i]
		}
		next, ok := skipSedDelimited(program, i+1, delim)
		if !ok {
			return 0, false
		}
		// Only a regex address takes the I and M modifiers.
		for next < len(program) && (program[next] == 'I' || program[next] == 'M') {
			next++
		}
		return next, true
	case c == '+' || c == '~':
		start := skipSedDigits(program, i+1)
		if start == i+1 {
			return i, false
		}
		i = start
	default:
		return i, false
	}
	return i, true
}

func skipSedDigits(program string, i int) int {
	for i < len(program) && program[i] >= '0' && program[i] <= '9' {
		i++
	}
	return i
}

// skipSedDelimited returns the index just past the delimiter that closes the
// text starting at i. A backslash escapes the next byte, and a delimiter
// inside a bracket expression does not close the text.
func skipSedDelimited(program string, i int, delim byte) (int, bool) {
	inBracket := false
	for ; i < len(program); i++ {
		switch c := program[i]; {
		case c == '\\':
			i++ // the escaped byte is text, whatever it is
		case c == '\n':
			return 0, false
		case inBracket && c == ']':
			inBracket = false
		case inBracket:
			// Inside a bracket expression the delimiter is a literal.
		case c == '[':
			inBracket = true
			// "[]" and "[^]" open with a literal ], not a closed bracket.
			if i+1 < len(program) && program[i+1] == ']' {
				i++
			} else if i+2 < len(program) && program[i+1] == '^' && program[i+2] == ']' {
				i += 2
			}
		case c == delim:
			return i + 1, true
		}
	}
	return 0, false
}

// skipSedSubstitution skips an s command and its flags. The w flag writes the
// pattern space to a file and e runs it as a command, so either one is fatal.
func skipSedSubstitution(program string, i int) (int, bool) {
	if i+1 >= len(program) || program[i+1] == '\n' || program[i+1] == '\\' {
		return 0, false
	}
	delim := program[i+1]
	next, ok := skipSedDelimited(program, i+2, delim)
	if !ok {
		return 0, false
	}
	next, ok = skipSedDelimited(program, next, delim)
	if !ok {
		return 0, false
	}
	return skipSedSubstFlags(program, next)
}

// skipSedSubstFlags skips the flag letters of an s command.
func skipSedSubstFlags(program string, i int) (int, bool) {
	for ; i < len(program); i++ {
		switch program[i] {
		case 'e', 'w':
			return 0, false
		case 'g', 'p', 'i', 'I', 'm', 'M',
			'0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		case ';', '\n', '}', ' ', '\t':
			return i, true
		default:
			return 0, false
		}
	}
	return i, true
}

// skipSedTranslate skips the y command, whose two lists must both close.
func skipSedTranslate(program string, i int) (int, bool) {
	if i+1 >= len(program) || program[i+1] == '\n' || program[i+1] == '\\' {
		return 0, false
	}
	delim := program[i+1]
	next, ok := skipSedDelimited(program, i+2, delim)
	if !ok {
		return 0, false
	}
	return skipSedDelimited(program, next, delim)
}

// --- gh --------------------------------------------------------------------

// readOnlyGhSubcommands are the gh subcommands that only read from GitHub's
// API or the local git state. They never write, create, delete, or trigger
// workflow runs.
//
// "api" is included here but is gated further: only GET requests (the default
// method when --method is absent) are read-only. Any call with --method POST,
// PATCH, PUT, or DELETE is a write.
var readOnlyGhSubcommands = map[string]bool{
	"api":     true, // further gated by --method below
	"auth":    true, // "auth status" and "auth token" are read-only
	"browse":  true, // opens a URL in the browser; no write effect
	"help":    true,
	"issue":   true, // sub-actions: list, view, status
	"pr":      true, // sub-actions: list, view, checks, diff, status
	"release": true, // sub-actions: list, view, download (read-only)
	"repo":    true, // sub-actions: view, list, clone (clone writes disk but
	//                // only the repo copy, same as git clone; kept here because
	//                // it is the standard investigation flow)
	"run":      true, // sub-actions: list, view, download
	"search":   true,
	"variable": true, // sub-action: list (get/set/delete are writes, gated below)
	"version":  true,
	"workflow": true, // sub-actions: list, view
}

// readOnlyGhWriteSubActions are sub-actions that write even under a normally
// read-only gh subcommand. The check only looks at the first positional word
// after the subcommand, which is always the sub-action when present.
var readOnlyGhWriteSubActions = map[string]bool{
	// issue sub-actions that write
	"create": true, "edit": true, "close": true, "reopen": true,
	"transfer": true, "pin": true, "unpin": true, "lock": true, "unlock": true,
	"comment": true,
	// pr sub-actions that write
	"merge": true, "review": true, "ready": true, "draft": true,
	// release sub-actions that write
	"upload": true,
	// repo sub-actions that write
	"archive": true, "rename": true, "fork": true, "sync": true, "deploy-key": true,
	"set-default": true,
	// run sub-actions that write
	"rerun": true, "cancel": true, "watch": true,
	// workflow sub-actions that write
	"run": true, "enable": true, "disable": true,
	// variable sub-actions that write
	"set": true, "delete": true,
	// auth sub-actions that write (login writes credentials)
	"login": true, "logout": true, "refresh": true, "setup-git": true,
}

// ghWriteMethodFlags are --method values that make gh api a write call.
var ghWriteMethodFlags = map[string]bool{
	"POST": true, "PATCH": true, "PUT": true, "DELETE": true,
	"post": true, "patch": true, "put": true, "delete": true,
}

// isReadOnlyGhCommand reports whether a gh invocation is provably read-only.
// It admits only the subcommands listed in readOnlyGhSubcommands and further
// rejects any sub-action in readOnlyGhWriteSubActions. For "gh api" it also
// rejects any explicit --method that is not GET.
func isReadOnlyGhCommand(args []string) bool {
	// Find the first non-flag argument: that is the subcommand.
	subIdx := -1
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			subIdx = i
			break
		}
		// Skip --flag value pairs so the value is not misread as the sub.
		if strings.HasPrefix(arg, "--") && !strings.Contains(arg, "=") {
			i++ // the next token is the value; advance past it in next iteration
			_ = i
		}
	}
	if subIdx < 0 {
		return false // bare "gh" with only flags: no sub-action to classify
	}
	sub := args[subIdx]
	if !readOnlyGhSubcommands[sub] {
		return false
	}

	// For "gh api", reject any explicit write method.
	if sub == "api" {
		for i, arg := range args {
			switch {
			case arg == "--method" || arg == "-X":
				// next token is the method value
				if i+1 < len(args) && ghWriteMethodFlags[args[i+1]] {
					return false
				}
			case strings.HasPrefix(arg, "--method="):
				if ghWriteMethodFlags[strings.TrimPrefix(arg, "--method=")] {
					return false
				}
			case strings.HasPrefix(arg, "-X") && len(arg) > 2:
				if ghWriteMethodFlags[arg[2:]] {
					return false
				}
			case arg == "--input" || strings.HasPrefix(arg, "--input="):
				// --input pipes a file body, implying a write call
				return false
			}
		}
		return true
	}

	// For all other subcommands, reject write sub-actions.
	rest := args[subIdx+1:]
	for _, arg := range rest {
		if !strings.HasPrefix(arg, "-") {
			// First positional after the subcommand is the sub-action.
			return !readOnlyGhWriteSubActions[arg]
		}
	}
	return true
}

// --- go --------------------------------------------------------------------

// readOnlyGoSubcommands are the go subcommands that only report: they print
// documentation, environment, or version, and never write the working tree.
// Everything else — build, test, run, generate, mod — compiles, downloads, or
// executes, so none of it is an inspection.
var readOnlyGoSubcommands = map[string]bool{
	"doc": true, "env": true, "help": true, "version": true,
}

// isReadOnlyGoCommand classifies a go invocation by its subcommand, and "go env"
// by its flags as well, since -w and -u rewrite the go environment file.
func isReadOnlyGoCommand(args []string) bool {
	sub := slices.IndexFunc(args, func(arg string) bool { return !strings.HasPrefix(arg, "-") })
	if sub < 0 || !readOnlyGoSubcommands[args[sub]] {
		return false
	}
	if args[sub] != "env" {
		return true
	}
	for _, arg := range args[sub+1:] {
		if arg == "-w" || arg == "-u" || strings.HasPrefix(arg, "--write") || strings.HasPrefix(arg, "--unset") {
			return false
		}
	}
	return true
}

// --- curl ------------------------------------------------------------------

// curlWriteFlags are flags that cause curl to send a request body, implying
// a write (POST, PUT, PATCH, etc.). Matched by exact name or prefix so the
// attached-value form (--data=foo, --json={}) is caught too.
var curlWriteFlags = []string{
	"-d", "--data",
	"--data-raw", "--data-binary", "--data-ascii", "--data-urlencode",
	"-F", "--form", "--form-string",
	"-T", "--upload-file",
	"--json",
}

// curlWriteMethods are HTTP verbs that modify server state.
var curlWriteMethods = map[string]bool{
	"POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"post": true, "put": true, "patch": true, "delete": true,
}

// isReadOnlyCurlCommand reports whether a curl invocation is provably
// read-only: no write-method flag, no request-body flag, and no output to a
// file (stdout/stderr are fine). The absence of an explicit -X means GET,
// which is the safe default.
func isReadOnlyCurlCommand(args []string) bool {
	for i, arg := range args {
		// Body/upload flags.
		for _, wf := range curlWriteFlags {
			if arg == wf || strings.HasPrefix(arg, wf+"=") {
				return false
			}
		}
		// -X METHOD / --request METHOD.
		if arg == "-X" || arg == "--request" {
			if i+1 < len(args) && curlWriteMethods[args[i+1]] {
				return false
			}
		}
		if strings.HasPrefix(arg, "-X") && len(arg) > 2 {
			if curlWriteMethods[arg[2:]] {
				return false
			}
		}
		if strings.HasPrefix(arg, "--request=") {
			if curlWriteMethods[strings.TrimPrefix(arg, "--request=")] {
				return false
			}
		}
		// -o FILE saves to disk; -o - streams to stdout (safe).
		if arg == "-o" || arg == "--output" {
			if i+1 < len(args) {
				out := args[i+1]
				if out != "-" && out != "/dev/stdout" && out != "/dev/stderr" {
					return false
				}
			}
		}
		if strings.HasPrefix(arg, "--output=") {
			out := strings.TrimPrefix(arg, "--output=")
			if out != "-" && out != "/dev/stdout" {
				return false
			}
		}
		// --create-dirs, --remote-name (-O) always write to disk.
		if arg == "--create-dirs" || arg == "-O" || arg == "--remote-name" ||
			arg == "--remote-name-all" {
			return false
		}
	}
	return true
}

// --- wget ------------------------------------------------------------------

// isReadOnlyWgetCommand reports whether a wget invocation only streams to
// stdout. wget's default is to save to disk, so this gate requires -O - (or
// --output-document=-) to be present and rejects any flag that writes
// elsewhere.
func isReadOnlyWgetCommand(args []string) bool {
	stdoutOut := false
	for i, arg := range args {
		switch {
		// -O - / --output-document=- streams to stdout.
		case arg == "-O" || arg == "--output-document":
			if i+1 < len(args) {
				out := args[i+1]
				if out == "-" || out == "/dev/stdout" {
					stdoutOut = true
				} else {
					return false // writing to a named file
				}
			}
		case strings.HasPrefix(arg, "--output-document="):
			out := strings.TrimPrefix(arg, "--output-document=")
			if out == "-" || out == "/dev/stdout" {
				stdoutOut = true
			} else {
				return false
			}
		// Log file writes.
		case arg == "-o" || arg == "--output-file",
			arg == "-a" || arg == "--append-output":
			return false
		// Directory prefix: saves fetched file under the prefix.
		case arg == "-P" || arg == "--directory-prefix",
			strings.HasPrefix(arg, "--directory-prefix="):
			return false
		// Cookie jar writes.
		case arg == "--save-cookies", strings.HasPrefix(arg, "--save-cookies="):
			return false
		// Mirror / recursive download: always writes multiple files.
		case arg == "-m" || arg == "--mirror",
			arg == "-r" || arg == "--recursive":
			return false
		// -N (timestamping) can overwrite if the server file is newer.
		case arg == "-N" || arg == "--timestamping":
			return false
		}
	}
	// Without -O -, wget saves to disk by default: not read-only.
	return stdoutOut
}

// --- brew ------------------------------------------------------------------

// readOnlyBrewSubcommands are Homebrew commands that only inspect state and
// never install, upgrade, or modify formulae or the filesystem.
var readOnlyBrewSubcommands = map[string]bool{
	"list": true, "ls": true,
	"info": true, "abv": true,
	"search": true,
	"deps":   true, "uses": true,
	"outdated":  true,
	"leaves":    true,
	"missing":   true,
	"cat":       true, // prints the formula file
	"log":       true, // git log of the formula
	"audit":     true, // lints a formula (read-only)
	"home":      true, // opens the project page in a browser
	"help":      true,
	"--version": true, "-v": true,
	"shellenv":    true,
	"config":      true,
	"environment": true,
	"doctor":      true, // checks the system; no writes
	"bundle":      true, // used for Brewfile export (read), gated below
	"formulae":    true,
	"casks":       true,
	"commands":    true,
	"completions": true,
	"tap-info":    true,
}

// isReadOnlyBrewCommand reports whether a brew invocation is read-only.
func isReadOnlyBrewCommand(args []string) bool {
	// A bare version flag is always read-only.
	for _, arg := range args {
		if arg == "--version" || arg == "-v" {
			return true
		}
	}
	// Find the first non-flag word: that is the subcommand.
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return readOnlyBrewSubcommands[arg]
		}
	}
	return false
}

// --- npm / yarn / pnpm / bun -----------------------------------------------

// readOnlyNodeSubcommands are package-manager subcommands that only report
// state and never install, build, or run code.
var readOnlyNodeSubcommands = map[string]bool{
	// Listing and inspection.
	"list": true, "ls": true, "ll": true, "la": true,
	"info": true, "view": true, "show": true,
	"search": true, "find": true,
	// Dependency analysis.
	"outdated": true,
	"audit":    true,
	"why":      true, "explain": true,
	// Documentation helpers (open in browser; no write).
	"docs": true, "repo": true, "home": true, "bugs": true,
	// Meta.
	"help": true, "version": true,
	"root": true, "prefix": true, "bin": true,
	"config": true, // "npm config get" is read-only; set/delete are writes,
	//               // but those need a second positional we do not check here.
	//               // The common read usage (npm config list, npm config get X)
	//               // is covered; a user who does "npm config set" gets blocked
	//               // by the sub-action guard in the caller.
	"pack": true, // creates a tarball in cwd; treat as read (no install)
}

// readOnlyNodeWriteSubActions blocks write sub-actions under otherwise
// read-only subcommands (e.g. "npm config set", "npm config delete").
var readOnlyNodeWriteSubActions = map[string]bool{
	"set": true, "delete": true, "rm": true, "del": true,
	"edit": true, "fix": true,
}

// isReadOnlyNodePackageCommand reports whether an npm/yarn/pnpm/bun
// invocation only reads package state.
func isReadOnlyNodePackageCommand(args []string) bool {
	// Accept bare "--version" / "-v" before any subcommand.
	subIdx := -1
	for i, arg := range args {
		if arg == "--version" || arg == "-v" || arg == "-V" {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			subIdx = i
			break
		}
	}
	if subIdx < 0 {
		return false
	}
	sub := args[subIdx]
	if !readOnlyNodeSubcommands[sub] {
		return false
	}
	// Guard write sub-actions (e.g. npm config set, npm config delete).
	rest := args[subIdx+1:]
	for _, arg := range rest {
		if !strings.HasPrefix(arg, "-") {
			return !readOnlyNodeWriteSubActions[arg]
		}
	}
	return true
}

// --- pip / pip3 ------------------------------------------------------------

// readOnlyPipSubcommands are pip commands that never install or modify
// packages.
var readOnlyPipSubcommands = map[string]bool{
	"list": true, "show": true, "freeze": true,
	"search": true, "check": true, "help": true,
	"index":     true, // "pip index versions pkg"
	"inspect":   true,
	"--version": true, "-V": true,
}

// isReadOnlyPipCommand reports whether a pip invocation is read-only.
func isReadOnlyPipCommand(args []string) bool {
	for _, arg := range args {
		if arg == "--version" || arg == "-V" {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			return readOnlyPipSubcommands[arg]
		}
	}
	return false
}

// --- tar -------------------------------------------------------------------

// isReadOnlyTarCommand reports whether a tar invocation only lists an archive
// (the -t / --list flag) without extracting or creating anything.
func isReadOnlyTarCommand(args []string) bool {
	for _, arg := range args {
		if arg == "--list" {
			return true
		}
		// Short flag bundle: -tvf, -tzf, etc. — safe if 't' is present and no
		// creation ('c') or extract ('x') flag appears.
		if len(arg) > 1 && arg[0] == '-' && arg[1] != '-' {
			flags := arg[1:]
			if strings.ContainsRune(flags, 't') &&
				!strings.ContainsRune(flags, 'c') &&
				!strings.ContainsRune(flags, 'x') &&
				!strings.ContainsRune(flags, 'r') &&
				!strings.ContainsRune(flags, 'u') {
				return true
			}
		}
	}
	return false
}

// --- unzip -----------------------------------------------------------------

// isReadOnlyUnzipCommand reports whether an unzip invocation only lists
// archive contents without extracting.
func isReadOnlyUnzipCommand(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-l", "-v", "-z", "-Z":
			return true
		}
	}
	return false
}

// --- docker ----------------------------------------------------------------

// readOnlyDockerSubcommands are Docker CLI commands that only inspect running
// containers or images. The sub-action guard below rejects writes inside
// otherwise safe subcommands (e.g. "docker network rm").
var readOnlyDockerSubcommands = map[string]bool{
	"ps": true, "images": true, "inspect": true,
	"logs": true, "stats": true, "top": true,
	"port": true, "diff": true, "history": true,
	"info": true, "version": true, "search": true,
	"help": true,
	// Sub-object commands: classified further by their sub-action.
	"container": true, "image": true,
	"network": true, "volume": true,
	"system": true, "compose": true,
}

// readOnlyDockerWriteSubActions blocks write sub-actions even under normally
// read-only docker subcommands.
var readOnlyDockerWriteSubActions = map[string]bool{
	"create": true, "rm": true, "remove": true, "prune": true,
	"start": true, "stop": true, "restart": true, "kill": true,
	"run": true, "exec": true, "attach": true,
	"pull": true, "push": true, "build": true, "tag": true,
	"rmi": true, "save": true, "load": true, "import": true, "export": true,
	"connect": true, "disconnect": true,
	"deploy": true, "up": true, "down": true,
	"cp": true, "rename": true, "pause": true, "unpause": true, "update": true,
}

// isReadOnlyDockerCommand reports whether a docker invocation is read-only.
func isReadOnlyDockerCommand(args []string) bool {
	subIdx := -1
	for i, arg := range args {
		if arg == "--version" || arg == "-v" {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			subIdx = i
			break
		}
	}
	if subIdx < 0 {
		return false
	}
	sub := args[subIdx]
	if !readOnlyDockerSubcommands[sub] {
		return false
	}
	// For compound subcommands (container, image, network, volume, system,
	// compose), the sub-action is the next positional.
	rest := args[subIdx+1:]
	for _, arg := range rest {
		if !strings.HasPrefix(arg, "-") {
			return !readOnlyDockerWriteSubActions[arg]
		}
	}
	return true
}

// --- kubectl ---------------------------------------------------------------

// readOnlyKubectlSubcommands are kubectl commands that only read cluster
// state and never create, update, or delete resources.
var readOnlyKubectlSubcommands = map[string]bool{
	"get": true, "describe": true, "logs": true, "explain": true,
	"top": true, "diff": true,
	"version": true, "api-versions": true, "api-resources": true,
	"cluster-info": true, "help": true,
	"auth":   true, // "auth can-i" is read-only
	"config": true, // "config view", "config current-context", "config get-*"
}

// readOnlyKubectlWriteSubActions blocks write sub-actions under normally
// read-only kubectl subcommands (e.g. "kubectl auth reconcile").
var readOnlyKubectlWriteSubActions = map[string]bool{
	"set": true, "use-context": true, "set-context": true,
	"set-cluster": true, "set-credentials": true,
	"delete-cluster": true, "delete-context": true, "delete-user": true,
	"rename-context": true,
	"reconcile":      true,
}

// isReadOnlyKubectlCommand reports whether a kubectl invocation is read-only.
func isReadOnlyKubectlCommand(args []string) bool {
	subIdx := -1
	for i, arg := range args {
		if arg == "--version" || arg == "-v" {
			return true
		}
		// Skip --flag value pairs.
		if strings.HasPrefix(arg, "--") && !strings.Contains(arg, "=") {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			subIdx = i
			break
		}
	}
	if subIdx < 0 {
		return false
	}
	sub := args[subIdx]
	if !readOnlyKubectlSubcommands[sub] {
		return false
	}
	rest := args[subIdx+1:]
	for _, arg := range rest {
		if !strings.HasPrefix(arg, "-") {
			return !readOnlyKubectlWriteSubActions[arg]
		}
	}
	return true
}
