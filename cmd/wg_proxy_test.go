package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/teknoir/cli/pkg/auth"
	"github.com/teknoir/wg-tunnel/pkg/api"
)

func goodOpts(hub, dir string) wgProxyOptions {
	return wgProxyOptions{
		domain: "teknoir.cloud", namespace: "ns1", device: "dev-1", port: 22,
		knownHostsFile: filepath.Join(dir, "known_hosts"), hubURL: hub,
	}
}

func TestWGProxyOptionsValidate(t *testing.T) {
	if err := (func() error { o := goodOpts("https://wg.teknoir.cloud", t.TempDir()); return o.validate() })(); err != nil {
		t.Fatalf("good options rejected: %v", err)
	}
	cases := map[string]func(*wgProxyOptions){
		"no domain":          func(o *wgProxyOptions) { o.domain = "" },
		"no namespace":       func(o *wgProxyOptions) { o.namespace = "" },
		"bad namespace":      func(o *wgProxyOptions) { o.namespace = "NS 1" },
		"no device":          func(o *wgProxyOptions) { o.device = "" },
		"device with option": func(o *wgProxyOptions) { o.device = "-oProxyCommand=x" },
		"port 0":             func(o *wgProxyOptions) { o.port = 0 },
		"port too big":       func(o *wgProxyOptions) { o.port = 70000 },
		"no known hosts":     func(o *wgProxyOptions) { o.knownHostsFile = "" },
		"no hub":             func(o *wgProxyOptions) { o.hubURL = "" },
		"http hub":           func(o *wgProxyOptions) { o.hubURL = "http://wg.teknoir.cloud" },
		"direct and turn":    func(o *wgProxyOptions) { o.direct = true; o.turnServer = "h:1" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := goodOpts("https://wg.teknoir.cloud", t.TempDir())
			mutate(&o)
			if err := o.validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestWGHintCoversEveryReason(t *testing.T) {
	reasons := []string{
		api.ReasonUnauthenticated, api.ReasonNotEnrolled, api.ReasonReenroll, api.ReasonEnrollClosed, api.ReasonEnrollConflict,
		api.ReasonSessionLimit, api.ReasonReplay, api.ReasonForbidden, api.ReasonDeviceUnknown, api.ReasonDeviceDisabled,
		api.ReasonDeviceRevoked, api.ReasonDeviceOffline, api.ReasonPortNotAllowed, api.ReasonPosture, api.ReasonSessionUnknown,
		api.ReasonSessionMaxAge, api.ReasonRateLimited, api.ReasonBadRequest, api.ReasonInternal,
	}
	for _, r := range reasons {
		if h := wgHint(r, "ns1", "dev-1"); h == "" || h == "access denied" {
			t.Errorf("reason %s has no specific hint (%q)", r, h)
		}
	}
	if got := wgHint("made_up", "ns1", "dev-1"); got != "access denied" {
		t.Errorf("unknown reason: %q", got)
	}
	if got := wgHint(api.ReasonDeviceRevoked, "ns1", "dev-1"); got != "device is flagged compromised/health-check-failed in Teknoir" {
		t.Errorf("revoked: %q", got)
	}
	if got := wgHint(api.ReasonForbidden, "ns1", "dev-1"); got != "you need edit/admin access to teamspace ns1" {
		t.Errorf("forbidden: %q", got)
	}
	if got := wgHint(api.ReasonDeviceOffline, "ns1", "dev-1"); got != "the device agent is not connected" {
		t.Errorf("offline: %q", got)
	}
}

func okCred(context.Context, string) (string, error) { return "test-token", nil }

func TestRunWGProxyDenials(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    any
		wantErr string
	}{
		{"revoked", 403, api.Error{Reason: api.ReasonDeviceRevoked},
			"tnctl: wireguard: device_revoked: device is flagged compromised/health-check-failed in Teknoir\n"},
		{"revoked with operator note", 403, api.Error{Reason: api.ReasonDeviceRevoked, Message: "stolen from site 4"},
			"tnctl: wireguard: device_revoked: device is flagged compromised/health-check-failed in Teknoir (hub: stolen from site 4)\n"},
		{"forbidden", 403, api.Error{Reason: api.ReasonForbidden},
			"tnctl: wireguard: forbidden: you need edit/admin access to teamspace ns1\n"},
		{"offline", 409, api.Error{Reason: api.ReasonDeviceOffline},
			"tnctl: wireguard: device_offline: the device agent is not connected\n"},
		{"not enrolled", 409, api.Error{Reason: api.ReasonNotEnrolled},
			"tnctl: wireguard: not_enrolled: the device agent has not enrolled with the hub yet\n"},
		{"401 without body", 401, nil,
			"tnctl: wireguard: unauthenticated: your tnctl login was not accepted; run 'tnctl login' and try again\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			var gotAuth, gotPath string
			var gotReq api.ClientSessionRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotReq)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				if c.body != nil {
					_ = json.NewEncoder(w).Encode(c.body)
				}
			}))
			defer srv.Close()

			dir := t.TempDir()
			o := goodOpts(srv.URL, dir)
			var stdout, stderr bytes.Buffer
			code := runWGProxy(context.Background(), o, okCred, nil, strings.NewReader("SSH-2.0-x\r\n"), &stdout, &stderr)

			if code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			if stderr.String() != c.wantErr {
				t.Errorf("stderr\n got %q\nwant %q", stderr.String(), c.wantErr)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout must stay clean for ssh, got %q", stdout.String())
			}
			if strings.Contains(stderr.String(), "test-token") {
				t.Error("token leaked to stderr")
			}
			if _, err := os.Stat(o.knownHostsFile); err == nil {
				t.Error("known_hosts written for a denied session")
			}
			if calls.Load() != 1 || gotPath != api.ClientSessionPath || gotAuth != "Bearer test-token" {
				t.Errorf("calls=%d path=%s auth-set=%v", calls.Load(), gotPath, gotAuth == "Bearer test-token")
			}
			if gotReq.Namespace != "ns1" || gotReq.Device != "dev-1" || gotReq.Port != 22 ||
				gotReq.Posture.Software != "tnctl" || gotReq.Posture.Version != Version || gotReq.Posture.OS == "" || gotReq.Posture.Arch == "" {
				t.Errorf("request = %+v", gotReq)
			}
		})
	}
}

