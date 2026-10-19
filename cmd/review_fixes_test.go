package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/api"
)

// ---------- ready: a release with several workloads ----------

func TestEvalPodsReadyOnlyHoldsTheChangedWorkloadToTheImage(t *testing.T) {
	const img = "ghcr.io/teknoir/notebook:pr-123"
	const old = "ghcr.io/teknoir/notebook:main"
	const side = "ghcr.io/teknoir/sidecar:1"
	wls := asObjs(deployJSON("notebook", 2, 2, 1, 1, 1, img), deployJSON("sidecar", 1, 1, 1, 1, 1, side))
	pods := asObjs(
		podMap(podOpt{name: "notebook-b", app: "notebook", image: img, ready: true}),
		podMap(podOpt{name: "notebook-a", app: "notebook", image: old, ready: true, terminating: true}),
		podMap(podOpt{name: "sidecar-a", app: "sidecar", image: side, ready: true}),
	)
	if r := evalPodsReady(wls, pods, img); r.state != checkDone || !strings.Contains(r.msg, "2 pod") {
		t.Errorf("the sidecar has other pods and is judged on its own: %+v", r)
	}
	// the sidecar not ready is still waited for
	pods[2] = podMap(podOpt{name: "sidecar-a", app: "sidecar", image: side, phase: "Pending"})
	if r := evalPodsReady(wls, pods, img); r.state != checkPending || !strings.Contains(r.msg, "sidecar-a") {
		t.Errorf("got %+v", r)
	}
	// an image no workload has filters nothing
	if r := evalPodsReady(wls[1:], asObjs(podMap(podOpt{name: "sidecar-a", app: "sidecar", image: side, ready: true})), "elsewhere:1"); r.state != checkDone {
		t.Errorf("got %+v", r)
	}
}

