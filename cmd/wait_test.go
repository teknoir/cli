package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

// viewAt returns a target view whose controller is at the given revision with the given state of the notebook override.
func viewAt(rev, state, msg string) *api.ConfigTargetView {
	v := standardView()
	v.Overrides = api.DeviceOverrides{Revision: "r2", Resources: map[string]api.ResourceOverride{nbID: {ValuesContent: "image:\n  tag: pr-123\n"}}}
	v.OverridesRevision = rev
	if state != "" {
		v.OverrideStatus = []api.OverrideStatus{{Target: nbID, State: state, Message: msg}}
	}
	return v
}

// sequence serves the views in order, then the last one forever.
func sequence(t *testing.T, p *fakePlatform, views ...*api.ConfigTargetView) *int {
	var mu sync.Mutex
	n := 0
	p.onFunc("GET", "/config/vision-station-01", func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		i := n
		if i >= len(views) {
			i = len(views) - 1
		}
		n++
		return 200, jsonOf(t, views[i])
	})
	return &n
}

func setWait(t *testing.T, e *cliEnv, extra ...string) (string, string, error) {
	args := append([]string{"config", "set", "vision-station-01", nbID, "image.tag=pr-123"}, extra...)
	return runCLI(t, e, args...)
}

func TestWaitPublished(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	polls := sequence(t, p, viewAt("r1", "active", ""), viewAt("r1", "active", ""), viewAt("r2", "pending", ""), viewAt("r2", "active", ""))
	e, clk := testEnv(t, p, nil)
	t0 := clk.now()

	out, errs, err := setWait(t, e, "--wait=published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if *polls != 4 {
		t.Errorf("polls: %d, want 4", *polls)
	}
	if !strings.Contains(errs, "waiting for the controller to evaluate revision r2 (it has r1)") || !strings.Contains(errs, "published after") {
		t.Errorf("stderr:\n%s", errs)
	}
	if strings.Contains(out, "published after") {
		t.Errorf("progress must not be on stdout: %s", out)
	}
	if d := clk.now().Sub(t0); d != 3*e.pollInterval {
		t.Errorf("waited %s", d)
	}
}

func TestWaitPublishedJSONHasWaitObject(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	sequence(t, p, viewAt("r2", "redundant", "same as git"))
	e, _ := testEnv(t, p, nil)
	out, _, err := setWait(t, e, "--wait=published", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Revision string
		Wait     struct {
			Level   string
			Reached bool
		}
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Revision != "r2" || v.Wait.Level != "published" || !v.Wait.Reached {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestWaitPublishedRejectedStates(t *testing.T) {
	for _, st := range []string{"invalid", "no-target", "not-applicable"} {
		p := newFakePlatform(t)
		p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
		sequence(t, p, viewAt("r2", st, "container nope not found"))
		e, _ := testEnv(t, p, nil)
		_, _, err := setWait(t, e, "--wait=published")
		if exitCodeOf(err) != codeRejected || !strings.Contains(err.Error(), st) || !strings.Contains(err.Error(), "container nope not found") {
			t.Errorf("%s: exit %d: %v", st, exitCodeOf(err), err)
		}
	}
	// the write itself is not undone: the override is there
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	sequence(t, p, viewAt("r2", "invalid", "bad"))
	e, _ := testEnv(t, p, nil)
	out, _, err := setWait(t, e, "--wait=published", "-o", "json")
	if err == nil || !strings.Contains(out, `"reached": false`) || !strings.Contains(out, `"error"`) {
		t.Errorf("json result must still be printed with reached false: %v\n%s", err, out)
	}
}

func TestWaitPublishedTimeout(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	sequence(t, p, viewAt("r1", "active", ""))
	e, clk := testEnv(t, p, nil)
	t0 := clk.now()
	_, _, err := setWait(t, e, "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "timed out after 10s") || !strings.Contains(err.Error(), "revision r2") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if d := clk.now().Sub(t0); d < 10*1e9 || d > 14*1e9 {
		t.Errorf("waited %s", d)
	}
}

func TestWaitPublishedLocalItemIgnoresState(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, `{"id":"`+nbID+`","storage":"local-item","revision":"r2","dryRun":false,"overrides":{},"warnings":[]}`)
	sequence(t, p, viewAt("r2", "not-applicable", "local item"))
	e, _ := testEnv(t, p, nil)
	if _, _, err := setWait(t, e, "--wait=published"); err != nil {
		t.Errorf("an in-place override of a local item has no state to wait for: %v", err)
	}
}

func TestWaitToleratesFlakyNetworkButNotRejection(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	var mu sync.Mutex
	n := 0
	p.onFunc("GET", "/config/vision-station-01", func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n <= 2 {
			return 502, `bad gateway`
		}
		return 200, jsonOf(t, viewAt("r2", "active", ""))
	})
	e, _ := testEnv(t, p, nil)
	if _, errs, err := setWait(t, e, "--wait=published"); err != nil || !strings.Contains(errs, "retrying") {
		t.Errorf("%v\n%s", err, errs)
	}
	p.on("GET", "/config/vision-station-01", 404, `{"error":{"name":"NotFoundError","message":"gone"}}`)
	if _, _, err := setWait(t, e, "--wait=published"); exitCodeOf(err) != codeNotFound {
		t.Errorf("a 404 while waiting is final: %d %v", exitCodeOf(err), err)
	}
}

func TestWaitCommandPublished(t *testing.T) {
	p := newFakePlatform(t)
	sequence(t, p, viewAt("r1", "active", ""), viewAt("r2", "active", ""))
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "config", "wait", "vision-station-01", "--for", "published")
	if err != nil || out != "vision-station-01: published\n" {
		t.Errorf("%v %q\n%s", err, out, errs)
	}
	out, _, err = runCLI(t, e, "config", "wait", "vision-station-01", "--for", "published", "-o", "json")
	if err != nil || !strings.Contains(out, `"reached": true`) || !strings.Contains(out, `"revision": "r2"`) {
		t.Errorf("%v\n%s", err, out)
	}
	if _, _, err := runCLI(t, e, "config", "wait", "vision-station-01", "--for", "soon"); exitCodeOf(err) != codeUsage {
		t.Errorf("bad level: %v", err)
	}
}

