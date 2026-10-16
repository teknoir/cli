package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/wg-tunnel/pkg/api"
)

func TestFormatWGStatus(t *testing.T) {
	r := wgStatusReport{
		Device: "ns1/dev-1", Hub: "https://wg.teknoir.cloud", HubReachable: true, DeviceOnline: "yes",
		Path: api.PathDirectLAN, Candidates: 3, RTT: 12300 * time.Microsecond,
		Services: []api.ServiceSpec{
			{ID: "ssh", Kind: "external", Protocol: "tcp"},
			{ID: "grafana", Kind: "internal", Protocol: "http", Web: true, Description: "Dashboards"},
		},
	}
	got := formatWGStatus(r)
	for _, want := range []string{
		"Device         ns1/dev-1",
		"Hub            reachable (https://wg.teknoir.cloud)",
		"Device online  yes",
		"Path           direct (LAN)",
		"Candidates     3",
		"RTT            12.3ms (TCP connect to ssh)",
		"Services       ssh (external, tcp)",
		"               grafana (internal, http, web) - Dashboards",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing line %q in:\n%s", want, got)
		}
	}
}

func TestFormatWGStatusProblem(t *testing.T) {
	got := formatWGStatus(wgStatusReport{
		Device: "ns1/dev-1", Hub: "https://wg.x", HubReachable: true, DeviceOnline: "no",
		Problem: "device_offline: the device agent is not connected", ServicesNote: "none published (catalog)",
	})
	for _, want := range []string{"Device online  no", "Problem        device_offline: the device agent is not connected", "Services       none published (catalog)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Path") || strings.Contains(got, "RTT") {
		t.Errorf("path/RTT shown without a session:\n%s", got)
	}
	un := formatWGStatus(wgStatusReport{Device: "a/b", Hub: "https://wg.x", DeviceOnline: "unknown"})
	if !strings.Contains(un, "unreachable (https://wg.x)") {
		t.Errorf("hub state:\n%s", un)
	}
}

func TestRunWGStatusDeviceOffline(t *testing.T) {
	var req api.ClientSessionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(api.Error{Reason: api.ReasonDeviceOffline})
	}))
	defer srv.Close()
	o := wgStatusOptions{domain: "teknoir.cloud", namespace: "ns1", device: "dev-1", hubURL: srv.URL, p2p: true}
	lookup := func(context.Context) ([]api.ServiceSpec, error) {
		return []api.ServiceSpec{{ID: "ssh", Kind: "external", Protocol: "tcp"}}, nil
	}
	var out, errb bytes.Buffer
	code := runWGStatus(context.Background(), o, okCred, nil, lookup, &out, &errb)
	if code != wgExitFail {
		t.Errorf("code %d", code)
	}
	for _, want := range []string{"reachable (" + srv.URL + ")", "Device online  no", "device_offline: the device agent is not connected", "ssh (external, tcp)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if req.Service != "ssh" || req.Device != "dev-1" {
		t.Errorf("request %+v", req)
	}
}

func TestRunWGStatusHubUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	o := wgStatusOptions{domain: "teknoir.cloud", namespace: "ns1", device: "dev-1", hubURL: url}
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := runWGStatus(ctx, o, okCred, nil, func(context.Context) ([]api.ServiceSpec, error) { return nil, nil }, &out, &errb)
	if code != wgExitFail || !strings.Contains(out.String(), "unreachable") {
		t.Errorf("code %d\n%s", code, out.String())
	}
}

func TestServicesFromAnnotations(t *testing.T) {
	ann := map[string]string{api.AnnotationServices: `[{"id":"ssh","kind":"external","protocol":"tcp"},{"id":"ui","protocol":"http","web":true}]`}
	s, err := servicesFromAnnotations(ann)
	if err != nil || len(s) != 2 || !s[1].Web {
		t.Fatalf("%v %v", s, err)
	}
	if s, err := servicesFromAnnotations(nil); err != nil || s != nil {
		t.Errorf("empty: %v %v", s, err)
	}
	if _, err := servicesFromAnnotations(map[string]string{api.AnnotationServices: "{"}); err == nil {
		t.Error("bad JSON accepted")
	}
}
