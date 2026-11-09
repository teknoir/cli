package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/auth"
)

// ---------- whoami ----------

func TestWhoamiBotTableAndJSON(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/whoami", 200, botWhoamiBody)
	e := botEnv(t, p)
	out, _, err := runCLI(t, e, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bot teknoir-demo/scout for a@b.c, access read, unit Karlstad (se-karlstad)\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	out, _, err = runCLI(t, e, "whoami", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Mode string
		Bot  struct {
			Namespace, Name, Unit, Access string
			UnitID                        string `json:"unitId"`
			ConfiguredAccess              string `json:"configuredAccess"`
		}
		Email string
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Mode != "bot" || v.Bot.Name != "scout" || v.Bot.Access != "read" ||
		v.Bot.UnitID != "se-karlstad" || v.Bot.ConfiguredAccess != "read" {
		t.Errorf("%v: %s", err, out)
	}
}

// A bot set to creator whose creator may only view acts at read; the table says why. A platform that
// predates configuredAccess and unitId prints as before.
func TestWhoamiBotLoweredAccess(t *testing.T) {
	cases := map[string]string{
		`{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":"Karlstad","unitId":"Karlstad","access":"read","configuredAccess":"creator"},"creator":{"email":"a@b.c"},"domain":"teknoir.cloud"}`: "bot teknoir-demo/scout for a@b.c, access read (set to creator, but a@b.c may only view), unit Karlstad\n",
		`{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":null,"unitId":null,"access":"none","configuredAccess":"read"},"creator":{"email":"a@b.c"},"domain":"teknoir.cloud"}`:                "bot teknoir-demo/scout for a@b.c, access none (set to read), unit —\n",
		`{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":"Karlstad","access":"creator"},"creator":{"email":"a@b.c"},"domain":"teknoir.cloud"}`:                                               "bot teknoir-demo/scout for a@b.c, access creator, unit Karlstad\n",
	}
	for body, want := range cases {
		p := newBotAPI(t)
		p.on("GET", "/whoami", 200, body)
		e := botEnv(t, p)
		out, _, err := runCLI(t, e, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		if out != want {
			t.Errorf("got %q, want %q", out, want)
		}
	}
}

func TestWhoamiBotWithoutUnit(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/whoami", 200, `{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":null,"access":"none"},"creator":{"email":"a@b.c"},"domain":"teknoir.cloud"}`)
	e := botEnv(t, p)
	out, _, err := runCLI(t, e, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bot teknoir-demo/scout for a@b.c, access none, unit —\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// useConfigFile gives this test a HOME of its own with that .tnctl.yaml in it: initConfig reads it
// on every command run, as it does for a person, and the test that comes next brings its own.
// cfgFile names that file, so viper's search paths (which only ever grow) cannot pick up another
// test's file or the developer's. Nothing here resets viper: that would also drop the flag bindings
// and defaults root.go binds once.
func useConfigFile(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".tnctl.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	old := cfgFile
	cfgFile = filepath.Join(home, ".tnctl.yaml")
	t.Cleanup(func() { cfgFile = old })
}

// useLogin stores a login for teknoir.cloud in a temporary config file, as the login tests do.
func useLogin(t *testing.T, claims map[string]any, namespace string) {
	t.Helper()
	auths := map[string]any{"teknoir_cloud": map[string]any{
		"access_token": testJWT(t, claims), "namespace": namespace,
	}}
	b, _ := json.Marshal(map[string]any{"domain": "teknoir.cloud", "auths": auths})
	useConfigFile(t, string(b))
}

func TestWhoamiUserTableAndJSON(t *testing.T) {
	noBotMode(t)
	useLogin(t, map[string]any{"email": "a@b.c", "preferred_username": "a"}, "teknoir-demo")
	e, _ := testEnv(t, newFakePlatform(t), nil)
	out, _, err := runCLI(t, e, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if want := "user a@b.c on teknoir.cloud, teamspace teknoir-demo\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	out, _, err = runCLI(t, e, "whoami", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v whoamiUserJSON
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Mode != "user" || v.Email != "a@b.c" || v.Domain != "teknoir.cloud" || v.Namespace != "teknoir-demo" {
		t.Errorf("%+v", v)
	}
}

func TestWhoamiUserNotLoggedInExits6(t *testing.T) {
	noBotMode(t)
	useConfigFile(t, "domain: teknoir.cloud\n")
	e, _ := testEnv(t, newFakePlatform(t), nil)
	_, _, err := runCLI(t, e, "whoami")
	if err == nil || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

// ---------- robot add ----------

func TestRobotAdd(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("POST", "/robots", 200, `{"name":"amr-02","namespace":"teknoir-demo","type":"amr","device":"amr-02","phase":"Provisioning"}`)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "robot", "add", "amr-02", "--type", "amr", "--device", "amr-01",
		"--unit", "karlstad", "--label", "site=karlstad", "--label", "env=prod")
	if err != nil {
		t.Fatal(err)
	}
	if want := "robot amr-02 created (device amr-02)\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	got := p.seen("POST", "/robots")
	if len(got) != 1 {
		t.Fatalf("requests: %+v", p.reqs)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "amr-02" || body["type"] != "amr" || body["device"] != "amr-01" || body["orgUnit"] != "karlstad" {
		t.Errorf("body %s", got[0].Body)
	}
	labels, _ := body["labels"].(map[string]any)
	if len(labels) != 2 || labels["site"] != "karlstad" || labels["env"] != "prod" {
		t.Errorf("labels %s", got[0].Body)
	}

	out, _, err = runCLI(t, e, "robot", "add", "amr-03", "--type", "amr", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rob struct{ Name, Device string }
	if err := json.Unmarshal([]byte(out), &rob); err != nil || rob.Name != "amr-02" {
		t.Errorf("%v: %s", err, out)
	}
}

func TestRobotAddValidation(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"robot", "add", "amr-02"}, "--type is required"},
		{[]string{"robot", "add", "Amr_02", "--type", "amr"}, "is not a DNS label"},
		{[]string{"robot", "add", "amr-02", "--type", "amr", "--device", "Amr_01"}, "is not a DNS label"},
		{[]string{"robot", "add", "amr-02", "--type", "amr", "--label", "novalue"}, "expected key=value"},
		{[]string{"robot", "add", "amr-02", "--type", "amr", "--label", "a b=c"}, "is not a Kubernetes label"},
		{[]string{"robot", "add"}, "robot add needs a robot name"},
	}
	for _, c := range cases {
		_, _, err := runCLI(t, e, c.args...)
		if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: exit %d, %v", c.args, exitCodeOf(err), err)
		}
	}
	if n := len(p.seen("POST", "/robots")); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// ---------- api ----------

func TestAPICallsTheFleetRoot(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots", 200, `[{"name":"a"}]`)
	p.on("POST", "/robots", 200, `{"name":"b"}`)
	p.on("GET", "/robots/a", 200, `{"name":"a","extra":1}`)
	e := testAPIRoot(t, p)
	stdin := strings.NewReader(`{"name":"from-stdin","type":"amr"}`)

	if out, _, err := runCLIWith(t, e, nil, "api", "GET", "teknoir-demo/robots"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, `"name": "a"`) {
		t.Errorf("stdout %q", out)
	}
	file := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(file, []byte(`{"name":"from-file","type":"amr"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cliStdin = stdin
	t.Cleanup(func() { cliStdin = nil })
	for _, args := range [][]string{
		{"api", "POST", "teknoir-demo/robots", "--data", `{"name":"from-text","type":"amr"}`},
		{"api", "POST", "teknoir-demo/robots", "--data", "@" + file},
		{"api", "POST", "teknoir-demo/robots", "--data", "-"},
	} {
		if _, _, err := runCLIWith(t, e, nil, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	got := p.seen("POST", "/robots")
	if len(got) != 3 {
		t.Fatalf("requests: %+v", p.reqs)
	}
	for i, want := range []string{"from-text", "from-file", "from-stdin"} {
		if !strings.Contains(got[i].Body, `"name":"`+want+`"`) {
			t.Errorf("body %d %s, want %s", i, got[i].Body, want)
		}
	}
	// the fleet root, not the teamspace base: -n does not change the path
	_, _, err := runCLIWith(t, e, nil, "api", "GET", "teknoir-demo/robots/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.seen("GET", "/robots/a")) != 1 {
		t.Errorf("requests: %+v", p.reqs)
	}
}

// testAPIRoot is runCLIWith with api's env: the fleet root of the fake platform.
func testAPIRoot(t *testing.T, p *fakePlatform) *cliEnv {
	t.Helper()
	e, _ := testEnv(t, p, nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{
			Base:  p.srv.URL + "/api/device/fleet/teknoir-demo",
			Root:  p.srv.URL + "/api/device/fleet",
			Token: func(context.Context) (string, error) { return "tok", nil },
		}, "teknoir.cloud", "teknoir-demo", nil
	}
	return e
}

func TestAPIRefusesDeviceLoginsAndBadInput(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("POST", "/devices/d1/credentials", 200, credsJSON("d1"))
	e := testAPIRoot(t, p)
	for _, args := range [][]string{
		{"api", "POST", "teknoir-demo/devices/d1/credentials"},
		{"api", "POST", "teknoir-demo/devices/d1/credentials/"},
		{"api", "POST", "teknoir-demo/devices/d1/credentials?x=1"},
		{"api", "POST", "teknoir-demo/devices/credentials"},
	} {
		_, _, err := runCLIWith(t, e, nil, args...)
		if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "does not call a device's login route") {
			t.Errorf("%v: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
	// bad method and body
	for _, args := range [][]string{
		{"api", "FETCH", "teknoir-demo/robots"},
		{"api", "POST", "teknoir-demo/robots", "--data", "not-json"},
		{"api", "GET", "https://example.com/x"},
		{"api", "GET"},
	} {
		if _, _, err := runCLIWith(t, e, nil, args...); err == nil || exitCodeOf(err) != codeUsage {
			t.Errorf("%v: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
	// bot mode refuses a device login too
	pb := newBotAPI(t)
	eb := botEnv(t, pb)
	if _, _, err := runCLI(t, eb, "api", "POST", "teknoir-demo/devices/d1/credentials"); err == nil || exitCodeOf(err) != codeUsage {
		t.Errorf("bot mode: %v", err)
	}
	if n := pb.count(); n != 0 {
		t.Errorf("%d requests were sent in bot mode", n)
	}
}

// TestAPIPathRules is M3: the path rules of `tnctl api`, in both modes.
func TestAPIPathRules(t *testing.T) {
	bad := []string{
		"../../etc",
		"a/../b",
		"a/./b",
		"/abs",
		"a//b",
		`a\b`,
		"http://x/y",
		"teknoir-demo/devices/x/credentials/.",
		"teknoir-demo/devices/x/credentials/x/..",
		"teknoir-demo/devices/credentials%2f.",
		"teknoir-demo/devices/credentials;x",
		"teknoir-demo/devices/CREDENTIALS",
	}
	for _, mode := range []string{"human", "bot"} {
		t.Run(mode, func(t *testing.T) {
			var e *cliEnv
			var on func(path string, status int, body string)
			var seen func(path string) []rec
			var count func() int
			if mode == "bot" {
				p := newBotAPI(t)
				e = botEnv(t, p)
				on = func(path string, status int, body string) {
					p.on("GET", "/fleet/teknoir-demo"+path, status, body)
				}
				seen = func(path string) []rec { return p.seen("GET", "/fleet/teknoir-demo"+path) }
				count = p.count
			} else {
				noBotMode(t)
				p := newFakePlatform(t)
				e = testAPIRoot(t, p)
				on = func(path string, status int, body string) { p.on("GET", path, status, body) }
				seen = func(path string) []rec { return p.seen("GET", path) }
				count = p.countAll
			}
			for _, path := range bad {
				if _, _, err := runCLI(t, e, "api", "GET", path); err == nil || exitCodeOf(err) != codeUsage {
					t.Errorf("%q: exit %d, %v", path, exitCodeOf(err), err)
				}
			}
			if n := count(); n != 0 {
				t.Errorf("%d requests were sent", n)
			}
			// a good path with a query reaches the route, query unchanged
			on("/robots", 200, `[]`)
			if _, _, err := runCLI(t, e, "api", "GET", "teknoir-demo/robots?unit=a%2Fb"); err != nil {
				t.Fatal(err)
			}
			got := seen("/robots")
			if len(got) != 1 || got[0].Query != "unit=a%2Fb" {
				t.Errorf("requests %+v", got)
			}
		})
	}
}

// TestAPIDashDIsTheDomain is M4: -d is the root's --domain, so a body written with it must not pass
// for a body.
func TestAPIDashDIsTheDomain(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("POST", "/robots", 200, `{"name":"x"}`)
	e := testAPIRoot(t, p)
	for _, v := range []string{`{"name":"x"}`, `["a"]`, "@body.json", "-"} {
		_, _, err := runCLI(t, e, "api", "POST", "teknoir-demo/robots", "-d", v)
		if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "-d is --domain; send a body with --data") {
			t.Errorf("-d %s: exit %d, %v", v, exitCodeOf(err), err)
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
	// --data is the body
	if _, _, err := runCLI(t, e, "api", "POST", "teknoir-demo/robots", "--data", `{"name":"x"}`); err != nil {
		t.Fatal(err)
	}
	if n := len(p.seen("POST", "/robots")); n != 1 {
		t.Errorf("%d requests", n)
	}
}

// countAll is how many requests the fake platform saw in total.
func (p *fakePlatform) countAll() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// ---------- inspect ----------

const inspectPodBody = `{"robot":"vs","device":"vs","pod":{"name":"whoami-1","namespace":"default","phase":"Running",
"node":"vs-agent","restarts":3,"conditions":[{"type":"Ready","status":"True"}],
"containers":[{"name":"whoami","init":false,"image":"traefik/whoami:v1","ready":true,"restarts":3,"state":{"kind":"running"},"oomKilled":false,"env":[],"envFrom":[],"mounts":[]}],
"initContainers":[],"labels":{}},
"events":[{"type":"Normal","reason":"Scheduled","count":1},{"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container","count":7,"last":"2026-11-08T10:00:00Z"}],
"eventsTruncated":false,"fetchedAt":"2026-11-08T10:00:00Z"}`

func TestInspectPod(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots/vs/inspect/pods/default/whoami-1", 200, inspectPodBody)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "inspect", "pod", "vs", "default/whoami-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"default/whoami-1", "Running", "whoami  ready, 3 restarts, running", "BackOff", "7"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	got := p.seen("GET", "/robots/vs/inspect/pods/default/whoami-1")
	if len(got) != 1 || got[0].Query != "enable=false" {
		t.Errorf("query %q", got[0].Query)
	}
	// -o json is the platform's answer unchanged
	out, _, err = runCLI(t, e, "inspect", "pod", "vs", "default/whoami-1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil || raw["eventsTruncated"] != false {
		t.Errorf("%v: %s", err, out)
	}
}

func TestInspectLogs(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots/vs/inspect/pods/default/whoami-1/logs", 200,
		`{"robot":"vs","device":"vs","namespace":"default","pod":"whoami-1","container":"app","previous":true,"tail":50,"since":"10m",
"text":"line one\nline two\n","lines":2,"bytes":18,"truncated":{"bytes":false,"lines":false},"redacted":0,"fetchedAt":"2026-11-08T10:00:00Z"}`)
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "inspect", "logs", "vs", "default/whoami-1", "-c", "app", "--previous", "--tail", "50", "--since", "10m")
	if err != nil {
		t.Fatal(err)
	}
	if out != "line one\nline two\n" {
		t.Errorf("stdout %q", out)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	got := p.seen("GET", "/robots/vs/inspect/pods/default/whoami-1/logs")
	if len(got) != 1 {
		t.Fatalf("requests %+v", p.reqs)
	}
	for _, want := range []string{"container=app", "enable=false", "previous=true", "since=10m", "tail=50"} {
		if !strings.Contains(got[0].Query, want) {
			t.Errorf("query %q lacks %q", got[0].Query, want)
		}
	}
	// a bad --since and a bad container name are client side
	for _, args := range [][]string{
		{"inspect", "logs", "vs", "default/whoami-1", "--since", "ten minutes"},
		{"inspect", "logs", "vs", "default/whoami-1", "-c", "a b"},
		{"inspect", "logs", "vs", "whoami-1"},
	} {
		if _, _, err := runCLI(t, e, args...); err == nil || exitCodeOf(err) != codeUsage {
			t.Errorf("%v: exit %d %v", args, exitCodeOf(err), err)
		}
	}
}

func TestInspectLogsShellOffShowsThePlatformsWords(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots/vs/inspect/pods/default/whoami-1/logs", 409,
		`{"code":"terminal_off","message":"The Shell of vs is off; ask a person to turn it on.","error":{"name":"x","message":"y"}}`)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "inspect", "logs", "vs", "default/whoami-1")
	if err == nil || err.Error() != "The Shell of vs is off; ask a person to turn it on." {
		t.Errorf("err %v", err)
	}
	if exitCodeOf(err) != codeRejected {
		t.Errorf("exit %d, want %d", exitCodeOf(err), codeRejected)
	}
}

func TestInspectRelease(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots/vs/inspect/releases/default/notebook", 200,
		`{"robot":"vs","device":"vs","name":"notebook","namespace":"default","revision":3,"status":"deployed",
"chart":"notebook","chartVersion":"0.0.5","appVersion":"2.17.0","lastDeployed":"2026-11-08T09:00:00Z",
"history":[{"revision":3,"status":"deployed"}],
"objects":[{"index":0,"apiVersion":"apps/v1","kind":"Deployment","name":"notebook","namespace":"default"},
{"index":1,"kind":"Secret","name":"notebook-secrets","namespace":"default","hidden":true},
{"index":2,"kind":"ClusterRole","name":"notebook-reader","namespace":""}],
"yaml":"x: 1\n","fetchedAt":"2026-11-08T10:00:00Z"}`)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "inspect", "release", "vs", "default/notebook")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"default/notebook", "deployed", "3", "notebook 0.0.5", "Deployment", "notebook-secrets", "values hidden", "ClusterRole"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	got := p.seen("GET", "/robots/vs/inspect/releases/default/notebook")
	if len(got) != 1 || got[0].Query != "enable=false" {
		t.Errorf("query %q", got[0].Query)
	}
}

// The bot's inspect calls go through the bot API, with its key.
func TestInspectInBotMode(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/fleet/teknoir-demo/robots/scout/inspect/pods/default/whoami-1", 200, inspectPodBody)
	e := botEnv(t, p)
	if _, _, err := runCLI(t, e, "inspect", "pod", "scout", "default/whoami-1"); err != nil {
		t.Fatal(err)
	}
	got := p.seen("GET", "/fleet/teknoir-demo/robots/scout/inspect/pods/default/whoami-1")
	if len(got) != 1 || got[0].Query != "enable=false" {
		t.Errorf("requests %+v", p.reqs)
	}
	p.mu.Lock()
	auth := p.auths[0]
	p.mu.Unlock()
	if auth != "Bearer "+botKey1 {
		t.Errorf("Authorization %q", auth)
	}
}

// ---------- mqtt stream ----------

// sseBotAPI serves the stream route of a bot API with the given writes, once per connection.
func sseBotAPI(t *testing.T, perConn func(i int) string) (*botAPI, *int) {
	t.Helper()
	p := newBotAPI(t)
	var mu sync.Mutex
	conns := 0
	p.onFunc("GET", "/fleet/teknoir-demo/mqtt/stream", func(r rec) (int, string) { return 0, "" })
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/device/bot/v1/fleet/teknoir-demo/mqtt/stream" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+botKey1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept %q", r.Header.Get("Accept"))
		}
		mu.Lock()
		conns++
		i := conns
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, perConn(i))
	})
	return p, &conns
}

