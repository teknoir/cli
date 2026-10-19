package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------- kubectl JSON builders ----------

func chartJSON(values, version string) string {
	spec := map[string]any{"chart": "notebook", "valuesContent": values}
	if version != "" {
		spec["version"] = version
	}
	return mustJSON(map[string]any{"kind": "HelmChart", "metadata": map[string]any{"name": "notebook", "namespace": "default"}, "spec": spec})
}

func jobJSON(uid string, succeeded, failed int) string {
	st := map[string]any{}
	if succeeded > 0 {
		st["succeeded"] = succeeded
	}
	if failed > 0 {
		st["failed"] = failed
	}
	return mustJSON(map[string]any{"kind": "Job", "metadata": map[string]any{"name": "helm-install-notebook", "uid": uid}, "status": st})
}

func deployJSON(name string, gen, observed int, replicas, updated, ready int, image string) map[string]any {
	return map[string]any{
		"kind": "Deployment",
		"metadata": map[string]any{
			"name": name, "generation": gen,
			"annotations": map[string]any{"meta.helm.sh/release-name": "notebook"},
		},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{"matchLabels": map[string]any{"app": name}},
			"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": name, "image": image}}}},
		},
		"status": map[string]any{"observedGeneration": observed, "replicas": replicas, "updatedReplicas": updated, "readyReplicas": ready},
	}
}

type podOpt struct {
	name, app, image, phase, waiting string
	ready                            bool
	restarts                         int
	terminating                      bool
}

func podMap(o podOpt) map[string]any {
	if o.phase == "" {
		o.phase = "Running"
	}
	cs := map[string]any{"name": o.app, "ready": o.ready, "restartCount": o.restarts, "state": map[string]any{"running": map[string]any{}}}
	if o.waiting != "" {
		cs["state"] = map[string]any{"waiting": map[string]any{"reason": o.waiting}}
	}
	md := map[string]any{"name": o.name, "labels": map[string]any{"app": o.app}}
	if o.terminating {
		md["deletionTimestamp"] = "2026-10-19T12:00:00Z"
	}
	return map[string]any{
		"metadata": md,
		"spec":     map[string]any{"containers": []any{map[string]any{"name": o.app, "image": o.image}}},
		"status":   map[string]any{"phase": o.phase, "containerStatuses": []any{cs}},
	}
}

func listJSON(items ...map[string]any) string {
	l := make([]any, len(items))
	for i, it := range items {
		l[i] = it
	}
	return mustJSON(map[string]any{"kind": "List", "items": l})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// probeOutput assembles the output of a probe script.
func probeOutput(sections map[string]string) string {
	var b strings.Builder
	for _, name := range []string{"chart", "job", "workloads", "pods", "obj"} {
		if v, ok := sections[name]; ok {
			fmt.Fprintf(&b, "%s%s\n%s\n", sectionMark, name, v)
		}
	}
	return b.String()
}

func TestParseSections(t *testing.T) {
	out := "noise\n@@tnctl:chart\n{\"a\":1}\n\n@@tnctl:job\n\n@@tnctl:pods\nError from server (Forbidden): pods is forbidden\n"
	s := parseSections(out)
	if s["chart"] != `{"a":1}` || s["job"] != "" || !strings.HasPrefix(s["pods"], "Error from server") {
		t.Errorf("got %#v", s)
	}
	if o, errText := parseObj(s["pods"]); o != nil || !strings.Contains(errText, "Forbidden") {
		t.Errorf("error text must survive: %v %q", o, errText)
	}
	if o, errText := parseObj(""); o != nil || errText != "" {
		t.Errorf("empty is not found: %v %q", o, errText)
	}
}

func TestProbeScriptsAreSafe(t *testing.T) {
	s := helmProbeScript("kube-system", "notebook", "default", true)
	for _, frag := range []string{
		"sudo -n k3s kubectl -n kube-system get helmcharts.helm.cattle.io notebook -o json --ignore-not-found",
		"get job helm-install-notebook",
		"-n default get deployments,statefulsets,daemonsets -o json",
		"-n default get pods -o json",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("missing %q in\n%s", frag, s)
		}
	}
	if strings.Contains(helmProbeScript("ns", "n", "ns", false), "pods") {
		t.Error("no pods without ready")
	}
	if got := workloadProbeScript("Deployment", "default", "whoami"); !strings.Contains(got, "get deployment whoami -o json") {
		t.Errorf("got %s", got)
	}
	if got := remoteShell("echo 'hi'"); got != `sh -c 'echo '\''hi'\'''` {
		t.Errorf("remoteShell = %s", got)
	}
}

// ---------- HelmChart applied ----------

