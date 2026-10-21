package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"go.yaml.in/yaml/v3"
)

// ---------- config add ----------

type addFlags struct {
	file    string
	replace bool
	dryRun  bool
	wait    waitFlags
}

func newConfigAddCmd(e *cliEnv) *cobra.Command {
	var f addFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "add <target> -f <file|-> [--replace] [--dry-run] [--wait[=published|applied|ready]]",
		Short:        "Add a local resource (any supported Kubernetes resource) to a robot or device",
		Long: `Add a Kubernetes resource to one robot or device. The platform checks it against the Kubernetes
schemas, stores it as a local item and publishes it to the robot, whose agent applies it. A local item
wins over a git copy with the same Kind/namespace/name (the answer says which copy it hides) and stays
until you 'tnctl config remove' it. 'tnctl config kinds' lists the supported kinds with the reasons for the
refused ones; 'tnctl config kinds --template Deployment' prints a starter manifest.

  -f FILE          the manifest: YAML or JSON, - reads stdin. Several documents separated by --- are
                   added one by one, in order; the first failure stops the run (the documents before it
                   are written) and decides the exit code. Namespace documents are sent first, so they
                   are stored before the resources in them; the output says so when that changes the
                   order. This orders the requests only: the platform decides the order in which the
                   robot applies the items. A dry run does not write the Namespace, so it does not
                   complain about a namespace that a Namespace document of the file creates.
  --replace        overwrite a local item with the same identity (otherwise exit 8). Identical content
                   is "unchanged": nothing is written.
  --dry-run        check the manifest and say what would happen, write nothing
  --wait[=LEVEL]   published: the platform published a new config with the item in the robot's local
                   layer
                   applied: the robot has it. The object exists (workloads: the controller saw the
                   latest generation); a HelmChart: its helm-install Job finished and a release exists
                   ready: applied, and Deployments, StatefulSets and DaemonSets finished rolling out
                   with stable pods; a Job completed. A HelmChartConfig counts as applied once the object is on the
                   robot (the chart's reinstall is not followed). A bare --wait is ready; write --wait=applied.
  --timeout T      how long --wait may take (default 5m; exit code 4 when it runs out)

Problems in a manifest are printed as FILE:LINE:COL: error: message, with the line of the file you gave
(also for the second and later documents) and exit code 5. -o json|yaml prints the platform's answer for
each document (a single object for one document, a list for several), plus a "wait" object with --wait.

Two documents that are the same Kind/namespace/name in one file are refused (exit 2) before anything is sent.

Exit codes as in 'tnctl config --help'; 8 also means the item already exists (use --replace) or is not
yours to replace (a git item or a platform item).`,
		Example: `  tnctl config add vision-station-01 -f whoami.yaml --dry-run
  tnctl config add vision-station-01 -f whoami.yaml --wait=applied
  tnctl config add vision-station-01 -f stack.yaml --replace --wait
  tnctl config kinds --template ConfigMap | tnctl config add vision-station-01 -f - --dry-run`,
		Args: argsRange(1, 1, "add needs a robot or device name and a manifest, for example: tnctl config add vision-station-01 -f whoami.yaml"),
		RunE: func(cmd *cobra.Command, args []string) error { return e.runAdd(cmd, args[0], &f) },
	}
	c.Flags().StringVarP(&f.file, "file", "f", "", "the manifest to add, YAML or JSON (- for stdin); --- separates several")
	c.Flags().BoolVar(&f.replace, "replace", false, "overwrite a local item with the same identity")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "check the manifest, write nothing")
	f.wait.add(c)
	return c
}

// localDoc is one YAML document of the input.
type localDoc struct {
	// index is the number of the document among the non-empty ones, from 1.
	index int
	// line is the line of the input where the text starts, from 1.
	line int
	// prefix is the number of characters before the text on its first line: the "--- " of a
	// "--- kind: x" line. Columns the platform reports on the first line are shifted by it.
	prefix int
	text   string
	// kind, ns and name are read from the text without the platform (empty when it does not parse).
	kind, ns, name string
}

