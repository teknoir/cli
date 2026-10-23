package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

// ---------- fixtures ----------

// offImpact is the impact of turning the notebook off: it deletes its data and asks for a confirm.
func offImpact(confirm string) string {
	c := ""
	if confirm != "" {
		c = `,"confirm":"` + confirm + `"`
	}
	return `{"identity":"HelmChart-notebook-default","kind":"HelmChart",` +
		`"source":{"layer":"fleet","source":"test","url":"https://github.com/teknoir/teknoir-demo-gitops"},` +
		`"release":{"name":"notebook","targetNamespace":"notebook-ns","chart":"notebook","version":"0.0.5","repo":"https://charts.example"},` +
		`"deletesData":"yes","dataReasons":["its PersistentVolumeClaim is deleted"],` +
		`"dependents":["Deployment/default/whoami"],"keeps":["ConfigMap/default/settings"],` +
		`"warnings":["the robot's agent deletes it"],"toe":{"capable":true,"version":"toe_v4-4c366dae"}` + c + `}`
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// offWrite is the platform's answer to an off/on write of the notebook.
func offWrite(id string, dry, noop bool, impact string) string {
	imp := ""
	if impact != "" {
		imp = `,"impact":` + impact
	}
	np := ""
	if noop {
		np = `,"noop":true`
	}
	return `{"target":{"kind":"robot","name":"vision-station-01","device":"vision-station-01"},"id":"` + id +
		`","storage":"device-overrides","revision":"r2","dryRun":` + boolStr(dry) + np +
		`,"overrides":{},"warnings":[]` + imp + `}`
}

// offRequests counts the requests on a route.
func offRequests(t *testing.T, p *fakePlatform, method, path string) []rec {
	t.Helper()
	return p.seen(method, path)
}

// offView is the standard view plus the controller's state of the notebook override. The write
// answers revision r2, and the controller has evaluated it.
func offView(state string, off bool) *api.ConfigTargetView {
	v := standardView()
	v.Published = &api.PublishedInfo{Hash: "h1"}
	v.Overrides = api.DeviceOverrides{Revision: "r2", Resources: map[string]api.ResourceOverride{nbID: {Off: off}}}
	v.OverridesRevision = "r2"
	if state != "" {
		v.OverrideStatus = []api.OverrideStatus{{Target: nbID, State: state}}
	}
	if off || state == api.StateOff {
		for i := range v.Configuration.Effective {
			if v.Configuration.Effective[i].ID() == nbID {
				v.Configuration.Effective[i].Status = api.StatusOff
			}
		}
	}
	return v
}

// ---------- off: the write ----------

func TestConfigOffWrite(t *testing.T) {
	p := newFakePlatform(t)
	var n int
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		n++
		if strings.Contains(r.Query, "dryRun=1") {
			if r.Body != `{"off":true}` {
				t.Errorf("dry run body %s", r.Body)
			}
			return 200, offWrite(nbID, true, false, offImpact(""))
		}
		if r.Query != "id=HelmChart%2Fdefault%2Fnotebook" {
			t.Errorf("write query %q", r.Query)
		}
		if r.Body != `{"off":true}` {
			t.Errorf("write body %s", r.Body)
		}
		return 200, offWrite(nbID, false, false, offImpact(""))
	})
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "config", "off", "vision-station-01", nbID)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if n != 2 {
		t.Errorf("%d requests, want a dry run and the write", n)
	}
	for _, want := range []string{
		"Turned off HelmChart/default/notebook on vision-station-01: sent to the robot.\n",
		"  from fleet test (https://github.com/teknoir/teknoir-demo-gitops)\n",
		"  uninstalls release notebook in notebook-ns\n",
		"  deletes data: yes (its PersistentVolumeClaim is deleted)\n",
		"  uses it: Deployment/default/whoami\n",
		"  stays: ConfigMap/default/settings\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errs, "warning: the robot's agent deletes it") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigOffDryRun(t *testing.T) {
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		if !strings.Contains(r.Query, "dryRun=1") {
			t.Errorf("a write in a dry run: %q", r.Query)
		}
		return 200, offWrite(nbID, true, false, offImpact(""))
	})
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Would turn off HelmChart/default/notebook on vision-station-01.\n") || !strings.Contains(out, "uninstalls release notebook") {
		t.Errorf("stdout:\n%s", out)
	}
	if n := len(offRequests(t, p, "PATCH", patchPath)); n != 1 {
		t.Errorf("%d requests, want only the dry run", n)
	}
}