func TestEvalHelmApplied(t *testing.T) {
	want := "image:\n  tag: pr-123\n"
	exp := expectedHelm{values: parseValuesYAML(want), version: "0.0.6"}
	job := "helm-install-notebook"
	snap := func(chart, jobs string) helmSnapshot {
		return parseHelmSnapshot(probeOutput(map[string]string{"chart": chart, "job": jobs}))
	}
	cases := []struct {
		name  string
		base  helmBaseline
		snap  helmSnapshot
		state checkState
		frag  string
	}{
		{"no chart yet", helmBaseline{}, snap("", ""), checkPending, "not on the device yet"},
		{"old values", helmBaseline{}, snap(chartJSON("image:\n  tag: main\n", "0.0.6"), jobJSON("u1", 1, 0)), checkPending, "published values"},
		{"old version", helmBaseline{}, snap(chartJSON(want, "0.0.5"), jobJSON("u1", 1, 0)), checkPending, "chart version"},
		{"arrived, old job still there", helmBaseline{present: true, sig: "old", jobUID: "u1"}, snap(chartJSON(want, "0.0.6"), jobJSON("u1", 1, 0)), checkPending, "replace Job"},
		{"arrived, no job yet", helmBaseline{present: true, sig: "old", jobUID: "u1"}, snap(chartJSON(want, "0.0.6"), ""), checkPending, "create Job"},
		{"new job running", helmBaseline{present: true, sig: "old", jobUID: "u1"}, snap(chartJSON(want, "0.0.6"), jobJSON("u2", 0, 0)), checkPending, "is running"},
		{"new job done", helmBaseline{present: true, sig: "old", jobUID: "u1"}, snap(chartJSON(want, "0.0.6"), jobJSON("u2", 1, 0)), checkDone, "succeeded"},
		{"new job failed", helmBaseline{present: true, sig: "old", jobUID: "u1"}, snap(chartJSON(want, "0.0.6"), jobJSON("u2", 0, 2)), checkUnhealthy, "failed 2 time"},
		{"retry then success", helmBaseline{present: true, sig: "old", jobUID: "u1"}, snap(chartJSON(want, "0.0.6"), jobJSON("u2", 1, 1)), checkDone, "succeeded"},
		{"nothing changed on the device: the existing job counts", helmBaseline{present: true, sig: helmSig(parseHelmSnapshot(probeOutput(map[string]string{"chart": chartJSON(want, "0.0.6")})).chart), jobUID: "u1"},
			snap(chartJSON(want, "0.0.6"), jobJSON("u1", 1, 0)), checkDone, "succeeded"},
		{"no baseline at all (config wait): a succeeded job is enough", helmBaseline{}, snap(chartJSON(want, "0.0.6"), jobJSON("u1", 1, 0)), checkDone, "succeeded"},
		{"no helm support", helmBaseline{}, snap("error: the server doesn't have a resource type \"helmcharts\"", ""), checkFatal, "no HelmChart support"},
		{"cannot read", helmBaseline{}, snap("Error from server (Forbidden): nope", ""), checkPending, "cannot read"},
	}
	for _, c := range cases {
		r := evalHelmApplied(exp, c.base, c.snap, job)
		if r.state != c.state || !strings.Contains(r.msg, c.frag) {
			t.Errorf("%s: got state %d %q, want state %d containing %q", c.name, r.state, r.msg, c.state, c.frag)
		}
	}
}

func TestEvalHelmAppliedValuesAreComparedAsYAML(t *testing.T) {
	// Another key order, comments and quoting are the same values.
	exp := expectedHelm{values: parseValuesYAML("b: 2\na:\n  x: \"1\"\n")}
	chart := chartJSON("# comment\na:\n  x: '1'\nb: 2\n", "")
	s := parseHelmSnapshot(probeOutput(map[string]string{"chart": chart, "job": jobJSON("u1", 1, 0)}))
	if r := evalHelmApplied(exp, helmBaseline{}, s, "helm-install-notebook"); r.state != checkDone {
		t.Errorf("got %+v", r)
	}
	// An unknown expectation (nil) accepts any values; a version that is not pinned is not compared.
	s = parseHelmSnapshot(probeOutput(map[string]string{"chart": chartJSON("x: 1", "9.9.9"), "job": jobJSON("u1", 1, 0)}))
	if r := evalHelmApplied(expectedHelm{}, helmBaseline{}, s, "helm-install-notebook"); r.state != checkDone {
		t.Errorf("got %+v", r)
	}
	// Removing every value override leaves empty values: nil, {} and "" are the same.
	s = parseHelmSnapshot(probeOutput(map[string]string{"chart": chartJSON("", ""), "job": jobJSON("u1", 1, 0)}))
	if r := evalHelmApplied(expectedHelm{values: map[string]any{}}, helmBaseline{}, s, "helm-install-notebook"); r.state != checkDone {
		t.Errorf("empty values: %+v", r)
	}
}