// docSeparator matches a line that starts a document: --- alone, or followed by a space or tab and
// a comment or, as YAML allows, the first line of the next document ("--- !!map", "--- kind: x").
var docSeparator = regexp.MustCompile(`^---([ \t].*)?$`)

// splitDocs splits a multi-document YAML stream at its --- lines. Documents without content
// (empty or only comments) are dropped. Every document keeps the line it starts on, so that a
// position inside it can be translated to a line of the input.
func splitDocs(data string) []localDoc {
	var docs []localDoc
	var buf strings.Builder
	start := 1
	prefix := 0
	flush := func() {
		text := buf.String()
		buf.Reset()
		if !hasContent(text) {
			return
		}
		d := localDoc{index: len(docs) + 1, line: start, prefix: prefix, text: text}
		d.kind, d.ns, d.name = docIdentity(text)
		docs = append(docs, d)
	}
	data = strings.TrimPrefix(data, "\ufeff") // a byte-order mark would hide a first ---
	for i, line := range strings.SplitAfter(data, "\n") {
		if m := docSeparator.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
			flush()
			start = i + 2
			prefix = 0
			// Content after the --- belongs to the new document and starts on this very line.
			if rest := strings.TrimSpace(m[1]); rest != "" && !strings.HasPrefix(rest, "#") {
				start = i + 1
				content := strings.TrimLeft(m[1], " \t")
				prefix = len("---") + len(m[1]) - len(content)
				buf.WriteString(content + lineEnd(line))
			}
			continue
		}
		buf.WriteString(line)
	}
	flush()
	return docs
}

// lineEnd is the line break at the end of line ("" for the last line without one).
func lineEnd(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}

func hasContent(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}

// docIdentity reads kind, namespace and name of a document; a namespaced item without one is in default.
func docIdentity(text string) (kind, ns, name string) {
	var m struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(text), &m); err != nil {
		return "", "", ""
	}
	ns = m.Metadata.Namespace
	if ns == "" {
		ns = "default"
	}
	return m.Kind, ns, m.Metadata.Name
}

// where says which document of the input is meant: "document 2 (line 8, Deployment/whoami)".
func (d localDoc) where() string {
	if d.kind != "" && d.name != "" {
		return fmt.Sprintf("document %d (line %d, %s/%s)", d.index, d.line, d.kind, d.name)
	}
	return fmt.Sprintf("document %d (line %d)", d.index, d.line)
}

func (d localDoc) label() string {
	if d.kind != "" && d.name != "" {
		return d.kind + "/" + d.name
	}
	return fmt.Sprintf("document %d", d.index)
}

// readManifest reads the -f input.
func readManifest(cmd *cobra.Command, file string) (string, error) {
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return "", usageErr("cannot read %s: %v", fileLabel(file), err)
	}
	return string(data), nil
}

func fileLabel(file string) string {
	if file == "-" {
		return "<stdin>"
	}
	return file
}

func (e *cliEnv) runAdd(cmd *cobra.Command, target string, f *addFlags) error {
	if f.file == "" {
		return usageErr("add needs the manifest: -f <file> (or -f - for stdin)\nusage: %s", cmd.UseLine())
	}
	if err := f.wait.misplaced(cmd.Flags().Args()); err != nil {
		return err
	}
	data, err := readManifest(cmd, f.file)
	if err != nil {
		return err
	}
	docs := splitDocs(data)
	if len(docs) == 0 {
		return usageErr("%s has no YAML document to add", fileLabel(f.file))
	}
	if err := checkDuplicateDocs(fileLabel(f.file), docs); err != nil {
		return err
	}
	s, err := e.begin(cmd, target, &f.wait, f.dryRun)
	if err != nil {
		return err
	}
	docs, moved := namespacesFirst(docs)
	if len(moved) > 0 {
		note := fmt.Sprintf("sending Namespace %s first, so it is stored before the resources in it (the platform decides the order on the robot)", strings.Join(moved, ", "))
		if s.format == "table" {
			fmt.Fprintln(cmd.OutOrStdout(), note)
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(), "note:", note)
		}
	}
	run := s.putLocal(cmd.Context(), fileLabel(f.file), docs, f.replace)
	return s.finishLocal(run)
}