func msgEvent(topic, payload string) string {
	b, _ := json.Marshal(api.StreamMessage{Topic: topic, Payload: payload, TS: "2026-11-08T10:00:00Z"})
	return "event: message\ndata: " + string(b) + "\n\n"
}

func TestMqttStreamOutput(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	// The user's fleet: the stream route of the fake platform answers JSON; this test serves its
	// own stream server below.
	p.onFunc("GET", "/mqtt/stream", func(r rec) (int, string) {
		return 0, "" // never used: the handler below answers first
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/device/fleet/teknoir-demo/mqtt/stream" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query()["topic"][0] != "/devices/+/state/#" {
			t.Errorf("topics %v", r.URL.Query()["topic"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": keepalive\n\n")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
		_, _ = io.WriteString(w, "event: message\ndata: {\"topic\":\"/devices/d1/state/y\",\"payloadBase64\":\"AAEC\",\"ts\":\"t\"}\n\n")
		_, _ = io.WriteString(w, "event: dropped\ndata: {\"count\":2,\"reason\":\"rate\"}\n\n")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/z", "bye"))
	}))
	defer srv.Close()
	e, _ := testEnv(t, p, nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Root: srv.URL + "/api/device/fleet",
			Token: func(context.Context) (string, error) { return "tok", nil }}, "teknoir.cloud", "teknoir-demo", nil
	}
	out, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#")
	if err != nil {
		t.Fatal(err)
	}
	want := "/devices/d1/state/x hello\n/devices/d1/state/y <base64:3 bytes>\n/devices/d1/state/z bye\n"
	if out != want {
		t.Errorf("stdout:\n got %q\nwant %q", out, want)
	}
	if !strings.Contains(errs, "dropped 2 message(s): too many messages too fast") {
		t.Errorf("stderr %q", errs)
	}

	// --json: each message's data as one JSON line
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
	}))
	defer srv2.Close()
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv2.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}, "teknoir.cloud", "teknoir-demo", nil
	}
	out, _, err = runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Topic, Payload string }
	line := strings.TrimSuffix(out, "\n")
	if strings.Contains(line, "\n") || json.Unmarshal([]byte(line), &m) != nil || m.Topic != "/devices/d1/state/x" || m.Payload != "hello" {
		t.Errorf("json line %q", out)
	}
}