func TestEvalPodsReadyFollowsInitContainerImages(t *testing.T) {
	dep := deployJSON("app", 2, 2, 1, 1, 1, "app:1")
	dep["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["initContainers"] = []any{map[string]any{"name": "init-db", "image": "db-init:v2"}}
	newPod := podMap(podOpt{name: "app-b", app: "app", image: "app:1", ready: true})
	newPod["spec"].(map[string]any)["initContainers"] = []any{map[string]any{"name": "init-db", "image": "db-init:v2"}}
	oldPod := podMap(podOpt{name: "app-a", app: "app", image: "app:1", ready: true})
	oldPod["spec"].(map[string]any)["initContainers"] = []any{map[string]any{"name": "init-db", "image": "db-init:v1"}}

	if r := evalPodsReady(asObjs(dep), asObjs(newPod), "db-init:v2"); r.state != checkDone {
		t.Errorf("a pod with the init image is the new one: %+v", r)
	}
	if r := evalPodsReady(asObjs(dep), asObjs(oldPod), "db-init:v2"); r.state != checkPending {
		t.Errorf("a pod with the old init image is not: %+v", r)
	}
	// several wanted images: the pod must run all of them
	if r := evalPodsReady(asObjs(dep), asObjs(newPod), "db-init:v2", "app:1"); r.state != checkDone {
		t.Errorf("got %+v", r)
	}
	if r := evalPodsReady(asObjs(dep), asObjs(oldPod), "db-init:v2", "app:1"); r.state != checkPending {
		t.Errorf("got %+v", r)
	}
}

func TestWaitReadyHelmChartWithSeveralWorkloads(t *testing.T) {
	p := appliedPlatform(t)
	const side = "ghcr.io/teknoir/sidecar:1"
	dev := &helmDevice{step: func(n int) map[string]string {
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 1, 0),
			"workloads": listJSON(deployJSON("notebook", 2, 2, 1, 1, 1, imgNew), deployJSON("sidecar", 1, 1, 1, 1, 1, side)),
			"pods": listJSON(podMap(podOpt{name: "notebook-b", app: "notebook", image: imgNew, ready: true}),
				podMap(podOpt{name: "sidecar-a", app: "sidecar", image: side, ready: true}))}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, errs, err := setWait(t, e, "--wait", "--image", ":pr-123", "--timeout", "2m")
	if err != nil {
		t.Fatalf("a workload that does not run the image must not block ready: %v\n%s", err, errs)
	}
	if !strings.Contains(errs, "2 pod(s) ready") {
		t.Errorf("progress:\n%s", errs)
	}
}

// ---------- applied: credentials the platform hides ----------

func TestSameValuesIgnoresWhatThePlatformHides(t *testing.T) {
	exp := parseValuesYAML("image:\n  tag: pr-1\ndbPassword: <hidden>\nimagePullSecrets:\n- name: <hidden>\nauth:\n  apiKey: <hidden>\n  user: bob\n")
	have := parseValuesYAML("image:\n  tag: pr-1\ndbPassword: s3cret\nimagePullSecrets:\n- name: regcred\nauth:\n  apiKey: k-123\n  user: bob\n")
	if !sameValues(exp, have) {
		t.Error("hidden leaves only have to exist on the device")
	}
	other := parseValuesYAML("image:\n  tag: OLD\ndbPassword: s3cret\nimagePullSecrets:\n- name: regcred\nauth:\n  apiKey: k-123\n  user: bob\n")
	if sameValues(exp, other) {
		t.Error("a visible difference still counts")
	}
	missing := parseValuesYAML("image:\n  tag: pr-1\nimagePullSecrets:\n- name: regcred\nauth:\n  apiKey: k-123\n  user: bob\n")
	if sameValues(exp, missing) {
		t.Error("a hidden key the device lacks is not applied yet")
	}
	shorter := parseValuesYAML("image:\n  tag: pr-1\ndbPassword: s3cret\nimagePullSecrets: []\nauth:\n  apiKey: k-123\n  user: bob\n")
	if sameValues(exp, shorter) {
		t.Error("a list of another length is not the same")
	}
	// the inputs are not modified
	if exp["dbPassword"] != "<hidden>" || have["dbPassword"] != "s3cret" {
		t.Errorf("inputs changed: %v %v", exp, have)
	}
}

func TestWaitAppliedWithHiddenCredentials(t *testing.T) {
	p := appliedPlatform(t)
	d := notebookDetail(nil)
	d.Rendered = map[string]any{"spec": map[string]any{"valuesContent": "dbPassword: <hidden>\nimage:\n  tag: pr-123\nreplicas: 1\n", "version": "0.0.5"}}
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	onDevice := "image:\n  tag: pr-123\nreplicas: 1\ndbPassword: hunter2\n"
	dev := &helmDevice{step: func(n int) map[string]string {
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		return map[string]string{"chart": chartJSON(onDevice, "0.0.5"), "job": jobJSON("u2", 1, 0)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	if _, errs, err := setWait(t, e, "--wait=applied", "--timeout", "30s"); err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
}

// ---------- applied: a helm Job that fails once is retried ----------

func TestWaitAppliedSurvivesAFailedFirstJobAttempt(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{step: func(n int) map[string]string {
		switch n {
		case 1:
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		case 2:
			return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 0, 1)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobJSON("u2", 1, 1)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, errs, err := setWait(t, e, "--wait=applied")
	if err != nil || !strings.Contains(errs, "helm-controller retries it") {
		t.Errorf("%v\n%s", err, errs)
	}
}

func TestWaitAppliedJobBackoffLimitIsExit7(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{logText: "pull access denied\n", step: func(n int) map[string]string {
		if n == 1 {
			return map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
		}
		return map[string]string{"chart": chartJSON(newValues, "0.0.5"), "job": jobWithJSON("u2", 0, 1, true)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := setWait(t, e, "--wait=applied")
	if exitCodeOf(err) != codeUnhealthy || !strings.Contains(err.Error(), "pull access denied") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

// ---------- applied: a removed version override ----------

func TestWaitAppliedUnsetVersionNeedsTheDeviceToDropIt(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	sequence(t, p, viewAt("r2", "active", ""))
	d := notebookDetail(nil)
	d.Rendered = map[string]any{"spec": map[string]any{"valuesContent": newValues}} // git pins no version
	d.Resource.Chart = &api.ChartInfo{Chart: "notebook"}
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	dev := &helmDevice{step: func(n int) map[string]string {
		// the device keeps the version the override set
		return map[string]string{"chart": chartJSON(newValues, "0.0.6"), "job": jobJSON("u1", 1, 0)}
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook", "--version", "--wait=applied", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), `still has chart version "0.0.6"`) {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	// once the device has no version, it is applied
	dev.step = func(n int) map[string]string {
		return map[string]string{"chart": chartJSON(newValues, ""), "job": jobJSON("u1", 1, 0)}
	}
	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook", "--version", "--wait=applied", "--timeout", "10s"); err != nil {
		t.Errorf("%v", err)
	}
}

// ---------- applied: sudo without a password ----------

func TestWaitAppliedSudoWithoutPasswordFailsBeforeTheWrite(t *testing.T) {
	p := appliedPlatform(t)
	ex := &fakeExec{run: func(string) (execResult, error) {
		return execResult{Stdout: "@@tnctl:chart\nsudo: a password is required\n@@tnctl:job\nsudo: a password is required\n"}, nil
	}}
	e, _ := testEnv(t, p, ex)
	_, _, err := setWait(t, e, "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if len(p.seen("PATCH", patchPath)) != 0 {
		t.Error("nothing may be written")
	}
	// the same in a Deployment's probe, after the write (config wait has no prepare)
	const wid = "Deployment/default/whoami"
	p2 := newFakePlatform(t)
	v := standardView()
	v.Overrides = api.DeviceOverrides{Resources: map[string]api.ResourceOverride{wid: {}}}
	v.OverridesRevision = "r1"
	v.Overrides.Revision = "r1"
	sequence(t, p2, v)
	d := &api.ConfigResourceDetail{ID: wid, Resource: deployResource("whoami", "whoami", "traefik/whoami:v1.11.0"),
		Rendered: map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "whoami", "image": "traefik/whoami:v1.11.0"}}}}}}}
	p2.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	ex2 := &fakeExec{run: func(string) (execResult, error) {
		return execResult{Stdout: "@@tnctl:obj\nsudo: a password is required\n"}, nil
	}}
	e2, _ := testEnv(t, p2, ex2)
	_, _, err = runCLI(t, e2, "config", "wait", "vision-station-01", wid, "--for", "applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Errorf("workload: exit %d: %v", exitCodeOf(err), err)
	}
}

// ---------- signals ----------

func TestWaitInterruptedClosesTheDevice(t *testing.T) {
	p := appliedPlatform(t)
	var mu sync.Mutex
	n := 0
	ex := &fakeExec{}
	ex.run = func(script string) (execResult, error) {
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		if k == 2 {
			// the user hits Ctrl-C (or an agent's timeout sends SIGTERM) while the change is awaited
			_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
		}
		if k >= 2 {
			time.Sleep(2 * time.Millisecond)
		}
		return execResult{Stdout: probeOutput(map[string]string{"chart": chartJSON("old: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)})}, nil
	}
	e, _ := testEnv(t, p, ex)
	_, _, err := setWait(t, e, "--wait=applied")
	if err == nil || !strings.Contains(err.Error(), "interrupted") || exitCodeOf(err) != codeError {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	ex.mu.Lock()
	closed := ex.closed
	ex.mu.Unlock()
	if !closed {
		t.Error("the shell (its key directory and ssh master) must be closed when interrupted")
	}
}

func TestClassifyCanceledIsAnInterruption(t *testing.T) {
	err := classify(errors.New("x: " + context.Canceled.Error()))
	if exitCodeOf(err) != codeError {
		t.Errorf("%v", err)
	}
	if err := classify(context.Canceled); !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("got %v", err)
	}
}

// ---------- set -f keeps the file's YAML ----------

func TestSetFromFileKeepsPlainScalarsAndComments(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(file, []byte("# my values\nenabled: yes\nimage:\n  tag: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := platformWith(t, standardView())
	p.on("PUT", patchPath, 200, writeOK(nbID, "r3"))
	e, _ := testEnv(t, p, nil)
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "-f", file, "replicas=2"); err != nil {
		t.Fatal(err)
	}
	vc, _ := lastBody(t, p, "PUT", patchPath)["valuesContent"].(string)
	if !strings.Contains(vc, "# my values") || !strings.Contains(vc, "enabled: yes\n") || strings.Contains(vc, `"yes"`) || !strings.Contains(vc, "replicas: 2") {
		t.Errorf("the file must not be re-spelled:\n%s", vc)
	}
}

func TestSetFlowValueReadsYAML11(t *testing.T) {
	p := platformWith(t, standardView())
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r3"))
	e, _ := testEnv(t, p, nil)
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "feature={enabled: yes}"); err != nil {
		t.Fatal(err)
	}
	b := lastBody(t, p, "PATCH", patchPath)
	if got := jsonOf(t, b["ops"]); got != `[{"op":"set","path":"feature","value":{"enabled":true}}]` {
		t.Errorf("a flow map reads yes like k=yes does: %s", got)
	}
}

// ---------- try: the resources of the new revision ----------

func TestTryWaitFollowsTheNewRevisionsResources(t *testing.T) {
	const repo = "https://github.com/teknoir/teknoir-demo-gitops"
	const appPath = "/config/vision-station-01/overrides/application"
	before := standardView()
	// feature-x renames the notebook manifest
	after := standardView()
	renamed := helmResource("notebook2", "fleet", "test", "0.0.5", map[string]any{"replicas": 1})
	renamed.Application = before.Configuration.Effective[0].Application
	after.Configuration.Effective = []api.FleetConfigResource{renamed, after.Configuration.Effective[1]}
	after.Overrides = api.DeviceOverrides{Revision: "r7", Applications: []api.ApplicationOverride{{RepoURL: repo, Path: "apps/mock-vision", Revision: "feature-x"}}}
	after.OverridesRevision = "r7"

	p := newFakePlatform(t)
	p.on("PUT", appPath, 200, `{"revision":"r7","dryRun":false,"overrides":{},"warnings":[]}`)
	var mu sync.Mutex
	wrote := false
	p.onFunc("GET", "/config/vision-station-01", func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if wrote {
			return 200, jsonOf(t, after)
		}
		return 200, jsonOf(t, before)
	})
	p.onFunc("PUT", appPath, func(rec) (int, string) {
		mu.Lock()
		wrote = true
		mu.Unlock()
		return 200, `{"revision":"r7","dryRun":false,"overrides":{},"warnings":[]}`
	})
	asked := map[string]int{}
	p.onFunc("GET", "/config/vision-station-01/resource", func(r rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		asked[r.Query]++
		if strings.Contains(r.Query, "notebook2") {
			d := notebookDetail(nil)
			d.Rendered = map[string]any{"spec": map[string]any{"valuesContent": "replicas: 1\n", "version": "0.0.5"}}
			return 200, jsonOf(t, d)
		}
		return 404, `{"error":{"name":"NotFoundError","message":"is not part of the configuration"}}`
	})
	dev := &helmDevice{step: func(n int) map[string]string {
		return map[string]string{"chart": chartJSON("replicas: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
	}}
	ex := &fakeExec{run: dev.run}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "try", "vision-station-01", "apps/mock-vision", "feature-x", "--wait=applied", "--timeout", "30s")
	if err != nil {
		t.Fatalf("the renamed resource is checked, the old one is not asked for: %v\n%s", err, errs)
	}
	var sawNew bool
	for _, c := range ex.callList() {
		if strings.Contains(c, "notebook2") {
			sawNew = true
		}
	}
	if !sawNew {
		t.Errorf("notebook2 was never probed: %v\n%s\n%v", ex.callList(), errs, asked)
	}
}

// A resource that left the configuration while waiting is skipped, not an error.
func TestWaitDeviceSkipsResourcesThatAreGone(t *testing.T) {
	p := newFakePlatform(t)
	v := viewAt("r2", "active", "")
	sequence(t, p, v)
	p.on("GET", "/config/vision-station-01/resource", 404, `{"error":{"name":"NotFoundError","message":"gone"}}`)
	ex := &fakeExec{}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "wait", "vision-station-01", "--for", "applied")
	if err != nil || !strings.Contains(errs, "no longer part of the configuration") {
		t.Errorf("%v\n%s", err, errs)
	}
}

// ---------- published: somebody else wrote after us ----------

func TestRevisionSuperseded(t *testing.T) {
	const mine = "20261019T120000Z-aaaa"
	cases := []struct {
		have string
		want bool
	}{
		{"20261019T120001Z-0000", true},
		{"20261019T120000Z-ffff", false}, // same second: order unknown
		{"20261019T115959Z-ffff", false},
		{"", false},
		{"r9", false},
	}
	for _, c := range cases {
		if got := revisionSuperseded(c.have, mine); got != c.want {
			t.Errorf("%q: %v", c.have, got)
		}
	}
	if revisionSuperseded("r2", "r1") {
		t.Error("revisions that are not tokens are only compared for equality")
	}
}

func TestWaitPublishedFollowsASupersedingWrite(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PATCH", patchPath, 200, writeOK(nbID, "20261019T120000Z-aaaa"))
	sequence(t, p, viewAt("20261019T115000Z-0000", "active", ""), viewAt("20261019T120004Z-bbbb", "active", ""))
	e, _ := testEnv(t, p, nil)
	_, errs, err := setWait(t, e, "--wait=published")
	if err != nil || !strings.Contains(errs, "superseded by 20261019T120004Z-bbbb") {
		t.Errorf("%v\n%s", err, errs)
	}
}

// ---------- diff with a stale override ----------

func TestDiffWithStaleOverride(t *testing.T) {
	v := standardView()
	v.Overrides = api.DeviceOverrides{Resources: map[string]api.ResourceOverride{
		nbID:                         {ValuesContent: "image:\n  tag: pr-123\n"},
		"Deployment/default/dropped": {ValuesContent: "x: 1\n"},
	}}
	p := platformWith(t, v)
	d := notebookDetail(nil)
	d.Base = map[string]any{"spec": map[string]any{"valuesContent": "image:\n  tag: main\n"}}
	d.Rendered = map[string]any{"spec": map[string]any{"valuesContent": "image:\n  tag: pr-123\n"}}
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	e, _ := testEnv(t, p, nil)

	out, _, err := runCLI(t, e, "config", "diff", "vision-station-01")
	if err != nil {
		t.Fatalf("one stale override must not fail the whole diff: %v", err)
	}
	for _, frag := range []string{"# Deployment/default/dropped", "stale override", "tnctl config reset vision-station-01 Deployment/default/dropped", "# HelmChart/default/notebook", "+      tag: pr-123"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in\n%s", frag, out)
		}
	}
	out, _, err = runCLI(t, e, "config", "diff", "vision-station-01", "-o", "json")
	if err != nil || !strings.Contains(out, `"stale": true`) {
		t.Errorf("%v\n%s", err, out)
	}
	// the platform answering 404 for a listed override is stale too
	p.on("GET", "/config/vision-station-01/resource", 404, `{"error":{"name":"NotFoundError","message":"is not part of the configuration"}}`)
	if out, _, err := runCLI(t, e, "config", "diff", "vision-station-01"); err != nil || !strings.Contains(out, "stale override") {
		t.Errorf("%v\n%s", err, out)
	}
}

// ---------- exit codes ----------

func TestRateLimitIsExit9WithRetryAfter(t *testing.T) {
	err := &api.Error{Status: 429, Code: "TooManyRequestsError", Message: "slow down", RetryAfter: 12}
	if exitCodeOf(err) != codeRateLimit || codeRateLimit != 9 {
		t.Errorf("exit %d", exitCodeOf(err))
	}
	var b bytes.Buffer
	writeError(&b, err, "json")
	var v struct {
		Error struct {
			Code       string
			Message    string
			RetryAfter int
		}
	}
	if json.Unmarshal(b.Bytes(), &v) != nil || v.Error.Code != "rate_limited" || v.Error.RetryAfter != 12 {
		t.Errorf("%s", b.String())
	}
	b.Reset()
	writeError(&b, &api.Error{Status: 404}, "json")
	if strings.Contains(b.String(), "retryAfter") {
		t.Errorf("only a rate limit has retryAfter: %s", b.String())
	}
}

func TestHelpDoesNotPromiseExit3ForRevisions(t *testing.T) {
	if strings.Contains(configLong, "3  not found: no such robot, device, resource or revision") {
		t.Error("an unknown revision is exit 5")
	}
	if !strings.Contains(configLong, "9  rate limited") {
		t.Error("exit 9 must be documented")
	}
}

// ---------- --wait without = ----------

func TestMisplacedWaitLevelIsCaught(t *testing.T) {
	p := platformWith(t, standardView())
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	p.on("DELETE", "/config/vision-station-01/overrides/resource", 200, writeOK(nbID, "r2"))
	sequence(t, p, viewAt("r2", "active", ""))
	e, _ := testEnv(t, p, nil)
	cases := map[string][]string{
		"delete": {"config", "delete", "vision-station-01", "notebook", "foo", "--wait", "applied"},
		"unset":  {"config", "unset", "vision-station-01", "notebook", "image.tag", "--wait", "ready"},
		"reset":  {"config", "reset", "vision-station-01", "--all", "--wait", "applied"},
		"try":    {"config", "try", "vision-station-01", "apps/mock-vision", "main", "--wait", "applied"},
		"untry":  {"config", "untry", "vision-station-01", "apps/mock-vision", "--wait", "applied"},
	}
	for name, args := range cases {
		_, _, err := runCLI(t, e, args...)
		if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "--wait=") {
			t.Errorf("%s: exit %d: %v", name, exitCodeOf(err), err)
		}
	}
	if len(p.seen("PATCH", patchPath)) != 0 || len(p.seen("DELETE", "/config/vision-station-01/overrides/resource")) != 0 {
		t.Error("nothing may be written")
	}
	// the explicit forms and a key that happens to be called like a level (no --wait) still work
	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook", "applied"); err != nil {
		t.Errorf("a key called applied without --wait: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook", "image.tag", "--wait=published"); err != nil {
		t.Errorf("explicit: %v", err)
	}
}
