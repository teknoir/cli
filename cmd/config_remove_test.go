package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

const (
	llmID      = "HelmChart/default/local-llm"
	removePath = "/config/vision-station-01/local"
)

// localView is the standard view plus a local-llm HelmChart written by Devstudio; gone drops it.
func localView(gone bool) *api.ConfigTargetView {
	v := standardView()
	if !gone {
		llm := helmResource("local-llm", api.LayerLocal, "devstudio", "1.2.0", nil)
		v.Configuration.Resources = append(v.Configuration.Resources, llm)
		v.Configuration.Effective = append(v.Configuration.Effective, llm)
	}
	return v
}

func removedBody(id string, dry bool, extra string) string {
	d := "false"
	if dry {
		d = "true"
	}
	return `{"target":{"kind":"robot","name":"vision-station-01","device":"vision-station-01"},"id":"` + id + `","dryRun":` + d +
		`,"hadOverride":false,"owner":"devstudio","removed":{"apiVersion":"helm.cattle.io/v1","kind":"HelmChart","metadata":{"name":"local-llm","namespace":"default"},"spec":{"chart":"llm","version":"1.2.0","targetNamespace":"llm-ns"}}` + extra + `,"warnings":["Devstudio may write it again"]}`
}

func localPlatform(t *testing.T) *fakePlatform {
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, localView(false)))
	return p
}

func TestConfigRemoveSuccess(t *testing.T) {
	p := localPlatform(t)
	p.on("DELETE", removePath, 200, removedBody(llmID, false, ""))
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.HasPrefix(out, "Removed HelmChart/default/local-llm from vision-station-01.\n") || !strings.Contains(out, "written by: devstudio") ||
		!strings.Contains(out, "chart: llm 1.2.0 in namespace llm-ns") {
		t.Errorf("stdout:\n%s", out)
	}
	if !strings.Contains(errs, "warning: Devstudio may write it again") {
		t.Errorf("stderr:\n%s", errs)
	}
	reqs := p.seen("DELETE", removePath)
	if len(reqs) != 1 || reqs[0].Query != "id=HelmChart%2Fdefault%2Flocal-llm" {
		t.Errorf("requests: %+v", reqs)
	}
	// a full id needs no lookup of the target
	if len(p.seen("GET", "/config/vision-station-01")) != 0 {
		t.Error("a Kind/namespace/name id must not load the target")
	}
}

func TestConfigRemoveDryRun(t *testing.T) {
	p := localPlatform(t)
	p.onFunc("DELETE", removePath, func(r rec) (int, string) {
		if !strings.Contains(r.Query, "dryRun=1") {
			t.Errorf("a DELETE without dryRun=1 in a dry run: %q", r.Query)
		}
		return 200, removedBody(llmID, true, "")
	})
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--dry-run")
	if err != nil || !strings.HasPrefix(out, "Would remove HelmChart/default/local-llm from vision-station-01.\n") || !strings.Contains(out, "chart: llm") {
		t.Errorf("%v\n%s", err, out)
	}
	if _, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--dry-run", "--wait=published"); exitCodeOf(err) != codeUsage {
		t.Errorf("--dry-run with --wait: %v", err)
	}
	// without --dry-run the flag is not sent
	p.onFunc("DELETE", removePath, func(r rec) (int, string) {
		if strings.Contains(r.Query, "dryRun") {
			t.Errorf("dryRun sent without --dry-run: %q", r.Query)
		}
		return 200, removedBody(llmID, false, "")
	})
	if _, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRemoveNotLocalPassesTheServersMessage(t *testing.T) {
	p := localPlatform(t)
	msg := "whoami is a robot app of vision-station-01: remove it from the robot's apps, not here"
	p.on("DELETE", removePath, 409, `{"error":{"name":"ConflictError","message":"`+msg+`"}}`)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", "Deployment/default/whoami")
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), msg) {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	// a short name of a resource that is not local is still sent to the server
	_, _, err = runCLI(t, e, "config", "remove", "vision-station-01", "whoami")
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), msg) {
		t.Errorf("short name: exit %d: %v", exitCodeOf(err), err)
	}
	if got := p.seen("DELETE", removePath); len(got) != 2 || !strings.Contains(got[1].Query, "Deployment%2Fdefault%2Fwhoami") {
		t.Errorf("requests: %+v", got)
	}
}

func TestConfigRemoveErrors(t *testing.T) {
	p := localPlatform(t)
	e, _ := testEnv(t, p, nil)
	for status, want := range map[int]int{404: codeNotFound, 403: codeAuth, 400: codeRejected} {
		p.on("DELETE", removePath, status, `{"error":{"name":"Error","message":"nope"}}`)
		_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", "HelmChart/default/missing")
		if exitCodeOf(err) != want {
			t.Errorf("%d: exit %d: %v", status, exitCodeOf(err), err)
		}
	}
	// an unknown short name never reaches the server
	n := len(p.seen("DELETE", removePath))
	_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", "nosuch")
	if exitCodeOf(err) != codeNotFound || len(p.seen("DELETE", removePath)) != n {
		t.Errorf("unknown name: exit %d: %v", exitCodeOf(err), err)
	}
	if _, _, err := runCLI(t, e, "config", "remove", "vision-station-01"); exitCodeOf(err) != codeUsage {
		t.Errorf("missing resource: %v", err)
	}
}

