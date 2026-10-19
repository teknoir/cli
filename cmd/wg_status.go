package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/teknoir/wg-tunnel/pkg/api"
	"github.com/teknoir/wg-tunnel/pkg/wgclient"
)

type wgStatusOptions struct {
	domain     string
	namespace  string
	device     string
	hubURL     string
	turnServer string
	stunServer string
	p2p        bool
}

// wgStatusReport is what `tnctl wg status` found out.
type wgStatusReport struct {
	Device       string
	Hub          string // URL
	HubReachable bool
	// DeviceOnline: "yes", "no" or "unknown" (the hub did not get that far).
	DeviceOnline string
	Problem      string // the reason line when the session could not be created
	Path         string // api.Path*, empty when no session
	Candidates   int    // device candidates offered for the direct path
	RTT          time.Duration
	RTTErr       string
	Services     []api.ServiceSpec
	ServicesNote string // why Services is empty, or where it came from
}

// formatWGStatus renders the report as aligned "label  value" lines.
func formatWGStatus(r wgStatusReport) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	row := func(k, v string) { _, _ = fmt.Fprintf(w, "%s\t%s\n", k, v) }
	row("Device", r.Device)
	if r.HubReachable {
		row("Hub", "reachable ("+r.Hub+")")
	} else {
		row("Hub", "unreachable ("+r.Hub+")")
	}
	row("Device online", r.DeviceOnline)
	if r.Problem != "" {
		row("Problem", r.Problem)
	}
	if r.Path != "" {
		row("Path", pathLabel(r.Path))
		row("Candidates", fmt.Sprintf("%d", r.Candidates))
		switch {
		case r.RTTErr != "":
			row("RTT", "n/a ("+r.RTTErr+")")
		case r.RTT > 0:
			row("RTT", r.RTT.Round(100*time.Microsecond).String()+" (TCP connect to ssh)")
		}
	}
	if len(r.Services) == 0 {
		note := r.ServicesNote
		if note == "" {
			note = "none published"
		}
		row("Services", note)
	} else {
		for i, s := range r.Services {
			label := ""
			if i == 0 {
				label = "Services"
			}
			row(label, formatServiceLine(s))
		}
	}
	_ = w.Flush()
	return b.String()
}

func formatServiceLine(s api.ServiceSpec) string {
	parts := []string{s.ID}
	var attrs []string
	if s.Kind != "" {
		attrs = append(attrs, s.Kind)
	}
	if s.Protocol != "" {
		attrs = append(attrs, s.Protocol)
	}
	if s.Web {
		attrs = append(attrs, "web")
	}
	if len(attrs) > 0 {
		parts = append(parts, "("+strings.Join(attrs, ", ")+")")
	}
	if s.Description != "" {
		parts = append(parts, "- "+s.Description)
	}
	return strings.Join(parts, " ")
}

// statusDenial maps a hub denial to (device online, problem text).
func statusDenial(err error, o wgStatusOptions) (online, problem string) {
	var he *wgclient.Error
	if !errors.As(err, &he) {
		return "unknown", ""
	}
	po := wgProxyOptions{domain: o.domain, namespace: o.namespace, device: o.device}
	problem = describeWGError(err, &po)
	if he.Reason == api.ReasonDeviceOffline {
		return "no", problem
	}
	return "unknown", problem
}

// runWGStatus opens one short-lived ssh session and reports. lookup may be nil.
func runWGStatus(ctx context.Context, o wgStatusOptions, cred wgclient.Credential, hc *http.Client,
	lookup func(context.Context) ([]api.ServiceSpec, error), stdout, stderr io.Writer) int {
	if o.domain == "" {
		wgLine(stderr, "no domain: set --domain or log in first")
		return wgExitUsage
	}
	if err := validateWGTarget(o.namespace, o.device, 22); err != nil {
		wgLine(stderr, "%v", err)
		return wgExitUsage
	}
	if o.hubURL == "" {
		wgLine(stderr, "--wg-hub-url is required")
		return wgExitUsage
	}
	if err := validateHubURL(o.hubURL); err != nil {
		wgLine(stderr, "%v", err)
		return wgExitUsage
	}
	if err := validateHubHost(o.hubURL, o.domain); err != nil {
		wgLine(stderr, "%v", err)
		return wgExitUsage
	}

	rep := wgStatusReport{Device: o.namespace + "/" + o.device, Hub: o.hubURL, DeviceOnline: "unknown"}
	code := wgExitOK

	opts := wgclient.Options{
		HubURL: o.hubURL, Kind: api.KindClient, Credential: cred,
		ClientRequest: &api.ClientSessionRequest{
			Namespace: o.namespace, Device: o.device, Service: api.ServiceSSHID, Posture: wgPosture(),
		},
		TURNServerOverride: o.turnServer, HTTPClient: hc, P2P: o.p2p, P2PSTUN: o.stunServer,
	}
	if err := opts.Validate(); err != nil {
		wgLine(stderr, "%s", oneLine(err.Error()))
		return wgExitUsage
	}
	cctx, cancel := context.WithTimeout(ctx, wgCreateTimeout)
	sess, err := wgclient.Create(cctx, opts)
	cancel()
	if err != nil {
		var he *wgclient.Error
		rep.HubReachable = errors.As(err, &he) // an answer, even a denial, means the hub is up
		rep.DeviceOnline, rep.Problem = statusDenial(err, o)
		if rep.Problem == "" {
			rep.Problem = describeWGError(err, &wgProxyOptions{domain: o.domain, namespace: o.namespace, device: o.device})
		}
		code = wgExitFail
	} else {
		defer func() { _ = sess.Close() }()
		rep.HubReachable = true
		rep.DeviceOnline = "yes"
		if g := sess.Response().P2P; g != nil {
			rep.Candidates = len(g.DeviceCandidates)
		}
		if o.p2p {
			waitDirectPath(ctx, sess, wgPathWait)
		}
		rep.Path = sess.Path()
		dctx, dc := context.WithTimeout(ctx, wgDialTimeout)
		start := time.Now()
		conn, derr := sess.DialService(dctx, api.ServiceSSHID)
		dc()
		if derr != nil {
			rep.RTTErr = describeWGError(derr, &wgProxyOptions{domain: o.domain, namespace: o.namespace, device: o.device})
			code = wgExitFail
		} else {
			rep.RTT = time.Since(start)
			_ = conn.Close()
			rep.Path = sess.Path() // the dial may have used a path the probe did not see yet
		}
	}

	if lookup != nil {
		lctx, lc := context.WithTimeout(ctx, wgDialTimeout)
		svcs, lerr := lookup(lctx)
		lc()
		switch {
		case lerr != nil:
			rep.ServicesNote = "unknown (platform: " + oneLine(lerr.Error()) + ")"
		case len(svcs) == 0:
			rep.ServicesNote = "none published"
		default:
			rep.Services = svcs
		}
	} else {
		rep.ServicesNote = "unknown"
	}

	_, _ = io.WriteString(stdout, formatWGStatus(rep))
	return code
}
