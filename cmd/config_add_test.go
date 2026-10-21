package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

const (
	addPath    = "/config/vision-station-01/local"
	whoamiID   = "Deployment/default/whoami"
	stampV1    = "2026-10-21T10:00:00Z"
	stampV2    = "2026-10-21T10:05:00Z"
	whoamiYAML = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: whoami\nspec:\n  replicas: 1\n  template:\n    spec:\n      containers:\n        - name: whoami\n          image: traefik/whoami:v1.11.0\n"
)

func tempFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// storedDeployment is the item as the platform stores it.
func storedDeployment(name, image, stamp string, replicas int) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": "default", "annotations": map[string]any{changedAtAnnotation: stamp, "teknoir.org/managed-by": "backstage"}},
		"spec":     map[string]any{"replicas": replicas, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": name, "image": image}}}}},
	}
}

type issue = api.LocalValidationIssue

// writeResult is a PUT answer.
func writeResult(id, action string, dry bool, item map[string]any, issues []issue, warnings ...string) string {
	verdict := api.VerdictValid
	for _, i := range issues {
		if i.Severity == "error" {
			verdict = api.VerdictInvalid
		}
	}
	if issues == nil {
		issues = []issue{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	r := api.LocalWriteResult{
		Target: api.ConfigTargetRef{Kind: "robot", Name: "vision-station-01", Device: "vision-station-01"},
		ID:     id, DryRun: dry, Action: action, Item: item, Warnings: warnings,
		Validation: api.LocalValidation{Verdict: verdict, Issues: issues},
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func addOK(id, action string) string { return writeResult(id, action, false, nil, nil) }

func TestSplitDocs(t *testing.T) {
	in := "# header\n---\nkind: A\nmetadata:\n  name: a\n---\n# only a comment\n---   # next\nkind: B\nmetadata: {name: b, namespace: ns1}\n--- \n\n---\nkind: C\n"
	docs := splitDocs(in)
	if len(docs) != 3 {
		t.Fatalf("%d docs: %+v", len(docs), docs)
	}
	want := []struct {
		line int
		id   string
	}{{3, "A/default/a"}, {9, "B/ns1/b"}, {14, "C/default/"}}
	for i, w := range want {
		d := docs[i]
		if d.line != w.line || d.index != i+1 || d.kind+"/"+d.ns+"/"+d.name != w.id {
			t.Errorf("doc %d: line %d id %s %s %s", i+1, d.line, d.kind, d.ns, d.name)
		}
		// the line numbers are exact: the first line of the text is the file's line d.line
		first := strings.SplitN(d.text, "\n", 2)[0]
		if got := strings.Split(in, "\n")[d.line-1]; got != first {
			t.Errorf("doc %d: text starts with %q but the file's line %d is %q", i+1, first, d.line, got)
		}
	}
	if got := splitDocs("  \n# nothing\n---\n---\n"); len(got) != 0 {
		t.Errorf("empty input: %+v", got)
	}
	if got := splitDocs("kind: A"); len(got) != 1 || got[0].line != 1 {
		t.Errorf("no separators: %+v", got)
	}
	// CRLF files
	if got := splitDocs("kind: A\r\n---\r\nkind: B\r\n"); len(got) != 2 || got[1].line != 3 {
		t.Errorf("crlf: %+v", got)
	}
}

func TestConfigAddSingleDocument(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, addOK(whoamiID, "create"))
	e, _ := testEnv(t, p, nil)
	f := tempFile(t, "whoami.yaml", whoamiYAML)
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != "added Deployment/default/whoami to vision-station-01\n" {
		t.Errorf("stdout %q", out)
	}
	reqs := p.seen("PUT", addPath)
	if len(reqs) != 1 || reqs[0].Query != "" {
		t.Fatalf("requests: %+v", reqs)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil || body["manifest"] != whoamiYAML || body["replace"] != nil {
		t.Errorf("body: %s", reqs[0].Body)
	}
}

func TestConfigAddMultiDocumentInOrder(t *testing.T) {
	p := newFakePlatform(t)
	var order []string
	var mu sync.Mutex
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		var b api.LocalWriteRequest
		_ = json.Unmarshal([]byte(r.Body), &b)
		_, _, n := docIdentity(b.Manifest)
		mu.Lock()
		order = append(order, n)
		mu.Unlock()
		return 200, addOK("ConfigMap/default/"+n, map[string]string{"cfg": "create", "again": "replace"}[n])
	})
	e, _ := testEnv(t, p, nil)
	in := "---\n# settings\nkind: ConfigMap\nmetadata:\n  name: cfg\n---\n---\nkind: ConfigMap\nmetadata:\n  name: again\n"
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "stack.yaml", in), "--replace")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if strings.Join(order, ",") != "cfg,again" {
		t.Errorf("order %v", order)
	}
	if out != "added ConfigMap/default/cfg to vision-station-01\nreplaced ConfigMap/default/again on vision-station-01\n" {
		t.Errorf("stdout %q", out)
	}
	for _, r := range p.seen("PUT", addPath) {
		if !strings.Contains(r.Body, `"replace":true`) {
			t.Errorf("--replace not sent: %s", r.Body)
		}
	}
}

func TestConfigAddFromStdin(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, addOK(whoamiID, "create"))
	e, _ := testEnv(t, p, nil)
	cliStdin = strings.NewReader(whoamiYAML)
	defer func() { cliStdin = nil }()
	if _, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", "-"); err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if got := p.seen("PUT", addPath); len(got) != 1 || !strings.Contains(got[0].Body, "traefik/whoami") {
		t.Errorf("%+v", got)
	}
}

func TestConfigAddUsageErrors(t *testing.T) {
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	for name, args := range map[string][]string{
		"no file":        {"config", "add", "vision-station-01"},
		"no target":      {"config", "add", "-f", "x.yaml"},
		"missing file":   {"config", "add", "vision-station-01", "-f", "/nonexistent/x.yaml"},
		"bad wait":       {"config", "add", "vision-station-01", "-f", tempFile(t, "a.yaml", whoamiYAML), "--wait=soon"},
		"dry and wait":   {"config", "add", "vision-station-01", "-f", tempFile(t, "b.yaml", whoamiYAML), "--dry-run", "--wait=applied"},
		"empty":          {"config", "add", "vision-station-01", "-f", tempFile(t, "c.yaml", "# nothing here\n---\n")},
		"misplaced wait": {"config", "add", "vision-station-01", "-f", tempFile(t, "d.yaml", whoamiYAML), "--wait", "applied"},
	} {
		if _, _, err := runCLI(t, e, args...); exitCodeOf(err) != codeUsage {
			t.Errorf("%s: exit %d: %v", name, exitCodeOf(err), err)
		}
	}
	if len(p.seen("PUT", addPath)) != 0 {
		t.Error("a usage error must not reach the platform")
	}
}