// A dropped event with the reason slow says the reader did not keep up.
func TestMqttStreamDroppedSlowReason(t *testing.T) {
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keepalive\n\n")
		_, _ = io.WriteString(w, "event: dropped\ndata: {\"count\":3,\"reason\":\"slow\"}\n\n")
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	_, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs, "tnctl: the platform dropped 3 message(s): the reader was too slow") {
		t.Errorf("stderr %q", errs)
	}
}

func TestMqttStreamCountAndSeconds(t *testing.T) {
	noBotMode(t)
	makeEnv := func(reply func(w http.ResponseWriter)) *cliEnv {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			reply(w)
		}))
		t.Cleanup(srv.Close)
		e, _ := testEnv(t, newFakePlatform(t), nil)
		e.fleet = func() (*api.Fleet, string, string, error) {
			return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}, "teknoir.cloud", "teknoir-demo", nil
		}
		return e
	}
	// --count 2 stops after two messages although the stream keeps going
	e := makeEnv(func(w http.ResponseWriter) {
		for i := 0; i < 10; i++ {
			_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "m"))
		}
	})
	out, _, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--count", "2")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if n := strings.Count(out, "\n"); n != 2 {
		t.Errorf("%d lines: %q", n, out)
	}
	// --seconds stops when the stream stays open (the server sleeps)
	e = makeEnv(func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "one"))
		w.(http.Flusher).Flush()
		time.Sleep(2 * time.Second)
	})
	start := time.Now()
	out, _, err = runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--seconds", "1")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "one") {
		t.Errorf("output %q", out)
	}
	if d := time.Since(start); d > 1900*time.Millisecond {
		t.Errorf("--seconds 1 took %s", d)
	}
}

