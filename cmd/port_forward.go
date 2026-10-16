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
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
)

var (
	localPort  int
	targetAddr string

	pfWireGuard  bool
	pfNoP2P      bool
	pfTURNServer string
	pfWGHubURL   string
)

var portForwardCmd = &cobra.Command{
	Use:   "port-forward [device] [local-port]:[target-host]:[target-port]",
	Short: "Forward a local port to a device or a service reachable from the device",
	Long: `Forward a local port to a device. 
Example:
  tnctl port-forward my-device 8080:localhost:80
  tnctl port-forward my-device 8080:192.168.1.10:80
  tnctl port-forward my-device 31883:31883 (defaults to localhost:31883)
  tnctl port-forward --port 8080 --to localhost:80

Through the WireGuard (ZTNA) tunnel the target is a service ID that the device
publishes, not host:port. A direct path to the device is tried first and the
hub relays when that fails:
  tnctl port-forward --wireguard my-device 8080:grafana
  tnctl port-forward --wireguard my-device grafana   (random local port)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !pfWireGuard && (pfNoP2P || pfTURNServer != "" || pfWGHubURL != "") {
			return fmt.Errorf("--no-p2p, --turn-server and --wg-hub-url require --wireguard")
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
		argIdx := 0
		if pfWireGuard {
			// [device] [local:]service-id. One bare word is the device, unless
			// --device names it, in which case the word is the service.
			if len(args) > 2 {
				return fmt.Errorf("too many arguments: expected [device] [local-port:]service-id")
			}
			if len(args) == 2 || (len(args) == 1 && !strings.Contains(args[0], ":") && device == "") {
				deviceName = args[0]
				argIdx = 1
			} else if device != "" {
				deviceName = device
			}
		} else if len(args) > argIdx && !strings.Contains(args[argIdx], ":") {
			deviceName = args[argIdx]
			argIdx++
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

		if pfWireGuard {
			if len(args) <= argIdx {
				return fmt.Errorf("a service ID is required: tnctl port-forward --wireguard %s [local-port:]service-id (see 'tnctl wg status %s')", deviceName, deviceName)
			}
			local, service, err := parseWGForwardArg(args[argIdx])
			if err != nil {
				return err
			}
			if localPort != 0 {
				if local != 0 && local != localPort {
					return fmt.Errorf("--port %d conflicts with the local port %d in the argument", localPort, local)
				}
				local = localPort
			}
			cmd.SilenceUsage = true
			return runPortForwardWireGuard(cmd, domain, namespace, deviceName, service, local)
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
		sshArgs := []string{
			"-o", "ProxyCommand=" + proxyCommand,
			"-o", "ServerAliveInterval=60",
			"-i", tmpFile.Name(),
			"-p", remoteAccessPort,
			"-L", fmt.Sprintf("%d:%s", localPort, targetAddr),
			"-N",
		}
		sshArgs = append(sshArgs, deviceHostKeyOpts(deviceName, namespace, domain)...)
		sshArgs = append(sshArgs, fmt.Sprintf("%s@127.0.0.1", username))
		sshExec := exec.Command("ssh", sshArgs...)

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
	portForwardCmd.Flags().BoolVar(&pfWireGuard, "wireguard", false,
		"forward a device service through the WireGuard (ZTNA) tunnel; the target is a service ID")
	portForwardCmd.Flags().BoolVar(&pfNoP2P, "no-p2p", false,
		"with --wireguard: do not try a direct connection to the device, always go through the hub")
	portForwardCmd.Flags().StringVar(&pfTURNServer, "turn-server", "",
		"with --wireguard: TURN server host:port overriding the hub's (env TNCTL_WG_TURN_SERVER)")
	portForwardCmd.Flags().StringVar(&pfWGHubURL, "wg-hub-url", "",
		"with --wireguard: WireGuard hub URL (default https://wg.<domain>, env TNCTL_WG_HUB)")
	portForwardCmd.Flags().StringVarP(&targetAddr, "to", "t", "", "Target address on the device network (e.g. 127.0.0.1:80)")
}