func TestConfigAddInvalidTranslatesPositionsToFileLines(t *testing.T) {
	p := newFakePlatform(t)
	n := 0
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		n++
		if n == 1 {
			return 200, addOK("ConfigMap/default/cfg", "create")
		}
		// document 2 starts at line 8 of the file; the platform counts from its own first line
		return 422, writeResult("Deployment/default/whoami", "create", false, nil, []issue{
			{Severity: "error", Message: "spec.replicas: Invalid type. Expected: integer, given: string", Path: "spec.replicas", Line: 3, Column: 13},
			{Severity: "error", Message: "Additional property foo is not allowed", Path: "spec.foo", Line: 5},
			{Severity: "warning", Message: "image has no tag", Path: "spec.template.spec.containers[0].image", Line: 9, Column: 17},
			{Severity: "error", Message: "metadata.name is required", Path: "metadata.name"},
		})
	})
	e, _ := testEnv(t, p, nil)
	in := "kind: ConfigMap\nmetadata:\n  name: cfg\ndata:\n  a: b\n---\n# the deployment\napiVersion: apps/v1\nkind: Deployment\nspec:\n  replicas: \"1\"\n"
	f := tempFile(t, "stack.yaml", in)
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f)
	if exitCodeOf(err) != codeRejected || err == nil {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	// document 2's first line is line 7 of the file ("# the deployment")
	for _, frag := range []string{
		f + ":9:13: error: spec.replicas: Invalid type. Expected: integer, given: string",
		f + ":11: error: Additional property foo is not allowed (spec.foo)",
		f + ":15:17: warning: image has no tag (spec.template.spec.containers[0].image)",
		f + ": error: document 2: metadata.name is required",
	} {
		if !strings.Contains(errs, frag) {
			t.Errorf("stderr lacks %q:\n%s", frag, errs)
		}
	}
	if !strings.Contains(err.Error(), "document 2 (line 7) is invalid (3 error(s) above)") || !strings.Contains(err.Error(), "written before it: ConfigMap/cfg") {
		t.Errorf("error: %v", err)
	}
	if out != "added ConfigMap/default/cfg to vision-station-01\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestConfigAddInvalidSingleDocumentIsExit5WithoutDocumentNumbers(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 422, writeResult("Deployment/default/whoami", "create", false, nil, []issue{{Severity: "error", Message: "bad", Line: 2, Column: 3}}))
	e, _ := testEnv(t, p, nil)
	f := tempFile(t, "w.yaml", whoamiYAML)
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f)
	if exitCodeOf(err) != codeRejected || out != "" || !strings.HasPrefix(errs, f+":2:3: error: bad\n") || !strings.Contains(err.Error(), "the manifest is invalid (1 error(s) above); it was not written") {
		t.Errorf("exit %d err %v\nout %q\nerrs %q", exitCodeOf(err), err, out, errs)
	}
	// the structured answer of a refused manifest carries the findings
	out, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "-o", "json")
	var v api.LocalWriteResult
	if exitCodeOf(err) != codeRejected || json.Unmarshal([]byte(out), &v) != nil || v.Validation.Verdict != "invalid" || len(v.Validation.Issues) != 1 {
		t.Errorf("exit %d\n%s", exitCodeOf(err), out)
	}
}

func TestConfigAddConflictIsExit8(t *testing.T) {
	p := newFakePlatform(t)
	msg := "Deployment/default/whoami already exists as a local item on vision-station-01"
	p.on("PUT", addPath, 409, `{"error":{"name":"ConflictError","message":"`+msg+`"}}`)
	p.on("GET", addPath, 200, manifestBody(whoamiID, storedYAML)) // a local item is there: --replace helps
	e, _ := testEnv(t, p, nil)
	f := tempFile(t, "w.yaml", whoamiYAML)
	_, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f)
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), msg) || !strings.Contains(err.Error(), "use --replace to overwrite it") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	// a platform or git item cannot be replaced, and the hint would mislead
	p.on("PUT", addPath, 409, `{"error":{"name":"ConflictError","message":"wg-agent is managed by the platform and cannot be replaced"}}`)
	_, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--replace")
	if exitCodeOf(err) != codeConflict || strings.Contains(err.Error(), "--replace") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	// other statuses keep their codes
	for status, want := range map[int]int{403: codeAuth, 404: codeNotFound, 429: codeRateLimit} {
		p.on("PUT", addPath, status, `{"error":{"message":"nope"}}`)
		if _, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f); exitCodeOf(err) != want {
			t.Errorf("%d: exit %d", status, exitCodeOf(err))
		}
	}
}

func TestConfigAddStopsAtTheFirstFailure(t *testing.T) {
	p := newFakePlatform(t)
	n := 0
	p.onFunc("PUT", addPath, func(rec) (int, string) {
		n++
		if n == 2 {
			return 409, `{"error":{"message":"ConfigMap/default/b already exists"}}`
		}
		return 200, addOK("ConfigMap/default/x", "create")
	})
	p.on("GET", addPath, 200, manifestBody("ConfigMap/default/b", "kind: ConfigMap\nmetadata: {name: b}\n"))
	e, _ := testEnv(t, p, nil)
	in := "kind: ConfigMap\nmetadata: {name: a}\n---\nkind: ConfigMap\nmetadata: {name: b}\n---\nkind: ConfigMap\nmetadata: {name: c}\n"
	out, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "m.yaml", in))
	if exitCodeOf(err) != codeConflict || len(p.seen("PUT", addPath)) != 2 {
		t.Fatalf("exit %d, %d requests: %v", exitCodeOf(err), len(p.seen("PUT", addPath)), err)
	}
	if !strings.Contains(err.Error(), "document 2 (line 4, ConfigMap/b): ConfigMap/default/b already exists") || !strings.Contains(err.Error(), "written before it: ConfigMap/a") || !strings.Contains(err.Error(), "--replace") {
		t.Errorf("error: %v", err)
	}
	if strings.Count(out, "added") != 1 {
		t.Errorf("stdout %q", out)
	}
}