func TestMqttStreamErrorEventExitsNonZero(t *testing.T) {
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: error\ndata: {\"code\":\"rate_limited\",\"message\":\"Too many streams for one bot.\"}\n\n")
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}, "teknoir.cloud", "teknoir-demo", nil
	}
	_, _, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--reconnect")
	if err == nil || !strings.Contains(err.Error(), "Too many streams for one bot.") || exitCodeOf(err) == 0 {
		t.Fatalf("%v", err)
	}
}

func TestMqttStreamReconnects(t *testing.T) {
	noBotMode(t)
	oldDelay, oldMax := mqttReconnectDelay, mqttReconnectMax
	mqttReconnectDelay, mqttReconnectMax = time.Millisecond, time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay, mqttReconnectMax = oldDelay, oldMax })

	p, conns := sseBotAPI(t, func(i int) string {
		if i <= 2 {
			return msgEvent("/devices/d1/state/x", "one")
		}
		return msgEvent("/devices/d1/state/x", "two")
	})
	e := botEnv(t, p)
	out, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--count", "3", "--reconnect")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if want := "/devices/d1/state/x one\n/devices/d1/state/x one\n/devices/d1/state/x two\n"; out != want {
		t.Errorf("output\n got %q\nwant %q", out, want)
	}
	if !strings.Contains(errs, "connecting again") {
		t.Errorf("stderr %q", errs)
	}
	if *conns < 2 {
		t.Errorf("%d connections, want at least 2", *conns)
	}
}

