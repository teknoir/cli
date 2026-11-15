package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// docsCmdLine is one command line the council docs show: the page it is in, the 1-based number of
// the line that starts it, and its text with the continuation lines joined and the comment still on
// it (docsCmdCheck cuts it).
type docsCmdLine struct {
	page string // the file path as given, for the messages
	line int    // 1-based line number of the line that starts with tnctl
	text string // the command text after joining continuations
}

// docsCmdShell tracks the shell context of a left-to-right scan of a command text: whether it is
// inside a quoted span ('…', "…" or `…`), inside a $( … ) command substitution, and how deep in
// brackets [ … ] it is. The comment cut, the segment split, the redirect cut and the tokenizer all
// step through their text with it, so they agree on what "outside" means: outside a quote, outside
// a substitution and, where brackets count, at bracket depth 0.
type docsCmdShell struct {
	quote byte // the quote character the scan is inside, 0 outside
	sub   int  // the depth of the $( … ) substitution the scan is in, 0 outside
	brack int  // the bracket depth
	prev  byte // the byte before the one step takes next, 0 at the start
}

// outside reports whether the scan is in plain text: not inside a quoted span, not inside a command
// substitution and, when brackets is true, not inside a bracket.
func (s docsCmdShell) outside(brackets bool) bool {
	if s.quote != 0 || s.sub > 0 {
		return false
	}
	return !brackets || s.brack == 0
}

// step applies the next byte of the text to the state. Quotes have no escapes, so a quote closes at
// the same character that opened it. Outside quotes, $( opens a substitution and, inside one, every
// ( raises the depth and every ) lowers it (nested substitutions included); quotes are not tracked
// inside a substitution. Outside both, [ raises the bracket depth and ] lowers it, never below 0.
func (s *docsCmdShell) step(b byte) {
	switch {
	case s.quote != 0:
		if b == s.quote {
			s.quote = 0
		}
	case s.sub > 0:
		switch b {
		case '(':
			s.sub++
		case ')':
			s.sub--
		}
	default:
		switch b {
		case '\'', '"', '`':
			s.quote = b
		case '(':
			if s.prev == '$' {
				s.sub = 1
			}
		case '[':
			s.brack++
		case ']':
			if s.brack > 0 {
				s.brack--
			}
		}
	}
	s.prev = b
}

// docsCmdExtract returns the tnctl commands of the ```bash and ```sh blocks of one Markdown page.
// A page saved with CRLF line endings gives the same commands. The fences are lenient, as MkDocs
// renders them: an indented fence (a list item's) and a fence with an info string (```bash
// title="…", ```{.bash}) open a block too, and a fence closes it only when it is of the same
// character, at least as long, and has nothing after it, so a ```bash example shown inside a
// ````markdown block is skipped whole. A line starts a command when it holds at least one tnctl
// command (docsCmdSegments); a following line that starts with a blank and then [ or -, or that
// follows a line ending in a backslash, continues the command above it.
func docsCmdExtract(page, markdown string) []docsCmdLine {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	var out []docsCmdLine
	var cur docsCmdLine
	has := false  // cur holds a command that is not in out yet
	cont := false // the last line of cur ended with a backslash
	// flush ends the command in cur: it goes to out when it holds one.
	flush := func() {
		if has {
			out = append(out, cur)
			has = false
		}
	}
	inBlock := false    // the scan is inside a fenced block
	openRun := ""       // the fence that opened it, such as ``` or ~~~~
	shellBlock := false // that block is a shell block, not one to skip
	for i, raw := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimLeft(raw, " \t")
		if run, info := docsCmdFence(trimmed); run != "" {
			switch {
			case !inBlock:
				inBlock, openRun = true, run
				lang := docsCmdFenceLang(info)
				shellBlock = lang == "bash" || lang == "sh"
				flush()
				cur, has, cont = docsCmdLine{}, false, false
				continue
			case run[0] == openRun[0] && len(run) >= len(openRun) && strings.TrimSpace(info) == "":
				inBlock, openRun, shellBlock = false, "", false
				flush()
				cur, has, cont = docsCmdLine{}, false, false
				continue
			}
			// Any other fence line inside a block is ordinary content: a ```bash example shown
			// inside a ````markdown block, or a line that is no command.
		}
		if !inBlock || !shellBlock {
			flush()
			has, cont = false, false
			continue
		}
		switch {
		case has && cont:
			cur.text += " " + strings.TrimSpace(raw)
			cur.text, cont = docsCmdDropBackslash(cur.text)
		case has && len(trimmed) > 0 && (raw[0] == ' ' || raw[0] == '\t') && (trimmed[0] == '[' || trimmed[0] == '-'):
			cur.text += " " + strings.TrimSpace(raw)
			cur.text, cont = docsCmdDropBackslash(cur.text)
		default:
			text, backslash := docsCmdDropBackslash(trimmed)
			if len(docsCmdSegments(text)) > 0 {
				flush()
				cur, has, cont = docsCmdLine{page: page, line: i + 1, text: text}, true, backslash
			} else {
				flush()
				has, cont = false, false
			}
		}
	}
	flush()
	return out
}