func TestConfigOffNeedsConfirm(t *testing.T) {
	const confirm = "delete notebook volumes"
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		if strings.Contains(r.Query, "dryRun=1") {
			return 200, offWrite(nbID, true, false, offImpact(confirm))
		}
		return 200, offWrite(nbID, false, false, offImpact(confirm))
	})
	e, _ := testEnv(t, p, nil)

	// without --confirm: the impact and the line asking for it, nothing written, exit 2
	_, errs, err := runCLI(t, e, "config", "off", "vision-station-01", nbID)
	if exitCodeOf(err) != codeUsage {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	want := "Turning off HelmChart/default/notebook deletes data on the robot (its PersistentVolumeClaim is deleted). Run it again with --confirm " + confirm + " to go ahead."
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error:\n%s", err)
	}
	if !strings.Contains(errs, "uninstalls release notebook") {
		t.Errorf("the impact is printed:\n%s", errs)
	}
	if n := len(offRequests(t, p, "PATCH", patchPath)); n != 1 {
		t.Fatalf("%d requests: the write must not happen without --confirm", n)
	}

	// a wrong --confirm asks again
	_, _, err = runCLI(t, e, "config", "off", "vision-station-01", nbID, "--confirm", "yes")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), confirm) {
		t.Errorf("wrong confirm: exit %d: %v", exitCodeOf(err), err)
	}
	if n := len(offRequests(t, p, "PATCH", patchPath)); n != 2 {
		t.Errorf("%d requests", n)
	}

	// the right one writes, with confirm= in the query
	out, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--confirm", confirm)
	if err != nil || !strings.HasPrefix(out, "Turned off HelmChart/default/notebook") {
		t.Fatalf("%v\n%s", err, out)
	}
	reqs := offRequests(t, p, "PATCH", patchPath)
	last := reqs[len(reqs)-1]
	if strings.Contains(last.Query, "dryRun") || !strings.Contains(last.Query, "confirm=delete+notebook+volumes") {
		t.Errorf("write query %q", last.Query)
	}
}

func TestConfigOffConfirmChangedInBetween(t *testing.T) {
	// the dry run needs no confirm, the write does (something changed in between): exit 8.
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		if strings.Contains(r.Query, "dryRun=1") {
			return 200, offWrite(nbID, true, false, offImpact(""))
		}
		return 409, `{"error":{"name":"ConflictError","message":"turning this off deletes data: send confirm=delete notebook volumes"}}`
	})
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID)
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), "run it again with --confirm delete notebook volumes") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigOffErrors(t *testing.T) {
	for status, want := range map[int]int{409: codeConflict, 404: codeNotFound, 400: codeRejected} {
		p := newFakePlatform(t)
		p.on("PATCH", patchPath, status, `{"error":{"name":"Error","message":"nope"}}`)
		e, _ := testEnv(t, p, nil)
		_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID)
		if exitCodeOf(err) != want || !strings.Contains(err.Error(), "nope") {
			t.Errorf("%d: exit %d: %v", status, exitCodeOf(err), err)
		}
	}
}

