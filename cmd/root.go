package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"teknoir/cli/pkg/config"
)

var (
	cfgFile   string
	domain    string
	namespace string
	device    string
	debug     bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "tnctl",
	Short: "tnctl is a CLI for managing Teknoir cloud resources and devices",
	Long: `tnctl (Teknoir Control) provides a command-line interface to interact with
Teknoir's cloud platform and edge devices. It handles authentication,
device management, and namespace-scoped operations.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Set defaults
	defaultCfg := config.DefaultConfig()
	viper.SetDefault("domain", defaultCfg.Domain)

	// Persistent flags that are available to every subcommand
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.tnctl.yaml)")
	rootCmd.PersistentFlags().StringVarP(&domain, "domain", "d", "", "Teknoir domain (e.g., teknoir.cloud)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Target namespace")
	rootCmd.PersistentFlags().StringVar(&device, "device", "", "Target device ID")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "Enable debug output")

	// Bind flags to viper
	viper.BindPFlag("domain", rootCmd.PersistentFlags().Lookup("domain"))
	viper.BindPFlag("flag_namespace", rootCmd.PersistentFlags().Lookup("namespace"))
	viper.BindPFlag("flag_device", rootCmd.PersistentFlags().Lookup("device"))
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))

	// Bind environment variables
	viper.BindEnv("flag_namespace", "TNCTL_NAMESPACE")
	viper.BindEnv("flag_device", "TNCTL_DEVICE")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// Search config in home directory with name ".tnctl" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".tnctl")
	}

	viper.AutomaticEnv() // read in environment variables that match
	viper.SetEnvPrefix("TNCTL")

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		// Config file found and read successfully
	} else {
		// If no config file found, create a default one
		createDefaultConfig()
		// Try to read it back in so viper knows where to write to later
		_ = viper.ReadInConfig()
	}
}

func createDefaultConfig() {
	path, err := config.GetConfigPath()
	if err != nil {
		return
	}

	// Only create if it doesn't exist
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("Creating default configuration file at %s\n", path)

		// Create directory if it doesn't exist (though it's usually $HOME)
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			os.MkdirAll(dir, 0755)
		}

		if err := viper.SafeWriteConfigAs(path); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating default config: %v\n", err)
		}
	}
}