func TestMqttStreamDoesNotReconnectAfterARefusedRequest(t *testing.T) {
	noBotMode(t)
	oldDelay := mqttReconnectDelay
	mqttReconnectDelay = time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay = oldDelay })
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"no_write","message":"You may not follow MQTT here."}`)
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}, "teknoir.cloud", "teknoir-demo", nil
	}
	_, _, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--reconnect")
	if err == nil || !strings.Contains(err.Error(), "You may not follow MQTT here.") {
		t.Fatalf("%v", err)
	}
	if hits != 1 {
		t.Errorf("%d connections, want 1", hits)
	}
}

// A 503 the platform marks as temporary (a portal restart) is waited out for its Retry-After.
func TestMqttStreamReconnectWaitsOutA503(t *testing.T) {
	noBotMode(t)
	oldUnit := mqttRetryAfterUnit
	mqttRetryAfterUnit = time.Millisecond
	t.Cleanup(func() { mqttRetryAfterUnit = oldUnit })

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"name":"ServiceUnavailableError","message":"The portal is restarting. Connect again in a few seconds."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	out, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--count", "1", "--reconnect")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if want := "/devices/d1/state/x hello\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if want := "The portal is restarting. Connect again in a few seconds.; connecting again in 5ms"; !strings.Contains(errs, want) {
		t.Errorf("stderr %q, want it to contain %q", errs, want)
	}
	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 2 {
		t.Errorf("%d requests, want 2", got)
	}
}

// mqttStreamEnvFor is a CLI environment whose fleet asks srv for the stream.
func mqttStreamEnvFor(t *testing.T, srv *httptest.Server) *cliEnv {
	t.Helper()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	return e
}

// write503 writes the platform's temporary 503; retryAfter "" means none.
func write503(w http.ResponseWriter, retryAfter, message string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, `{"error":{"name":"ServiceUnavailableError","message":"`+message+`"}}`)
}

// final503 runs mqtt stream against a server whose first answer is a 503 with the given Retry-After
// ("" for none) and message. Every later request gets 500 "unexpected second request", and the
// backoff and the Retry-After unit are 1 ms: a build that wrongly retries fails at once with
// "N requests, want 1" instead of looping until go test's -timeout.
func final503(t *testing.T, retryAfter, message string, reconnect bool) (errs string, requests int, err error) {
	t.Helper()
	noBotMode(t)
	oldUnit, oldDelay := mqttRetryAfterUnit, mqttReconnectDelay
	mqttRetryAfterUnit, mqttReconnectDelay = time.Millisecond, time.Millisecond
	t.Cleanup(func() { mqttRetryAfterUnit, mqttReconnectDelay = oldUnit, oldDelay })

	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if !first {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"name":"Error","message":"unexpected second request"}}`)
			return
		}
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"name":"ServiceUnavailableError","message":"`+message+`"}}`)
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	args := []string{"mqtt", "stream", "/devices/+/state/#"}
	if reconnect {
		args = append(args, "--reconnect")
	}
	_, errs, err = runCLI(t, e, args...)
	mu.Lock()
	defer mu.Unlock()
	return errs, n, err
}

// A 503 that says live MQTT is not configured is final: --reconnect does not try again.
func TestMqttStreamReconnectStopsOnNotConfigured(t *testing.T) {
	const msg = "The stream is off: live MQTT is not configured for teamspace teknoir-demo."
	errs, requests, err := final503(t, "300", msg, true)
	if requests != 1 {
		t.Errorf("%d requests, want 1", requests)
	}
	if err == nil || exitCodeOf(err) == 0 || !strings.Contains(err.Error(), msg) {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, errs)
	}
}

// Without --reconnect a temporary 503 ends the command with the server's message.
func TestMqttStreamNoReconnectKeepsA503Final(t *testing.T) {
	const msg = "The portal is restarting. Connect again in a few seconds."
	errs, requests, err := final503(t, "5", msg, false)
	if requests != 1 {
		t.Errorf("%d requests, want 1", requests)
	}
	if err == nil || exitCodeOf(err) == 0 || !strings.Contains(err.Error(), msg) {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, errs)
	}
	if strings.Contains(errs, "connecting again") {
		t.Errorf("stderr %q: it reconnected without --reconnect", errs)
	}
}

// The pause is the Retry-After, really slept: not skipped, not the backoff.
func TestMqttStreamReconnectSleepsTheRetryAfter(t *testing.T) {
	noBotMode(t)
	oldUnit, oldDelay := mqttRetryAfterUnit, mqttReconnectDelay
	mqttRetryAfterUnit, mqttReconnectDelay = 20*time.Millisecond, time.Hour
	t.Cleanup(func() { mqttRetryAfterUnit, mqttReconnectDelay = oldUnit, oldDelay })

	var mu sync.Mutex
	var at []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at = append(at, time.Now())
		n := len(at)
		mu.Unlock()
		if n == 1 {
			write503(w, "5", "The portal is restarting. Connect again in a few seconds.")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
	}))
	defer srv.Close()
	out, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--count", "1", "--seconds", "5", "--reconnect")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != "/devices/d1/state/x hello\n" {
		t.Errorf("stdout %q, stderr %q", out, errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) != 2 {
		t.Fatalf("%d requests, want 2", len(at))
	}
	if gap := at[1].Sub(at[0]); gap < 100*time.Millisecond {
		t.Errorf("reconnected after %s, want at least the Retry-After of 100ms", gap)
	}
}

// A Retry-After far in the future (an HTTP date, which saturates at 292 years) waits the cap:
// the pause must not overflow, and the message must not be lost to the backoff.
func TestMqttStreamReconnectCapsAHugeRetryAfter(t *testing.T) {
	noBotMode(t)
	oldMax, oldDelay := mqttRetryAfterMax, mqttReconnectDelay
	mqttRetryAfterMax, mqttReconnectDelay = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { mqttRetryAfterMax, mqttReconnectDelay = oldMax, oldDelay })

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			write503(w, "Fri, 31 Dec 9999 23:59:59 GMT", "The portal is restarting. Connect again in a few seconds.")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
	}))
	defer srv.Close()
	_, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--count", "1", "--reconnect", "--seconds", "5")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if want := "The portal is restarting. Connect again in a few seconds.; connecting again in 20ms"; !strings.Contains(errs, want) {
		t.Errorf("stderr %q, want it to contain %q", errs, want)
	}
	if strings.Contains(errs, "the stream ended") {
		t.Errorf("stderr %q: the 503 was reported as a clean end", errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("%d requests, want 2", requests)
	}
}

// A temporary 503 whose Retry-After exceeds mqttRetryAfterMax is waited out for mqttRetryAfterMax:
// the text shows the capped wait and the pause is the capped one.
func TestMqttStreamReconnectCapsRetryAfter(t *testing.T) {
	noBotMode(t)
	oldUnit, oldMax := mqttRetryAfterUnit, mqttRetryAfterMax
	mqttRetryAfterUnit, mqttRetryAfterMax = time.Hour, 10*time.Millisecond
	t.Cleanup(func() { mqttRetryAfterUnit, mqttRetryAfterMax = oldUnit, oldMax })

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			write503(w, "10", "The MQTT broker of teamspace teknoir-demo cannot be reached right now.")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
	}))
	defer srv.Close()
	out, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--count", "1", "--seconds", "5", "--reconnect")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if want := "cannot be reached right now.; connecting again in 10ms\n"; !strings.Contains(errs, want) {
		t.Errorf("stderr %q, want it to contain %q", errs, want)
	}
	if want := "/devices/d1/state/x hello\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("%d requests, want 2", requests)
	}
}

// A Retry-After wait neither resets nor doubles the backoff of the clean-end/network path.
func TestMqttStreamRetryAfterLeavesTheBackoffAlone(t *testing.T) {
	noBotMode(t)
	oldDelay, oldMax, oldUnit := mqttReconnectDelay, mqttReconnectMax, mqttRetryAfterUnit
	mqttReconnectDelay, mqttReconnectMax, mqttRetryAfterUnit = 10*time.Millisecond, time.Second, time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay, mqttReconnectMax, mqttRetryAfterUnit = oldDelay, oldMax, oldUnit })

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		switch n {
		case 2:
			write503(w, "5", "The portal is restarting. Connect again in a few seconds.")
			return
		case 4:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream") // 1 and 3: a clean end with nothing in it
	}))
	defer srv.Close()
	_, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--count", "1", "--seconds", "10", "--reconnect")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "tnctl: the stream ended; connecting again in 10ms\n" +
		"tnctl: The portal is restarting. Connect again in a few seconds.; connecting again in 5ms\n" +
		"tnctl: the stream ended; connecting again in 20ms\n"
	if errs != want {
		t.Errorf("stderr\n%q\nwant\n%q", errs, want)
	}
}

// A 429 (too many streams) is final with --reconnect too, with the rate-limit exit code.
func TestMqttStreamReconnectDoesNotRetryA429(t *testing.T) {
	noBotMode(t)
	oldUnit := mqttRetryAfterUnit
	mqttRetryAfterUnit = time.Millisecond
	t.Cleanup(func() { mqttRetryAfterUnit = oldUnit })

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n > 1 {
			// A retry would get a stream, so a wrong retry ends the command with 0.
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
			return
		}
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"name":"TooManyRequestsError","message":"You already have 4 live MQTT streams open. Close one before you open another."}}`)
	}))
	defer srv.Close()
	_, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--count", "1", "--reconnect")
	if err == nil || exitCodeOf(err) != codeRateLimit ||
		!strings.Contains(err.Error(), "You already have 4 live MQTT streams open. Close one before you open another.") {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, errs)
	}
	if strings.Contains(errs, "connecting again") {
		t.Errorf("stderr %q: it retried a 429", errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Errorf("%d requests, want 1", requests)
	}
}