func TestConfigOffNoop(t *testing.T) {
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		if strings.Contains(r.Query, "dryRun=1") {
			return 200, offWrite(nbID, true, false, offImpact(""))
		}
		return 200, `{"id":"` + nbID + `","dryRun":false,"noop":true,"overrides":{},"warnings":["HelmChart/default/notebook is already off on vision-station-01"]}`
	})
	// The controller reports the stored off as invalid. A noop is waited for like any write, so
	// --wait=published ends with exit 5 instead of claiming success.
	sequence(t, p, offView(api.StateInvalid, true))
	e, _ := testEnv(t, p, nil)
	_, errs, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=published")
	if exitCodeOf(err) != codeRejected || !strings.Contains(err.Error(), "did not turn HelmChart/default/notebook off") {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if !strings.Contains(errs, "already off") {
		t.Errorf("stderr:\n%s", errs)
	}
	// without --wait a noop is exit 0 and says the robot already is in that state
	e2, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e2, "config", "off", "vision-station-01", nbID)
	if err != nil {
		t.Fatalf("noop without a wait: %v", err)
	}
	if !strings.HasPrefix(out, "Nothing changed: HelmChart/default/notebook is already off on vision-station-01.") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestConfigOffNoopNeedsNoConfirm(t *testing.T) {
	// The backend still sends a confirm text on a noop dry run: a repeat off must not exit 2.
	p := newFakePlatform(t)
	var writeQuery string
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		if strings.Contains(r.Query, "dryRun=1") {
			body := offWrite(nbID, true, true, offImpact("delete notebook volumes"))
			return 200, body
		}
		writeQuery = r.Query
		return 200, offWrite(nbID, false, true, offImpact(""))
	})
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(writeQuery, "confirm") {
		t.Errorf("a noop write must not carry a confirmation: %q", writeQuery)
	}
	if !strings.HasPrefix(out, "Nothing changed: ") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestConfigOffNoopAppNeedsNoConfirm(t *testing.T) {
	// An application route that would write nothing asks for no confirmation either.
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, standardView()))
	var writeQuery string
	p.onFunc("PATCH", appOffPath, func(r rec) (int, string) {
		if strings.Contains(r.Query, "dryRun=1") {
			return 200, `{"dryRun":true,"revision":"r2","overrides":{},"warnings":[],"ids":[],"confirm":"delete the app's volumes"}`
		}
		writeQuery = r.Query
		return 200, `{"dryRun":false,"revision":"r2","overrides":{},"warnings":[],"ids":[],"noop":true}`
	})
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(writeQuery, "confirm") {
		t.Errorf("a noop write must not carry a confirmation: %q", writeQuery)
	}
	if !strings.HasPrefix(out, "Nothing changed: every resource of apps/mock-vision already is off on vision-station-01.") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestConfigOffNote(t *testing.T) {
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		if r.Body != `{"note":"not used here","off":true}` {
			t.Errorf("body %s", r.Body)
		}
		return 200, offWrite(nbID, strings.Contains(r.Query, "dryRun=1"), false, offImpact(""))
	})
	e, _ := testEnv(t, p, nil)
	if _, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--note", "not used here"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigOffResourceAndApp(t *testing.T) {
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--app", "apps/mock-vision")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "not both") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if len(p.seen("PATCH", patchPath)) != 0 || len(p.seen("PATCH", appOffPath)) != 0 {
		t.Error("nothing may be sent")
	}
}

func TestConfigOffNeedsAConfirmationOnlyWithDataLoss(t *testing.T) {
	// deletesData no: no confirm, one dry run and one write
	p := newFakePlatform(t)
	impact := `{"identity":"Deployment-whoami-default","kind":"Deployment","deletesData":"no"}`
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite(nbID, strings.Contains(r.Query, "dryRun=1"), false, impact)
	})
	e, _ := testEnv(t, p, nil)
	if _, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID); err != nil {
		t.Fatal(err)
	}
	if n := len(p.seen("PATCH", patchPath)); n != 2 {
		t.Errorf("%d requests", n)
	}
}

// ---------- on ----------

func TestConfigOn(t *testing.T) {
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		if r.Body != `{"off":false}` {
			t.Errorf("on must send off:false explicitly, body %s", r.Body)
		}
		if strings.Contains(r.Query, "dryRun") {
			t.Errorf("on has no dry-run step: %q", r.Query)
		}
		return 200, offWrite(nbID, false, false, offImpact(""))
	})
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "on", "vision-station-01", nbID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Turned on HelmChart/default/notebook on vision-station-01: sent to the robot.\n") ||
		!strings.Contains(out, "  reinstalls from fleet test") {
		t.Errorf("stdout:\n%s", out)
	}
	if n := len(p.seen("PATCH", patchPath)); n != 1 {
		t.Errorf("%d requests", n)
	}
}

