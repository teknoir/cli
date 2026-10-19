package cmd

import (
	"context"
	"encoding/json"
	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/auth"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

const nbID = "HelmChart/default/notebook"

// platformWith returns a fake platform serving the standard view and a notebook detail.
func platformWith(t *testing.T, view *api.ConfigTargetView) *fakePlatform {
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, view))
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, notebookDetail(nil)))
	return p
}

func notebookDetail(ov *api.ResourceOverride) *api.ConfigResourceDetail {
	d := &api.ConfigResourceDetail{
		Target:   api.ConfigTargetRef{Kind: "robot", Name: "vision-station-01", Device: "vision-station-01"},
		ID:       nbID,
		Resource: standardView().Configuration.Effective[0],
		Images:   []api.ImageKey{{Path: "image", Form: "map", Current: "ghcr.io/teknoir/notebook:pr-123", Source: "values", Primary: true}},
		Rendered: map[string]any{"spec": map[string]any{"valuesContent": "image:\n  tag: pr-123\nreplicas: 1\n", "version": "0.0.5"}},
	}
	d.Editable.Values, d.Editable.Version, d.Editable.Images = true, true, true
	if ov != nil {
		d.Override = &struct {
			api.ResourceOverride
			Storage string `json:"storage"`
		}{*ov, api.StorageDeviceOverrides}
	}
	return d
}

func writeOK(id, rev string) string {
	return `{"target":{"kind":"robot","name":"vision-station-01","device":"vision-station-01"},"id":"` + id + `","storage":"device-overrides","revision":"` + rev + `","dryRun":false,"overrides":{},"warnings":[]}`
}

func TestExitCodesFromThePlatform(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   int
	}{
		{404, `{"error":{"name":"NotFoundError","message":"No robot or device called vision-station-01."}}`, codeNotFound},
		{400, `{"error":{"name":"InputError","message":"image.tag is a string, not a map"}}`, codeRejected},
		{403, `{"error":{"name":"NotAllowedError","message":"You may not change this teamspace."}}`, codeAuth},
		{409, `{"error":{"name":"ConflictError","message":"changed by someone else, try again"}}`, codeConflict},
		{500, `{"error":{"name":"Error","message":"this platform's device CRD does not support overrides yet"}}`, codeError},
	}
	for _, c := range cases {
		p := platformWith(t, standardView())
		p.on("PATCH", "/config/vision-station-01/overrides/resource", c.status, c.body)
		e, _ := testEnv(t, p, nil)
		_, _, err := runCLI(t, e, "config", "set", "vision-station-01", nbID, "replicas=2")
		if exitCodeOf(err) != c.want {
			t.Errorf("status %d: exit code %d (%v), want %d", c.status, exitCodeOf(err), err, c.want)
		}
		if err != nil && !strings.Contains(err.Error(), "HTTP") && c.status != 0 {
			t.Errorf("the error should carry the status: %v", err)
		}
	}
}

func TestNotLoggedInIsExit6(t *testing.T) {
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: p.srv.URL + "/api/device/fleet/teknoir-demo", Token: func(_ context.Context) (string, error) { return "expired", nil }}, "d", "teknoir-demo", nil
	}
	_, _, err := runCLI(t, e, "robots")
	if exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "tnctl login") {
		t.Errorf("a 302 to /oauth2/start must say run tnctl login, exit 6: %d %v", exitCodeOf(err), err)
	}
	e.fleet = func() (*api.Fleet, string, string, error) { return nil, "", "", auth.ErrNotLoggedIn }
	_, _, err = runCLI(t, e, "robots")
	if exitCodeOf(err) != codeAuth {
		t.Errorf("no stored login: %d %v", exitCodeOf(err), err)
	}
}

