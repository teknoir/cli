package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	odd  bool   // a line of it held a non-breaking or zero-width space
}

// docsCmdOddLen is the byte length of the space-like character a shell does not split at that text
// starts with (U+00A0, U+2007, U+202F, U+200B, U+FEFF): 0 when it starts with none of them.
func docsCmdOddLen(text string) int {
	for _, s := range []string{"\u00a0", "\u2007", "\u202f", "\u200b", "\ufeff"} {
		if strings.HasPrefix(text, s) {
			return len(s)
		}
	}
	return 0
}

// docsCmdFixOddSpaces turns every non-breaking or zero-width space (docsCmdOddLen) outside quotes,
// substitutions and the comment into a blank, so the command is still found and walked, and reports
// whether the line held one there: a shell reads it as part of a word, never as a blank, so the line
// is no command at all (and docsCmdCheck says so). A BOM at the start of a line is one of them.
func docsCmdFixOddSpaces(line string) (string, bool) {
	var (
		b   strings.Builder
		sh  docsCmdShell
		odd bool
	)
	for i := 0; i < len(line); i++ {
		if n := docsCmdOddLen(line[i:]); n > 0 {
			if sh.outside(false) {
				b.WriteByte(' ')
				sh.step(' ')
				odd = true
			} else {
				b.WriteString(line[i : i+n])
				for k := 0; k < n; k++ {
					sh.step(line[i+k])
				}
			}
			i += n - 1
			continue
		}
		if sh.outside(false) && line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			b.WriteString(line[i:]) // the comment is no part of the command
			return b.String(), odd
		}
		b.WriteByte(line[i])
		sh.step(line[i])
	}
	return b.String(), odd
}

// docsCmdShell tracks the shell context of a left-to-right scan of a command text: whether it is
// inside a quoted span ('…', "…" or `…`), inside a $( … ) command substitution, behind a backslash
// escape, how deep in brackets [ … ] it is, and how deep in a synopsis group (--a|--b) or {--a|--b}.
// The comment cut, the segment split, the redirect cut and the tokenizer all step through their text
// with it, so they agree on what "outside" means: outside a quote, outside a substitution, outside an
// escape and, where brackets count, at bracket and group depth 0.
type docsCmdShell struct {
	quote byte // the quote character the scan is inside, 0 outside
	sub   int  // the depth of the $( … ) substitution the scan is in, 0 outside
	brack int  // the bracket depth
	group int  // the synopsis group depth: a ( or { that the next byte shows to start a flag
	prev  byte // the byte before the one step takes next, 0 at the start
	next  byte // the byte after the one step takes, set by the caller
	esc   bool // the next byte is escaped by a backslash
}

// outside reports whether the scan is in plain text: not inside a quoted span, not inside a command
// substitution, not behind a backslash escape and, when brackets is true, not inside a bracket or a
// synopsis group.
func (s docsCmdShell) outside(brackets bool) bool {
	if s.quote != 0 || s.sub > 0 || s.esc {
		return false
	}
	return !brackets || (s.brack == 0 && s.group == 0)
}

