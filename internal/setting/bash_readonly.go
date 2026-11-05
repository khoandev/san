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
