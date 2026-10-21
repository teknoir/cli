package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/auth"
	"github.com/teknoir/cli/pkg/config"
	"github.com/teknoir/wg-tunnel/pkg/api"
	"github.com/teknoir/wg-tunnel/pkg/knownhosts"
	"github.com/teknoir/wg-tunnel/pkg/wgclient"
)

// Version is the tnctl version, set from main (goreleaser ldflags).
var Version = "dev"

const (
	wgExitOK    = 0
	wgExitFail  = 1
	wgExitUsage = 2

	wgCreateTimeout = 40 * time.Second
	wgDialTimeout   = 15 * time.Second
	wgDrainGrace    = 5 * time.Second
)

// exitError makes Execute exit with a code without cobra printing anything.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

type wgProxyOptions struct {
	domain         string
	namespace      string
	device         string
	port           int
	knownHostsFile string
	turnServer     string
	hubURL         string
	direct         bool
	p2p            bool   // ask for a direct (plane B) path; the ssh stream uses service "ssh"
	stunServer     string // STUN host:port overriding the hub's
}

var wgProxyOpts wgProxyOptions

// wgProxyCmd is the ssh ProxyCommand behind `tnctl ssh` over WireGuard. It is not
// meant to be run by hand: stdin/stdout carry the raw SSH byte stream.
var wgProxyCmd = &cobra.Command{
	Use:           "wg-proxy",
	Short:         "SSH ProxyCommand over the WireGuard tunnel (used by tnctl ssh and the Ansible inventory)",
	Hidden:        false,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		o := wgProxyOpts
		// --domain is the root's persistent flag; Load resolves flag, config, default.
		cfg, lerr := config.Load()
		if lerr != nil {
			wgLine(os.Stderr, "cannot read the tnctl configuration: %s", oneLine(lerr.Error()))
			return &exitError{code: wgExitUsage}
		}
		o.domain = cfg.Domain
		if o.hubURL == "" {
			o.hubURL = resolveWGOption("", envWGHub, "")
		}
		if o.turnServer == "" {
			o.turnServer = resolveWGOption("", envWGTurnServer, "")
		}
		if o.hubURL == "" && o.domain != "" {
			o.hubURL = defaultHubURL(o.domain)
		}

		// ssh ends its ProxyCommand with SIGHUP when the connection closes. Caught, it ends the
		// relay and the deferred Close tells the hub; uncaught, the process dies first and the
		// hub keeps the session until it expires, so a few quick ssh/exec calls in a row hit the
		// per-device session limit. A write to stdout after ssh has gone must not kill it
		// either: with SIGPIPE ignored the write fails and the relay ends normally. (A hangup
		// before the tunnel's handshake still leaves the session to expire: the hub cannot be
		// told without the tunnel.)
		signal.Ignore(syscall.SIGPIPE)
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer stop()

		// The token is read from ~/.tnctl.yaml (and refreshed) on every call,
		// never from argv or the environment. Session creation may use the
		// cached token; every renewal must present a newly issued one (the hub
		// refuses a token it has already accepted), so renewals force a refresh.
		cred := newWGCredential(o.domain)
		code := runWGProxy(ctx, o, cred, nil, os.Stdin, os.Stdout, os.Stderr)
		if code != wgExitOK {
			return &exitError{code: code}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(wgProxyCmd)
	// Local flags: they shadow the root's persistent --namespace/--device on
	// purpose, so nothing is taken from the config or environment by accident.
	f := wgProxyCmd.Flags()
	f.StringVar(&wgProxyOpts.namespace, "namespace", "", "teamspace (namespace) of the device")
	f.StringVar(&wgProxyOpts.device, "device", "", "device name")
	f.IntVar(&wgProxyOpts.port, "port", 22, "device TCP port")
	f.StringVar(&wgProxyOpts.knownHostsFile, "known-hosts-file", "", "file to write the device SSH host keys to")
	f.StringVar(&wgProxyOpts.turnServer, "turn-server", "", "TURN server host:port overriding the hub's (env TNCTL_WG_TURN_SERVER)")
	f.StringVar(&wgProxyOpts.hubURL, "hub-url", "", "WireGuard hub URL (default https://wg.<domain>, env TNCTL_WG_HUB)")
	f.BoolVar(&wgProxyOpts.p2p, "p2p", false, "try a direct WireGuard path to the device (falls back to the hub)")
	f.StringVar(&wgProxyOpts.stunServer, "stun-server", "", "STUN server host:port overriding the hub's")
	f.BoolVar(&wgProxyOpts.direct, "direct", false, "debug: send WireGuard straight to the hub over UDP (in-cluster only)")
}

func (o *wgProxyOptions) validate() error {
	if o.domain == "" {
		return errors.New("no domain: set --domain or log in first")
	}
	if err := validateWGTarget(o.namespace, o.device, o.port); err != nil {
		return err
	}
	if o.knownHostsFile == "" {
		return errors.New("--known-hosts-file is required")
	}
	if o.hubURL == "" {
		return errors.New("--hub-url is required")
	}
	if err := validateHubURL(o.hubURL); err != nil {
		return err
	}
	if err := validateHubHost(o.hubURL, o.domain); err != nil {
		return err
	}
	if o.direct && o.p2p {
		return errors.New("--direct and --p2p are mutually exclusive")
	}
	if o.direct && o.turnServer != "" {
		return errors.New("--direct and --turn-server are mutually exclusive")
	}
	return nil
}

// wgLine prints the one stderr line every failure ends in.
func wgLine(stderr io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(stderr, "tnctl: wireguard: "+format+"\n", a...)
}

// wgHint is the human explanation for a hub denial reason.
func wgHint(reason, namespace, device string) string {
	switch reason {
	case api.ReasonUnauthenticated:
		return "your tnctl login was not accepted; run 'tnctl login' and try again"
	case api.ReasonForbidden:
		return fmt.Sprintf("you need edit/admin access to teamspace %s", namespace)
	case api.ReasonDeviceRevoked:
		return "device is flagged compromised/health-check-failed in Teknoir"
	case api.ReasonDeviceOffline:
		return "the device agent is not connected"
	case api.ReasonDeviceDisabled:
		return fmt.Sprintf("WireGuard is not enabled for device %s; enable it in the Teknoir Console", device)
	case api.ReasonDeviceUnknown:
		return fmt.Sprintf("device %s is not known to the WireGuard gateway", device)
	case api.ReasonNotEnrolled:
		return "the device agent has not enrolled with the hub yet"
	case api.ReasonReenroll, api.ReasonEnrollClosed, api.ReasonEnrollConflict:
		return "the device agent identity needs attention; ask a teamspace admin to reset its enrollment"
	case api.ReasonPortNotAllowed:
		return "this port is not allowed for the device"
	case api.ReasonPendingApproval:
		return "the device identity is waiting for approval; ask a teamspace admin to approve its fingerprint"
	case api.ReasonAttestRequired:
		return "the device agent could not prove its identity (host-key attestation); ask a teamspace admin"
	case api.ReasonServiceUnknown:
		return "the device has no such service; list them with 'tnctl wg status <device>'"
	case api.ReasonServiceDenied:
		return "the device refused the service target; ask a teamspace admin to check the service definition"
	case api.ReasonP2PUnavailable:
		return "the device cannot take a direct connection; retry with --no-p2p to go through the hub"
	case api.ReasonPosture:
		return "this tnctl version is not accepted by the hub; update tnctl"
	case api.ReasonSessionLimit:
		return "too many open WireGuard sessions; close one and retry"
	case api.ReasonSessionMaxAge:
		return "the session reached its maximum lifetime; reconnect"
	case api.ReasonSessionUnknown, api.ReasonReplay:
		return "the session is no longer valid; reconnect"
	case api.ReasonRateLimited:
		return "too many requests; wait a moment and retry"
	case api.ReasonBadRequest:
		return "the hub rejected the request; update tnctl"
	case api.ReasonInternal:
		return "the hub failed; retry shortly"
	}
	return "access denied"
}

// describeWGError turns an error from the session machinery into the text
// after "tnctl: wireguard: ".
func describeWGError(err error, o *wgProxyOptions) string {
	var he *wgclient.Error
	switch {
	case errors.Is(err, auth.ErrSessionExpired):
		return auth.SessionExpiredMessage
	case errors.As(err, &he):
		reason := he.Reason
		if reason == "" {
			reason = "unknown"
		}
		msg := wgHint(he.Reason, o.namespace, o.device)
		if he.Message != "" && !strings.EqualFold(he.Message, msg) {
			msg += " (hub: " + he.Message + ")"
		}
		return reason + ": " + msg
	case errors.Is(err, auth.ErrNotLoggedIn):
		return fmt.Sprintf("not logged in to %s; run 'tnctl login'", o.domain)
	case errors.Is(err, wgclient.ErrHandshakeTimeout):
		return "handshake with the hub timed out; is the TURN relay (UDP) reachable from this network?"
	case errors.Is(err, wgclient.ErrExpired):
		return "the WireGuard session expired: the hub could not be reached to renew it"
	}
	return oneLine(err.Error())
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// runWGProxy creates the session, records the device host keys, then relays
// stdin/stdout through the tunnel. It returns the process exit code. hc may be
// nil (default client).
func runWGProxy(ctx context.Context, o wgProxyOptions, cred wgclient.Credential, hc *http.Client, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := o.validate(); err != nil {
		wgLine(stderr, "%v", err)
		return wgExitUsage
	}

	req := &api.ClientSessionRequest{Namespace: o.namespace, Device: o.device, Port: o.port, Posture: wgPosture()}
	if o.p2p {
		// Named services are what plane B grants are made for (phase 2). The
		// raw-port request stays for the plain hub path, which older hubs know.
		req.Service, req.Port = api.ServiceSSHID, 0
	}
	opts := wgclient.Options{
		HubURL:             o.hubURL,
		Kind:               api.KindClient,
		Credential:         cred,
		ClientRequest:      req,
		Direct:             o.direct,
		TURNServerOverride: o.turnServer,
		HTTPClient:         hc,
		P2P:                o.p2p,
		P2PSTUN:            o.stunServer,
	}
	if err := opts.Validate(); err != nil {
		wgLine(stderr, "%s", oneLine(err.Error()))
		return wgExitUsage
	}

	cctx, cancel := context.WithTimeout(ctx, wgCreateTimeout)
	sess, err := wgclient.Create(cctx, opts)
	cancel()
	if err != nil {
		wgLine(stderr, "%s", describeWGError(err, &o))
		return wgExitFail
	}
	defer func() { _ = sess.Close() }()

	// Host keys first: ssh must be able to verify the device before any byte flows.
	resp := sess.Response()
	if len(resp.SSHHostKeys) == 0 {
		wgLine(stderr, "the hub returned no SSH host keys for device %s; refusing to connect without host key verification", o.device)
		return wgExitFail
	}
	if err := knownhosts.Write(o.knownHostsFile, api.KnownHostsAlias(o.namespace, o.device), resp.SSHHostKeys); err != nil {
		wgLine(stderr, "cannot record the device SSH host keys: %s", oneLine(err.Error()))
		return wgExitFail
	}

	dctx, dcancel := context.WithTimeout(ctx, wgDialTimeout)
	var conn net.Conn
	if o.p2p {
		conn, err = sess.DialService(dctx, api.ServiceSSHID)
	} else {
		conn, err = sess.Dial(dctx)
	}
	dcancel()
	if err != nil {
		wgLine(stderr, "cannot reach the device through the tunnel: %s", describeWGError(err, &o))
		return wgExitFail
	}
	defer func() { _ = conn.Close() }()
	return relayWGStdio(ctx, sess, conn, stdin, stdout, stderr, &o)
}

// relayWGStdio copies stdin to conn and conn to stdout until either side ends,
// the session is torn down (for example because the device was revoked), or
// ctx is cancelled.
func relayWGStdio(ctx context.Context, sess *wgclient.Session, conn net.Conn, stdin io.Reader, stdout, stderr io.Writer, o *wgProxyOptions) int {
	connDone := make(chan error, 1) // conn -> stdout finished
	stdinDone := make(chan struct{})
	go func() {
		_, err := io.Copy(stdout, conn)
		connDone <- err
	}()
	go func() {
		_, _ = io.Copy(conn, stdin)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite() // stdin EOF: tell the device we are done sending
		}
		close(stdinDone)
	}()

	sessionEnded := func() int {
		if err := sess.Err(); err != nil {
			wgLine(stderr, "session ended: %s", describeWGError(err, o))
			return wgExitFail
		}
		return wgExitOK
	}
	select {
	case err := <-connDone:
		select { // a revoked session closes the conn; report why if the hub said so
		case <-sess.Done():
			return sessionEnded()
		case <-time.After(100 * time.Millisecond):
		}
		if err != nil {
			wgLine(stderr, "connection lost: %s", oneLine(err.Error()))
			return wgExitFail
		}
		return wgExitOK
	case <-stdinDone:
		// ssh closed its end: let the device finish, but not forever.
		select {
		case <-connDone:
		case <-sess.Done():
		case <-time.After(wgDrainGrace):
		case <-ctx.Done():
		}
		return wgExitOK
	case <-sess.Done():
		return sessionEnded()
	case <-ctx.Done():
		return wgExitOK
	}
}