// --seconds that runs out during a Retry-After wait is a clean stop (exit 0); the wait is capped at 60 s.
func TestMqttStreamSecondsEndsARetryAfterWaitCleanly(t *testing.T) {
	noBotMode(t)
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n > 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
			return
		}
		write503(w, "300", "The MQTT broker of teamspace teknoir-demo cannot be reached right now.")
	}))
	defer srv.Close()
	_, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--seconds", "1", "--reconnect")
	if err != nil {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, errs)
	}
	if !strings.Contains(errs, "cannot be reached right now.; connecting again in 1m0s\n") {
		t.Errorf("stderr %q", errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Errorf("%d requests, want 1", requests)
	}
}

// The gateway's own 502, 503 or 504 while the portal's pod is being replaced is the one refusal
// --reconnect retries: it says nothing about the request.
func TestMqttStreamReconnectRetriesAGatewayAnswer(t *testing.T) {
	noBotMode(t)
	oldDelay := mqttReconnectDelay
	mqttReconnectDelay = time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay = oldDelay })

	cases := []struct {
		name   string
		status int
	}{
		{"502", http.StatusBadGateway},
		{"503", http.StatusServiceUnavailable},
		{"504", http.StatusGatewayTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				n := requests
				mu.Unlock()
				if n == 1 {
					w.Header().Set("Content-Type", "text/plain")
					w.WriteHeader(c.status)
					_, _ = io.WriteString(w, "no healthy upstream")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "hello"))
			}))
			defer srv.Close()
			out, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--count", "1", "--reconnect", "--seconds", "5")
			if err != nil {
				t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, errs)
			}
			if want := "/devices/d1/state/x hello\n"; out != want {
				t.Errorf("stdout %q, want %q", out, want)
			}
			if want := "tnctl: no healthy upstream; connecting again in 1ms"; !strings.Contains(errs, want) {
				t.Errorf("stderr %q, want it to contain %q", errs, want)
			}
			mu.Lock()
			defer mu.Unlock()
			if requests != 2 {
				t.Errorf("%d requests, want 2", requests)
			}
		})
	}
}

// A portal restart: the stream ends cleanly, the gateway answers 503 with no Retry-After until the
// new pod is ready (backstage-api is one replica with strategy Recreate, so that lasts a minute or
// more), and the stream comes back. --reconnect rides it out.
func TestMqttStreamReconnectSurvivesAPortalRestart(t *testing.T) {
	noBotMode(t)
	oldDelay := mqttReconnectDelay
	mqttReconnectDelay = time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay = oldDelay })

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		switch n {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "before"))
		case 2:
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "no healthy upstream")
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "after"))
		}
	}))
	defer srv.Close()
	out, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--count", "2", "--reconnect", "--seconds", "5")
	if err != nil {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, errs)
	}
	if want := "/devices/d1/state/x before\n/devices/d1/state/x after\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 3 {
		t.Errorf("%d requests, want 3", requests)
	}
}

// Without --reconnect the gateway's 503 ends the command with its message, after one request.
func TestMqttStreamNoReconnectKeepsAGateway503Final(t *testing.T) {
	noBotMode(t)
	oldDelay := mqttReconnectDelay
	mqttReconnectDelay = time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay = oldDelay })

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "no healthy upstream")
	}))
	defer srv.Close()
	_, errs, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#")
	if err == nil || exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "no healthy upstream") {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, errs)
	}
	if requests != 1 {
		t.Errorf("%d requests, want 1", requests)
	}
}