// docsCmdFence reports whether the line, with its leading blanks already trimmed, is a fence: three
// or more backticks or three or more tildes. run is that whole run of one character and info what
// follows it; both are empty when the line is no fence.
func docsCmdFence(trimmed string) (run, info string) {
	if trimmed == "" || (trimmed[0] != '`' && trimmed[0] != '~') {
		return "", ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == trimmed[0] {
		n++
	}
	if n < 3 {
		return "", ""
	}
	return trimmed[:n], trimmed[n:]
}

// docsCmdFenceLang is the language a fence's info string names: its first field once {, } and . are
// blanks, so ```bash, ```bash title="…", ```{.bash} and ```{ .bash } all give bash.
func docsCmdFenceLang(info string) string {
	fields := strings.Fields(strings.NewReplacer("{", " ", "}", " ", ".", " ").Replace(info))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// docsCmdDropBackslash trims a line and cuts the shell continuation it ends with, and reports
// whether it ended with one. It is lenient on purpose: a bare trailing \ (with no blank before it)
// continues the line too, and the join adds a blank where the shell adds none, because a docs page
// writes one argument per line and never splits one.
func docsCmdDropBackslash(text string) (string, bool) {
	text = strings.TrimRight(text, " \t")
	if !strings.HasSuffix(text, `\`) {
		return text, false
	}
	return strings.TrimRight(strings.TrimSuffix(text, `\`), " \t"), true
}

// docsCmdStripComment drops a # that starts a word (it is the first character or follows a blank) and
// is outside quotes and substitutions, and everything after it.
func docsCmdStripComment(text string) string {
	var sh docsCmdShell
	for i := 0; i < len(text); i++ {
		if sh.outside(false) && text[i] == '#' && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t') {
			return text[:i]
		}
		sh.step(text[i])
	}
	return text
}

// docsCmdSegments returns, in order, the tnctl commands one command text holds. A line may hold
// several: the parts are cut at |, && and || (with the blanks as written, so --mode
// auto|quick|council is never cut) and at every ; (with or without blanks, also at the end). Each
// part is cut at its first redirection, its leading environment assignments (COUNCIL=ops,
// X=$(…)) are dropped, and it is a command only when its first word is tnctl. A tnctl inside a
// command substitution is not checked: the substitution is one placeholder token.
func docsCmdSegments(text string) [][]string {
	var out [][]string
	for _, part := range docsCmdSplitOps(docsCmdStripComment(text)) {
		part = docsCmdCutRedirect(part)
		toks := docsCmdDropAssignments(docsCmdTokens(part))
		if len(toks) > 0 && toks[0] == "tnctl" {
			out = append(out, toks)
		}
	}
	return out
}

// docsCmdSplitOps cuts the text into parts at every shell operator that separates two commands: the
// spaced |, && and ||, and every ;. It never cuts inside quotes, substitutions or brackets, so the
// | of [--bot <bot> | --teamspace] (which the shell would read as a pipe, but a docs page writes as
// an alternative) stays inside its part.
func docsCmdSplitOps(text string) []string {
	var out []string
	var sh docsCmdShell
	start := 0
	for i := 0; i < len(text); i++ {
		if sh.outside(true) {
			if text[i] == ';' {
				out = append(out, text[start:i])
				start = i + 1
			} else if n := docsCmdOpLen(text[i:]); n > 0 {
				out = append(out, text[start:i])
				i += n - 1
				start = i + 1
			}
		}
		sh.step(text[i])
	}
	return append(out, text[start:])
}

// docsCmdOpLen is the length of the command-separating operator the text starts with ( | , && or
// || , blanks included), 0 when it starts with none.
func docsCmdOpLen(text string) int {
	for _, op := range []string{" | ", " && ", " || "} {
		if strings.HasPrefix(text, op) {
			return len(op)
		}
	}
	return 0
}

// docsCmdCutRedirect cuts a part at its first redirection outside quotes, substitutions and
// brackets ( > , >> and 2> ), so the file a command writes to is not read as an argument of it.
func docsCmdCutRedirect(text string) string {
	var sh docsCmdShell
	for i := 0; i < len(text); i++ {
		if sh.outside(true) {
			for _, op := range []string{" > ", " >> ", " 2>"} {
				if strings.HasPrefix(text[i:], op) {
					return text[:i]
				}
			}
		}
		sh.step(text[i])
	}
	return text
}

// docsCmdDropAssignments drops the leading environment assignments of a command, so COUNCIL=ops
// tnctl chat scout and X=$(tnctl config get bot) tnctl chat scout are the commands they run.
func docsCmdDropAssignments(toks []string) []string {
	for len(toks) > 0 {
		i := strings.IndexByte(toks[0], '=')
		if i <= 0 || !docsCmdEnvName(toks[0][:i]) {
			return toks
		}
		toks = toks[1:]
	}
	return toks
}

// docsCmdEnvName reports whether name is a shell variable name: [A-Za-z_][A-Za-z0-9_]*.
func docsCmdEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case docsCmdIsASCIILetter(c) || c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// docsCmdTokens splits the text into words at runs of blanks outside quotes and substitutions. A
// quoted span ('…', "…", `…`) or a substitution ($(…), nested ones included) stays in one token
// with whatever touches it, so $(tnctl council role list -o name), --bot=$(echo scout) and
// `tnctl council role list -o name` are each one token.
func docsCmdTokens(text string) []string {
	var (
		out []string
		cur strings.Builder
		has bool
		sh  docsCmdShell
	)
	for i := 0; i < len(text); i++ {
		c := text[i]
		blank := sh.outside(false) && (c == ' ' || c == '\t')
		switch {
		case blank && has:
			out = append(out, cur.String())
			cur.Reset()
			has = false
		case !blank:
			cur.WriteByte(c)
			has = true
		}
		sh.step(c)
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// docsCmdCheck walks every tnctl command of one line down the tree of root and returns one message
// per problem, in order: a subcommand, argument or flag that tnctl does not have. It runs nothing.
func docsCmdCheck(root *cobra.Command, c docsCmdLine) []string {
	var msgs []string
	for _, toks := range docsCmdSegments(c.text) {
		msgs = append(msgs, docsCmdWalk(root, c, toks)...)
	}
	return msgs
}

// docsCmdWalk walks one command's tokens (toks[0] is tnctl) down the tree of root and returns one
// message per problem: a subcommand or argument tnctl would refuse, or a flag the command the walk
// ends on does not have. It runs nothing.
func docsCmdWalk(root *cobra.Command, c docsCmdLine, toks []string) []string {
	var msgs, flagToks []string
	cur := root
	walking := true
	badSub, anySub := false, false
	// unknownSub is the message for a word cur refuses: a subcommand tnctl does not have, or a
	// positional argument on a command that takes none.
	unknownSub := func(word string) string {
		return fmt.Sprintf("%s:%d: %s: unknown subcommand %q", c.page, c.line, cur.CommandPath(), word)
	}
loop:
	for i := 1; i < len(toks); i++ {
		tok := toks[i]
		switch {
		case tok == "|":
			// A spaced alternative inside a bracket ([--bot <bot> | --teamspace]): the segment
			// split never cuts inside a bracket, so the | arrives here as a word.
		case tok == "--":
			// -- ends the flags: nothing after it is a flag or a word of the command.
			if walking && i+1 < len(toks) && !docsCmdIsPlaceholder(toks[i+1]) && docsCmdRefusesWord(cur, toks[i+1]) {
				badSub = true
				msgs = append(msgs, unknownSub(toks[i+1]))
			}
			break loop
		case docsCmdIsFlagToken(tok):
			flagToks = append(flagToks, tok)
			// A flag that takes a value owns the next token: it is not a word of the command. When
			// that value is itself a flag (an unknown --nope before --jsno), cobra's walk still
			// skips it, but it is checked too, so every problem of the line is reported.
			if walking && docsCmdTakesValue(cur, tok) && i+1 < len(toks) {
				i++
				if docsCmdIsFlagToken(toks[i]) {
					flagToks = append(flagToks, toks[i])
				} else {
					flagToks = append(flagToks, docsCmdDashParts(toks[i])...)
				}
			}
		case docsCmdIsPlaceholder(tok):
			// The flags of such a line belong to some subcommand (council role <command> --bot
			// <bot>), so any of them may name a flag down there.
			if walking && cur.HasSubCommands() && docsCmdRefusesWord(cur, tok) {
				anySub = true
			}
			walking = false
			flagToks = append(flagToks, docsCmdDashParts(tok)...)
		case !walking:
			flagToks = append(flagToks, docsCmdDashParts(tok)...)
		default:
			// A plain word while the walk is on the command tree.
			if !cur.HasParent() && (tok == "help" || tok == "completion") {
				// cobra adds help and completion to the root only, and only when a command runs;
				// the flags written before the word are still checked, nothing after it is.
				break loop
			}
			if sub := docsCmdSub(cur, tok); sub != nil {
				cur = sub
				continue
			}
			if strings.Contains(tok, "|") && cur.HasSubCommands() && docsCmdRefusesWord(cur, tok) {
				// The subcommand slot names alternatives (roster add|remove): walk each of them
				// and return the union of their messages, in order and without duplicates.
				var alt []string
				seen := map[string]bool{}
				for _, part := range strings.Split(tok, "|") {
					if part == "" {
						continue
					}
					cp := append([]string(nil), toks...)
					cp[i] = part
					for _, m := range docsCmdWalk(root, c, cp) {
						if !seen[m] {
							seen[m] = true
							alt = append(alt, m)
						}
					}
				}
				return alt
			}
			walking = false
			// The message also covers a positional argument on a leaf that takes none: council
			// apply triage.yaml takes the file with -f.
			if docsCmdRefusesWord(cur, tok) || docsCmdNearSub(cur, tok) {
				badSub = true
				msgs = append(msgs, unknownSub(tok))
			}
		}
	}
	if badSub {
		// The flags belong to a command that does not exist: checking them against the group
		// would add a second, misleading message.
		return msgs
	}
	for _, tok := range flagToks {
		norm := docsCmdTrimFlag(tok)
		for _, part := range docsCmdFlagParts(norm) {
			if docsCmdFlagExists(cur, part) || docsCmdNotAFlag(part) {
				continue
			}
			if anySub && docsCmdSubHasFlag(cur, part) {
				continue
			}
			shown := tok
			if strings.Contains(norm, "|") {
				shown = part
			}
			msgs = append(msgs, fmt.Sprintf("%s:%d: %s: unknown flag %q", c.page, c.line, cur.CommandPath(), shown))
		}
	}
	return msgs
}

// docsCmdRefusesWord reports whether c refuses word: it is the root (cobra's legacyArgs refuses any
// unknown first word there), it cannot run, it reports the word as an unknown command, or it takes
// no positional argument and says so in its own words.
func docsCmdRefusesWord(c *cobra.Command, word string) bool {
	return !c.HasParent() || !c.Runnable() || docsCmdUnknownArgs(c, word) || docsCmdTakesNoArgs(c, word)
}

// docsCmdTakesNoArgs reports whether c refuses any positional argument in its own words, such as
// council apply ("takes the file with -f") and council role list ("takes no arguments"), rather
// than by the count of its arguments (roster add's arity text does not count).
func docsCmdTakesNoArgs(c *cobra.Command, word string) bool {
	return c.ValidateArgs(nil) == nil && c.ValidateArgs([]string{word}) != nil
}

// docsCmdNearSub reports whether a subcommand of c that is not Hidden has a name or alias one edit
// from word: one byte inserted, deleted or replaced, or two adjacent bytes swapped. It is a
// heuristic: a real positional argument one edit from a subcommand name would be flagged.
func docsCmdNearSub(c *cobra.Command, word string) bool {
	for _, sub := range c.Commands() {
		if sub.Hidden {
			continue
		}
		names := append([]string{sub.Name()}, sub.Aliases...)
		for _, name := range names {
			if name != word && docsCmdNearWord(name, word) {
				return true
			}
		}
	}
	return false
}

// docsCmdNearWord reports whether a and b are one edit apart: one byte inserted, deleted or
// replaced, or two adjacent bytes swapped.
func docsCmdNearWord(a, b string) bool {
	if a == b {
		return false
	}
	if len(a) == len(b) {
		diff, at := 0, -1
		for i := 0; i < len(a); i++ {
			if a[i] != b[i] {
				diff++
				at = i
			}
			if diff > 2 {
				return false
			}
		}
		switch diff {
		case 1:
			return true
		case 2:
			return at > 0 && a[at-1] == b[at] && a[at] == b[at-1]
		}
		return false
	}
	short, long := a, b
	if len(a) > len(b) {
		short, long = b, a
	}
	if len(long)-len(short) != 1 {
		return false
	}
	for i := 0; i < len(short); i++ {
		if short[i] != long[i] {
			return short[i:] == long[i+1:]
		}
	}
	return true
}

// docsCmdTakesValue reports whether the flag token tok, on cur, takes the next token as its value,
// as cobra's stripFlags does: the raw token must have no = in it and must not end with ] or ), the
// shorthand forms (-oyaml) own no value here, --help, -h and the root's --version and -v own none,
// and an unknown flag does own one. A flag with a NoOptDefVal (a bool) does not.
func docsCmdTakesValue(cur *cobra.Command, tok string) bool {
	if strings.Contains(tok, "=") || strings.HasSuffix(tok, "]") || strings.HasSuffix(tok, ")") {
		return false
	}
	name := docsCmdAltFlag(tok)
	if docsCmdNotAFlag(name) {
		return false
	}
	if strings.HasPrefix(name, "-") && !strings.HasPrefix(name, "--") && len(name) != 2 {
		return false
	}
	if name == "--help" || name == "-h" {
		return false
	}
	if !cur.HasParent() && (name == "--version" || name == "-v") {
		return false
	}
	f := docsCmdFlagOf(cur, name)
	return f == nil || f.NoOptDefVal == ""
}

// docsCmdDashParts returns the flags a plain word hides behind |, as written: [--how
// <text>|--how-file <file>] gives the token <text>|--how-file, whose part --how-file is a flag to
// check. A flag token, and a token that starts with ' " ` or $, hides nothing.
func docsCmdDashParts(tok string) []string {
	if tok == "" || docsCmdIsFlagToken(tok) || strings.ContainsRune("'\"`$", rune(tok[0])) {
		return nil
	}
	if !strings.Contains(tok, "|") {
		return nil
	}
	var out []string
	for _, p := range strings.Split(tok, "|") {
		if strings.HasPrefix(p, "-") {
			out = append(out, p)
		}
	}
	return out
}

// docsCmdSubHasFlag reports whether any descendant of c (children, grandchildren and so on) has the
// flag tok names.
func docsCmdSubHasFlag(c *cobra.Command, tok string) bool {
	for _, sub := range c.Commands() {
		if docsCmdFlagExists(sub, tok) || docsCmdSubHasFlag(sub, tok) {
			return true
		}
	}
	return false
}

// docsCmdIsFlagToken reports whether tok is a flag: a dash, or a bracket or parenthesis and a dash.
func docsCmdIsFlagToken(tok string) bool {
	if strings.HasPrefix(tok, "[") || strings.HasPrefix(tok, "(") {
		tok = tok[1:]
	}
	return strings.HasPrefix(tok, "-") && tok != "-"
}

// docsCmdIsPlaceholder reports whether tok is a placeholder or a quoted argument, never a
// subcommand: a token that starts with < [ ( ' " { $ or a backtick, or the ellipsis alone.
func docsCmdIsPlaceholder(tok string) bool {
	if tok == "" {
		return false
	}
	if tok == "..." || tok == "…" {
		return true
	}
	return strings.ContainsRune("<[('\"{$`", rune(tok[0]))
}

// docsCmdSub returns the subcommand of c that word names, by its name or one of its aliases.
func docsCmdSub(c *cobra.Command, word string) *cobra.Command {
	for _, sub := range c.Commands() {
		if sub.Name() == word {
			return sub
		}
		for _, a := range sub.Aliases {
			if a == word {
				return sub
			}
		}
	}
	return nil
}

// docsCmdUnknownArgs reports whether c refuses word as an unknown subcommand rather than as a
// positional argument of the wrong count.
func docsCmdUnknownArgs(c *cobra.Command, word string) bool {
	err := c.ValidateArgs([]string{word})
	return err != nil && strings.Contains(err.Error(), "unknown command")
}

// docsCmdFlagSets are the flag sets a command sees: its own, the ones it inherits from its parents,
// and its own persistent ones.
func docsCmdFlagSets(c *cobra.Command) []*pflag.FlagSet {
	// InheritedFlags merges the parents' persistent flags into c's own set, so the first set below
	// sees them whether or not it is called first.
	return []*pflag.FlagSet{c.Flags(), c.InheritedFlags(), c.PersistentFlags()}
}

// docsCmdNotAFlag reports whether tok can never be a flag: empty, - or --, or a one-dash token
// whose first character after the dash is not an ASCII letter (a negative number in a prompt).
func docsCmdNotAFlag(tok string) bool {
	if tok == "" || tok == "-" || tok == "--" || !strings.HasPrefix(tok, "-") {
		return true
	}
	if strings.HasPrefix(tok, "--") {
		return false
	}
	rest := tok[1:]
	return rest == "" || !docsCmdIsASCIILetter(rest[0])
}

// docsCmdIsASCIILetter reports whether b is an ASCII letter.
func docsCmdIsASCIILetter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// docsCmdFlagOf returns the flag a normalized token names on c, nil when it names none. -x=value is
// accepted as pflag accepts it: the value is cut at the first =. Only a one-byte shorthand is looked
// up here, because pflag panics on a longer one; a value glued to a value-taking shorthand (-oyaml,
// -nteknoir-demo) is a known flag all the same, which docsCmdFlagExists checks for. --help and -h
// exist on every command and --version and -v on the root only: they are no flags here, because
// cobra adds them when rootCmd runs, and Execute sets rootCmd.Version (cmd/root.go:58).
func docsCmdFlagOf(c *cobra.Command, tok string) *pflag.Flag {
	if docsCmdNotAFlag(tok) {
		return nil
	}
	if strings.HasPrefix(tok, "--") {
		name := strings.TrimPrefix(tok, "--")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		for _, fs := range docsCmdFlagSets(c) {
			if f := fs.Lookup(name); f != nil {
				return f
			}
		}
		return nil
	}
	rest := tok[1:]
	if i := strings.IndexByte(rest, '='); i >= 0 {
		rest = rest[:i]
	}
	if len(rest) != 1 {
		return nil
	}
	for _, fs := range docsCmdFlagSets(c) {
		if f := fs.ShorthandLookup(rest); f != nil {
			return f
		}
	}
	return nil
}

// docsCmdFlagExists reports whether the normalized token names a flag c has. --help and -h exist on
// every command, and --version and -v on the root only, because cobra adds them when rootCmd runs
// and Execute sets rootCmd.Version (cmd/root.go:58). -x=value is accepted as pflag accepts it, and
// a value glued to a value-taking shorthand is a known flag, as pflag reads -oyaml as -o yaml and
// -nteknoir-demo as -n teknoir-demo (-tt stays unknown: there is no value-taking -t).
func docsCmdFlagExists(c *cobra.Command, tok string) bool {
	if docsCmdNotAFlag(tok) {
		return false
	}
	if strings.HasPrefix(tok, "--") {
		name := strings.TrimPrefix(tok, "--")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		return name == "help" || (!c.HasParent() && name == "version") || docsCmdFlagOf(c, "--"+name) != nil
	}
	rest := tok[1:]
	if i := strings.IndexByte(rest, '='); i >= 0 {
		rest = rest[:i]
	}
	switch {
	case rest == "h":
		return true
	case !c.HasParent() && rest == "v":
		return true
	case len(rest) == 1:
		return docsCmdFlagOf(c, "-"+rest) != nil
	}
	f := docsCmdFlagOf(c, "-"+rest[:1])
	return f != nil && f.NoOptDefVal == ""
}

// docsCmdTrimFlag strips the bracket a synopsis puts around a flag and the punctuation that closes
// it: [--bot] and --json] are --bot and --json.
func docsCmdTrimFlag(tok string) string {
	if strings.HasPrefix(tok, "[") || strings.HasPrefix(tok, "(") {
		tok = tok[1:]
	}
	for {
		before := tok
		tok = strings.TrimRight(tok, "]),.")
		tok = strings.TrimSuffix(tok, "…")
		tok = strings.TrimSuffix(tok, "...")
		if tok == before {
			return tok
		}
	}
}

// docsCmdFlagParts splits a normalized token into the flags it names. A token with | names
// alternatives ([--user|--project|--managed]): each part that starts with - is a flag, the others
// are its values.
func docsCmdFlagParts(norm string) []string {
	if !strings.Contains(norm, "|") {
		return []string{norm}
	}
	var parts []string
	for _, p := range strings.Split(norm, "|") {
		if strings.HasPrefix(p, "-") {
			parts = append(parts, p)
		}
	}
	return parts
}

// docsCmdAltFlag is the name a flag token is looked up by while the command is walked: its last
// alternative, as [--user|--project|--managed DIR] is looked up by --managed.
func docsCmdAltFlag(tok string) string {
	norm := docsCmdTrimFlag(tok)
	if !strings.Contains(norm, "|") {
		return norm
	}
	parts := strings.Split(norm, "|")
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.HasPrefix(parts[i], "-") {
			return parts[i]
		}
	}
	return norm
}

// docsCmdPages are the pages that describe the council commands, relative to the repository root.
var docsCmdPages = []string{
	"docs/bot-mode.md",
	"docs/commands/agent.md",
	"docs/commands/chat.md",
	"docs/commands/council.md",
	"docs/commands/mcp.md",
}

// TestDocsCouncilCommands checks every tnctl command line in the council docs against the command
// tree: the pages must only name subcommands and flags that tnctl has.
func TestDocsCouncilCommands(t *testing.T) {
	const repoRoot = ".."
	pages := append([]string(nil), docsCmdPages...)
	for _, p := range pages {
		if _, err := os.Stat(filepath.Join(repoRoot, p)); err != nil {
			t.Fatalf("council docs page %s: %v", p, err)
		}
	}
	more, err := filepath.Glob(filepath.Join(repoRoot, "docs", "commands", "council-*.md"))
	if err != nil {
		t.Fatalf("glob council docs pages: %v", err)
	}
	for _, m := range more {
		rel, err := filepath.Rel(repoRoot, m)
		if err != nil {
			t.Fatalf("page path %s: %v", m, err)
		}
		pages = append(pages, rel)
	}
	sort.Strings(pages)
	var lines []docsCmdLine
	for _, p := range pages {
		b, err := os.ReadFile(filepath.Join(repoRoot, p))
		if err != nil {
			t.Fatalf("read council docs page %s: %v", p, err)
		}
		lines = append(lines, docsCmdExtract(p, string(b))...)
	}
	if len(lines) < 10 {
		t.Fatalf("the council docs pages yield %d commands, want at least 10: the parser found no fences", len(lines))
	}
	var msgs []string
	for _, l := range lines {
		msgs = append(msgs, docsCmdCheck(rootCmd, l)...)
	}
	if len(msgs) > 0 {
		t.Errorf("the council docs name commands or flags tnctl does not have:\n%s", strings.Join(msgs, "\n"))
	}
}

// TestDocsCouncilParser checks the parser of TestDocsCouncilCommands on synthetic pages: each case
// gives the text of a page and the messages it must yield.
func TestDocsCouncilParser(t *testing.T) {
	// A shell block of one page: the opening fence is line 1, so the command is on line 2.
	bash := func(body string) string { return "```bash\n" + body + "\n```\n" }
	cases := []struct {
		name string
		page string
		want []string
		cmds int // when not zero, the number of commands the page must yield
	}{
		{
			name: "a good line",
			page: bash("tnctl council role list --bot scout --templates -n teknoir-demo"),
		},
		{
			name: "an unknown flag",
			page: bash("tnctl council role list --nope"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "--nope"`},
		},
		{
			name: "an unknown subcommand word",
			page: bash("tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a group that does not run",
			page: bash("tnctl agent instal"),
			want: []string{`case.md:2: tnctl agent: unknown subcommand "instal"`},
		},
		{
			name: "a continuation line",
			page: bash("tnctl chat <bot> [prompt...] [--thread <id>] [--new] [--mode auto|quick|council]\n" +
				"                             [--member fleet|coder] [--effort low|medium|high] [--jsno]"),
			want: []string{`case.md:2: tnctl chat: unknown flag "[--jsno]"`},
		},
		{
			name: "a comment",
			page: bash("tnctl chat threads scout # --nope here"),
		},
		{
			name: "a pipe",
			page: bash("tnctl council roster get ops --bot scout -o yaml | grep --nope"),
		},
		{
			name: "a quoted prompt",
			page: bash(`tnctl chat scout "is --x a flag?"`),
		},
		{
			name: "an arity check is not an unknown command",
			page: bash("tnctl council roster add ops-scout analyst --bot scout"),
		},
		{
			name: "placeholders as arguments",
			page: bash("tnctl council roster add <council> <member>"),
		},
		{
			name: "a global flag before the path",
			page: bash("tnctl -n teknoir-demo council roster list --bot scout"),
		},
		{
			name: "alternatives",
			page: bash("tnctl agent list [--user|--project|--managed DIR]"),
		},
		{
			name: "an unknown alternative",
			page: bash("tnctl agent list [--user|--projct]"),
			want: []string{`case.md:2: tnctl agent list: unknown flag "--projct"`},
		},
		{
			name: "a root typo",
			page: bash("tnctl counsil role list"),
			want: []string{`case.md:2: tnctl: unknown subcommand "counsil"`},
		},
		{
			name: "an unknown subcommand hides its flags",
			page: bash("tnctl council roster lst --bot scout"),
			want: []string{`case.md:2: tnctl council roster: unknown subcommand "lst"`},
		},
		{
			name: "a negative number",
			page: bash("tnctl chat scout -5"),
		},
		{
			name: "a text fence",
			page: "```text\ntnctl council rolee list\n```\n",
		},
		{
			name: "a shell prompt",
			page: bash("$ tnctl council rolee list"),
		},
		{
			name: "a tnctl after a pipe or an operator",
			page: bash("cat council.yaml | tnctl council aply -f - --yes\n" +
				"tnctl council role export triage --bot scout > t.yaml && tnctl council role aply -f t.yaml\n" +
				"tnctl login --check || tnctl council rolee list"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "aply"`,
				`case.md:3: tnctl council role: unknown subcommand "aply"`,
				`case.md:4: tnctl council: unknown subcommand "rolee"`,
			},
		},
		{
			name: "a semicolon without a blank before it",
			page: bash("tnctl council role export triage --bot scout -o yaml; tnctl council apply -f x.yaml"),
		},
		{
			name: "a semicolon at the end",
			page: bash("tnctl council role list;"),
		},
		{
			name: "a semicolon between commands",
			page: bash("tnctl council role list --bot scout; tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a semicolon with no blanks",
			page: bash("tnctl council role list;echo --e"),
		},
		{
			name: "an environment prefix",
			page: bash("COUNCIL=ops tnctl chat scout --nope"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--nope"`},
		},
		{
			name: "an environment prefix before a group",
			page: bash("TNCTL_NAMESPACE=teknoir-demo tnctl council role list --nope"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "--nope"`},
		},
		{
			name: "a tnctl reading a pipe",
			page: bash("cat ops.yaml | tnctl council apply -f - --nope"),
			want: []string{`case.md:2: tnctl council apply: unknown flag "--nope"`},
		},
		{
			name: "a # inside quotes is not a comment",
			page: bash(`tnctl chat scout "what is issue #12?" --nope`),
			want: []string{`case.md:2: tnctl chat: unknown flag "--nope"`},
		},
		{
			name: "a # inside single quotes is not a comment",
			page: bash(`tnctl chat scout 'issue #12?' --nope`),
			want: []string{`case.md:2: tnctl chat: unknown flag "--nope"`},
		},
		{
			name: "an operator inside quotes does not cut",
			page: bash(`tnctl chat scout "which pods use > 2 GiB?" --nope`),
			want: []string{`case.md:2: tnctl chat: unknown flag "--nope"`},
		},
		{
			name: "every shell operator cuts",
			page: bash("tnctl council role list > --a\n" +
				"tnctl council role list >> --b\n" +
				"tnctl council role list 2> --c\n" +
				"tnctl council role list && echo --d\n" +
				"tnctl council role list ; echo --e\n" +
				"tnctl council role list || echo --f"),
		},
		{
			name: "a spaced alternative in a bracket",
			page: bash("tnctl council role new <name> --job <text> [--bot <bot> | --teamspace] [--dry-run] [--nope]"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "[--nope]"`},
		},
		{
			name: "spaced alternatives",
			page: bash("tnctl agent list [--user | --project]"),
		},
		{
			name: "a spaced alternative misspelt",
			page: bash("tnctl agent list [--user | --projct]"),
			want: []string{`case.md:2: tnctl agent list: unknown flag "--projct]"`},
		},
		{
			name: "an alternative glued to a value",
			page: bash("tnctl council role new <name> --job <text> [--how <text>|--how-nope <file>]"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "--how-nope"`},
		},
		{
			name: "subcommand alternatives",
			page: bash("tnctl council roster add|remove <name> <member> --bot <bot> [--scope <scope>] [--as <name>]"),
		},
		{
			name: "a subcommand alternative misspelt",
			page: bash("tnctl council roster add|remuve <name> <member>"),
			want: []string{`case.md:2: tnctl council roster: unknown subcommand "remuve"`},
		},
		{
			name: "subcommand alternatives with a shared flag",
			page: bash("tnctl council role list|get --bot scout"),
		},
		{
			name: "a placeholder subcommand",
			page: bash("tnctl council role <command> [--bot <bot>] [-o table|json|yaml]"),
		},
		{
			name: "a placeholder two levels up",
			page: bash("tnctl council <command> --bot scout"),
		},
		{
			name: "an ellipsis subcommand",
			page: bash("tnctl council role ..."),
		},
		{
			name: "a subcommand typo under a command with args",
			page: bash("tnctl chat thread scout -o json"),
			want: []string{`case.md:2: tnctl chat: unknown subcommand "thread"`},
		},
		{
			name: "a swapped-letter subcommand typo",
			page: bash("tnctl chat thraeds scout"),
			want: []string{`case.md:2: tnctl chat: unknown subcommand "thraeds"`},
		},
		{
			name: "a word on a leaf that takes none",
			page: bash("tnctl council roster list scout\n" +
				"tnctl council role list scout\n" +
				"tnctl council apply triage.yaml"),
			want: []string{
				`case.md:2: tnctl council roster list: unknown subcommand "scout"`,
				`case.md:3: tnctl council role list: unknown subcommand "scout"`,
				`case.md:4: tnctl council apply: unknown subcommand "triage.yaml"`,
			},
		},
		{
			name: "a double dash ends the flags",
			page: bash("tnctl chat scout -- --why"),
		},
		{
			name: "a word after a double dash",
			page: bash("tnctl council -- rolee"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "help below the root",
			page: bash("tnctl council help\n" +
				"tnctl council role completion bash\n" +
				"tnctl council role help list"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "help"`,
				`case.md:3: tnctl council role: unknown subcommand "completion"`,
				`case.md:4: tnctl council role: unknown subcommand "help"`,
			},
		},
		{
			name: "completion at the root",
			page: bash("tnctl completion bash --no-descriptions\n" +
				"tnctl completion bash --nope\n" +
				"tnctl -n x completion bash\n" +
				"tnctl help council --nope"),
		},
		{
			name: "a flag before help at the root",
			page: bash("tnctl --nope help"),
			want: []string{`case.md:2: tnctl: unknown flag "--nope"`},
		},
		{
			name: "help at the root",
			page: bash("tnctl help council"),
		},
		{
			name: "a placeholder stops the walk",
			page: bash("tnctl council <group> list"),
		},
		{
			name: "a variable is a placeholder",
			page: bash("tnctl council roster $VERB ops"),
		},
		{
			name: "an unknown flag owns a value",
			page: bash("tnctl agent install --agnet claude --mcp"),
			want: []string{`case.md:2: tnctl agent install: unknown flag "--agnet"`},
		},
		{
			name: "an unknown flag in a command with no args",
			page: bash("tnctl mcp --councl yes"),
			want: []string{`case.md:2: tnctl mcp: unknown flag "--councl"`},
		},
		{
			name: "a leaf flag before the leaf",
			page: bash("tnctl council --bot scout role list"),
		},
		{
			name: "an unknown flag takes the subcommand as its value",
			page: bash("tnctl council role --templates list"),
			want: []string{`case.md:2: tnctl council role: unknown flag "--templates"`},
		},
		{
			name: "a closed bracket flag owns no value",
			page: bash("tnctl council role list [--bot] [--nope]"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "[--nope]"`},
		},
		{
			name: "a flag=value owns no value",
			page: bash("tnctl --namespace=teknoir-demo council role list --bot=scout"),
		},
		{
			name: "a bool flag owns no value",
			page: bash("tnctl --debug council role list"),
		},
		{
			name: "a flag after a placeholder owns no value",
			page: bash("tnctl chat <bot> --thread --jsno"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--jsno"`},
		},
		{
			name: "a glued shorthand value",
			page: bash("tnctl council role get stream-watcher --bot scout -oyaml"),
		},
		{
			name: "a shorthand with =",
			page: bash("tnctl council role get stream-watcher --bot scout -o=json"),
		},
		{
			name: "the version flag at the root",
			page: bash("tnctl --version\ntnctl -v"),
		},
		{
			name: "the version flag below the root",
			page: bash("tnctl council --version\ntnctl council role list -v"),
			want: []string{
				`case.md:2: tnctl council: unknown flag "--version"`,
				`case.md:3: tnctl council role list: unknown flag "-v"`,
			},
		},
		{
			name: "--help and -h",
			page: bash("tnctl council role list --help\ntnctl council roster -h"),
		},
		{
			name: "an unknown shorthand",
			page: bash("tnctl council role list -x"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "-x"`},
		},
		{
			name: "a one-dash word",
			page: bash("tnctl council role list -tt"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "-tt"`},
		},
		{
			name: "a sibling's flag",
			page: bash("tnctl council roster get ops --bot scout --as ops-copy\n" +
				"tnctl council roster list --bot scout --templates"),
			want: []string{
				`case.md:2: tnctl council roster get: unknown flag "--as"`,
				`case.md:3: tnctl council roster list: unknown flag "--templates"`,
			},
		},
		{
			name: "a parent's own flag",
			page: bash("tnctl chat threads scout --thread thr_1"),
			want: []string{`case.md:2: tnctl chat threads: unknown flag "--thread"`},
		},
		{
			name: "an unknown flag=value",
			page: bash("tnctl council role list --nope=1"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "--nope=1"`},
		},
		{
			name: "every problem of a line",
			page: bash("tnctl council role list --nope --jsno"),
			want: []string{
				`case.md:2: tnctl council role list: unknown flag "--nope"`,
				`case.md:2: tnctl council role list: unknown flag "--jsno"`,
			},
		},
		{
			name: "a command substitution",
			page: bash("tnctl council role get $(tnctl council role list --templates -o name)"),
		},
		{
			name: "a substitution as a flag value",
			page: bash("tnctl council role list --bot $(tnctl config get bot)"),
		},
		{
			name: "a substitution after =",
			page: bash("tnctl council role list --bot=$(echo scout)"),
		},
		{
			name: "a nested substitution with a pipe",
			page: bash("tnctl council role get $(echo $(tnctl council role list --templates -o name | head -1))"),
		},
		{
			name: "a backtick substitution",
			page: bash("tnctl council role get `tnctl council role list --templates -o name`"),
		},
		{
			name: "a flag after a substitution",
			page: bash("tnctl council role get $(echo triage) --nope"),
			want: []string{`case.md:2: tnctl council role get: unknown flag "--nope"`},
		},
		{
			name: "fences mkdocs renders as shell",
			page: "````bash\ntnctl council rolee list\n````\n" +
				"~~~bash\ntnctl council rostr list\n~~~\n" +
				"```bash title=\"List the members\"\ntnctl council rol list\n```\n",
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolee"`,
				`case.md:5: tnctl council: unknown subcommand "rostr"`,
				`case.md:8: tnctl council: unknown subcommand "rol"`,
			},
		},
		{
			name: "attribute fences",
			page: "```{.bash}\ntnctl council rolee list\n```\n" +
				"```{ .bash }\ntnctl council rostr list\n```\n",
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolee"`,
				`case.md:5: tnctl council: unknown subcommand "rostr"`,
			},
		},
		{
			name: "a shell fence shown inside a markdown fence is text",
			page: "````markdown\n```bash\ntnctl council rolee list\n```\n````\n",
		},
		{
			name: "a sh fence",
			page: "```sh\ntnctl council rolee list\n```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a bare fence is skipped",
			page: "```\ntnctl council rolee list\n```\n",
		},
		{
			name: "a fence in a list item",
			page: "1. List the members:\n\n    ```bash\n    tnctl council rolee list\n    ```\n",
			want: []string{`case.md:4: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a fence under a bullet",
			page: "- item\n\n   ```bash\n   tnctl council rolee list\n   ```\n",
			want: []string{`case.md:4: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a flag line at column 0 does not continue",
			page: bash("tnctl council role list\n--nope"),
		},
		{
			name: "a backslash continuation",
			page: bash("tnctl council roster add \\\n  ops-scout stream-watcher --bot scout --nope"),
			want: []string{`case.md:2: tnctl council roster add: unknown flag "--nope"`},
		},
		{
			name: "a backslash continuation without an indent",
			page: bash("tnctl council role new \"Stream watcher\" \\\n--job \"watches MQTT streams for gaps\" \\\n--bot scout --nope"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "--nope"`},
		},
		{
			name: "an indented backslash continuation",
			page: bash("tnctl council role new \"Stream watcher\" \\\n  --job \"watches MQTT streams for gaps\" \\\n  --bot scout --nope"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "--nope"`},
		},
		{
			name: "CRLF line endings",
			page: "```bash\r\ntnctl council role list --bot scout --templates\r\ntnctl\r\n```\r\n",
			cmds: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := docsCmdExtract("case.md", tc.page)
			if tc.cmds != 0 && len(lines) != tc.cmds {
				t.Errorf("commands: got %d, want %d", len(lines), tc.cmds)
			}
			var got []string
			for _, l := range lines {
				got = append(got, docsCmdCheck(rootCmd, l)...)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("messages\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
