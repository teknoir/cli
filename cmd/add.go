package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"teknoir/cli/pkg/config"
)

var addCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a resource",
}

var addDomainCmd = &cobra.Command{
	Use:   "domain [domain]",
	Short: "Add a new domain",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		newDomain := args[0]

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Auths == nil {
			cfg.Auths = make(map[string]config.AuthConfig)
		}
		// Initialize the new domain if it doesn't exist
		if _, exists := cfg.Auths[config.SanitizeDomain(newDomain)]; !exists {
			cfg.Auths[config.SanitizeDomain(newDomain)] = config.AuthConfig{}
		}

		cfg.UpdateDomain(newDomain)

		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Added and switched to domain: %s\n", newDomain)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.AddCommand(addDomainCmd)
}
