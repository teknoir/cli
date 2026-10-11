package cmd

import (
	"fmt"
	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
	"github.com/teknoir/cli/pkg/ui"
)

var deviceCmd = &cobra.Command{
	Use:     "device [device]",
	Aliases: []string{"dev"},
	Short:   "Print device status",
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

		var selected string
		if len(args) > 0 {
			selected = args[0]
		} else {
			// Fetch devices from Backstage
			devices, err := api.FetchDevices(cmd.Context(), domain, namespace)
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

			selected = devices[idx]
		}

		// Fetch and print device details
		deviceDetails, err := api.FetchDeviceDetails(cmd.Context(), domain, namespace, selected)
		if err != nil {
			return fmt.Errorf("failed to fetch device details: %w", err)
		}

		ui.PrintDeviceStatus(deviceDetails)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(deviceCmd)
}
