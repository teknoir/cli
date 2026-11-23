package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// councilSlugRE is a member's slug and a council's name (the hub's SLUG_RE).
var councilSlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}$`)

// parseMemberRef splits "slug" or "<scope>/slug" (scope bot, teamspace or template). ok is false when
// the scope is another word, the slug does not match councilSlugRE, or there is more than one "/".
func parseMemberRef(ref string) (scope, slug string, ok bool) {
	scope, slug = "", ref
	if i := strings.Index(ref, "/"); i >= 0 {
		scope, slug = ref[:i], ref[i+1:]
		switch scope {
		case "bot", "teamspace", "template":
		default:
			return "", "", false
		}
		if strings.Contains(slug, "/") {
			return "", "", false
		}
	}
	if !councilSlugRE.MatchString(slug) {
		return "", "", false
	}
	return scope, slug, true
}

// hubText is text from the hub made safe for one terminal line.
func hubText(s string) string { return terminalText(cleanBotText(s)) }

// councilStatus is a member's or a council's status in the web UI's words: ok, unavailable (invalid),
// not checked (unchecked); any other value through escapeLine.
func councilStatus(s string) string {
	switch s {
	case "ok":
		return "ok"
	case "invalid":
		return "unavailable"
	case "unchecked":
		return "not checked"
	}
	return escapeLine(s)
}

// councilBot checks a bot name (--bot, or tnctl chat's <bot>) before any request: the hub's bot-name
// rule (isDNSLabel in cmd/robot_add.go). A mismatch is a usage error.
func councilBot(bot string) error {
	if !isDNSLabel(bot) {
		return usageErr("%q is not a bot name: use the bot's name, such as scout", bot)
	}
	return nil
}

// councilTime formats a hub time (milliseconds since the epoch) as "2006-01-02 15:04 UTC".
func councilTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC")
}

// councilUsedBy is the councils a member is in, in the web UI's words.
func councilUsedBy(u []api.CouncilUse, hidden int) string {
	parts := make([]string, 0, len(u)+1)
	for _, c := range u {
		parts = append(parts, escapeLine(c.Bot)+" ("+hubText(c.DisplayName)+")")
	}
	if hidden > 0 {
		switch {
		case len(parts) > 0:
			parts = append(parts, fmt.Sprintf("%d more you cannot see", hidden))
		case hidden == 1:
			parts = append(parts, "1 council you cannot see")
		default:
			parts = append(parts, fmt.Sprintf("%d councils you cannot see", hidden))
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// councilErrorCode is the exit code of each hub council code. The code alone decides it, never the
// HTTP status: the hub answers the same failure with 400, 404, 409 or 422 as it sees fit.
var councilErrorCode = map[string]int{
	"unknown_member": codeUsage,
	"unknown_roster": codeUsage,
	"ambiguous":      codeUsage,

	"council_outdated": codeError,
	"role_invalid":     codeError,
	"roster_gone":      codeError,
	"role_gone":        codeError,
	"pin_empty":        codeError,
	"creator_only":     codeError,
	"council_off":      codeError,

	"conflict":  codeConflict,
	"exists":    codeConflict,
	"turn_open": codeConflict,

	// The bot's state and the open-turn limit, at a turn's start: the platform's sentence says what
	// to do, so it is printed without "(HTTP n)"; the exit codes are those of their statuses (409, 429).
	"bot_not_ready":  codeConflict,
	"bot_suspended":  codeConflict,
	"too_many_turns": codeRateLimit,

	"invalid":            codeRejected,
	"bad_request":        codeRejected,
	"template_read_only": codeRejected,
	"limit":              codeRejected,
	"role_in_use":        codeRejected,
	"roster_default":     codeRejected,
	"too_large":          codeRejected,

	"not_admin":                codeAuth,
	"council_edit_not_allowed": codeAuth,
	"chat_not_allowed":         codeAuth,
	"not_author":               codeAuth,
	"bot_caller":               codeAuth,

	"not_found": codeNotFound,
}

// councilError maps an error of a council route to tnctl's exit code and the hub's own words.
func councilError(err error) error {
	ce, ok := api.CouncilErrorOf(err)
	if !ok || ce.Code == "" {
		return classify(err)
	}
	code, known := councilErrorCode[ce.Code]
	if !known {
		return classify(err)
	}
	var lines []string
	// The hub's top message is the field messages joined: the fields say it better. A field without
	// words is left out; with none left, the top message, and without that the code.
	for _, f := range ce.Fields {
		msg := hubText(f.Message)
		if msg == "" {
			continue
		}
		if f.Path == "" || f.Path == "yaml" {
			lines = append(lines, msg)
			continue
		}
		lines = append(lines, escapeLine(f.Path)+": "+msg)
	}
	if len(lines) == 0 {
		msg := hubText(ce.Message)
		if msg == "" {
			msg = "the platform refused the request (" + escapeLine(ce.Code) + ")"
		}
		lines = append(lines, msg)
	}
	if ce.Document != nil {
		lines[0] = fmt.Sprintf("document %d: %s", *ce.Document+1, lines[0])
	}
	if ce.Code == "role_in_use" {
		if used := councilUsedBy(ce.UsedBy, ce.HiddenCount); used != "" {
			lines = append(lines, "used by "+used)
		}
	}
	return newErr(code, "%s", strings.Join(lines, "\n"))
}

// councilMessage is the hub's own message of a council error, "" when err is none.
func councilMessage(err error) string {
	if ce, ok := api.CouncilErrorOf(err); ok {
		return ce.Message
	}
	return ""
}

// councilNotFound reports whether err is the hub's not_found: a template fallback keys on the code,
// never on the status.
func councilNotFound(err error) bool {
	ce, ok := api.CouncilErrorOf(err)
	return ok && ce.Code == "not_found"
}

// councilGroupArgs refuses a positional argument to a command group: a mistyped subcommand is a usage
// error, not the group's help with exit 0. It suggests the closest visible subcommands, as cobra does
// at the root.
func councilGroupArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	// cobra sets its edit-distance default (2) only for the root's own check; without it,
	// SuggestionsFor matches prefixes only, so "rle" would get no suggestion.
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		return usageErr("unknown command %q for %q: did you mean %s?", args[0], cmd.CommandPath(), orList(s))
	}
	return usageErr("unknown command %q for %q", args[0], cmd.CommandPath())
}

// newCouncilRoleCmd is "tnctl council role": the members a bot's councils can use.
func newCouncilRoleCmd(e *cliEnv) *cobra.Command {
	c := &cobra.Command{
		Use:          "role",
		Short:        "List, read, make and archive the members a bot's councils can use",
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, args []string) error { return cmd.Help() },
		Args:         councilGroupArgs,
	}
	c.AddCommand(newCouncilRoleListCmd(e), newCouncilRoleGetCmd(e), newCouncilRoleExportCmd(e), newCouncilRoleNewCmd(e), newCouncilRoleArchiveCmd(e), newCouncilApplyCmd(e))
	return c
}

// councilRow is one member of a list: the view the table shows, and the entry the hub sent, which
// -o json prints unchanged (a field tnctl does not know must survive).
type councilRow struct {
	view api.RoleView
	raw  json.RawMessage
}

// ---------- council role list ----------

func newCouncilRoleListCmd(e *cliEnv) *cobra.Command {
	var bot string
	var templates bool
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the members a bot's councils can use",
		Long: `List the members a bot's councils can use: with --bot, the bot's own members and the teamspace's
