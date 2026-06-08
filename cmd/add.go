package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

		var cfg config.Config
		if err := viper.Unmarshal(&cfg); err != nil {
			return err
		}
		if cfg.Auths == nil {
			cfg.Auths = make(map[string]config.AuthConfig)
		}
		auth := cfg.Auths[config.SanitizeDomain(newDomain)]
		cfg.Auths[config.SanitizeDomain(newDomain)] = auth
		viper.Set("auths", cfg.Auths)
		viper.Set("domain", newDomain)

		if err := viper.WriteConfig(); err != nil {
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
