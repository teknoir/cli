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
	line int    // 1-based line number of the line the command starts on
	text string // the command text after joining continuations
	odd  bool   // a line of it held a non-breaking or zero-width space
	// unended is the delimiter of a here-document the line opens and the block never ends; text is
	// empty then and docsCmdCheck reports the here-document, not a command.
	unended string
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
// whether the line held one there: a shell reads it as part of a word, never as a blank (and
// docsCmdCheck says so). A BOM at the start of a line is one of them. The scan starts in the shell
// state sh: the zero state for a line of its own, and the state a command's earlier lines left for a
// line that goes on inside a quote or $( they opened (docsCmdStateAt).
func docsCmdFixOddSpaces(sh docsCmdShell, line string) (string, bool) {
	var (
		b   strings.Builder
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
// ````markdown block is skipped whole; a fence inside a blockquote (> ```bash), which MkDocs renders
// as code, opens a block too, read with its > markers dropped (docsCmdQuote). A line starts a
// command when it holds at least one tnctl command (docsCmdHasCmd), with a console transcript's "$ "
// prompt dropped; a following line that is more indented than the line that starts the command and
// then starts with [ or -, or that follows a line ending in a backslash, or whose quote or $( is
// still open (unless the next line starts a tnctl command), continues the command above it. A blank
// line ends a command, also after a backslash. The body of a here-document is data, never a command,
// and one whose delimiter never comes is reported (docsCmdLine.unended). Once a block has shown a
// "$ " prompt it is a console transcript: only the "$ " lines start a command, a "> " prompt is
// dropped, and every other line is output. A block without a prompt is read like bash. An indented
// code block (no fence) is never read here: docsCmdIndentProblems reports a command in one.
func docsCmdExtract(page, markdown string) []docsCmdLine {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	var out []docsCmdLine
	var cur docsCmdLine
	has := false          // cur holds a command that is not in out yet
	cont := false         // the last line of cur is continued: a backslash, or a quote or $( still open
	curIndent := 0        // the indentation of the line that started cur
	var heredocs []string // the delimiters of the here-documents still to read, in order
	heredocLine := 0      // the 1-based line number of the line that opened them
	var prose []string    // the lines outside every fence, for their inline code spans
	proseLine := 0        // the 1-based line number of prose[0]
	// flush ends the command in cur: it goes to out when it holds one.
	flush := func() {
		if has {
			out = append(out, cur)
			has = false
		}
	}
	// unended reports the here-documents a block ends inside, and forgets them.
	unended := func() {
		if len(heredocs) > 0 {
			out = append(out, docsCmdLine{page: page, line: heredocLine, unended: heredocs[0]})
			heredocs = nil
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
	openDepth := 0      // the blockquote markers before the fence that opened it
	shellBlock := false // that block is a shell block, not one to skip
	transcript := false // that block has shown a "$ " prompt: from there on only prompt lines start a command
	for i, row := range strings.Split(markdown, "\n") {
		line := row // the line as the page has it: the prose keeps it, and docsCmdInline fixes a span's odd spaces itself
		fixed, odd := docsCmdFixOddSpaces(docsCmdShell{}, line)
		limit := -1
		if inBlock {
			limit = openDepth
		}
		raw, depth := docsCmdQuote(fixed, limit)
		if inBlock && depth < openDepth {
			// The blockquote ended before the fence closed: so does the block (docsCmdFenceProblems
			// reports the fence).
			inBlock, openRun, shellBlock, transcript = false, "", false, false
			flush()
			unended()
			cur, has, cont = docsCmdLine{}, false, false
			raw, depth = docsCmdQuote(fixed, -1)
		}
		trimmed := strings.TrimLeft(raw, " \t")
		if run, info := docsCmdFence(trimmed); run != "" {
			switch {
			case !inBlock:
				flush()
				flushProse()
				inBlock, openRun, openDepth, transcript, heredocs = true, run, depth, false, nil
				shellBlock = docsCmdShellLang(strings.ToLower(docsCmdFenceLang(info)))
				cur, has, cont = docsCmdLine{}, false, false
				continue
			case run[0] == openRun[0] && len(run) >= len(openRun) && strings.TrimSpace(info) == "":
				inBlock, openRun, shellBlock, transcript = false, "", false, false
				flush()
				unended()
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
			prose = append(prose, line)
			flush()
			has, cont = false, false
			continue
		}
		if !shellBlock {
			flush()
			has, cont = false, false
			continue
		}
		ps2 := false
		if transcript && strings.HasPrefix(trimmed, "> ") {
			// A transcript's PS2 prompt (a here-document line, or the line after a backslash or an
			// open quote) is no part of what was typed.
			trimmed = strings.TrimLeft(trimmed[2:], " \t")
			raw, ps2 = trimmed, true
		}
		if len(heredocs) > 0 {
			// A here-document's body is data (a chat prompt, a YAML file), never a command.
			if strings.TrimSpace(raw) == heredocs[0] {
				heredocs = heredocs[1:]
			}
			flush()
			has, cont = false, false
			continue
		}
		if strings.HasPrefix(trimmed, "$ ") {
			// Set only outside a here-document: a "$ " line in a body is data.
			transcript = true
		}
		if transcript && (!has || !cont) && !strings.HasPrefix(trimmed, "$ ") {
			// A transcript's line without a prompt is output, never a command.
			flush()
			has, cont = false, false
			continue
		}
		start := docsCmdShell{} // the shell state the line starts in
		if has && cont && docsCmdOpen(docsCmdStripComment(cur.text)) {
			if len(docsCmdSegments(trimmed)) > 0 {
				// An open quote or $( never swallows the next tnctl command: the line above is
				// reported as open instead.
				cont = false
			} else {
				// The line goes on inside the quote or $( the line above left open: scan it from
				// there, so a << or a non-breaking space inside the quote is the quote's text.
				start = docsCmdStateAt(docsCmdStripComment(cur.text))
				src, _ := docsCmdQuote(line, depth)
				if t := strings.TrimLeft(src, " \t"); ps2 && strings.HasPrefix(t, "> ") {
					src = strings.TrimLeft(t[2:], " \t")
				}
				raw, odd = docsCmdFixOddSpaces(start, src)
				trimmed = strings.TrimLeft(raw, " \t")
			}
		}
		if heredocs = docsCmdHeredoc(start, trimmed); len(heredocs) > 0 {
			heredocLine = i + 1
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
			if docsCmdHasCmd(text) {
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
	unended()
	flushProse()
	return out
}

// docsCmdQuote drops the blockquote markers a line starts with (each > with at most one blank after
// it, and the blanks before it), at most limit of them, or all of them when limit is negative, and
// returns the rest of the line and how many it dropped. A line with no marker comes back whole. A
// block opened inside a blockquote (> ```bash, which pymdownx.superfences renders as code) is read
// with that many markers dropped from each of its lines; a block opened outside one drops none, so a
// "> " prompt line of a ```text transcript, or a "> EOF" in a here-document, stays as it is.
func docsCmdQuote(line string, limit int) (string, int) {
	n := 0
	for limit < 0 || n < limit {
		t := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(t, ">") {
			break
		}
		line = strings.TrimPrefix(t[1:], " ")
		n++
	}
	return line, n
}

// docsCmdHeredoc returns the delimiters, in order, of the here-documents a line opens (<<EOF,
// <<-EOF, <<'EOF', << "EOF", <<\EOF, <<E'O'F: quote removal drops every quote and backslash)
// outside quotes and substitutions, nil when it opens none. The scan starts in the shell state sh
// (docsCmdStateAt for a line that goes on inside a quote) and ends at a comment. A here-string (<<<)
// is no here-document: its three < are stepped over whole, so the quotes after it still count. A
// shift (<<= and an arithmetic line, (( … )) or let) and a << with no delimiter open none.
func docsCmdHeredoc(sh docsCmdShell, text string) []string {
	if t := strings.TrimLeft(text, " \t"); sh.outside(false) && (strings.HasPrefix(t, "((") || strings.HasPrefix(t, "let ")) {
		return nil // arithmetic: << is a shift there, never a here-document
	}
	var out []string
	for i := 0; i+1 < len(text); i++ {
		if sh.outside(false) && text[i] == '#' && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t') {
			return out // the comment is no part of the command
		}
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
			// <<= is a shift assignment; an empty delimiter is no here-document bash would read.
			if d := strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(rest); d != "" && !strings.HasPrefix(text[i+2:], "=") {
				out = append(out, d)
			}
			sh.step('<')
			sh.step('<')
			i++
			continue
		}
		sh.step(text[i])
	}
	return out
}

// docsCmdStateAt is the shell state at the end of a command text, once the blank the join adds after
// it is stepped too: the state the next line of the command starts in.
func docsCmdStateAt(text string) docsCmdShell {
	var sh docsCmdShell
	for i := 0; i < len(text); i++ {
		sh.step(text[i])
	}
	sh.step(' ')
	return sh
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
// follows it; both are empty when the line is no fence. A backtick run whose info string holds a
// backtick is no fence but an inline code span (```bash``` blocks), as in CommonMark.
func docsCmdFence(trimmed string) (run, info string) {
	if trimmed == "" || (trimmed[0] != '`' && trimmed[0] != '~') {
		return "", ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == trimmed[0] {
		n++
	}
	if n < 3 || (trimmed[0] == '`' && strings.Contains(trimmed[n:], "`")) {
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
// fences as docsCmdExtract does, so the two never disagree. A fence in a blockquote ends with the
// blockquote: a line with fewer > markers than the fence had closes nothing, and the fence never
// closes (superfences renders it as text).
func docsCmdFenceProblems(page, markdown string) []string {
	var msgs []string
	openRun, openLine, openDepth := "", 0, 0
	for i, raw := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		fixed, _ := docsCmdFixOddSpaces(docsCmdShell{}, raw)
		limit := -1
		if openRun != "" {
			limit = openDepth
		}
		rest, depth := docsCmdQuote(fixed, limit)
		if openRun != "" && depth < openDepth {
			// The blockquote ended before the fence closed.
			msgs = append(msgs, fmt.Sprintf("%s:%d: the fence never closes", page, openLine))
			openRun = ""
			rest, depth = docsCmdQuote(fixed, -1)
		}
		run, info := docsCmdFence(strings.TrimLeft(rest, " \t"))
		switch {
		case run == "":
		case openRun == "":
			openRun, openLine, openDepth = run, i+1, depth
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
// writes one argument per line and never splits one. An odd run of trailing backslashes continues
// the line; an even run is escaped backslashes, and ends it. A backslash inside a trailing comment
// ends nothing: in bash a # starts a comment that runs to the end of the line, backslash included.
func docsCmdDropBackslash(text string) (string, bool) {
	text = strings.TrimRight(text, " \t")
	// An even run of trailing backslashes is escaped backslashes, not a continuation.
	n := len(text) - len(strings.TrimRight(text, `\`))
	if n%2 == 0 || docsCmdStripComment(text) != text {
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
// words before it (do tnctl …, if tnctl …, xargs -n1 tnctl …, sudo tnctl …, a case pattern
// list) tnctl …), the assignments after them (FOO=1 sudo BAR=2 tnctl …) and the ) that closes a
// subshell are dropped, and it is a command only when its first word is tnctl. A tnctl inside a
// command substitution is no segment of its own: the substitution is one token here, and
// docsCmdSubs and docsCmdCheck read it on its own.
func docsCmdSegments(text string) [][]string {
	var out [][]string
	for _, part := range docsCmdSplitOps(docsCmdStripComment(text)) {
		part = docsCmdCutRedirect(part)
		toks := docsCmdDropCloser(docsCmdDropKeywords(docsCmdDropAssignments(docsCmdTokens(part))))
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
// auto|quick, names alternatives), and a & that is no part of &&, &>, >& or <&.
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
// tnctl chat scout and X=$(tnctl config get bot) tnctl chat scout are the commands they run. A line
// that is only an assignment from a substitution (id=$(tnctl chat threads scout --json)) holds no
// segment of its own: docsCmdHasCmd finds the tnctl inside it, and docsCmdCheck checks it there.
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

// docsCmdWrapperValueOpts holds, per command wrapper, the options whose value may be the next word
// (GNU xargs, sudo, env, timeout, watch, bash exec). An option whose value is optional and attached only
// (xargs -i, -e, -l) is not here.
var docsCmdWrapperValueOpts = map[string]map[string]bool{
	"xargs": {"-I": true, "-L": true, "-n": true, "-P": true, "-d": true, "-E": true, "-s": true, "-a": true,
		"--arg-file": true, "--delimiter": true, "--max-args": true, "--max-procs": true, "--max-chars": true,
		"--process-slot-var": true},
	"sudo": {"-u": true, "-g": true, "-U": true, "-C": true, "-D": true, "-p": true, "-r": true, "-t": true,
		"-T": true, "-R": true, "--user": true, "--group": true, "--other-user": true, "--close-from": true,
		"--chdir": true, "--prompt": true, "--role": true, "--type": true, "--command-timeout": true, "--chroot": true},
	"env":     {"-u": true, "-C": true, "--unset": true, "--chdir": true},
	"timeout": {"-s": true, "-k": true, "--signal": true, "--kill-after": true},
	"watch":   {"-n": true, "--interval": true},
	"exec":    {"-a": true},
}

// docsCmdDropKeywords drops the shell words that may come before a command in a list or a loop (do
// tnctl …, if tnctl …, ! tnctl …, time tnctl …), a case pattern (list) tnctl …, *) tnctl …), and the
// words a command wrapper runs it through (xargs -n1 tnctl …, sudo tnctl …, env FOO=1 tnctl …,
// timeout 10s tnctl …, watch -n 0.5 tnctl …), with the assignments after them. Only a wrapper's
// options (with the value of one that takes it apart, docsCmdWrapperValueOpts), numbers and {} are
// skipped, so sudo mv tnctl /usr/local/bin/ stays no command.
func docsCmdDropKeywords(toks []string) []string {
	for len(toks) > 0 {
		switch toks[0] {
		case "if", "then", "else", "elif", "do", "while", "until", "!", "time", "command", "{", "(":
			toks = docsCmdDropAssignments(toks[1:])
			continue
		case "xargs", "watch", "sudo", "env", "nohup", "exec", "timeout":
			// The wrapped command is the first word that is no option, number or {} of the wrapper.
			// An option that takes a value written apart from it (xargs -I %, sudo -u root) skips
			// that value too.
			j := 1
			for j < len(toks) && (strings.HasPrefix(toks[j], "-") || toks[j] == "{}" || docsCmdNumber(toks[j])) {
				if docsCmdWrapperValueOpts[toks[0]][toks[j]] && j+1 < len(toks) {
					j++
				}
				j++
			}
			toks = docsCmdDropAssignments(toks[j:])
			continue
		}
		if strings.HasSuffix(toks[0], ")") && !strings.Contains(toks[0], "(") {
			// A case pattern, list) tnctl … or *) tnctl …: the command follows it.
			toks = docsCmdDropAssignments(toks[1:])
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

// docsCmdNumber reports whether tok is a number or a duration a wrapper takes: 10, 0.5, 10s, 5m,
// 1.5h, 2d.
func docsCmdNumber(tok string) bool {
	if n := len(tok); n > 1 && strings.IndexByte("smhd", tok[n-1]) >= 0 {
		tok = tok[:n-1]
	}
	return strings.Trim(tok, "0123456789.") == "" && strings.ContainsAny(tok, "0123456789")
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
// arguments than the command takes, a flag tnctl hides, -o on a command that takes --json instead,
// -o json on council role export, a quote or $( that never closes, a non-breaking space the shell
// reads as part of a word, or a here-document whose delimiter never comes. The tnctl commands inside
// a command substitution of the line are checked the same way. It runs nothing.
func docsCmdCheck(root *cobra.Command, c docsCmdLine) []string {
	if c.unended != "" {
		return []string{fmt.Sprintf("%s:%d: a here-document whose delimiter %q never comes: the rest of the block is read as its body", c.page, c.line, c.unended)}
	}
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
	for _, sub := range docsCmdSubs(docsCmdStripComment(c.text)) {
		inner := c
		inner.text, inner.odd = sub, false
		msgs = append(msgs, docsCmdCheck(root, inner)...)
	}
	return msgs
}

// docsCmdSubs returns the texts of the outermost command substitutions of text, $( … ) and ` … `,
// the ones bash runs: outside single quotes, inside double quotes or outside any, and not behind a
// backslash. A ' inside "…" and a " inside '…' are plain characters. A $( … ) ends at the ) that
// closes it as docsCmdShell counts it, so a ) inside a quote in it ("1) restart") closes nothing.
// docsCmdCheck reads a nested substitution when it checks the outer one; an unclosed one is left to
// docsCmdOpen.
func docsCmdSubs(text string) []string {
	var out []string
	var quote byte // ' or " when the scan is inside that quote, 0 outside
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\\' && quote != '\'':
			i++
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '"':
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
		case c == '\'' && quote == 0:
			quote = '\''
		case c == '$' && i+1 < len(text) && text[i+1] == '(':
			sh := docsCmdShell{sub: 1}
			j := i + 2
			for ; j < len(text); j++ {
				if sh.step(text[j]); sh.sub == 0 {
					break
				}
			}
			if j < len(text) {
				out = append(out, text[i+2:j])
			}
			i = j
		case c == '`':
			j := i + 1
			for j < len(text) && text[j] != '`' {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(text) {
				out = append(out, text[i+1:j])
			}
			i = j
		}
	}
	return out
}

// docsCmdHasCmd reports whether text holds a tnctl command, itself or in a command substitution.
func docsCmdHasCmd(text string) bool {
	if len(docsCmdSegments(text)) > 0 {
		return true
	}
	for _, sub := range docsCmdSubs(docsCmdStripComment(text)) {
		if docsCmdHasCmd(sub) {
			return true
		}
	}
	return false
}

// docsCmdWalk walks one command's tokens (toks[0] is tnctl) down the tree of root and returns one
// message per problem: a subcommand, argument or flag tnctl would refuse, more positional arguments
// than the command the walk ends on takes, a flag the command does not have, a hidden flag, -o on a
// command that takes --json, or -o json on council role export. It runs nothing.
func docsCmdWalk(root *cobra.Command, c docsCmdLine, toks []string) []string {
	toks = docsCmdJoinSpaced(toks)
	var msgs, flagToks []string
	cur := root
	walking := true
	badSub, anySub := false, false
	subPh := ""      // the placeholder in a subcommand slot that stopped the walk
	var pos []string // the positional arguments the line gives, for the too-many check
	concrete := true // the count is exact: no spaced | alternative, and every placeholder is a quoted word or a one-word <name> outside the subcommand slot
	outVal := ""     // the value the line gives the root's -o/--output flag, "" when none
	// slotSubs are the commands a slot placeholder and the word after it may name (council
	// <role|roster> apply is council role apply or council roster apply): the line's flags are theirs.
	var slotSubs []*cobra.Command
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
			// -- ends the flags: nothing after it is a flag or a word of the command. The words after
			// it reach Args too, so the positional words before it are a lower bound: a too-many
			// result on them stays real, and the words after it are not counted.
			if walking && i+1 < len(toks) && !docsCmdIsPlaceholder(toks[i+1]) && docsCmdRefusesWord(cur, toks[i+1]) {
				badSub = true
				msgs = append(msgs, unknownSub(toks[i+1]))
			}
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
					outVal = strings.TrimRight(next, "]})")
				}
			}
		case docsCmdIsPlaceholder(tok):
			// The flags of such a line belong to some subcommand (council role <command> --bot
			// <bot>), so any of them may name a flag down there. A quoted word and a <name> for one
			// word are real arguments, counted for the too-many check (council roster use <name>
			// <bot>); any other placeholder (the group's subcommand slot, <file|->, [prompt...], $VAR)
			// leaves the count unknown.
			slot := walking && cur.HasSubCommands() && docsCmdRefusesWord(cur, tok)
			if slot {
				anySub = true
				subPh = tok
			}
			if docsCmdIsQuoted(tok) || (!slot && docsCmdIsOneWord(tok)) {
				pos = append(pos, tok)
			} else {
				concrete = false
			}
			walking = false
			flagToks = append(flagToks, docsCmdDashParts(tok)...)
		case !walking:
			// A plain word after the walk left the tree is a positional argument of the command it
			// ended on, counted for the too-many check.
			// cobra's help and completion are subcommands of the root here whether or not a run has added
			// them yet: tnctl always has them, and the line must mean the same before and after a run
			// (tnctl <command> help is tnctl help <command> written the wrong way round). A placeholder
			// that may stand for a child with that subcommand is that child's command (council <group>
			// apply is council role apply), and the line's flags are checked against those commands.
			if anySub && !badSub && docsCmdWordPlaceholder(subPh) && (docsCmdSub(cur, tok) != nil || docsCmdCobraOwn(cur, tok)) {
				if subs := docsCmdSlotSubs(cur, subPh, tok); len(subs) > 0 {
					if slotSubs == nil {
						slotSubs = subs
					}
				} else {
					// A word placeholder fills the subcommand slot, so the real subcommand after it is only
					// a positional argument, and the group refuses it (council roster <name> add).
					badSub = true
					msgs = append(msgs, fmt.Sprintf("%s:%d: %s: a placeholder before the subcommand %q", c.page, c.line, cur.CommandPath(), tok))
				}
			}
			pos = append(pos, tok)
			flagToks = append(flagToks, docsCmdDashParts(tok)...)
		default:
			// A plain word while the walk is on the command tree.
			if docsCmdCobraOwn(cur, tok) {
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
				// and return the union of their messages, in order and without duplicates. A word of
				// pipes alone (a spaced || inside a bracket or a group, which the split never cuts)
				// names no alternatives: it is refused as a word, and without the guard above the
				// union would be empty and drop every message of the line.
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
			if (anySub && slotSubs == nil && docsCmdSubHasFlag(cur, part)) || docsCmdAnyHasFlag(slotSubs, part) {
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

// docsCmdIsOneWord reports whether tok is a placeholder for one word: <name>, with no | or ellipsis.
func docsCmdIsOneWord(tok string) bool {
	return len(tok) > 2 && tok[0] == '<' && tok[len(tok)-1] == '>' && !strings.ContainsAny(tok, "|") &&
		!strings.Contains(tok, "...") && !strings.Contains(tok, "…")
}

// docsCmdJoinSpaced joins the words of a placeholder with spaces in it (<robot type>, <one line job>)
// into one token: the page means one value, though bash would split it. A token that opens a < and
// does not close it takes the tokens after it up to the first one with a >. The letter after the <
// leaves an input redirect (<) and a here-document (<<EOF) alone.
func docsCmdJoinSpaced(toks []string) []string {
	var out []string
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if len(tok) > 1 && tok[0] == '<' && docsCmdIsASCIILetter(tok[1]) && !strings.Contains(tok, ">") {
			for j := i + 1; j < len(toks); j++ {
				if strings.Contains(toks[j], ">") {
					tok = strings.Join(toks[i:j+1], " ")
					i = j
					break
				}
			}
		}
		out = append(out, tok)
	}
	return out
}

// docsCmdWordPlaceholder reports whether a placeholder stands for one word on the command line (a
// <name> or a quoted word), not for flags, a variable, an elision or a shell expansion.
func docsCmdWordPlaceholder(tok string) bool {
	if docsCmdIsQuoted(tok) && !strings.ContainsAny(tok, "$`") {
		return true
	}
	if !strings.HasPrefix(tok, "<") || !strings.HasSuffix(tok, ">") {
		return false
	}
	l := strings.ToLower(tok)
	return !strings.Contains(l, "flag") && !strings.Contains(l, "option") && !strings.HasPrefix(l, "<-")
}

// docsCmdSlotSubs returns the commands named word under the children of c that the placeholder ph in
// c's subcommand slot may stand for: the children ph names (<role|roster>, <role or roster>), or every
// child for a generic slot (<group>, <command>, <subcommand>, <cmd>). Then the line is one of those
// commands (council <group> apply is council role apply), and its flags are theirs. A <name> or a
// quoted word stands for none, and cobra's own help and completion are never such a child, so the
// answer is the same before and after a run.
func docsCmdSlotSubs(c *cobra.Command, ph, word string) []*cobra.Command {
	if !strings.HasPrefix(ph, "<") {
		return nil
	}
	var out []*cobra.Command
	add := func(child *cobra.Command) {
		if docsCmdCobraOwn(c, child.Name()) {
			return
		}
		if s := docsCmdSub(child, word); s != nil {
			for _, o := range out {
				if o == s {
					return
				}
			}
			out = append(out, s)
		}
	}
	parts := strings.FieldsFunc(strings.ToLower(ph), func(r rune) bool { return r < 'a' || r > 'z' })
	for _, p := range parts {
		switch p {
		case "group", "command", "subcommand", "cmd":
			for _, child := range c.Commands() {
				add(child)
			}
			return out
		}
		if child := docsCmdSub(c, p); child != nil {
			add(child)
		}
	}
	return out
}

// docsCmdAnyHasFlag reports whether one of cmds, or a descendant of one, has the flag tok names. A
// hidden flag is no flag here, as in docsCmdSubHasFlag.
func docsCmdAnyHasFlag(cmds []*cobra.Command, tok string) bool {
	for _, s := range cmds {
		f := docsCmdFlagOf(s, tok)
		if ((f == nil || !f.Hidden) && docsCmdFlagExists(s, tok)) || docsCmdSubHasFlag(s, tok) {
			return true
		}
	}
	return false
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

// docsCmdCobraOwn reports whether word names one of the commands cobra adds to the root c only when
// a command runs, help and completion: tnctl always has both, so the rules decide on the name alone,
// never on whether the root has the command yet, and a line means the same before and after a run.
func docsCmdCobraOwn(c *cobra.Command, word string) bool {
	return !c.HasParent() && (word == "help" || word == "completion")
}

// docsCmdSubHasFlag reports whether any descendant of c (children, grandchildren and so on) has the
// flag tok names. cobra's help and completion commands, which it adds to the root only when a
// command runs, are never counted: a placeholder line must mean the same before and after a run.
// A hidden flag is no flag here either, so a page cannot name one through a placeholder.
func docsCmdSubHasFlag(c *cobra.Command, tok string) bool {
	for _, sub := range c.Commands() {
		if docsCmdCobraOwn(c, sub.Name()) {
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
// - belongs. A token that is one dash and nothing else is a word, as an ASCII - is: pflag reads it as
// an argument (a word of a chat prompt set off by an em dash, or a flag's value).
func docsCmdTypoDash(tok string) bool {
	for _, d := range []string{"\u2010", "\u2011", "\u2012", "\u2013", "\u2014", "\u2015", "\u2212"} {
		if tok == d {
			return false
		}
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
// prose, and they are checked as the commands they are. A non-breaking or zero-width space after
// tnctl (docsCmdOddLen) is one the shell would not split at: the span is found all the same, and
// docsCmdInline reports the space.
var docsCmdInlineRe = regexp.MustCompile("`(tnctl[ \\x{00a0}\\x{2007}\\x{202f}\\x{200b}\\x{feff}][^`]+)`")

// docsCmdInlineSkip are the inline code spans that start with tnctl but are no command a page asks
// anyone to run: the refusal the serve command prints. A release name (docsCmdInlineVersionRe) is
// skipped too.
var docsCmdInlineSkip = map[string]bool{
	"tnctl council serve runs only inside a TeknoirBot": true,
}

// docsCmdInlineVersionRe is an inline span that names a release, such as `tnctl v0.3.0` or
// `tnctl v0.0.9-beta`: a version, not a command.
var docsCmdInlineVersionRe = regexp.MustCompile(`^tnctl v[0-9]+\.[0-9]+(\.[0-9]+)?\S*$`)

// docsCmdHintWordRe is the plain word that follows a hint's verb: "lists its councils", "makes it the
// default", "lists them:".
var docsCmdHintWordRe = regexp.MustCompile(`^[a-z]+[.,:;]?$`)

// docsCmdCutHint cuts the clause tnctl's own hints end a council command with ("tnctl council roster
// list --bot scout lists its councils", "… --bot scout makes it the default", "… --templates lists each
// member's slug and scope"), so the span is checked as the command it names. It cuts only a tnctl
// council command, at a bare lists, makes or saves followed by a plain word, so a quoted "lists the
// pods" or a chat prompt is never cut. A bare lists, makes or saves followed by a plain word in a
// council command hides the flags after it; no council command takes such a positional argument.
func docsCmdCutHint(span string) string {
	toks := docsCmdTokens(span)
	if len(toks) < 2 || toks[1] != "council" {
		return span
	}
	for k := 2; k+1 < len(toks); k++ {
		switch toks[k] {
		case "lists", "makes", "saves":
			if docsCmdHintWordRe.MatchString(toks[k+1]) {
				return strings.Join(toks[:k], " ")
			}
		}
	}
	return span
}

// docsCmdInline returns the commands a run of prose lines quotes in inline code (docsCmdInlineRe).
// A span's line breaks and blanks are collapsed (a span wrapped over two lines is one command), and a
// non-breaking or zero-width space in it is reported (docsCmdFixOddSpaces). A \| stays as written:
// MkDocs shows it with its backslash in a code span, in a table cell too, and a bare | inside
// backticks does not end a table cell there. A span that names a release, or is a refusal text, is no
// command, and a hint tnctl prints is checked as the command it names (docsCmdCutHint). Its line
// number is the line the span starts on: startLine is the 1-based line number of lines[0]. The lines
// come as the page has them, so a span's own odd spaces are still in them.
func docsCmdInline(page string, startLine int, lines []string) []docsCmdLine {
	text := strings.Join(lines, "\n")
	var out []docsCmdLine
	for _, m := range docsCmdInlineRe.FindAllStringSubmatchIndex(text, -1) {
		fixed, odd := docsCmdFixOddSpaces(docsCmdShell{}, text[m[2]:m[3]])
		span := strings.Join(strings.Fields(fixed), " ")
		if docsCmdInlineSkip[span] || docsCmdInlineVersionRe.MatchString(span) {
			continue
		}
		span = docsCmdCutHint(span)
		out = append(out, docsCmdLine{page: page, line: startLine + strings.Count(text[:m[0]], "\n"), text: span, odd: odd})
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
// fence that is wrong (docsCmdFenceProblems), a tnctl command in an indented code block, which MkDocs
// shows as code but the parser never reads (docsCmdIndentProblems), and a page that yields no tnctl
// command at all, which means its commands are written the way the parser does not read them.
func docsCmdPage(page, markdown string) ([]docsCmdLine, []string) {
	msgs := docsCmdFenceProblems(page, markdown)
	msgs = append(msgs, docsCmdIndentProblems(page, markdown)...)
	lines := docsCmdExtract(page, markdown)
	if len(lines) == 0 {
		msgs = append(msgs, fmt.Sprintf("%s yields no tnctl command: are its commands outside ```bash blocks?", page))
	}
	return lines, msgs
}

// docsCmdIndentProblems reports each tnctl command a page shows in an indented code block, which
// MkDocs renders as code but docsCmdExtract never reads. Such a block is outside every fence (read
// as docsCmdExtract reads them, blockquotes too), after a blank line, a heading, or another line of
// the same block, and indented at least 4 columns past the container it is in: the page, 4 past the
// 4-column level a list item's marker is on (a marker indented 1 to 3 blanks is an item of the same
// list), 4 past a !!!, ??? or === opener, or the start of a blockquote's text, measured after its >
// markers (the container's base comes back when the blockquote ends). Only a line that starts a block (after a blank)
// leaves a container; a hanging continuation line indented 2 does not. A tab counts as 4 blanks; a
// non-breaking space counts as none. A "$ " prompt is dropped before the line is read.
func docsCmdIndentProblems(page, markdown string) []string {
	var msgs []string
	openRun, openDepth := "", 0
	prevBlank, inCode := true, false
	base := 0
	quoteBase, inQuote := 0, false // the base of the container a blockquote is in, while the scan is in one
	for i, row := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		fixed, _ := docsCmdFixOddSpaces(docsCmdShell{}, row)
		limit := -1
		if openRun != "" {
			limit = openDepth
		}
		rest, depth := docsCmdQuote(fixed, limit)
		if openRun != "" && depth < openDepth {
			openRun = "" // the blockquote ended the block, as in docsCmdExtract
			rest, depth = docsCmdQuote(fixed, -1)
		}
		run, info := docsCmdFence(strings.TrimLeft(rest, " \t")) // the fences as docsCmdExtract reads them
		src, _ := docsCmdQuote(row, depth)                       // the indent as Markdown counts it: a non-breaking space is no blank
		line := strings.ReplaceAll(src, "\t", "    ")
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if run != "" {
			switch {
			case openRun == "":
				openRun, openDepth = run, depth
			case run[0] == openRun[0] && len(run) >= len(openRun) && strings.TrimSpace(info) == "":
				openRun = ""
			}
			prevBlank, inCode = false, false
			continue
		}
		if openRun != "" {
			continue
		}
		switch {
		case depth > 0 && !inQuote:
			// A blockquote starts a block, and its lines are measured after their > markers: its
			// content starts at column 0 of its own, whatever container the blockquote is in. The
			// blanks before its first > are its indent in that container: a blockquote that starts a
			// block left of a list item's or admonition's content leaves it, as any block does.
			lead := row[:len(row)-len(strings.TrimLeft(row, " \t"))]
			if outer := len(strings.ReplaceAll(lead, "\t", "    ")); prevBlank && outer < base {
				base = outer - outer%4
			}
			quoteBase, base, inQuote, prevBlank = base, 0, true, true
		case depth == 0 && inQuote:
			// The blockquote ended: the lines are back in the list item or admonition it was in.
			base, inQuote = quoteBase, false
		}
		if trimmed == "" {
			prevBlank = true
			continue
		}
		if (prevBlank || inCode) && indent >= base+4 {
			inCode, prevBlank = true, false
			if t := strings.TrimPrefix(trimmed, "$ "); docsCmdHasCmd(t) {
				msgs = append(msgs, fmt.Sprintf("%s:%d: an indented code block holds a tnctl command: fence it as ```bash", page, i+1))
			}
			continue
		}
		startsBlock := prevBlank
		inCode, prevBlank = false, strings.HasPrefix(trimmed, "#") && indent < base+4 // a heading is one line: an indented line after it is code
		switch {
		case docsCmdListMarker(trimmed):
			// An item's content starts 4 past the 4-column level its marker is on: a marker
			// indented 1 to 3 blanks (a 2- or 3-space nested list) is an item of the same list.
			base = indent - indent%4 + 4
		case strings.HasPrefix(trimmed, "!!!") || strings.HasPrefix(trimmed, "???") || strings.HasPrefix(trimmed, "==="):
			base = indent + 4
		case startsBlock && indent < base:
			// Only a line that starts a block leaves the list item or admonition: a hanging
			// (lazy) continuation line indented two blanks stays in it.
			base = indent - indent%4
		}
	}
	return msgs
}

// docsCmdListMarker reports whether a trimmed line starts a list item: -, * or + and a blank, or a
// number, a dot and a blank.
func docsCmdListMarker(t string) bool {
	if len(t) > 1 && strings.ContainsRune("-*+", rune(t[0])) && t[1] == ' ' {
		return true
	}
	j := 0
	for j < len(t) && t[j] >= '0' && t[j] <= '9' {
		j++
	}
	return j > 0 && j+1 < len(t) && t[j] == '.' && t[j+1] == ' '
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
			name: "a value flag after a placeholder swallows a flag, which is still checked",
			page: bash("tnctl chat <bot> --thread --jsno"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--jsno"`},
		},
		{
			name: "a flag after a placeholder is checked",
			page: bash("tnctl chat <bot> --jsno"),
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
			// The commands inside a substitution are checked like any other: role list takes --json,
			// so --templates --json is the form to show (-o name here would be refused).
			name: "a command substitution",
			page: bash("tnctl council role get $(tnctl council role list --templates --json | jq -r '.available[0].slug')"),
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
			page: bash("tnctl council role get $(echo $(tnctl council role list --templates --json | jq -r '.available[0].slug' | head -1))"),
		},
		{
			name: "a backtick substitution",
			page: bash("tnctl council role get `tnctl council role list --templates --json | jq -r '.available[0].slug'`"),
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
			want: []string{`case.md:2: tnctl council role new: too many arguments: "extra"`},
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
			// MkDocs (Python-Markdown tables) shows a code span's \| with its backslash, and a bare |
			// inside backticks does not end the cell. These four replace the two escaped-pipe cases
			// that read \| as |.
			name: "an escaped pipe in a table cell",
			page: "| `tnctl council roster list\\|get --bot B` | x |\n",
			want: []string{`case.md:1: tnctl council roster: unknown subcommand "list\\"`},
		},
		{
			name: "a bare pipe in a table cell",
			page: "| `tnctl council roster list|get --bot B` | x |\n",
		},
		{
			name: "a bare pipe with a typo in a table cell",
			page: "| `tnctl council roster list|gett` | x |\n",
			want: []string{`case.md:1: tnctl council roster: unknown subcommand "gett"`},
		},
		{
			name: "an escaped pipe in a list item",
			page: "- `tnctl council roster add\\|remove <c> <m>`\n",
			want: []string{`case.md:1: tnctl council roster: unknown subcommand "add\\"`},
		},
		{
			name: "a span after a fence keeps its line",
			page: "```bash\ntnctl council role list\n```\nsee `tnctl council role list --nope`\n",
			want: []string{`case.md:4: tnctl council role list: unknown flag "--nope"`},
		},
		{
			name: "a span on a later line of prose after a fence",
			page: "intro `tnctl council role list`\n```bash\ntnctl council role list\n```\nmore\nsee `tnctl council role list --nope`\n",
			want: []string{`case.md:6: tnctl council role list: unknown flag "--nope"`},
		},
		{
			name: "a span inside a text fence",
			page: "```text\n`tnctl council rolee list`\n```\n",
		},
		// tnctl's own hint lines, quoted in inline code (docsCmdCutHint).
		{
			name: "a hint quoted inline",
			page: "`roster use nope` adds `tnctl council roster list --bot scout lists its councils`.\nA member adds `tnctl council role list --bot scout --templates lists the members scout can use`.\n",
			cmds: 2,
		},
		{
			name: "a hint quoted inline with a typo",
			page: "adds `tnctl council rostr list --bot scout lists its councils`.\n",
			want: []string{`case.md:1: tnctl council: unknown subcommand "rostr"`},
		},
		{
			name: "a quoted hint",
			page: "adds `tnctl council roster list --bot scout lists its councils` and `tnctl council role list --bot scout --templates lists the members scout can use` and `tnctl council role list -n teknoir-demo --bot scout --templates lists every member` and `tnctl council roster list --bot scout lists them`.\n",
		},
		{
			name: "a quoted hint of another verb",
			page: "prints `tnctl council roster use <council> --bot scout makes another council the default` and `tnctl council roster add <council> <member> --bot scout --as scout-council saves a copy with that member`.\n",
		},
		{
			name: "a quoted hint of every form",
			page: "says `tnctl council roster use scout-council --bot scout makes it the default`, `tnctl council role list --bot <bot> --templates lists each member's slug and scope`, `tnctl council role list --bot scout --templates lists each` and `tnctl council role list --templates lists them: part member`.\n",
		},
		{
			name: "a quoted hint with a typo",
			page: "adds `tnctl council roster lst --bot scout lists its councils`.\n",
			want: []string{`case.md:1: tnctl council roster: unknown subcommand "lst"`},
		},
		{
			name: "a hint with a bad flag",
			page: "adds `tnctl council roster list --bott scout lists its councils`.\n",
			want: []string{`case.md:1: tnctl council roster list: unknown flag "--bott"`},
		},
		{
			name: "a quoted word lists is no hint",
			page: "run `tnctl council role new \"X\" --job \"lists the pods\" --bot scout --nope`.\n",
			want: []string{`case.md:1: tnctl council role new: unknown flag "--nope"`},
		},
		{
			name: "a chat prompt is no hint",
			page: "run `tnctl chat scout lists the pods --jsno`.\n",
			want: []string{`case.md:1: tnctl chat: unknown flag "--jsno"`},
		},
		{
			name: "a hint verb before a flag is no hint",
			page: "run `tnctl council roster list lists --bot scout`.\n",
			want: []string{`case.md:1: tnctl council roster list: unknown subcommand "lists"`},
		},
		{
			name: "a hint's words inside a chat prompt cut nothing",
			page: bash("tnctl chat scout \"which command lists them\" --nope") +
				"run `tnctl chat scout \"which command lists them\" --nope`.\n",
			want: []string{
				`case.md:2: tnctl chat: unknown flag "--nope"`,
				`case.md:4: tnctl chat: unknown flag "--nope"`,
			},
		},
		{
			name: "a hint's ending in the middle of a bash line cuts nothing",
			page: bash("tnctl council roster list --bot scout lists its councils; tnctl council rostr x"),
			want: []string{
				`case.md:2: tnctl council roster list: unknown subcommand "lists"`,
				`case.md:2: tnctl council: unknown subcommand "rostr"`,
			},
		},
		{
			name: "a hint line: lists its councils",
			page: "names them: `tnctl council roster list --bot scout lists its councils`.\n",
		},
		{
			name: "a hint line: lists the members",
			page: "`tnctl council role list --bot scout --templates lists the members scout can use`\n",
		},
		{
			name: "a hint line from the help, with an apostrophe",
			page: "`tnctl council role list --bot <bot> --templates lists each member's slug and scope.`\n",
		},
		{
			name: "a hint line: lists the councils",
			page: "`tnctl council roster list --bot <bot> lists the councils`\n",
		},
		{
			name: "a hint line: makes another the default",
			page: "`tnctl council roster use <council> --bot scout makes another council the default`\n",
		},
		{
			name: "a hint line with a wrong command",
			page: "`tnctl council roster lst --bot scout lists its councils`\n",
			want: []string{`case.md:1: tnctl council roster: unknown subcommand "lst"`},
		},
		{
			name: "a hint line with a wrong flag",
			page: "`tnctl council role list --bto scout --templates lists the members scout can use`\n",
			want: []string{`case.md:1: tnctl council role list: unknown flag "--bto"`},
		},
		{
			name: "a quoted version",
			page: "runs `tnctl v0.3.0` and `tnctl v0.0.9-beta`.\n",
		},
		// A word of pipes alone inside a bracket (the guard on the subcommand alternatives).
		{
			name: "a doubled pipe inside a bracket",
			page: bash("tnctl council role [--bot <bot> || --teamspace] list"),
			want: []string{`case.md:2: tnctl council role: unknown subcommand "||"`},
		},
		{
			name: "a doubled pipe inside a bracket one level up",
			page: bash("tnctl council [--bot <bot> || --nope] role list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "||"`},
		},
		{
			name: "a tripled pipe inside a bracket",
			page: bash("tnctl council role [--help ||| --nope] list"),
			want: []string{`case.md:2: tnctl council role: unknown subcommand "|||"`},
		},
		// Here-documents.
		{
			name: "a backslash-quoted here-document delimiter",
			page: bash("tnctl council apply -f - <<\\EOF\n- name: x\nEOF\ntnctl council rolee list"),
			want: []string{`case.md:5: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a partly quoted here-document delimiter",
			page: bash("tnctl council apply -f - <<E'O'F\n- name: x\nEOF\ntnctl council rolee list"),
			want: []string{`case.md:5: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a here-document that never ends",
			page: bash("tnctl chat scout <<'EOF'\nhi\nEOF.\ntnctl council rolee list"),
			want: []string{`case.md:2: a here-document whose delimiter "EOF" never comes: the rest of the block is read as its body`},
		},
		{
			name: "two here-documents on one line",
			page: bash("cat <<A <<B\nx\nA\ntnctl council role list shows nothing\nB\ntnctl council rolee list"),
			want: []string{`case.md:7: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a bare << opens no here-document",
			page: bash("tnctl chat scout <<\ntnctl council rolee list"),
			want: []string{`case.md:3: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a shift in arithmetic opens no here-document",
			page: bash("(( x <<= 2 ))\n(( y = 1 << 2 ))\ntnctl council rolee list"),
			want: []string{`case.md:4: tnctl council: unknown subcommand "rolee"`},
		},
		{
			// A here-document in a list item (an indented delimiter), the <<- form with a tab-indented
			// body, and one a closing fence ends: the next block is read again. The unended
			// here-document of the third block is reported on its own line (16).
			name: "a here-document in a list item, with <<-, and one a fence ends",
			page: "1. Apply:\n\n    ```bash\n    tnctl council apply -f - <<'EOF'\n    - name: triage --nope\n    EOF\n    tnctl council rolee list\n    ```\n" +
				bash("cat <<-EOF | tnctl council apply -f -\n\t- name: x --nope\n\tEOF\ntnctl council rostr list") +
				bash("tnctl council apply -f - <<'EOF'\n- name: x") +
				bash("tnctl council rol list"),
			want: []string{
				`case.md:7: tnctl council: unknown subcommand "rolee"`,
				`case.md:13: tnctl council: unknown subcommand "rostr"`,
				`case.md:16: a here-document whose delimiter "EOF" never comes: the rest of the block is read as its body`,
				`case.md:20: tnctl council: unknown subcommand "rol"`,
			},
		},
		// Console transcripts: only the "$ " lines are commands.
		{
			name: "transcript output is no command",
			page: "```console\n$ tnctl council roster use nope --bot scout\nError: There is no council 'nope' for scout.\ntnctl council roster list --bot scout lists its councils\nRun the command with --help for usage and examples.\n```\n",
			cmds: 1,
		},
		{
			name: "transcript output is no continuation",
			page: "```console\n$ tnctl council role list --bot scout\n  - analyst --x\n```\n",
			cmds: 1,
		},
		{
			name: "a PS2 continuation",
			page: "```console\n$ tnctl council role new \"Stream watcher\" \\\n>   --job \"x\" --bot scout --nope\n```\n",
			want: []string{`case.md:2: tnctl council role new: unknown flag "--nope"`},
		},
		{
			name: "a PS2 here-document ends at > EOF",
			page: "```console\n$ tnctl chat scout <<'EOF'\n> why --x\n> EOF\n$ tnctl council rolee list\n```\n",
			want: []string{`case.md:5: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a $ line in a here-document starts no transcript",
			page: bash("tnctl chat scout <<'EOF'\n$ tnctl council role list\nEOF\ntnctl council rolee list"),
			want: []string{`case.md:5: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a console fence without prompts is read",
			page: "```console\ntnctl council rolee list\n```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "zsh and shell-session fences",
			page: "```zsh\ntnctl council rolee list\n```\n```shell-session\n$ tnctl council rostr list\n```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`, `case.md:5: tnctl council: unknown subcommand "rostr"`},
		},
		// Fences.
		{
			name: "a backtick info string is no fence",
			page: "```` ```bash ```` blocks.\n\n```bash\ntnctl council rolee list\n```\n",
			want: []string{`case.md:4: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a fence in a blockquote",
			page: "> ```bash\n> tnctl council rolee list\n> ```\n",
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a fence in a nested blockquote",
			page: "> > ```bash\n> > tnctl council role list --nope\n> > ```\n",
			want: []string{`case.md:2: tnctl council role list: unknown flag "--nope"`},
		},
		{
			name: "a prompt line of a text transcript is no fence",
			page: "```text\ntokens today: 1 of 5\n> ```\n```\n\n```bash\ntnctl council role list\n```\n",
			cmds: 1,
		},
		{
			name: "a blockquote ends the fence it opened",
			page: "> ```bash\n> tnctl council role list\n\n```bash\ntnctl council role list\n```\n",
			want: []string{"case.md:1: the fence never closes"},
			cmds: 2,
		},
		{
			name: "a blockquote that ends its fence leaves the text after it",
			page: "> ```bash\n> tnctl council role list\n\n```text\ntnctl council rostr list\n```\n",
			want: []string{"case.md:1: the fence never closes"},
			cmds: 1,
		},
		// The command lines the parser cuts out of a block.
		{
			name: "a spaced brace alternative cuts nothing",
			page: bash("tnctl council role new <name> --job <text> {--bot <bot> | --teamspace} [--dry-runn]"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "[--dry-runn]"`},
		},
		{
			name: "a brace group is no synopsis",
			page: bash("{ echo hi; tnctl council rolee list; }"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a command after a synopsis group",
			page: bash("tnctl chat <bot> (--thread <id> | --new) && tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a command after a brace synopsis group",
			page: bash("tnctl council role new <name> --job <text> {--bot <bot> | --teamspace} && tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a glued json on the export command",
			page: bash("tnctl council role export triage --bot scout -ojson\n" +
				"tnctl council role export triage --bot scout --output=json\n" +
				"tnctl council role export <name> --bot <bot> [-o json]\n" +
				"tnctl council role export <name> --bot <bot> [--output json]"),
			want: []string{
				"case.md:2: tnctl council role export: -o json is refused here (export prints YAML only; use council role get -o json)",
				"case.md:3: tnctl council role export: -o json is refused here (export prints YAML only; use council role get -o json)",
				"case.md:4: tnctl council role export: -o json is refused here (export prints YAML only; use council role get -o json)",
				"case.md:5: tnctl council role export: -o json is refused here (export prints YAML only; use council role get -o json)",
			},
		},
		{
			name: "--output on a command that takes --json",
			page: bash("tnctl council role list --bot scout --output json"),
			want: []string{`case.md:2: tnctl council role list: -o is ignored here, use --json`},
		},
		{
			name: "a word before a double dash is counted",
			page: bash("tnctl council roster use ops scout -- x"),
			want: []string{`case.md:2: tnctl council roster use: too many arguments: "scout"`},
		},
		{
			name: "the words after a double dash are not counted",
			page: bash("tnctl council roster use ops -- x y z"),
		},
		{
			name: "a redirect after a tab",
			page: bash("tnctl council role list --bot scout\t>x.txt"),
		},
		{
			name: "an escaped quote in quotes, a quote in a substitution, an open substitution",
			page: bash("tnctl chat scout \"say \\\" now\" --jsno\ntnctl council role get $(echo ')') --nope\n" +
				"tnctl council role get $(tnctl council role list --json; tnctl council rolee list"),
			want: []string{
				`case.md:2: tnctl chat: unknown flag "--jsno"`,
				`case.md:3: tnctl council role get: unknown flag "--nope"`,
				`case.md:4: a quote or $( that never closes`,
			},
		},
		{
			name: "a quoted prompt over three lines",
			page: bash("tnctl chat scout \"Check the pods on arm-2\nand restart the ones that crash,\n" +
				"then report back\" --jsno"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--jsno"`},
			cmds: 1,
		},
		{
			name: "a bang, time, a spaced subshell and a loop after a pipe",
			page: bash("! tnctl council rolee list\ntime tnctl council rostr list\n( tnctl council rol list )\n" +
				"tnctl council role list --json | while read r; do tnctl council rolee get \"$r\"; done"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolee"`,
				`case.md:3: tnctl council: unknown subcommand "rostr"`,
				`case.md:4: tnctl council: unknown subcommand "rol"`,
				`case.md:5: tnctl council: unknown subcommand "rolee"`,
			},
		},
		{
			name: "a wrapper's options, numbers and {} are skipped, nothing else",
			page: bash("watch -n 5 tnctl council rolee list\nxargs -I {} tnctl council rostr get {}\nsudo mv tnctl /usr/local/bin/"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolee"`,
				`case.md:3: tnctl council: unknown subcommand "rostr"`,
			},
		},
		{
			// xargs takes the string of --replace glued (--replace={}), so a {} after it would be the
			// command in a shell. The walk is lenient there: a page that writes it means the tnctl after.
			name: "a {} before the command is skipped",
			page: bash("xargs --replace {} tnctl council rolee get {}"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "every other keyword before a command",
			page: bash("if false; then tnctl council rolea list; fi\n" +
				"if false; then :; elif tnctl council roleb list; then :; fi\n" +
				"if false; then :; else tnctl council rolec list; fi\n" +
				"while tnctl council roled list; do sleep 1; done\n" +
				"until tnctl council rolee list; do sleep 1; done\n" +
				"command tnctl council rolef list\n" +
				"{ tnctl council roleg list; }"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolea"`,
				`case.md:3: tnctl council: unknown subcommand "roleb"`,
				`case.md:4: tnctl council: unknown subcommand "rolec"`,
				`case.md:5: tnctl council: unknown subcommand "roled"`,
				`case.md:6: tnctl council: unknown subcommand "rolee"`,
				`case.md:7: tnctl council: unknown subcommand "rolef"`,
				`case.md:8: tnctl council: unknown subcommand "roleg"`,
			},
		},
		{
			name: "every other wrapper before a command",
			page: bash("sudo tnctl council roleh list\n" +
				"env COUNCIL=ops tnctl council rolei list\n" +
				"nohup tnctl council rolej list &\n" +
				"exec tnctl council rolek list\n" +
				"timeout 10 tnctl council rolel list"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "roleh"`,
				`case.md:3: tnctl council: unknown subcommand "rolei"`,
				`case.md:4: tnctl council: unknown subcommand "rolej"`,
				`case.md:5: tnctl council: unknown subcommand "rolek"`,
				`case.md:6: tnctl council: unknown subcommand "rolel"`,
			},
		},
		{
			name: "a wrapper option with its value apart",
			page: bash("tnctl council role list --bot scout --json | xargs -I % tnctl council rolee get % --bot scout\n" +
				"sudo -u anders tnctl council rostr list\n" +
				"timeout -s KILL 10 tnctl council rolee list\n" +
				"env -u FOO tnctl council rostr list\n" +
				"watch -n 0.5 tnctl council rolee list\n" +
				"tnctl council role list --bot scout --json | xargs -d '\\n' -n 1 tnctl council role get --bot scout"),
			want: []string{
				`case.md:2: tnctl council: unknown subcommand "rolee"`,
				`case.md:3: tnctl council: unknown subcommand "rostr"`,
				`case.md:4: tnctl council: unknown subcommand "rolee"`,
				`case.md:5: tnctl council: unknown subcommand "rostr"`,
				`case.md:6: tnctl council: unknown subcommand "rolee"`,
			},
		},
		{
			name: "a wrapper option value is no command",
			page: bash("sudo -u root mv tnctl /usr/local/bin/\nsudo mv tnctl /usr/local/bin/\ntnctl council role list"),
			cmds: 1,
		},
		{
			name: "an assignment before a wrapper",
			page: bash("FOO=1 sudo tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a duration before the command",
			page: bash("timeout 10s tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a fractional interval before the command",
			page: bash("watch -n 0.5 tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a case pattern",
			page: bash("case \"$1\" in\n  list) tnctl council rolee list ;;\nesac"),
			want: []string{`case.md:3: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a wrapper's word that is no number",
			page: bash("sudo mv tnctl /usr/local/bin/"),
		},
		{
			name: "a typographic dash owns a value",
			page: bash("tnctl council role get triage \u2014bott scout"),
			want: []string{`case.md:2: tnctl council role get: unknown flag "` + "\u2014" + `bott"`},
		},
		{
			name: "odd spaces in a comment, on an indented line, and of every kind",
			page: bash("tnctl council role list # all\u00a0roles\ntnctl council roster add <name> <member>\n    --bot\u00a0<bot> [--scope <scope>]\n" +
				"tnctl\u202fcouncil role list\ntnctl\u2007council role list\ntnctl\u200bcouncil role list"),
			want: []string{
				`case.md:3: a non-breaking or zero-width space: the shell reads it as part of a word`,
				`case.md:5: a non-breaking or zero-width space: the shell reads it as part of a word`,
				`case.md:6: a non-breaking or zero-width space: the shell reads it as part of a word`,
				`case.md:7: a non-breaking or zero-width space: the shell reads it as part of a word`,
			},
		},
		{
			name: "a typographic dash behind a value",
			page: bash("tnctl council role new <name> --job <text> [--how <text>|\u2013how-file <file>]"),
			want: []string{`case.md:2: tnctl council role new: unknown flag "` + "\u2013" + `how-file"`},
		},
		{
			name: "a non-breaking space after tnctl in inline code",
			page: "Run `tnctl\u00a0council rolee list`.\n",
			want: []string{
				`case.md:1: a non-breaking or zero-width space: the shell reads it as part of a word`,
				`case.md:1: tnctl council: unknown subcommand "rolee"`,
			},
			cmds: 1,
		},
		{
			name: "a non-breaking space inside inline code",
			page: "Run `tnctl council\u00a0role list --nope`.\n",
			want: []string{
				`case.md:1: a non-breaking or zero-width space: the shell reads it as part of a word`,
				`case.md:1: tnctl council role list: unknown flag "--nope"`,
			},
		},
		{
			name: "a zero-width space in inline code after a stray backtick",
			page: "A ` sign, then `tnctl council\u200brole list`.\n",
			want: []string{`case.md:1: a non-breaking or zero-width space: the shell reads it as part of a word`},
		},
		{
			name: "a non-breaking space on the second line of a wrapped inline span",
			page: "Run `tnctl council\nrole\u00a0 list`.\n",
			want: []string{`case.md:1: a non-breaking or zero-width space: the shell reads it as part of a word`},
		},
		{
			name: "a non-breaking space in prose and inside quotes in inline code",
			page: "Run `tnctl chat scout \"hi\u00a0there\"` now.\n",
			cmds: 1,
		},
		{
			name: "a pipe with a blank after it only",
			page: bash("tnctl council role get triage -o json| jq ."),
		},
		{
			name: "a command after a pipe with a blank after it only",
			page: bash("tnctl council role list --json| tnctl council rolee list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a # inside quotes before a backslash",
			page: bash("tnctl chat scout \"issue #12\" \\\n  --jsno"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--jsno"`},
		},
		{
			name: "a # inside a word before a backslash",
			page: bash("tnctl chat scout a#b \\\n  --jsno"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--jsno"`},
		},
		{
			name: "an assignment from a substitution",
			page: bash("id=$(tnctl chat threads scout -o json | jq -r '.[0].id')"),
			want: []string{`case.md:2: tnctl chat threads: -o is ignored here, use --json`},
		},
		{
			name: "a typo inside a quoted substitution",
			page: bash("tnctl chat scout --thread \"$(tnctl chat thread scout --json | jq -r '.[0].id')\" --follow"),
			want: []string{`case.md:2: tnctl chat: unknown subcommand "thread"`},
		},
		{
			name: "a ) in a quoted prompt inside a substitution",
			page: bash(`answer=$(tnctl chat scout "pick one: 1) restart 2) skip" --json)`),
			cmds: 1,
		},
		{
			name: "an apostrophe in a prompt before a substitution",
			page: bash(`tnctl chat scout "what's new" --thread "$(tnctl chat thread scout --json | jq -r '.[0].id')"`),
			want: []string{`case.md:2: tnctl chat: unknown subcommand "thread"`},
		},
		{
			name: "a substitution inside single quotes is text",
			page: bash(`tnctl chat scout "what's up?" --json; echo '$(tnctl council rolee list)'`),
			cmds: 1,
		},
		{
			name: "an arithmetic expansion around a substitution",
			page: bash(`n=$(( $(tnctl council role list --bot scout --json | jq length) + 1 ))`),
			cmds: 1,
		},
		{
			name: "a bot placeholder as an argument",
			page: bash("tnctl council roster use <name> <bot>\ntnctl council roster use ops <bot>\ntnctl council roster add <name> <member> <bot>"),
			want: []string{
				`case.md:2: tnctl council roster use: too many arguments: "<bot>"`,
				`case.md:3: tnctl council roster use: too many arguments: "<bot>"`,
				`case.md:4: tnctl council roster add: too many arguments: "<bot>"`,
			},
		},
		{
			name: "a placeholder that is no one word leaves the count open",
			page: bash("tnctl council role new <name> [extra...]\ntnctl council role <command> --bot <bot>\ntnctl council roster apply -f <file|-> <x|y>\ntnctl council roster use <name> $BOT"),
		},
		{
			name: "a placeholder with spaces is one value",
			page: bash("tnctl council role new <name> --job <one line job> --bot <bot name>\n" +
				"tnctl council role new Scout --job <one line job> --bot scout\n" +
				"tnctl council roster add <member> --bot <bot> --scope <bot or teamspace>\n" +
				"tnctl council roster use <council name> <bot name>"),
			want: []string{`case.md:5: tnctl council roster use: too many arguments: "<bot name>"`},
		},
		{
			name: "an input redirect is no placeholder",
			page: bash("tnctl council roster apply -f - < council.yaml\ntnctl council roster apply -f - <<EOF\nEOF"),
		},
		{
			name: "a placeholder before the subcommand",
			page: bash("tnctl council roster <name> add <member> --bot <bot>\ntnctl council <name> roster add <member>\ntnctl council roster 'ops scout' add <member>"),
			want: []string{
				`case.md:2: tnctl council roster: a placeholder before the subcommand "add"`,
				`case.md:3: tnctl council: a placeholder before the subcommand "roster"`,
				`case.md:4: tnctl council roster: a placeholder before the subcommand "add"`,
			},
		},
		{
			name: "a group placeholder before a subcommand its groups share",
			page: bash("tnctl council <group> apply -f <file>\ntnctl council <role|roster> apply -f <file|->\n" +
				"tnctl council <role or roster> apply -f <file>\ntnctl council <command> apply -f council.yaml"),
		},
		{
			name: "a name placeholder before a subcommand its groups share",
			page: bash("tnctl council <name> apply -f council.yaml\ntnctl council <council> apply -f council.yaml"),
			want: []string{
				`case.md:2: tnctl council: a placeholder before the subcommand "apply"`,
				`case.md:3: tnctl council: a placeholder before the subcommand "apply"`,
			},
		},
		{
			// TestDocsCouncilPlaceholderBeforeCobraCommands pins this on a tree with and without help
			// and completion; here it holds in any test order on rootCmd.
			name: "a placeholder before cobra's help or completion is flagged",
			page: bash("tnctl <command> help\ntnctl <command> completion\ntnctl <command> --bot -h help --no-descriptions"),
			want: []string{
				`case.md:2: tnctl: a placeholder before the subcommand "help"`,
				`case.md:3: tnctl: a placeholder before the subcommand "completion"`,
				`case.md:4: tnctl: a placeholder before the subcommand "help"`,
			},
		},
		{
			name: "a group placeholder's flags are those of the commands it may name",
			page: bash("tnctl council <role|roster> apply -f <file> --bot <bot>\ntnctl council <group> apply -f council.yaml --json\n" +
				"tnctl council <role|roster> apply -f council.yaml --dry-run --yes -o json\ntnctl <group> add -f <file> --replace\n" +
				"tnctl <command> logs <robot> <podNamespace>/<pod> -c <container> --previous"),
			want: []string{
				`case.md:2: tnctl council: unknown flag "--bot"`,
				`case.md:3: tnctl council: unknown flag "--json"`,
			},
		},
		{
			name: "flags, a variable or an elision before the subcommand",
			page: bash("tnctl [flags] council role list\ntnctl council [flags] role list\ntnctl council <global-flags> role list\ntnctl council $OPTS roster list\ntnctl council \u2026 role list"),
		},
		{
			name: "a lone em dash in a prompt",
			page: bash("tnctl chat scout what changed \u2014 and why"),
		},
		{
			name: "a lone dash is a word, not a flag",
			page: bash("tnctl council role list \u2014 --bot scout"),
			want: []string{`case.md:2: tnctl council role list: unknown subcommand "` + "\u2014" + `"`},
		},
		{
			name: "a lone dash is a flag's value",
			page: bash("tnctl council role list --bot \u2014"),
		},
		{
			name: "a lone dash in the subcommand slot is refused",
			page: bash("tnctl council \u2014 role list"),
			want: []string{`case.md:2: tnctl council: unknown subcommand "` + "\u2014" + `"`},
		},
		{
			name: "an escaped backslash ends no line",
			page: bash("tnctl chat scout foo\\\\\ntnctl council rolee list"),
			want: []string{`case.md:3: tnctl council: unknown subcommand "rolee"`},
			cmds: 2,
		},
		{
			name: "three backslashes continue the line",
			page: bash("tnctl chat scout foo\\\\\\\n  --jsno"),
			want: []string{`case.md:2: tnctl chat: unknown flag "--jsno"`},
			cmds: 1,
		},
		{
			name: "a << inside a quote the line above opened",
			page: bash("tnctl chat scout \"compare a\nand b << c\"\ntnctl council rolee list"),
			want: []string{`case.md:4: tnctl council: unknown subcommand "rolee"`},
		},
		{
			name: "a non-breaking space inside a quote the line above opened",
			page: bash("tnctl chat scout \"why does\namr-02\u00a0restart?\""),
		},
		{
			name: "a # inside a quote the line above opened, then a here-document",
			page: bash("tnctl chat scout \"why\n#1 and 2\" <<EOF\ntnctl council rolee list\nEOF"),
		},
		{
			name: "a non-breaking space after a quote closes on the next line",
			page: bash("tnctl chat scout \"why\ndoes\"\u00a0--nope"),
			want: []string{"case.md:2: a non-breaking or zero-width space: the shell reads it as part of a word", `case.md:2: tnctl chat: unknown flag "--nope"`},
		},
		{
			name: "an open quote above a tnctl line with a here-document",
			page: bash("tnctl chat scout \"oops\ntnctl chat scout <<EOF\ntnctl council rolee list\nEOF"),
			want: []string{"case.md:2: a quote or $( that never closes"},
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

// TestDocsCouncilSubHasFlagSkipsCobraCommands pins, on a tree of its own, that docsCmdSubHasFlag never
// counts the flags of cobra's help and completion commands of the root. cobra adds those to the real
// root only when a command runs, so a check on rootCmd would depend on the order of the tests.
func TestDocsCouncilSubHasFlagSkipsCobraCommands(t *testing.T) {
	root := &cobra.Command{Use: "tnctl"}
	comp := &cobra.Command{Use: "completion"}
	comp.Flags().Bool("no-descriptions", false, "")
	help := &cobra.Command{Use: "help", Run: func(*cobra.Command, []string) {}}
	help.Flags().Bool("only-on-help", false, "")
	council := &cobra.Command{Use: "council"}
	council.Flags().Bool("bot-flag", false, "")
	deep := &cobra.Command{Use: "help", Run: func(*cobra.Command, []string) {}} // a real subcommand that happens to be called help
	deep.Flags().Bool("deep-help-flag", false, "")
	council.AddCommand(deep)
	root.AddCommand(comp, help, council)
	for _, tok := range []string{"--no-descriptions", "--only-on-help"} {
		if docsCmdSubHasFlag(root, tok) {
			t.Errorf("docsCmdSubHasFlag(root, %q) = true: cobra's own help/completion command counted", tok)
		}
	}
	for _, tok := range []string{"--bot-flag", "--deep-help-flag"} {
		if !docsCmdSubHasFlag(root, tok) {
			t.Errorf("docsCmdSubHasFlag(root, %q) = false: a real subcommand's flag not counted", tok)
		}
	}
}

// TestDocsCouncilPlaceholderBeforeCobraCommands pins, on two trees of their own, one with cobra's help
// and completion commands and one without, that a word placeholder in the root's subcommand slot
// followed by help or completion is a "placeholder before the subcommand" on both: tnctl always has
// those two, and cobra adds them to the real root only when a command runs, so the answer must not
// depend on the order of the tests.
func TestDocsCouncilPlaceholderBeforeCobraCommands(t *testing.T) {
	tree := func(cobraOwn bool) *cobra.Command {
		root := &cobra.Command{Use: "tnctl"}
		council := &cobra.Command{Use: "council"}
		roster := &cobra.Command{Use: "roster"}
		roster.AddCommand(&cobra.Command{Use: "add", Run: func(*cobra.Command, []string) {}})
		council.AddCommand(roster, &cobra.Command{Use: "list", Run: func(*cobra.Command, []string) {}})
		// A root command named like a subcommand of cobra's completion: a placeholder before it is
		// still flagged once completion exists, since completion is never a child the slot stands for.
		root.AddCommand(council, &cobra.Command{Use: "bash", Run: func(*cobra.Command, []string) {}})
		if cobraOwn {
			root.InitDefaultHelpCmd()
			root.InitDefaultCompletionCmd()
			if docsCmdSub(root, "help") == nil || docsCmdSub(root, "completion") == nil {
				t.Fatal("the tree has no help or completion command")
			}
		}
		return root
	}
	cases := []struct {
		line string
		want []string
	}{
		{line: "tnctl <command> help", want: []string{`case.md:2: tnctl: a placeholder before the subcommand "help"`}},
		{line: "tnctl <command> completion bash", want: []string{`case.md:2: tnctl: a placeholder before the subcommand "completion"`}},
		{line: `tnctl "x" help`, want: []string{`case.md:2: tnctl: a placeholder before the subcommand "help"`}},
		{line: "tnctl help <command>"},
		{line: "tnctl <help> council", want: []string{`case.md:2: tnctl: a placeholder before the subcommand "council"`}},
		{line: "tnctl <command> council", want: []string{`case.md:2: tnctl: a placeholder before the subcommand "council"`}},
		{line: "tnctl <command> bash", want: []string{`case.md:2: tnctl: a placeholder before the subcommand "bash"`}},
		{line: "tnctl council <command> list", want: []string{`case.md:2: tnctl council: a placeholder before the subcommand "list"`}},
		{line: "tnctl council roster <name> add", want: []string{`case.md:2: tnctl council roster: a placeholder before the subcommand "add"`}},
	}
	for _, cobraOwn := range []bool{false, true} {
		root := tree(cobraOwn)
		for _, tc := range cases {
			lines, msgs := docsCmdPage("case.md", "```bash\n"+tc.line+"\n```\n")
			got := msgs
			for _, l := range lines {
				got = append(got, docsCmdCheck(root, l)...)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("help and completion %v: %s: got %q, want %q", cobraOwn, tc.line, got, tc.want)
			}
		}
	}
}

// TestDocsCouncilPlaceholderFlagsOfItsCommands pins, on a tree of its own, that the flags of a line
// whose group placeholder stands for some commands (council <group> apply) are checked against those
// commands and their subcommands: a descendant's flag counts, a hidden flag does not.
func TestDocsCouncilPlaceholderFlagsOfItsCommands(t *testing.T) {
	run := func(*cobra.Command, []string) {}
	root := &cobra.Command{Use: "tnctl"}
	council := &cobra.Command{Use: "council"}
	role, roster := &cobra.Command{Use: "role"}, &cobra.Command{Use: "roster"}
	roleApply := &cobra.Command{Use: "apply", Run: run}
	roleApply.Flags().StringP("file", "f", "", "")
	roleApply.Flags().Bool("secret", false, "")
	_ = roleApply.Flags().MarkHidden("secret")
	rosterApply := &cobra.Command{Use: "apply", Run: run}
	rosterApply.Flags().StringP("file", "f", "", "")
	deep := &cobra.Command{Use: "deep", Run: run}
	deep.Flags().Bool("deep", false, "")
	rosterApply.AddCommand(deep)
	role.AddCommand(roleApply)
	roster.AddCommand(rosterApply)
	// council apply is a command of its own too, as in tnctl, so apply after a group placeholder is the
	// subcommand the slot rule looks at.
	councilApply := &cobra.Command{Use: "apply", Run: run}
	councilApply.Flags().StringP("file", "f", "", "")
	council.AddCommand(role, roster, councilApply)
	council.Flags().Bool("council-only", false, "")
	root.AddCommand(council)
	cases := []struct {
		line string
		want []string
	}{
		{line: "tnctl council <group> apply -f x --deep"},
		{line: "tnctl council <group> apply -f x --secret", want: []string{`case.md:2: tnctl council: unknown flag "--secret"`}},
		{line: "tnctl council <role|roster> apply -f x --council-only"},
	}
	for _, tc := range cases {
		lines, msgs := docsCmdPage("case.md", "```bash\n"+tc.line+"\n```\n")
		got := msgs
		for _, l := range lines {
			got = append(got, docsCmdCheck(root, l)...)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.line, got, tc.want)
		}
	}
}

// TestDocsCouncilPageProblems checks docsCmdPage on synthetic pages: a page in the docs must show its
// commands the way the parser reads them, so a page that yields none, or whose fence is wrong, is a
// finding even when nothing in it would be refused.
func TestDocsCouncilPageProblems(t *testing.T) {
	const indentMsg = ": an indented code block holds a tnctl command: fence it as ```bash"
	const noneMsg = "case.md yields no tnctl command: are its commands outside ```bash blocks?"
	const tail = "\n```bash\ntnctl council role list\n```\n" // a block that yields a command
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
		// Indented code blocks: MkDocs shows them as code, the parser never reads them.
		{
			name: "an indented code block",
			page: "Run:\n\n    tnctl council rolee list\n",
			want: []string{"case.md:3" + indentMsg, noneMsg},
		},
		{
			name: "a list item's indented block",
			page: "- Read it:\n\n        tnctl council rolee get stream-watcher --bot scout\n",
			want: []string{"case.md:3" + indentMsg, noneMsg},
		},
		{
			name: "an admonition's indented block",
			page: "!!! note\n    Run:\n\n        tnctl council roster list --bot scout\n",
			want: []string{"case.md:4" + indentMsg, noneMsg},
		},
		{
			name: "every line of an indented block",
			page: "Run:\n\n    tnctl council role list\n    tnctl council rolee list\n" + tail,
			want: []string{"case.md:3" + indentMsg, "case.md:4" + indentMsg},
		},
		{
			name: "a tab-indented block",
			page: "Run:\n\n\ttnctl council role list\n",
			want: []string{"case.md:3" + indentMsg, noneMsg},
		},
		{
			name: "a prompt in an indented block",
			page: "1. Export it:\n\n        $ tnctl council role export stream-watcher --bot scout > stream-watcher.yaml\n\n2. Apply it.\n\n" +
				"```bash\ntnctl council apply -f stream-watcher.yaml\n```\n",
			want: []string{"case.md:3" + indentMsg},
		},
		{
			name: "an admonition's prose",
			page: "!!! note\n\n    tnctl asks on a terminal; --yes gives it up front.\n" + tail,
		},
		{
			name: "a list item's paragraph",
			page: "- Read it:\n\n    tnctl council role get x is the command.\n" + tail,
		},
		{
			name: "an indented output block",
			page: "Output:\n\n    SLUG  NAME\n" + tail,
		},
		{
			name: "a lazy continuation",
			page: "Run\n    tnctl council rolee list\n" + tail,
		},
		{
			name: "a fenced block in a list item",
			page: "1. Export it:\n\n    ```bash\n    tnctl council role export stream-watcher --bot scout\n    ```\n",
		},
		{
			name: "a hanging list item's 4-space paragraph",
			page: "- Without --yes nothing is removed, and the\n  command exits 0.\n\n    tnctl prints what it would do on stderr.\n" + tail,
		},
		{
			name: "a hanging list item's 8-space block",
			page: "- Read it, the\n  command is:\n\n        tnctl council rolee list\n" + tail,
			want: []string{"case.md:4" + indentMsg},
		},
		{
			name: "a top-level paragraph after a list, then a block",
			page: "- a\n\n  b\n\n    tnctl council rolee list\n" + tail,
			want: []string{"case.md:5" + indentMsg},
		},
		{
			name: "a blockquote's indented block",
			page: "> Run:\n>\n>     tnctl council rolee list\n" + tail,
			want: []string{"case.md:3" + indentMsg},
		},
		{
			name: "a block right after a heading",
			page: "## Run\n    tnctl council rolee list\n" + tail,
			want: []string{"case.md:2" + indentMsg},
		},
		{
			name: "a tab's indented block",
			page: "=== \"bot\"\n\n    Run:\n\n        tnctl council rolee list\n" + tail,
			want: []string{"case.md:5" + indentMsg},
		},
		{
			name: "a list item's text after its block",
			page: "1. Export it:\n\n        tnctl council role list\n\n    Then apply it.\n\n2. Next.\n" + tail,
			want: []string{"case.md:3" + indentMsg},
		},
		{
			name: "a non-breaking-space indent is a paragraph",
			page: "Run:\n\n\u00a0\u00a0\u00a0\u00a0tnctl council rolee list\n" + tail,
		},
		{
			name: "a quoted fence",
			page: "> ```bash\n> tnctl council role list\n> ```\n\nok\n",
		},
		{
			name: "a list item's paragraph after its blockquote",
			page: "1. Apply it. The hub answers:\n\n    > council_edit_not_allowed\n\n    `tnctl council apply` exits 6 then.\n\n2. Next.\n" + tail,
		},
		{
			name: "an admonition's paragraph after its blockquote",
			page: "!!! quote \"The hub\"\n\n    > not allowed\n\n    The hub refuses, and `tnctl council role new` exits 6.\n" + tail,
		},
		{
			name: "a list item's indented block after its blockquote",
			page: "1. Apply it:\n\n    > council_edit_not_allowed\n\n        tnctl council rolee list\n" + tail,
			want: []string{"case.md:5" + indentMsg},
		},
		{
			name: "a 2-space blockquote leaves its list item",
			page: "- Read it:\n\n  > Only a person reads members.\n\n      tnctl council rolee list\n" + tail,
			want: []string{"case.md:5" + indentMsg},
		},
		{
			name: "a 2-space blockquote's paragraph",
			page: "- Read it:\n\n  > Only a person reads members.\n\n  `tnctl council apply` exits 6.\n" + tail,
		},
		{
			name: "an indented block that opens a blockquote",
			page: "Text\n>     tnctl council rolee list\n" + tail,
			want: []string{"case.md:2" + indentMsg},
		},
		{
			name: "a blockquote's indented block inside a list item",
			page: "1. Apply it:\n\n    > Run:\n    >\n    >     tnctl council rolee list\n" + tail,
			want: []string{"case.md:5" + indentMsg},
		},
		{
			name: "a list item's blockquote that opens with an indented block",
			page: "1. Apply it:\n\n    >     tnctl council rolee list\n" + tail,
			want: []string{"case.md:3" + indentMsg},
		},
		{
			name: "a blockquote's paragraph inside a list item",
			page: "1. Apply it:\n\n    > Run:\n    >\n    > tnctl council role list prints it.\n" + tail,
		},
		{
			name: "a 2-space nested item's indented block",
			page: "- a\n  - b\n\n        tnctl council rolee list\n" + tail,
			want: []string{"case.md:4" + indentMsg},
		},
		{
			name: "a 3-space nested item's indented block",
			page: "1. a\n   - b\n\n         tnctl council rolee list\n" + tail,
			want: []string{"case.md:4" + indentMsg},
		},
		{
			name: "a 2-space nested item's paragraph",
			page: "- a\n  - b\n\n    tnctl council role list prints it.\n" + tail,
		},
		{
			name: "a nested item's indented block inside an item",
			page: "1. Step\n\n    text\n\n    - sub\n      - subsub\n\n            tnctl council rolee list\n" + tail,
			want: []string{"case.md:8" + indentMsg},
		},
		{
			name: "an indented block after a blockquote ended its fence",
			page: "> ```bash\n> tnctl council role list\n\nRun:\n\n    tnctl council rolee list\n",
			want: []string{"case.md:1: the fence never closes", "case.md:6" + indentMsg},
		},
		{
			name: "an indented block with CRLF line endings",
			page: "Run:\r\n\r\n    tnctl council rolee list\r\n" + tail,
			want: []string{"case.md:3" + indentMsg},
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