// namespacesFirst returns the documents with the Namespace documents in front (the order of the
// others, and of the Namespaces among themselves, is kept), and the names of the Namespaces that were
// moved forward: those that stood after another kind. Nothing is moved, and the list is empty, when
// the file has no Namespace or they are in front already. Positions (index, line) stay those of the file.
func namespacesFirst(docs []localDoc) (ordered []localDoc, moved []string) {
	var nss, rest []localDoc
	for _, d := range docs {
		if d.kind == "Namespace" {
			nss = append(nss, d)
		} else {
			rest = append(rest, d)
		}
	}
	if len(nss) == 0 || len(rest) == 0 {
		return docs, nil
	}
	seenOther := false
	for _, d := range docs {
		switch {
		case d.kind != "Namespace":
			seenOther = true
		case seenOther:
			moved = append(moved, d.label())
		}
	}
	if len(moved) == 0 {
		return docs, nil
	}
	for i, d := range moved {
		moved[i] = strings.TrimPrefix(d, "Namespace/")
	}
	return append(nss, rest...), moved
}

// namespacesOf is the names of the namespaces that the Namespace documents of the input create.
func namespacesOf(docs []localDoc) map[string]bool {
	m := map[string]bool{}
	for _, d := range docs {
		if d.kind == "Namespace" && d.name != "" {
			m[d.name] = true
		}
	}
	return m
}

// namespaceMissing reports whether a finding or warning of the platform says that the namespace ns
// of the document does not exist (yet): the error "namespace x does not exist ..." at
// metadata.namespace, or the warning "Nothing in this robot's configuration creates the namespace x".
// It matches on the text and the namespace name, so that another finding about the namespace (for
// example one that refuses it) is kept.
func namespaceMissing(msg, ns string) bool {
	m := strings.ToLower(msg)
	ns = strings.ToLower(ns)
	if !strings.Contains(m, "namespace "+ns) {
		return false
	}
	return strings.Contains(m, "does not exist") || strings.Contains(m, "creates the namespace") ||
		strings.Contains(m, "doesn't exist") || strings.Contains(m, "not found")
}

// dropPendingNamespaceIssues removes, from the answer to a dry run, the complaints about a namespace
// that a Namespace document of the same file would create: a dry run writes nothing, so the platform
// does not know the namespace yet (it warns, or refuses), while a real run, with the Namespace sent
// first, is accepted without it. It returns the namespaces it let pass. When no error is left, the
// verdict becomes valid.
func dropPendingNamespaceIssues(res *api.LocalWriteResult, d localDoc, created map[string]bool) []string {
	if !created[d.ns] || d.kind == "Namespace" {
		return nil
	}
	dropped := false
	var kept []api.LocalValidationIssue
	for _, is := range res.Validation.Issues {
		if is.Path == "metadata.namespace" && namespaceMissing(is.Message, d.ns) {
			dropped = true
			continue
		}
		kept = append(kept, is)
	}
	var warnings []string
	for _, w := range res.Warnings {
		if namespaceMissing(w, d.ns) {
			dropped = true
			continue
		}
		warnings = append(warnings, w)
	}
	if !dropped {
		return nil
	}
	res.Validation.Issues = kept
	res.Warnings = warnings
	for _, is := range kept {
		if is.Severity != "warning" {
			return []string{d.ns}
		}
	}
	res.Validation.Verdict = api.VerdictValid
	return []string{d.ns}
}

// checkDuplicateDocs refuses a file in which two documents are the same item: the second would
// fail after the first was written (or, with --replace, silently overwrite it), and a dry run
// cannot see that because it writes nothing.
func checkDuplicateDocs(file string, docs []localDoc) error {
	seen := map[string]localDoc{}
	for _, d := range docs {
		if d.kind == "" || d.name == "" {
			continue // the platform reports what is wrong with it
		}
		id := d.kind + "/" + d.ns + "/" + d.name
		if first, ok := seen[id]; ok {
			return usageErr("%s: %s (line %d) is the same item as document %d (line %d): %s; nothing was sent", file, d.where(), d.line, first.index, first.line, id)
		}
		seen[id] = d
	}
	return nil
}

// ---------- writing documents ----------

