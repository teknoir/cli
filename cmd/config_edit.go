package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"golang.org/x/term"
)

// ---------- config get --manifest ----------

// notLocalHint says how to get a local copy of an item that is not local.
func notLocalHint(err error, target, resArg string) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == 409 {
		return &cliError{code: codeConflict, err: fmt.Errorf("%w\nhint: only local items can be edited here. To make a local copy that wins over this one on %s:\n  tnctl config get %s %s --rendered > item.yaml   # edit it\n  tnctl config add %s -f item.yaml --dry-run", err, target, target, resArg, target)}
	}
	return err
}

// printManifest prints the stored local item of a resource, ready to edit (-o json|yaml: the platform's answer).
func printManifest(cmd *cobra.Command, fl *api.Fleet, format, target, id, resArg string) error {
	src, raw, err := fl.LocalItem(cmd.Context(), target, id)
	if err != nil {
		return classify(notLocalHint(err, target, resArg))
	}
	out := cmd.OutOrStdout()
	if format != "table" {
		return writeStructured(out, format, raw)
	}
	m := src.Manifest
	if !strings.HasSuffix(m, "\n") {
		m += "\n"
	}
	_, err = fmt.Fprint(out, m)
	return err
}

// ---------- config edit ----------

func newConfigEditCmd(e *cliEnv) *cobra.Command {
	var f struct{ wait waitFlags }
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "edit <target> <resource> [--wait[=published|applied|ready]]",
		Short:        "Edit a local item of a robot or device in your editor, with a check before it is saved",
		Long: `Open a local item in $VISUAL or $EDITOR (vi when neither is set; 'code --wait' works) and save it
when you close the editor. The manifest is checked by the platform first: when it has errors, the editor opens
again with the errors at the top of the file (as comments, removed when you save; the line numbers are those
of the file in the editor). Close the editor without changing anything to leave: nothing is written. Saving
without changes, or an empty file, writes nothing either.

Only local items can be edited, that is items that were added with 'tnctl config add' or written by a tool
such as Devstudio. For an item from git the command answers with exit 8 and shows how to make a local copy.
The kind, namespace and name cannot change in an edit (that would be another item; use 'config add').
A Secret shows <hidden> for its values; leave them as they are to keep the stored ones.

An editor that runs in a terminal (vi, nano) is fine. A graphical one must wait until the file is
closed, or tnctl sees an unchanged file at once: VISUAL='code --wait', 'subl -w', 'gedit --wait'.
The command needs a terminal on stdin; in a script use 'tnctl config get --manifest' and 'config add --replace'.
If the item is changed by someone else while the editor is open, nothing is written (exit 8).

  --wait[=LEVEL]   as in 'tnctl config add --wait': published, applied or ready (a bare --wait is ready)
  --timeout T      how long --wait may take (default 5m)`,
		Example: `  tnctl config edit vision-station-01 Deployment/default/whoami
  tnctl config edit vision-station-01 whoami --wait=applied
  VISUAL='code --wait' tnctl config edit vision-station-01 ConfigMap/default/settings`,
		Args: argsRange(2, 2, "edit needs a robot or device name and a local resource"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.wait.misplaced(args[1:]); err != nil {
				return err
			}
			return e.runEdit(cmd, args[0], args[1], &f.wait)
		},
	}
	f.wait.add(c)
	return c
}

const (
	editHeaderStart = "# tnctl:"
	editHeaderEnd   = "# ---- end of the tnctl messages (this block is removed when you save) ----"
)

// stripEditHeader removes the message block that a previous round put at the top of the file.
func stripEditHeader(text string) string {
	if !strings.HasPrefix(text, editHeaderStart) {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		if strings.TrimRight(l, "\r\n") == editHeaderEnd {
			return strings.Join(lines[i+1:], "")
		}
	}
	return text
}

// withEditHeader puts messages above the manifest: first the general ones, then one line per finding,
// with the line numbers of the file as the editor will show it (the header pushes the manifest down).
func withEditHeader(manifest string, problems []api.LocalValidationIssue, extra []string) string {
	offset := len(extra) + len(problems) + 2 // the first and the last line of the block
	var b strings.Builder
	b.WriteString(editHeaderStart + " the platform refused this manifest. Fix it and save, or close without changes to leave.\n")
	for _, m := range extra {
		b.WriteString("# " + strings.ReplaceAll(m, "\n", " ") + "\n")
	}
	for _, is := range problems {
		b.WriteString("# " + strings.ReplaceAll(headerIssue(is, offset), "\n", " ") + "\n")
	}
	b.WriteString(editHeaderEnd + "\n")
	return b.String() + manifest
}

