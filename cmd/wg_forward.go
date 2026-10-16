package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teknoir/wg-tunnel/pkg/api"
	"github.com/teknoir/wg-tunnel/pkg/svc"
	"github.com/teknoir/wg-tunnel/pkg/wgclient"
)

// wgPathWait is how long to wait for the direct path before reporting "via hub":
// the P2P race budget plus a little slack.
const wgPathWait = api.P2PDialRaceMillis*time.Millisecond + 300*time.Millisecond

// wgForwardOptions are the settings of `tnctl port-forward` over WireGuard.
type wgForwardOptions struct {
	domain     string
	namespace  string
	device     string
	service    string
	localPort  int // 0 = random free port
	hubURL     string
	turnServer string
	stunServer string
	p2p        bool
}

func (o *wgForwardOptions) validate() error {
	if o.domain == "" {
		return errors.New("no domain: set --domain or log in first")
	}
	if err := validateWGTarget(o.namespace, o.device, 22); err != nil {
		return err
	}
	if !svc.ValidID(o.service) {
		return fmt.Errorf("%q is not a service ID (lowercase letters, digits and '-')", o.service)
	}
	if o.localPort < 0 || o.localPort > 65535 {
		return errors.New("the local port must be between 1 and 65535")
	}
	if o.hubURL == "" {
		return errors.New("--wg-hub-url is required")
	}
	if err := validateHubURL(o.hubURL); err != nil {
		return err
	}
	return validateHubHost(o.hubURL, o.domain)
}

// parseWGForwardArg parses "[<local>:]<service-id>". local 0 means "pick a free port".
func parseWGForwardArg(arg string) (local int, service string, err error) {
	parts := strings.Split(arg, ":")
	switch len(parts) {
	case 1:
		service = parts[0]
	case 2:
		p, perr := strconv.Atoi(parts[0])
		if perr != nil || p < 1 || p > 65535 {
			return 0, "", fmt.Errorf("invalid local port %q (want 1-65535)", parts[0])
		}
		local, service = p, parts[1]
	default:
		return 0, "", fmt.Errorf("invalid forward %q: expected [local-port:]service-id (over WireGuard the target is a service ID, not host:port)", arg)
	}
	if !svc.ValidID(service) {
		return 0, "", fmt.Errorf("invalid service ID %q: expected lowercase letters, digits and '-' (list the device's services with 'tnctl wg status <device>')", service)
	}
	return local, service, nil
}

// pathLabel is the human form of a wgclient path.
func pathLabel(path string) string {
	switch path {
	case api.PathDirectLAN:
		return "direct (LAN)"
	case api.PathDirect:
		return "direct"
	case api.PathHub, "":
		return "via hub"
	}
	return path
}

// formatForwardBanner is the line printed once the forward is listening.
func formatForwardBanner(local int, device, service, path string) string {
	return fmt.Sprintf("Forwarding 127.0.0.1:%d -> %s/%s (path: %s)", local, device, service, pathLabel(path))
}

// forwardSession is the part of a wgclient session the forwarder uses
// (a fake in tests).
type forwardSession interface {
	DialService(ctx context.Context, service string) (net.Conn, error)
	Path() string
	Done() <-chan struct{}
	Err() error
}

// waitDirectPath polls until the session has left the hub path or the wait is over.
func waitDirectPath(ctx context.Context, s forwardSession, wait time.Duration) string {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if p := s.Path(); p != api.PathHub && p != "" {
			return p
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return s.Path()
		case <-ctx.Done():
			return s.Path()
		case <-s.Done():
			return s.Path()
		}
	}
}