func TestRunWGProxyUsageErrors(t *testing.T) {
	o := goodOpts("https://wg.teknoir.cloud", t.TempDir())
	o.device = ""
	var stdout, stderr bytes.Buffer
	code := runWGProxy(context.Background(), o, okCred, nil, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.HasPrefix(stderr.String(), "tnctl: wireguard: --device is required") || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("stderr %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout %q", stdout.String())
	}
}

func TestRunWGProxyNotLoggedIn(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	defer srv.Close()
	cred := func(context.Context, string) (string, error) { return "", &auth.NotLoggedInError{Domain: "teknoir.cloud"} }
	var stdout, stderr bytes.Buffer
	code := runWGProxy(context.Background(), goodOpts(srv.URL, t.TempDir()), cred, nil, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || stderr.String() != "tnctl: wireguard: not logged in to teknoir.cloud; run 'tnctl login'\n" {
		t.Errorf("code=%d stderr=%q", code, stderr.String())
	}
	if hit.Load() {
		t.Error("hub contacted without credentials")
	}
}

func TestDescribeWGErrorOther(t *testing.T) {
	o := goodOpts("https://x", "")
	if got := describeWGError(errors.New("line1\nline2"), &o); got != "line1 line2" {
		t.Errorf("got %q", got)
	}
}

func TestWGProxyCommandRegistration(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "wg-proxy" {
			found = true
			if !c.Hidden {
				t.Error("wg-proxy must be hidden")
			}
			for _, f := range []string{"namespace", "device", "port", "known-hosts-file", "turn-server", "hub-url", "direct"} {
				if c.Flags().Lookup(f) == nil {
					t.Errorf("flag --%s missing", f)
				}
			}
		}
	}
	if !found {
		t.Fatal("wg-proxy not registered")
	}
}

// Through cobra: a bad invocation exits with code 2 via exitError, with the
// one-line message and nothing on stdout.
func TestWGProxyCobraUsageExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	r, w, _ := os.Pipe()
	oldErr := os.Stderr
	os.Stderr = w
	rootCmd.SetArgs([]string{"wg-proxy", "--hub-url", "https://wg.teknoir.cloud"})
	err := rootCmd.Execute()
	_ = w.Close()
	os.Stderr = oldErr
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	rootCmd.SetArgs(nil)

	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(buf.String(), "tnctl: wireguard: --namespace is required") {
		t.Errorf("stderr %q", buf.String())
	}
}