// ---------- applied and ready on the device ----------

// helmDevice scripts the robot's answers to the probes. step is called for each probe of the HelmChart.
type helmDevice struct {
	mu      sync.Mutex
	probes  int
	step    func(n int) map[string]string
	logText string
}

func (d *helmDevice) run(script string) (execResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case strings.Contains(script, "logs job/"):
		return execResult{Stdout: d.logText}, nil
	case strings.Contains(script, "helmcharts.helm.cattle.io"):
		d.probes++
		return execResult{Stdout: probeOutput(d.step(d.probes))}, nil
	}
	return execResult{}, errors.New("unexpected script: " + script)
}

const newValues = "image:\n  tag: pr-123\nreplicas: 1\n"
const imgNew = "ghcr.io/teknoir/notebook:pr-123"
const imgOld = "ghcr.io/teknoir/notebook:main"

func appliedPlatform(t *testing.T) *fakePlatform {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	sequence(t, p, viewAt("r2", "active", ""))
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, notebookDetail(nil)))
	return p
}

func TestWaitAppliedHelmChart(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{step: func(n int) map[string]string {
		switch n {
		case 1: // baseline before the write: old values, old job
			return map[string]string{"chart": chartJSON("image:\n  tag: main\nreplicas: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		case 2: // the write has not reached the device
			return map[string]string{"chart": chartJSON("image:\n  tag: main\nreplicas: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		case 3: // values arrived, the old Job is still there
			return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u1", 1, 0)}
		case 4: // new Job running
			return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 0, 0)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 1, 0)}
	}}
	ex := &fakeExec{run: dev.run}
	e, _ := testEnv(t, p, ex)
	_, errs, err := setWait(t, e, "--wait=applied")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if dev.probes != 5 {
		t.Errorf("probes: %d, want 5", dev.probes)
	}
	for _, frag := range []string{"the device does not have the published values yet", "waiting for helm-controller to replace Job helm-install-notebook", "is running", "helm Job helm-install-notebook succeeded", "applied after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("progress lacks %q:\n%s", frag, errs)
		}
	}
	calls := ex.callList()
	if calls[0] != "OPEN vision-station-01" || !ex.closed {
		t.Errorf("the shell is opened once and closed: %v closed=%v", calls, ex.closed)
	}
	for _, c := range calls[1:] {
		if strings.Contains(c, "get pods") {
			t.Errorf("applied does not look at pods: %s", c)
		}
	}
}

// The robot's state is recorded before the write: the old Job is how a new one is recognised.
func TestWaitBaselineIsTakenBeforeTheWrite(t *testing.T) {
	var mu sync.Mutex
	var trace []string
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(rec) (int, string) {
		mu.Lock()
		trace = append(trace, "write")
		mu.Unlock()
		return 200, writeOK(nbID, "r2")
	})
	sequence(t, p, viewAt("r2", "active", ""))
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, notebookDetail(nil)))
	dev := &helmDevice{step: func(n int) map[string]string {
		mu.Lock()
		trace = append(trace, fmt.Sprintf("probe%d", n))
		mu.Unlock()
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 1, 0)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	if _, _, err := setWait(t, e, "--wait=applied"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(trace, ","); got != "probe1,write,probe2" {
		t.Errorf("order: %s", got)
	}
}

func TestWaitAppliedFailingJobIsExit7WithLog(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{logText: "Error: INSTALLATION FAILED: pull access denied\n", step: func(n int) map[string]string {
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 0, 1)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := setWait(t, e, "--wait=applied")
	if exitCodeOf(err) != codeUnhealthy || !strings.Contains(err.Error(), "helm Job helm-install-notebook failed 1 time(s)") || !strings.Contains(err.Error(), "pull access denied") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestWaitAppliedTimeout(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{step: func(n int) map[string]string {
		return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := setWait(t, e, "--wait=applied", "--timeout", "20s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "applied") || !strings.Contains(err.Error(), "published values") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestWaitAppliedNeedsDeviceBeforeWriting(t *testing.T) {
	p := appliedPlatform(t)
	ex := &fakeExec{run: func(string) (execResult, error) {
		return execResult{}, errors.New("cannot reach vision-station-01 over WireGuard: Connection timed out")
	}}
	e, _ := testEnv(t, p, ex)
	_, _, err := setWait(t, e, "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot check the robot before the change") || !strings.Contains(err.Error(), "--wait=published") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if len(p.seen("PATCH", patchPath)) != 0 {
		t.Error("nothing may be written when the device cannot be checked")
	}
	// published needs no device at all
	e2, _ := testEnv(t, p, nil)
	if _, _, err := setWait(t, e2, "--wait=published"); err != nil {
		t.Errorf("published: %v", err)
	}
}

func TestWaitReadyHelmChart(t *testing.T) {
	p := appliedPlatform(t)
	// pods become ready on the third poll after the Job finished; stability needs 20 s of 2 s polls
	dev := &helmDevice{step: func(n int) map[string]string {
		chart, job := chartJSON(newValues, "0.0.5"), jobJSON("u2", 1, 0)
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		wl := deployJSON("notebook", 2, 2, 1, 1, 1, imgNew)
		switch {
		case n <= 3:
			wl = deployJSON("notebook", 2, 2, 1, 0, 1, imgNew)
			return map[string]string{"chart": chart, "job": job, "workloads": listJSON(wl),
				"pods": listJSON(podMap(podOpt{name: "notebook-a", app: "notebook", image: imgOld, ready: true}), podMap(podOpt{name: "notebook-b", app: "notebook", image: imgNew, phase: "Pending"}))}
		case n == 4: // a restart while checking
			return map[string]string{"chart": chart, "job": job, "workloads": listJSON(wl),
				"pods": listJSON(podMap(podOpt{name: "notebook-b", app: "notebook", image: imgNew, ready: true, restarts: 0}))}
		case n == 5:
			return map[string]string{"chart": chart, "job": job, "workloads": listJSON(wl),
				"pods": listJSON(podMap(podOpt{name: "notebook-b", app: "notebook", image: imgNew, ready: true, restarts: 1}))}
		}
		return map[string]string{"chart": chart, "job": job, "workloads": listJSON(wl),
			"pods": listJSON(podMap(podOpt{name: "notebook-b", app: "notebook", image: imgNew, ready: true, restarts: 1}))}
	}}
	e, clk := testEnv(t, p, &fakeExec{run: dev.run})
	t0 := clk.now()
	_, errs, err := setWait(t, e, "--wait", "--image", ":pr-123")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	// probe 1 is the baseline; 2-3 rollout not done; 4 ready; 5 a restart starts the 20 s over; ready at probe 15
	if dev.probes != 15 {
		t.Errorf("probes: %d, want 15", dev.probes)
	}
	if d := clk.now().Sub(t0); d < readyStableFor {
		t.Errorf("ready too early: %s", d)
	}
	for _, frag := range []string{"checking for restarts", "no restarts for 20s", "ready after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("progress lacks %q:\n%s", frag, errs)
		}
	}
}

func TestWaitReadyCrashLoopIsExit7(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{step: func(n int) map[string]string {
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 1, 0),
			"workloads": listJSON(deployJSON("notebook", 2, 2, 1, 1, 0, imgNew)),
			"pods":      listJSON(podMap(podOpt{name: "notebook-b", app: "notebook", image: imgNew, waiting: "ImagePullBackOff"}))}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := setWait(t, e, "--wait=ready", "--image", ":pr-123")
	if exitCodeOf(err) != codeUnhealthy || !strings.Contains(err.Error(), "ImagePullBackOff") || !strings.Contains(err.Error(), "notebook-b") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestWaitReleaseWithoutWorkloadsIsReadyWhenApplied(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{step: func(n int) map[string]string {
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 1, 0), "workloads": listJSON(), "pods": listJSON()}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, errs, err := setWait(t, e, "--wait")
	if err != nil || !strings.Contains(errs, "no Deployment, StatefulSet or DaemonSet") {
		t.Errorf("%v\n%s", err, errs)
	}
}

func TestWaitWorkloadImage(t *testing.T) {
	const wid = "Deployment/default/whoami"
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(wid, "r2"))
	v := viewAt("r2", "", "")
	v.OverrideStatus = []api.OverrideStatus{{Target: wid, State: "active"}}
	sequence(t, p, v)
	d := &api.ConfigResourceDetail{ID: wid, Resource: deployResource("whoami", "whoami", "traefik/whoami:v1.11.0"),
		Images:   []api.ImageKey{{Path: "whoami", Form: "container", Current: "traefik/whoami:v1.11.0", Source: "manifest", Primary: true}},
		Rendered: map[string]any{"kind": "Deployment", "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "whoami", "image": "traefik/whoami:v1.11.0"}}}}}}}
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))

	var mu sync.Mutex
	n := 0
	ex := &fakeExec{run: func(script string) (execResult, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		const img = "traefik/whoami:v1.11.0"
		switch {
		case n == 1: // not yet on the device
			return execResult{Stdout: probeOutput(map[string]string{"obj": mustJSON(deployJSON("whoami", 4, 4, 1, 1, 1, "traefik/whoami:v1.10.4")), "pods": listJSON()})}, nil
		case n == 2: // spec updated, controller not caught up
			return execResult{Stdout: probeOutput(map[string]string{"obj": mustJSON(deployJSON("whoami", 5, 4, 1, 0, 1, img)), "pods": listJSON()})}, nil
		}
		return execResult{Stdout: probeOutput(map[string]string{"obj": mustJSON(deployJSON("whoami", 5, 5, 1, 1, 1, img)),
			"pods": listJSON(podMap(podOpt{name: "whoami-x", app: "whoami", image: img, ready: true}))})}, nil
	}}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "set", "vision-station-01", wid, "--image", "whoami=traefik/whoami:v1.11.0", "--wait=applied")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(errs, `runs "traefik/whoami:v1.10.4"`) || !strings.Contains(errs, "generation 5") || !strings.Contains(errs, "has the published spec") {
		t.Errorf("progress:\n%s", errs)
	}
	for _, c := range ex.callList() {
		if strings.Contains(c, "helmcharts") {
			t.Errorf("a Deployment is not probed as a HelmChart: %s", c)
		}
	}
}

func TestWaitCommandAppliedForOverriddenResources(t *testing.T) {
	p := newFakePlatform(t)
	sequence(t, p, viewAt("r2", "active", ""))
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, notebookDetail(nil)))
	dev := &helmDevice{step: func(n int) map[string]string {
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 1, 0)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	out, _, err := runCLI(t, e, "config", "wait", "vision-station-01", "--for", "applied")
	if err != nil || out != "vision-station-01: applied\n" || dev.probes != 1 {
		t.Errorf("%v %q probes=%d", err, out, dev.probes)
	}
}

func TestShellHelpers(t *testing.T) {
	if got := shq("it's"); got != `'it'\''s'` {
		t.Errorf("shq: %s", got)
	}
	if got := shJoin([]string{"sudo", "k3s", "kubectl", "get", "pods", "-A", "-o", "jsonpath={.items[*].metadata.name}"}); got != `sudo k3s kubectl get pods -A -o 'jsonpath={.items[*].metadata.name}'` {
		t.Errorf("shJoin: %s", got)
	}
	base := []string{"-o", "ProxyCommand=x", "-i", "/t/id", "teknoir@dev-1"}
	got := buildSSHExecArgs(base, "/t/cm", "uname -a")
	want := []string{"-o", "ProxyCommand=x", "-i", "/t/id", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=60",
		"-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-o", "ControlPath=/t/cm", "teknoir@dev-1", "uname -a"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if got := buildSSHExecArgs(base, "", "id"); strings.Contains(strings.Join(got, " "), "ControlMaster") || got[len(got)-2] != "teknoir@dev-1" {
		t.Errorf("no multiplexing without a control path: %v", got)
	}
	if base[len(base)-1] != "teknoir@dev-1" || len(base) != 5 {
		t.Error("the base arguments must not be modified")
	}
}