func TestConfigRemoveResolvesAShortName(t *testing.T) {
	p := localPlatform(t)
	p.on("DELETE", removePath, 200, removedBody(llmID, false, ""))
	e, _ := testEnv(t, p, nil)
	for _, arg := range []string{"local-llm", "HelmChart/local-llm"} {
		if _, _, err := runCLI(t, e, "config", "remove", "vision-station-01", arg); err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
	}
	for _, r := range p.seen("DELETE", removePath) {
		if r.Query != "id=HelmChart%2Fdefault%2Flocal-llm" {
			t.Errorf("query %q", r.Query)
		}
	}
}

func TestConfigRemoveJSON(t *testing.T) {
	p := localPlatform(t)
	p.on("DELETE", removePath, 200, removedBody(llmID, false, ""))
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID, Owner string
		DryRun    bool
		Removed   map[string]any
		Warnings  []string
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.ID != llmID || v.Owner != "devstudio" || v.Removed["kind"] != "HelmChart" || len(v.Warnings) != 1 {
		t.Errorf("%v\n%s", err, out)
	}
	if strings.Contains(out, `"wait"`) {
		t.Errorf("no wait object without --wait:\n%s", out)
	}
}

func TestConfigRemoveNoop(t *testing.T) {
	p := localPlatform(t)
	p.on("DELETE", removePath, 200, `{"id":"`+llmID+`","dryRun":false,"noop":true,"hadOverride":false,"warnings":[]}`)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID)
	if err != nil || out != "HelmChart/default/local-llm was already gone from vision-station-01.\n" {
		t.Errorf("%v %q", err, out)
	}
}

// ---------- --wait ----------

func TestConfigRemoveWaitPublished(t *testing.T) {
	p := newFakePlatform(t)
	p.on("DELETE", removePath, 200, removedBody(llmID, false, ""))
	polls := sequence(t, p, localView(false), localView(false), localView(true))
	e, clk := testEnv(t, p, nil)
	t0 := clk.now()
	out, errs, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if *polls != 3 || clk.now().Sub(t0) != 2*e.pollInterval {
		t.Errorf("polls %d, waited %s", *polls, clk.now().Sub(t0))
	}
	if !strings.Contains(errs, "is still in the robot's local configuration") || !strings.Contains(errs, "published after") || strings.Contains(out, "published after") {
		t.Errorf("stderr:\n%s\nstdout:\n%s", errs, out)
	}
	// json carries the wait object
	p2 := newFakePlatform(t)
	p2.on("DELETE", removePath, 200, removedBody(llmID, false, ""))
	sequence(t, p2, localView(true))
	e2, _ := testEnv(t, p2, nil)
	out, _, err = runCLI(t, e2, "config", "remove", "vision-station-01", llmID, "--wait=published", "-o", "json")
	if err != nil || !strings.Contains(out, `"reached": true`) || !strings.Contains(out, `"level": "published"`) {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestConfigRemoveWaitPublishedTimeout(t *testing.T) {
	p := newFakePlatform(t)
	p.on("DELETE", removePath, 200, removedBody(llmID, false, ""))
	sequence(t, p, localView(false))
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "waiting for the change to be published") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

// goneDevice scripts the robot: each probe gets the number of the probe and answers with the sections.
type goneDevice struct {
	mu     sync.Mutex
	probes int
	step   func(n int) (obj, release string)
}

func (d *goneDevice) run(script string) (execResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !strings.Contains(script, "get") {
		return execResult{}, errors.New("unexpected script: " + script)
	}
	d.probes++
	obj, rel := d.step(d.probes)
	out := sectionMark + "obj\n" + obj + "\n"
	if strings.Contains(script, "get secrets") {
		out += sectionMark + "release\n" + rel + "\n"
	}
	return execResult{Stdout: out}, nil
}

func removeAppliedPlatform(t *testing.T) *fakePlatform {
	p := newFakePlatform(t)
	p.on("DELETE", removePath, 200, removedBody(llmID, false, ""))
	sequence(t, p, localView(true))
	return p
}

func TestConfigRemoveWaitAppliedHelmChart(t *testing.T) {
	p := removeAppliedPlatform(t)
	dev := &goneDevice{step: func(n int) (string, string) {
		switch n {
		case 1:
			return "helmchart.helm.cattle.io/local-llm", "secret/sh.helm.release.v1.local-llm.v1"
		case 2: // chart deleted, release not yet uninstalled
			return "", "secret/sh.helm.release.v1.local-llm.v1\nsecret/sh.helm.release.v1.local-llm.v2"
		}
		return "", "No resources found in llm-ns namespace."
	}}
	ex := &fakeExec{run: dev.run}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=applied")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if dev.probes != 3 {
		t.Errorf("probes %d", dev.probes)
	}
	for _, frag := range []string{"is still on the robot", "helm release local-llm is still installed in namespace llm-ns (2 release secret(s))", "is gone from the robot and its helm release is uninstalled", "applied after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("progress lacks %q:\n%s", frag, errs)
		}
	}
	calls := ex.callList()
	if calls[0] != "OPEN vision-station-01" || !ex.closed {
		t.Errorf("the shell is opened once (before the delete) and closed: %v closed=%v", calls, ex.closed)
	}
	if !strings.Contains(calls[1], "helmcharts.helm.cattle.io") || !strings.Contains(calls[1], "owner=helm,name=local-llm") || !strings.Contains(calls[1], "-n llm-ns") {
		t.Errorf("probe: %s", calls[1])
	}
}