// ---------- workloads applied ----------

func TestEvalWorkloadApplied(t *testing.T) {
	exp := expectedWorkload{kind: "Deployment", images: map[string]string{"whoami": "traefik/whoami:v1.11.0"}}
	dep := func(gen, observed int, image string) obj { return deployJSON("whoami", gen, observed, 1, 1, 1, image) }
	cases := []struct {
		name  string
		o     obj
		state checkState
		frag  string
	}{
		{"missing", nil, checkPending, "not on the device"},
		{"old image", dep(2, 2, "traefik/whoami:v1.10.4"), checkPending, "runs \"traefik/whoami:v1.10.4\""},
		{"new image, controller behind", dep(3, 2, "traefik/whoami:v1.11.0"), checkPending, "generation 3"},
		{"applied", dep(3, 3, "traefik/whoami:v1.11.0"), checkDone, "published spec"},
	}
	for _, c := range cases {
		r := evalWorkloadApplied(exp, c.o)
		if r.state != c.state || !strings.Contains(r.msg, c.frag) {
			t.Errorf("%s: got %d %q", c.name, r.state, r.msg)
		}
	}
	// CronJob images sit deeper, Pod's are in spec.containers
	cron := obj{"spec": obj{"jobTemplate": obj{"spec": obj{"template": obj{"spec": obj{"containers": []any{obj{"name": "c", "image": "busybox:1.37"}}}}}}}}
	if r := evalWorkloadApplied(expectedWorkload{kind: "CronJob", images: map[string]string{"c": "busybox:1.37"}}, cron); r.state != checkDone {
		t.Errorf("cronjob: %+v", r)
	}
	pod := obj{"spec": obj{"containers": []any{obj{"name": "c", "image": "busybox:1.37"}}, "initContainers": []any{obj{"name": "init", "image": "alpine:3"}}}}
	if r := evalWorkloadApplied(expectedWorkload{kind: "Pod", images: map[string]string{"c": "busybox:1.37", "init": "alpine:3"}}, pod); r.state != checkDone {
		t.Errorf("pod: %+v", r)
	}
}

// ---------- ready ----------

