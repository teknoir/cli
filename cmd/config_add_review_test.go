package cmd

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// ---------- HelmChart applied: the whole spec and the stamp ----------

func stampedChart(stamp string, spec map[string]any) string {
	ann := map[string]any{}
	if stamp != "" {
		ann[changedAtAnnotation] = stamp
	}
	spec["chart"] = "whoami"
	return mustJSON(map[string]any{"kind": "HelmChart", "metadata": map[string]any{"name": "whoami", "namespace": "kube-system", "annotations": ann}, "spec": spec})
}

func TestEvalHelmAppliedSeesChangesOutsideTheValues(t *testing.T) {
	snap := func(chart, job string) helmSnapshot {
		return parseHelmSnapshot(probeOutput(map[string]string{"chart": chart, "job": job}))
	}
	oldChart := stampedChart("t1", map[string]any{"set": map[string]any{"replicas": "1"}})
	newChart := stampedChart("t2", map[string]any{"set": map[string]any{"replicas": "3"}})
	base := baselineFromSnapshot(snap(oldChart, jobJSON("u1", 1, 0)))
	exp := expectedHelm{values: obj{}, stamp: "t2"}

	// --replace changed only spec.set: the robot still has the old chart and its old succeeded Job
	if r := evalHelmApplied(exp, base, snap(oldChart, jobJSON("u1", 1, 0)), "helm-install-whoami"); r.state != checkPending || !strings.Contains(r.msg, "older version") {
		t.Errorf("old chart: %+v", r)
	}
	// the new chart arrived (stamp and spec) but the old Job is still there
	if r := evalHelmApplied(exp, base, snap(newChart, jobJSON("u1", 1, 0)), "helm-install-whoami"); r.state != checkPending || !strings.Contains(r.msg, "replace Job") {
		t.Errorf("old job: %+v", r)
	}
	// the new Job ran
	if r := evalHelmApplied(exp, base, snap(newChart, jobJSON("u2", 1, 0)), "helm-install-whoami"); r.state != checkDone {
		t.Errorf("new job: %+v", r)
	}
	// without a stamp from the platform nothing about the annotation is required
	if r := evalHelmApplied(expectedHelm{values: obj{}}, base, snap(newChart, jobJSON("u2", 1, 0)), "helm-install-whoami"); r.state != checkDone {
		t.Errorf("no stamp: %+v", r)
	}
	// chart and repo are part of the signature too
	a := parseHelmSnapshot(probeOutput(map[string]string{"chart": stampedChart("t", map[string]any{"repo": "https://a"})})).chart
	b := parseHelmSnapshot(probeOutput(map[string]string{"chart": stampedChart("t", map[string]any{"repo": "https://b"})})).chart
	if helmSig(a) == helmSig(b) {
		t.Error("helmSig ignores spec.repo")
	}
}

func TestAddedCheckOfAHelmChartCarriesTheStamp(t *testing.T) {
	w := &waiter{}
	a := &addedItem{id: "HelmChart/kube-system/whoami", kind: "HelmChart", ns: "kube-system", name: "whoami",
		item: obj{"metadata": obj{"annotations": obj{changedAtAnnotation: "t9"}}, "spec": obj{"chart": "whoami"}}}
	c := w.addedCheck(a)
	if c.helm == nil || c.helm.stamp != "t9" {
		t.Errorf("%+v", c.helm)
	}
}

func TestHelmChartConfigAppliedSaysThatTheChartIsNotFollowed(t *testing.T) {
	c := &devCheck{id: "HelmChartConfig/kube-system/traefik", kind: "HelmChartConfig", name: "traefik", stamp: "t1"}
	o := obj{"metadata": obj{"annotations": obj{changedAtAnnotation: "t1"}}}
	if r := evalAddedApplied(c, o); r.state != checkDone || !strings.Contains(r.msg, "reinstalled by the robot afterwards") {
		t.Errorf("%+v", r)
	}
}

// ---------- duplicate documents ----------

func TestConfigAddRefusesTheSameItemTwiceBeforeAnyRequest(t *testing.T) {
	p := newFakePlatform(t)
	p.on("PUT", addPath, 200, addOK("ConfigMap/default/a", "create"))
	e, _ := testEnv(t, p, nil)
	in := "kind: ConfigMap\nmetadata: {name: a}\n---\nkind: ConfigMap\nmetadata: {name: b}\n---\nkind: ConfigMap\nmetadata: {name: a, namespace: default}\n"
	f := tempFile(t, "dup.yaml", in)
	for _, args := range [][]string{{"--dry-run"}, {}, {"--replace"}} {
		a := append([]string{"config", "add", "vision-station-01", "-f", f}, args...)
		_, _, err := runCLI(t, e, a...)
		if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "document 3") || !strings.Contains(err.Error(), "same item as document 1") || !strings.Contains(err.Error(), "ConfigMap/default/a") {
			t.Errorf("%v: exit %d: %v", args, exitCodeOf(err), err)
		}
	}
	if n := len(p.seen("PUT", addPath)); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
	// the same name in another namespace, or another kind, is another item
	ok := "kind: ConfigMap\nmetadata: {name: a}\n---\nkind: ConfigMap\nmetadata: {name: a, namespace: x}\n---\nkind: Secret\nmetadata: {name: a}\n"
	if _, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", tempFile(t, "ok.yaml", ok), "--dry-run"); err != nil {
		t.Errorf("%v", err)
	}
}

// ---------- document separators ----------