// localOutcome is the platform's answer for one document.
type localOutcome struct {
	doc localDoc
	res *api.LocalWriteResult
	raw json.RawMessage
}

// localRun is what putLocal did: the answers, the state of --wait and the error that ended it.
type localRun struct {
	outs    []localOutcome
	waited  bool
	waitErr error
	seconds int
	err     error
}

// putLocal writes the documents in order, one request each, and stops at the first failure.
// With --wait it follows all written items together at the end.
func (s *session) putLocal(ctx context.Context, file string, docs []localDoc, replace bool) *localRun {
	run := &localRun{}
	errw := s.cmd.ErrOrStderr()
	w := s.waiter()
	defer w.close()
	waiting := !s.dryRun && s.level != waitNone
	var hashBefore string
	if waiting {
		var ids []string
		for _, d := range docs {
			if d.kind == "HelmChart" && d.name != "" {
				ids = append(ids, "HelmChart/"+d.ns+"/"+d.name)
			}
		}
		var err error
		if hashBefore, err = w.prepareAdd(ctx, s.target, ids); err != nil {
			run.err = err
			return run
		}
	}
	var added []*addedItem
	var written []string // Kind/name of what this run wrote, in sending order
	created := namespacesOf(docs)
	for _, d := range docs {
		res, raw, err := s.fl.PutLocalItem(ctx, s.target, api.LocalWriteRequest{Manifest: d.text, Replace: replace}, s.dryRun)
		if err != nil {
			run.err = docError(err, d, len(docs) > 1, written, replace, func() bool { return s.localItemExists(ctx, d) })
			return run
		}
		if s.dryRun {
			if ns := dropPendingNamespaceIssues(res, d, created); len(ns) > 0 {
				raw = withFields(raw, map[string]any{"validation": res.Validation, "warnings": res.Warnings})
				fmt.Fprintf(errw, "note: namespace %s does not exist on %s yet; this file creates it with a Namespace document, which is sent first\n", ns[0], s.target)
			}
		}
		run.outs = append(run.outs, localOutcome{doc: d, res: res, raw: raw})
		for _, wn := range res.Warnings {
			fmt.Fprintln(errw, "warning:", wn)
		}
		if n := printIssues(errw, file, d, len(docs) > 1, res.Validation); res.Invalid() {
			run.err = invalidError(d, len(docs) > 1, n, written)
			return run
		}
		if s.format == "table" {
			fmt.Fprintln(s.cmd.OutOrStdout(), localResultLine(res, d, s.target))
		}
		if s.dryRun && !replace && res.Action == api.LocalActionReplace {
			// The same write without --replace would be refused (409), so say so now.
			msg := file + ": "
			if len(docs) > 1 {
				msg += d.where() + ": "
			}
			run.err = newErr(codeConflict, "%san item with this identity exists; use --replace to replace it", msg)
			return run
		}
		if !s.dryRun {
			a, err := newAddedItem(res, d)
			if err != nil {
				run.err = err
				return run
			}
			added = append(added, a)
			written = append(written, d.label())
		}
	}
	if waiting && len(added) > 0 {
		start := s.e.clock()
		info := writeInfo{added: added, hashBefore: hashBefore}
		for _, a := range added {
			info.resources = append(info.resources, a.id)
		}
		run.waitErr = w.wait(ctx, info)
		run.waited = true
		run.seconds = int(s.e.clock().Sub(start).Seconds())
	}
	return run
}

// finishLocal prints the structured answer of a run and returns its error.
func (s *session) finishLocal(run *localRun) error {
	if s.format != "table" && len(run.outs) > 0 {
		var items []json.RawMessage
		for _, o := range run.outs {
			raw := o.raw
			if run.waited {
				ws := map[string]any{"level": s.level, "reached": run.waitErr == nil, "seconds": run.seconds}
				if run.waitErr != nil {
					ws["error"] = run.waitErr.Error()
				}
				raw = withFields(raw, map[string]any{"wait": ws})
			}
			items = append(items, raw)
		}
		var out json.RawMessage
		if len(items) == 1 {
			out = items[0]
		} else {
			out = marshalRaw(items)
		}
		if err := writeStructured(s.cmd.OutOrStdout(), s.format, out); err != nil {
			return err
		}
	}
	if run.err != nil {
		return run.err
	}
	return run.waitErr
}

