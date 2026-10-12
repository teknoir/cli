package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
	"github.com/teknoir/cli/pkg/ui"
)

var devicesCmd = &cobra.Command{
	Use:     "devices [metadata labels...]",
	Aliases: []string{"devs"},
	Short:   "Print a summary list of all devices in the current namespace",
	Long: `Print a summary device list status for all devices in the namespace selected by the current context.
You can optionally filter by metadata labels by passing key=value pairs.
Example:
  tnctl devices
  tnctl devices env=prod region=us-east`,
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

		// Parse metadata labels from args
		labels := make(map[string]string)
		for _, arg := range args {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) == 2 {
				labels[parts[0]] = parts[1]
			} else {
				return fmt.Errorf("invalid label format: %s. Expected key=value", arg)
			}
		}

		// Fetch devices with labels filter
		devices, err := api.FetchDeviceDetailsList(cmd.Context(), domain, namespace, labels)
		if err != nil {
			return fmt.Errorf("failed to fetch devices: %w", err)
		}

		if len(devices) == 0 {
			if len(labels) > 0 {
				return fmt.Errorf("no devices found in namespace %s with the specified labels", namespace)
			}
			return fmt.Errorf("no devices found in namespace %s", namespace)
		}

		ui.PrintDevicesSummary(devices)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(devicesCmd)
}