func TestConfigOffRefusesWaitReady(t *testing.T) {
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=ready")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "published or applied") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

// ---------- --app ----------

const appOffPath = "/config/vision-station-01/overrides/application/off"

// appOffAnswer is the answer of the application route: the ids are dash identities
// (<Kind>-<name>-<namespace>), as the platform sends them, not override keys.
func appOffAnswer(dry bool, confirm string) string {
	c := ""
	if confirm != "" {
		c = `,"confirm":"` + confirm + `"`
	}
	return `{"target":{"kind":"robot","name":"vision-station-01","device":"vision-station-01"},"dryRun":` + boolStr(dry) +
		`,"revision":"r2","overrides":{},"warnings":[],` +
		`"ids":["HelmChart-notebook-default"],` +
		`"skipped":[{"identity":"Deployment-whoami-default","reason":"the item is a robot app on this robot"}],` +
		`"impacts":[` + offImpact("") + `]` + c + `}`
}

func TestConfigOffApp(t *testing.T) {
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, standardView()))
	var writes int
	p.onFunc("PATCH", appOffPath, func(r rec) (int, string) {
		if !strings.Contains(r.Query, "repo_url=https%3A%2F%2Fgithub.com%2Fteknoir%2Fteknoir-demo-gitops") ||
			!strings.Contains(r.Query, "path=apps%2Fmock-vision") {
			t.Errorf("query %q", r.Query)
		}
		if !strings.Contains(r.Query, "dryRun=1") {
			writes++
		}
		return 200, appOffAnswer(strings.Contains(r.Query, "dryRun=1"), "")
	})
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision")
	if err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Errorf("%d writes, want one", writes)
	}
	for _, want := range []string{
		"Turned off HelmChart/default/notebook on vision-station-01: sent to the robot.\n",
		"  uninstalls release notebook in notebook-ns\n",
		"skipped: Deployment-whoami-default (the item is a robot app on this robot)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigOffAppAmbiguousPath(t *testing.T) {
	v := standardView()
	other := helmResource("gpu", "fleet", "test", "1.0.0", nil)
	other.Application = &api.GitOpsApplicationView{GitOpsApplicationRef: api.GitOpsApplicationRef{
		RepoURL: "https://github.com/teknoir/other-gitops", Path: "apps/mock-vision"}}
	v.Configuration.Resources = append(v.Configuration.Resources, other)
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, v))
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "several repositories") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if len(p.seen("PATCH", appOffPath)) != 0 {
		t.Error("nothing may be written")
	}
	// --repo picks one
	p.onFunc("PATCH", appOffPath, func(rec) (int, string) { return 200, appOffAnswer(false, "") })
	if _, _, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision", "--repo", "https://github.com/teknoir/other-gitops"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigOffAppWaitPublished(t *testing.T) {
	// The application route answers dash identities; the wait follows the override key they map to.
	p := newFakePlatform(t)
	sequence(t, p, offView(api.StatePending, true), offView(api.StateOff, true))
	p.onFunc("PATCH", appOffPath, func(r rec) (int, string) {
		return 200, appOffAnswer(strings.Contains(r.Query, "dryRun=1"), "")
	})
	e, _ := testEnv(t, p, nil)
	_, errs, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision", "--wait=published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(errs, "published after") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigOnAppWaitPublishedStaysOff(t *testing.T) {
	// The write says the revision changed, but the view still shows the item off: not success.
	p := newFakePlatform(t)
	sequence(t, p, offView(api.StateOff, true))
	p.onFunc("PATCH", appOffPath, func(r rec) (int, string) {
		return 200, appOffAnswer(false, "")
	})
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "on", "vision-station-01", "--app", "apps/mock-vision", "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigOffAppWaitAppliedChecksTheRobot(t *testing.T) {
	p := newFakePlatform(t)
	sequence(t, p, offView(api.StateOff, true))
	p.onFunc("PATCH", appOffPath, func(r rec) (int, string) {
		return 200, appOffAnswer(strings.Contains(r.Query, "dryRun=1"), "")
	})
	var probes int
	ex := &fakeExec{run: func(script string) (execResult, error) {
		probes++
		if !strings.Contains(script, "helmcharts.helm.cattle.io notebook") || !strings.Contains(script, "owner=helm,name=notebook") {
			t.Errorf("probe script: %s", script)
		}
		return execResult{Stdout: sectionMark + "obj\n\n" + sectionMark + "release\nNo resources found in notebook-ns namespace.\n" +
			sectionMark + "deletejob\n" + `{"metadata":{"name":"helm-delete-notebook"},"status":{"conditions":[{"type":"Complete","status":"True"}],"succeeded":1}}` + "\n"}, nil
	}}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision", "--wait")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if probes != 1 {
		t.Errorf("%d probes, want the one after publish", probes)
	}
	if !strings.Contains(errs, "helm-delete-notebook Job Complete") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigOffAppConfirm(t *testing.T) {
	const confirm = "delete the app's volumes"
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, standardView()))
	p.onFunc("PATCH", appOffPath, func(r rec) (int, string) {
		dry := strings.Contains(r.Query, "dryRun=1")
		if !dry && !strings.Contains(r.Query, "confirm=delete+the+app%27s+volumes") {
			t.Errorf("write query %q", r.Query)
		}
		return 200, appOffAnswer(dry, confirm)
	})
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), confirm) {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if n := len(p.seen("PATCH", appOffPath)); n != 1 {
		t.Fatalf("%d requests", n)
	}
	if _, _, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision", "--confirm", confirm); err != nil {
		t.Fatal(err)
	}
}

