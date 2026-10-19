package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/teknoir/cli/pkg/config"

	"github.com/spf13/cobra"
)

var (
	sshWireGuard  bool
	sshTURNServer string
	sshWGHubURL   string
	sshNoP2P      bool
	// sshShowPassword prints the device's (sudo) password in the banner.
	sshShowPassword bool
)

var sshCmd = &cobra.Command{
	Use:   "ssh [device-name]",
	Short: "SSH into a device through its tunnel",
	Long: `SSH into a device through its tunnel.

The tunnel is chosen automatically: the WireGuard tunnel when the device has
one, otherwise the legacy SSH tunnel (deprecated; it goes away once production
has moved to WireGuard).`,
	Example: "  tnctl ssh my-device",
	RunE: func(cmd *cobra.Command, args []string) error {
		fl, domain, namespace, err := defaultEnv.targetClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()

		deviceName, err := pickDevice(ctx, fl, namespace, presetDevice(args))
		if err != nil {
			return err
		}

		// What the platform says about the device: its tunnels. No secrets in this answer.
		device, err := fl.Device(ctx, deviceName)
		if err != nil {
			return classify(err)
		}

		kind, err := selectTunnel(device, deviceName, sshWireGuard, os.Stderr)
		if err != nil {
			return err
		}
		if kind != tunnelWireGuard && (sshNoP2P || sshTURNServer != "" || sshWGHubURL != "") {
			return fmt.Errorf("--turn-server, --wg-hub-url and --no-p2p only apply to a WireGuard tunnel")
		}

		// The device login comes from the platform's credentials route, which only developers who
		// may open a terminal can use. Both tunnels need it (the hub only carries the connection).
		login, err := loadLogin(ctx, fl, deviceName)
		if err != nil {
			return err
		}

		if kind == tunnelWireGuard {
			// Access is decided by the hub (see ssh_wg.go).
			// Errors from here on are runtime failures (a hub denial, ssh's exit
			// status), not usage mistakes: don't print the usage text.
			cmd.SilenceUsage = true
			return runSSHWireGuard(domain, namespace, deviceName, login)
		}

		username, password, privateKey, err := decodeDeviceLogin(login)
		if err != nil {
			return err
		}

		// Create secure temp file for private key
		tmpFile, err := os.CreateTemp("", "tnctl-ssh-*")
		if err != nil {
			return fmt.Errorf("failed to create temporary file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		if err := os.WriteFile(tmpFile.Name(), privateKey, 0600); err != nil {
			return fmt.Errorf("failed to write private key to temporary file: %w", err)
		}

		deadendHost := fmt.Sprintf("deadend-%s.%s", namespace, domain)
		remoteAccessPort := device.Tunnel.Legacy.Port

		// Construct ProxyCommand as specified in the requirements
		// Replicating the exact proxy chaining structure found in legacy ssh_device.sh
		proxyCommand := fmt.Sprintf("ssh -o ProxyCommand='ncat --ssl %s 2222' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i %s -N -W %%h:%%p teknoir@%s -p 2222",
			deadendHost, tmpFile.Name(), deadendHost)

		// Final SSH execution command using os/exec
		sshArgs := []string{"-p", remoteAccessPort, "-i", tmpFile.Name(), "-o", "ProxyCommand=" + proxyCommand}
		sshArgs = append(sshArgs, deviceHostKeyOpts(deviceName, namespace, domain)...)
		sshArgs = append(sshArgs, fmt.Sprintf("%s@127.0.0.1", username))
		sshExec := exec.Command("ssh", sshArgs...)

		// Wire standard streams for interactive terminal session
		sshExec.Stdin = os.Stdin
		sshExec.Stdout = os.Stdout
		sshExec.Stderr = os.Stderr

		if config.Debug {
			fmt.Printf("DEBUG: Executing command: %s %v\n", sshExec.Path, sshExec.Args)
		}

		printRemoteShellBanner(deviceName, password)

		return sshExec.Run()
	},
}

// printRemoteShellBanner prints the colorful message. The device password (the sudo password) is
// only printed with --show-password: it would otherwise end up in every scrollback and log.
func printRemoteShellBanner(deviceName, password string) {
	fmt.Printf("\033[1;32m\n>>> You are now using a remote shell on device: %s\033[0m\n", deviceName)
	switch {
	case password != "" && sshShowPassword:
		fmt.Printf("\033[1;36m>>> SSH Password: %s\033[0m\n\n", password)
	case password != "":
		fmt.Printf("\033[1;36m>>> The device password is not shown; tnctl ssh --show-password prints it\033[0m\n\n")
	default:
		fmt.Println()
	}
}

func init() {
	rootCmd.AddCommand(sshCmd)
	sshCmd.Flags().BoolVar(&sshWireGuard, "wireguard", false,
		"force the WireGuard tunnel; fail instead of falling back to the legacy SSH tunnel")
	_ = sshCmd.Flags().MarkHidden("wireguard")
	sshCmd.Flags().BoolVar(&sshShowPassword, "show-password", false,
		"print the device's SSH/sudo password in the banner (it is not shown by default)")
	sshCmd.Flags().BoolVar(&sshNoP2P, "no-p2p", false,
		"WireGuard: do not try a direct connection to the device, always go through the hub")
	sshCmd.Flags().StringVar(&sshTURNServer, "turn-server", "",
		"WireGuard: TURN server host:port overriding the hub's (env TNCTL_WG_TURN_SERVER)")
	sshCmd.Flags().StringVar(&sshWGHubURL, "wg-hub-url", "",
		"WireGuard: hub URL (default https://wg.<domain>, env TNCTL_WG_HUB)")
}