func TestRobots(t *testing.T) {
	p := newFakePlatform(t)
	body := `[
	  {"name":"vision-station-01","namespace":"teknoir-demo","type":"vision","labels":{"site":"karlstad"},"device":"vision-station-01","phase":"Online","nodes":[{"role":"main","nodeName":"a","joined":true,"ready":true,"k3s":"server"}],"layers":[],"deviations":0,"overrides":2,"oldestOverrideAt":"2026-10-17T12:00:00Z","fleets":["test"],"futureField":1},
	  {"name":"amr-02","namespace":"teknoir-demo","type":"amr","labels":{"site":"lund"},"device":"amr-02","phase":"Offline","nodes":[],"layers":[],"deviations":0,"fleets":[]}
	]`
	p.on("GET", "/robots", 200, body)
	e, _ := testEnv(t, p, nil)

	out, errs, err := runCLI(t, e, "robots")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"NAME", "vision-station-01", "Online", "1/1", "2 (oldest 2d)", "test", "amr-02", "Offline"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in\n%s", frag, out)
		}
	}
	if errs != "" {
		t.Errorf("nothing on stderr: %q", errs)
	}

	out, _, err = runCLI(t, e, "robots", "site=karlstad")
	if err != nil || strings.Contains(out, "amr-02") || !strings.Contains(out, "vision-station-01") {
		t.Errorf("label filter: %v\n%s", err, out)
	}
	// -o json is the platform's JSON, filtered, with fields tnctl does not know
	out, _, err = runCLI(t, e, "robots", "site=karlstad", "-o", "json")
	var list []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &list) != nil || len(list) != 1 || list[0]["futureField"] != 1.0 {
		t.Errorf("json: %v\n%s", err, out)
	}
	out, _, err = runCLI(t, e, "robots", "-o", "yaml")
	if err != nil || !strings.Contains(out, "- name: amr-02") && !strings.Contains(out, "name: amr-02") {
		t.Errorf("yaml: %v\n%s", err, out)
	}
	// An empty list is not an error, in every format.
	for _, args := range [][]string{{"robots", "site=nowhere"}, {"robots", "site=nowhere", "-o", "json"}, {"robots", "site=nowhere", "-o", "yaml"}} {
		out, errs, err := runCLI(t, e, args...)
		if err != nil || (len(args) == 2 && !strings.Contains(errs, "no robots in teamspace teknoir-demo with the specified labels")) {
			t.Errorf("%v: %v %q", args, err, errs)
		}
		if strings.Contains(strings.Join(args, " "), "json") && strings.TrimSpace(out) != "[]" {
			t.Errorf("json: %q", out)
		}
	}
	_, _, err = runCLI(t, e, "robots", "site")
	if exitCodeOf(err) != codeUsage {
		t.Errorf("bad label: %v", err)
	}
	_, _, err = runCLI(t, e, "robots", "-o", "xml")
	if exitCodeOf(err) != codeUsage {
		t.Errorf("bad format: %v", err)
	}
}

func TestRobot(t *testing.T) {
	p := newFakePlatform(t)
	p.on("GET", "/robots/vision-station-01", 200, `{"name":"vision-station-01","namespace":"teknoir-demo","type":"vision","typeDisplayName":"Vision station","labels":{"site":"karlstad"},"device":"vision-station-01","phase":"Online","nodes":[{"role":"main","nodeName":"a","joined":true,"ready":true,"k3s":"server"}],"layers":[{"layer":"fleet","source":"test","applications":2}],"deviations":0,"overrides":1,"fleets":["test"],"spec":{"type":"vision"},"configuration":{"resources":[],"effective":[{"identity":"x","kind":"HelmChart","name":"notebook","namespace":"default","layer":"fleet","source":"test","status":"winner"}],"deviations":[],"warnings":[]}}`)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "robot", "vision-station-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"vision-station-01", "Vision station", "Online", "a (main, server) ready", "fleet test: 2 applications", "tnctl config get vision-station-01", "1 effective"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in\n%s", frag, out)
		}
	}
	if _, _, err := runCLI(t, e, "robot"); exitCodeOf(err) != codeUsage {
		t.Errorf("no name: %v", err)
	}
	if _, _, err := runCLI(t, e, "robot", "nope"); exitCodeOf(err) != codeNotFound {
		t.Errorf("unknown robot: %v", err)
	}
}