func TestConfigAddUnchangedAndReplaced(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, addOK(whoamiID, "unchanged"))
	e, _ := testEnv(t, p, nil)
	f := tempFile(t, "w.yaml", whoamiYAML)
	out, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--replace")
	if err != nil || out != "unchanged Deployment/default/whoami on vision-station-01 (already as given)\n" {
		t.Errorf("%v %q", err, out)
	}
	p.on("PUT", addPath, 200, writeResult(whoamiID, "replace", false, nil, nil, "Replaces the fleet gpu-nvidia version (git) on this robot while it exists."))
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--replace")
	if err != nil || out != "replaced Deployment/default/whoami on vision-station-01\n" || !strings.Contains(errs, "warning: Replaces the fleet gpu-nvidia version (git)") {
		t.Errorf("%v %q\n%s", err, out, errs)
	}
}

func TestConfigAddDryRun(t *testing.T) {
	p := newFakePlatform(t)
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		if r.Query != "dryRun=1" {
			t.Errorf("query %q", r.Query)
		}
		return 200, writeResult(whoamiID, "create", true, nil, nil)
	})
	e, _ := testEnv(t, p, nil)
	f := tempFile(t, "w.yaml", whoamiYAML)
	out, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--dry-run")
	if err != nil || out != "would add Deployment/default/whoami to vision-station-01\n" {
		t.Errorf("%v %q", err, out)
	}
	p.on("PUT", addPath, 200, writeResult(whoamiID, "replace", true, nil, nil))
	out, _, _ = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--dry-run", "--replace")
	if out != "would replace Deployment/default/whoami on vision-station-01\n" {
		t.Errorf("%q", out)
	}
	p.on("PUT", addPath, 200, writeResult(whoamiID, "unchanged", true, nil, nil))
	out, _, _ = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--dry-run", "--replace")
	if out != "would leave Deployment/default/whoami unchanged on vision-station-01 (already as given)\n" {
		t.Errorf("%q", out)
	}
	// without --replace, a dry run that answers "replace" is the 409 a real write would get: exit 8
	p.on("PUT", addPath, 200, writeResult(whoamiID, "replace", true, nil, nil, "A local Deployment default/whoami already exists on vision-station-01; saving replaces it."))
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--dry-run")
	if exitCodeOf(err) != codeConflict || out != "would replace Deployment/default/whoami on vision-station-01\n" ||
		err.Error() != f+": an item with this identity exists; use --replace to replace it" || !strings.Contains(errs, "warning: A local Deployment") {
		t.Errorf("exit %d err %v\nout %q\n%s", exitCodeOf(err), err, out, errs)
	}
	// with --replace the same answer is fine
	out, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--dry-run", "--replace")
	if err != nil || out != "would replace Deployment/default/whoami on vision-station-01\n" {
		t.Errorf("%v %q", err, out)
	}
	// a dry run always answers 200; an invalid verdict is still a failure (exit 5)
	p.on("PUT", addPath, 200, writeResult(whoamiID, "create", true, nil, []issue{{Severity: "error", Message: "bad", Line: 1, Column: 1}}))
	_, errs, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--dry-run")
	if exitCodeOf(err) != codeRejected || !strings.Contains(errs, f+":1:1: error: bad") {
		t.Errorf("exit %d\n%s", exitCodeOf(err), errs)
	}
}

func TestConfigAddJSONOutput(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, addOK(whoamiID, "create"))
	e, _ := testEnv(t, p, nil)
	f := tempFile(t, "w.yaml", whoamiYAML)
	out, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "-o", "json")
	var one map[string]any
	if err != nil || json.Unmarshal([]byte(out), &one) != nil || one["id"] != whoamiID || one["action"] != "create" || one["wait"] != nil {
		t.Errorf("one document is the platform's object: %v\n%s", err, out)
	}
	two := whoamiYAML + "---\n" + strings.Replace(whoamiYAML, "whoami", "other", 1)
	out, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "two.yaml", two), "-o", "json")
	var list []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &list) != nil || len(list) != 2 {
		t.Errorf("several documents are a list: %v\n%s", err, out)
	}
	out, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "-o", "yaml")
	if err != nil || !strings.Contains(out, "action: create") || strings.Contains(out, "added ") {
		t.Errorf("yaml: %v\n%s", err, out)
	}
}

// ---------- --wait ----------

// addedView is the standard view plus whoami in the local layer, published at hash.
func addedView(hash string, withItem bool) *api.ConfigTargetView {
	v := standardView()
	v.Published = &api.PublishedInfo{Hash: hash}
	v.Configuration.Effective = v.Configuration.Effective[:1] // drop the git whoami
	v.Configuration.Resources = v.Configuration.Resources[:1]
	if withItem {
		d := deployResource("whoami", "whoami", "traefik/whoami:v1.11.0")
		d.Layer = api.LayerLocal
		d.Source = "backstage"
		v.Configuration.Resources = append(v.Configuration.Resources, d)
		v.Configuration.Effective = append(v.Configuration.Effective, d)
	}
	return v
}

func renderedDetail(stamp string, replicas int) string {
	d := api.ConfigResourceDetail{ID: whoamiID, Rendered: storedDeployment("whoami", "traefik/whoami:v1.11.0", stamp, replicas)}
	return mustJSON(d)
}

func waitAddPlatform(t *testing.T, action string, views ...*api.ConfigTargetView) *fakePlatform {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, writeResult(whoamiID, action, false, storedDeployment("whoami", "traefik/whoami:v1.11.0", stampV2, 1), nil))
	sequence(t, p, views...)
	p.on("GET", "/config/vision-station-01/resource", 200, renderedDetail(stampV2, 1))
	return p
}

func TestConfigAddWaitPublished(t *testing.T) {
	p := waitAddPlatform(t, "create", addedView("h1", false), addedView("h1", false), addedView("h2", false), addedView("h2", true))
	e, clk := testEnv(t, p, nil)
	t0 := clk.now()
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--wait=published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	// poll 1 is the baseline before the write; 2 has the old hash; 3 the new hash without the item; 4 done
	if d := clk.now().Sub(t0); d != 2*e.pollInterval {
		t.Errorf("waited %s", d)
	}
	for _, frag := range []string{"waiting for device-controller to publish a new config (still h1)", "waiting for Deployment/default/whoami to win in the robot's local layer (it is -)", "published after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("stderr lacks %q:\n%s", frag, errs)
		}
	}
	if out != "added Deployment/default/whoami to vision-station-01\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestConfigAddWaitPublishedNeedsTheNewVersion(t *testing.T) {
	// a replace: the local layer already has the item, the published copy carries the old stamp
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, writeResult(whoamiID, "replace", false, storedDeployment("whoami", "traefik/whoami:v1.11.0", stampV2, 1), nil))
	sequence(t, p, addedView("h1", true), addedView("h2", true))
	var mu sync.Mutex
	polls := 0
	p.onFunc("GET", "/config/vision-station-01/resource", func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		if polls < 3 {
			return 200, renderedDetail(stampV1, 1)
		}
		return 200, renderedDetail(stampV2, 1)
	})
	e, _ := testEnv(t, p, nil)
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--replace", "--wait=published", "-o", "json")
	if err != nil || polls != 3 || !strings.Contains(errs, "is in the local layer, but the published item is an older version") {
		t.Errorf("%v polls %d\n%s", err, polls, errs)
	}
}

