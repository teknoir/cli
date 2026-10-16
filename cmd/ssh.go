package cmd

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
)

var (
	sshWireGuard  bool
	sshTURNServer string
	sshWGHubURL   string
)

var sshCmd = &cobra.Command{
	Use:   "ssh [device-name]",
	Short: "SSH into a device",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !sshWireGuard && (sshTURNServer != "" || sshWGHubURL != "") {
			return fmt.Errorf("--turn-server and --wg-hub-url require --wireguard")
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}

		domain := cfg.Domain
		namespace := cfg.GetNamespace()

		if domain == "" || namespace == "" {
			return fmt.Errorf("domain and namespace are required. Please set them via flags or config")
		}

		var deviceName string
		if len(args) > 0 {
			deviceName = args[0]
		} else if device != "" {
			deviceName = device
		}

		if deviceName == "" {
			// Fetch and select device via fuzzy finder
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
			deviceName = devices[idx]
		}

		// Fetch device details from Backstage API
		device, err := api.FetchDeviceDetails(cmd.Context(), domain, namespace, deviceName)
		if err != nil {
			return err
		}

		if sshWireGuard {
			// The WireGuard path does not use the SSH tunnel's remote_access
			// switch; access is decided by the hub (see ssh_wg.go).
			// Errors from here on are runtime failures (a hub denial, ssh's exit
			// status), not usage mistakes: don't print the usage text.
			cmd.SilenceUsage = true
			return runSSHWireGuard(domain, namespace, deviceName, device)
		}

		if !device.Spec.Subresources.Status.RemoteAccess.Active {
			return fmt.Errorf("remote access is not enabled for device %s. Please enable it in the Teknoir Console", deviceName)
		}

		username, password, privateKey, err := decodeDeviceLogin(device)
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
		remoteAccessPort := device.Spec.Subresources.Status.RemoteAccess.Port

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

// decodeDeviceLogin decodes the base64 username, password and RSA private key
// from the device details. password is empty when the device has none.
func decodeDeviceLogin(device *api.BackstageDevice) (username, password string, privateKey []byte, err error) {
	usernameBytes, err := base64.StdEncoding.DecodeString(device.Spec.Settings.Username)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to decode username: %w", err)
	}
	username = string(usernameBytes)

	if device.Spec.Settings.Password != "" {
		passwordBytes, err := base64.StdEncoding.DecodeString(device.Spec.Settings.Password)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to decode password: %w", err)
		}
		password = string(passwordBytes)
	}

	privateKey, err = base64.StdEncoding.DecodeString(device.Spec.Settings.RSAPrivate)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to decode RSA private key: %w", err)
	}
	return username, password, privateKey, nil
}

// printRemoteShellBanner prints the colorful message and the device password.
func printRemoteShellBanner(deviceName, password string) {
	fmt.Printf("\033[1;32m\n>>> You are now using a remote shell on device: %s\033[0m\n", deviceName)
	if password != "" {
		fmt.Printf("\033[1;36m>>> SSH Password: %s\033[0m\n\n", password)
	} else {
		fmt.Println()
	}
}

func init() {
	rootCmd.AddCommand(sshCmd)
	sshCmd.Flags().BoolVar(&sshWireGuard, "wireguard", false,
		"connect through the WireGuard (ZTNA) tunnel instead of the SSH tunnel")
	sshCmd.Flags().StringVar(&sshTURNServer, "turn-server", "",
		"with --wireguard: TURN server host:port overriding the hub's (env TNCTL_WG_TURN_SERVER)")
	sshCmd.Flags().StringVar(&sshWGHubURL, "wg-hub-url", "",
		"with --wireguard: WireGuard hub URL (default https://wg.<domain>, env TNCTL_WG_HUB)")
}
