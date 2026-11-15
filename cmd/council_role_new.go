package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// councilRoleNewFlags are role new's flags.
type councilRoleNewFlags struct {
	job, how, howFile, from, bot string
	teamspace, dryRun, yes       bool
}

// newCouncilRoleNewCmd is "tnctl council role new": a member from a name and its job in words.
func newCouncilRoleNewCmd(e *cliEnv) *cobra.Command {
	var f councilRoleNewFlags
	c := &cobra.Command{
		Use:   "new <name>",
		Short: "Make a member from a name and its job in words",
		Long: `Make a member from a name and its job in words. The platform drafts it: it picks a free slug
and the premade member to start from (--from names one), takes that member's model, tools and
skills, and uses --how as the instructions (the premade member's otherwise). tnctl prints the
draft, then saves it.

The member belongs to one bot (--bot) or is shared in the teamspace (--teamspace, for teamspace
admins). A new member never reads pods and logs and never acts with the bot's full access: add
that later in YAML (tnctl council role export, edit, tnctl council apply).

--dry-run prints the draft and saves nothing. -o json|yaml prints the platform's answer instead
of the lines (with --dry-run, the draft; it is not a council file: to change more than --how
allows, save the member, then tnctl council role export, edit, tnctl council apply).`,
		Example: `  tnctl council role new "Stream watcher" --job "watches MQTT streams for gaps" --bot scout
  tnctl council role new "Cost watcher" --job "finds idle GPUs" --from analyst --teamspace --dry-run
  tnctl council role new "Triage" --job "sorts alerts by urgency" --how-file triage.md --bot scout`,
		SilenceUsage: true,
		Args:         argsRange(1, 1, "council role new needs one name (quote a name with spaces), for example: tnctl council role new \"Stream watcher\" --job \"watches MQTT streams for gaps\" --bot scout"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCouncilRoleNew(cmd, e, args[0], &f)
		},
	}
	c.Flags().StringVar(&f.job, "job", "", "what the member does, in one sentence (required)")
	c.Flags().StringVar(&f.how, "how", "", "the member's instructions (the premade member's by default)")
	c.Flags().StringVar(&f.howFile, "how-file", "", "read the instructions from a file (- for stdin)")
	c.Flags().StringVar(&f.from, "from", "", "the premade member to start from, such as analyst (the closest one by default; tnctl council role list --templates lists them: part member, not coder)")
	c.Flags().StringVar(&f.bot, "bot", "", "the TeknoirBot the member belongs to")
	c.Flags().BoolVar(&f.teamspace, "teamspace", false, "share the member in the teamspace (teamspace admins only)")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the draft; save nothing")
	c.Flags().BoolVar(&f.yes, "yes", false, "confirm what the platform asks to confirm (a new member never asks)")
	return c
}

