package cmd

import (
	"fmt"
	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"teknoir/cli/pkg/config"
)

var deviceCmd = &cobra.Command{
	Use:     "device [device]",
	Aliases: []string{"dev"},
	Short:   "Select active device",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		domain := cfg.Domain
		namespace := cfg.GetNamespace()

		if domain == "" || namespace == "" {
			return fmt.Errorf("domain and namespace are required. Please set them via flags or config")
		}

		if len(args) > 0 {
			cfg.SetDevice(args[0])
			return cfg.Save()
		}

		// Fetch devices from Backstage
		devices, err := fetchDevices(cmd.Context(), domain, namespace)
		if err != nil {
			return err
		}

		if len(devices) == 0 {
			return fmt.Errorf("no devices found in namespace %s", namespace)
		}

		idx, err := fuzzyfinder.Find(devices, func(i int) string {
			return devices[i]
		})
		if err != nil {
			return err
		}

		selected := devices[idx]
		cfg.SetDevice(selected)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Switched to device: %s\n", selected)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(deviceCmd)
}
