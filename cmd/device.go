package cmd

import (
	"fmt"
	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var deviceCmd = &cobra.Command{
	Use:     "device [device]",
	Aliases: []string{"dev"},
	Short:   "Select active device",
	RunE: func(cmd *cobra.Command, args []string) error {
		domain := viper.GetString("domain")
		namespace := viper.GetString("namespace")

		if domain == "" || namespace == "" {
			return fmt.Errorf("domain and namespace are required. Please set them via flags or config")
		}

		if len(args) > 0 {
			viper.Set("device", args[0])
			return viper.WriteConfig()
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
		viper.Set("device", selected)
		if err := viper.WriteConfig(); err != nil {
			return err
		}
		fmt.Printf("Switched to device: %s\n", selected)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(deviceCmd)
}
