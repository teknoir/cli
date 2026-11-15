package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// councilConfirmAll is what --yes confirms: every confirmation the platform asks for.
var councilConfirmAll = []string{"inspect", "creator"}

// confirmOpts says how a write that the platform asks to confirm goes on.
type confirmOpts struct {
	yes       bool // --yes: confirm everything with the first request
	dryRun    bool // a preview: confirm by itself and never ask (only apply has one)
	canPrompt bool // stdin is a terminal and is not the command's input (-f -, --how-file -)
}

// councilConfirm sends a write that the platform may ask to confirm (409 confirm_needed). It sends at
// most two requests: with --yes one, carrying every confirmation; otherwise one without any and, when
// the platform asks and the person (or a dry run) says yes, one more with exactly what it asked for.
// send makes one request with the given confirmations and keeps its answer. The error councilConfirm
// returns is mapped already (exit code and words).
func councilConfirm(cmd *cobra.Command, o confirmOpts, send func(confirm []string) error) error {
	var first []string
	if o.yes {
		first = councilConfirmAll
	}
	err := send(first)
	if err == nil {
		return nil
	}
	ce, ok := api.CouncilErrorOf(err)
	if !ok || ce.Code != "confirm_needed" || len(ce.Needs) == 0 {
		return councilError(err)
	}
	needs := ce.Needs
	if o.yes {
		return newErr(codeError, "the platform asked to confirm (%s) although --yes confirmed %s: nothing was written; to answer its question, run the command on a terminal without --yes, with files instead of - for stdin",
			councilNeeds(needs), strings.Join(councilConfirmAll, ", "))
	}
	errw := cmd.ErrOrStderr()
	question := confirmDocs(ce.Documents) + confirmMessage(ce)
	switch {
	case o.dryRun:
		suffix := ""
		if words := confirmDocWords(ce.Documents); words != "" {
			suffix = ", for " + words
		}
		_, _ = fmt.Fprintf(errw, "· the real apply asks to confirm: %s%s (on a terminal it asks, unless -f - gives the file; otherwise pass --yes)\n", councilNeeds(needs), suffix)
	case !o.canPrompt:
		return usageErr("%s Nothing was written: pass --yes to confirm: %s", question, councilNeeds(needs))
	default:
		_, _ = fmt.Fprintf(errw, "%s Continue? [y/N] ", question)
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if !strings.HasSuffix(line, "\n") {
			// EOF or a read error: nothing ended the question's line.
			_, _ = fmt.Fprintln(errw)
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if (readErr != nil && !errors.Is(readErr, io.EOF)) || (answer != "y" && answer != "yes") {
			return newErr(codeError, "not confirmed: nothing was written")
		}
	}
	err = send(needs)
	if err == nil {
		return nil
	}
	if again, ok := api.CouncilErrorOf(err); ok && again.Code == "confirm_needed" {
		asked := again.Needs
		if len(asked) == 0 {
			asked = needs
		}
		return newErr(codeConflict, "the platform asked again to confirm (%s): someone changed a member meanwhile; run the command again", councilNeeds(asked))
	}
	return councilError(err)
}

// councilNeeds is the platform's confirmation words on one line: "inspect, creator".
func councilNeeds(needs []string) string {
	out := make([]string, 0, len(needs))
	for _, n := range needs {
		out = append(out, escapeLine(n))
	}
	return strings.Join(out, ", ")
}

// confirmMessage is the platform's question, made safe for one line; without words, one that names the needs.
func confirmMessage(ce *api.CouncilError) string {
	if msg := hubText(ce.Message); msg != "" {
		return msg
	}
	return "This change needs your confirmation (" + councilNeeds(ce.Needs) + ")."
}

// confirmDocWords names the documents of a file by their place in it, counted from 1: "document 2",
// "documents 2 and 4", "documents 1, 2 and 4"; "" for none.
func confirmDocWords(docs []int) string {
	nums := make([]string, 0, len(docs))
	for _, d := range docs {
		nums = append(nums, strconv.Itoa(d+1))
	}
	switch len(nums) {
	case 0:
		return ""
	case 1:
		return "document " + nums[0]
	}
	return "documents " + strings.Join(nums[:len(nums)-1], ", ") + " and " + nums[len(nums)-1]
}

// confirmDocs is the question's prefix that names the documents: "Document 2: ", "Documents 2 and 4: ",
// or "" when the platform names none (a member's PUT never does).
func confirmDocs(docs []int) string {
	words := confirmDocWords(docs)
	if words == "" {
		return ""
	}
	return strings.ToUpper(words[:1]) + words[1:] + ": "
}

// councilApplyMax is the largest council file the platform takes, in bytes (64 KiB).
const councilApplyMax = 65536

// newCouncilApplyCmd is "tnctl council apply": members and councils from a YAML file.
func newCouncilApplyCmd(e *cliEnv) *cobra.Command {
	var file string
	var dryRun, yes bool
	c := &cobra.Command{
		Use:   "apply -f <file|->",
		Short: "Apply members and councils from a YAML file",
		Long: `Apply a council file: members (kind CouncilRole) and councils (kind CouncilRoster), one or more
documents separated by ---, as tnctl council role export <member> and tnctl council roster get
<name> --bot <bot> -o yaml print them. The platform checks every document and applies all of them
or none; tnctl sends the file as it is. A file holds at most 20 documents and 64 KiB.

A member that can read pods and logs (tool group inspect), or that may act with the bot's full
access (accessCeiling: creator), needs your confirmation. tnctl asks on a terminal; --yes gives it
up front. Applying a member that someone else saved last asks again, so a script that applies
another person's export needs --yes. Without a terminal, or with -f -, tnctl cannot ask: it
writes nothing and exits 2.

--dry-run checks the file and shows what would change, and writes nothing. -o json|yaml prints
the platform's answer instead of the table.

Exit codes: 0 applied; 1 not confirmed; 2 wrong arguments, a council that names a member it
cannot use (unknown, or ambiguous without a scope), or a confirmation tnctl cannot ask for; 3 a
bot the file names does not exist; 5 the platform refused the file; 6 not allowed; 8 a member
changed meanwhile: run it again.`,
		Example: `  tnctl council role export triage --bot scout > triage.yaml
  tnctl council apply -f triage.yaml --dry-run
  tnctl council apply -f triage.yaml
  tnctl council roster get ops-scout --bot scout -o yaml > ops-scout.yaml
  tnctl council apply -f ops-scout.yaml
  cat council.yaml | tnctl council apply -f - --yes`,
		SilenceUsage: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErr("council apply takes the file with -f, for example: tnctl council apply -f %s", escapeLine(args[0]))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			if file == "" {
				return usageErr("council apply needs a file: -f <file>, or -f - for stdin")
			}
			text, err := readManifest(cmd, file)
			if err != nil {
				return err
			}
			if !utf8.ValidString(text) {
				return usageErr("%s is not UTF-8 text", fileLabel(file))
			}
			if len(text) > councilApplyMax {
				return newErr(codeRejected, "The file is larger than 64 KiB.")
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			var res *api.ApplyResult
			var raw json.RawMessage
			send := func(confirm []string) error {
				r, answer, applyErr := fl.ApplyCouncil(cmd.Context(), api.ApplyRequest{YAML: text, DryRun: dryRun, Confirm: confirm})
				if applyErr == nil {
					res, raw = r, answer
				}
				return applyErr
			}
			if err := councilConfirm(cmd, confirmOpts{yes: yes, dryRun: dryRun, canPrompt: stdinIsTerminal() && file != "-"}, send); err != nil {
				return err
			}
			out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if format != "table" {
				if err := writeStructured(out, format, raw); err != nil {
					return err
				}
			} else if err := printCouncilApply(out, errw, res.Results); err != nil {
				return err
			}
			if dryRun {
				_, _ = fmt.Fprintf(errw, "· dry run: nothing was written\n")
			}
			return nil
		},
	}
	c.Flags().StringVarP(&file, "file", "f", "", "the council file (- for stdin)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "check the file and show what would change; write nothing")
	c.Flags().BoolVar(&yes, "yes", false, "confirm a member that reads pods and logs or acts with the bot's full access")
	return c
}

// printCouncilApply prints what applying each document did, one row per document, numbered from 1 as
// the file counts them.
func printCouncilApply(out, errw io.Writer, results []api.ApplyItem) error {
	if len(results) == 0 {
		_, _ = fmt.Fprintf(errw, "· the platform applied no document\n")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "DOC\tKIND\tNAME\tSCOPE\tBOT\tACTION\tVERSION\n")
	for _, r := range results {
		bot := "-"
		if r.Bot != "" {
			bot = councilCell(r.Bot)
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%d\n", r.Index+1, councilCell(r.Kind), councilCell(r.Name),
			councilCell(r.Scope), bot, councilCell(r.Action), r.Version)
	}
	return tw.Flush()
}
