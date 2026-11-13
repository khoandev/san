package setting

// Fish is not bash, but san's classifier reads bash. Two fish redirections mean
// something else there, so a fish command is granted as read-only only after
// they are ruled out; the rest of the surface san admits is spelled the same in
// both shells.
//
//   - ^, ^^ and &^ redirect stderr in fish 3. Bash reads the caret as an
//     ordinary character, so "ls ^/tmp/log" would pass as an inspection while
//     fish writes the log. Fish 4 dropped the operator, but a 3.x install still
//     honours it, so no unquoted caret joins a read-only grant.
//   - >? and >>? are fish's no-clobber redirects. Bash reads them as redirects
//     to a file named "?…", which the shared redirect guard then refuses; they
//     are checked here too so the refusal never rests on how bash happens to
//     lex the question mark.
//
// Parentheses need no check of their own: bash rejects "(...)" adjacent to a
// word, and for a free-standing group it classifies the commands inside, so
// fish's "echo (rm -rf x)" is refused either way. The same holds for the
// constructs the bash classifier already refuses for its own reasons:
// $command, $(command), backticks, and env-var assignments.

// IsReadOnlyFishCommand reports whether a fish invocation provably only reads.
// The bash classifier answers for the grammar both shells share; the fish
// redirections are checked first because it cannot see them.
func IsReadOnlyFishCommand(cmd string) bool {
	if fishWriteOperator(cmd) {
		return false
	}
	return IsReadOnlyBashCommand(cmd)
}

// fishWriteOperator reports whether cmd contains a fish redirection that bash
// would not read as one. Quote and escape state is honoured, because a caret or
// ">?" inside quotes, or escaped, is a literal in both shells.
func fishWriteOperator(cmd string) bool {
	var quote byte
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote == 0 && c == '\\':
			i++ // The next byte is literal in both shells.
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\':
			i++ // Escaped inside double quotes in both shells.
		case quote != 0:
			// Quoted text cannot open a redirection.
		case c == '^':
			return true
		case c == '?' && i > 0 && cmd[i-1] == '>':
			return true
		}
	}
	return false
}