// headerIssue renders a finding for the message block: "line 14:5: error: message (path)".
func headerIssue(is api.LocalValidationIssue, offset int) string {
	sev := is.Severity
	if sev == "" {
		sev = "error"
	}
	msg := is.Message
	if is.Path != "" && !strings.Contains(msg, is.Path) {
		msg += " (" + is.Path + ")"
	}
	pos := ""
	if is.Line > 0 {
		pos = fmt.Sprintf("line %d", is.Line+offset)
		if is.Column > 0 {
			pos += fmt.Sprintf(":%d", is.Column)
		}
		pos += ": "
	}
	return pos + sev + ": " + msg
}

// stdinIsTerminal says whether the editor can be used: it is a variable so that tests can say yes.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func (e *cliEnv) runEdit(cmd *cobra.Command, target, resArg string, wf *waitFlags) error {
	if !stdinIsTerminal() {
		return usageErr("edit opens an editor and needs a terminal on stdin; to change a local item from a script, write the new manifest and run: tnctl config add %s -f FILE --replace (tnctl config get %s <resource> --manifest prints the stored one)", target, target)
	}
	s, err := e.begin(cmd, target, wf, false)
	if err != nil {
		return err
	}
	// A hangup or SIGTERM (a closed terminal, a dropped ssh session) must end through the deferred
	// removal of the temporary file, which may hold plaintext values.
	ctx, stop := signalContext(cmd.Context())
	defer stop()
	id, _, err := resolveTarget(ctx, s.fl, target, resArg)
	if err != nil {
		return classify(err)
	}
	src, _, err := s.fl.LocalItem(ctx, target, id)
	if err != nil {
		return classify(notLocalHint(err, target, resArg))
	}
	errw := cmd.ErrOrStderr()
	if src.ID != "" {
		id = src.ID
	}
	if src.HasOverride {
		fmt.Fprintln(errw, "note: this item carries an override on this robot; saving a new version drops it")
	}
	if src.Owner != "" && src.Owner != "backstage" {
		fmt.Fprintf(errw, "note: written by %s, which may write it again\n", src.Owner)
	}
	argv, err := editorCommand()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "tnctl-edit-")
	if err != nil {
		return newErr(codeError, "cannot create a temporary directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return newErr(codeError, "cannot protect the temporary directory: %v", err)
	}
	kind, _, name := splitID(id)
	path := filepath.Join(dir, safeFileName(kind+"-"+name)+".yaml")

	original := src.Manifest
	if !strings.HasSuffix(original, "\n") {
		original += "\n"
	}
	content := original
	var prevSent string
	var lastMsgs []string
	for round := 1; ; round++ {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return newErr(codeError, "cannot write %s: %v", path, err)
		}
		if err := runEditorProcess(ctx, argv, path); err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return newErr(codeError, "cannot read the edited file: %v", err)
		}
		text := stripEditHeader(string(b))
		if !hasContent(text) {
			fmt.Fprintf(cmd.OutOrStdout(), "Edit cancelled, the file was empty: %s on %s is unchanged.\n", id, target)
			return nil
		}
		if strings.TrimSpace(text) == strings.TrimSpace(original) {
			fmt.Fprintf(cmd.OutOrStdout(), "No changes: %s on %s is unchanged.\n", id, target)
			return nil
		}
		if round > 1 && text == prevSent {
			for _, m := range lastMsgs {
				fmt.Fprintln(errw, m)
			}
			return newErr(codeRejected, "the manifest is still invalid and was not changed; nothing was written")
		}
		prevSent = text
		docs := splitDocs(text)
		if len(docs) > 1 {
			extra := []string{fmt.Sprintf("edit works on one item and the file has %d documents: use tnctl config add for more", len(docs))}
			lastMsgs = extra
			content = withEditHeader(text, nil, extra)
			continue
		}
		doc := docs[0]
		res, _, err := s.fl.PutLocalItem(ctx, target, api.LocalWriteRequest{Manifest: text, Replace: true}, true)
		if err != nil {
			return docError(err, doc, false, 0, true, nil)
		}
		var problems []api.LocalValidationIssue
		var extra []string
		for _, is := range res.Validation.Issues {
			if is.Severity != "warning" {
				problems = append(problems, is)
			}
		}
		if !res.Invalid() && res.ID != "" && res.ID != id {
			extra = append(extra, fmt.Sprintf("this is %s, not %s: kind, namespace and name cannot change here (use tnctl config add for another item)", res.ID, id))
		}
		if res.Invalid() && len(problems) == 0 {
			extra = append(extra, "the platform refused the manifest")
		}
		if len(problems) > 0 || len(extra) > 0 {
			lastMsgs = lastMsgs[:0]
			for _, is := range problems {
				lastMsgs = append(lastMsgs, formatIssue("<edit>", 0, false, 1, is))
			}
			lastMsgs = append(lastMsgs, extra...)
			content = withEditHeader(text, problems, extra)
			continue
		}
		if res.Action == api.LocalActionUnchanged {
			fmt.Fprintf(cmd.OutOrStdout(), "No changes: %s on %s is unchanged.\n", id, target)
			return nil
		}
		// The platform has no revision to check, so look once more: if the item changed while the
		// editor was open (another person, or Devstudio writing it), saving would drop that.
		if cur, _, err := s.fl.LocalItem(ctx, target, id); err != nil {
			return classify(notLocalHint(err, target, resArg))
		} else if strings.TrimSpace(cur.Manifest) != strings.TrimSpace(src.Manifest) {
			return newErr(codeConflict, "%s on %s changed while you were editing it; nothing was written. Run tnctl config edit again to start from the new version (your text is lost; copy it first if you need it)", id, target)
		}
		run := s.putLocal(ctx, "<edit>", []localDoc{doc}, true)
		return s.finishLocal(run)
	}
}

