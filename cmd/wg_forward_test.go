package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teknoir/wg-tunnel/pkg/api"
)

func TestParseWGForwardArg(t *testing.T) {
	ok := []struct {
		in      string
		local   int
		service string
	}{
		{"grafana", 0, "grafana"},
		{"8080:grafana", 8080, "grafana"},
		{"ssh", 0, "ssh"},
		{"65535:node-red", 65535, "node-red"},
	}
	for _, c := range ok {
		l, s, err := parseWGForwardArg(c.in)
		if err != nil || l != c.local || s != c.service {
			t.Errorf("%q -> %d %q %v", c.in, l, s, err)
		}
	}
	bad := []string{"", "0:grafana", "70000:grafana", "x:grafana", "8080:localhost:80", "Grafana", "8080:", "-oProxy", "a b", "localhost:80:x"}
	for _, in := range bad {
		if _, _, err := parseWGForwardArg(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestFormatForwardBanner(t *testing.T) {
	cases := map[string]string{
		api.PathDirectLAN: "Forwarding 127.0.0.1:8080 -> dev-1/grafana (path: direct (LAN))",
		api.PathDirect:    "Forwarding 127.0.0.1:8080 -> dev-1/grafana (path: direct)",
		api.PathHub:       "Forwarding 127.0.0.1:8080 -> dev-1/grafana (path: via hub)",
	}
	for path, want := range cases {
		if got := formatForwardBanner(8080, "dev-1", "grafana", path); got != want {
			t.Errorf("%s: %q", path, got)
		}
	}
}

func goodForward(hub string) wgForwardOptions {
	return wgForwardOptions{domain: "teknoir.cloud", namespace: "ns1", device: "dev-1", service: "grafana", hubURL: hub, p2p: true}
}

func TestWGForwardOptionsValidate(t *testing.T) {
	o := goodForward("https://wg.teknoir.cloud")
	if err := o.validate(); err != nil {
		t.Fatalf("good rejected: %v", err)
	}
	cases := map[string]func(*wgForwardOptions){
		"no domain":     func(o *wgForwardOptions) { o.domain = "" },
		"bad device":    func(o *wgForwardOptions) { o.device = "-x" },
		"bad service":   func(o *wgForwardOptions) { o.service = "a:b" },
		"empty service": func(o *wgForwardOptions) { o.service = "" },
		"neg port":      func(o *wgForwardOptions) { o.localPort = -1 },
		"http hub":      func(o *wgForwardOptions) { o.hubURL = "http://wg.teknoir.cloud" },
		"foreign hub":   func(o *wgForwardOptions) { o.hubURL = "https://evil.example.com" },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			o := goodForward("https://wg.teknoir.cloud")
			m(&o)
			if o.validate() == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// fakeSession dials into an in-process echo (or a failing dial).
type fakeSession struct {
	mu      sync.Mutex
	path    string
	dials   atomic.Int32
	dialErr error
	done    chan struct{}
	err     error
	last    string
}

func newFake(path string) *fakeSession { return &fakeSession{path: path, done: make(chan struct{})} }

func (f *fakeSession) DialService(_ context.Context, service string) (net.Conn, error) {
	f.dials.Add(1)
	f.mu.Lock()
	f.last = service
	f.mu.Unlock()
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	a, b := net.Pipe()
	go func() { // upper-casing echo
		defer func() { _ = b.Close() }()
		buf := make([]byte, 256)
		for {
			n, err := b.Read(buf)
			if n > 0 {
				_, _ = b.Write([]byte(strings.ToUpper(string(buf[:n]))))
			}
			if err != nil {
				return
			}
		}
	}()
	return a, nil
}
func (f *fakeSession) Path() string          { f.mu.Lock(); defer f.mu.Unlock(); return f.path }
func (f *fakeSession) setPath(p string)      { f.mu.Lock(); f.path = p; f.mu.Unlock() }
func (f *fakeSession) Done() <-chan struct{} { return f.done }
func (f *fakeSession) Err() error            { return f.err }

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func roundTrip(t *testing.T, addr, msg string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	return string(buf)
}

func TestServeForwardRelaysAndStops(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := newFake(api.PathDirect)
	var stderr syncBuf
	o := goodForward("https://wg.teknoir.cloud")
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- serveForward(ctx, f, ln, "grafana", &o, &stderr) }()

	addr := ln.Addr().String()
	for i := 0; i < 3; i++ { // several connections, each its own dial
		if got := roundTrip(t, addr, "hello"); got != "HELLO" {
			t.Fatalf("got %q", got)
		}
	}
	if f.dials.Load() != 3 {
		t.Errorf("dials %d", f.dials.Load())
	}
	if f.last != "grafana" {
		t.Errorf("dialled service %q", f.last)
	}
	// The path falls back to the hub: the user is told once.
	f.setPath(api.PathHub)
	roundTrip(t, addr, "x")
	roundTrip(t, addr, "x")
	if n := strings.Count(stderr.String(), "path is now: via hub"); n != 1 {
		t.Errorf("path change reported %d times: %q", n, stderr.String())
	}
	cancel()
	select {
	case err := <-res:
		if err != nil {
			t.Errorf("err %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serveForward did not stop")
	}
	if _, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
		t.Error("listener still open")
	}
}

func TestServeForwardDialFailureKeepsListening(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	f := newFake(api.PathHub)
	f.dialErr = &wgErr{reason: api.ReasonServiceDenied}
	var stderr syncBuf
	o := goodForward("https://wg.teknoir.cloud")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = serveForward(ctx, f, ln, "grafana", &o, &stderr) }()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("expected the connection to be closed")
	}
	_ = c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(stderr.String(), "cannot reach dev-1/grafana") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "cannot reach dev-1/grafana") {
		t.Errorf("no error line: %q", stderr.String())
	}
	// still accepting
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("listener closed after a failed dial: %v", err)
	}
	_ = c2.Close()
}

