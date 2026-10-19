package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/config"
)

// newNsCmd is `tnctl ns`: it selects the active namespace (teamspace) from the ones the platform
// says the account can work with.
func newNsCmd(e *cliEnv) *cobra.Command {
	var list bool
	c := &cobra.Command{
		Use:   "ns [namespace]",
		Short: "Select active namespace",
		Long: `Select the active namespace (teamspace).

With a name, sets it. Without one, lists the teamspaces your account has access to and lets you
pick one. --list only prints them (name, your access, display name).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && !list {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				cfg.SetNamespace(args[0])
				return cfg.Save()
			}

			fl, _, err := e.domainFleet()
			if err != nil {
				return classify(err)
			}
			teamspaces, err := fl.Teamspaces(cmd.Context())
			if err != nil {
				return classify(err)
			}
			if len(teamspaces) == 0 {
				return fmt.Errorf("no namespaces found: your account has no access to a teamspace yet, ask a teamspace admin")
			}

			if list {
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tACCESS\tDISPLAY NAME")
				for _, t := range teamspaces {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Access, t.DisplayName)
				}
				return tw.Flush()
			}

			idx, err := fuzzyfinder.Find(teamspaces, func(i int) string {
				return teamspaces[i].Name
			})
			if err != nil {
				return err
			}

			selected := teamspaces[idx].Name
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.SetNamespace(selected)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Switched to namespace: %s\n", selected)
			return nil
		},
	}
	c.Flags().BoolVar(&list, "list", false, "only print the teamspaces you have access to")
	return c
}

func init() {
	rootCmd.AddCommand(newNsCmd(defaultEnv))
}
