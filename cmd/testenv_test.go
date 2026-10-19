package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

// cliStdin is the stdin of the next runCLI, when a test sets it.
var cliStdin io.Reader

// rec is one request the fake platform saw.
type rec struct {
	Method, Path, Query, Body string
}

// fakePlatform is a fleet API for tests. Routes are matched on "METHOD /path-after-the-base".
type fakePlatform struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]func(r rec) (int, string)
	reqs   []rec
	srv    *httptest.Server
}

func newFakePlatform(t *testing.T) *fakePlatform {
	p := &fakePlatform{t: t, routes: map[string]func(rec) (int, string){}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/device/fleet/teknoir-demo")
		rc := rec{r.Method, path, r.URL.RawQuery, string(b)}
		p.mu.Lock()
		p.reqs = append(p.reqs, rc)
		h := p.routes[r.Method+" "+path]
		p.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Redirect(w, r, "https://teknoir.cloud/oauth2/start?rd=x", http.StatusFound)
			return
		}
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"name":"NotFoundError","message":"no route ` + r.Method + " " + path + `"}}`))
			return
		}
		status, body := h(rc)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePlatform) on(method, path string, status int, body string) {
	p.onFunc(method, path, func(rec) (int, string) { return status, body })
}

func (p *fakePlatform) onFunc(method, path string, f func(rec) (int, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[method+" "+path] = f
}

// seen returns the requests with this method and path, in order.
func (p *fakePlatform) seen(method, path string) []rec {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []rec
	for _, r := range p.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// fakeClock is the time of a test: sleeping advances it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

// fakeExec is a device shell for tests. run gets each script and answers; calls are recorded.
type fakeExec struct {
	mu     sync.Mutex
	run    func(script string) (execResult, error)
	calls  []string
	closed bool
	stream func(line string) int
}

func (f *fakeExec) Run(_ context.Context, cmdline string) (execResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmdline)
	f.mu.Unlock()
	if f.run == nil {
		return execResult{}, nil
	}
	return f.run(cmdline)
}

func (f *fakeExec) Stream(_ context.Context, cmdline string, _ io.Reader, stdout, stderr io.Writer) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmdline)
	f.mu.Unlock()
	if f.run != nil {
		r, err := f.run(cmdline)
		if err != nil {
			return 0, err
		}
		_, _ = io.WriteString(stdout, r.Stdout)
		_, _ = io.WriteString(stderr, r.Stderr)
	}
	if f.stream != nil {
		return f.stream(cmdline), nil
	}
	return 0, nil
}

func (f *fakeExec) Close() { f.closed = true }

func (f *fakeExec) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// testEnv wires a cliEnv to the fake platform, a fake clock and a fake device shell.
func testEnv(t *testing.T, p *fakePlatform, ex *fakeExec) (*cliEnv, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	e := &cliEnv{
		fleet: func() (*api.Fleet, string, string, error) {
			return &api.Fleet{
				Base:  p.srv.URL + "/api/device/fleet/teknoir-demo",
				Token: func(context.Context) (string, error) { return "tok", nil },
			}, "teknoir.cloud", "teknoir-demo", nil
		},
		sleep:        clk.sleep,
		now:          clk.now,
		pollInterval: 2 * time.Second,
	}
	if ex != nil {
		e.newExec = func(ctx context.Context, domain, ns, dev string) (deviceExec, error) {
			ex.mu.Lock()
			ex.calls = append(ex.calls, "OPEN "+dev)
			ex.mu.Unlock()
			return ex, nil
		}
	}
	return e, clk
}

// runCLI runs a command line against env and returns stdout, stderr and the error.
func runCLI(t *testing.T, e *cliEnv, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "tnctl", SilenceErrors: true}
	root.PersistentFlags().StringVarP(&outputFlag, "output", "o", "", "")
	root.PersistentFlags().StringVar(&device, "device", "", "")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageErr("%v", err) })
	root.AddCommand(newConfigCmd(e), newRobotsCmd(e), newRobotCmd(e), newExecCmd(e), newLogsCmd(e))
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	if cliStdin != nil {
		root.SetIn(cliStdin)
	}
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---------- fixtures ----------

func helmResource(name, layer, source, chartVer string, merged map[string]any) api.FleetConfigResource {
	r := api.FleetConfigResource{
		Identity: "HelmChart-" + name + "-default", Kind: "HelmChart", Name: name, Namespace: "default",
		Layer: layer, Source: source, Status: "winner",
		Chart: &api.ChartInfo{Chart: name, Version: chartVer},
	}
	if merged != nil {
		r.Values = &api.FleetValues{Merged: merged}
	}
	return r
}

func deployResource(name, container, image string) api.FleetConfigResource {
	return api.FleetConfigResource{
		Identity: "Deployment-" + name + "-default", Kind: "Deployment", Name: name, Namespace: "default",
		Layer: "robot", Source: "vision-station-01", Status: "winner",
		Images: []api.ContainerImage{{Container: container, Image: image}},
	}
}

// standardView is vision-station-01 with a notebook HelmChart and a whoami Deployment.
func standardView() *api.ConfigTargetView {
	app := &api.GitOpsApplicationView{GitOpsApplicationRef: api.GitOpsApplicationRef{
		RepoURL: "https://github.com/teknoir/teknoir-demo-gitops", Path: "apps/mock-vision", Revision: "main"}}
	nb := helmResource("notebook", "fleet", "test", "0.0.5", map[string]any{"image": map[string]any{"tag": "main"}, "replicas": 1})
	nb.Application = app
	return &api.ConfigTargetView{
		Target: api.ConfigTargetRef{Kind: "robot", Name: "vision-station-01", Device: "vision-station-01", Robot: "vision-station-01"},
		Configuration: api.RobotConfigurationView{
			Resources: []api.FleetConfigResource{nb, deployResource("whoami", "whoami", "traefik/whoami:v1.10.4")},
			Effective: []api.FleetConfigResource{nb, deployResource("whoami", "whoami", "traefik/whoami:v1.10.4")},
		},
	}
}