// ---------- --wait ----------

func offPlatform(t *testing.T, views ...*api.ConfigTargetView) *fakePlatform {
	t.Helper()
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite(nbID, strings.Contains(r.Query, "dryRun=1"), false, offImpact(""))
	})
	sequence(t, p, views...)
	return p
}

func TestConfigOffWaitPublished(t *testing.T) {
	p := offPlatform(t, offView(api.StatePending, true), offView(api.StatePending, true), offView(api.StateOff, true))
	e, clk := testEnv(t, p, nil)
	t0 := clk.now()
	out, errs, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(out, "Turned off") {
		t.Errorf("stdout:\n%s", out)
	}
	for _, frag := range []string{"waiting for the controller to turn HelmChart/default/notebook off (state: pending)", "published after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("stderr lacks %q:\n%s", frag, errs)
		}
	}
	if clk.now().Sub(t0) != 2*e.pollInterval {
		t.Errorf("waited %s", clk.now().Sub(t0))
	}
}

func TestConfigOffWaitInvalid(t *testing.T) {
	p := offPlatform(t, offView(api.StateInvalid, true))
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=published")
	if exitCodeOf(err) != codeRejected || !strings.Contains(err.Error(), "the controller did not turn HelmChart/default/notebook off: invalid") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	// not-applicable too, and a final state the controller reached for some other reason
	for _, state := range []string{api.StateNotApplicable, api.StateRedundant, api.StateActive, api.StateNoTarget} {
		p2 := offPlatform(t, offView(state, true))
		e2, _ := testEnv(t, p2, nil)
		_, _, err = runCLI(t, e2, "config", "off", "vision-station-01", nbID, "--wait=published")
		if exitCodeOf(err) != codeRejected || !strings.Contains(err.Error(), state) {
			t.Errorf("%s: exit %d: %v", state, exitCodeOf(err), err)
		}
	}
}

func TestConfigOffWaitTimeout(t *testing.T) {
	p := offPlatform(t, offView(api.StatePending, true))
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "timed out after 10s waiting for the change to be published") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigOffWaitApplied(t *testing.T) {
	// bare --wait is applied for off. prepare loads the view (device), then the state is published,
	// then the object is gone, the release is uninstalled and helm-delete's Job completed.
	p := offPlatform(t, offView(api.StateOff, true))
	probes := 0
	ex := &fakeExec{run: func(script string) (execResult, error) {
		probes++
		if !strings.Contains(script, "helm-delete-notebook") || !strings.Contains(script, "owner=helm,name=notebook") || !strings.Contains(script, "notebook-ns") {
			t.Errorf("probe script: %s", script)
		}
		if probes == 1 {
			return execResult{Stdout: sectionMark + "obj\nhelmchart.helm.cattle.io/notebook\n"}, nil
		}
		return execResult{Stdout: sectionMark + "obj\n\n" + sectionMark + "release\nNo resources found in notebook-ns namespace.\n" +
			sectionMark + "deletejob\n" + `{"metadata":{"name":"helm-delete-notebook"},"status":{"conditions":[{"type":"Complete","status":"True"}],"succeeded":1}}` + "\n"}, nil
	}}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	for _, frag := range []string{"is still on the robot", "helm-delete-notebook Job Complete", "applied after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("stderr lacks %q:\n%s", frag, errs)
		}
	}
	if calls := ex.callList(); len(calls) == 0 || calls[0] != "OPEN vision-station-01" || !ex.closed {
		t.Errorf("the shell is opened before the write and closed: %v closed=%v", calls, ex.closed)
	}
}