func TestConfigAddWaitPublishedUnchangedNeedsNoNewHash(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, writeResult(whoamiID, "unchanged", false, storedDeployment("whoami", "traefik/whoami:v1.11.0", stampV2, 1), nil))
	polls := sequence(t, p, addedView("h1", true))
	p.on("GET", "/config/vision-station-01/resource", 200, renderedDetail(stampV2, 1))
	e, clk := testEnv(t, p, nil)
	t0 := clk.now()
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--replace", "--wait=published")
	if err != nil || *polls != 2 || clk.now() != t0 {
		t.Errorf("%v polls %d\n%s", err, *polls, errs)
	}
}

func TestConfigAddWaitPublishedTimeout(t *testing.T) {
	p := waitAddPlatform(t, "create", addedView("h1", false))
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--wait=published", "--timeout", "10s")
	if exitCodeOf(err) != codeTimeout || !strings.Contains(err.Error(), "waiting for the change to be published") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigAddWaitJSONHasWaitObject(t *testing.T) {
	p := waitAddPlatform(t, "create", addedView("h1", false), addedView("h2", true))
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--wait=published", "-o", "json")
	if err != nil || !strings.Contains(out, `"reached": true`) || !strings.Contains(out, `"level": "published"`) {
		t.Errorf("%v\n%s", err, out)
	}
}

// addDevice scripts the robot for the added items: obj answers the workload probes, chart and job
// the HelmChart probes, release the release secret listing.
type addDevice struct {
	mu      sync.Mutex
	n       int
	obj     func(n int) (objJSON, pods string)
	chart   func(n int) (chart, job string)
	release func(n int) string
}

func (d *addDevice) run(script string) (execResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	switch {
	case strings.Contains(script, "get secrets"):
		return execResult{Stdout: sectionMark + "release\n" + d.release(d.n) + "\n"}, nil
	case strings.Contains(script, "helmcharts.helm.cattle.io"):
		c, j := d.chart(d.n)
		return execResult{Stdout: probeOutput(map[string]string{"chart": c, "job": j})}, nil
	}
	o, pods := d.obj(d.n)
	return execResult{Stdout: probeOutput(map[string]string{"obj": o, "pods": pods})}, nil
}

func deployOnDevice(stamp string, gen, observed, replicas, updated, ready int) string {
	o := deployJSON("whoami", gen, observed, replicas, updated, ready, "traefik/whoami:v1.11.0")
	o["metadata"].(map[string]any)["annotations"] = map[string]any{changedAtAnnotation: stamp}
	return mustJSON(o)
}

func TestConfigAddWaitAppliedDeployment(t *testing.T) {
	p := waitAddPlatform(t, "replace", addedView("h1", true), addedView("h2", true))
	dev := &addDevice{obj: func(n int) (string, string) {
		switch n {
		case 1: // not on the robot yet
			return "", "{}"
		case 2: // the old version
			return deployOnDevice(stampV1, 1, 1, 1, 1, 1), "{}"
		case 3: // the new version, the controller is behind
			return deployOnDevice(stampV2, 2, 1, 1, 1, 1), "{}"
		}
		return deployOnDevice(stampV2, 2, 2, 1, 0, 0), "{}" // applied; not ready, and applied does not mind
	}}
	ex := &fakeExec{run: dev.run}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--replace", "--wait=applied")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	for _, frag := range []string{"the Deployment is not on the device yet", "the robot has an older version of Deployment/default/whoami", "the controller has not seen generation 2 yet (at 1)", "Deployment/default/whoami is on the robot", "applied after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("progress lacks %q:\n%s", frag, errs)
		}
	}
	calls := ex.callList()
	if calls[0] != "OPEN vision-station-01" || !ex.closed || !strings.Contains(calls[1], "get deployment whoami") || !strings.Contains(calls[1], "-n default") {
		t.Errorf("calls %v closed=%v", calls, ex.closed)
	}
}

func TestConfigAddWaitAppliedNeedsTheRobotBeforeWriting(t *testing.T) {
	p := waitAddPlatform(t, "create", addedView("h1", false))
	e, _ := testEnv(t, p, nil) // no device shell
	_, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot check the robot before the change") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if len(p.seen("PUT", addPath)) != 0 {
		t.Error("nothing may be written when the wait cannot work")
	}
}

func TestConfigAddWaitReadyDeployment(t *testing.T) {
	p := waitAddPlatform(t, "create", addedView("h1", false), addedView("h2", true))
	pod := func(ready bool) string {
		return listJSON(podMap(podOpt{name: "whoami-a", app: "whoami", image: "traefik/whoami:v1.11.0", ready: ready}))
	}
	dev := &addDevice{obj: func(n int) (string, string) {
		if n <= 2 { // rollout in progress
			return deployOnDevice(stampV2, 1, 1, 1, 1, 0), pod(false)
		}
		return deployOnDevice(stampV2, 1, 1, 1, 1, 1), pod(true)
	}}
	e, clk := testEnv(t, p, &fakeExec{run: dev.run})
	t0 := clk.now()
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--wait")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if clk.now().Sub(t0) < readyStableFor {
		t.Errorf("ready too early")
	}
	for _, frag := range []string{"Deployment/whoami: 0/1 ready", "rollout complete, 1 pod(s) ready, no restarts for 20s", "ready after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("progress lacks %q:\n%s", frag, errs)
		}
	}
}