// wgErr is a stand-in that is not a *wgclient.Error (plain dial failure text).
type wgErr struct{ reason string }

func (e *wgErr) Error() string { return "dial failed: " + e.reason }

func TestServeForwardSessionEndReturnsError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	f := newFake(api.PathHub)
	f.err = errors.New("session expired")
	o := goodForward("https://wg.teknoir.cloud")
	res := make(chan error, 1)
	go func() { res <- serveForward(context.Background(), f, ln, "grafana", &o, io.Discard) }()
	close(f.done)
	select {
	case err := <-res:
		if err == nil {
			t.Error("session end not reported")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not return")
	}
}

func TestWaitDirectPath(t *testing.T) {
	f := newFake(api.PathHub)
	go func() { time.Sleep(120 * time.Millisecond); f.setPath(api.PathDirectLAN) }()
	if got := waitDirectPath(context.Background(), f, 2*time.Second); got != api.PathDirectLAN {
		t.Errorf("got %q", got)
	}
	g := newFake(api.PathHub)
	start := time.Now()
	if got := waitDirectPath(context.Background(), g, 150*time.Millisecond); got != api.PathHub || time.Since(start) > time.Second {
		t.Errorf("got %q after %v", got, time.Since(start))
	}
}

func TestRunWGPortForwardDenials(t *testing.T) {
	cases := []struct {
		name   string
		status int
		reason string
		want   string
	}{
		{"service unknown", 404, api.ReasonServiceUnknown, "tnctl: wireguard: service_unknown: the device has no such service; list them with 'tnctl wg status <device>'\n"},
		{"service denied", 403, api.ReasonServiceDenied, "tnctl: wireguard: service_denied: the device refused the service target; ask a teamspace admin to check the service definition\n"},
		{"p2p unavailable", 409, api.ReasonP2PUnavailable, "tnctl: wireguard: p2p_unavailable: the device cannot take a direct connection; retry with --no-p2p to go through the hub\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got api.ClientSessionRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_ = json.NewEncoder(w).Encode(api.Error{Reason: c.reason})
			}))
			defer srv.Close()
			var out, errb bytes.Buffer
			code := runWGPortForward(context.Background(), goodForward(srv.URL), okCred, nil, nil, &out, &errb)
			if code != wgExitFail || errb.String() != c.want || out.Len() != 0 {
				t.Errorf("code %d stderr %q stdout %q", code, errb.String(), out.String())
			}
			if got.Service != "grafana" || got.Port != 0 || got.Device != "dev-1" {
				t.Errorf("request %+v", got)
			}
		})
	}
}

func TestRunWGPortForwardUsage(t *testing.T) {
	o := goodForward("https://wg.teknoir.cloud")
	o.service = "Bad Service"
	var out, errb bytes.Buffer
	if code := runWGPortForward(context.Background(), o, okCred, nil, nil, &out, &errb); code != wgExitUsage {
		t.Errorf("code %d", code)
	}
}

func TestPortForwardWireGuardFlags(t *testing.T) {
	for _, n := range []string{"wireguard", "no-p2p", "turn-server", "wg-hub-url", "port", "to"} {
		if portForwardCmd.Flags().Lookup(n) == nil {
			t.Errorf("--%s missing on port-forward", n)
		}
	}
	if f := portForwardCmd.Flags().Lookup("wireguard"); f.DefValue != "false" {
		t.Errorf("--wireguard default %q", f.DefValue)
	}
	if sshCmd.Flags().Lookup("no-p2p") == nil {
		t.Error("--no-p2p missing on ssh")
	}
}

func TestProxyCommandP2PFlag(t *testing.T) {
	a := proxyArgs{Exe: "/usr/bin/tnctl", Domain: "d", Namespace: "n", Device: "x", Port: 22, KnownHostsFile: "/tmp/kh", HubURL: "https://wg.d"}
	if strings.Contains(buildProxyCommand(a, "linux"), "--p2p") {
		t.Error("--p2p present when P2P is off")
	}
	a.P2P = true
	if !strings.HasSuffix(buildProxyCommand(a, "linux"), " --p2p") {
		t.Errorf("--p2p missing: %s", buildProxyCommand(a, "linux"))
	}
}

func TestWGProxyP2PRequestsSSHService(t *testing.T) {
	var got api.ClientSessionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(api.Error{Reason: api.ReasonDeviceOffline})
	}))
	defer srv.Close()
	o := goodOpts(srv.URL, t.TempDir())
	o.p2p = true
	var out, errb bytes.Buffer
	if code := runWGProxy(context.Background(), o, okCred, nil, strings.NewReader(""), &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
	if got.Service != api.ServiceSSHID || got.Port != 0 {
		t.Errorf("request %+v", got)
	}
}