func TestConfigOffWaitAppliedDeleteJobFailed(t *testing.T) {
	// helm-controller keeps the HelmChart (Terminating) while its uninstall Job runs, so the probe
	// still sees the object and its release secret; the failed Job is the verdict.
	p := offPlatform(t, offView(api.StateOff, true))
	ex := &fakeExec{run: func(script string) (execResult, error) {
		if strings.Contains(script, "logs job/helm-delete-notebook") {
			return execResult{Stdout: "Error: uninstall: failed to delete release: context deadline exceeded\n"}, nil
		}
		return execResult{Stdout: sectionMark + "obj\nhelmchart.helm.cattle.io/notebook\n" +
			sectionMark + "release\nsecret/sh.helm.release.v1.notebook.v1\n" +
			sectionMark + "deletejob\n" + `{"metadata":{"name":"helm-delete-notebook"},"status":{"conditions":[{"type":"Failed","status":"True"}],"failed":1}}` + "\n"}, nil
	}}
	e, _ := testEnv(t, p, ex)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=applied")
	if exitCodeOf(err) != codeUnhealthy || !strings.Contains(err.Error(), "uninstall Job helm-delete-notebook failed") ||
		!strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigOffWaitAppliedOldFailedJobIsNotUnhealthy(t *testing.T) {
	// Everything is gone: a failed Job left over from an earlier attempt is not the verdict.
	p := offPlatform(t, offView(api.StateOff, true))
	ex := &fakeExec{run: func(script string) (execResult, error) {
		if strings.Contains(script, "logs job/") {
			t.Errorf("a leftover failed Job must not print a log tail: %s", script)
		}
		return execResult{Stdout: sectionMark + "obj\n\n" + sectionMark + "release\nNo resources found in notebook-ns namespace.\n" +
			sectionMark + "deletejob\n" + `{"metadata":{"name":"helm-delete-notebook"},"status":{"conditions":[{"type":"Failed","status":"True"}],"failed":1}}` + "\n"}, nil
	}}
	e, _ := testEnv(t, p, ex)
	if _, errs, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=applied"); err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
}

func TestConfigOffWaitAppliedNeedsTheDevice(t *testing.T) {
	p := offPlatform(t, offView(api.StateOff, true))
	e, _ := testEnv(t, p, nil) // no way to run commands on the robot
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot check the robot before the change") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	for _, r := range p.seen("PATCH", patchPath) {
		if !strings.Contains(r.Query, "dryRun=1") {
			t.Error("nothing may be written when the robot cannot be checked")
		}
	}
}

func TestConfigOnWaitPublished(t *testing.T) {
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite(nbID, false, false, offImpact(""))
	})
	on := offView(api.StateActive, false)
	on.Configuration.Effective[0].Status = "winner"
	sequence(t, p, offView(api.StateOff, true), on)
	e, _ := testEnv(t, p, nil)
	_, errs, err := runCLI(t, e, "config", "on", "vision-station-01", nbID, "--wait=published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(errs, "waiting for the controller to install HelmChart/default/notebook again (it is off)") || !strings.Contains(errs, "published after") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigOffWaitPublishedNeedsTheWriteRevision(t *testing.T) {
	// The view still reports off, but for the revision before the write: still waiting.
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite(nbID, strings.Contains(r.Query, "dryRun=1"), false, offImpact(""))
	})
	old := offView(api.StateOff, true)
	old.OverridesRevision = "r1"
	sequence(t, p, old)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "waiting for the controller to evaluate revision r2") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigOffAppUnmappableIdentity(t *testing.T) {
	// The answer names an item the view does not know: the wait cannot follow it (exit 1).
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, standardView()))
	p.onFunc("PATCH", appOffPath, func(r rec) (int, string) {
		return 200, `{"dryRun":false,"revision":"r2","overrides":{},"warnings":[],"ids":["HelmChart-gone-default"]}`
	})
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "off", "vision-station-01", "--app", "apps/mock-vision", "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot follow HelmChart-gone-default: it is not in the robot's configuration view") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigOnWaitPublishedNeedsTheWriteRevision(t *testing.T) {
	// The row says winner and the state is active, but for an older revision: still waiting.
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite(nbID, false, false, offImpact(""))
	})
	old := offView(api.StateActive, false)
	old.Configuration.Effective[0].Status = api.StatusWinner
	old.OverridesRevision = "r1"
	sequence(t, p, old)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "on", "vision-station-01", nbID, "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "waiting for the controller to evaluate revision r2") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigOnWaitPublishedNeedsTheRowStatus(t *testing.T) {
	// The revision is evaluated and the state is not off, but the row is not published yet.
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite(nbID, false, false, offImpact(""))
	})
	pending := offView(api.StateActive, false)
	pending.Configuration.Effective[0].Status = "pending"
	sequence(t, p, pending)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "on", "vision-station-01", nbID, "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "row status: pending") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