func TestConfigAddWaitReadyCrashLoopIsExit7(t *testing.T) {
	p := waitAddPlatform(t, "create", addedView("h1", false), addedView("h2", true))
	dev := &addDevice{obj: func(int) (string, string) {
		return deployOnDevice(stampV2, 1, 1, 1, 1, 0), listJSON(podMap(podOpt{name: "whoami-a", app: "whoami", image: "traefik/whoami:v1.11.0", waiting: "CrashLoopBackOff"}))
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	_, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "w.yaml", whoamiYAML), "--wait=ready")
	if exitCodeOf(err) != codeUnhealthy || !strings.Contains(err.Error(), "CrashLoopBackOff") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

const jobID = "Job/default/migrate"

func jobPlatform(t *testing.T) *fakePlatform {
	item := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "migrate", "namespace": "default"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "m", "image": "busybox:1.36"}}}}}}
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, writeResult(jobID, "create", false, item, nil))
	v := addedView("h2", false)
	j := deployResource("migrate", "m", "busybox:1.36")
	j.Kind, j.Identity, j.Layer = "Job", "Job-migrate-default", api.LayerLocal
	v.Configuration.Effective = append(v.Configuration.Effective, j)
	v0 := addedView("h1", false)
	sequence(t, p, v0, v)
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, api.ConfigResourceDetail{ID: jobID, Rendered: item}))
	return p
}

func jobOnDevice(status map[string]any) string {
	return mustJSON(map[string]any{"kind": "Job", "metadata": map[string]any{"name": "migrate", "generation": 1},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "m", "image": "busybox:1.36"}}}}}, "status": status})
}

func TestConfigAddWaitReadyJob(t *testing.T) {
	p := jobPlatform(t)
	dev := &addDevice{obj: func(n int) (string, string) {
		if n < 3 {
			return jobOnDevice(map[string]any{"active": 1}), "{}"
		}
		return jobOnDevice(map[string]any{"succeeded": 1, "conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}), "{}"
	}}
	e, _ := testEnv(t, p, &fakeExec{run: dev.run})
	f := tempFile(t, "j.yaml", "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migrate\n")
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--wait=ready")
	if err != nil || !strings.Contains(errs, "job running (1 active, 0 of 1 succeeded, 0 failed)") || !strings.Contains(errs, "job complete") {
		t.Errorf("%v\n%s", err, errs)
	}
	// applied does not wait for the Job to finish
	dev2 := &addDevice{obj: func(int) (string, string) { return jobOnDevice(map[string]any{"active": 1}), "{}" }}
	p2 := jobPlatform(t)
	e2, _ := testEnv(t, p2, &fakeExec{run: dev2.run})
	if _, errs, err := runCLI(t, e2, "config", "add", "vision-station-01", "-f", f, "--wait=applied"); err != nil {
		t.Errorf("%v\n%s", err, errs)
	}
	// a failed Job is unhealthy
	dev3 := &addDevice{obj: func(int) (string, string) {
		return jobOnDevice(map[string]any{"failed": 4, "conditions": []any{map[string]any{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"}}}), "{}"
	}}
	e3, _ := testEnv(t, jobPlatform(t), &fakeExec{run: dev3.run})
	if _, _, err := runCLI(t, e3, "config", "add", "vision-station-01", "-f", f, "--wait=ready"); exitCodeOf(err) != codeUnhealthy {
		t.Errorf("failed job: exit %d: %v", exitCodeOf(err), err)
	}
}

const chartItemID = "HelmChart/default/local-llm"

func helmAddPlatform(t *testing.T) *fakePlatform {
	item := map[string]any{"apiVersion": "helm.cattle.io/v1", "kind": "HelmChart", "metadata": map[string]any{"name": "local-llm", "namespace": "default"},
		"spec": map[string]any{"chart": "llm", "version": "1.2.0", "targetNamespace": "llm-ns", "valuesContent": "replicas: 2\n"}}
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, writeResult(chartItemID, "create", false, item, nil))
	v := addedView("h2", false)
	c := helmResource("local-llm", api.LayerLocal, "backstage", "1.2.0", nil)
	v.Configuration.Effective = append(v.Configuration.Effective, c)
	sequence(t, p, addedView("h1", false), v)
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, api.ConfigResourceDetail{ID: chartItemID, Rendered: item}))
	return p
}

func llmChart(values string) string {
	return mustJSON(map[string]any{"kind": "HelmChart", "metadata": map[string]any{"name": "local-llm", "namespace": "default"},
		"spec": map[string]any{"chart": "llm", "version": "1.2.0", "targetNamespace": "llm-ns", "valuesContent": values}})
}

func llmJob(uid string, succeeded int) string {
	return mustJSON(map[string]any{"kind": "Job", "metadata": map[string]any{"name": "helm-install-local-llm", "uid": uid}, "status": map[string]any{"succeeded": succeeded}})
}

func TestConfigAddWaitAppliedHelmChartNeedsJobAndRelease(t *testing.T) {
	p := helmAddPlatform(t)
	dev := &addDevice{
		chart: func(n int) (string, string) {
			switch n {
			case 1: // the baseline before the write: nothing
				return "", ""
			case 2: // chart arrived, no Job yet
				return llmChart("replicas: 2\n"), ""
			}
			return llmChart("replicas: 2\n"), llmJob("u1", 1)
		},
		release: func(n int) string {
			if n <= 4 {
				return "No resources found in llm-ns namespace."
			}
			return "secret/sh.helm.release.v1.local-llm.v1"
		},
	}
	ex := &fakeExec{run: dev.run}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "h.yaml", "apiVersion: helm.cattle.io/v1\nkind: HelmChart\nmetadata:\n  name: local-llm\n"), "--wait=applied")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	for _, frag := range []string{"waiting for helm-controller to create Job helm-install-local-llm", "the helm Job succeeded, waiting for the release local-llm in namespace llm-ns", "helm Job succeeded and release local-llm is installed in namespace llm-ns", "applied after"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("progress lacks %q:\n%s", frag, errs)
		}
	}
	var sawRelease bool
	for _, c := range ex.callList() {
		if strings.Contains(c, "get secrets") {
			sawRelease = true
			if !strings.Contains(c, "-n llm-ns") || !strings.Contains(c, "owner=helm,name=local-llm") {
				t.Errorf("release probe: %s", c)
			}
		}
	}
	if !sawRelease {
		t.Error("no release probe")
	}
}

