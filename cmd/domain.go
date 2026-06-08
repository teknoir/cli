package cmd

import (
	"fmt"
	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"slices"
	"teknoir/cli/pkg/config"
)

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Select active domain",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		if len(cfg.Auths) == 0 {
			return fmt.Errorf("no domains configured. Use 'tnctl add domain <domain>' to add one")
		}

		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range cfg.Auths {
				if !yield(config.DesanitizeDomain(k)) {
					return
				}
			}
		})

		idx, err := fuzzyfinder.Find(keys, func(i int) string {
			return keys[i]
		})
		if err != nil {
			return err
		}

		selected := keys[idx]
		cfg.UpdateDomain(selected)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Switched to domain: %s\n", selected)
		if ns := cfg.GetNamespace(); ns != "" {
			fmt.Printf("Active namespace: %s\n", ns)
		}
		if dev := cfg.GetDevice(); dev != "" {
			fmt.Printf("Active device: %s\n", dev)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(domainCmd)
}