// safeFileName keeps a file name to letters, digits and . _ -.
func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

// editorCommand is $VISUAL, else $EDITOR, else vi, split into words without a shell.
func editorCommand() ([]string, error) {
	spec := strings.TrimSpace(os.Getenv("VISUAL"))
	if spec == "" {
		spec = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if spec == "" {
		spec = "vi"
	}
	argv, err := splitCommand(spec, runtime.GOOS != "windows")
	if err != nil {
		return nil, usageErr("cannot read the editor %q from $VISUAL or $EDITOR: %v", spec, err)
	}
	if len(argv) == 0 {
		return nil, usageErr("$VISUAL and $EDITOR name no editor")
	}
	return argv, nil
}

// runEditorProcess runs the editor on the file, attached to the terminal.
func runEditorProcess(ctx context.Context, argv []string, path string) error {
	c := exec.CommandContext(ctx, argv[0], append(append([]string{}, argv[1:]...), path)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return newErr(codeError, "interrupted; nothing was written")
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return newErr(codeError, "the editor %q exited with status %d; nothing was written", argv[0], ee.ExitCode())
		}
		return newErr(codeError, "cannot run the editor %q: %v (set $VISUAL or $EDITOR)", argv[0], err)
	}
	return nil
}

// splitCommand splits a command line into words like a shell would for quotes, but runs nothing:
// single quotes keep everything, double quotes keep everything except backslash escapes, and
// (when backslashes is set) a backslash outside quotes escapes the next character.
func splitCommand(s string, backslashes bool) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\'':
			inWord = true
			j := i + 1
			for ; j < len(rs) && rs[j] != '\''; j++ {
				cur.WriteRune(rs[j])
			}
			if j == len(rs) {
				return nil, errors.New("a ' is not closed")
			}
			i = j
		case r == '"':
			inWord = true
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				if backslashes && rs[j] == '\\' && j+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[j+1]) {
					j++
				}
				cur.WriteRune(rs[j])
			}
			if j == len(rs) {
				return nil, errors.New("a \" is not closed")
			}
			i = j
		case backslashes && r == '\\':
			if i+1 == len(rs) {
				return nil, errors.New("a trailing \\")
			}
			inWord = true
			i++
			cur.WriteRune(rs[i])
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			inWord = true
			cur.WriteRune(r)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
