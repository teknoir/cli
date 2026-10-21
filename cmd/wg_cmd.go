package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	wgapi "github.com/teknoir/wg-tunnel/pkg/api"
)

var (
	wgStatusNoP2P      bool
	wgStatusTURNServer string
	wgStatusHubURL     string
)

// hubSettings resolves the hub URL and TURN override from flags, environment
// and the logged-in domain.
func hubSettings(flagHub, flagTURN, domain string) (hub, turn string) {
	hub = resolveWGOption(flagHub, envWGHub, "")
	if hub == "" && domain != "" {
		hub = defaultHubURL(domain)
	}
	return hub, resolveWGOption(flagTURN, envWGTurnServer, "")
}

// fleetServices reads the device's published services (annotation ztna.teknoir.org/services) from
// the fleet API.
func fleetServices(ctx context.Context, fl *api.Fleet, dev string) ([]wgapi.ServiceSpec, error) {
	d, err := fl.Device(ctx, dev)
	if err != nil {
		return nil, classify(err)
	}
	return deviceServices(d)
}

// deviceServices are the services a device publishes. The platform parses the annotation; it
// flags one it cannot read.
func deviceServices(d *api.FleetDevice) ([]wgapi.ServiceSpec, error) {
	if d.ServicesInvalid {
		return nil, fmt.Errorf("the services annotation of the device is not valid")
	}
	return d.Services, nil
}

func runPortForwardWireGuard(cmd *cobra.Command, fl *api.Fleet, domain, ns, dev, service string, local int) error {
	hub, turn := hubSettings(pfWGHubURL, pfTURNServer, domain)
	o := wgForwardOptions{
		domain: domain, namespace: ns, device: dev, service: service, localPort: local,
		hubURL: hub, turnServer: turn, p2p: !pfNoP2P,
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), wgSignals()...)
	defer stop()
	web := func(c context.Context) bool {
		svcs, err := fleetServices(c, fl, dev)
		if err != nil {
			return false
		}
		for _, s := range svcs {
			if s.ID == service {
				return s.Web
			}
		}
		return false
	}
	if code := runWGPortForward(ctx, o, newWGCredential(domain), nil, web, os.Stdout, os.Stderr); code != wgExitOK {
		return &exitError{code: code}
	}
	return nil
}

var wgCmd = &cobra.Command{
	Use:   "wg",
	Short: "Inspect the WireGuard tunnel to devices",
}

var wgStatusCmd = &cobra.Command{
	Use:           "status [device]",
	Short:         "Show whether and how a device can be reached through WireGuard",
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		fl, domain, ns, err := defaultEnv.targetClient()
		if err != nil {
			return err
		}
		dev := device
		if len(args) > 0 {
			dev = args[0]
		}
		if dev == "" {
			return fmt.Errorf("a device is required: tnctl wg status <device>")
		}
		hub, turn := hubSettings(wgStatusHubURL, wgStatusTURNServer, domain)
		o := wgStatusOptions{domain: domain, namespace: ns, device: dev, hubURL: hub, turnServer: turn, p2p: !wgStatusNoP2P}
		ctx, stop := signal.NotifyContext(cmd.Context(), wgSignals()...)
		defer stop()
		lookup := func(c context.Context) ([]wgapi.ServiceSpec, error) { return fleetServices(c, fl, dev) }
		if code := runWGStatus(ctx, o, newWGCredential(domain), nil, lookup, os.Stdout, os.Stderr); code != wgExitOK {
			return &exitError{code: code}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(wgCmd)
	wgCmd.AddCommand(wgStatusCmd)
	f := wgStatusCmd.Flags()
	f.BoolVar(&wgStatusNoP2P, "no-p2p", false, "skip the direct-path attempt")
	f.StringVar(&wgStatusTURNServer, "turn-server", "", "TURN server host:port overriding the hub's (env TNCTL_WG_TURN_SERVER)")
	f.StringVar(&wgStatusHubURL, "wg-hub-url", "", "WireGuard hub URL (default https://wg.<domain>, env TNCTL_WG_HUB)")
}

// wgSignals are the signals that end a WireGuard command cleanly (closing its hub session): SIGINT,
// SIGTERM, and SIGHUP (the terminal closed) unless it is ignored, so that 'nohup tnctl port-forward'
// keeps running.
func wgSignals() []os.Signal {
	sigs := []os.Signal{syscall.SIGTERM, syscall.SIGINT}
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return sigs
}