func asObjs(items ...map[string]any) []obj {
	out := make([]obj, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

func TestEvalPodsReady(t *testing.T) {
	const img = "ghcr.io/teknoir/notebook:pr-123"
	const old = "ghcr.io/teknoir/notebook:main"
	dep := func(gen, observed, replicas, updated, ready int) []obj {
		return asObjs(deployJSON("notebook", gen, observed, replicas, updated, ready, img))
	}
	good := podMap(podOpt{name: "notebook-b", app: "notebook", image: img, ready: true})
	cases := []struct {
		name  string
		wl    []obj
		pods  []obj
		want  string
		state checkState
		frag  string
	}{
		{"rollout in progress", dep(2, 2, 1, 0, 1), asObjs(podMap(podOpt{name: "notebook-a", app: "notebook", image: old, ready: true})), img, checkPending, "0 updated"},
		{"controller behind", dep(3, 2, 1, 1, 1), asObjs(good), img, checkPending, "new spec"},
		{"new pod not ready", dep(2, 2, 1, 1, 1), asObjs(podMap(podOpt{name: "notebook-b", app: "notebook", image: img, phase: "Pending"})), img, checkPending, "phase Pending"},
		{"old pod terminating alongside", dep(2, 2, 1, 1, 1),
			asObjs(good, podMap(podOpt{name: "notebook-a", app: "notebook", image: old, ready: true, terminating: true})), img, checkDone, "1 pod"},
		{"old pod still serving", dep(2, 2, 1, 1, 1), asObjs(podMap(podOpt{name: "notebook-a", app: "notebook", image: old, ready: true})), img, checkPending, "0/1 pods ready"},
		{"image pull backoff", dep(2, 2, 1, 1, 0), asObjs(podMap(podOpt{name: "notebook-b", app: "notebook", image: img, waiting: "ImagePullBackOff"})), img, checkUnhealthy, "ImagePullBackOff"},
		{"crash loop", dep(2, 2, 1, 1, 0), asObjs(podMap(podOpt{name: "notebook-b", app: "notebook", image: img, waiting: "CrashLoopBackOff", restarts: 4})), img, checkUnhealthy, "CrashLoopBackOff"},
		{"an old pod crashing is not the new image's fault", dep(2, 2, 1, 1, 1),
			asObjs(good, podMap(podOpt{name: "notebook-a", app: "notebook", image: old, waiting: "CrashLoopBackOff"})), img, checkDone, "1 pod"},
		{"no image expected: any crash counts", dep(2, 2, 1, 1, 0), asObjs(podMap(podOpt{name: "notebook-b", app: "notebook", image: old, waiting: "CrashLoopBackOff"})), "", checkUnhealthy, "CrashLoopBackOff"},
		{"no pods yet", dep(2, 2, 1, 1, 1), nil, img, checkPending, "0/1 pods"},
		{"ready", dep(2, 2, 1, 1, 1), asObjs(good), img, checkDone, "rollout complete"},
		{"ready without image check", dep(2, 2, 1, 1, 1), asObjs(good), "", checkDone, "1 pod"},
		{"container not ready", dep(2, 2, 1, 1, 1), asObjs(podMap(podOpt{name: "notebook-b", app: "notebook", image: img})), img, checkPending, "not ready"},
	}
	for _, c := range cases {
		r := evalPodsReady(c.wl, c.pods, c.want)
		if r.state != c.state || !strings.Contains(r.msg, c.frag) {
			t.Errorf("%s: got state %d %q, want %d containing %q", c.name, r.state, r.msg, c.state, c.frag)
		}
	}
	// the signature changes with restarts and pods
	a := evalPodsReady(dep(2, 2, 1, 1, 1), asObjs(good), img)
	b := evalPodsReady(dep(2, 2, 1, 1, 1), asObjs(podMap(podOpt{name: "notebook-b", app: "notebook", image: img, ready: true, restarts: 1})), img)
	if a.sig == "" || a.sig == b.sig {
		t.Errorf("sig must reflect restarts: %q vs %q", a.sig, b.sig)
	}
}

func TestRolloutDone(t *testing.T) {
	ds := obj{"kind": "DaemonSet", "metadata": obj{"name": "agent", "generation": 1}, "status": obj{"observedGeneration": 1, "desiredNumberScheduled": 2, "updatedNumberScheduled": 2, "numberReady": 1}}
	if ok, msg := rolloutDone(ds); ok || !strings.Contains(msg, "1/2") {
		t.Errorf("daemonset: %v %q", ok, msg)
	}
	ds["status"].(obj)["numberReady"] = 2
	if ok, _ := rolloutDone(ds); !ok {
		t.Error("daemonset ready")
	}
	sts := obj{"kind": "StatefulSet", "metadata": obj{"name": "db", "generation": 1}, "spec": obj{"replicas": 3}, "status": obj{"observedGeneration": 1, "updatedReplicas": 3, "readyReplicas": 2}}
	if ok, msg := rolloutDone(sts); ok || !strings.Contains(msg, "2/3") {
		t.Errorf("statefulset: %v %q", ok, msg)
	}
	// 2 replicas where 1 is wanted: an old pod still terminating
	dep := deployJSON("x", 1, 1, 1, 1, 1, "i")
	dep["status"].(map[string]any)["replicas"] = 2
	if ok, msg := rolloutDone(dep); ok || !strings.Contains(msg, "old pods") {
		t.Errorf("deployment: %v %q", ok, msg)
	}
	// replicas: 0 is a finished rollout of nothing
	zero := deployJSON("x", 1, 1, 0, 0, 0, "i")
	if ok, _ := rolloutDone(zero); !ok {
		t.Error("scaled to zero")
	}
}

func TestStability(t *testing.T) {
	var s stability
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if d := s.observe("a:0", t0); d != 0 {
		t.Errorf("first = %s", d)
	}
	if d := s.observe("a:0", t0.Add(15*time.Second)); d != 15*time.Second {
		t.Errorf("same sig = %s", d)
	}
	if d := s.observe("a:1", t0.Add(16*time.Second)); d != 0 {
		t.Errorf("a restart starts over, got %s", d)
	}
	if d := s.observe("a:1", t0.Add(40*time.Second)); d < readyStableFor {
		t.Errorf("stable = %s", d)
	}
	s.reset()
	if d := s.observe("a:1", t0.Add(50*time.Second)); d != 0 {
		t.Errorf("after reset = %s", d)
	}
}

func TestReleaseWorkloads(t *testing.T) {
	a := deployJSON("a", 1, 1, 1, 1, 1, "i")
	b := deployJSON("b", 1, 1, 1, 1, 1, "i")
	b["metadata"].(map[string]any)["annotations"] = map[string]any{"meta.helm.sh/release-name": "other"}
	got := releaseWorkloads(asObjs(a, b), "notebook")
	if len(got) != 1 || digStr(got[0], "metadata", "name") != "a" {
		t.Errorf("got %v", got)
	}
}
