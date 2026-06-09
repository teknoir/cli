package cmd

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"teknoir/cli/pkg/api"
	"teknoir/cli/pkg/config"
)

var (
	localPort  int
	targetAddr string
)

var portForwardCmd = &cobra.Command{
	Use:   "port-forward [device] [local-port]:[target-host]:[target-port]",
	Short: "Forward a local port to a device or a service reachable from the device",
	Long: `Forward a local port to a device. 
Example:
  tnctl port-forward my-device 8080:localhost:80
  tnctl port-forward my-device 8080:192.168.1.10:80
  tnctl port-forward my-device 31883:31883 (defaults to localhost:31883)
  tnctl port-forward --port 8080 --to localhost:80`,
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
		if len(args) > argIdx && !strings.Contains(args[argIdx], ":") {
			deviceName = args[argIdx]
			argIdx++
		} else if cmd.Flags().Changed("device") {
			deviceName = cfg.GetDevice()
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

		// Handle port mapping from arguments if provided
		if len(args) > argIdx {
			mapping := args[argIdx]
			parts := strings.Split(mapping, ":")
			switch len(parts) {
			case 2:
				// local:targetPort
				p, err := strconv.Atoi(parts[0])
				if err != nil {
					return fmt.Errorf("invalid local port: %s", parts[0])
				}
				localPort = p
				targetAddr = "127.0.0.1:" + parts[1]
			case 3:
				// local:targetHost:targetPort
				p, err := strconv.Atoi(parts[0])
				if err != nil {
					return fmt.Errorf("invalid local port: %s", parts[0])
				}
				localPort = p
				targetAddr = parts[1] + ":" + parts[2]
			default:
				return fmt.Errorf("invalid port mapping format: %s. Expected [local-port]:[target-port] or [local-port]:[target-host]:[target-port]", mapping)
			}
		}

		// Use defaults if not set by flags or args
		if localPort == 0 {
			// Random port between 8000 and 65000
			localPort = rand.IntN(65000-8000) + 8000
		}

		if targetAddr == "" {
			return fmt.Errorf("target address is required. Use --to <host>:<port> or provide a mapping argument")
		}

		// Fetch device details from Backstage API
		device, err := api.FetchDeviceDetails(cmd.Context(), domain, namespace, deviceName)
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
		tmpFile, err := os.CreateTemp("", "tnctl-portforward-*")
		if err != nil {
			return fmt.Errorf("failed to create temporary file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		if err := os.WriteFile(tmpFile.Name(), privateKey, 0600); err != nil {
			return fmt.Errorf("failed to write private key to temporary file: %w", err)
		}

		deadendHost := fmt.Sprintf("deadend-%s.%s", namespace, domain)
		remoteAccessPort := device.Spec.Subresources.Status.RemoteAccess.Port

		// Determine if it's a web service
		targetPort := 0
		if lastColon := strings.LastIndex(targetAddr, ":"); lastColon != -1 {
			targetPort, _ = strconv.Atoi(targetAddr[lastColon+1:])
		}

		isWeb := false
		protocol := "http"
		switch targetPort {
		case 80, 8080, 3000, 5000, 9000, 9090:
			isWeb = true
		case 443, 8443:
			isWeb = true
			protocol = "https"
		}

		fmt.Printf("\033[1;32m\n>>> Port forwarding active: 127.0.0.1:%d -> %s\033[0m\n", localPort, targetAddr)
		if isWeb {
			fmt.Printf("\033[1;36m>>> Web Service URL: %s://localhost:%d\033[0m\n", protocol, localPort)
		}
		fmt.Println("\033[1;33m>>> Press Ctrl+C to stop forwarding\033[0m")

		// Construct ProxyCommand as specified in the requirements
		proxyCommand := fmt.Sprintf("ssh -o ProxyCommand='ncat --ssl %s 2222' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i %s -N -W %%h:%%p teknoir@%s -p 2222",
			deadendHost, tmpFile.Name(), deadendHost)

		// Final SSH execution command using os/exec
		// ssh -o "ProxyCommand=${PROXY_CMD}" -o 'UserKnownHostsFile=/dev/null' -o 'StrictHostKeyChecking=no' -o 'ServerAliveInterval=60' -i ${RSA_KEY_FILE} ${USERNAME}@127.0.0.1 -p ${REMOTE_ACCESS_PORT} -L ${PORT}:${TO} -N
		sshExec := exec.Command("ssh",
			"-o", "ProxyCommand="+proxyCommand,
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "StrictHostKeyChecking=no",
			"-o", "ServerAliveInterval=60",
			"-i", tmpFile.Name(),
			"-p", remoteAccessPort,
			"-L", fmt.Sprintf("%d:%s", localPort, targetAddr),
			"-N",
			fmt.Sprintf("%s@127.0.0.1", username),
		)

		// Wire standard streams
		sshExec.Stdin = os.Stdin
		sshExec.Stdout = os.Stdout
		sshExec.Stderr = os.Stderr

		if config.Debug {
			fmt.Printf("DEBUG: Executing command: %s %v\n", sshExec.Path, sshExec.Args)
		}

		return sshExec.Run()
	},
}

func init() {
	rootCmd.AddCommand(portForwardCmd)
	portForwardCmd.Flags().IntVarP(&localPort, "port", "p", 0, "Local port to listen on")
	portForwardCmd.Flags().StringVarP(&targetAddr, "to", "t", "", "Target address on the device network (e.g. 127.0.0.1:80)")
}