// step applies the next byte of the text to the state. A backslash outside single quotes escapes the
// byte after it, so an escaped quote opens and closes nothing. A quote closes at the character that
// opened it. Outside quotes, $( opens a substitution and, inside one, every ( raises the depth and
// every ) lowers it (nested substitutions included); quotes are tracked inside a substitution too, so
// a ) in one does not close it. Outside both, a ( or a { whose next byte starts a flag opens a
// synopsis group ((--a|--b), {--a|--b}) and ) or } closes one, so a synopsis is never cut as shell;
// [ raises the bracket depth and ] lowers it, never below 0.
func (s *docsCmdShell) step(b byte) {
	if s.esc {
		s.esc, s.prev = false, b
		return
	}
	if b == '\\' && s.quote != '\'' {
		s.esc, s.prev = true, b
		return
	}
	switch {
	case s.quote != 0:
		if b == s.quote {
			s.quote = 0
		}
	case s.sub > 0:
		switch b {
		case '\'', '"', '`':
			s.quote = b
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
			} else if s.next == '-' {
				s.group++
			}
		case '{':
			if s.next == '-' {
				s.group++
			}
		case ')', '}':
			if s.group > 0 {
				s.group--
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

// docsCmdExtract returns the tnctl commands of one Markdown page: the commands of its ```bash and
// ```sh blocks (docsCmdShellLang names the other shell fence languages it reads too), and the
// commands its inline code spans quote outside every fence (`tnctl council role list --bot scout`).
// A page saved with CRLF line endings gives the same commands. The fences are lenient, as MkDocs
// renders them: an indented fence (a list item's) and a fence with an info string (```bash
// title="…", ```{.bash}) open a block too, and a fence closes it only when it is of the same
// character, at least as long, and has nothing after it, so a ```bash example shown inside a
// ````markdown block is skipped whole. A line starts a command when it holds at least one tnctl
// command (docsCmdSegments), with a console transcript's "$ " prompt dropped; a following line that
// is more indented than the line that starts the command and then starts with [ or -, or that
// follows a line ending in a backslash, continues the command above it. A blank line ends a command,
// also after a backslash, and so does a line whose quote or $( is still open when the next line
// starts a tnctl command. The body of a here-document is data, never a command.
func docsCmdExtract(page, markdown string) []docsCmdLine {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	var out []docsCmdLine
	var cur docsCmdLine
	has := false       // cur holds a command that is not in out yet
	cont := false      // the last line of cur is continued: a backslash, or a quote or $( still open
	curIndent := 0     // the indentation of the line that started cur
	heredoc := ""      // the delimiter of the here-document the scan is in, "" outside one
	var prose []string // the lines outside every fence, for their inline code spans
	proseLine := 0     // the 1-based line number of prose[0]
	// flush ends the command in cur: it goes to out when it holds one.
	flush := func() {
		if has {
			out = append(out, cur)
			has = false
		}
	}
	// flushProse adds the commands the lines outside the fences quote, and drops them.
	flushProse := func() {
		if len(prose) > 0 {
			out = append(out, docsCmdInline(page, proseLine, prose)...)
			prose = nil
		}
	}
	inBlock := false    // the scan is inside a fenced block
	openRun := ""       // the fence that opened it, such as ``` or ~~~~
	shellBlock := false // that block is a shell block, not one to skip
	for i, raw := range strings.Split(markdown, "\n") {
		raw, odd := docsCmdFixOddSpaces(raw)
		trimmed := strings.TrimLeft(raw, " \t")
		if run, info := docsCmdFence(trimmed); run != "" {
			switch {
			case !inBlock:
				flush()
				flushProse()
				inBlock, openRun, heredoc = true, run, ""
				shellBlock = docsCmdShellLang(strings.ToLower(docsCmdFenceLang(info)))
				cur, has, cont = docsCmdLine{}, false, false
				continue
			case run[0] == openRun[0] && len(run) >= len(openRun) && strings.TrimSpace(info) == "":
				inBlock, openRun, shellBlock, heredoc = false, "", false, ""
				flush()
				cur, has, cont = docsCmdLine{}, false, false
				continue
			}
			// Any other fence line inside a block is ordinary content: a ```bash example shown
			// inside a ````markdown block, or a line that is no command.
		}
		if !inBlock {
			if len(prose) == 0 {
				proseLine = i + 1
			}
			prose = append(prose, raw)
			flush()
			has, cont = false, false
			continue
		}
		if !shellBlock {
			flush()
			has, cont = false, false
			continue
		}
		if heredoc != "" {
			// A here-document's body is data (a chat prompt, a YAML file), never a command.
			if strings.TrimSpace(raw) == heredoc {
				heredoc = ""
			}
			flush()
			has, cont = false, false
			continue
		}
		heredoc = docsCmdHeredoc(docsCmdStripComment(trimmed))
		if has && cont && docsCmdOpen(docsCmdStripComment(cur.text)) && len(docsCmdSegments(trimmed)) > 0 {
			// An open quote or $( never swallows the next tnctl command: the line above is reported
			// as open instead.
			cont = false
		}
		indent := len(raw) - len(trimmed)
		switch {
		case trimmed == "":
			flush()
			has, cont = false, false
		case has && cont:
			cur.odd = cur.odd || odd
			cur.text += " " + strings.TrimSpace(raw)
			cur.text, cont = docsCmdDropBackslash(cur.text)
			cont = cont || docsCmdOpen(docsCmdStripComment(cur.text))
		case has && indent > curIndent && (trimmed[0] == '[' || trimmed[0] == '-'):
			cur.odd = cur.odd || odd
			cur.text += " " + strings.TrimSpace(raw)
			cur.text, cont = docsCmdDropBackslash(cur.text)
		default:
			if strings.HasPrefix(trimmed, "$ ") {
				// A console transcript's prompt is no part of the command.
				trimmed = strings.TrimLeft(trimmed[2:], " \t")
			}
			text, backslash := docsCmdDropBackslash(trimmed)
			if len(docsCmdSegments(text)) > 0 {
				flush()
				cur, has, cont = docsCmdLine{page: page, line: i + 1, text: text, odd: odd}, true,
					backslash || docsCmdOpen(docsCmdStripComment(text))
				curIndent = indent
			} else {
				flush()
				has, cont = false, false
			}
		}
	}
	flush()
	flushProse()
	return out
}

// docsCmdHeredoc returns the delimiter of the here-document a line opens (<<EOF, <<-EOF, <<'EOF',
// << "EOF") outside quotes and substitutions, or "" when it opens none. A here-string (<<<) is no
// here-document: its three < are stepped over whole, so the quotes after it still count.
func docsCmdHeredoc(text string) string {
	var sh docsCmdShell
	for i := 0; i+1 < len(text); i++ {
		if sh.outside(false) && strings.HasPrefix(text[i:], "<<<") {
			sh.step('<')
			sh.step('<')
			sh.step('<')
			i += 2
			continue
		}
		if sh.outside(false) && text[i] == '<' && text[i+1] == '<' {
			rest := strings.TrimLeft(strings.TrimPrefix(text[i+2:], "-"), " \t")
			if end := strings.IndexAny(rest, " \t;|&><)"); end >= 0 {
				rest = rest[:end]
			}
			return strings.Trim(rest, `'"`)
		}
		sh.step(text[i])
	}
	return ""
}

// docsCmdOpen reports whether the text ends inside a quote or a $( … ) substitution, so the shell
// would read the next line as part of it.
func docsCmdOpen(text string) bool {
	var sh docsCmdShell
	for i := 0; i < len(text); i++ {
		sh.step(text[i])
	}
	return sh.quote != 0 || sh.sub > 0
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

// docsCmdShellLang reports whether a fence's language, lowercased, is a shell: bash and sh are the
// page's own examples, and shell, zsh, console and shell-session are the other names a writer (or a
// pasted transcript) gives a block of shell commands.
func docsCmdShellLang(lang string) bool {
	switch lang {
	case "bash", "sh", "shell", "zsh", "console", "shell-session":
		return true
	}
	return false
}

// docsCmdFenceProblems reports the fences of one page that are wrong: one that never closes, and one
// that a block opened by a fence of the same character and length (or a shorter one) swallowed, so
// the shell block below it is read as text (a ```text block someone forgot to close). It reads the
// fences as docsCmdExtract does, so the two never disagree.
func docsCmdFenceProblems(page, markdown string) []string {
	var msgs []string
	openRun, openLine := "", 0
	for i, raw := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		fixed, _ := docsCmdFixOddSpaces(raw)
		run, info := docsCmdFence(strings.TrimLeft(fixed, " \t"))
		switch {
		case run == "":
		case openRun == "":
			openRun, openLine = run, i+1
		case run[0] == openRun[0] && len(run) >= len(openRun) && strings.TrimSpace(info) == "":
			openRun = ""
		case run[0] == openRun[0] && len(run) >= len(openRun) && docsCmdFenceLang(info) != "":
			msgs = append(msgs, fmt.Sprintf("%s:%d: a fence inside the block opened at line %d: close that block first", page, i+1, openLine))
		}
	}
	if openRun != "" {
		msgs = append(msgs, fmt.Sprintf("%s:%d: the fence never closes", page, openLine))
	}
	return msgs
}

// docsCmdDropBackslash trims a line and cuts the shell continuation it ends with, and reports
// whether it ended with one. It is lenient on purpose: a bare trailing \ (with no blank before it)
// continues the line too, and the join adds a blank where the shell adds none, because a docs page
// writes one argument per line and never splits one. A backslash inside a trailing comment ends
// nothing: in bash a # starts a comment that runs to the end of the line, backslash included.
func docsCmdDropBackslash(text string) (string, bool) {
	text = strings.TrimRight(text, " \t")
	if !strings.HasSuffix(text, `\`) || docsCmdStripComment(text) != text {
		return text, false
	}
	return strings.TrimRight(strings.TrimSuffix(text, `\`), " \t"), true
}

// docsCmdStripComment drops a # that starts a word (it is the first character or follows a blank) and
// is outside quotes, substitutions and escapes, and everything after it.
func docsCmdStripComment(text string) string {
	var sh docsCmdShell
	for i := 0; i < len(text); i++ {
		if sh.outside(false) && text[i] == '#' && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t') {
			return text[:i]
		}
		if i+1 < len(text) {
			sh.next = text[i+1]
		} else {
			sh.next = 0
		}
		sh.step(text[i])
	}
	return text
}

// docsCmdSegments returns, in order, the tnctl commands one command text holds. A line may hold
// several: the parts are cut at every ;, && and ||, at a | with a blank on either side (a | between
// two words, auto|quick, names alternatives), and at a single & that belongs to no &>, >& or <&;
// each part is cut at its first redirection. A part's leading environment assignments, the shell's
// words before it (do tnctl …, if tnctl …, xargs -n1 tnctl …, sudo tnctl …) and the ) that closes a
// subshell are dropped, and it is a command only when its first word is tnctl. A tnctl inside a
// command substitution is not checked: the substitution is one placeholder token.
func docsCmdSegments(text string) [][]string {
	var out [][]string
	for _, part := range docsCmdSplitOps(docsCmdStripComment(text)) {
		part = docsCmdCutRedirect(part)
		toks := docsCmdDropCloser(docsCmdDropAssignments(docsCmdDropKeywords(docsCmdTokens(part))))
		if len(toks) > 0 && toks[0] == "tnctl" {
			out = append(out, toks)
		}
	}
	return out
}

// docsCmdSplitOps cuts the text into parts at every shell operator that separates two commands
// (docsCmdOpAt). It never cuts inside quotes, substitutions, brackets or synopsis groups, so the |
// of [--bot <bot> | --teamspace] and of (--bot <bot>|--teamspace) (which the shell would read as a
// pipe, but a docs page writes as alternatives) stays inside its part.
func docsCmdSplitOps(text string) []string {
	var out []string
	var sh docsCmdShell
	start := 0
	for i := 0; i < len(text); i++ {
		if sh.outside(true) {
			if n := docsCmdOpAt(text, i); n > 0 {
				out = append(out, text[start:i])
				i += n - 1
				start = i + 1
			}
		}
		if i+1 < len(text) {
			sh.next = text[i+1]
		} else {
			sh.next = 0
		}
		sh.step(text[i])
	}
	return append(out, text[start:])
}

// docsCmdOpAt is the length of the operator that separates two commands at text[i], 0 when there is
// none: ; && || anywhere, a | with a blank on either side or at the end (a | between two words,
// auto|quick, names alternatives), and a & that is no part of &&, &> or >&.
func docsCmdOpAt(text string, i int) int {
	blank := func(j int) bool { return j < 0 || j >= len(text) || text[j] == ' ' || text[j] == '\t' }
	next := func(j int) byte {
		if j < len(text) {
			return text[j]
		}
		return 0
	}
	switch c := text[i]; {
	case c == ';':
		return 1
	case (c == '&' || c == '|') && next(i+1) == c:
		return 2
	case c == '|':
		if blank(i-1) || blank(i+1) {
			return 1
		}
	case c == '&' && next(i+1) != '>' && i > 0 && text[i-1] != '>' && text[i-1] != '<':
		return 1
	}
	return 0
}

// docsCmdCutRedirect cuts a part at its first redirection outside quotes, substitutions and
// brackets: a word that starts with >, >>, &> or N> (3>x, 10>&2), so the file a command writes to is
// not read as an argument of it. A redirect glued to the word before it (list>x) is left alone, so a
// <placeholder> keeps its > .
func docsCmdCutRedirect(text string) string {
	var sh docsCmdShell
	for i := 0; i < len(text); i++ {
		if sh.outside(true) && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t') {
			j := i
			for j < len(text) && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			if strings.HasPrefix(text[j:], ">") || (j == i && strings.HasPrefix(text[j:], "&>")) {
				return text[:i]
			}
		}
		if i+1 < len(text) {
			sh.next = text[i+1]
		} else {
			sh.next = 0
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

// docsCmdDropKeywords drops the shell words that may come before a command in a list or a loop (do
// tnctl …, if tnctl …, ! tnctl …, time tnctl …), and the words a command wrapper runs it through
// (xargs -n1 tnctl …, sudo tnctl …, env FOO=1 tnctl …, timeout 10 tnctl …), with the assignments
// after them. Only a wrapper's options, numbers and {} are skipped, so sudo mv tnctl /usr/local/bin/
// stays no command.
func docsCmdDropKeywords(toks []string) []string {
	for len(toks) > 0 {
		switch toks[0] {
		case "if", "then", "else", "elif", "do", "while", "until", "!", "time", "command", "{", "(":
			toks = docsCmdDropAssignments(toks[1:])
			continue
		case "xargs", "watch", "sudo", "env", "nohup", "exec", "timeout":
			// The wrapped command is the first word that is no option, number or {} of the wrapper.
			j := 1
			for j < len(toks) && (strings.HasPrefix(toks[j], "-") || toks[j] == "{}" || strings.Trim(toks[j], "0123456789") == "") {
				j++
			}
			toks = docsCmdDropAssignments(toks[j:])
			continue
		}
		if strings.HasPrefix(toks[0], "(") && len(toks[0]) > 1 {
			toks = append([]string{toks[0][1:]}, toks[1:]...)
			continue
		}
		return toks
	}
	return toks
}

// docsCmdDropCloser drops the ) that closes a subshell, (tnctl council role list) or ( … ), from the
// end of a command: a last token ) or a ) more than the last token opens. A balanced $( … ) stays.
func docsCmdDropCloser(toks []string) []string {
	for len(toks) > 0 {
		last := toks[len(toks)-1]
		if !strings.HasSuffix(last, ")") || strings.Count(last, ")") <= strings.Count(last, "(") {
			return toks
		}
		if last = last[:len(last)-1]; last == "" {
			toks = toks[:len(toks)-1]
		} else {
			toks = append(toks[:len(toks)-1:len(toks)-1], last)
		}
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

// docsCmdTokens splits the text into words at runs of blanks outside quotes, substitutions and
// escapes. A quoted span ('…', "…", `…`) or a substitution ($(…), nested ones included) stays in one
// token with whatever touches it, so $(tnctl council role list -o name), --bot=$(echo scout) and
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
// per problem, in order: a subcommand, argument or flag that tnctl does not have, more positional
// arguments than the command takes, a flag tnctl hides, -o on a command that takes --json instead, a
// quote or $( that never closes, or a non-breaking space the shell reads as part of a word. It runs
// nothing.
func docsCmdCheck(root *cobra.Command, c docsCmdLine) []string {
	var msgs []string
	if c.odd {
		msgs = append(msgs, fmt.Sprintf("%s:%d: a non-breaking or zero-width space: the shell reads it as part of a word", c.page, c.line))
	}
	if docsCmdOpen(docsCmdStripComment(c.text)) {
		return append(msgs, fmt.Sprintf("%s:%d: a quote or $( that never closes", c.page, c.line))
	}
	for _, toks := range docsCmdSegments(c.text) {
		msgs = append(msgs, docsCmdWalk(root, c, toks)...)
	}
	return msgs
}

// docsCmdWalk walks one command's tokens (toks[0] is tnctl) down the tree of root and returns one
// message per problem: a subcommand, argument or flag tnctl would refuse, more positional arguments
// than the command the walk ends on takes, or a flag the command does not have. It runs nothing.
func docsCmdWalk(root *cobra.Command, c docsCmdLine, toks []string) []string {
	var msgs, flagToks []string
	cur := root
	walking := true
	badSub, anySub := false, false
	var pos []string // the positional arguments the line gives, for the too-many check
	concrete := true // every one is one the page means: no placeholder, no | and no --
	outVal := ""     // the value the line gives the root's -o/--output flag, "" when none
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
			// split never cuts inside a bracket or a synopsis group, so the | arrives here as a
			// word. The value after it is the other alternative of the flag before it, not a word
			// of the command, but it may still hide a flag.
			concrete = false
			if i+1 < len(toks) && !docsCmdIsFlagToken(toks[i+1]) {
				i++
				flagToks = append(flagToks, docsCmdDashParts(toks[i])...)
			}
		case tok == "--":
			// -- ends the flags: nothing after it is a flag or a word of the command.
			if walking && i+1 < len(toks) && !docsCmdIsPlaceholder(toks[i+1]) && docsCmdRefusesWord(cur, toks[i+1]) {
				badSub = true
				msgs = append(msgs, unknownSub(toks[i+1]))
			}
			concrete = false
			break loop
		case docsCmdIsFlagToken(tok):
			flagToks = append(flagToks, tok)
			// A flag that takes a value owns the next token, whether or not the walk is still on the
			// command tree, as pflag does. An unknown flag owns one too, but a flag after it is left
			// to the loop, with its own value: the unknown flag fails the run anyway, and the typo is
			// worth naming instead of the stray word it would swallow.
			if docsCmdTakesValue(cur, tok) && i+1 < len(toks) {
				next := toks[i+1]
				unknown := !docsCmdFlagExists(cur, docsCmdAltFlag(tok))
				if unknown && docsCmdIsFlagToken(next) && !docsCmdNotAFlag(docsCmdTrimFlag(next)) {
					break
				}
				i++
				if docsCmdIsFlagToken(next) {
					flagToks = append(flagToks, next)
				} else {
					flagToks = append(flagToks, docsCmdDashParts(next)...)
				}
				if v, ok := docsCmdOutputValue(docsCmdTrimFlag(tok)); ok && v == "" {
					outVal = next
				}
			}
		case docsCmdIsPlaceholder(tok):
			// The flags of such a line belong to some subcommand (council role <command> --bot
			// <bot>), so any of them may name a flag down there. A quoted word is a real argument (a
			// name with spaces), counted for the too-many check; any other placeholder leaves the
			// count unknown.
			if walking && cur.HasSubCommands() && docsCmdRefusesWord(cur, tok) {
				anySub = true
			}
			if docsCmdIsQuoted(tok) {
				pos = append(pos, tok)
			} else {
				concrete = false
			}
			walking = false
			flagToks = append(flagToks, docsCmdDashParts(tok)...)
		case !walking:
			// A plain word after the walk left the tree is a positional argument of the command it
			// ended on, counted for the too-many check.
			pos = append(pos, tok)
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
			if strings.Contains(tok, "|") && strings.Trim(tok, "|") != "" && cur.HasSubCommands() && docsCmdRefusesWord(cur, tok) {
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
			pos = append(pos, tok)
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
	if concrete && cur.Runnable() && len(pos) > 0 && cur.ValidateArgs(pos) != nil {
		// The command takes fewer arguments than the line gives it: name the first argument past the
		// longest prefix the command accepts (role new Stream watcher: "watcher").
		for k := len(pos) - 1; k >= 0; k-- {
			if cur.ValidateArgs(pos[:k]) == nil {
				msgs = append(msgs, fmt.Sprintf("%s:%d: %s: too many arguments: %q", c.page, c.line, cur.CommandPath(), pos[k]))
				break
			}
		}
	}
	for _, tok := range flagToks {
		norm := docsCmdTrimFlag(tok)
		if v, ok := docsCmdOutputValue(norm); ok && v != "" {
			outVal = v
		}
		for _, part := range docsCmdFlagParts(norm) {
			if docsCmdIsOutput(part) && docsCmdJSONOnly(cur) {
				msgs = append(msgs, fmt.Sprintf("%s:%d: %s: -o is ignored here, use --json", c.page, c.line, cur.CommandPath()))
				continue
			}
			if f := docsCmdFlagOf(cur, part); f != nil && f.Hidden {
				msgs = append(msgs, fmt.Sprintf("%s:%d: %s: hidden flag %q", c.page, c.line, cur.CommandPath(), "--"+f.Name))
				continue
			}
			if !docsCmdTypoDash(part) && (docsCmdFlagExists(cur, part) || docsCmdNotAFlag(part)) {
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
	if cur.CommandPath() == "tnctl council role export" && strings.EqualFold(outVal, "json") {
		msgs = append(msgs, fmt.Sprintf("%s:%d: %s: -o json is refused here (export prints YAML only; use council role get -o json)", c.page, c.line, cur.CommandPath()))
	}
	return msgs
}

// docsCmdIsQuoted reports whether tok is one quoted word ("Stream watcher"): it is longer than one
// byte and starts and ends with the same quote.
func docsCmdIsQuoted(tok string) bool {
	return len(tok) > 1 && (tok[0] == '"' || tok[0] == '\'') && tok[len(tok)-1] == tok[0]
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

// docsCmdJSONOnly reports whether c takes --json instead of the root's -o: it has a local json flag,
// and its RunE never calls outputFormat (root.go's -o help names every such command).
func docsCmdJSONOnly(c *cobra.Command) bool {
	return c.LocalNonPersistentFlags().Lookup("json") != nil
}

// docsCmdIsOutput reports whether the normalized flag token is the root's -o/--output in one of its
// forms: -o, --output, -o=json, -oyaml or --output=json.
func docsCmdIsOutput(p string) bool {
	return p == "-o" || p == "--output" || strings.HasPrefix(p, "--output=") || (strings.HasPrefix(p, "-o") && !strings.HasPrefix(p, "--"))
}

// docsCmdOutputValue is the value a -o/--output token carries itself (-o=json, -oyaml,
// --output=json), and whether the token is that flag at all. -o and --output carry none: their value
// is the next token.
func docsCmdOutputValue(p string) (string, bool) {
	switch {
	case p == "-o" || p == "--output":
		return "", true
	case strings.HasPrefix(p, "--output="):
		return strings.TrimPrefix(p, "--output="), true
	case strings.HasPrefix(p, "-o="):
		return strings.TrimPrefix(p, "-o="), true
	case strings.HasPrefix(p, "-o") && !strings.HasPrefix(p, "--"):
		return strings.TrimPrefix(p, "-o"), true
	}
	return "", false
}

// docsCmdTakesValue reports whether the flag token tok, on cur, takes the next token as its value,
// as cobra's stripFlags does: the raw token must have no = in it and must not end with ] , ) or },
// the shorthand forms (-oyaml) own no value here, --help, -h and the root's --version and -v own
// none, and an unknown flag, or one written with a typographic dash, does own one. A flag with a
// NoOptDefVal (a bool) does not.
func docsCmdTakesValue(cur *cobra.Command, tok string) bool {
	if strings.Contains(tok, "=") || strings.HasSuffix(tok, "]") || strings.HasSuffix(tok, ")") || strings.HasSuffix(tok, "}") {
		return false
	}
	name := docsCmdAltFlag(tok)
	if docsCmdTypoDash(name) {
		return true // an unknown flag owns a value, as the walk reads it
	}
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
		if strings.HasPrefix(p, "-") || docsCmdTypoDash(p) {
			out = append(out, p)
		}
	}
	return out
}

// docsCmdSubHasFlag reports whether any descendant of c (children, grandchildren and so on) has the
// flag tok names. cobra's help and completion commands, which it adds to the root only when a
// command runs, are never counted: a placeholder line must mean the same before and after a run.
// A hidden flag is no flag here either, so a page cannot name one through a placeholder.
func docsCmdSubHasFlag(c *cobra.Command, tok string) bool {
	for _, sub := range c.Commands() {
		if !c.HasParent() && (sub.Name() == "help" || sub.Name() == "completion") {
			continue
		}
		f := docsCmdFlagOf(sub, tok)
		if ((f == nil || !f.Hidden) && docsCmdFlagExists(sub, tok)) || docsCmdSubHasFlag(sub, tok) {
			return true
		}
	}
	return false
}

// docsCmdIsFlagToken reports whether tok is a flag: a dash, a typographic dash where one belongs, or
// a bracket, parenthesis or brace and a dash.
func docsCmdIsFlagToken(tok string) bool {
	if strings.HasPrefix(tok, "[") || strings.HasPrefix(tok, "(") || strings.HasPrefix(tok, "{") {
		tok = tok[1:]
	}
	return strings.HasPrefix(tok, "-") && tok != "-" || docsCmdTypoDash(tok)
}

// docsCmdTypoDash reports whether tok starts with a typographic dash (‐ ‑ ‒ – — ― −) where a flag's
// - belongs.
func docsCmdTypoDash(tok string) bool {
	for _, d := range []string{"\u2010", "\u2011", "\u2012", "\u2013", "\u2014", "\u2015", "\u2212"} {
		if strings.HasPrefix(tok, d) {
			return true
		}
	}
	return false
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

// docsCmdTrimFlag strips the bracket, parenthesis or brace a synopsis puts around a flag and the
// punctuation that closes it: [--bot], {--bot} and --json] are --bot and --json.
func docsCmdTrimFlag(tok string) string {
	if strings.HasPrefix(tok, "[") || strings.HasPrefix(tok, "(") || strings.HasPrefix(tok, "{") {
		tok = tok[1:]
	}
	for {
		before := tok
		tok = strings.TrimRight(tok, "]}),.")
		tok = strings.TrimSuffix(tok, "…")
		tok = strings.TrimSuffix(tok, "...")
		if tok == before {
			return tok
		}
	}
}

// docsCmdFlagParts splits a normalized token into the flags it names. A token with | names
// alternatives ([--user|--project|--managed]): each part that starts with a dash, or with a
// typographic dash, is a flag, the others are its values.
func docsCmdFlagParts(norm string) []string {
	if !strings.Contains(norm, "|") {
		return []string{norm}
	}
	var parts []string
	for _, p := range strings.Split(norm, "|") {
		if strings.HasPrefix(p, "-") || docsCmdTypoDash(p) {
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

// docsCmdInlineRe is an inline code span that starts with tnctl: the docs quote many commands in
// prose, and they are checked as the commands they are.
var docsCmdInlineRe = regexp.MustCompile("`(tnctl [^`]+)`")

// docsCmdInlineSkip are the inline code spans that start with tnctl but are no command a page asks
// anyone to run: the refusal the serve command prints, and the name of an older release.
var docsCmdInlineSkip = map[string]bool{
	"tnctl council serve runs only inside a TeknoirBot": true,
	"tnctl v0.0.9-beta": true,
}

// docsCmdInline returns the commands a run of prose lines quotes in inline code (docsCmdInlineRe).
// A span's line breaks and blanks are collapsed (a span wrapped over two lines is one command) and a
// table cell's \| is read as |. Its line number is the line the span starts on: startLine is the
// 1-based line number of lines[0].
func docsCmdInline(page string, startLine int, lines []string) []docsCmdLine {
	text := strings.Join(lines, "\n")
	var out []docsCmdLine
	for _, m := range docsCmdInlineRe.FindAllStringSubmatchIndex(text, -1) {
		span := strings.Join(strings.Fields(strings.ReplaceAll(text[m[2]:m[3]], `\|`, "|")), " ")
		if docsCmdInlineSkip[span] {
			continue
		}
		out = append(out, docsCmdLine{page: page, line: startLine + strings.Count(text[:m[0]], "\n"), text: span})
	}
	return out
}

// docsCmdPages are the pages that describe the council commands, relative to the repository root.
var docsCmdPages = []string{
	"docs/bot-mode.md",
	"docs/commands/agent.md",
	"docs/commands/chat.md",
	"docs/commands/council.md",
	"docs/commands/index.md",
	"docs/commands/mcp.md",
}

// TestDocsCouncilCommands checks every tnctl command line in the council docs against the command
// tree: the pages must only name subcommands and flags that tnctl has, their fences must close, and
// a page must show its commands in a way the test reads.
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
	var msgs []string
	for _, p := range pages {
		b, err := os.ReadFile(filepath.Join(repoRoot, p))
		if err != nil {
			t.Fatalf("read council docs page %s: %v", p, err)
		}
		pageLines, pageMsgs := docsCmdPage(p, string(b))
		lines = append(lines, pageLines...)
		msgs = append(msgs, pageMsgs...)
	}
	if len(lines) < 10 {
		t.Fatalf("the council docs pages yield %d commands, want at least 10: the parser found no fences", len(lines))
	}
	for _, l := range lines {
		msgs = append(msgs, docsCmdCheck(rootCmd, l)...)
	}
	if len(msgs) > 0 {
		t.Errorf("the council docs name commands or flags tnctl does not have:\n%s", strings.Join(msgs, "\n"))
	}
}

// docsCmdPage reads one page and returns its commands and the messages about the page itself: a
// fence that is wrong (docsCmdFenceProblems), and a page that yields no tnctl command at all, which
// means its commands are written the way the parser does not read them.
func docsCmdPage(page, markdown string) ([]docsCmdLine, []string) {
	msgs := docsCmdFenceProblems(page, markdown)
	lines := docsCmdExtract(page, markdown)
	if len(lines) == 0 {
		msgs = append(msgs, fmt.Sprintf("%s yields no tnctl command: are its commands outside ```bash blocks?", page))
	}
	return lines, msgs
}

// TestDocsCouncilJSONOnlyCommands pins the commands that take --json instead of the root's -o: they
// are exactly the commands with a local json flag, and root.go's -o help names each of them.
func TestDocsCouncilJSONOnlyCommands(t *testing.T) {
	var got []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if docsCmdJSONOnly(c) {
			got = append(got, strings.TrimPrefix(c.CommandPath(), "tnctl "))
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	sort.Strings(got)
	want := []string{"chat", "chat threads", "council role list", "council roster list", "mqtt sample", "mqtt stream"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the commands with a local --json flag: got %q, want %q", got, want)
	}
	usage := rootCmd.PersistentFlags().Lookup("output").Usage
	for _, path := range want {
		if !docsCmdInHelp(usage, path) {
			t.Errorf("root.go's -o help does not name %q: %s", path, usage)
		}
	}
}

// docsCmdInHelp reports whether the -o help mentions the command at path: each of its words appears
// in the help, in order, as a word of its own (so "mqtt stream|sample" names mqtt sample).
func docsCmdInHelp(usage, path string) bool {
	words := strings.FieldsFunc(usage, func(r rune) bool {
		return !docsCmdIsASCIILetter(byte(r)) && (r < '0' || r > '9')
	})
	rest := strings.Fields(path)
	for _, w := range words {
		if len(rest) > 0 && w == rest[0] {
			rest = rest[1:]
		}
	}
	return len(rest) == 0
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
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a shell fence",
			page: "```shell\ntnctl council rolee list\n```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a console fence",
			page: "```console\n$ tnctl council rolee list\n```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a fence language in capitals",
			page: "```Bash\ntnctl council rolee list\n```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
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
			name: "a flag=value before help at the root",
			page: bash("tnctl --nope=1 help council\ntnctl --nope=1 completion bash"),
			want: []string{
				`case.md:2: tnctl: unknown flag "--nope=1"`,
				`case.md:3: tnctl: unknown flag "--nope=1"`,
			},
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
			name: "a shorter fence inside a longer one is text",
			page: "````markdown\n```\n```bash\ntnctl council rolee list\n```\n````\n",
		},
		{
			name: "a fence with an info string does not close a block",
			page: "```text\n```bash\n```\n```bash\ntnctl council rolee list\n```\n",
			want: []string{
				`case.md:2: a fence inside the block opened at line 1: close that block first`,
				`case.md:5: tnctl council: unknown subcommand "rolee"`,
			},
		},
		{
			name: "a backtick fence does not close a tilde block",
			page: "~~~text\n```\n```bash\ntnctl council rolee list\n```\n~~~\n",
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
			name: "an indented flag line in a list item does not continue",
			page: "1. Step:\n\n    ```bash\n    tnctl council role list\n    --nope\n    ```\n" +
				bash("for r in triage scout; do\n  tnctl council role get \"$r\" --bot scout\n  [ $? -eq 0 ] || echo failed\ndone"),
		},
		{
			name: "a loop body at the command's indent does not continue",
			page: "1. Step:\n\n    ```bash\n    tnctl council role list --bot scout\n    [ -s roles.txt ] || tnctl council rolee list\n    ```\n",
			want: []string{`case.md:5: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a blank line ends a backslash continuation",
			page: bash("tnctl council role list \\\n\n  --nope"),
		},
		{
			name: "an unknown flag before a value flag",
			page: bash("tnctl council roster list --jsno --bot scout"),
			want: []string{`case.md:2: tnctl council roster list: unknown flag "--jsno"`},
		},
		{
			name: "an unknown flag before a value flag on a group",
			page: bash("tnctl council --nope --bot scout role list"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "--nope"`},
		},
		{
			name: "a known value flag swallows a flag",
			page: bash("tnctl council role list -n --bot scout"),
			want: []string{`case.md:2: tnctl council role list: unknown subcommand "scout"`},
		},
		{
			name: "a glued shorthand value before the path",
			page: bash("tnctl -nteknoir-demo council role list"),
		},
		{
			name: "an alternative glued to a flag's value",
			page: bash("tnctl council role list [--bot <bot>|--tmplates]"),
			want: []string{`case.md:2: tnctl council role list: unknown flag "--tmplates]"`},
		},
		{
			name: "a closed parenthesis flag owns no value",
			page: bash("tnctl council role (--json) list"),
		},
		{
			name: "a closed brace flag owns no value",
			page: bash("tnctl council role list {--bot} scout"),
			want: []string{`case.md:2: tnctl council role list: unknown subcommand "scout"`},
		},
		{
			name: "the -h flag owns no value",
			page: bash("tnctl council -h rolee"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "the -v flag at the root owns no value",
			page: bash("tnctl -v counsil"),
			want: []string{`case.md:2: tnctl: unknown subcommand "counsil"`},
		},
		{
			name: "an unquoted name with a space",
			page: bash(`tnctl council role new Stream watcher --job "x" --bot scout`),
			want: []string{`case.md:2: tnctl council role new: too many arguments: "watcher"`},
		},
		{
			name: "a quoted name with a space",
			page: bash(`tnctl council role new "Stream watcher" --job "x" --bot scout`),
		},
		{
			name: "a single-quoted name with a space",
			page: bash(`tnctl council role new 'Stream watcher' --job "x" --bot scout`),
		},
		{
			name: "a word after a quoted name",
			page: bash(`tnctl council role new "Stream watcher" extra`),
			want: []string{`case.md:2: tnctl council role new: too many arguments: "extra"`},
		},
		{
			name: "a word after the positional",
			page: bash("tnctl council roster get ops --bot scout extra -o yaml"),
			want: []string{`case.md:2: tnctl council roster get: too many arguments: "extra"`},
		},
		{
			name: "a word after a positional on a command with one argument",
			page: bash("tnctl council roster use ops scout"),
			want: []string{`case.md:2: tnctl council roster use: too many arguments: "scout"`},
		},
		{
			name: "a command with no positional argument",
			page: bash("tnctl council roster delete"),
		},
		{
			name: "a chat prompt is many arguments",
			page: bash("tnctl chat scout what runs on arm-2"),
		},
		{
			name: "a word after a subcommand alternative",
			page: bash("tnctl council roster add|remove ops-scout analyst triage --bot scout"),
			want: []string{
				`case.md:2: tnctl council roster add: too many arguments: "triage"`,
				`case.md:2: tnctl council roster remove: too many arguments: "triage"`,
			},
		},
		{
			name: "a placeholder is no concrete argument",
			page: bash("tnctl council role new <name> extra"),
		},
		{
			name: "a double dash is a value flag's value",
			page: bash("tnctl chat scout --thread -- --why"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--why"`},
		},
		{
			name: "a doubled operator with no blanks",
			page: bash("tnctl login --check||tnctl council rolee list\ntnctl council role list |& tnctl council rostr list"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolee"`,
				`case.md:3: tnctl council: unknown subcommand "rostr"`,
			},
		},
		{
			name: "a spaced value alternative in a bracket",
			page: bash("tnctl council apply -f <file> [-o json | yaml]"),
		},
		{
			name: "spaced value alternatives",
			page: bash("tnctl agent install [--agent claude | codex | opencode | all] [--mcp]"),
		},
		{
			name: "a glued brace alternative",
			page: bash("tnctl council role new <name> --job <text> {--bot <bot>|--teamspace}"),
		},
		{
			name: "a spaced parenthesis alternative cuts nothing",
			page: bash("tnctl council role new <name> --job <text> (--bot <bot> | --teamspace) [--dry-runn]"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "[--dry-runn]"`},
		},
		{
			name: "a glued brace alternative misspelt",
			page: bash("tnctl council role new <name> --job <text> {--bott <bot>|--teamspace}"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "{--bott"`},
		},
		{
			name: "a subshell after a semicolon",
			page: bash("(cd x; tnctl council rolee list)"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a redirect with no blank",
			page: bash("tnctl council roster list --bot scout >councils.txt\n" +
				"tnctl council role list --bot scout >>councils.txt\n" +
				"tnctl council role list --bot scout &> out.txt\n" +
				"tnctl council role list --bot scout 1> out.txt\n" +
				"tnctl council role list 3>x"),
		},
		{
			name: "an operator at the end of a line",
			page: bash("tnctl council role list --bot scout &&\n  tnctl council rolee list\n" +
				"tnctl council --nope=1 ||\n  echo failed\ntnctl council role list --bot scout |||"),
			want: []string{
				`case.md:3: tnctl council: unknown subcommand "rolee"`,
				`case.md:4: tnctl council: unknown flag "--nope=1"`,
			},
		},
		{
			name: "operators without blanks",
			page: bash("tnctl council role list --bot scout >roles.txt\n" +
				"tnctl council role list --bot scout &> roles.txt\n" +
				"tnctl mcp & tnctl council rolee list\n" +
				"tnctl council role get triage -o json |jq .\n" +
				"tnctl login --check&&tnctl council rostr list\n" +
				"tnctl council role list --bot scout 10> x"),
			want: []string{
				`case.md:4: tnctl council: unknown subcommand "rolee"`,
				`case.md:6: tnctl council: unknown subcommand "rostr"`,
			},
		},
		{
			name: "a shell keyword before the command",
			page: bash("for r in triage scout; do tnctl council rolee get \"$r\" --bot scout; done\n" +
				"if tnctl council rostr list; then echo ok; fi\n" +
				"tnctl council role list -o name | xargs -n1 tnctl council rol get --bot scout"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolee"`,
				`case.md:3: tnctl council: unknown subcommand "rostr"`,
				// The -o on role list is a second, real problem of the line: role list takes --json.
				`case.md:4: tnctl council role list: -o is ignored here, use --json`,
				`case.md:4: tnctl council: unknown subcommand "rol"`,
			},
		},
		{
			name: "a subshell",
			page: bash("(tnctl council role list)\n( tnctl council role list )\n(cd x && tnctl council role list)"),
		},
		{
			name: "a subshell with a typo",
			page: bash("(tnctl council rolee list)"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a here-document body is data",
			page: bash("tnctl chat scout <<'EOF'\n  - restart --force if needed\ntnctl council role list shows nothing\nEOF\ntnctl council rolee list"),
			want: []string{`case.md:6: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a here-string opens no here-document",
			page: bash("tnctl chat scout <<< \"hi\"\ntnctl council rolee list"),
			want: []string{`case.md:3: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a here-string before a here-document",
			page: bash("tnctl chat scout <<< \"hi\" <<EOF\n  --force is data\nEOF\ntnctl council rolee list"),
			want: []string{`case.md:5: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "an escaped quote",
			page: bash("tnctl chat scout don\\'t --jsno\ntnctl chat scout what's up --jsno"),
			want: []string{
				`case.md:2: tnctl chat: unknown flag "--jsno"`,
				`case.md:3: a quote or $( that never closes`,
			},
		},
		{
			name: "an open quote never swallows the next command",
			page: bash("tnctl chat scout what's up\ntnctl council rolee list\ntnctl chat scout it's fine"),
			want: []string{
				`case.md:2: a quote or $( that never closes`,
				`case.md:3: tnctl council: unknown subcommand "rolee"`,
				`case.md:4: a quote or $( that never closes`,
			},
		},
		{
			name: "a typographic dash and a non-breaking space",
			page: bash("tnctl chat scout \u2013jsno\ntnctl\u00a0council rolee list"),
			want: []string{
				`case.md:2: tnctl chat: unknown flag "` + "\u2013" + `jsno"`,
				`case.md:3: a non-breaking or zero-width space: the shell reads it as part of a word`,
				`case.md:3: tnctl council: unknown subcommand "rolee"`,
			},
		},
		{
			name: "a byte order mark before a fence",
			page: "\ufeff```bash\ntnctl council rolee list\n```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
			cmds: 1,
		},
		{
			name: "a non-breaking space inside quotes",
			page: bash("tnctl chat scout \"hi\u00a0there\""),
		},
		{
			name: "a non-breaking space on a continued line",
			page: bash("tnctl council role list \\\n\u00a0--bot scout"),
			want: []string{
				`case.md:2: a non-breaking or zero-width space: the shell reads it as part of a word`,
			},
		},
		{
			name: "typographic dashes in an alternative",
			page: bash("tnctl council role list [\u2013a|\u2013b]"),
			want: []string{
				`case.md:2: tnctl council role list: unknown flag "` + "\u2013" + `a"`,
				`case.md:2: tnctl council role list: unknown flag "` + "\u2013" + `b"`,
			},
		},
		{
			name: "a comment ending in a backslash",
			page: bash("tnctl council role list --bot scout  # list the roles \\\ntnctl council rolee list"),
			want: []string{`case.md:3: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a continued command whose comment ends in a backslash",
			page: bash("tnctl council role list \\\n  --bot scout  # roles \\\ntnctl council rolee list"),
			want: []string{`case.md:4: tnctl council: unknown subcommand "rolee"`},
			cmds: 2,
		},
		{
			name: "a text block that swallows a shell fence",
			page: "```text\nsome output\n\n```bash\ntnctl council rolee list\n```\n",
			want: []string{`case.md:4: a fence inside the block opened at line 1: close that block first`},
		},
		{
			name: "a text block that swallows a longer shell fence",
			page: "```text\nout\n\n````bash\ntnctl council rolee list\n````\n",
			want: []string{`case.md:4: a fence inside the block opened at line 1: close that block first`},
		},
		{
			name: "a fence that never closes",
			page: "```bash\ntnctl council role list\n",
			want: []string{`case.md:1: the fence never closes`},
		},
		{
			name: "a placeholder subcommand and a completion flag",
			page: bash("tnctl <command> --no-descriptions"),
			want: []string{`case.md:2: tnctl: unknown flag "--no-descriptions"`},
		},
		{
			name: "an option on a command that takes --json",
			page: bash("tnctl council role list --bot scout -o json"),
			want: []string{`case.md:2: tnctl council role list: -o is ignored here, use --json`},
		},
		{
			name: "a glued option on a command that takes --json",
			page: bash("tnctl chat threads scout -oyaml"),
			want: []string{`case.md:2: tnctl chat threads: -o is ignored here, use --json`},
		},
		{
			name: "--output= on a command that takes --json",
			page: bash("tnctl council roster list --bot scout --output=json"),
			want: []string{`case.md:2: tnctl council roster list: -o is ignored here, use --json`},
		},
		{
			name: "json on the export command",
			page: bash("tnctl council role export triage --bot scout -o json"),
			want: []string{`case.md:2: tnctl council role export: -o json is refused here (export prints YAML only; use council role get -o json)`},
		},
		{
			name: "yaml on the export command",
			page: bash("tnctl council role export triage --bot scout -o yaml"),
		},
		{
			name: "json on a command that takes it",
			page: bash("tnctl council role get triage --bot scout -o json"),
		},
		{
			name: "a hidden flag",
			page: bash("tnctl council serve --kit-dir /etc/teknoirbot/kit"),
			want: []string{`case.md:2: tnctl council serve: hidden flag "--kit-dir"`},
		},
		{
			name: "a hidden flag in a bracketed alternative",
			page: bash("tnctl council serve [--kit-dir=/x]"),
			want: []string{`case.md:2: tnctl council serve: hidden flag "--kit-dir"`},
		},
		{
			name: "the serve synopsis without the hidden flag",
			page: bash("tnctl council serve [--max-turns 1|2] [--work-dir /work]"),
		},
		{
			name: "a hidden flag behind a placeholder",
			page: bash("tnctl council <command> --kit-dir /x"),
			want: []string{`case.md:2: tnctl council: unknown flag "--kit-dir"`},
		},
		{
			name: "a far word at the root",
			page: bash("tnctl roster list\ntnctl threads scout"),
			want: []string{
				`case.md:2: tnctl: unknown subcommand "roster"`,
				`case.md:3: tnctl: unknown subcommand "threads"`,
			},
		},
		{
			name: "a far word on a group that does not run",
			page: bash("tnctl agent setup"),
			want: []string{`case.md:2: tnctl agent: unknown subcommand "setup"`},
		},
		{
			name: "an indented flag line continues",
			page: bash("tnctl council roster add <name> <member>\n    --bot <bot> [--scop <scope>]"),
			want: []string{`case.md:2: tnctl council roster add: unknown flag "[--scop"`},
		},
		{
			name: "a command after a backslash continuation",
			page: bash("tnctl council role new \"X\" \\\n  --job \"y\" --bot scout\ntnctl council role lst --bot scout"),
			want: []string{`case.md:4: tnctl council role: unknown subcommand "lst"`},
		},
		{
			name: "a closed bracket flag before a word",
			page: bash("tnctl council role [--nope] lst"),
			want: []string{`case.md:2: tnctl council role: unknown subcommand "lst"`},
		},
		{
			name: "inline code",
			page: "Run `tnctl council rolee list --bot scout`.\n",
			want: []string{`case.md:1: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a wrapped span",
			page: "intro\n`tnctl mcp\n--council` and `tnctl agent install\n--hooks`\n",
		},
		{
			name: "the refusal text",
			page: "says `tnctl council serve runs only inside a TeknoirBot`.\n",
		},
		{
			name: "an escaped pipe in a table cell",
			page: "| `tnctl council roster list\\|get --bot B` | x |\n",
		},
		{
			name: "an escaped pipe with a typo",
			page: "| `tnctl council roster list\\|gett` | x |\n",
			want: []string{`case.md:1: tnctl council roster: unknown subcommand "gett"`},
		},
		{
			name: "a span after a fence keeps its line",
			page: "```bash\ntnctl council role list\n```\nsee `tnctl council role list --nope`\n",
			want: []string{`case.md:4: tnctl council role list: unknown flag "--nope"`},
		},
		{
			name: "a span inside a text fence",
			page: "```text\n`tnctl council rolee list`\n```\n",
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
			got := docsCmdFenceProblems("case.md", tc.page)
			for _, l := range lines {
				got = append(got, docsCmdCheck(rootCmd, l)...)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("messages\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestDocsCouncilPageProblems checks docsCmdPage on synthetic pages: a page in the docs must show its
// commands the way the parser reads them, so a page that yields none, or whose fence is wrong, is a
// finding even when nothing in it would be refused.
func TestDocsCouncilPageProblems(t *testing.T) {
	cases := []struct {
		name string
		page string
		want []string
	}{
		{
			name: "a page with a shell block",
			page: "```bash\ntnctl council role list\n```\n",
		},
		{
			name: "a page with a shell fence",
			page: "```shell\ntnctl council role list\n```\n",
		},
		{
			name: "a page with an inline span",
			page: "Run `tnctl council role list`.\n",
		},
		{
			name: "a page with no command at all",
			page: "Prose only: tnctl council rolee list is in a text block.\n",
			want: []string{"case.md yields no tnctl command: are its commands outside ```bash blocks?"},
		},
		{
			name: "a command behind a text fence is no command",
			page: "Run:\n\n```text\ntnctl council rolee list\n```\n",
			want: []string{"case.md yields no tnctl command: are its commands outside ```bash blocks?"},
		},
		{
			name: "a page whose fence never closes",
			page: "```bash\ntnctl council role list\n",
			want: []string{"case.md:1: the fence never closes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := docsCmdPage("case.md", tc.page)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("messages\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