func TestSplitDocsSeparatorWithContentAndBOM(t *testing.T) {
	in := "kind: A\nmetadata: {name: a}\n--- kind: B\nmetadata: {name: b}\n---   !!map\nkind: C\nmetadata: {name: c}\n"
	docs := splitDocs(in)
	if len(docs) != 3 {
		t.Fatalf("%d docs: %+v", len(docs), docs)
	}
	for i, w := range []struct {
		line int
		id   string
	}{{1, "A/default/a"}, {3, "B/default/b"}, {5, "C/default/c"}} {
		d := docs[i]
		if d.line != w.line || d.kind+"/"+d.ns+"/"+d.name != w.id {
			t.Errorf("doc %d: line %d id %s/%s/%s", i+1, d.line, d.kind, d.ns, d.name)
		}
	}
	if !strings.HasPrefix(docs[1].text, "kind: B\n") || strings.Contains(docs[0].text, "kind: B") {
		t.Errorf("text %q / %q", docs[0].text, docs[1].text)
	}
	// --- is only a separator at the start of a line and followed by a break, space or tab
	if got := splitDocs("kind: A\nmetadata:\n  name: a\n  description: |\n    ---\n    text\n"); len(got) != 1 {
		t.Errorf("indented ---: %d docs", len(got))
	}
	if got := splitDocs("a: 1\n---foo: 2\n----\n"); len(got) != 1 {
		t.Errorf("not a separator: %d docs", len(got))
	}
	// a byte-order mark before the first ---
	got := splitDocs("\ufeff---\nkind: A\nmetadata: {name: a}\n---\nkind: B\nmetadata: {name: b}\n")
	if len(got) != 2 || got[0].line != 2 || got[0].kind != "A" || got[1].line != 5 || strings.Contains(got[0].text, "---") {
		t.Errorf("bom: %+v", got)
	}
	if got := splitDocs("\ufeffkind: A\nmetadata: {name: a}\n"); len(got) != 1 || got[0].kind != "A" {
		t.Errorf("bom without separator: %+v", got)
	}
	// CRLF keeps its breaks
	if got := splitDocs("kind: A\r\n--- kind: B\r\nmetadata: {name: b}\r\n"); len(got) != 2 || !strings.HasPrefix(got[1].text, "kind: B\r\n") || got[1].line != 2 {
		t.Errorf("crlf: %+v", got)
	}
}

// ---------- the --replace hint ----------

func TestConfigAddReplaceHintOnlyWhenALocalItemIsThere(t *testing.T) {
	f := tempFile(t, "w.yaml", whoamiYAML)
	// 409 and the item is not a local one: whatever the message says, --replace cannot help
	p := newFakePlatform(t)
	p.on("PUT", addPath, 409, `{"error":{"name":"ConflictError","message":"Deployment/default/whoami exists in the fleet config; remove it there"}}`)
	p.on("GET", addPath, 409, `{"error":{"name":"ConflictError","message":"not a local item"}}`)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "add", "vision-station-01", "-f", f)
	if exitCodeOf(err) != codeConflict || strings.Contains(err.Error(), "--replace") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	// a local item is there: the hint, whatever the wording of the message
	p = newFakePlatform(t)
	p.on("PUT", addPath, 409, `{"error":{"name":"ConflictError","message":"Deployment/default/whoami is taken"}}`)
	p.on("GET", addPath, 200, manifestBody(whoamiID, storedYAML))
	e, _ = testEnv(t, p, nil)
	_, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", f)
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), "use --replace to overwrite it") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	// with --replace the item is not looked up and no hint is given
	_, _, err = runCLI(t, e, "config", "add", "vision-station-01", "-f", f, "--replace")
	if exitCodeOf(err) != codeConflict || strings.Contains(err.Error(), "use --replace") || len(p.seen("GET", addPath)) != 1 {
		t.Errorf("exit %d: %v, %d lookups", exitCodeOf(err), err, len(p.seen("GET", addPath)))
	}
}

// ---------- edit: terminal, signals, concurrent change ----------

func TestConfigEditNeedsATerminal(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	stdinIsTerminal = func() bool { return false }
	defer func() { stdinIsTerminal = func() bool { return true } }()
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "needs a terminal") || !strings.Contains(err.Error(), "config add") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if ed.runs() != 0 || len(*puts) != 0 || len(p.seen("GET", addPath)) != 0 {
		t.Errorf("editor ran %d times, %d writes, %d reads", ed.runs(), len(*puts), len(p.seen("GET", addPath)))
	}
}

func TestConfigEditTerminatedRemovesTheTemporaryFile(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	ed.step(t, "term1", "1") // the editor's parent (tnctl) gets SIGTERM while the file is open
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if err == nil || !strings.Contains(err.Error(), "interrupted") || len(*puts) != 0 {
		t.Fatalf("%v, %d writes", err, len(*puts))
	}
	dir := strings.TrimSpace(ed.read(t, "tmpdir"))
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("the temporary directory %s is still there: %v", dir, statErr)
	}
}

func TestConfigEditRefusesToOverwriteAChangeMadeMeanwhile(t *testing.T) {
	p, puts := editPlatform(t)
	var gets atomic.Int32
	changed := strings.Replace(storedYAML, "replicas: 1", "replicas: 5", 1)
	p.onFunc("GET", addPath, func(rec) (int, string) {
		if gets.Add(1) == 1 {
			return 200, manifestBody(whoamiID, storedYAML)
		}
		return 200, manifestBody(whoamiID, changed) // someone else saved while the editor was open
	})
	ed := newEditor(t)
	ed.step(t, "sed1", "s/replicas: 1/replicas: 2/")
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), "changed while you were editing") || len(*puts) != 0 {
		t.Errorf("exit %d: %v, %d writes", exitCodeOf(err), err, len(*puts))
	}
}