// The exit code of each refusal the stream route answers, with --reconnect: the docs' table.
func TestMqttStreamRefusalExitCodes(t *testing.T) {
	noBotMode(t)
	cases := []struct {
		name, retryAfter, body string
		status, want           int
	}{
		{"too many", "30", `{"error":{"name":"TooManyRequestsError","message":"You already have 4 live MQTT streams open. Close one before you open another."}}`, 429, codeRateLimit},
		{"not found", "", `{"error":{"name":"NotFoundError","message":"device x not found"}}`, 404, codeNotFound},
		{"bad topics", "", `{"error":{"name":"InputError","message":"Give 1 to 8 topic parameters."}}`, 400, codeRejected},
		{"not configured", "300", `{"error":{"name":"ServiceUnavailableError","message":"The stream is off: live MQTT is not configured on this portal."}}`, 503, codeError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if c.retryAfter != "" {
					w.Header().Set("Retry-After", c.retryAfter)
				}
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			defer srv.Close()
			// --seconds bounds the run should a regression retry a refusal.
			_, _, err := runCLI(t, mqttStreamEnvFor(t, srv), "mqtt", "stream", "/devices/+/state/#", "--reconnect", "--seconds", "2")
			if got := exitCodeOf(err); got != c.want {
				t.Errorf("exit %d, want %d (%v)", got, c.want, err)
			}
			if hits != 1 {
				t.Errorf("%d requests, want 1", hits)
			}
		})
	}
}

// A missing bot key ends the stream at once, with or without --reconnect: no request, no loop.
func TestMqttStreamMissingKeyFailsAtOnce(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		p := newBotAPI(t)
		e := botEnv(t, p)
		if err := os.Remove(p.keyFile); err != nil {
			t.Fatal(err)
		}
		args := []string{"mqtt", "stream", "/devices/+/state/#"}
		if reconnect {
			args = append(args, "--reconnect")
		}
		_, errs, err := runCLI(t, e, args...)
		if err == nil || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "the bot key is missing or empty") {
			t.Errorf("--reconnect=%v: exit %d, %v", reconnect, exitCodeOf(err), err)
		}
		if strings.Contains(errs, "connecting again") {
			t.Errorf("--reconnect=%v: it reconnected: %q", reconnect, errs)
		}
		if n := p.count(); n != 0 {
			t.Errorf("--reconnect=%v: %d requests were sent", reconnect, n)
		}
	}
}

// A network failure without --reconnect is an error (exit 1), not a clean end.
func TestMqttStreamConnectionRefusedWithoutReconnect(t *testing.T) {
	noBotMode(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: "http://" + addr + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	_, _, err = runCLI(t, e, "mqtt", "stream", "/devices/+/state/#")
	if err == nil || exitCodeOf(err) != codeError {
		t.Fatalf("exit %d, %v", exitCodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error %v", err)
	}
}

// A stream the server ends after a 200 is a clean end: exit 0 without --reconnect.
func TestMqttStreamCleanEndExitsZero(t *testing.T) {
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "only"))
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	out, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if want := "/devices/d1/state/x only\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if strings.Contains(errs, "connecting again") {
		t.Errorf("stderr %q", errs)
	}
}

// The reconnect delay starts over after a connection that delivered messages.
func TestMqttStreamReconnectDelayResetsAfterADelivery(t *testing.T) {
	noBotMode(t)
	oldDelay, oldMax := mqttReconnectDelay, mqttReconnectMax
	mqttReconnectDelay, mqttReconnectMax = 10*time.Millisecond, time.Second
	t.Cleanup(func() { mqttReconnectDelay, mqttReconnectMax = oldDelay, oldMax })

	// Every connection delivers one message and ends: two reconnects, --count 3.
	p, conns := sseBotAPI(t, func(i int) string { return msgEvent("/devices/d1/state/x", "m") })
	e := botEnv(t, p)
	if _, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--count", "3", "--reconnect"); err != nil {
		t.Fatalf("%v\n%s", err, errs)
	} else if n := strings.Count(errs, "connecting again in 10ms"); n != 2 {
		t.Errorf("%d waits of 10ms in %q (the delay must not double after a delivery)", n, errs)
	}
	if *conns < 3 {
		t.Errorf("%d connections, want 3", *conns)
	}
}

// --json prints the server's data as it came, compacted: a field tnctl does not know survives.
func TestMqttStreamJSONKeepsUnknownFields(t *testing.T) {
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"topic\": \"/devices/d1/state/x\", \"payload\": \"hi\", \"extra\": {\"n\": 1}}\n\n")
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	out, _, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--json")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"topic":"/devices/d1/state/x","payload":"hi","extra":{"n":1}}` + "\n"
	if out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

// In the human format one message is one line: a text payload keeps its newlines escaped.
func TestMqttStreamEscapesPayloadNewlines(t *testing.T) {
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "one\ntwo\r\nthree"))
	}))
	defer srv.Close()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	out, _, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/devices/d1/state/x one\\ntwo\\r\\nthree\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

func TestMqttStreamTopicValidation(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	valid := [][]string{
		{"/devices/+/state/#"},
		{"/devices/d1/events/x/y"},
		{"/devices/+/state/#", "/devices/d1/events/x/y"},
	}
	// They reach the platform (which answers 404: the test asserts the request, not the stream).
	for _, topics := range valid {
		args := append([]string{"mqtt", "stream", "--count", "1"}, topics...)
		if _, _, err := runCLI(t, e, args...); err == nil {
			// a 404 is fine: what matters is that no usage error came back
			t.Logf("%v: no error", topics)
		} else if exitCodeOf(err) == codeUsage {
			t.Errorf("%v: usage error %v", topics, err)
		}
	}
	if n := len(p.seen("GET", "/mqtt/stream")); n != len(valid) {
		t.Errorf("%d stream requests, want %d", n, len(valid))
	}
	invalid := [][]string{
		{"#"},
		{"/devices/#"},
		{"$share/a//devices/+/state/#"},
		{"/devices/d1/config"},
		{"/devices/+/commands/#"},
	}
	for _, topics := range invalid {
		args := append([]string{"mqtt", "stream"}, topics...)
		if _, _, err := runCLI(t, e, args...); err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "not a topic a stream can follow") {
			t.Errorf("%v: exit %d %v", topics, exitCodeOf(err), err)
		}
	}
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = "/devices/+/state/#"
	}
	if _, _, err := runCLI(t, e, append([]string{"mqtt", "stream"}, nine...)...); err == nil || exitCodeOf(err) != codeUsage {
		t.Errorf("9 topics: exit %d %v", exitCodeOf(err), err)
	}
	if n := len(p.seen("GET", "/mqtt/stream")); n != len(valid) {
		t.Errorf("%d stream requests, want %d", n, len(valid))
	}
}