func TestConfigRemoveWaitAppliedWorkload(t *testing.T) {
	p := newFakePlatform(t)
	v := localView(false)
	dep := deployResource("whoami", "whoami", "traefik/whoami:v1.10.4")
	dep.Layer = api.LayerLocal
	v.Configuration.Effective = append(v.Configuration.Effective, dep)
	sequence(t, p, v, standardView())
	p.on("DELETE", removePath, 200, `{"id":"Deployment/default/whoami","dryRun":false,"hadOverride":false,"warnings":[],"removed":{"kind":"Deployment"}}`)
	dev := &goneDevice{step: func(n int) (string, string) {
		if n == 1 {
			return "deployment.apps/whoami", ""
		}
		return "", ""
	}}
	ex := &fakeExec{run: dev.run}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "remove", "vision-station-01", "Deployment/default/whoami", "--wait=applied")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	for _, c := range ex.callList() {
		if strings.Contains(c, "secrets") {
			t.Errorf("a Deployment has no helm release to look for: %s", c)
		}
	}
	if !strings.Contains(errs, "Deployment/default/whoami is gone from the robot") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigRemoveWaitAppliedStillThere(t *testing.T) {
	p := removeAppliedPlatform(t)
	dev := &goneDevice{step: func(int) (string, string) { return "helmchart.helm.cattle.io/local-llm", "" }}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=applied", "--timeout", "20s")
	if exitCodeOf(err) != codeTimeout {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	for _, frag := range []string{"still on the robot after 20s", "the robot's agent did not delete it", "tnctl exec vision-station-01 -- sudo k3s kubectl -n default delete helmcharts.helm.cattle.io local-llm"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("error lacks %q: %v", frag, err)
		}
	}
	// json still prints the result, with reached false
	p2 := removeAppliedPlatform(t)
	e2, _ := testEnv(t, p2, &fakeExec{run: dev.run})
	out, _, err := runCLI(t, e2, "config", "remove", "vision-station-01", llmID, "--wait=applied", "--timeout", "10s", "-o", "json")
	if err == nil || !strings.Contains(out, `"reached": false`) {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestConfigRemoveWaitAppliedReleaseStays(t *testing.T) {
	p := removeAppliedPlatform(t)
	dev := &goneDevice{step: func(int) (string, string) { return "", "secret/sh.helm.release.v1.local-llm.v1" }}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=applied", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "helm release local-llm is still installed") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigRemoveWaitAppliedNeedsTheDeviceBeforeDeleting(t *testing.T) {
	p := removeAppliedPlatform(t)
	e, _ := testEnv(t, p, nil) // no way to run commands on the device
	_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot check the robot before the change") || !strings.Contains(err.Error(), "--wait=published") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if len(p.seen("DELETE", removePath)) != 0 {
		t.Error("nothing may be removed when the device cannot be checked")
	}
}

func TestConfigRemoveWaitAppliedSudo(t *testing.T) {
	p := removeAppliedPlatform(t)
	dev := &goneDevice{step: func(int) (string, string) { return "sudo: a password is required", "" }}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestEvalGone(t *testing.T) {
	g := &goneItem{id: llmID, kind: "HelmChart", ns: "default", name: "local-llm", targetNS: "llm-ns"}
	probe := func(obj, rel string) string {
		return sectionMark + "obj\n" + obj + "\n" + sectionMark + "release\n" + rel + "\n"
	}
	for _, c := range []struct {
		out  string
		want checkState
	}{
		{probe("", ""), checkDone},
		{probe("", "No resources found in llm-ns namespace."), checkDone},
		{probe("Error from server (NotFound): helmcharts not found", ""), checkDone},
		{probe("error: the server doesn't have a resource type \"helmcharts\"", ""), checkDone},
		{probe("helmchart.helm.cattle.io/local-llm", ""), checkPending},
		{probe("", "secret/sh.helm.release.v1.local-llm.v3"), checkPending},
		{probe("", "secret/sh.helm.release.v1.local-llm-2.v1"), checkDone},
		{probe("", "error: Forbidden"), checkPending},
		{probe("sudo: a password is required", ""), checkFatal},
	} {
		if r := evalGone(g, c.out); r.state != c.want {
			t.Errorf("%q: state %d (%s), want %d", c.out, r.state, r.msg, c.want)
		}
	}
}
