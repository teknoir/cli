package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// rosterGoneHint is the next step when a council names a member that no longer exists: the web chat's
// Council tab, or the export and apply that write the council again.
const rosterGoneHint = "fix it in the web chat's Council tab, or write the council again with tnctl council apply -f <file>"

// rosterListHint is the command that lists the councils of bot.
func rosterListHint(bot string) string {
	return "tnctl council roster list --bot " + escapeLine(bot) + " lists its councils"
}

// rosterUseHint is the command that makes another council the default.
func rosterUseHint(bot string) string {
	return "tnctl council roster use <council> --bot " + escapeLine(bot) + " makes another council the default"
}

// rosterHints adds one line under the hub's refusal for the code it names; any other error, and a
// refusal of another code, comes back as councilError maps it.
func rosterHints(err error, hints map[string]string) error {
	mapped := councilError(err)
	if ce, ok := api.CouncilErrorOf(err); ok {
		if hint, found := hints[ce.Code]; found && hint != "" {
			return newErr(exitCodeOf(mapped), "%s\n%s", mapped.Error(), hint)
		}
	}
	return mapped
}

// ---------- the bot's council, read once ----------

// rosterCouncil is a bot's council as GET bots/<bot>/council answered it: the view, and each
// council.rosters entry as the hub sent it.
type rosterCouncil struct {
	view *api.CouncilView
	raw  []json.RawMessage
}

// readRosterCouncil reads the bot's council.
func readRosterCouncil(cmd *cobra.Command, fl *api.Fleet, bot string) (*rosterCouncil, error) {
	cv, raw, err := fl.BotCouncil(cmd.Context(), bot)
	if err != nil {
		return nil, councilError(err)
	}
	var ans struct {
		Council struct {
			Rosters []json.RawMessage `json:"rosters"`
		} `json:"council"`
	}
	_ = json.Unmarshal(raw, &ans)
	return &rosterCouncil{view: cv, raw: ans.Council.Rosters}, nil
}

// find is the index in the bot's councils of the council called name, -1 when there is none.
func (c *rosterCouncil) find(name string) int {
	for i, r := range c.view.Rosters {
		if r.Name == name {
			return i
		}
	}
	return -1
}

// taken is every council name the bot already has, the premade ones included.
func (c *rosterCouncil) taken() map[string]bool {
	m := make(map[string]bool, len(c.view.Rosters))
	for _, r := range c.view.Rosters {
		m[r.Name] = true
	}
	return m
}

// missing is the refusal of a council name the bot does not have: the names it has follow on the
// second line.
func (c *rosterCouncil) missing(name, bot string) error {
	names := make([]string, 0, len(c.view.Rosters))
	for _, r := range c.view.Rosters {
		names = append(names, escapeLine(r.Name))
	}
	if len(names) == 0 {
		return usageErr("There is no council '%s' for %s.", name, escapeLine(bot))
	}
	return usageErr("There is no council '%s' for %s.\ncouncils: %s", name, escapeLine(bot), strings.Join(names, ", "))
}

// ---------- the checks every roster command makes ----------

// rosterCheckName checks a council's name as the person typed it.
func rosterCheckName(name string) error {
	if !councilSlugRE.MatchString(name) {
		return usageErr("%q is not a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter", name)
	}
	return nil
}

// rosterNeedsBot refuses a roster command that was given no --bot. needs is the command's own text.
func rosterNeedsBot(cmd *cobra.Command, bot, needs string) error {
	if !cmd.Flags().Changed("bot") {
		return usageErr("%s", needs)
	}
	return councilBot(bot)
}

// deriveRosterName is the name tnctl suggests for a bot's new council: <bot>-council (c- in front when
// the bot's name starts with a digit), cut to 24 characters without a trailing dash; while that name
// is taken, -2, -3, … with the base cut so the whole stays within 24 (spec 12, C3a ruling 5). A cut
// that is not a council's name (a one-letter bot followed by 23 or more dashes leaves one letter)
// counts as taken.
func deriveRosterName(bot string, taken map[string]bool) string {
	base := bot + "-council"
	if bot != "" && bot[0] >= '0' && bot[0] <= '9' {
		base = "c-" + base
	}
	cut := func(s string, n int) string {
		if len(s) > n {
			s = s[:n]
		}
		return strings.TrimRight(s, "-")
	}
	name := cut(base, 24)
	for n := 2; taken[name] || !councilSlugRE.MatchString(name); n++ {
		suffix := fmt.Sprintf("-%d", n)
		name = cut(base, 24-len(suffix)) + suffix
	}
	return name
}

// ---------- council roster list ----------