func TestConfigAddWaitAppliedHelmChartFailedJobIsExit7(t *testing.T) {
	p := helmAddPlatform(t)
	dev := &addDevice{
		chart: func(int) (string, string) {
			job := mustJSON(map[string]any{"kind": "Job", "metadata": map[string]any{"uid": "u1"}, "status": map[string]any{"failed": 1, "conditions": []any{map[string]any{"type": "Failed", "status": "True"}}}})
			return llmChart("replicas: 2\n"), job
		},
		release: func(int) string { return "" },
	}
	e, _ := testEnv(t, p, &fakeExec{run: func(s string) (execResult, error) {
		if strings.Contains(s, "logs job/") {
			return execResult{Stdout: "Error: chart not found\n"}, nil
		}
		return dev.run(s)
	}})
	_, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "h.yaml", "apiVersion: helm.cattle.io/v1\nkind: HelmChart\nmetadata:\n  name: local-llm\n"), "--wait=applied")
	if exitCodeOf(err) != codeUnhealthy || !strings.Contains(err.Error(), "chart not found") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigAddWaitConfigMapIsJustPresent(t *testing.T) {
	item := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "settings", "namespace": "default", "annotations": map[string]any{changedAtAnnotation: stampV2}}, "data": map[string]any{"a": "b"}}
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, writeResult("ConfigMap/default/settings", "create", false, item, nil))
	v := addedView("h2", false)
	cm := api.FleetConfigResource{Kind: "ConfigMap", Name: "settings", Namespace: "default", Layer: api.LayerLocal}
	v.Configuration.Effective = append(v.Configuration.Effective, cm)
	sequence(t, p, addedView("h1", false), v)
	p.on("GET", "/config/vision-station-01/resource", 200, jsonOf(t, api.ConfigResourceDetail{ID: "ConfigMap/default/settings", Rendered: item}))
	n := 0
	ex := &fakeExec{run: func(s string) (execResult, error) {
		n++
		if n == 1 {
			return execResult{Stdout: probeOutput(map[string]string{"obj": "", "pods": "{}"})}, nil
		}
		return execResult{Stdout: probeOutput(map[string]string{"obj": mustJSON(item), "pods": "{}"})}, nil
	}}
	e, _ := testEnv(t, p, ex)
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "c.yaml", "kind: ConfigMap\nmetadata:\n  name: settings\n"), "--wait=ready")
	if err != nil || !strings.Contains(errs, "ConfigMap applied") {
		t.Errorf("%v\n%s", err, errs)
	}
	if !strings.Contains(ex.callList()[1], "get configmap settings") {
		t.Errorf("calls %v", ex.callList())
	}
}

func TestKubectlResourceNames(t *testing.T) {
	for kind, want := range map[string]string{
		"Deployment": "deployment", "HelmChart": "helmcharts.helm.cattle.io", "HelmChartConfig": "helmchartconfigs.helm.cattle.io",
		"PersistentVolumeClaim": "persistentvolumeclaim", "HorizontalPodAutoscaler": "horizontalpodautoscaler",
	} {
		if got := kubectlResource(kind); got != want {
			t.Errorf("%s: %s", kind, got)
		}
	}
}

func TestFormatIssue(t *testing.T) {
	is := issue{Severity: "error", Message: "x", Path: "a.b", Line: 2, Column: 4}
	if got := formatIssue("f.yaml", 10, false, 1, is); got != "f.yaml:12:4: error: x (a.b)" {
		t.Error(got)
	}
	if got := formatIssue("f.yaml", 0, true, 3, issue{Message: "no position"}); got != "f.yaml: error: document 3: no position" {
		t.Error(got)
	}
	_ = fmt.Sprint
}

func TestConfigAddColumnsOnDocumentMarkerLine(t *testing.T) {
	// "--- kind: x" puts the document's first line after the marker: columns on that line shift by the marker
	p := newFakePlatform(t)
	p.on("PUT", addPath, 422, writeResult("ConfigMap/default/cfg", "create", true, nil, []issue{
		{Severity: "error", Message: "kind: bad", Line: 1, Column: 7},
		{Severity: "error", Message: "second line", Line: 2, Column: 3},
	}))
	e, _ := testEnv(t, p, nil)
	f := tempFile(t, "m.yaml", "# top\n---  kind: ConfigMap\nmetadata:\n  name: cfg\n")
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--dry-run")
	if exitCodeOf(err) != codeRejected {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	// "---  " is 5 characters; the first line is file line 2
	for _, frag := range []string{f + ":2:12: error: kind: bad", f + ":3:3: error: second line"} {
		if !strings.Contains(errs, frag) {
			t.Errorf("stderr lacks %q:\n%s", frag, errs)
		}
	}
	if d := splitDocs("--- kind: x\n"); len(d) != 1 || d[0].prefix != 4 {
		t.Errorf("prefix: %+v", d)
	}
	if d := splitDocs("--- # c\nkind: x\n"); len(d) != 1 || d[0].prefix != 0 {
		t.Errorf("comment marker: %+v", d)
	}
}

// ---------- Namespace documents ----------

const nsYAML = "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: llm-ns\n"

func deployIn(ns string) string {
	return "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: llm\n  namespace: " + ns + "\nspec:\n  replicas: 1\n"
}

// putOrder records the Kind/name of each PUT in the order the platform got them.
func putOrder(p *fakePlatform, order *[]string, mu *sync.Mutex) {
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		var b api.LocalWriteRequest
		_ = json.Unmarshal([]byte(r.Body), &b)
		k, _, n := docIdentity(b.Manifest)
		mu.Lock()
		*order = append(*order, k+"/"+n)
		mu.Unlock()
		return 200, addOK(k+"/default/"+n, "create")
	})
}

func TestNamespacesFirst(t *testing.T) {
	docs := splitDocs("kind: A\nmetadata: {name: a}\n---\nkind: Namespace\nmetadata: {name: n1}\n---\nkind: B\nmetadata: {name: b}\n---\nkind: Namespace\nmetadata: {name: n2}\n")
	got, moved := namespacesFirst(docs)
	var ids []string
	for _, d := range got {
		ids = append(ids, d.label())
	}
	if strings.Join(ids, ",") != "Namespace/n1,Namespace/n2,A/a,B/b" || strings.Join(moved, ",") != "n1,n2" {
		t.Errorf("order %v moved %v", ids, moved)
	}
	// the positions stay those of the file
	if got[0].index != 2 || got[0].line != 4 || got[2].index != 1 {
		t.Errorf("positions: %+v", got)
	}
	// already in front, only Namespaces, or none: nothing moves
	for _, in := range []string{
		"kind: Namespace\nmetadata: {name: n}\n---\nkind: A\nmetadata: {name: a}\n",
		"kind: Namespace\nmetadata: {name: n}\n---\nkind: Namespace\nmetadata: {name: m}\n",
		"kind: A\nmetadata: {name: a}\n---\nkind: B\nmetadata: {name: b}\n",
	} {
		if g, m := namespacesFirst(splitDocs(in)); len(m) != 0 || len(g) != 2 || g[0].index != 1 {
			t.Errorf("%q: %v %v", in, g, m)
		}
	}
}

