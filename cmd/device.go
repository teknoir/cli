package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/ui"
)

// newDeviceCmd is `tnctl device`.
func newDeviceCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:     "device [device]",
		Aliases: []string{"dev"},
		Short:   "Print device status",
		RunE: func(cmd *cobra.Command, args []string) error {
			fl, _, namespace, err := e.targetClient()
			if err != nil {
				return err
			}

			var preset string
			if len(args) > 0 {
				preset = args[0]
			}
			selected, err := pickDevice(cmd.Context(), fl, namespace, preset)
			if err != nil {
				return err
			}

			deviceDetails, err := fl.Device(cmd.Context(), selected)
			if err != nil {
				return fmt.Errorf("failed to fetch device details: %w", classify(err))
			}

			ui.PrintDeviceStatus(deviceDetails)
			return nil
		},
	}
}

func init() {
	rootCmd.AddCommand(newDeviceCmd(defaultEnv))
}