// localResultLine is the table-mode line of one document.
func localResultLine(res *api.LocalWriteResult, d localDoc, target string) string {
	id := res.ID
	if id == "" {
		id = d.label()
	}
	switch {
	case res.Action == api.LocalActionUnchanged && res.DryRun:
		return fmt.Sprintf("would leave %s unchanged on %s (already as given)", id, target)
	case res.Action == api.LocalActionUnchanged:
		return fmt.Sprintf("unchanged %s on %s (already as given)", id, target)
	case res.Action == api.LocalActionReplace && res.DryRun:
		return fmt.Sprintf("would replace %s on %s", id, target)
	case res.Action == api.LocalActionReplace:
		return fmt.Sprintf("replaced %s on %s", id, target)
	case res.DryRun:
		return fmt.Sprintf("would add %s to %s", id, target)
	}
	return fmt.Sprintf("added %s to %s", id, target)
}

// printIssues prints the findings of a validation as FILE:LINE:COL: severity: message, with the
// line translated from the document to the file. It returns the number of errors.
func printIssues(w io.Writer, file string, d localDoc, multi bool, v api.LocalValidation) int {
	errs := 0
	for _, is := range v.Issues {
		if is.Severity != "warning" {
			errs++
		}
		is := is
		if is.Line == 1 && is.Column > 0 {
			is.Column += d.prefix
		}
		fmt.Fprintln(w, formatIssue(file, d.line-1, multi, d.index, is))
	}
	return errs
}

// formatIssue renders one finding. offset is added to the line the platform reported.
func formatIssue(file string, offset int, multi bool, doc int, is api.LocalValidationIssue) string {
	sev := is.Severity
	if sev == "" {
		sev = "error"
	}
	msg := is.Message
	if is.Path != "" && !strings.Contains(msg, is.Path) {
		msg += " (" + is.Path + ")"
	}
	switch {
	case is.Line > 0 && is.Column > 0:
		return fmt.Sprintf("%s:%d:%d: %s: %s", file, is.Line+offset, is.Column, sev, msg)
	case is.Line > 0:
		return fmt.Sprintf("%s:%d: %s: %s", file, is.Line+offset, sev, msg)
	case multi:
		return fmt.Sprintf("%s: %s: document %d: %s", file, sev, doc, msg)
	}
	return fmt.Sprintf("%s: %s: %s", file, sev, msg)
}

// invalidError is the error of a manifest the platform refused (exit 5).
func invalidError(d localDoc, multi bool, errs int, written []string) error {
	what := "the manifest"
	if multi {
		what = d.where()
	}
	msg := fmt.Sprintf("%s is invalid (%d error(s) above); it was not written", what, errs)
	if len(written) > 0 {
		msg += "; written before it: " + strings.Join(written, ", ")
	}
	return newErr(codeRejected, "%s", msg)
}

// localItemExists reports whether the target has a local item with the identity of the document.
// A platform, reserved or git identity answers 409 there too, and is not replaceable.
func (s *session) localItemExists(ctx context.Context, d localDoc) bool {
	if d.kind == "" || d.name == "" {
		return false
	}
	_, _, err := s.fl.LocalItem(ctx, s.target, d.kind+"/"+d.ns+"/"+d.name)
	return err == nil
}

// docError gives a failed request its exit code and, for several documents, says which one failed.
// exists is asked on a 409 without --replace: whether a local item with this identity is there, so
// that --replace would help. nil means "do not know".
func docError(err error, d localDoc, multi bool, written []string, replace bool, exists func() bool) error {
	err = classify(err)
	var ce *cliError
	if !errors.As(err, &ce) {
		return err
	}
	inner := ce.err.Error()
	if ce.code == codeConflict && !replace && exists != nil && exists() {
		inner += "; use --replace to overwrite it"
	}
	if multi {
		inner = d.where() + ": " + inner
		if len(written) > 0 {
			inner += "; written before it: " + strings.Join(written, ", ")
		}
	}
	return &cliError{code: ce.code, err: errors.New(inner)}
}