func TestConfigGetTargetAndResource(t *testing.T) {
	v := standardView()
	v.Overrides = api.DeviceOverrides{Revision: "r1", Resources: map[string]api.ResourceOverride{nbID: {ValuesContent: "image:\n  tag: pr-123\n", Note: "try pr-123", SetBy: "a@teknoir.ai"}}}
	v.OverrideStatus = []api.OverrideStatus{{Target: nbID, State: "active"}}
	v.OverrideCount = 1
	v.OverridesRevision = "r1"
	v.Configuration.Effective[0].Override = &api.ResourceOverrideInfo{Storage: api.StorageDeviceOverrides, State: "active", Paths: []string{"image.tag"}}
	p := platformWith(t, v)
	e, _ := testEnv(t, p, nil)

	out, _, err := runCLI(t, e, "config", "get", "vision-station-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"Target: robot vision-station-01", "Overrides: 1 (revision r1, evaluated r1)", "RESOURCE", "HelmChart/default/notebook", "notebook@0.0.5", "Deployment/default/whoami", "traefik/whoami:v1.10.4", "active"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in\n%s", frag, out)
		}
	}
	out, _, _ = runCLI(t, e, "config", "get", "vision-station-01", "--overrides")
	if !strings.Contains(out, "values image.tag") || !strings.Contains(out, "try pr-123") || !strings.Contains(out, "a@teknoir.ai") {
		t.Errorf("overrides:\n%s", out)
	}
	out, _, _ = runCLI(t, e, "config", "get", "vision-station-01", "--layers")
	if !strings.Contains(out, "STATUS") {
		t.Errorf("layers:\n%s", out)
	}

	// -o json is the server's JSON unchanged
	out, _, err = runCLI(t, e, "config", "get", "vision-station-01", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	_ = json.Unmarshal([]byte(out), &got)
	_ = json.Unmarshal([]byte(jsonOf(t, v)), &want)
	if jsonOf(t, got) != jsonOf(t, want) {
		t.Errorf("json differs:\n%s", out)
	}

	// one resource, by short name
	d := notebookDetail(&api.ResourceOverride{ValuesContent: "image:\n  tag: pr-123\n", SetBy: "a@teknoir.ai", Note: "try pr-123"})
	d.Resource.Values = &api.FleetValues{Merged: map[string]any{"image": map[string]any{"tag": "pr-123"}}, Provenance: []api.ValueProvenance{{Path: "image.tag", Value: "pr-123", Layer: "override", Source: "vision-station-01"}}}
	d.OverrideStatus = &api.OverrideStatus{Target: nbID, State: "active"}
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	out, _, err = runCLI(t, e, "config", "get", "vision-station-01", "notebook")
	if err != nil {
		t.Fatal(err)
	}
	// "notebook" is only a HelmChart in the standard view: unique
	if !strings.Contains(out, "Resource: HelmChart/default/notebook") || !strings.Contains(out, "tag: pr-123") || !strings.Contains(out, "State:    active") || !strings.Contains(out, "try pr-123") {
		t.Errorf("resource:\n%s", out)
	}
	if q := p.seen("GET", "/config/vision-station-01/resource"); len(q) == 0 || q[len(q)-1].Query != "id=HelmChart%2Fdefault%2Fnotebook" {
		t.Errorf("the id must be resolved to Kind/ns/name: %+v", q)
	}
	out, _, _ = runCLI(t, e, "config", "get", "vision-station-01", "notebook", "--provenance")
	if !strings.Contains(out, "image.tag") || !strings.Contains(out, "override") || !strings.Contains(out, "PATH") {
		t.Errorf("provenance:\n%s", out)
	}
	out, _, _ = runCLI(t, e, "config", "get", "vision-station-01", "notebook", "--rendered")
	if !strings.Contains(out, "valuesContent") || strings.Contains(out, "Resource:") {
		t.Errorf("rendered:\n%s", out)
	}

	if _, _, err := runCLI(t, e, "config", "get", "vision-station-01", "--provenance"); exitCodeOf(err) != codeUsage {
		t.Errorf("--provenance needs a resource: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "get"); exitCodeOf(err) != codeUsage {
		t.Errorf("no target: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "get", "vision-station-01", "nope"); exitCodeOf(err) != codeNotFound {
		t.Errorf("unknown resource: %v", err)
	}
}

func TestConfigGetAmbiguousResource(t *testing.T) {
	v := standardView()
	v.Configuration.Effective = append(v.Configuration.Effective, deployResource("notebook", "nb", "x:1"))
	p := platformWith(t, v)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "get", "vision-station-01", "notebook")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "ambiguous, did you mean") ||
		!strings.Contains(err.Error(), "Deployment/default/notebook") || !strings.Contains(err.Error(), "HelmChart/default/notebook") {
		t.Errorf("got %d %v", exitCodeOf(err), err)
	}
}

// patchBody decodes the body of the last PATCH.
func lastBody(t *testing.T, p *fakePlatform, method, path string) map[string]any {
	t.Helper()
	reqs := p.seen(method, path)
	if len(reqs) == 0 {
		t.Fatalf("no %s %s", method, path)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(reqs[len(reqs)-1].Body), &m); err != nil {
		t.Fatalf("body %q: %v", reqs[len(reqs)-1].Body, err)
	}
	return m
}

const patchPath = "/config/vision-station-01/overrides/resource"