// ---------- a token error under --reconnect ----------

// A key that disappears between two connections ends the stream at once, --reconnect or not: it is
// not a network failure, and no amount of waiting brings the key back.
func TestMqttStreamReconnectStopsOnAKeyThatWentAway(t *testing.T) {
	oldDelay := mqttReconnectDelay
	mqttReconnectDelay = time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay = oldDelay })

	var p *botAPI
	perConn := func(i int) string {
		if i == 1 {
			// The key is read again for the next connection: it must fail then, not be retried.
			if err := os.Remove(p.keyFile); err != nil {
				t.Error(err)
			}
			return msgEvent("/devices/d1/state/x", "one")
		}
		return msgEvent("/devices/d1/state/x", "again")
	}
	var conns *int
	p, conns = sseBotAPI(t, perConn)
	e := botEnv(t, p)
	start := time.Now()
	_, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--reconnect", "--seconds", "2")
	elapsed := time.Since(start)

	if err == nil || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "the bot key is missing or empty") {
		t.Errorf("exit %d, %v", exitCodeOf(err), err)
	}
	if n := *conns; n != 1 {
		t.Errorf("%d connections, want 1: %s", n, errs)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("took %s: the key error was retried", elapsed)
	}
}

// A person's login that ends between two connections is the same: --reconnect does not wait for a
// session that will never come back.
func TestMqttStreamReconnectStopsOnAnEndedLogin(t *testing.T) {
	noBotMode(t)
	oldDelay := mqttReconnectDelay
	mqttReconnectDelay = time.Millisecond
	t.Cleanup(func() { mqttReconnectDelay = oldDelay })

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "one"))
	}))
	defer srv.Close()
	// The login is good for the first connection and over for the next, as a session that ends.
	calls := 0
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) {
			calls++
			// One connection reads the token twice: before the loop and for the request.
			if calls <= 2 {
				return "tok", nil
			}
			return "", auth.ErrSessionExpired
		}}, "teknoir.cloud", "teknoir-demo", nil
	}
	start := time.Now()
	_, errs, err := runCLI(t, e, "mqtt", "stream", "/devices/+/state/#", "--reconnect", "--seconds", "2")
	elapsed := time.Since(start)

	if err == nil || exitCodeOf(err) != codeAuth {
		t.Fatalf("exit %d, %v", exitCodeOf(err), err)
	}
	// One wait, for the clean end of the first stream: the attempt after it ends on the token error.
	if n := strings.Count(errs, "connecting again"); n != 1 {
		t.Errorf("%d waits before connecting again: %q", n, errs)
	}
	if hits != 1 {
		t.Errorf("%d requests, want 1", hits)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("took %s: the login error was retried", elapsed)
	}
}

// ---------- inspect logs: what the server refuses ----------

func TestInspectLogsRejectsWhatTheServerRefuses(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	for _, args := range [][]string{
		{"inspect", "logs", "vs", "default/whoami-1", "--tail", "-1"},
		{"inspect", "logs", "vs", "default/whoami-1", "--tail", "2001"},
		{"inspect", "logs", "vs", "default/whoami-1", "--since", "500ms"},
		{"inspect", "logs", "vs", "default/whoami-1", "--since", "0s"},
		{"inspect", "logs", "vs", "default/whoami-1", "--since", "169h"},
	} {
		if _, _, err := runCLI(t, e, args...); err == nil || exitCodeOf(err) != codeUsage {
			t.Errorf("%v: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
	// The largest values the server takes are sent as they are.
	p.on("GET", "/robots/vs/inspect/pods/default/whoami-1/logs", 200, `{"text":"a line\n"}`)
	if _, _, err := runCLI(t, e, "inspect", "logs", "vs", "default/whoami-1", "--tail", "2000", "--since", "168h"); err != nil {
		t.Fatal(err)
	}
	got := p.seen("GET", "/robots/vs/inspect/pods/default/whoami-1/logs")
	if len(got) != 1 {
		t.Fatalf("requests %+v", p.reqs)
	}
	for _, want := range []string{"tail=2000", "since=168h", "enable=false"} {
		if !strings.Contains(got[0].Query, want) {
			t.Errorf("query %q lacks %q", got[0].Query, want)
		}
	}
}

// ---------- errors in bot mode ----------

// A bot's refused key is reported with the platform's own words, and without advice a bot cannot
// take: only a person can re-create it.
func TestBotRefusedKeySaysWhatThePlatformSaid(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/whoami", 401, `{"error":{"name":"AuthenticationError","message":"The bot key was not accepted."}}`)
	e := botEnv(t, p)
	_, _, err := runCLI(t, e, "whoami")
	if err == nil || exitCodeOf(err) != codeAuth {
		t.Fatalf("exit %d, %v", exitCodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "The bot key was not accepted.") {
		t.Errorf("the platform's words are lost: %v", err)
	}
	if strings.Contains(err.Error(), "tnctl login") {
		t.Errorf("the error tells a bot to log in: %v", err)
	}
}

// A 503 carries its Retry-After like a 429 does.
func TestServiceUnavailableCarriesRetryAfterInJSON(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/whoami", 503, `{"error":{"name":"ServiceUnavailable","message":"try again later"}}`)
	inner := p.srv.Config.Handler
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		inner.ServeHTTP(w, r)
	})
	e := botEnv(t, p)
	_, _, err := runCLI(t, e, "whoami")
	if err == nil {
		t.Fatal("want an error")
	}
	var buf bytes.Buffer
	writeError(&buf, err, "json")
	var body struct {
		Error struct {
			Code       string
			Message    string
			RetryAfter int `json:"retryAfter"`
		}
	}
	if jerr := json.Unmarshal(buf.Bytes(), &body); jerr != nil {
		t.Fatalf("%v: %q", jerr, buf.String())
	}
	if body.Error.RetryAfter != 5 {
		t.Errorf("retryAfter %d: %q", body.Error.RetryAfter, buf.String())
	}
}

// ---------- whoami without a creator ----------

func TestWhoamiBotWithoutCreatorEmail(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/whoami", 200, `{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":null,"access":"none"},"creator":{"email":null},"domain":"teknoir.cloud"}`)
	e := botEnv(t, p)
	out, _, err := runCLI(t, e, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bot teknoir-demo/scout for an unknown person, access none, unit —\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}
