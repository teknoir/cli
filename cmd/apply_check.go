package cmd

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// This file is the pure part of `--wait=applied|ready`: the commands that probe a robot's k3s
// over WireGuard SSH, and the functions that judge their output. The wait loop (config_wait.go)
// runs them with a deviceExec; tests feed them canned output.

// kubectlOnDevice is how the robot's k3s is reached: the login user has NOPASSWD sudo and the
// kubeconfig is root-only. -n makes sudo fail instead of waiting for a password.
const kubectlOnDevice = "sudo -n k3s kubectl"

// readyStableFor is how long pods must stay Ready, with unchanged restart counts, for ready.
const readyStableFor = 20 * time.Second

const sectionMark = "@@tnctl:"

// parseSections splits probe output at the "@@tnctl:<name>" marker lines.
func parseSections(out string) map[string]string {
	secs := map[string]string{}
	var name string
	var b strings.Builder
	flush := func() {
		if name != "" {
			secs[name] = strings.TrimSpace(b.String())
		}
		b.Reset()
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, sectionMark) {
			flush()
			name = strings.TrimSpace(strings.TrimPrefix(line, sectionMark))
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	flush()
	return secs
}

type obj = map[string]any

// parseObj reads one kubectl -o json section. An empty section is "not found" (nil, ""); text
// that is not JSON is kubectl's error message.
func parseObj(s string) (o obj, errText string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ""
	}
	if err := json.Unmarshal([]byte(s), &o); err != nil || o == nil {
		return nil, firstNonEmptyLine(s)
	}
	return o, ""
}