func TestSetBuildsOps(t *testing.T) {
	p := platformWith(t, standardView())
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	e, _ := testEnv(t, p, nil)

	out, errs, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook",
		"image.tag=1.10", "replicas=2", "debug=on", "pull=null", `nodeSelector.teknoir\.org/node-role=vision`, "name=010",
		`tolerations=[{key: gpu, operator: Exists}]`, "empty=", "--note", "try it", "--version", "0.0.6")
	if err != nil {
		t.Fatal(err)
	}
	b := lastBody(t, p, "PATCH", patchPath)
	want := `[{"op":"set","path":"image.tag","value":"1.10"},{"op":"set","path":"replicas","value":2},{"op":"set","path":"debug","value":true},{"op":"delete","path":"pull"},{"op":"set","path":"nodeSelector.teknoir\\.org/node-role","value":"vision"},{"op":"set","path":"name","value":"010"},{"op":"set","path":"tolerations","value":[{"key":"gpu","operator":"Exists"}]},{"op":"set","path":"empty","value":""}]`
	if got := jsonOf(t, b["ops"]); got != want {
		t.Errorf("ops:\n got %s\nwant %s", got, want)
	}
	if b["version"] != "0.0.6" || b["note"] != "try it" {
		t.Errorf("body: %v", b)
	}
	if q := p.seen("PATCH", patchPath)[0].Query; q != "id=HelmChart%2Fdefault%2Fnotebook" {
		t.Errorf("query %q", q)
	}
	if !strings.Contains(out, "override set: HelmChart/default/notebook on vision-station-01 (revision r2)") {
		t.Errorf("stdout: %s", out)
	}
	// coercion warnings go to stderr, once per coerced value
	if !strings.Contains(errs, "debug=on") || !strings.Contains(errs, "read as boolean true") || strings.Contains(errs, "image.tag") || strings.Contains(errs, "replicas") || strings.Contains(errs, "name=010") {
		t.Errorf("stderr: %s", errs)
	}
}

func TestSetStringFlagAndKeepsExistingStrings(t *testing.T) {
	v := standardView()
	v.Configuration.Effective[0].Values = &api.FleetValues{Merged: map[string]any{"port": "80", "replicas": float64(1)}}
	p := platformWith(t, v)
	d := notebookDetail(nil)
	d.Resource.Values = v.Configuration.Effective[0].Values
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	e, _ := testEnv(t, p, nil)

	_, errs, err := runCLI(t, e, "config", "set", "vision-station-01", nbID, "port=8080", "replicas=3", "x=1")
	if err != nil {
		t.Fatal(err)
	}
	ops := lastBody(t, p, "PATCH", patchPath)["ops"]
	if got, want := jsonOf(t, ops), `[{"op":"set","path":"port","value":"8080"},{"op":"set","path":"replicas","value":3},{"op":"set","path":"x","value":1}]`; got != want {
		t.Errorf("an existing string stays a string:\n got %s\nwant %s", got, want)
	}
	if errs != "" {
		t.Errorf("stderr: %s", errs)
	}
	_, _, err = runCLI(t, e, "config", "set", "vision-station-01", nbID, "a=1", "b=true", "c=null", "--string")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jsonOf(t, lastBody(t, p, "PATCH", patchPath)["ops"]), `[{"op":"set","path":"a","value":"1"},{"op":"set","path":"b","value":"true"},{"op":"set","path":"c","value":"null"}]`; got != want {
		t.Errorf("--string:\n got %s\nwant %s", got, want)
	}
}

func TestSetImage(t *testing.T) {
	p := platformWith(t, standardView())
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	e, _ := testEnv(t, p, nil)
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "--image", ":pr-123"); err != nil {
		t.Fatal(err)
	}
	b := lastBody(t, p, "PATCH", patchPath)
	if got := jsonOf(t, b["image"]); got != `{"value":":pr-123"}` || b["ops"] != nil {
		t.Errorf("image: %s %v", got, b)
	}
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "--image", "defaults.image=ghcr.io/x/y:2"); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, lastBody(t, p, "PATCH", patchPath)["image"]); got != `{"path":"defaults.image","value":"ghcr.io/x/y:2"}` {
		t.Errorf("image path: %s", got)
	}
	// a registry with a port keeps its colon
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "--image", "registry.local:5000/team/app:dev"); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, lastBody(t, p, "PATCH", patchPath)["image"]); got != `{"value":"registry.local:5000/team/app:dev"}` {
		t.Errorf("registry: %s", got)
	}

	// a workload: the container image
	p.on("PATCH", patchPath, 200, writeOK("Deployment/default/whoami", "r3"))
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "Deployment/default/whoami", "--image", "whoami=traefik/whoami:v1.11.0"); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, lastBody(t, p, "PATCH", patchPath)["image"]); got != `{"path":"whoami","value":"traefik/whoami:v1.11.0"}` {
		t.Errorf("workload image: %s", got)
	}
	// several containers: the images map, full images only
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "Deployment/default/whoami", "--image", "whoami=a/b:1", "--image", "sidecar=c/d:2"); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, lastBody(t, p, "PATCH", patchPath)["images"]); got != `{"sidecar":"c/d:2","whoami":"a/b:1"}` {
		t.Errorf("images: %s", got)
	}
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "Deployment/default/whoami", "--image", "whoami=:2", "--image", "sidecar=c/d:2"); exitCodeOf(err) != codeUsage {
		t.Errorf("tag-only with several images: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "--image", "a:1", "--image", "b=c:2"); exitCodeOf(err) != codeUsage {
		t.Errorf("two images on a HelmChart: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "--image", "has space"); exitCodeOf(err) != codeUsage {
		t.Errorf("bad image: %v", err)
	}
}

func TestSetUsageErrors(t *testing.T) {
	p := platformWith(t, standardView())
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r2"))
	e, _ := testEnv(t, p, nil)
	cases := map[string][]string{
		"nothing to set":         {"config", "set", "vision-station-01", "notebook"},
		"missing resource":       {"config", "set", "vision-station-01"},
		"not an assignment":      {"config", "set", "vision-station-01", "notebook", "replicas"},
		"--wait applied (space)": {"config", "set", "vision-station-01", "notebook", "replicas=2", "--wait", "applied"},
		"list index":             {"config", "set", "vision-station-01", "notebook", "items[0]=x"},
		"conflicting paths":      {"config", "set", "vision-station-01", "notebook", "a=1", "a.b=2"},
		"values on a workload":   {"config", "set", "vision-station-01", "Deployment/default/whoami", "a=1"},
		"version on a workload":  {"config", "set", "vision-station-01", "Deployment/default/whoami", "--version", "1"},
		"bad wait level":         {"config", "set", "vision-station-01", "notebook", "a=1", "--wait=soon"},
		"dry run with wait":      {"config", "set", "vision-station-01", "notebook", "a=1", "--dry-run", "--wait"},
		"file with image":        {"config", "set", "vision-station-01", "notebook", "-f", "x.yaml", "--image", ":1"},
		"unknown flag":           {"config", "set", "vision-station-01", "notebook", "a=1", "--bogus"},
		"NaN":                    {"config", "set", "vision-station-01", "notebook", "a=.nan"},
		"unsafe id":              {"config", "set", "vision-station-01", "HelmChart/default/x;ls", "a=1"},
	}
	for name, args := range cases {
		_, _, err := runCLI(t, e, args...)
		if exitCodeOf(err) != codeUsage {
			t.Errorf("%s: exit code %d (%v), want 2", name, exitCodeOf(err), err)
		}
	}
	if len(p.seen("PATCH", patchPath)) != 0 {
		t.Error("a usage error must not write anything")
	}
	_, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "a=1", "--wait", "applied")
	if err == nil || !strings.Contains(err.Error(), "did you mean --wait=applied") {
		t.Errorf("the hint: %v", err)
	}
}

