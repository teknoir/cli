package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/ui"
)

// newDevicesCmd is `tnctl devices`.
func newDevicesCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:     "devices [metadata labels...]",
		Aliases: []string{"devs"},
		Short:   "Print a summary list of all devices in the current namespace",
		Long: `Print a summary device list status for all devices in the namespace selected by the current context.
You can optionally filter by metadata labels by passing key=value pairs.
Example:
  tnctl devices
  tnctl devices env=prod region=us-east`,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			fl, _, namespace, err := e.targetClient()
			if err != nil {
				return err
			}

			// The platform lists the devices the account may see and applies the label filter
			devices, err := fl.Devices(cmd.Context(), labels)
			if err != nil {
				return fmt.Errorf("failed to fetch devices: %w", classify(err))
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
}

func init() {
	rootCmd.AddCommand(newDevicesCmd(defaultEnv))
}