// serveForward accepts connections on ln and relays each one to the service
// until ctx ends or the session does. It closes ln. The error is the session's
// when the session ended, nil on ctx cancel.
func serveForward(ctx context.Context, s forwardSession, ln net.Listener, service string, o *wgForwardOptions, stderr io.Writer) error {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		active  = map[net.Conn]struct{}{}
		lastTxt = pathLabel(s.Path())
	)
	stop := make(chan struct{})
	var once sync.Once
	shutdown := func() { once.Do(func() { close(stop); _ = ln.Close() }) }
	defer shutdown()

	go func() {
		select {
		case <-ctx.Done():
		case <-s.Done():
		case <-stop:
		}
		shutdown()
		mu.Lock()
		for c := range active {
			_ = c.Close()
		}
		mu.Unlock()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-stop:
			default:
				shutdown()
			}
			break
		}
		mu.Lock()
		active[c] = struct{}{}
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { mu.Lock(); delete(active, c); mu.Unlock(); _ = c.Close() }()
			dctx, cancel := context.WithTimeout(ctx, wgDialTimeout)
			up, derr := s.DialService(dctx, service)
			cancel()
			if derr != nil {
				wgLine(stderr, "cannot reach %s/%s: %s", o.device, service, describeForwardError(derr, o))
				return
			}
			mu.Lock()
			active[up] = struct{}{}
			mu.Unlock()
			defer func() { mu.Lock(); delete(active, up); mu.Unlock(); _ = up.Close() }()
			select { // stopped while dialling: the closer has already run
			case <-stop:
				return
			default:
			}
			p := pathLabel(s.Path())
			mu.Lock()
			changed := p != lastTxt
			lastTxt = p
			mu.Unlock()
			if changed {
				wgLine(stderr, "path is now: %s", p)
			}
			pipe(c, up)
		}()
	}
	// Give the closer goroutine its turn, then drain the relays.
	shutdown()
	mu.Lock()
	for c := range active {
		_ = c.Close()
	}
	mu.Unlock()
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	select {
	case <-s.Done():
		return s.Err()
	default:
	}
	return nil
}

// pipe copies both ways until one direction ends, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	// One direction ended (EOF or error): allow the other a moment to drain.
	t := time.NewTimer(wgDrainGrace)
	select {
	case <-done:
	case <-t.C:
	}
	t.Stop()
	_ = a.Close()
	_ = b.Close()
}

func describeForwardError(err error, o *wgForwardOptions) string {
	po := wgProxyOptions{domain: o.domain, namespace: o.namespace, device: o.device}
	return describeWGError(err, &po)
}

// runWGPortForward is `tnctl port-forward` over WireGuard. It returns the exit code.
func runWGPortForward(ctx context.Context, o wgForwardOptions, cred wgclient.Credential, hc *http.Client, web func(context.Context) bool, stdout, stderr io.Writer) int {
	if err := o.validate(); err != nil {
		wgLine(stderr, "%v", err)
		return wgExitUsage
	}
	opts := wgclient.Options{
		HubURL:     o.hubURL,
		Kind:       api.KindClient,
		Credential: cred,
		ClientRequest: &api.ClientSessionRequest{
			Namespace: o.namespace, Device: o.device, Service: o.service, Posture: wgPosture(),
		},
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
		wgLine(stderr, "%s", describeForwardError(err, &o))
		return wgExitFail
	}
	defer func() { _ = sess.Close() }()

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.localPort)))
	if err != nil {
		wgLine(stderr, "cannot listen on 127.0.0.1:%d: %s", o.localPort, oneLine(err.Error()))
		return wgExitFail
	}
	local := ln.Addr().(*net.TCPAddr).Port

	path := sess.Path()
	if o.p2p {
		path = waitDirectPath(ctx, sess, wgPathWait)
	}
	_, _ = fmt.Fprintln(stdout, formatForwardBanner(local, o.device, o.service, path))
	if web != nil && web(ctx) {
		_, _ = fmt.Fprintf(stdout, "Open http://localhost:%d\n", local)
	}
	_, _ = fmt.Fprintln(stdout, "Press Ctrl+C to stop.")

	if err := serveForward(ctx, sess, ln, o.service, &o, stderr); err != nil {
		wgLine(stderr, "session ended: %s", describeForwardError(err, &o))
		return wgExitFail
	}
	return wgExitOK
}