func TestSetDryRun(t *testing.T) {
	p := platformWith(t, standardView())
	before := standardView().Configuration.Effective[0]
	after := before
	after.Values = &api.FleetValues{Merged: map[string]any{"image": map[string]any{"tag": "pr-123"}, "replicas": 1}}
	res := api.OverrideWriteResult{ID: nbID, DryRun: true, Before: &before, After: &after, Warnings: []string{"the tag is not in the registry"}}
	p.on("PATCH", patchPath, 200, jsonOf(t, res))
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "image.tag=pr-123", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if q := p.seen("PATCH", patchPath)[0].Query; !strings.Contains(q, "dryRun=1") {
		t.Errorf("query %q", q)
	}
	for _, frag := range []string{"Dry run, nothing written", "-    tag: main", "+    tag: pr-123", "--- before", "+++ after"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in\n%s", frag, out)
		}
	}
	if !strings.Contains(errs, "warning: the tag is not in the registry") {
		t.Errorf("server warnings go to stderr: %q", errs)
	}
	// no change
	res.After = res.Before
	p.on("PATCH", patchPath, 200, jsonOf(t, res))
	out, _, _ = runCLI(t, e, "config", "set", "vision-station-01", "notebook", "replicas=1", "--dry-run")
	if !strings.Contains(out, "No change to the effective resource") {
		t.Errorf("out: %s", out)
	}
}