// runCouncilRoleNew drafts a member and, unless --dry-run, saves it.
func runCouncilRoleNew(cmd *cobra.Command, e *cliEnv, name string, f *councilRoleNewFlags) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if !cmd.Flags().Changed("job") {
		return usageErr("council role new needs --job \"<what the member does>\", for example: --job \"watches MQTT streams for gaps\"")
	}
	if cmd.Flags().Changed("how") && cmd.Flags().Changed("how-file") {
		return usageErr("use --how or --how-file, not both")
	}
	botGiven := cmd.Flags().Changed("bot")
	switch {
	case botGiven && f.teamspace:
		return usageErr("council role new takes --bot <bot> (a member of that bot) or --teamspace (a shared member; teamspace admins only), not both")
	case !botGiven && !f.teamspace:
		return usageErr("council role new needs --bot <bot> (a member of that bot) or --teamspace (a shared member; teamspace admins only)")
	}
	if botGiven {
		if err := councilBot(f.bot); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		ok   bool
		want string
	}{
		{utf8.ValidString(name), "the name is not UTF-8 text"},
		{utf8.ValidString(f.job), "--job is not UTF-8 text"},
		{utf8.ValidString(f.how), "--how is not UTF-8 text"},
		{utf8.ValidString(f.from), "--from is not UTF-8 text"},
	} {
		if !c.ok {
			return usageErr("%s", c.want)
		}
	}
	how := f.how
	if cmd.Flags().Changed("how-file") {
		if f.howFile == "" {
			return usageErr("--how-file needs a file, or - for stdin")
		}
		text, err := readManifest(cmd, f.howFile)
		if err != nil {
			return err
		}
		if !utf8.ValidString(text) {
			return usageErr("%s is not UTF-8 text", fileLabel(f.howFile))
		}
		how = text
	}
	fl, _, ns, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	scope := "teamspace"
	if botGiven {
		scope = "bot"
	}
	d, draftRaw, err := fl.DraftRole(ctx, api.RoleDraftRequest{Name: name, Job: f.job, How: how, From: f.from, Bot: f.bot, Scope: scope})
	if err != nil {
		return councilError(err)
	}
	if !councilSlugRE.MatchString(d.Slug) {
		return newErr(codeError, "the platform drafted a slug tnctl cannot use (%q): nothing was saved", d.Slug)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(d.Spec), []byte("{")) {
		return newErr(codeError, "the platform's draft holds no member spec: nothing was saved")
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if format == "table" {
		printCouncilDraft(out, d, f.bot)
	}
	for _, w := range d.Warnings {
		msg := hubText(w.Message)
		if msg == "" {
			continue
		}
		if w.Path == "" {
			_, _ = fmt.Fprintf(errw, "warning: %s\n", msg)
		} else {
			_, _ = fmt.Fprintf(errw, "warning: %s: %s\n", escapeLine(w.Path), msg)
		}
	}
	if f.dryRun {
		if format != "table" {
			if err := writeStructured(out, format, draftRaw); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(errw, "· dry run: nothing was written\n")
		return nil
	}
	zero := 0
	var w *api.RoleWrite
	var putRaw json.RawMessage
	var lastErr error
	send := func(confirm []string) error {
		body := api.RolePut{Spec: d.Spec, ExpectVersion: &zero, Confirm: confirm}
		var write *api.RoleWrite
		var answer json.RawMessage
		var err error
		if botGiven {
			write, answer, err = fl.PutBotRole(ctx, f.bot, d.Slug, body)
		} else {
			write, answer, err = fl.PutTeamspaceRole(ctx, d.Slug, body)
		}
		lastErr = err
		if err == nil {
			w, putRaw = write, answer
		}
		return err
	}
	if err := councilConfirm(cmd, confirmOpts{yes: f.yes, canPrompt: stdinIsTerminal() && f.howFile != "-"}, send); err != nil {
		if ce, ok := api.CouncilErrorOf(lastErr); ok && ce.Code == "exists" {
			return newErr(codeConflict, "%s\nrun the command again: the platform picks a free name", err.Error())
		}
		return err
	}
	if format != "table" {
		if err := writeStructured(out, format, putRaw); err != nil {
			return err
		}
	} else {
		action := hubText(w.Action)
		if action == "" {
			action = "saved"
		}
		if botGiven {
			_, _ = fmt.Fprintf(out, "%s %s (version %d) for %s\n", action, escapeLine(d.Slug), w.Role.Version, escapeLine(f.bot))
		} else {
			_, _ = fmt.Fprintf(out, "%s %s (version %d), shared in teamspace %s\n", action, escapeLine(d.Slug), w.Role.Version, escapeLine(ns))
		}
	}
	if botGiven {
		_, _ = fmt.Fprintf(errw, "· try it: tnctl chat %s --member %s \"…\"\n", escapeLine(f.bot), escapeLine(d.Slug))
	} else {
		_, _ = fmt.Fprintf(errw, "· try it: tnctl chat <bot> --member teamspace/%s \"…\"\n", escapeLine(d.Slug))
	}
	return nil
}

// printCouncilDraft prints the platform's draft of a member as key: value lines, as role get does. bot
// is the --bot it was drafted for, "" for a shared member.
func printCouncilDraft(out io.Writer, d *api.RoleDraft, bot string) {
	var spec struct {
		DisplayName string   `json:"displayName"`
		Model       string   `json:"model"`
		Effort      string   `json:"effort"`
		Tools       []string `json:"tools"`
	}
	_ = json.Unmarshal(d.Spec, &spec)
	line := func(key, value string) { _, _ = fmt.Fprintf(out, "%-13s%s\n", key+":", value) }
	line("member", escapeLine(d.Slug))
	line("name", councilOr(hubText(spec.DisplayName)))
	start := hubText(d.StartsFrom.Name)
	switch from := escapeLine(d.StartsFrom.Slug); {
	case start != "" && from != "":
		start += " (" + from + ")"
	case start == "":
		start = councilOr(from)
	}
	line("starts from", start)
	line("model", councilOr(hubText(spec.Model))+", effort "+councilOr(hubText(spec.Effort)))
	line("tools", councilJoined(spec.Tools))
	line("effective", councilEffective(d.EffectiveTools, bot))
}

// councilOr is s, or "-" when it is empty: no line ends in a space.
func councilOr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// newCouncilRoleArchiveCmd is "tnctl council role archive": archive a member.
func newCouncilRoleArchiveCmd(e *cliEnv) *cobra.Command {
	var bot string
	c := &cobra.Command{
		Use:   "archive <member>",
		Short: "Archive a member: councils can no longer use it",
		Long: `Archive a member that a bot or the teamspace made: councils can no longer use it. A member that
a council still names is refused: remove it from those councils first. tnctl council role export
still prints an archived member, and applying that YAML brings it back.

<member> is a slug such as triage, or <scope>/<slug> (scope bot or teamspace). With --bot it
archives that bot's own member; without it, the teamspace's shared member (teamspace admins
only). Premade members cannot be archived. -o json|yaml prints the archived member as the
platform answers it.`,
		Example: `  tnctl council role archive stream-watcher --bot scout
  tnctl council role archive cost-watcher`,
		SilenceUsage: true,
		Args:         argsRange(1, 1, "council role archive needs a member, for example: tnctl council role archive triage --bot scout"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCouncilRoleArchive(cmd, e, args[0], bot)
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose own member to archive (without it: the teamspace's shared member)")
	return c
}

// runCouncilRoleArchive archives one member of a bot or of the teamspace.
func runCouncilRoleArchive(cmd *cobra.Command, e *cliEnv, ref, bot string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	scope, slug, ok := parseMemberRef(ref)
	if !ok {
		return usageErr("%q is not a member: use a slug such as triage, or <scope>/<slug> with scope bot or teamspace", ref)
	}
	botGiven := cmd.Flags().Changed("bot")
	switch {
	case scope == "template":
		return usageErr("premade members cannot be archived: they are read-only")
	case scope == "bot" && !botGiven:
		return usageErr("a bot's own member needs --bot <bot>")
	case scope == "teamspace" && botGiven:
		return usageErr("teamspace/%s is a shared member: leave out --bot to archive it", escapeLine(slug))
	}
	if botGiven {
		if err := councilBot(bot); err != nil {
			return err
		}
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	var raw json.RawMessage
	if botGiven {
		_, raw, err = fl.ArchiveBotRole(ctx, bot, slug)
	} else {
		_, raw, err = fl.ArchiveTeamspaceRole(ctx, slug)
	}
	if err != nil {
		mapped := councilError(err)
		switch {
		case !botGiven && scope == "" && councilNotFound(err):
			return newErr(codeNotFound, "%s\na bot's own member needs --bot <bot>", mapped.Error())
		case botGiven && scope == "" && councilNotFound(err):
			return newErr(codeNotFound, "%s\nif you meant the teamspace's shared member, leave out --bot: tnctl council role archive teamspace/%s", mapped.Error(), escapeLine(slug))
		}
		return mapped
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if format != "table" {
		if err := writeStructured(out, format, raw); err != nil {
			return err
		}
	} else if botGiven {
		_, _ = fmt.Fprintf(out, "archived %s of %s\n", escapeLine(slug), escapeLine(bot))
	} else {
		_, _ = fmt.Fprintf(out, "archived shared member %s\n", escapeLine(slug))
	}
	s, b := escapeLine(slug), escapeLine(bot)
	if botGiven {
		_, _ = fmt.Fprintf(errw, "· to bring it back: tnctl council role export bot/%s --bot %s > %s.yaml, then tnctl council apply -f %s.yaml\n", s, b, s, s)
	} else {
		_, _ = fmt.Fprintf(errw, "· to bring it back: tnctl council role export %s > %s.yaml, then tnctl council apply -f %s.yaml\n", s, s, s)
	}
	return nil
}
