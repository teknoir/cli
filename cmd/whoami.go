package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

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
			raw, bot, user, err := whoamiJSON(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			if bot != nil {
				printBotWhoami(out, bot)
				return nil
			}
			printUserWhoami(out, user)
			return nil
		},
	}
}

// whoamiJSON is what `tnctl whoami -o json` prints, with the typed value the table is made from:
// bot is set in bot mode, user otherwise.
func whoamiJSON(ctx context.Context) (raw json.RawMessage, bot *api.BotWhoami, user *whoamiUserJSON, err error) {
	if on, apiBase, ns, keyFile, err := botMode(); err != nil {
		return nil, nil, nil, err
	} else if on {
		if err := checkBotNamespace(ns); err != nil {
			return nil, nil, nil, err
		}
		who, raw, err := botFleet(apiBase, ns, keyFile).BotWhoami(ctx)
		if err != nil {
			return nil, nil, nil, classify(err)
		}
		return raw, who, nil, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, err
	}
	id, err := tnauth.StoredIdentity(cfg.Domain)
	if err != nil {
		return nil, nil, nil, classify(err)
	}
	u := &whoamiUserJSON{Mode: "user", Email: id.Email, Domain: id.Domain, Namespace: cfg.GetNamespace()}
	return marshalRaw(*u), nil, u, nil
}

// printBotWhoami prints who the bot is, for whom and what it may do.
func printBotWhoami(out io.Writer, who *api.BotWhoami) {
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
	_, _ = fmt.Fprintf(out, "bot %s/%s for %s, access %s, unit %s\n", who.Bot.Namespace, who.Bot.Name, creator, access, unit)
}

// printUserWhoami prints the account of the stored login and the teamspace tnctl targets.
func printUserWhoami(out io.Writer, who *whoamiUserJSON) {
	email := who.Email
	if email == "" {
		email = "an unknown user"
	}
	_, _ = fmt.Fprintf(out, "user %s on %s, teamspace %s\n", email, who.Domain, who.Namespace)
}