func TestSetJSONOutputMergesWarnings(t *testing.T) {
	p := platformWith(t, standardView())
	p.on("PATCH", patchPath, 200, `{"id":"`+nbID+`","revision":"r2","dryRun":false,"overrides":{},"warnings":["server says hi"],"extra":true}`)
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "debug=yes", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Revision string   `json:"revision"`
		Warnings []string `json:"warnings"`
		Extra    bool     `json:"extra"`
		Wait     any      `json:"wait"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v.Revision != "r2" || !v.Extra || len(v.Warnings) != 2 || v.Warnings[0] != "server says hi" || !strings.Contains(v.Warnings[1], "debug=yes") || v.Wait != nil {
		t.Errorf("got %+v", v)
	}
	if strings.Contains(out, "override set:") {
		t.Errorf("no human text on stdout in json mode: %s", out)
	}
	if !strings.Contains(errs, "warning:") {
		t.Errorf("warnings also on stderr: %q", errs)
	}
}

func TestSetFromFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(file, []byte("image:\n  tag: pr-9\nresources:\n  limits:\n    memory: 2Gi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := platformWith(t, standardView())
	d := notebookDetail(&api.ResourceOverride{ValuesContent: "old: 1\n", Version: "0.0.7", Note: "keep me"})
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	p.on("PUT", patchPath, 200, writeOK(nbID, "r3"))
	e, _ := testEnv(t, p, nil)

	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "-f", file); err != nil {
		t.Fatal(err)
	}
	b := lastBody(t, p, "PUT", patchPath)
	if b["valuesContent"] != "image:\n  tag: pr-9\nresources:\n  limits:\n    memory: 2Gi\n" || b["version"] != "0.0.7" || b["note"] != "keep me" {
		t.Errorf("the file replaces the values and keeps version and note: %v", b)
	}
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "-f", file, "image.tag=other", "--version", "0.0.8", "--note", "new note"); err != nil {
		t.Fatal(err)
	}
	b = lastBody(t, p, "PUT", patchPath)
	vc, _ := b["valuesContent"].(string)
	if !strings.Contains(vc, "tag: other") || !strings.Contains(vc, "memory: 2Gi") || b["version"] != "0.0.8" || b["note"] != "new note" {
		t.Errorf("k=v on top of the file: %v", b)
	}
	cliStdin = strings.NewReader("replicas: 3\n")
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	cliStdin = nil
	if b := lastBody(t, p, "PUT", patchPath); b["valuesContent"] != "replicas: 3\n" {
		t.Errorf("stdin: %v", b)
	}
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "-f", "/nonexistent/values.yaml"); exitCodeOf(err) != codeUsage {
		t.Error("a missing file is a usage error")
	}
	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("a: [1, 2\n"), 0o600)
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "-f", bad); exitCodeOf(err) != codeUsage {
		t.Errorf("invalid YAML: %v", err)
	}
	list := filepath.Join(dir, "list.yaml")
	_ = os.WriteFile(list, []byte("- a\n- b\n"), 0o600)
	if _, _, err := runCLI(t, e, "config", "set", "vision-station-01", "notebook", "-f", list); exitCodeOf(err) != codeUsage {
		t.Errorf("a list is not values: %v", err)
	}
}

func TestUnsetDeleteReset(t *testing.T) {
	p := platformWith(t, standardView())
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r4"))
	p.on("DELETE", patchPath, 200, writeOK(nbID, "r5"))
	p.on("DELETE", "/config/vision-station-01/overrides", 200, writeOK("", "r6"))
	p.on("PATCH", patchPath, 200, writeOK(nbID, "r4"))
	e, _ := testEnv(t, p, nil)

	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook", `image.tag`, `nodeSelector.teknoir\.org/node-role`, "--version"); err != nil {
		t.Fatal(err)
	}
	b := lastBody(t, p, "PATCH", patchPath)
	if got, want := jsonOf(t, b["ops"]), `[{"op":"unset","path":"image.tag"},{"op":"unset","path":"nodeSelector.teknoir\\.org/node-role"}]`; got != want || b["version"] != nil {
		t.Errorf("unset ops %s, body %v", got, b)
	}
	if _, ok := b["version"]; !ok {
		t.Error("--version must send version: null")
	}

	out, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook")
	if err != nil || len(p.seen("DELETE", patchPath)) != 1 || !strings.Contains(out, "override removed") {
		t.Errorf("whole override: %v %s", err, out)
	}
	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook", "--dry-run"); exitCodeOf(err) != codeUsage {
		t.Errorf("no dry run for the whole override: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "Deployment/default/whoami", "--image", "whoami"); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, lastBody(t, p, "PATCH", patchPath)["images"]); got != `{"whoami":null}` {
		t.Errorf("image unset: %s", got)
	}
	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "notebook", "--image", "x"); exitCodeOf(err) != codeUsage {
		t.Errorf("--image on a HelmChart: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "unset", "vision-station-01", "Deployment/default/whoami", "a.b"); exitCodeOf(err) != codeUsage {
		t.Errorf("keys on a workload: %v", err)
	}

	// delete writes null ops
	if _, _, err := runCLI(t, e, "config", "delete", "vision-station-01", "notebook", "resources.limits", "a"); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, lastBody(t, p, "PATCH", patchPath)["ops"]); got != `[{"op":"delete","path":"resources.limits"},{"op":"delete","path":"a"}]` {
		t.Errorf("delete ops: %s", got)
	}
	if _, _, err := runCLI(t, e, "config", "delete", "vision-station-01", "notebook"); exitCodeOf(err) != codeUsage {
		t.Errorf("delete without keys: %v", err)
	}

	// reset
	out, _, err = runCLI(t, e, "config", "reset", "vision-station-01", "--all")
	if err != nil || len(p.seen("DELETE", "/config/vision-station-01/overrides")) != 1 || !strings.Contains(out, "Reset to git: all overrides on vision-station-01 (revision r6)") {
		t.Errorf("reset --all: %v %s", err, out)
	}
	out, _, err = runCLI(t, e, "config", "reset", "vision-station-01", "notebook")
	if err != nil || !strings.Contains(out, "Reset to git: HelmChart/default/notebook") {
		t.Errorf("reset resource: %v %s", err, out)
	}
	for _, args := range [][]string{{"config", "reset", "vision-station-01"}, {"config", "reset", "vision-station-01", "notebook", "--all"}} {
		if _, _, err := runCLI(t, e, args...); exitCodeOf(err) != codeUsage {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestTryAndUntry(t *testing.T) {
	const repo = "https://github.com/teknoir/teknoir-demo-gitops"
	v := standardView()
	p := platformWith(t, v)
	appPath := "/config/vision-station-01/overrides/application"
	p.on("PUT", appPath, 200, `{"revision":"r7","dryRun":false,"overrides":{},"warnings":[]}`)
	p.on("DELETE", appPath, 200, `{"revision":"r8","dryRun":false,"overrides":{},"warnings":[]}`)
	e, _ := testEnv(t, p, nil)

	out, _, err := runCLI(t, e, "config", "try", "vision-station-01", "apps/mock-vision/", "feature-x", "--note", "testing")
	if err != nil {
		t.Fatal(err)
	}
	b := lastBody(t, p, "PUT", appPath)
	if b["repo_url"] != repo || b["path"] != "apps/mock-vision" || b["revision"] != "feature-x" || b["note"] != "testing" {
		t.Errorf("body %v", b)
	}
	if !strings.Contains(out, "Trying revision feature-x") {
		t.Errorf("out %s", out)
	}
	// whole repo, explicit --repo
	if _, _, err := runCLI(t, e, "config", "try", "vision-station-01", ".", "main", "--repo", "https://github.com/acme/other"); err != nil {
		t.Fatal(err)
	}
	if b := lastBody(t, p, "PUT", appPath); b["repo_url"] != "https://github.com/acme/other" || b["path"] != nil && b["path"] != "" {
		t.Errorf("whole repo: %v", b)
	}
	if _, _, err := runCLI(t, e, "config", "try", "vision-station-01", "apps/nope", "main"); exitCodeOf(err) != codeNotFound {
		t.Errorf("unknown app: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "try", "vision-station-01", ".", "main"); exitCodeOf(err) != codeUsage {
		t.Errorf("whole repo needs --repo: %v", err)
	}
	if _, _, err := runCLI(t, e, "config", "try", "vision-station-01", "apps/mock-vision"); exitCodeOf(err) != codeUsage {
		t.Errorf("missing revision: %v", err)
	}
	// the platform says the ref does not exist
	p.on("PUT", appPath, 400, `{"error":{"name":"InputError","message":"The revision \"nope\" does not exist in teknoir/teknoir-demo-gitops."}}`)
	if _, _, err := runCLI(t, e, "config", "try", "vision-station-01", "apps/mock-vision", "nope"); exitCodeOf(err) != codeRejected {
		t.Errorf("unknown ref: %v", err)
	}

	// untry
	if _, _, err := runCLI(t, e, "config", "untry", "vision-station-01", "apps/mock-vision"); exitCodeOf(err) != codeNotFound {
		t.Errorf("nothing tried: %v", err)
	}
	v.Overrides.Applications = []api.ApplicationOverride{{RepoURL: repo, Path: "apps/mock-vision", Revision: "feature-x"}}
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, v))
	out, _, err = runCLI(t, e, "config", "untry", "vision-station-01", "apps/mock-vision")
	if err != nil || !strings.Contains(out, "Back to the application's own revision") {
		t.Errorf("untry: %v %s", err, out)
	}
	if q := p.seen("DELETE", appPath)[0].Query; q != "path=apps%2Fmock-vision&repo_url=https%3A%2F%2Fgithub.com%2Fteknoir%2Fteknoir-demo-gitops" {
		t.Errorf("query %s", q)
	}
}

func TestDiff(t *testing.T) {
	v := standardView()
	v.Overrides = api.DeviceOverrides{Resources: map[string]api.ResourceOverride{nbID: {ValuesContent: "image:\n  tag: pr-123\n"}}, Applications: []api.ApplicationOverride{{RepoURL: "https://github.com/teknoir/x", Path: "apps/a", Revision: "feature-x"}}}
	p := platformWith(t, v)
	d := notebookDetail(nil)
	d.Base = map[string]any{"spec": map[string]any{"valuesContent": "image:\n  tag: main\n"}}
	d.Rendered = map[string]any{"spec": map[string]any{"valuesContent": "image:\n  tag: pr-123\n"}}
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	e, _ := testEnv(t, p, nil)

	out, _, err := runCLI(t, e, "config", "diff", "vision-station-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"# HelmChart/default/notebook", "--- git", "+++ effective", "-      tag: main", "+      tag: pr-123", "# application https://github.com/teknoir/x apps/a", "try revision: feature-x"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in\n%s", frag, out)
		}
	}
	out, _, err = runCLI(t, e, "config", "diff", "vision-station-01", "notebook", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Resources []struct {
			ID      string
			Diff    string
			Changed bool
		}
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil || len(j.Resources) != 1 || !j.Resources[0].Changed || !strings.Contains(j.Resources[0].Diff, "pr-123") {
		t.Errorf("json: %v\n%s", err, out)
	}
	// no overrides
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, standardView()))
	out, _, _ = runCLI(t, e, "config", "diff", "vision-station-01")
	if !strings.Contains(out, "No overrides on vision-station-01") {
		t.Errorf("out: %s", out)
	}
}

func TestLinksAndPromote(t *testing.T) {
	d := notebookDetail(nil)
	d.Contributions = []api.FleetConfigResource{{Layer: "fleet", Source: "test", File: "apps/mock-vision/notebook.yaml",
		Links: &api.ConfigLinks{Repo: "https://github.com/teknoir/teknoir-demo-gitops", View: "https://github.com/teknoir/teknoir-demo-gitops/blob/main/apps/mock-vision/notebook.yaml",
			Edit: "https://github.com/teknoir/teknoir-demo-gitops/edit/main/apps/mock-vision/notebook.yaml"}}}
	d.Resource.ChartLinks = &api.ChartLinks{Owner: "teknoir", Repo: "notebook-helm", ChartDir: "charts/notebook", Source: "https://github.com/teknoir/notebook-helm/tree/notebook-0.0.5/charts/notebook", Guessed: true}
	p := platformWith(t, standardView())
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, d))
	p.on("GET", "/config/vision-station-01/promote", 200, `{"yaml":"apiVersion: helm.cattle.io/v1\nkind: HelmChart\n","repo_url":"https://github.com/teknoir/teknoir-demo-gitops","branch":"main","path":"robots/vision-station-01/notebook.yaml","newFileUrl":"https://github.com/new","note":"Commit it, then reset the override."}`)
	e, _ := testEnv(t, p, nil)

	out, _, err := runCLI(t, e, "config", "links", "vision-station-01", "notebook")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"fleet test: apps/mock-vision/notebook.yaml", "view", "blob/main/apps/mock-vision/notebook.yaml", "edit ", "chart teknoir/notebook-helm (guessed by convention)", "source"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in\n%s", frag, out)
		}
	}
	out, _, err = runCLI(t, e, "config", "links", "vision-station-01", "notebook", "-o", "json")
	if err != nil || !strings.Contains(out, `"files"`) || !strings.Contains(out, `"chart"`) {
		t.Errorf("json: %v\n%s", err, out)
	}

	out, errs, err := runCLI(t, e, "config", "promote", "vision-station-01", "notebook", "--to", "fleet:test")
	if err != nil {
		t.Fatal(err)
	}
	if out != "apiVersion: helm.cattle.io/v1\nkind: HelmChart\n" {
		t.Errorf("stdout must be only the YAML: %q", out)
	}
	for _, frag := range []string{"Commit as: robots/vision-station-01/notebook.yaml in https://github.com/teknoir/teknoir-demo-gitops (branch main)", "New file:  https://github.com/new", "Commit it"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("missing %q in stderr:\n%s", frag, errs)
		}
	}
	if q := p.seen("GET", "/config/vision-station-01/promote")[0].Query; q != "id=HelmChart%2Fdefault%2Fnotebook&to=fleet%3Atest" {
		t.Errorf("query %s", q)
	}
}

func TestHelpTextsForAgents(t *testing.T) {
	e := defaultEnv
	cmd := newConfigCmd(e)
	for _, frag := range []string{"Exit codes:", "0  ok", "2  usage", "3  not found", "4  timeout", "5  rejected", "6  not logged in", "7  applied but unhealthy", "8  conflict", "vision-station-01", "HelmChart/default/notebook"} {
		if !strings.Contains(cmd.Long, frag) {
			t.Errorf("config --help lacks %q", frag)
		}
	}
	for _, sub := range cmd.Commands() {
		if sub.Short == "" || len(sub.Short) > 110 {
			t.Errorf("%s: one-line summary %q", sub.Name(), sub.Short)
		}
		if !strings.Contains(sub.Example, "vision-station-01") {
			t.Errorf("%s: examples with real-looking names are missing", sub.Name())
		}
	}
	for _, c := range []*cobra.Command{newRobotCmd(e), newExecCmd(e), newLogsCmd(e)} {
		if c.Short == "" || !strings.Contains(c.Example, "vision-station-01") {
			t.Errorf("help of %q lacks a summary or examples", c.Name())
		}
	}
	if c := newRobotsCmd(e); c.Short == "" || c.Example == "" {
		t.Error("robots help")
	}
}
