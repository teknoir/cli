# Skill: Adding a new CLI Command

This skill provides instructions for adding new subcommands to the `tnctl` CLI.

## Instructions

1. **File Location**: All commands must be placed in the `cmd/` directory.
2. **Naming**: Use descriptive names for the command files (e.g., `cmd/login.go`, `cmd/devices_list.go`).
3. **Cobra Template**: Use the standard Cobra command template:
   ```go
   var myCmd = &cobra.Command{
       Use:   "command-name",
       Short: "Brief description",
       Long:  `Detailed description of what the command does.`,
       Run: func(cmd *cobra.Command, args []string) {
           // Logic goes here
       },
   }

   func init() {
       rootCmd.AddCommand(myCmd)
       // Add local flags here if needed
   }
   ```
4. **Global Flags**: Access global flags via Viper or by reading them from the `rootCmd`.
5. **Validation**: Ensure all commands validate their inputs and provide helpful error messages.
6. **Documentation**: Add a `Long` description to every command for the auto-generated help.