// onStampedPlatform serves an on write whose published item carries published; the robot's copy
// carries device. The robot's copy must carry the published stamp (testApplyStamp's value).
func onStampedPlatform(t *testing.T, published, device string) (*fakePlatform, *fakeExec) {
	t.Helper()
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite(nbID, false, false, offImpact(""))
	})
	on := offView(api.StateActive, false)
	on.Configuration.Effective[0].Status = api.StatusWinner
	sequence(t, p, on)
	rendered := map[string]any{
		"apiVersion": "helm.cattle.io/v1", "kind": "HelmChart",
		"metadata": map[string]any{"name": "notebook", "namespace": "default",
			"annotations": map[string]any{changedAtAnnotation: published}},
		"spec": map[string]any{"chart": "notebook", "version": "0.0.5", "targetNamespace": "notebook-ns",
			"valuesContent": "replicas: 1\n"},
	}
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, api.ConfigResourceDetail{ID: nbID, Rendered: rendered}))
	chart := map[string]any{
		"apiVersion": "helm.cattle.io/v1", "kind": "HelmChart",
		"metadata": map[string]any{"name": "notebook", "namespace": "default",
			"annotations": map[string]any{changedAtAnnotation: device}},
		"spec": map[string]any{"chart": "notebook", "version": "0.0.5", "targetNamespace": "notebook-ns",
			"valuesContent": "replicas: 1\n"},
	}
	chartJSON := mustJSON(chart)
	job := mustJSON(map[string]any{"kind": "Job", "metadata": map[string]any{"name": "helm-install-notebook", "uid": "u1"},
		"status": map[string]any{"succeeded": 1}})
	var baseline bool
	ex := &fakeExec{run: func(script string) (execResult, error) {
		switch {
		case strings.Contains(script, "get helmcharts.helm.cattle.io"):
			if !baseline { // the probe before the write: nothing is there yet
				baseline = true
				return execResult{Stdout: sectionMark + "chart\n" + sectionMark + "job\n"}, nil
			}
			return execResult{Stdout: sectionMark + "chart\n" + chartJSON + "\n" + sectionMark + "job\n" + job + "\n"}, nil
		case strings.Contains(script, "get secrets"):
			return execResult{Stdout: sectionMark + "release\nsecret/sh.helm.release.v1.notebook.v1\n"}, nil
		case strings.Contains(script, "get deployments,statefulsets,daemonsets"):
			return execResult{Stdout: sectionMark + "workloads\n" + `{"items":[]}` + "\n"}, nil
		case strings.Contains(script, "get pods"):
			return execResult{Stdout: sectionMark + "pods\n" + `{"items":[]}` + "\n"}, nil
		}
		return execResult{}, nil
	}}
	return p, ex
}