func dig(m obj, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(obj)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func digStr(m obj, path ...string) string {
	s, _ := dig(m, path...).(string)
	return s
}

func digInt(m obj, path ...string) int64 {
	switch n := dig(m, path...).(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func digList(m obj, path ...string) []any {
	l, _ := dig(m, path...).([]any)
	return l
}

func digMap(m obj, path ...string) obj {
	o, _ := dig(m, path...).(obj)
	return o
}

// ---------- probes ----------

func helmProbeScript(ns, name, targetNS string, withWorkloads bool) string {
	var b strings.Builder
	k := kubectlOnDevice
	fmt.Fprintf(&b, "echo %s; %s -n %s get helmcharts.helm.cattle.io %s -o json --ignore-not-found 2>&1\n", shq(sectionMark+"chart"), k, shq(ns), shq(name))
	fmt.Fprintf(&b, "echo %s; %s -n %s get job %s -o json --ignore-not-found 2>&1\n", shq(sectionMark+"job"), k, shq(ns), shq("helm-install-"+name))
	if withWorkloads {
		fmt.Fprintf(&b, "echo %s; %s -n %s get deployments,statefulsets,daemonsets -o json 2>&1\n", shq(sectionMark+"workloads"), k, shq(targetNS))
		fmt.Fprintf(&b, "echo %s; %s -n %s get pods -o json 2>&1\n", shq(sectionMark+"pods"), k, shq(targetNS))
	}
	b.WriteString("true\n")
	return b.String()
}

func helmJobLogScript(ns, name string) string {
	return fmt.Sprintf("%s -n %s logs job/%s --tail=30 2>&1; true\n", kubectlOnDevice, shq(ns), shq("helm-install-"+name))
}

// kubectlResource is the kubectl resource name of a kind.
func kubectlResource(kind string) string { return strings.ToLower(kind) }

func workloadProbeScript(kind, ns, name string) string {
	var b strings.Builder
	k := kubectlOnDevice
	fmt.Fprintf(&b, "echo %s; %s -n %s get %s %s -o json --ignore-not-found 2>&1\n", shq(sectionMark+"obj"), k, shq(ns), shq(kubectlResource(kind)), shq(name))
	fmt.Fprintf(&b, "echo %s; %s -n %s get pods -o json 2>&1\n", shq(sectionMark+"pods"), k, shq(ns))
	b.WriteString("true\n")
	return b.String()
}

// ---------- results ----------

type checkState int

const (
	checkPending checkState = iota
	checkDone
	checkUnhealthy
	checkFatal // cannot ever succeed (for example no HelmChart support on the device)
)

type checkResult struct {
	state checkState
	msg   string
}

func pending(format string, a ...any) checkResult {
	return checkResult{checkPending, fmt.Sprintf(format, a...)}
}

func done(format string, a ...any) checkResult {
	return checkResult{checkDone, fmt.Sprintf(format, a...)}
}

func unhealthy(format string, a ...any) checkResult {
	return checkResult{checkUnhealthy, fmt.Sprintf(format, a...)}
}

// ---------- HelmChart ----------

// expectedHelm is what the device must have after the change.
type expectedHelm struct {
	values  obj    // parsed valuesContent; nil when not known
	version string // chart version; "" = not set
	// targetNS is where the release's workloads run.
	targetNS string
	// wantImage is the image the pods must run (from --image), if any.
	wantImage string
}

// helmBaseline is what the device had before the write.
type helmBaseline struct {
	present bool
	sig     string // valuesContent + version of the on-device HelmChart
	jobUID  string
}

type helmSnapshot struct {
	chart                 obj
	chartErr              string
	job                   obj
	workloads, pods       []obj
	workloadsErr, podsErr string
}

func helmSig(chart obj) string {
	if chart == nil {
		return ""
	}
	return digStr(chart, "spec", "valuesContent") + "\x00" + digStr(chart, "spec", "version")
}

func parseHelmSnapshot(out string) helmSnapshot {
	secs := parseSections(out)
	var s helmSnapshot
	s.chart, s.chartErr = parseObj(secs["chart"])
	s.job, _ = parseObj(secs["job"])
	if w, errText := parseObj(secs["workloads"]); w != nil {
		for _, it := range digList(w, "items") {
			if o, ok := it.(obj); ok {
				s.workloads = append(s.workloads, o)
			}
		}
	} else {
		s.workloadsErr = errText
	}
	if p, errText := parseObj(secs["pods"]); p != nil {
		for _, it := range digList(p, "items") {
			if o, ok := it.(obj); ok {
				s.pods = append(s.pods, o)
			}
		}
	} else {
		s.podsErr = errText
	}
	return s
}

func baselineFromSnapshot(s helmSnapshot) helmBaseline {
	return helmBaseline{present: s.chart != nil, sig: helmSig(s.chart), jobUID: digStr(s.job, "metadata", "uid")}
}

func parseValuesYAML(s string) obj {
	var m obj
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// sameYAML compares two decoded YAML documents; nil and empty maps are equal.
func sameYAML(a, b obj) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(normalizeYAML(a), normalizeYAML(b))
}

// normalizeYAML round-trips through JSON so ints and floats compare the same way.
func normalizeYAML(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// evalHelmApplied judges whether helm-controller applied the expected HelmChart.
func evalHelmApplied(exp expectedHelm, base helmBaseline, s helmSnapshot, jobName string) checkResult {
	if s.chart == nil {
		if strings.Contains(s.chartErr, "doesn't have a resource type") || strings.Contains(s.chartErr, "the server could not find the requested resource") {
			return checkResult{checkFatal, "the device has no HelmChart support (is this a k3s robot?)"}
		}
		if s.chartErr != "" {
			return pending("cannot read the HelmChart on the device: %s", s.chartErr)
		}
		return pending("the HelmChart is not on the device yet")
	}
	if exp.values != nil {
		have := parseValuesYAML(digStr(s.chart, "spec", "valuesContent"))
		if !sameYAML(exp.values, have) {
			return pending("the device does not have the published values yet")
		}
	}
	if exp.version != "" && digStr(s.chart, "spec", "version") != exp.version {
		return pending("the device has chart version %q, expected %q", digStr(s.chart, "spec", "version"), exp.version)
	}
	if s.job == nil {
		return pending("waiting for helm-controller to create Job %s", jobName)
	}
	uid := digStr(s.job, "metadata", "uid")
	changed := !base.present || helmSig(s.chart) != base.sig
	if changed && uid == base.jobUID && uid != "" {
		return pending("waiting for helm-controller to replace Job %s", jobName)
	}
	if digInt(s.job, "status", "succeeded") >= 1 {
		return done("helm Job %s succeeded", jobName)
	}
	if n := digInt(s.job, "status", "failed"); n > 0 {
		return unhealthy("helm Job %s failed %d time(s)", jobName, n)
	}
	return pending("helm Job %s is running", jobName)
}

// releaseWorkloads picks the workloads that belong to a Helm release.
func releaseWorkloads(all []obj, release string) []obj {
	var out []obj
	for _, w := range all {
		if digStr(w, "metadata", "annotations", "meta.helm.sh/release-name") == release {
			out = append(out, w)
		}
	}
	return out
}

// ---------- workloads and pods ----------

// podSpecOf returns the pod spec of a pod-template kind.
func podSpecOf(kind string, o obj) obj {
	switch kind {
	case "Pod":
		return digMap(o, "spec")
	case "CronJob":
		return digMap(o, "spec", "jobTemplate", "spec", "template", "spec")
	}
	return digMap(o, "spec", "template", "spec")
}

// containerImages maps container name -> image of a pod spec (init containers included).
func containerImages(spec obj) map[string]string {
	m := map[string]string{}
	for _, key := range []string{"containers", "initContainers"} {
		for _, c := range digList(spec, key) {
			if co, ok := c.(obj); ok {
				m[digStr(co, "name")] = digStr(co, "image")
			}
		}
	}
	return m
}

// expectedWorkload is what a raw workload must look like after the change.
type expectedWorkload struct {
	kind   string
	images map[string]string
	// wantImage: pods must run it (the override's image), if known.
	wantImage string
}

// evalWorkloadApplied judges whether the workload has the expected images and the controller
// has seen the latest generation.
func evalWorkloadApplied(exp expectedWorkload, o obj) checkResult {
	if o == nil {
		return pending("the %s is not on the device yet", exp.kind)
	}
	have := containerImages(podSpecOf(exp.kind, o))
	names := make([]string, 0, len(exp.images))
	for n := range exp.images {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if have[n] != exp.images[n] {
			return pending("container %s runs %q on the device, expected %q", n, have[n], exp.images[n])
		}
	}
	if gen, observed := digInt(o, "metadata", "generation"), digInt(o, "status", "observedGeneration"); observed != 0 && gen > observed {
		return pending("the controller has not seen generation %d yet (at %d)", gen, observed)
	}
	return done("%s has the published spec", exp.kind)
}

// rolloutDone reports whether a Deployment, StatefulSet or DaemonSet finished rolling out; msg says what is missing.
func rolloutDone(w obj) (bool, string) {
	kind := digStr(w, "kind")
	name := kind + "/" + digStr(w, "metadata", "name")
	if gen, observed := digInt(w, "metadata", "generation"), digInt(w, "status", "observedGeneration"); gen > observed {
		return false, name + ": waiting for the controller to see the new spec"
	}
	switch kind {
	case "DaemonSet":
		want := digInt(w, "status", "desiredNumberScheduled")
		if digInt(w, "status", "updatedNumberScheduled") < want || digInt(w, "status", "numberReady") < want {
			return false, fmt.Sprintf("%s: %d/%d ready", name, digInt(w, "status", "numberReady"), want)
		}
	case "StatefulSet", "Deployment":
		want := int64(1)
		if r := dig(w, "spec", "replicas"); r != nil {
			want = digInt(w, "spec", "replicas")
		}
		if digInt(w, "status", "updatedReplicas") < want || digInt(w, "status", "readyReplicas") < want {
			return false, fmt.Sprintf("%s: %d/%d ready, %d updated", name, digInt(w, "status", "readyReplicas"), want, digInt(w, "status", "updatedReplicas"))
		}
		if kind == "Deployment" && digInt(w, "status", "replicas") > want {
			return false, fmt.Sprintf("%s: %d old pods still terminating", name, digInt(w, "status", "replicas")-want)
		}
	}
	return true, ""
}

// failingReasons are container waiting reasons that will not fix themselves.
var failingReasons = map[string]bool{
	"CrashLoopBackOff": true, "ImagePullBackOff": true, "ErrImagePull": true,
	"InvalidImageName": true, "CreateContainerConfigError": true,
}

func podsOf(w obj, pods []obj) []obj {
	sel := digMap(w, "spec", "selector", "matchLabels")
	if len(sel) == 0 {
		return nil
	}
	var out []obj
	for _, p := range pods {
		labels := digMap(p, "metadata", "labels")
		match := true
		for k, v := range sel {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, p)
		}
	}
	return out
}

// readyResult is the verdict on pods, plus a signature of what stability is measured on.
type readyResult struct {
	checkResult
	// sig changes whenever a pod appears, goes or restarts; equal sigs for readyStableFor means stable.
	sig string
}

// evalPodsReady judges a set of workloads and their pods: rollout finished, no failing
// containers, every pod Running and Ready, on the wanted image.
func evalPodsReady(workloads []obj, pods []obj, wantImage string) readyResult {
	var sig []string
	var waiting []string
	for _, w := range workloads {
		name := digStr(w, "kind") + "/" + digStr(w, "metadata", "name")
		wp := podsOf(w, pods)
		for _, p := range wp {
			if digStr(p, "metadata", "deletionTimestamp") != "" {
				continue
			}
			if wantImage != "" && !podRuns(p, wantImage) {
				continue // an old pod: not ours to judge
			}
			for _, cs := range digList(p, "status", "containerStatuses") {
				c, _ := cs.(obj)
				if r := digStr(c, "state", "waiting", "reason"); failingReasons[r] {
					return readyResult{checkResult: unhealthy("pod %s: container %s is %s", digStr(p, "metadata", "name"), digStr(c, "name"), r)}
				}
			}
		}
		if ok, msg := rolloutDone(w); !ok {
			waiting = append(waiting, msg)
			continue
		}
		want := int64(1)
		if r := dig(w, "spec", "replicas"); r != nil {
			want = digInt(w, "spec", "replicas")
		}
		if digStr(w, "kind") == "DaemonSet" {
			want = digInt(w, "status", "desiredNumberScheduled")
		}
		var live int64
		for _, p := range wp {
			// Old pods that are going away, or run the old image, do not count; the
			// rollout check above and the number of live pods below catch a rollout that is not done.
			if digStr(p, "metadata", "deletionTimestamp") != "" || (wantImage != "" && !podRuns(p, wantImage)) {
				continue
			}
			if ok, why := podReady(p); !ok {
				waiting = append(waiting, "pod "+digStr(p, "metadata", "name")+": "+why)
				continue
			}
			live++
			sig = append(sig, fmt.Sprintf("%s:%d", digStr(p, "metadata", "name"), podRestarts(p)))
		}
		if live < want {
			waiting = append(waiting, fmt.Sprintf("%s: %d/%d pods ready", name, live, want))
		}
	}
	if len(waiting) > 0 {
		return readyResult{checkResult: pending("%s", strings.Join(waiting, "; "))}
	}
	sort.Strings(sig)
	return readyResult{checkResult: done("rollout complete, %d pod(s) ready", len(sig)), sig: strings.Join(sig, ",")}
}

func podRuns(p obj, image string) bool {
	for _, c := range digList(p, "spec", "containers") {
		if co, ok := c.(obj); ok && digStr(co, "image") == image {
			return true
		}
	}
	return false
}

func podReady(p obj) (bool, string) {
	if ph := digStr(p, "status", "phase"); ph != "Running" {
		return false, "phase " + ph
	}
	for _, cs := range digList(p, "status", "containerStatuses") {
		if c, _ := cs.(obj); c != nil && dig(c, "ready") != true {
			return false, "container " + digStr(c, "name") + " is not ready"
		}
	}
	return true, ""
}

func podRestarts(p obj) int64 {
	var n int64
	for _, cs := range digList(p, "status", "containerStatuses") {
		if c, _ := cs.(obj); c != nil {
			n += digInt(c, "restartCount")
		}
	}
	return n
}

// stability tracks how long the ready signature has stayed the same.
type stability struct {
	sig   string
	since time.Time
	set   bool
}

// observe records a ready signature at time now and returns how long it has been unchanged.
func (s *stability) observe(sig string, now time.Time) time.Duration {
	if !s.set || s.sig != sig {
		s.sig, s.since, s.set = sig, now, true
	}
	return now.Sub(s.since)
}

func (s *stability) reset() { s.set = false }
