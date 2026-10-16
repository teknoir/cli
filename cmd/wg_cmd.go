package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
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

// catalogServices reads the device's published services from the Backstage
// catalog annotation ztna.teknoir.org/services.
func catalogServices(ctx context.Context, domain, ns, dev string) ([]wgapi.ServiceSpec, error) {
	d, err := api.FetchDeviceDetails(ctx, domain, ns, dev)
	if err != nil {
		return nil, err
	}
	return servicesFromAnnotations(d.Metadata.Annotations)
}

func servicesFromAnnotations(ann map[string]string) ([]wgapi.ServiceSpec, error) {
	raw := ann[wgapi.AnnotationServices]
	if raw == "" {
		return nil, nil
	}
	var out []wgapi.ServiceSpec
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("the services annotation of the device is not valid: %w", err)
	}
	return out, nil
}

func runPortForwardWireGuard(cmd *cobra.Command, domain, ns, dev, service string, local int) error {
	hub, turn := hubSettings(pfWGHubURL, pfTURNServer, domain)
	o := wgForwardOptions{
		domain: domain, namespace: ns, device: dev, service: service, localPort: local,
		hubURL: hub, turnServer: turn, p2p: !pfNoP2P,
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	web := func(c context.Context) bool {
		svcs, err := catalogServices(c, domain, ns, dev)
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
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		domain, ns := cfg.Domain, cfg.GetNamespace()
		if domain == "" || ns == "" {
			return fmt.Errorf("domain and namespace are required. Please set them via flags or config")
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
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		lookup := func(c context.Context) ([]wgapi.ServiceSpec, error) { return catalogServices(c, domain, ns, dev) }
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