func TestConfigOnWaitAppliedUsesTheStamp(t *testing.T) {
	const stamp = "2026-10-23T10:00:00Z"
	p, ex := onStampedPlatform(t, stamp, stamp)
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "on", "vision-station-01", nbID, "--wait=applied")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(errs, "helm Job succeeded and release notebook is installed in namespace notebook-ns") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigOnWaitReadyUsesTheStamp(t *testing.T) {
	const stamp = "2026-10-23T10:00:00Z"
	p, ex := onStampedPlatform(t, stamp, stamp)
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "on", "vision-station-01", nbID, "--wait=ready")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(errs, "the release has no Deployment") || !strings.Contains(errs, "ready after") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigOnWaitAppliedWithAnotherStampWaits(t *testing.T) {
	// The robot's copy carries an older version: applied must not accept it.
	p, ex := onStampedPlatform(t, "2026-10-23T10:00:00Z", "2026-10-22T09:00:00Z")
	e, _ := testEnv(t, p, ex)
	_, _, err := runCLI(t, e, "config", "on", "vision-station-01", nbID, "--wait=applied", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "older version of the HelmChart") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestGoneForMatchesTheDashIdentityOnly(t *testing.T) {
	// Two HelmCharts called x in different namespaces: each keeps its own release namespace.
	impacts := []api.OffImpact{
		{Identity: "HelmChart-x-a", Kind: "HelmChart", Release: &api.OffRelease{Name: "x", TargetNamespace: "ns-a"}},
		{Identity: "HelmChart-x-b", Kind: "HelmChart", Release: &api.OffRelease{Name: "x", TargetNamespace: "ns-b"}},
	}
	for _, c := range []struct{ id, want string }{
		{"HelmChart/a/x", "ns-a"},
		{"HelmChart/b/x", "ns-b"},
	} {
		g := goneFor(c.id, impacts)
		if g == nil || g.targetNS != c.want {
			t.Errorf("%s: %+v, want targetNS %s", c.id, g, c.want)
		}
	}
	if g := goneFor("HelmChart/other/x", impacts); g == nil || g.targetNS != "other" {
		t.Errorf("an item without an impact keeps its own namespace: %+v", g)
	}
}

func TestConfigOffNamespaceShowsWhatIsInIt(t *testing.T) {
	impact := `{"identity":"Namespace-llm-ns-default","kind":"Namespace","deletesData":"yes",` +
		`"dataReasons":["the Namespace is deleted"],"namespaceItems":["Deployment llm","PersistentVolumeClaim data"]}`
	p := newFakePlatform(t)
	p.onFunc("PATCH", patchPath, func(r rec) (int, string) {
		return 200, offWrite("Namespace/default/llm-ns", strings.Contains(r.Query, "dryRun=1"), false, impact)
	})
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "off", "vision-station-01", "Namespace/default/llm-ns", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  in it: Deployment llm\n", "  in it: PersistentVolumeClaim data\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigWaitWithAnOffItem(t *testing.T) {
	// An off item is not on the robot: no level may wait for it, and applied/ready never probe it.
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, offView(api.StateOff, true)))
	ex := &fakeExec{run: func(script string) (execResult, error) {
		return execResult{}, errors.New("an off item must not be probed: " + script)
	}}
	e, _ := testEnv(t, p, ex)
	for _, level := range []string{"published", "applied", "ready"} {
		if _, _, err := runCLI(t, e, "config", "wait", "vision-station-01", "--for", level, "--timeout", "10s"); err != nil {
			t.Errorf("%s: %v", level, err)
		}
	}
	if calls := ex.callList(); len(calls) != 0 {
		t.Errorf("the robot must not be touched: %v", calls)
	}
}

func TestConfigOffJSONHasTheImpactAndTheWait(t *testing.T) {
	p := offPlatform(t, offView(api.StateOff, true))
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "off", "vision-station-01", nbID, "--wait=published", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Impact map[string]any
		Wait   map[string]any
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v.Impact["deletesData"] != "yes" || v.Wait["level"] != "published" || v.Wait["reached"] != true {
		t.Errorf("JSON:\n%s", out)
	}
}

// ---------- set -f never sends off ----------

func TestConfigGetShowsOff(t *testing.T) {
	v := standardView()
	v.Configuration.Effective[0].Status = api.StatusOff
	v.Overrides = api.DeviceOverrides{Revision: "r2", Resources: map[string]api.ResourceOverride{nbID: {
		ValuesContent: "replicas: 2\n", Off: true, SetBy: "anders"}}}
	v.OverrideStatus = []api.OverrideStatus{{Target: nbID, State: api.StateOff}}
	p := newFakePlatform(t)
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, v))
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "get", "vision-station-01")
	if err != nil || !strings.Contains(out, "off") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, _, err = runCLI(t, e, "config", "get", "vision-station-01", "--overrides")
	if err != nil || !strings.Contains(out, "off; values replicas") {
		t.Errorf("%v\n%s", err, out)
	}
	out, _, err = runCLI(t, e, "config", "get", "vision-station-01", "-o", "json")
	if err != nil || !strings.Contains(out, `"status": "off"`) || !strings.Contains(out, `"off": true`) {
		t.Errorf("%v\n%s", err, out)
	}
}