func TestConfigAddSendsNamespacesFirstAndSaysSo(t *testing.T) {
	p := newFakePlatform(t)
	var order []string
	var mu sync.Mutex
	putOrder(p, &order, &mu)
	e, _ := testEnv(t, p, nil)
	in := deployIn("llm-ns") + "---\n" + nsYAML
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "stack.yaml", in))
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if strings.Join(order, ",") != "Namespace/llm-ns,Deployment/llm" {
		t.Errorf("order %v", order)
	}
	want := "sending Namespace llm-ns first, so it is stored before the resources in it (the platform decides the order on the robot)\nadded Namespace/llm-ns to vision-station-01\nadded Deployment/default/llm to vision-station-01\n"
	if out != want {
		t.Errorf("stdout %q", out)
	}
	// a file whose Namespace is in front already is sent as it is, silently
	order = nil
	out, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "ok.yaml", nsYAML+"---\n"+deployIn("llm-ns")))
	if err != nil || strings.Contains(out, "sending Namespace") || strings.Join(order, ",") != "Namespace/llm-ns,Deployment/llm" {
		t.Errorf("%v %v %q", err, order, out)
	}
	// with -o json the note goes to stderr and stdout stays parseable
	order = nil
	out, errs, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "j.yaml", in), "-o", "json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(errs, "note: sending Namespace llm-ns first") {
		t.Errorf("%v\nout %s\nerr %s", err, out, errs)
	}
}

func TestConfigAddNamespaceFirstKeepsFilePositionsInErrors(t *testing.T) {
	p := newFakePlatform(t)
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		if strings.Contains(r.Body, "kind: Namespace") {
			return 200, addOK("Namespace//llm-ns", "create")
		}
		return 200, writeResult("Deployment/llm-ns/llm", "create", false, nil, []issue{{Severity: "error", Message: "bad", Line: 2, Column: 3}})
	})
	e, _ := testEnv(t, p, nil)
	in := "---\n" + deployIn("llm-ns") + "---\n" + nsYAML
	f := tempFile(t, "s.yaml", in)
	_, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f)
	// the Deployment is document 1, line 2 of the file: line 2 of the document is line 3 of the file
	if exitCodeOf(err) != codeRejected || !strings.Contains(errs, f+":3:3: error: bad") ||
		!strings.Contains(err.Error(), "document 1 (line 2, Deployment/llm)") || !strings.Contains(err.Error(), "written before it: Namespace/llm-ns") {
		t.Errorf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
}

func TestConfigAddDryRunAcceptsTheNamespaceTheFileCreates(t *testing.T) {
	p := newFakePlatform(t)
	nsIssue := issue{Severity: "error", Code: api.IssueNamespaceUnknown, Message: "Namespace 'llm-ns' is not known to exist on this robot. Add a Namespace item named llm-ns first, then this one, or use namespace default.", Path: "metadata.namespace", Line: 5, Column: 13}
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		if strings.Contains(r.Body, "kind: Namespace") {
			return 200, writeResult("Namespace//llm-ns", "create", true, nil, nil)
		}
		if strings.Contains(r.Body, "namespace: other") {
			return 200, writeResult("Deployment/other/llm", "create", true, nil, []issue{{Severity: "error", Code: api.IssueNamespaceUnknown, Message: "Namespace 'other' is not known to exist on this robot. Add a Namespace item named other first, then this one, or use namespace default.", Path: "metadata.namespace", Line: 5, Column: 13}})
		}
		return 200, writeResult("Deployment/llm-ns/llm", "create", true, nil, []issue{nsIssue})
	})
	e, _ := testEnv(t, p, nil)
	// the Namespace of the file covers the Deployment
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-f", tempFile(t, "a.yaml", deployIn("llm-ns")+"---\n"+nsYAML))
	if err != nil || strings.Contains(errs, "error:") || !strings.Contains(errs, "note: namespace llm-ns does not exist on vision-station-01 yet") ||
		!strings.Contains(out, "would add Deployment/llm-ns/llm") {
		t.Fatalf("%v\n%s\n%s", err, out, errs)
	}
	// without the Namespace document the refusal stands
	_, errs, err = runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-f", tempFile(t, "b.yaml", deployIn("llm-ns")))
	if exitCodeOf(err) != codeRejected || !strings.Contains(errs, "Namespace 'llm-ns' is not known to exist") {
		t.Errorf("exit %d\n%s", exitCodeOf(err), errs)
	}
	// and so does the refusal of another namespace
	_, errs, err = runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-f", tempFile(t, "c.yaml", deployIn("other")+"---\n"+nsYAML))
	if exitCodeOf(err) != codeRejected || !strings.Contains(errs, "Namespace 'other' is not known to exist") {
		t.Errorf("exit %d\n%s", exitCodeOf(err), errs)
	}
	// -o json shows the verdict the command acted on
	out, _, err = runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-o", "json", "-f", tempFile(t, "d.yaml", deployIn("llm-ns")+"---\n"+nsYAML))
	if err != nil || strings.Contains(out, "is not known to exist") {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestConfigAddDryRunDropsTheBackendsNamespaceWarning(t *testing.T) {
	p := newFakePlatform(t)
	warn := "Nothing in this robot's configuration creates the namespace llm-ns. Add a Namespace item for it unless it already exists on the robot."
	other := "Nothing in this robot's configuration creates the namespace other. Add a Namespace item for it unless it already exists on the robot."
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		switch {
		case strings.Contains(r.Body, "kind: Namespace"):
			return 200, writeResult("Namespace//llm-ns", "create", true, nil, nil)
		case strings.Contains(r.Body, "namespace: other"):
			return 200, writeResult("Deployment/other/llm", "create", true, nil, nil, other)
		}
		return 200, writeResult("Deployment/llm-ns/llm", "create", true, nil, nil, warn)
	})
	e, _ := testEnv(t, p, nil)
	// the warning would not appear in a real run (the Namespace is sent first), so it is not shown
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-f", tempFile(t, "a.yaml", deployIn("llm-ns")+"---\n"+nsYAML))
	if err != nil || strings.Contains(errs, "creates the namespace") || !strings.Contains(errs, "note: namespace llm-ns") || !strings.Contains(out, "would add Deployment/llm-ns/llm") {
		t.Fatalf("%v\n%s\n%s", err, out, errs)
	}
	out, _, err = runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-o", "json", "-f", tempFile(t, "j.yaml", deployIn("llm-ns")+"---\n"+nsYAML))
	if err != nil || strings.Contains(out, "creates the namespace") {
		t.Errorf("%v\n%s", err, out)
	}
	// a namespace that the file does not create keeps its warning
	_, errs, err = runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-f", tempFile(t, "b.yaml", deployIn("other")+"---\n"+nsYAML))
	if err != nil || !strings.Contains(errs, "warning: "+other) {
		t.Errorf("%v\n%s", err, errs)
	}
}