shared ones; without it, only the teamspace's shared members. --templates lists the premade
members too, first. SCOPE is bot, teamspace or template (a member's <scope>/<slug>). STATUS is ok,
unavailable (the reasons follow on stderr) or not checked. --json prints the members as the
platform sent them.`,
		Example: "  tnctl council role list --bot scout\n" +
			"  tnctl council role list --bot scout --templates\n" +
			"  tnctl council role list --templates --json",
		SilenceUsage: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErr("council role list takes no arguments; name a bot with --bot, for example: tnctl council role list --bot scout")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Changed, not bot != "": an empty --bot "$BOT" is a mistake, never the teamspace.
			if cmd.Flags().Changed("bot") {
				if err := councilBot(bot); err != nil {
					return err
				}
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			rows, err := councilListRows(cmd, fl, bot, templates)
			if err != nil {
				return err
			}
			out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if asJSON {
				return writeCouncilRowsJSON(out, rows)
			}
			return printCouncilRows(out, errw, rows, bot, templates)
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose members to list")
	c.Flags().BoolVar(&templates, "templates", false, "list the premade members too")
	c.Flags().BoolVar(&asJSON, "json", false, "print the members as the platform sent them, as JSON")
	return c
}

// councilListRows reads the members the list shows: the bot's available ones, or without --bot the
// teamspace's catalog.
func councilListRows(cmd *cobra.Command, fl *api.Fleet, bot string, templates bool) ([]councilRow, error) {
	ctx := cmd.Context()
	if bot == "" {
		cat, raw, err := fl.CouncilCatalog(ctx)
		if err != nil {
			return nil, councilError(err)
		}
		var ans struct {
			Roles     []json.RawMessage `json:"roles"`
			Templates struct {
				Roles []json.RawMessage `json:"roles"`
			} `json:"templates"`
		}
		_ = json.Unmarshal(raw, &ans)
		rows := make([]councilRow, 0, len(cat.Templates.Roles)+len(cat.Roles))
		if templates {
			for i, d := range cat.Templates.Roles {
				rows = append(rows, councilRow{view: d.RoleView, raw: councilRawAt(ans.Templates.Roles, i)})
			}
		}
		for i, v := range cat.Roles {
			rows = append(rows, councilRow{view: v, raw: councilRawAt(ans.Roles, i)})
		}
		return rows, nil
	}
	cv, raw, err := fl.BotCouncil(ctx, bot)
	if err != nil {
		return nil, councilError(err)
	}
	var ans struct {
		Council struct {
			Available []json.RawMessage `json:"available"`
		} `json:"council"`
	}
	_ = json.Unmarshal(raw, &ans)
	rows := make([]councilRow, 0, len(cv.Available))
	for i, v := range cv.Available {
		if !templates && v.Scope == "template" {
			continue
		}
		rows = append(rows, councilRow{view: v, raw: councilRawAt(ans.Council.Available, i)})
	}
	return rows, nil
}

// councilRawAt is raw[i], or nil when the raw answer is shorter than the decoded one.
func councilRawAt(raw []json.RawMessage, i int) json.RawMessage {
	if i < len(raw) {
		return raw[i]
	}
	return nil
}

// printCouncilRows prints the members as a table, and on stderr the reasons of the ones that cannot be
// offered today. bot is the --bot the rows were read for, "" for the teamspace's catalog.
func printCouncilRows(out, errw io.Writer, rows []councilRow, bot string, templates bool) error {
	if len(rows) == 0 {
		switch {
		case templates:
			_, _ = fmt.Fprintf(errw, "· no members\n")
		case bot == "":
			_, _ = fmt.Fprintf(errw, "· no shared members in this teamspace yet; --templates lists the premade ones, --bot <bot> a bot's own; a teamspace admin makes one with tnctl council role new <name> --job \"…\" --teamspace\n")
		default:
			_, _ = fmt.Fprintf(errw, "· no members for %s yet (its own or the teamspace's); --templates lists the premade ones; the bot's creator or a teamspace admin makes one with tnctl council role new <name> --job \"…\" --bot %s\n", escapeLine(bot), escapeLine(bot))
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "SLUG\tNAME\tSCOPE\tPART\tMODEL\tEFFORT\tTOOLS\tSTATUS\n")
	for _, r := range rows {
		v := r.view
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", councilCell(v.Slug), councilName(v.Name),
			councilCell(v.Scope), hubText(v.Kind), hubText(v.Model), hubText(v.Effort),
			councilGroups(v.Groups), councilCell(councilStatus(v.Status)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, r := range rows {
		if r.view.Status != "invalid" {
			continue
		}
		if reasons := councilReasons(r.view.Reasons); reasons != "" {
			_, _ = fmt.Fprintf(errw, "· %s: %s\n", escapeLine(r.view.Slug), reasons)
		} else {
			_, _ = fmt.Fprintf(errw, "· %s\n", escapeLine(r.view.Slug))
		}
	}
	return nil
}

// writeCouncilRowsJSON prints the selected entries exactly as the hub sent them. json.Marshal would
// give null for none of them and would rewrite <, > and & in the entries.
func writeCouncilRowsJSON(out io.Writer, rows []councilRow) error {
	parts := make([][]byte, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, r.raw)
	}
	raw := append(append([]byte("["), bytes.Join(parts, []byte(","))...), ']')
	return writeStructured(out, "json", raw)
}

// councilName is a member's name for a table cell: at most 30 characters, cut with an ellipsis.
func councilName(name string) string {
	s := hubText(name)
	r := []rune(s)
	if len(r) <= 30 {
		return s
	}
	return string(r[:29]) + "…"
}

// councilCell is a short hub value for one table cell: on one line, without a tab to split the row.
func councilCell(s string) string {
	return strings.ReplaceAll(escapeLine(s), "\t", `\t`)
}

// councilGroups is a member's tool groups for one table cell.
func councilGroups(groups []string) string {
	if len(groups) == 0 {
		return "-"
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, hubText(g))
	}
	return strings.Join(out, ",")
}

// councilReasons is a member's reasons (or notes) for one line, each made safe for the terminal; a
// reason with no words left is left out.
func councilReasons(reasons []string) string {
	out := make([]string, 0, len(reasons))
	for _, s := range reasons {
		if t := hubText(s); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "; ")
}

// ---------- council role get and export ----------

// newCouncilRoleGetCmd is "tnctl council role get".
func newCouncilRoleGetCmd(e *cliEnv) *cobra.Command {
	return newCouncilRoleShowCmd(e, false)
}

// newCouncilRoleExportCmd is "tnctl council role export".
func newCouncilRoleExportCmd(e *cliEnv) *cobra.Command {
	return newCouncilRoleShowCmd(e, true)
}

// newCouncilRoleShowCmd builds both read commands: export is get with the YAML format fixed.
func newCouncilRoleShowCmd(e *cliEnv, export bool) *cobra.Command {
	var bot string
	var template bool
	var version int
	use := "get <member>"
	short := "Show one member: what it does, its model, tools and status"
	long := "Show one member a bot's councils can use: its part, model, tools, access, status and description,\n" +
		"and, when you may read all of it, its instructions and (for a shared member) the councils that use it.\n\n" +
		"<member> is a slug such as triage, or <scope>/<slug> (scope bot, teamspace or template) when\n" +
		"more than one member has that slug. Without --bot it reads the teamspace's shared member, or the\n" +
		"premade one when there is no shared member of that name; a bot's own member needs --bot <bot>.\n\n" +
		"-o json prints the member as the platform sent it. -o yaml prints it as YAML, byte for byte as\n" +
		"the platform exports it (as tnctl council role export does); --version N then prints an older version."
	example := `  tnctl council role get triage --bot scout
  tnctl council role get captain --template -o yaml
  tnctl council role get bot/triage --bot scout`
	args := argsRange(1, 1, "council role get needs a member, for example: tnctl council role get triage --bot scout")
	versionUsage := "print an older version (with -o yaml)"
	if export {
		use = "export <member>"
		short = "Print one member as YAML, to edit and apply"
		long = "Print one member as YAML, byte for byte as the platform exports it. --version N prints an older\n" +
			"version.\n\n" +
			"<member> is a slug such as triage, or <scope>/<slug> (scope bot, teamspace or template), read\n" +
			"as in tnctl council role get."
		example = `  tnctl council role export triage --bot scout > triage.yaml
  tnctl council role export triage --version 2`
		args = argsRange(1, 1, "council role export needs a member, for example: tnctl council role export triage --bot scout")
		versionUsage = "print an older version"
	}
	c := &cobra.Command{
		Use:          use,
		Short:        short,
		Long:         long,
		Example:      example,
		Args:         args,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCouncilRoleShow(cmd, e, export, args[0], bot, template, version)
		},
	}
	c.Flags().StringVar(&bot, "bot", "", "the TeknoirBot whose member to read")
	c.Flags().BoolVar(&template, "template", false, "read the premade member")
	c.Flags().IntVar(&version, "version", 0, versionUsage)
	return c
}

// runCouncilRoleShow reads one member and prints it in the format the flags ask for.
func runCouncilRoleShow(cmd *cobra.Command, e *cliEnv, export bool, ref, bot string, template bool, version int) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if export {
		if format == "json" {
			return usageErr("council role export prints YAML; use council role get -o json")
		}
		format = "yaml"
	}
	scope, slug, ok := parseMemberRef(ref)
	if !ok {
		return usageErr("%q is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template", ref)
	}
	if template {
		if scope != "" && scope != "template" {
			return usageErr("--template and %q name different scopes", ref)
		}
		scope = "template"
	}
	if scope == "bot" && !cmd.Flags().Changed("bot") {
		return usageErr("a bot's own member needs --bot <bot>")
	}
	versionGiven := cmd.Flags().Changed("version")
	if versionGiven && version < 1 {
		return usageErr("--version must be 1 or more")
	}
	if versionGiven && format != "yaml" {
		return usageErr("--version needs -o yaml (only an export can show an older version)")
	}
	// Changed, not bot != "": an empty --bot "$BOT" is a mistake, never the teamspace.
	if cmd.Flags().Changed("bot") {
		if err := councilBot(bot); err != nil {
			return err
		}
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	switch {
	case bot != "":
		if format == "yaml" {
			b, err := fl.BotRoleExport(ctx, bot, slug, scope, version)
			if err != nil {
				return councilError(err)
			}
			_, err = out.Write(b)
			return err
		}
		d, raw, err := fl.BotRole(ctx, bot, slug, scope)
		if err != nil {
			return councilError(err)
		}
		return printCouncilRole(out, format, d, raw, -1, bot)
	case scope == "template":
		if format == "yaml" {
			b, err := fl.TeamspaceRoleExport(ctx, slug, "template", version)
			if err != nil {
				return councilError(err)
			}
			_, err = out.Write(b)
			return err
		}
		cat, raw, err := fl.CouncilCatalog(ctx)
		if err != nil {
			return councilError(err)
		}
		i := councilTemplateIndex(cat, slug)
		if i < 0 {
			return newErr(codeNotFound, "There is no premade member '%s'.", escapeLine(slug))
		}
		return printCouncilRole(out, format, &cat.Templates.Roles[i], raw, i, bot)
	case scope == "" || scope == "teamspace":
		if format == "yaml" {
			b, err := fl.TeamspaceRoleExport(ctx, slug, "", version)
			if err != nil {
				if scope != "" || !councilNotFound(err) {
					return councilError(err)
				}
				// The hub answers not_found both for a shared member that is not there and for a
				// version it does not have; the premade member is the second answer.
				b, secondErr := fl.TeamspaceRoleExport(ctx, slug, "template", version)
				if secondErr != nil {
					if !councilNotFound(secondErr) {
						return councilError(secondErr)
					}
					if versionGiven {
						return councilVersionMissing(err, secondErr)
					}
					return councilNoMember(slug)
				}
				_, err = out.Write(b)
				return err
			}
			_, err = out.Write(b)
			return err
		}
		d, raw, err := fl.TeamspaceRole(ctx, slug)
		if err != nil {
			if scope != "" || !councilNotFound(err) {
				return councilError(err)
			}
			cat, raw, err := fl.CouncilCatalog(ctx)
			if err != nil {
				return councilError(err)
			}
			i := councilTemplateIndex(cat, slug)
			if i < 0 {
				return councilNoMember(slug)
			}
			return printCouncilRole(out, format, &cat.Templates.Roles[i], raw, i, bot)
		}
		return printCouncilRole(out, format, d, raw, -1, bot)
	}
	return nil
}

// councilNoMember is the answer when neither the teamspace's members nor the premade ones hold slug.
func councilNoMember(slug string) error {
	return newErr(codeNotFound, "there is no shared or premade member %q in this teamspace; a bot's own member needs --bot <bot>", escapeLine(slug))
}

// councilVersionMissing is the answer when the teamspace's export and the premade one both say
// not_found with a version: the hub's own two messages say which is which.
func councilVersionMissing(first, second error) error {
	var lines []string
	for _, err := range []error{first, second} {
		if msg := hubText(councilMessage(err)); msg != "" {
			lines = append(lines, msg)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "the platform refused the request (not_found)")
	}
	return newErr(codeNotFound, "%s", strings.Join(lines, "\n"))
}

// councilTemplateIndex is the position of slug in the catalog's premade members, -1 when there is none.
func councilTemplateIndex(cat *api.Catalog, slug string) int {
	for i, d := range cat.Templates.Roles {
		if d.Slug == slug {
			return i
		}
	}
	return -1
}

// printCouncilRole prints a member as the table or as JSON. templateIndex is the entry's position in
// the catalog's premade members when the member came from there, -1 otherwise; bot is the --bot it
// was read for, "" without one.
func printCouncilRole(out io.Writer, format string, d *api.RoleDetail, raw json.RawMessage, templateIndex int, bot string) error {
	if format == "json" {
		var entry json.RawMessage
		if templateIndex >= 0 {
			var ans struct {
				Templates struct {
					Roles []json.RawMessage `json:"roles"`
				} `json:"templates"`
			}
			_ = json.Unmarshal(raw, &ans)
			entry = councilRawAt(ans.Templates.Roles, templateIndex)
		} else {
			var ans struct {
				Role json.RawMessage `json:"role"`
			}
			_ = json.Unmarshal(raw, &ans)
			entry = ans.Role
		}
		return writeStructured(out, "json", entry)
	}
	printCouncilRoleTable(out, d, bot)
	return nil
}

// printCouncilRoleTable prints a member as key: value lines. bot is the --bot it was read for: only a
// bot's computer can say which tools a member really gets.
func printCouncilRoleTable(out io.Writer, d *api.RoleDetail, bot string) {
	v := d.RoleView
	line := func(key, value string) { _, _ = fmt.Fprintf(out, "%-13s%s\n", key+":", value) }
	line("slug", escapeLine(v.Slug))
	line("name", hubText(v.Name))
	line("scope", escapeLine(v.Scope))
	line("part", hubText(v.Kind))
	line("model", fmt.Sprintf("%s (%s), effort %s", hubText(v.Model), hubText(v.ModelID), hubText(v.Effort)))
	line("tools", councilJoined(v.Groups))
	line("effective", councilEffective(v.EffectiveTools, bot))
	access := fmt.Sprintf("%s (ceiling %s)", hubText(v.Level), hubText(v.Ceiling))
	if bot == "" && v.Ceiling != "none" {
		// Without a bot the hub's level is the ceiling (teamspaceFacts): only a bot's access gives the
		// real one. A ceiling of none needs no bot to say so.
		access = fmt.Sprintf("at most %s (the bot's access decides)", hubText(v.Ceiling))
	}
	line("access", access)
	line("skills", councilJoined(v.Skills))
	line("version", fmt.Sprintf("%d", v.Version))
	status := councilStatus(v.Status)
	if reasons := councilReasons(v.Reasons); v.Status == "invalid" && reasons != "" {
		status += ": " + reasons
	}
	line("status", status)
	if notes := councilReasons(v.Notes); notes != "" {
		line("notes", notes)
	}
	description := hubText(v.Description)
	if description == "" {
		description = "-"
	}
	line("description", description)
	if !d.Detail {
		return
	}
	if d.UpdatedAt != nil {
		at := councilTime(*d.UpdatedAt)
		if d.UpdatedBy != nil {
			at += " by " + hubText(*d.UpdatedBy)
		}
		line("updated", at)
	}
	if d.ArchivedAt != nil {
		at := councilTime(*d.ArchivedAt)
		if d.ArchivedBy != nil {
			at += " by " + hubText(*d.ArchivedBy)
		}
		line("archived", at)
	}
	hidden := 0
	if d.HiddenCount != nil {
		hidden = *d.HiddenCount
	}
	if used := councilUsedBy(d.UsedBy, hidden); used != "" {
		line("used by", used)
	}
	if d.Instructions != nil && *d.Instructions != "" {
		line("instructions", "")
		for _, text := range strings.Split(strings.ReplaceAll(cleanBotText(*d.Instructions), "\r", ""), "\n") {
			if text == "" {
				_, _ = fmt.Fprintf(out, "\n")
				continue
			}
			_, _ = fmt.Fprintf(out, "  %s\n", text)
		}
	}
}

// councilJoined is a list of short hub values on one line.
func councilJoined(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	out := make([]string, 0, len(values))
	for _, s := range values {
		out = append(out, hubText(s))
	}
	return strings.Join(out, ", ")
}

// councilEffective is the tools the bot's computer reported, as one line. Without --bot no computer
// is asked, so the tools are not known; with one, the computer has not reported them yet.
func councilEffective(tools *[]string, bot string) string {
	if tools == nil {
		if bot == "" {
			return "not known without a bot"
		}
		return "not checked yet"
	}
	if len(*tools) == 0 {
		return "none"
	}
	return councilJoined(*tools)
}
