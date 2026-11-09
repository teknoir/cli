package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
	tnauth "github.com/teknoir/cli/pkg/auth"
	"github.com/teknoir/cli/pkg/config"
)

// whoamiUserJSON is the -o json shape of `tnctl whoami` as a user.
type whoamiUserJSON struct {
	Mode      string `json:"mode"`
	Email     string `json:"email"`
	Domain    string `json:"domain"`
	Namespace string `json:"namespace"`
}

func newWhoamiCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "whoami [-o json]",
		Short:        "Show who this tnctl works as: a person, or the TeknoirBot it runs in",
		Long: `Show who this tnctl works as.

As a person it prints the account of the stored login and the teamspace it targets. Inside a
TeknoirBot's computer, where bot mode is on, it prints the bot, the person the bot works for and
what the bot may do: without a login and without reading the user's config.

-o json prints what the platform answered (bot mode) or {"mode":"user","email","domain","namespace"}.`,
		Example: `  tnctl whoami
  tnctl whoami -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			if on, apiBase, ns, keyFile, err := botMode(); err != nil {
				return err
			} else if on {
				if err := checkBotNamespace(ns); err != nil {
					return err
				}
				return runBotWhoami(cmd, format, botFleet(apiBase, ns, keyFile))
			}
			return runUserWhoami(cmd, format)
		},
	}
}

// runBotWhoami asks the bot API who this bot is.
func runBotWhoami(cmd *cobra.Command, format string, fl *api.Fleet) error {
	who, raw, err := fl.BotWhoami(cmd.Context())
	if err != nil {
		return classify(err)
	}
	out := cmd.OutOrStdout()
	if format != "table" {
		return writeStructured(out, format, raw)
	}
	unit := "—"
	if who.Bot.Unit != nil && *who.Bot.Unit != "" {
		unit = *who.Bot.Unit
		// The id is what robot add --unit takes; it is shown when the name is not the id.
		if who.Bot.UnitID != nil && *who.Bot.UnitID != "" && *who.Bot.UnitID != unit {
			unit += " (" + *who.Bot.UnitID + ")"
		}
	}
	access := who.Bot.Access
	if access == "" {
		access = api.BotAccessNone
	}
	creator := who.Creator.Email
	if creator == "" {
		creator = "an unknown person"
	}
	// The platform lowers a bot set to creator to read when its creator may only view.
	switch c := who.Bot.ConfiguredAccess; {
	case c == "" || c == access:
	case c == api.BotAccessCreator && access == api.BotAccessRead:
		access += fmt.Sprintf(" (set to %s, but %s may only view)", c, creator)
	default:
		access += fmt.Sprintf(" (set to %s)", c)
	}
	fmt.Fprintf(out, "bot %s/%s for %s, access %s, unit %s\n", who.Bot.Namespace, who.Bot.Name, creator, access, unit)
	return nil
}

// runUserWhoami prints the account of the stored login and the teamspace tnctl targets.
func runUserWhoami(cmd *cobra.Command, format string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	id, err := tnauth.StoredIdentity(cfg.Domain)
	if err != nil {
		return classify(err)
	}
	ns := cfg.GetNamespace()
	out := cmd.OutOrStdout()
	if format != "table" {
		return writeStructured(out, format, marshalRaw(whoamiUserJSON{
			Mode: "user", Email: id.Email, Domain: id.Domain, Namespace: ns,
		}))
	}
	who := id.Email
	if who == "" {
		who = "an unknown user"
	}
	fmt.Fprintf(out, "user %s on %s, teamspace %s\n", who, id.Domain, ns)
	return nil
}