func TestDropPendingNamespaceIssuesReadsCodeAndText(t *testing.T) {
	d := localDoc{kind: "ConfigMap", ns: "la-test", name: "settings"}
	created := map[string]bool{"la-test": true}
	run := func(is api.LocalValidationIssue) *api.LocalWriteResult {
		res := &api.LocalWriteResult{Validation: api.LocalValidation{Verdict: api.VerdictInvalid, Issues: []api.LocalValidationIssue{is}}}
		dropPendingNamespaceIssues(res, d, created)
		return res
	}
	const known = "Namespace 'la-test' is not known to exist on this robot. Add a Namespace item named la-test first, then this one, or use namespace default."
	for name, is := range map[string]api.LocalValidationIssue{
		"code":                             {Severity: "error", Path: "metadata.namespace", Code: api.IssueNamespaceUnknown, Message: "reworded"},
		"text of a platform without codes": {Severity: "error", Path: "metadata.namespace", Message: known},
		"older text":                       {Severity: "error", Path: "metadata.namespace", Message: "namespace la-test does not exist on this robot"},
	} {
		if res := run(is); len(res.Validation.Issues) != 0 || res.Validation.Verdict != api.VerdictValid {
			t.Errorf("%s: want dropped, got %+v", name, res.Validation)
		}
	}
	for name, is := range map[string]api.LocalValidationIssue{
		"a code-aware platform without the code": {Severity: "error", Path: "metadata.namespace", Code: "other", Message: known},
		"too late":                               {Severity: "error", Path: "metadata.namespace", Message: "Namespace 'la-test' is not known to exist on this robot, and a Namespace item added for it would come too late"},
		"another namespace":                      {Severity: "error", Path: "metadata.namespace", Message: strings.ReplaceAll(known, "la-test", "other")},
		"a namespace that starts with this one":  {Severity: "error", Path: "metadata.namespace", Message: strings.ReplaceAll(known, "la-test", "la-test-2")},
		"another field":                          {Severity: "error", Path: "metadata.name", Code: api.IssueNamespaceUnknown, Message: known},
	} {
		if res := run(is); len(res.Validation.Issues) != 1 || res.Validation.Verdict != api.VerdictInvalid {
			t.Errorf("%s: want kept, got %+v", name, res.Validation)
		}
	}
}

func TestConfigAddDryRunOnAPlatformWithoutIssueCodes(t *testing.T) {
	// backstage f5423d04: the namespace finding has no code, and an invalid manifest is "unchanged"
	p := newFakePlatform(t)
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		if strings.Contains(r.Body, "kind: Namespace") {
			return 200, writeResult("Namespace//llm-ns", "create", true, nil, nil)
		}
		return 200, writeResult("Deployment/llm-ns/llm", "unchanged", true, nil, []issue{{Severity: "error", Path: "metadata.namespace", Line: 5, Column: 14,
			Message: "Namespace 'llm-ns' is not known to exist on this robot. Add a Namespace item named llm-ns first, then this one, or use namespace default."}})
	})
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "config", "add", "vision-station-01", "--dry-run", "-f", tempFile(t, "a.yaml", deployIn("llm-ns")+"---\n"+nsYAML))
	if err != nil || strings.Contains(errs, "error:") || !strings.Contains(out, "would add Deployment/llm-ns/llm") || strings.Contains(out, "unchanged") {
		t.Errorf("%v\n%s\n%s", err, out, errs)
	}
}

func TestDropPendingNamespaceIssuesKeepsAValidUnchanged(t *testing.T) {
	// only a warning about the namespace: the verdict was valid and the item really is unchanged
	d := localDoc{kind: "ConfigMap", ns: "la", name: "settings"}
	res := &api.LocalWriteResult{Action: api.LocalActionUnchanged, Validation: api.LocalValidation{Verdict: api.VerdictValid},
		Warnings: []string{"Nothing in this robot's configuration creates the namespace la. Add a Namespace item for it unless it already exists on the robot."}}
	if got := dropPendingNamespaceIssues(res, d, map[string]bool{"la": true}); len(got) != 1 || len(res.Warnings) != 0 || res.Action != api.LocalActionUnchanged {
		t.Errorf("%v %+v", got, res)
	}
	// a finding about la-test is not one about la
	if namespaceMissing("Namespace 'la-test' is not known to exist on this robot.", "la") {
		t.Error("la matched la-test")
	}
	if !namespaceMissing("Namespace 'la' is not known to exist on this robot.", "la") || !namespaceMissing("namespace la does not exist", "la") {
		t.Error("la must match its own finding")
	}
}

func TestDropPendingNamespaceIssuesKeepsOtherFindings(t *testing.T) {
	d := localDoc{kind: "Deployment", ns: "llm-ns", name: "llm"}
	res := &api.LocalWriteResult{Validation: api.LocalValidation{Verdict: api.VerdictInvalid, Issues: []api.LocalValidationIssue{
		{Severity: "error", Path: "metadata.namespace", Message: "namespace llm-ns is not allowed for local items"},
	}}}
	if got := dropPendingNamespaceIssues(res, d, map[string]bool{"llm-ns": true}); len(got) != 0 || len(res.Validation.Issues) != 1 || res.Validation.Verdict != api.VerdictInvalid {
		t.Errorf("a refusal that is not about a missing namespace must stay: %v %+v", got, res.Validation)
	}
}

func TestConfigAddFailureNamesTheNamespaceThatWasWritten(t *testing.T) {
	// file order: Deployment, Namespace, Service; sending order: Namespace, Deployment, Service
	p := newFakePlatform(t)
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		if strings.Contains(r.Body, "kind: Deployment") {
			return 409, `{"error":{"message":"Deployment/llm-ns/llm already exists"}}`
		}
		return 200, addOK("Namespace//llm-ns", "create")
	})
	e, _ := testEnv(t, p, nil)
	in := deployIn("llm-ns") + "---\n" + nsYAML + "---\nkind: Service\nmetadata: {name: s, namespace: llm-ns}\n"
	_, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "s.yaml", in))
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), "document 1 (line 1, Deployment/llm)") || !strings.Contains(err.Error(), "written before it: Namespace/llm-ns") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}
