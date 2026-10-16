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
	wgapi "github.com/teknoir/wg-tunnel/pkg/api"
)

var (
	localPort  int
	targetAddr string

	pfWireGuard  bool // hidden: force WireGuard, no legacy fallback
	pfNoP2P      bool
	pfTURNServer string
	pfWGHubURL   string
)

var portForwardCmd = &cobra.Command{
	Use:   "port-forward [device] [[local-port:]service-id]",
	Short: "Forward a local port to a service on a device",
	Long: `Forward a local port to a service on a device, through the device's tunnel.

Forward one of the device's services; the target is a service ID, not host:port:
  tnctl port-forward my-device grafana          (random local port)
  tnctl port-forward my-device 8080:grafana     (local port 8080)

List the device's services (exit 0):
  tnctl port-forward my-device

The tunnel is chosen automatically. Over the legacy SSH tunnel (deprecated; it
goes away once production has moved to WireGuard) the target is host:port
instead, and --to is available:
  tnctl port-forward my-device 8080:localhost:80
  tnctl port-forward my-device 31883:31883 (defaults to localhost:31883)
  tnctl port-forward my-device --port 8080 --to localhost:80`,
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

		deviceName, mapping, err := parsePortForwardArgs(args, device)
		if err != nil {
			return err
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

		kind, err := selectTunnel(device, deviceName, pfWireGuard, os.Stderr)
		if err != nil {
			return err
		}

		if kind == tunnelWireGuard {
			if targetAddr != "" {
				return errToOverWireGuard(deviceName)
			}
			if mapping == "" {
				svcs, err := servicesFromAnnotations(device.Metadata.Annotations)
				if err != nil {
					return err
				}
				fmt.Print(formatServiceList(deviceName, svcs))
				return nil
			}
			local, service, err := parseWGForwardArg(mapping)
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

		if pfNoP2P || pfTURNServer != "" || pfWGHubURL != "" {
			return fmt.Errorf("--no-p2p, --turn-server and --wg-hub-url only apply to a WireGuard tunnel")
		}

		// Handle port mapping from arguments if provided
		if mapping != "" {
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

// parsePortForwardArgs splits `[device] [[local:]target]`. One bare word (no
// ':') is the device, unless --device already names it, in which case the word
// is the target. A word with ':' is always the target. mapping is "" when no
// target was given.
func parsePortForwardArgs(args []string, deviceFlag string) (deviceName, mapping string, err error) {
	switch len(args) {
	case 0:
		return deviceFlag, "", nil
	case 1:
		if strings.Contains(args[0], ":") || deviceFlag != "" {
			return deviceFlag, args[0], nil
		}
		return args[0], "", nil
	case 2:
		return args[0], args[1], nil
	}
	return "", "", fmt.Errorf("too many arguments: expected [device] [[local-port:]service-id]")
}

// errToOverWireGuard is the refusal for --to on a WireGuard device.
func errToOverWireGuard(deviceName string) error {
	return fmt.Errorf("--to is not available over WireGuard; forward one of the device's services: tnctl port-forward %s <service-id> (list them with tnctl port-forward %s)", deviceName, deviceName)
}

// formatServiceList renders the services a device publishes, one per line.
func formatServiceList(deviceName string, svcs []wgapi.ServiceSpec) string {
	var b strings.Builder
	if len(svcs) == 0 {
		fmt.Fprintf(&b, "%s publishes no services.\n", deviceName)
		return b.String()
	}
	fmt.Fprintf(&b, "Services on %s (forward with: tnctl port-forward %s [local-port:]<service-id>):\n", deviceName, deviceName)
	for _, s := range svcs {
		fmt.Fprintf(&b, "  %s\n", formatServiceLine(s))
	}
	return b.String()
}

func init() {
	rootCmd.AddCommand(portForwardCmd)
	portForwardCmd.Flags().IntVarP(&localPort, "port", "p", 0, "Local port to listen on")
	portForwardCmd.Flags().BoolVar(&pfWireGuard, "wireguard", false,
		"force the WireGuard tunnel; fail instead of falling back to the legacy SSH tunnel")
	_ = portForwardCmd.Flags().MarkHidden("wireguard")
	portForwardCmd.Flags().BoolVar(&pfNoP2P, "no-p2p", false,
		"WireGuard: do not try a direct connection to the device, always go through the hub")
	portForwardCmd.Flags().StringVar(&pfTURNServer, "turn-server", "",
		"WireGuard: TURN server host:port overriding the hub's (env TNCTL_WG_TURN_SERVER)")
	portForwardCmd.Flags().StringVar(&pfWGHubURL, "wg-hub-url", "",
		"WireGuard: hub URL (default https://wg.<domain>, env TNCTL_WG_HUB)")
	portForwardCmd.Flags().StringVarP(&targetAddr, "to", "t", "", "Legacy SSH tunnel only: target address on the device network (e.g. 127.0.0.1:80)")
}