func newCouncilRosterListCmd(e *cliEnv) *cobra.Command {
	var bot string
	var asJSON bool
	c := &cobra.Command{
		Use:   "list --bot <bot>",
		Short: "List a bot's councils: the premade ones and its own",
		Long: `List the councils a bot's chats can ask: the premade ones first, then the bot's own by name.
DEFAULT marks the council a new chat asks. STATUS is ok, unavailable (the reasons follow on
stderr) or not checked. --json prints the councils as the platform sent them.`,
		Example:      "  tnctl council roster list --bot scout",
		SilenceUsage: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErr("council roster list takes no arguments; name the bot with --bot, for example: tnctl council roster list --bot scout")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rosterNeedsBot(cmd, bot, "council roster list needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster list --bot scout"); err != nil {
				return err
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			c, err := readRosterCouncil(cmd, fl, bot)
			if err != nil {
				return err
			}
			out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if asJSON {
				return writeStructured(out, "json", rosterRawList(c.raw))
			}
			return printRosterList(out, errw, c, bot)
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose councils to list")
	c.Flags().BoolVar(&asJSON, "json", false, "print the councils as the platform sent them, as JSON")
	return c
}

// rosterRawList is the councils as one JSON array, each entry exactly as the hub sent it.
func rosterRawList(raw []json.RawMessage) json.RawMessage {
	parts := make([][]byte, 0, len(raw))
	for _, e := range raw {
		parts = append(parts, e)
	}
	return json.RawMessage(append(append([]byte("["), bytes.Join(parts, []byte(","))...), ']'))
}

// printRosterList prints a bot's councils as a table, and on stderr the reasons of those that cannot
// answer, the missing default and the missing council of the bot's own.
func printRosterList(out, errw io.Writer, c *rosterCouncil, bot string) error {
	if len(c.view.Rosters) == 0 {
		_, _ = fmt.Fprintf(errw, "· no councils for %s\n", escapeLine(bot))
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "NAME\tDISPLAY NAME\tSCOPE\tLEAD\tMEMBERS\tREVIEWER\tDEFAULT\tSTATUS\n")
	for _, r := range c.view.Rosters {
		members := "-"
		if len(r.Members) > 0 {
			parts := make([]string, 0, len(r.Members))
			for _, m := range r.Members {
				parts = append(parts, councilCell(m))
			}
			members = strings.Join(parts, ",")
		}
		reviewer := "-"
		if r.Reviewer != nil && *r.Reviewer != "" {
			reviewer = councilCell(*r.Reviewer)
		}
		def := "-"
		if r.IsDefault {
			def = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", councilCell(r.Name),
			councilOr(councilName(r.DisplayName)), councilCell(r.Scope), councilOr(councilCell(r.Lead)),
			members, reviewer, def, councilCell(councilStatus(r.Status)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, r := range c.view.Rosters {
		if r.Status != "invalid" {
			continue
		}
		if reasons := councilReasons(r.Reasons); reasons != "" {
			_, _ = fmt.Fprintf(errw, "· %s: %s\n", escapeLine(r.Name), reasons)
		} else {
			_, _ = fmt.Fprintf(errw, "· %s\n", escapeLine(r.Name))
		}
	}
	anyDefault, anyBot := false, false
	for _, r := range c.view.Rosters {
		if r.IsDefault {
			anyDefault = true
		}
		if r.Scope == "bot" {
			anyBot = true
		}
	}
	if !anyDefault {
		if c.view.CanEdit.OK {
			_, _ = fmt.Fprintf(errw, "· %s's default council is gone; tnctl council roster use <council> --bot %s makes another one the default\n", escapeLine(bot), escapeLine(bot))
		} else {
			_, _ = fmt.Fprintf(errw, "· %s's default council is gone; the bot's creator or a teamspace admin makes another one the default\n", escapeLine(bot))
		}
	}
	if !anyBot && c.view.CanEdit.OK {
		_, _ = fmt.Fprintf(errw, "· %s has no council of its own yet; tnctl council roster add <council> <member> --bot %s --as %s saves a copy with that member\n",
			escapeLine(bot), escapeLine(bot), deriveRosterName(bot, c.taken()))
	}
	return nil
}

// ---------- council roster get ----------

func newCouncilRosterGetCmd(e *cliEnv) *cobra.Command {
	var bot string
	c := &cobra.Command{
		Use:   "get <name> --bot <bot>",
		Short: "Show one council: its lead, members, reviewer and status",
		Long: `Show one of a bot's councils: its status and budget, then one row per member: the lead, the
members in order and the reviewer, with each one's scope, version (pinned when the council asks
for that version), model and status. A member that was archived or deleted shows as gone.

-o json prints the council as the platform sent it. -o yaml prints its council file as the
platform exports it, to edit and apply with tnctl council apply -f; a premade council's file
needs a new metadata.name, spec.scope: bot and spec.bot: <bot>, and no spec.version, before it
can be applied.`,
		Example: "  tnctl council roster get ops --bot scout\n" +
			"  tnctl council roster get ops-scout --bot scout -o yaml > ops-scout.yaml",
		SilenceUsage: true,
		Args:         argsRange(1, 1, "council roster get needs one council's name, for example: tnctl council roster get ops --bot scout"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRosterGet(cmd, e, args[0], bot)
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose council to show")
	return c
}

// runRosterGet shows one of a bot's councils.
func runRosterGet(cmd *cobra.Command, e *cliEnv, name, bot string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if err := rosterNeedsBot(cmd, bot, "council roster get needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster get ops --bot scout"); err != nil {
		return err
	}
	if err := rosterCheckName(name); err != nil {
		return err
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	c, err := readRosterCouncil(cmd, fl, bot)
	if err != nil {
		return err
	}
	i := c.find(name)
	if i < 0 {
		return c.missing(name, bot)
	}
	out := cmd.OutOrStdout()
	switch format {
	case "json":
		return writeStructured(out, "json", councilRawAt(c.raw, i))
	case "yaml":
		b, err := fl.RosterExport(cmd.Context(), bot, name)
		if err != nil {
			return rosterHints(err, map[string]string{"role_gone": rosterGoneHint})
		}
		_, err = out.Write(b)
		return err
	}
	return printRosterGet(out, c.view.Rosters[i])
}

// printRosterGet prints a council as key: value lines, then one row per member.
func printRosterGet(out io.Writer, v api.RosterView) error {
	line := func(key, value string) { _, _ = fmt.Fprintf(out, "%-13s%s\n", key+":", value) }
	line("council", chatCouncilName(v.Name, v.DisplayName))
	line("scope", councilOr(escapeLine(v.Scope)))
	line("version", strconv.Itoa(v.Version))
	if v.IsDefault {
		line("default", "yes")
	} else {
		line("default", "no")
	}
	status := councilStatus(v.Status)
	if reasons := councilReasons(v.Reasons); v.Status == "invalid" && reasons != "" {
		status += ": " + reasons
	}
	line("status", status)
	budget := "no council cap"
	if v.Budget != nil && v.Budget.MaxTokens > 0 {
		budget = fmt.Sprintf("%d tokens per question", v.Budget.MaxTokens)
	}
	line("budget", budget)
	description := "-"
	if v.Description != nil {
		if text := hubText(*v.Description); text != "" {
			description = text
		}
	}
	line("description", description)
	_, _ = fmt.Fprintf(out, "\n")
	return printRosterMembers(out, rosterMemberRows(v))
}

// rosterMemberRow is one row of a council's member table.
type rosterMemberRow struct {
	part, slug, name, scope, version, model, effort, status string
}

// rosterMemberRows is a council's members, in order: the lead, the members and the reviewer.
func rosterMemberRows(v api.RosterView) []rosterMemberRow {
	var rows []rosterMemberRow
	if v.Refs != nil {
		if v.Refs.Lead != nil {
			rows = append(rows, rosterRefRow(v, "lead", v.Refs.Lead))
		}
		for i := range v.Refs.Members {
			rows = append(rows, rosterRefRow(v, "member", &v.Refs.Members[i]))
		}
		if v.Refs.Reviewer != nil {
			rows = append(rows, rosterRefRow(v, "reviewer", v.Refs.Reviewer))
		}
		return rows
	}
	rows = append(rows, rosterSlugRow(v, "lead", v.Lead))
	for _, m := range v.Members {
		rows = append(rows, rosterSlugRow(v, "member", m))
	}
	if v.Reviewer != nil && *v.Reviewer != "" {
		rows = append(rows, rosterSlugRow(v, "reviewer", *v.Reviewer))
	}
	return rows
}

// rosterRefRow is one stored reference as a row: its pin and scope, and the member's view when the
// reference is not gone.
func rosterRefRow(v api.RosterView, part string, f *api.RosterRefView) rosterMemberRow {
	row := rosterMemberRow{part: part, slug: councilCell(f.Role), name: "-", scope: "-", version: "-", model: "-", effort: "-", status: "-"}
	if f.Scope != nil {
		row.scope = councilCell(*f.Scope)
	}
	if f.Version != nil {
		row.version = fmt.Sprintf("%d (pinned)", *f.Version)
	}
	if f.Gone {
		row.status = "gone"
		return row
	}
	for _, r := range v.Roles {
		if r.ID != f.RoleID {
			continue
		}
		if f.Scope == nil {
			row.scope = councilCell(r.Scope)
		}
		row.name = councilOr(councilName(r.Name))
		if f.Version == nil {
			row.version = strconv.Itoa(r.Version)
		}
		row.model = councilOr(hubText(r.Model))
		row.effort = councilOr(hubText(r.Effort))
		row.status = councilCell(councilStatus(r.Status))
		return row
	}
	return row
}

// rosterSlugRow is one reference of an older platform's answer, matched to its view by slug and part.
func rosterSlugRow(v api.RosterView, part, slug string) rosterMemberRow {
	row := rosterMemberRow{part: part, slug: councilCell(slug), name: "-", scope: "-", version: "-", model: "-", effort: "-", status: "gone"}
	for _, r := range v.Roles {
		if r.Slug != slug || r.Kind != part {
			continue
		}
		row.name = councilOr(councilName(r.Name))
		row.scope = councilCell(r.Scope)
		row.version = strconv.Itoa(r.Version)
		row.model = councilOr(hubText(r.Model))
		row.effort = councilOr(hubText(r.Effort))
		row.status = councilCell(councilStatus(r.Status))
		return row
	}
	return row
}

// printRosterMembers prints a council's members as a table.
func printRosterMembers(out io.Writer, rows []rosterMemberRow) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "PART\tSLUG\tNAME\tSCOPE\tVERSION\tMODEL\tEFFORT\tSTATUS\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.part, r.slug, r.name, r.scope, r.version,
			r.model, r.effort, r.status)
	}
	return tw.Flush()
}

// ---------- council roster use ----------

func newCouncilRosterUseCmd(e *cliEnv) *cobra.Command {
	var bot string
	c := &cobra.Command{
		Use:   "use <name> --bot <bot>",
		Short: "Make a council the bot's default: the one a new chat asks",
		Long: `Make a council the bot's default. A new chat asks the default council unless it names another
(tnctl chat --roster). Only the bot's creator or a teamspace admin may change it (exit 6
otherwise). -o json|yaml prints the bot's council as the platform answers.`,
		Example:      "  tnctl council roster use ops --bot scout",
		SilenceUsage: true,
		Args:         argsRange(1, 1, "council roster use needs one council's name, for example: tnctl council roster use ops --bot scout"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRosterUse(cmd, e, args[0], bot)
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose default council to set")
	return c
}

// runRosterUse makes one of a bot's councils the default.
func runRosterUse(cmd *cobra.Command, e *cliEnv, name, bot string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if err := rosterNeedsBot(cmd, bot, "council roster use needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster use ops --bot scout"); err != nil {
		return err
	}
	if err := rosterCheckName(name); err != nil {
		return err
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	cv, raw, err := fl.SetDefaultRoster(cmd.Context(), bot, name)
	if err != nil {
		return rosterHints(err, map[string]string{"unknown_roster": rosterListHint(bot)})
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if format != "table" {
		if err := writeStructured(out, format, raw); err != nil {
			return err
		}
	} else {
		shown := chatCouncilName(cv.Roster.Name, cv.Roster.DisplayName)
		if shown == "" {
			shown = escapeLine(name)
		}
		_, _ = fmt.Fprintf(out, "%s's default council is now %s\n", escapeLine(bot), shown)
	}
	if cv.Roster.Status == "invalid" {
		_, _ = fmt.Fprintf(errw, "· council %s%s\n", escapeLine(name), chatUnavailable(cv.Roster.Status, cv.Roster.Reasons))
	}
	return nil
}

// ---------- council roster add and remove ----------

// rosterChange is one roster add or remove, as the person asked for it, checked.
type rosterChange struct {
	add         bool   // add, else remove
	bot, name   string // the bot, and the council to change
	ref         string // the member as typed
	scope, slug string // the member's scope ("" when not given) and slug
	as          string // --as; "" when not given
	format      string // table, json or yaml
}

func newCouncilRosterAddCmd(e *cliEnv) *cobra.Command { return newCouncilRosterChangeCmd(e, true) }

func newCouncilRosterRemoveCmd(e *cliEnv) *cobra.Command { return newCouncilRosterChangeCmd(e, false) }

// newCouncilRosterChangeCmd builds council roster add and council roster remove.
func newCouncilRosterChangeCmd(e *cliEnv, add bool) *cobra.Command {
	var bot, scope, as string
	var short, long, example, argsText string
	if add {
		short = "Add a member to a council"
		example = "  tnctl council roster add ops-scout triage --bot scout\n" +
			"  tnctl council roster add standard stream-watcher --bot scout --as scout-council"
		argsText = "council roster add needs a council and a member, for example: tnctl council roster add ops-scout triage --bot scout"
		long = `Add a member to one of a bot's councils, after its members. <member> is a slug such as triage, or
<scope>/<slug> (scope bot, teamspace or template) when more than one member has that slug; tnctl
council role list --bot <bot> --templates lists them.

A premade council cannot be changed: --as <new-name> saves a copy with the change, and when the
premade council was the bot's default, the copy becomes the default. On one of the bot's own
councils, --as saves a copy and leaves the council as it was.

A council has a lead, 1 to 5 members and, when the Coder is a member, a reviewer: the platform
checks the changed council. -o json|yaml prints the council as the platform answers.

Exit codes: 0 done, or nothing to change; 1 the council names a member that was archived or
deleted; 2 wrong arguments, an unknown council or member, a slug more than one member has, a
premade council without --as, or an --as name that is taken; 3 no bot of that name; 5 the
platform refused the changed council; 6 not allowed; 8 someone changed the council meanwhile:
run it again.`
	} else {
		short = "Remove a member from a council"
		example = "  tnctl council roster remove ops-scout triage --bot scout"
		argsText = "council roster remove needs a council and a member, for example: tnctl council roster remove ops-scout triage --bot scout"
		long = `Remove a member from one of a bot's councils. <member> is the member's slug, such as triage, or
<scope>/<slug>. The lead and the reviewer are not members: the reviewer is changed in the web
chat's Council tab or in YAML (tnctl council apply), the lead in YAML for now.

A premade council cannot be changed: --as <new-name> saves a copy with the change, and when the
premade council was the bot's default, the copy becomes the default. On one of the bot's own
councils, --as saves a copy and leaves the council as it was.

A council keeps at least one member: the platform checks the changed council. -o json|yaml
prints the council as the platform answers.

Exit codes: 0 done; 1 the council names another member that was archived or deleted; 2 wrong
arguments, an unknown council, a member that is not in it, a premade council without --as, or an
--as name that is taken; 3 no bot of that name; 5 the platform refused the changed council; 6 not
allowed; 8 someone changed the council meanwhile: run it again.`
	}
	verb := "add"
	if !add {
		verb = "remove"
	}
	c := &cobra.Command{
		Use:          verb + " <name> <member> --bot <bot>",
		Short:        short,
		Long:         long,
		Example:      example,
		SilenceUsage: true,
		Args:         argsRange(2, 2, argsText),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, err := checkRosterChange(cmd, add, args, bot, scope, as)
			if err != nil {
				return err
			}
			return runRosterChange(cmd, e, ch)
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose council to change")
	c.Flags().StringVar(&scope, "scope", "", "the member's scope when more than one member has its slug: bot, teamspace or template")
	c.Flags().StringVar(&as, "as", "", "save the changed council as a new council with this name (a premade council needs it)")
	return c
}

// checkRosterChange runs add's and remove's local checks in their fixed order, before any request.
func checkRosterChange(cmd *cobra.Command, add bool, args []string, bot, scope, as string) (rosterChange, error) {
	var ch rosterChange
	format, err := outputFormat()
	if err != nil {
		return ch, err
	}
	ch.add, ch.bot, ch.name, ch.ref, ch.format = add, bot, args[0], args[1], format
	verb, example := "add", "tnctl council roster add ops-scout triage --bot scout"
	if !add {
		verb, example = "remove", "tnctl council roster remove ops-scout triage --bot scout"
	}
	if err := rosterNeedsBot(cmd, bot, fmt.Sprintf("council roster %s needs --bot <bot>: a council belongs to one bot, for example: %s", verb, example)); err != nil {
		return ch, err
	}
	if err := rosterCheckName(ch.name); err != nil {
		return ch, err
	}
	memberScope, slug, ok := parseMemberRef(ch.ref)
	if !ok {
		return ch, usageErr("%q is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template", ch.ref)
	}
	ch.slug = slug
	ch.scope = memberScope
	if cmd.Flags().Changed("scope") {
		switch scope {
		case "bot", "teamspace", "template":
		default:
			return ch, usageErr("--scope is bot, teamspace or template, not %q", scope)
		}
		if memberScope != "" && memberScope != scope {
			return ch, usageErr("--scope %s and %q name different scopes", scope, ch.ref)
		}
		ch.scope = scope
	}
	ch.as = as
	if cmd.Flags().Changed("as") && !councilSlugRE.MatchString(as) {
		return ch, usageErr("--as is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not %q", as)
	}
	return ch, nil
}

// runRosterChange adds or removes one member of one of a bot's councils.
func runRosterChange(cmd *cobra.Command, e *cliEnv, ch rosterChange) error {
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	bot := escapeLine(ch.bot)
	c, err := readRosterCouncil(cmd, fl, ch.bot)
	if err != nil {
		return err
	}
	i := c.find(ch.name)
	if i < 0 {
		return c.missing(ch.name, ch.bot)
	}
	src := c.view.Rosters[i]
	if !c.view.CanEdit.OK {
		reason := hubText(c.view.CanEdit.Reason)
		if reason == "" {
			reason = "you may not change " + bot + "'s councils"
		}
		return newErr(codeAuth, "%s", reason)
	}
	if src.Scope == "template" && ch.as == "" {
		return usageErr("'%s' is a premade council and cannot be changed: use --as %s to save a copy", ch.name, deriveRosterName(ch.bot, c.taken()))
	}
	if ch.as != "" {
		if err := rosterAsTaken(ch, c, src); err != nil {
			return err
		}
	}
	if src.Refs != nil {
		if part, f := rosterGoneRef(ch, src); f != nil {
			return rosterGoneError(ch, src, part, f)
		}
	}
	b, err := fl.RosterExport(cmd.Context(), ch.bot, ch.name)
	if err != nil {
		return rosterHints(err, map[string]string{"role_gone": rosterGoneHint})
	}
	spec, err := api.DecodeRosterExport(b)
	if err != nil {
		return newErr(codeError, "%s", escapeLine(err.Error()))
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if ch.add {
		done, err := rosterAddMember(ch, spec, c, i, out, errw)
		if err != nil || done {
			return err
		}
	} else if err := rosterRemoveMember(cmd, ch, spec); err != nil {
		return err
	}
	return rosterPut(cmd, fl, ch, src, spec, out, errw)
}

// rosterGoneRef is the first reference of the council that names a member that is gone. A remove of
// exactly that member is its fix and is skipped: only a member reference, never the lead or the
// reviewer, and only when the scope matches (when it was given).
func rosterGoneRef(ch rosterChange, src api.RosterView) (part string, f *api.RosterRefView) {
	skipped := func(f *api.RosterRefView) bool {
		return !ch.add && f.Role == ch.slug && (ch.scope == "" || (f.Scope != nil && *f.Scope == ch.scope))
	}
	if v := src.Refs.Lead; v != nil && v.Gone {
		return "lead", v
	}
	for i := range src.Refs.Members {
		if v := &src.Refs.Members[i]; v.Gone && !skipped(v) {
			return "member", v
		}
	}
	if v := src.Refs.Reviewer; v != nil && v.Gone {
		return "reviewer", v
	}
	return "", nil
}

// rosterGoneError refuses a change to a council that names a member that is gone, and names the step
// that fixes it.
func rosterGoneError(ch rosterChange, src api.RosterView, part string, f *api.RosterRefView) error {
	slug, name := escapeLine(f.Role), escapeLine(ch.name)
	var line1 string
	switch part {
	case "lead":
		line1 = fmt.Sprintf("council %s has a lead that was archived or deleted: %s", name, slug)
	case "reviewer":
		line1 = fmt.Sprintf("council %s has a reviewer that was archived or deleted: %s", name, slug)
	default:
		line1 = fmt.Sprintf("council %s names a member that was archived or deleted: %s", name, slug)
	}
	line2 := rosterGoneHint
	if part == "member" && src.Scope == "bot" && councilSlugRE.MatchString(f.Role) && rosterGoneCount(src) == 1 {
		line2 = fmt.Sprintf("take it out first: tnctl council roster remove %s %s --bot %s", name, slug, escapeLine(ch.bot))
	}
	return newErr(codeError, "%s\n%s", line1, line2)
}

// rosterGoneCount is how many of the council's references name a member that is gone: the lead, the
// members and the reviewer, the one a remove skips included. Taking one member out fixes the council
// only when it is the only one.
func rosterGoneCount(src api.RosterView) int {
	n := 0
	if v := src.Refs.Lead; v != nil && v.Gone {
		n++
	}
	for i := range src.Refs.Members {
		if src.Refs.Members[i].Gone {
			n++
		}
	}
	if v := src.Refs.Reviewer; v != nil && v.Gone {
		n++
	}
	return n
}

// rosterUnchangedBody is the answer to print for a council that already has the member: the council as
// the GET answered it, and the hub's "unchanged".
func rosterUnchangedBody(raw []json.RawMessage, i int) json.RawMessage {
	entry := councilRawAt(raw, i)
	if entry == nil {
		entry = json.RawMessage("{}")
	}
	return json.RawMessage(`{"roster":` + string(entry) + `,"action":"unchanged"}`)
}

// rosterAddMember adds the member to the spec. done is true when the council already has it: nothing
// is sent, and the answer is already printed.
func rosterAddMember(ch rosterChange, spec *api.RosterSpec, c *rosterCouncil, i int, out, errw io.Writer) (done bool, err error) {
	for _, m := range spec.Members {
		if m.Role != ch.slug {
			continue
		}
		if ch.scope != "" && m.Scope != "" && m.Scope != ch.scope {
			return false, usageErr("%s has a member '%s' already (%s/%s): member slugs are unique in a council", ch.name, ch.slug, escapeLine(m.Scope), ch.slug)
		}
		if ch.as != "" {
			_, _ = fmt.Fprintf(errw, "· %s is already a member of %s: nothing to change, so %s was not saved\n", escapeLine(ch.slug), escapeLine(ch.name), ch.as)
		} else {
			_, _ = fmt.Fprintf(errw, "· %s is already a member of %s: nothing to change\n", escapeLine(ch.slug), escapeLine(ch.name))
		}
		if ch.format != "table" {
			if err := writeStructured(out, ch.format, rosterUnchangedBody(c.raw, i)); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	spec.Members = append(spec.Members, api.RosterRef{Role: ch.slug, Scope: ch.scope})
	return false, nil
}

// rosterRemoveMember drops the member from the spec; a member that is not there is a usage error.
func rosterRemoveMember(cmd *cobra.Command, ch rosterChange, spec *api.RosterSpec) error {
	keep := make([]api.RosterRef, 0, len(spec.Members))
	removed := false
	for _, m := range spec.Members {
		if !removed && m.Role == ch.slug && (ch.scope == "" || m.Scope == ch.scope) {
			removed = true
			continue
		}
		keep = append(keep, m)
	}
	if removed {
		spec.Members = keep
		return nil
	}
	switch {
	case spec.Lead.Role == ch.slug:
		return usageErr("'%s' leads %s; it is not a member: the lead is changed in YAML (tnctl council apply) for now", ch.slug, ch.name)
	case spec.Reviewer != nil && spec.Reviewer.Role == ch.slug:
		return usageErr("'%s' is the reviewer of %s, not a member: change the reviewer in the web chat's Council tab or in YAML (tnctl council apply)", ch.slug, ch.name)
	}
	ref := ch.ref
	if !strings.Contains(ch.ref, "/") && cmd.Flags().Changed("scope") {
		ref = ch.scope + "/" + ch.ref
	}
	names := make([]string, 0, len(spec.Members))
	for _, m := range spec.Members {
		names = append(names, escapeLine(m.Role))
	}
	return usageErr("'%s' is not a member of %s. Members: %s.", ref, ch.name, strings.Join(names, ", "))
}

// rosterPut writes the changed council, then a copy's default switch, and prints the answers.
func rosterPut(cmd *cobra.Command, fl *api.Fleet, ch rosterChange, src api.RosterView, spec *api.RosterSpec, out, errw io.Writer) error {
	target := ch.name
	var body api.RosterPut
	if ch.as != "" {
		target = ch.as
		copied := *spec
		copied.DisplayName = ch.as
		zero := 0
		body = api.RosterPut{Spec: copied, ExpectVersion: &zero}
	} else {
		v := src.Version
		body = api.RosterPut{Spec: *spec, ExpectVersion: &v, ExpectID: src.ID}
	}
	w, raw, err := fl.PutRoster(cmd.Context(), ch.bot, target, body)
	if err != nil {
		hints := map[string]string{}
		if ch.add {
			hints["unknown_member"] = fmt.Sprintf("tnctl council role list --bot %s --templates lists the members %s can use",
				escapeLine(ch.bot), escapeLine(ch.bot))
			hint := fmt.Sprintf("name its scope (bot, teamspace or template): tnctl council roster add %s <scope>/%s --bot %s",
				escapeLine(ch.name), escapeLine(ch.slug), escapeLine(ch.bot))
			if ch.as != "" {
				hint += " --as " + escapeLine(ch.as)
			}
			hints["ambiguous"] = hint
		}
		return rosterHints(err, hints)
	}
	action := hubText(w.Action)
	if action == "" {
		action = "saved"
	}
	targetName := escapeLine(target)
	members := councilJoined(w.Roster.Members)
	if ch.format != "table" {
		if err := writeStructured(out, ch.format, raw); err != nil {
			return err
		}
	} else if ch.as != "" {
		_, _ = fmt.Fprintf(out, "%s council %s (version %d) for %s, a copy of %s; members: %s\n", action, targetName,
			w.Roster.Version, escapeLine(ch.bot), escapeLine(ch.name), members)
	} else {
		_, _ = fmt.Fprintf(out, "%s council %s (version %d) for %s; members: %s\n", action, targetName,
			w.Roster.Version, escapeLine(ch.bot), members)
	}
	if w.Roster.Status == "invalid" {
		_, _ = fmt.Fprintf(errw, "· council %s%s\n", targetName, chatUnavailable(w.Roster.Status, w.Roster.Reasons))
	}
	if ch.as == "" || src.Scope != "template" || !src.IsDefault {
		return nil
	}
	cv, _, err := fl.SetDefaultRoster(cmd.Context(), ch.bot, target)
	if err != nil {
		mapped := councilError(err)
		return newErr(exitCodeOf(mapped), "%s\ncouncil %s was saved, but it is not the default: tnctl council roster use %s --bot %s makes it the default",
			mapped.Error(), targetName, targetName, escapeLine(ch.bot))
	}
	shown := chatCouncilName(cv.Roster.Name, cv.Roster.DisplayName)
	if shown == "" {
		shown = targetName
	}
	if ch.format != "table" {
		_, _ = fmt.Fprintf(errw, "· %s's default council is now %s\n", escapeLine(ch.bot), shown)
	} else {
		_, _ = fmt.Fprintf(out, "%s's default council is now %s\n", escapeLine(ch.bot), shown)
	}
	return nil
}

// ---------- council roster delete ----------

func newCouncilRosterDeleteCmd(e *cliEnv) *cobra.Command {
	var bot string
	c := &cobra.Command{
		Use:   "delete <name> --bot <bot>",
		Short: "Delete one of a bot's own councils",
		Long: `Delete one of a bot's own councils. Chats that asked it go back to the bot's default council.
The default council cannot be deleted: make another one the default first (tnctl council roster
use). Premade councils cannot be deleted. tnctl asks nothing: to keep a copy, save it first with
tnctl council roster get <name> --bot <bot> -o yaml > <name>.yaml; tnctl council apply -f
<name>.yaml makes it again.`,
		Example:      "  tnctl council roster delete ops-scout --bot scout",
		SilenceUsage: true,
		Args:         argsRange(1, 1, "council roster delete needs one council's name, for example: tnctl council roster delete ops-scout --bot scout"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := rosterNeedsBot(cmd, bot, "council roster delete needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster delete ops-scout --bot scout"); err != nil {
				return err
			}
			if err := rosterCheckName(name); err != nil {
				return err
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			if err := fl.DeleteRoster(cmd.Context(), bot, name); err != nil {
				return rosterHints(err, map[string]string{
					"roster_default": rosterUseHint(bot),
					"unknown_roster": rosterListHint(bot),
				})
			}
			out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
			_, _ = fmt.Fprintf(out, "deleted council %s of %s\n", escapeLine(name), escapeLine(bot))
			_, _ = fmt.Fprintf(errw, "· chats that asked it now ask %s's default council\n", escapeLine(bot))
			return nil
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose council to delete")
	return c
}

// ---------- the group ----------

// newCouncilRosterCmd is "tnctl council roster": a bot's councils.
func newCouncilRosterCmd(e *cliEnv) *cobra.Command {
	c := &cobra.Command{
		Use:          "roster",
		Short:        "List, read and change a bot's councils, and pick its default",
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, args []string) error { return cmd.Help() },
		Args:         councilGroupArgs,
	}
	c.AddCommand(newCouncilRosterListCmd(e), newCouncilRosterGetCmd(e), newCouncilRosterUseCmd(e),
		newCouncilRosterAddCmd(e), newCouncilRosterRemoveCmd(e), newCouncilRosterDeleteCmd(e), newCouncilApplyCmd(e))
	return c
}

// rosterMemberArg is the member as a command line names it again: the slug with its scope when one was
// given, by the member or by --scope.
func rosterMemberArg(ch rosterChange) string {
	if ch.scope != "" {
		return ch.scope + "/" + ch.slug
	}
	return ch.slug
}

// rosterAsTaken refuses an --as name the bot already has (exit 2, naming the derived name). --as naming
// the council being changed, or another council of the bot's own, also names the command that changes
// that council in place.
func rosterAsTaken(ch rosterChange, c *rosterCouncil, src api.RosterView) error {
	taken := c.taken()
	if !taken[ch.as] {
		return nil
	}
	bot, derived := escapeLine(ch.bot), deriveRosterName(ch.bot, taken)
	verb := "add"
	if !ch.add {
		verb = "remove"
	}
	inPlace := fmt.Sprintf("tnctl council roster %s %s %s --bot %s", verb, ch.as, rosterMemberArg(ch), bot)
	if ch.as == ch.name {
		if src.Scope == "template" {
			return usageErr("'%s' is a premade council and cannot be changed: use --as %s to save a copy", ch.name, derived)
		}
		return usageErr("--as %s names the council you are changing: leave out --as to change it (%s), or give the copy a new name, such as %s", ch.as, inPlace, derived)
	}
	if j := c.find(ch.as); j >= 0 && c.view.Rosters[j].Scope == "bot" {
		return usageErr("there is a council '%s' for %s already: to change it, %s; to save another copy, pick another name with --as, such as %s", ch.as, bot, inPlace, derived)
	}
	return usageErr("there is a council '%s' for %s already: pick another name with --as, such as %s", ch.as, bot, derived)
}
