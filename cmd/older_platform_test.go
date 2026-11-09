package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

// An older platform does not know enable=false on its inspect routes. It must be named as such, with
// the refusal exit code, not as a permission problem or a missing pod.
func TestInspectPodOnAnOlderPlatformExitsFive(t *testing.T) {
	const want = "this platform is older than this tnctl: it cannot read a robot without turning its Shell on, so tnctl does not ask it (ask for a platform update)"
	p := newFakePlatform(t)
	p.on("GET", "/robots/vs/inspect/pods/default/whoami-1", 400,
		`{"error":{"name":"InputError","message":"The query parameter enable is not known."}}`)
	e, _ := testEnv(t, p, nil)

	_, _, err := runCLI(t, e, "inspect", "pod", "vs", "default/whoami-1")
	if err == nil || exitCodeOf(err) != codeRejected || !strings.Contains(err.Error(), want) {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	var printed bytes.Buffer
	writeError(&printed, err, "table")
	if !strings.Contains(printed.String(), want) {
		t.Errorf("tnctl printed %q, want the platform-update message", printed.String())
	}
	if n := len(p.seen("GET", "/robots/vs/inspect/pods/default/whoami-1")); n != 1 {
		t.Errorf("%d requests, want 1: tnctl must never retry without enable=false", n)
	}
}

// A platform without the stream route is not_found (exit 3), not a missing device or a login problem.
func TestMqttStreamOnAnOlderPlatformIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	fl := &api.Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo",
		Token: func(context.Context) (string, error) { return "tok", nil }}

	_, err := fl.MqttStream(context.Background(), []string{"/devices/+/state/#"})
	if err == nil {
		t.Fatal("a 404 was no error")
	}
	if got := exitCodeOf(err); got != codeNotFound || codeNames[got] != "not_found" {
		t.Errorf("exit %d (%s): %v", got, codeNames[got], err)
	}
	want := "this platform has no live MQTT stream yet: it is older than this tnctl (ask for a platform update)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
	var printed bytes.Buffer
	writeError(&printed, err, "json")
	if !strings.Contains(printed.String(), `"code":"not_found"`) || !strings.Contains(printed.String(), "older than this tnctl") {
		t.Errorf("the JSON error is %q", printed.String())
	}
}
