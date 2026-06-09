package cmd

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"teknoir/cli/pkg/config"
)

var (
	socksPort int
)

var socksProxyCmd = &cobra.Command{
	Use:   "socks-proxy [device] [port]",
	Short: "Establish a SOCKS5 proxy via a device",
	Long: `Establish a SOCKS5 proxy via a device to access the device's network.
Example:
  tnctl socks-proxy my-device 1080
  tnctl socks-proxy --port 1080`,
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

		var deviceName string
		argIdx := 0
		if len(args) > argIdx {
			// Check if it's a port number
			if _, err := strconv.Atoi(args[argIdx]); err != nil {
				// Not a port, assume it's a device name
				deviceName = args[argIdx]
				argIdx++
			}
		}

		if deviceName == "" && cmd.Flags().Changed("device") {
			deviceName = cfg.GetDevice()
		}

		if deviceName == "" {
			// Fetch and select device via fuzzy finder
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
			deviceName = devices[idx]
		}

		// Handle port from arguments if provided
		if len(args) > argIdx {
			p, err := strconv.Atoi(args[argIdx])
			if err != nil {
				return fmt.Errorf("invalid port: %s", args[argIdx])
			}
			socksPort = p
		}

		// Use defaults if not set by flags or args
		if socksPort == 0 {
			// Random port between 8000 and 65000
			socksPort = rand.IntN(65000-8000) + 8000
		}

		// Fetch device details from Backstage API
		device, err := fetchDeviceDetails(cmd.Context(), domain, namespace, deviceName)
		if err != nil {
			return err
		}

		if !device.Spec.Subresources.Status.RemoteAccess.Active {
			return fmt.Errorf("remote access is not enabled for device %s. Please enable it in the Teknoir Console", deviceName)
		}

		// Decode base64 username and private key
		usernameBytes, err := base64.StdEncoding.DecodeString(device.Spec.Settings.Username)
		if err != nil {
			return fmt.Errorf("failed to decode username: %w", err)
		}
		username := string(usernameBytes)

		privateKey, err := base64.StdEncoding.DecodeString(device.Spec.Settings.RSAPrivate)
		if err != nil {
			return fmt.Errorf("failed to decode RSA private key: %w", err)
		}

		// Create secure temp file for private key
		tmpFile, err := os.CreateTemp("", "tnctl-socks-*")
		if err != nil {
			return fmt.Errorf("failed to create temporary file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		if err := os.WriteFile(tmpFile.Name(), privateKey, 0600); err != nil {
			return fmt.Errorf("failed to write private key to temporary file: %w", err)
		}

		deadendHost := fmt.Sprintf("deadend-%s.%s", namespace, domain)
		remoteAccessPort := device.Spec.Subresources.Status.RemoteAccess.Port

		fmt.Printf("\033[1;32m\n>>> SOCKS5 proxy active: 127.0.0.1:%d\033[0m\n", socksPort)
		fmt.Printf("\033[1;36m>>> Configure your browser or application to use SOCKS5 proxy at 127.0.0.1:%d\033[0m\n", socksPort)

		fmt.Println("\n\033[1mExample Chrome usage:\033[0m")
		fmt.Printf("  macOS: /Applications/Google\\ Chrome.app/Contents/MacOS/Google\\ Chrome --proxy-server=\"socks5://127.0.0.1:%d\" --user-data-dir=$(mktemp -d)\n", socksPort)
		fmt.Printf("  Linux: google-chrome --proxy-server=\"socks5://127.0.0.1:%d\" --user-data-dir=$(mktemp -d)\n", socksPort)

		fmt.Println("\n\033[1;33m>>> Press Ctrl+C to stop the proxy\033[0m")

		// Construct ProxyCommand
		proxyCommand := fmt.Sprintf("ssh -o ProxyCommand='ncat --ssl %s 2222' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i %s -N -W %%h:%%p teknoir@%s -p 2222",
			deadendHost, tmpFile.Name(), deadendHost)

		// Final SSH execution command using os/exec
		sshExec := exec.Command("ssh",
			"-o", "ProxyCommand="+proxyCommand,
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "StrictHostKeyChecking=no",
			"-o", "ServerAliveInterval=60",
			"-i", tmpFile.Name(),
			"-p", remoteAccessPort,
			"-D", strconv.Itoa(socksPort),
			"-N",
			fmt.Sprintf("%s@127.0.0.1", username),
		)

		// Wire standard streams
		sshExec.Stdin = os.Stdin
		sshExec.Stdout = os.Stdout
		sshExec.Stderr = os.Stderr

		if viper.GetBool("debug") {
			fmt.Printf("DEBUG: Executing command: %s %v\n", sshExec.Path, sshExec.Args)
		}

		return sshExec.Run()
	},
}

func init() {
	rootCmd.AddCommand(socksProxyCmd)
	socksProxyCmd.Flags().IntVarP(&socksPort, "port", "p", 0, "Local port for SOCKS5 proxy")
}
