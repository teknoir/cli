package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

// waitLevel is how far --wait follows a change.
type waitLevel string

const (
	waitNone      waitLevel = ""
	waitPublished waitLevel = "published"
	waitApplied   waitLevel = "applied"
	waitReady     waitLevel = "ready"
)

// parseWaitLevel reads the value of --wait. A bare --wait is "ready".
func parseWaitLevel(s string) (waitLevel, error) {
	switch waitLevel(strings.ToLower(s)) {
	case waitNone, waitPublished, waitApplied, waitReady:
		return waitLevel(strings.ToLower(s)), nil
	}
	return waitNone, usageErr("--wait must be published, applied or ready, not %q (write --wait=applied; a bare --wait means ready)", s)
}

const defaultWaitTimeout = 5 * time.Minute

// waitFlags are --wait and --timeout, shared by every command that writes.
type waitFlags struct {
	wait    string
	timeout time.Duration
}

func (f *waitFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&f.wait, "wait", "", "wait until the change is published, applied or ready on the robot (a bare --wait means ready)")
	c.Flags().Lookup("wait").NoOptDefVal = string(waitReady)
	c.Flags().DurationVar(&f.timeout, "timeout", defaultWaitTimeout, "how long --wait may take (exit code 4 when it runs out)")
}

func (f *waitFlags) level() (waitLevel, error) { return parseWaitLevel(f.wait) }

// misplaced catches "--wait applied": a level written after a bare --wait is taken as a
// positional argument (a key, a resource), which would be written or looked up as such.
func (f *waitFlags) misplaced(args []string) error {
	if f.wait != string(waitReady) {
		return nil
	}
	for _, a := range args {
		if h := lookupHint(a); h != "" {
			return usageErr("%q looks like a --wait level, not an argument%s", a, h)
		}
	}
	return nil
}

// writeInfo describes a write for the waiter.
type writeInfo struct {
	revision string
	storage  string
	// statusTargets are override targets whose state must become final: resource ids, app: keys.
	statusTargets []string
	// removed: the write removed overrides, so their status entries disappear.
	removed bool
	// resources are the resource ids to verify on the device (applied, ready).
	resources []string
	// resourcesAfter, when set, recomputes resources from the view the platform published: a
	// change of an application's revision can add, rename or remove its resources.
	resourcesAfter func(v *api.ConfigTargetView) []string
	// versionCleared: the write removed the chart version override.
	versionCleared bool
	// image: resource id -> the container or image path whose new image the pods must run
	// ("" for the primary one). Absent: no image is expected.
	image map[string]string
}

// waiter follows a change: published through the fleet API, applied and ready on the device.
type waiter struct {
	e      *cliEnv
	fl     *api.Fleet
	target string
	domain string
	ns     string
	device string
	level  waitLevel
	limit  time.Duration
	errw   io.Writer

	ex        deviceExec
	sigCtx    context.Context
	sigStop   context.CancelFunc
	lastView  *api.ConfigTargetView
	baselines map[string]helmBaseline
	lastMsg   map[string]string
	lastLine  string
	stage     string
	deadline  time.Time
}

func (e *cliEnv) newWaiter(fl *api.Fleet, domain, ns, target string, level waitLevel, limit time.Duration, errw io.Writer) *waiter {
	if limit <= 0 {
		limit = defaultWaitTimeout
	}
	return &waiter{e: e, fl: fl, target: target, domain: domain, ns: ns, level: level, limit: limit, errw: errw,
		baselines: map[string]helmBaseline{}, lastMsg: map[string]string{}}
}

func (w *waiter) close() {
	if w.ex != nil {
		w.ex.Close()
		w.ex = nil
	}
	if w.sigStop != nil {
		w.sigStop()
		w.sigStop, w.sigCtx = nil, nil
	}
}

// interruptible returns a context that ends on SIGINT, SIGTERM or SIGHUP, so that a killed
// tnctl returns through its deferred cleanup: the device login key is written to a temporary
// directory while a robot is checked, and the ssh master must not outlive the command. It is
// used from the first moment a shell to the device exists until close.
func (w *waiter) interruptible(ctx context.Context) context.Context {
	if w.sigCtx == nil {
		w.sigCtx, w.sigStop = signalContext(ctx)
	}
	return w.sigCtx
}

// progress prints a line to stderr when it differs from the last one for the same key.
func (w *waiter) progress(key, msg string) {
	if w.lastMsg[key] == msg {
		return
	}
	w.lastMsg[key] = msg
	w.lastLine = msg
	if key == "" {
		fmt.Fprintf(w.errw, "  %s\n", msg)
		return
	}
	fmt.Fprintf(w.errw, "  %s: %s\n", key, msg)
}

func (w *waiter) shell(ctx context.Context) (deviceExec, error) {
	if w.ex != nil {
		return w.ex, nil
	}
	if w.device == "" {
		v, _, err := w.fl.ConfigTarget(ctx, w.target)
		if err != nil {
			return nil, err
		}
		w.device = v.Target.Device
	}
	if w.e.newExec == nil {
		return nil, errors.New("running commands on devices is not available")
	}
	ex, err := w.e.newExec(ctx, w.domain, w.ns, w.device)
	if err != nil {
		return nil, err
	}
	w.ex = ex
	return ex, nil
}

// prepare runs before the write: for applied and ready it records what the device has now, so
// that "a new helm Job" can be told from the old one. It fails before anything is written when the
// device cannot be reached.
func (w *waiter) prepare(ctx context.Context, ids []string) error {
	if w.level != waitApplied && w.level != waitReady {
		return nil
	}
	ctx = w.interruptible(ctx)
	for _, id := range ids {
		kind, ns, name, err := checkID(id)
		if err != nil {
			return err
		}
		if kind != "HelmChart" {
			continue
		}
		ex, err := w.shell(ctx)
		if err == nil {
			var res execResult
			res, err = ex.Run(ctx, remoteShell(helmProbeScript(ns, name, ns, false)))
			if err == nil {
				snap := parseHelmSnapshot(res.Stdout)
				if sudoProblem(snap.chartErr) {
					return newErr(codeError, "cannot check the robot before the change: %s", sudoMessage(snap.chartErr))
				}
				w.baselines[id] = baselineFromSnapshot(snap)
			}
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			return newErr(codeError, "cannot check the robot before the change (%v); --wait=%s needs WireGuard SSH to %s. Use --wait=published, or see: tnctl wg status %s", err, w.level, orDash(w.device), orDash(w.device))
		}
	}
	return nil
}

// wait follows a written change up to the level. It returns nil when the level is reached.
func (w *waiter) wait(ctx context.Context, info writeInfo) error {
	if w.level == waitNone {
		return nil
	}
	if w.level == waitApplied || w.level == waitReady {
		ctx = w.interruptible(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, w.limit)
	defer cancel()
	start := w.e.clock()
	w.deadline = start.Add(w.limit)

	w.stage = "published"
	err := w.waitPublished(ctx, info)
	if err == nil && info.resourcesAfter != nil && w.lastView != nil {
		info.resources = info.resourcesAfter(w.lastView)
	}
	if err == nil && (w.level == waitApplied || w.level == waitReady) && len(info.resources) > 0 {
		w.stage = string(w.level)
		err = w.waitDevice(ctx, info)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || (ctx.Err() == context.Canceled && !errors.Is(err, context.DeadlineExceeded)) {
			return newErr(codeError, "interrupted while waiting for the change to be %s (the change itself is written)", w.stage)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return newErr(codeTimeout, "timed out after %s waiting for the change to be %s (last state: %s)", w.limit, w.stage, orDash(w.lastLine))
		}
		return classify(err)
	}
	fmt.Fprintf(w.errw, "%s after %s\n", w.level, w.e.clock().Sub(start).Round(time.Second))
	return nil
}

// pause waits one poll interval, or reports the timeout when the deadline has passed.
func (w *waiter) pause(ctx context.Context) error {
	if !w.e.clock().Before(w.deadline) {
		return context.DeadlineExceeded
	}
	return w.e.sleepFor(ctx, w.e.interval())
}

// transient tolerates a few failed polls (network, 5xx) before giving up.
type transient struct{ n int }

func (t *transient) fail(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) && (ae.IsAuth() || (ae.Status >= 400 && ae.Status < 500)) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	if t.n++; t.n > 5 {
		return err
	}
	return nil
}

func (t *transient) ok() { t.n = 0 }

// revisionStamp matches the time part of a revision token: 20261019T081500Z-3f9a.
var revisionStamp = regexp.MustCompile(`^(\d{8}T\d{6}Z)-[0-9a-z]+$`)

// revisionSuperseded reports whether the controller's revision was written later than ours.
// Revisions are time-ordered tokens; within the same second the order is unknown, and revisions
// in another format are only ever equal or not.
func revisionSuperseded(have, written string) bool {
	h, w := revisionStamp.FindStringSubmatch(have), revisionStamp.FindStringSubmatch(written)
	return h != nil && w != nil && h[1] > w[1]
}

// waitPublished waits until the platform reports the change as evaluated and published:
// the controller's revision equals the written one and the states of the targets are final.
func (w *waiter) waitPublished(ctx context.Context, info writeInfo) error {
	var tr transient
	for {
		v, _, err := w.fl.ConfigTarget(ctx, w.target)
		if err != nil {
			if ferr := tr.fail(err); ferr != nil {
				return ferr
			}
			w.progress("", "published: retrying after "+err.Error())
		} else {
			tr.ok()
			ok, perr := w.publishedState(v, info)
			if perr != nil {
				return perr
			}
			if ok {
				return nil
			}
		}
		if err := w.pause(ctx); err != nil {
			return err
		}
	}
}

// publishedState evaluates one view: (reached, error). An error means the platform rejected the
// override (exit code 5).
func (w *waiter) publishedState(v *api.ConfigTargetView, info writeInfo) (bool, error) {
	if info.revision != "" && v.OverridesRevision != info.revision {
		if !revisionSuperseded(v.OverridesRevision, info.revision) {
			w.progress("", fmt.Sprintf("published: waiting for the controller to evaluate revision %s (it has %s)", info.revision, orDash(v.OverridesRevision)))
			return false, nil
		}
		// Somebody else wrote after us: the controller works on their revision, which has ours in it.
		w.progress("", fmt.Sprintf("published: revision %s was superseded by %s, following that one", info.revision, v.OverridesRevision))
	}
	w.lastView = v
	if info.storage == api.StorageLocalItem {
		return true, nil // an in-place change to a local item has no override state
	}
	states := map[string]api.OverrideStatus{}
	for _, s := range v.OverrideStatus {
		states[s.Target] = s
	}
	allFinal := true
	for _, t := range info.statusTargets {
		s, ok := states[t]
		if !ok {
			if info.removed {
				continue
			}
			continue // evaluated, nothing reported: treat as in effect
		}
		switch s.State {
		case api.StateActive, api.StateRedundant:
		case api.StatePending, "":
			allFinal = false
			w.progress(t, "pending")
		default: // invalid, no-target, not-applicable
			msg := s.State
			if s.Message != "" {
				msg += ": " + s.Message
			}
			return false, newErr(codeRejected, "%s was not applied: the platform reports the override as %s", t, msg)
		}
	}
	return allFinal, nil
}

// devCheck is one resource followed on the device.
type devCheck struct {
	id, kind, ns, name string
	helm               *expectedHelm
	wl                 *expectedWorkload
	stab               stability
	finished           bool
}

// waitDevice waits until every resource is applied (and ready) on the robot.
func (w *waiter) waitDevice(ctx context.Context, info writeInfo) error {
	ex, err := w.shell(ctx)
	if err != nil {
		return newErr(codeError, "cannot check the robot: %v (see: tnctl wg status %s)", err, orDash(w.device))
	}
	var checks []*devCheck
	for _, id := range info.resources {
		c, err := w.expectation(ctx, id, info)
		if err != nil {
			if exitCodeOf(err) == codeNotFound {
				// The resource is no longer in the configuration (the change removed or renamed it):
				// there is nothing of it to find on the device.
				w.progress(id, "no longer part of the configuration, not checked")
				continue
			}
			return err
		}
		checks = append(checks, c)
	}
	var tr transient
	for {
		remaining := 0
		for _, c := range checks {
			if c.finished {
				continue
			}
			res, err := w.checkOnce(ctx, ex, c)
			if err != nil {
				if ferr := tr.fail(err); ferr != nil {
					return newErr(codeError, "cannot check the robot: %v", ferr)
				}
				w.progress(c.id, "retrying after "+err.Error())
				remaining++
				continue
			}
			tr.ok()
			switch res.state {
			case checkDone:
				c.finished = true
				w.progress(c.id, res.msg)
			case checkUnhealthy:
				return newErr(codeUnhealthy, "%s: %s", c.id, res.msg)
			case checkFatal:
				return newErr(codeError, "%s: %s", c.id, res.msg)
			default:
				w.progress(c.id, res.msg)
				remaining++
			}
		}
		if remaining == 0 {
			return nil
		}
		if err := w.pause(ctx); err != nil {
			return err
		}
	}
}

// expectation reads what the device must have for one resource, from the platform's rendered item.
func (w *waiter) expectation(ctx context.Context, id string, info writeInfo) (*devCheck, error) {
	kind, ns, name, err := checkID(id)
	if err != nil {
		return nil, err
	}
	d, _, err := w.fl.ConfigResource(ctx, w.target, id, false)
	if err != nil {
		return nil, classify(err)
	}
	c := &devCheck{id: id, kind: kind, ns: ns, name: name}
	want := ""
	if sel, ok := info.image[id]; ok {
		want = pickImage(d.Images, sel)
	}
	if kind == "HelmChart" {
		h := &expectedHelm{targetNS: ns, wantImage: want}
		if vc := digStr(d.Rendered, "spec", "valuesContent"); vc != "" {
			h.values = parseValuesYAML(vc)
			if h.values == nil {
				h.values = obj{}
			}
		} else if d.Resource.Values != nil {
			h.values = d.Resource.Values.Merged
		}
		h.version = digStr(d.Rendered, "spec", "version")
		if h.version == "" && d.Resource.Chart != nil {
			h.version = d.Resource.Chart.Version
		}
		h.noVersion = info.versionCleared && h.version == ""
		if t := digStr(d.Rendered, "spec", "targetNamespace"); t != "" {
			h.targetNS = t
		} else if d.Resource.Chart != nil && d.Resource.Chart.TargetNamespace != "" {
			h.targetNS = d.Resource.Chart.TargetNamespace
		}
		c.helm = h
		return c, nil
	}
	wl := &expectedWorkload{kind: kind, images: containerImages(podSpecOf(kind, d.Rendered))}
	if len(wl.images) == 0 {
		for _, im := range d.Resource.Images {
			wl.images[im.Container] = im.Image
		}
	}
	if _, ok := info.image[id]; ok {
		// Follow the image change: the new pods run every image of the published template.
		for _, im := range wl.images {
			wl.wantImages = append(wl.wantImages, im)
		}
		sort.Strings(wl.wantImages)
	}
	c.wl = wl
	return c, nil
}

// pickImage returns the current image of the image key with path sel, else the primary one.
func pickImage(keys []api.ImageKey, sel string) string {
	for _, k := range keys {
		if sel != "" && k.Path == sel {
			return k.Current
		}
	}
	for _, k := range keys {
		if k.Primary {
			return k.Current
		}
	}
	if len(keys) > 0 {
		return keys[0].Current
	}
	return ""
}

// checkOnce probes the device once for one resource and judges the output.
func (w *waiter) checkOnce(ctx context.Context, ex deviceExec, c *devCheck) (checkResult, error) {
	needReady := w.level == waitReady
	now := w.e.clock()
	if c.helm != nil {
		res, err := ex.Run(ctx, remoteShell(helmProbeScript(c.ns, c.name, c.helm.targetNS, needReady)))
		if err != nil {
			return checkResult{}, err
		}
		snap := parseHelmSnapshot(res.Stdout)
		job := "helm-install-" + c.name
		r := evalHelmApplied(*c.helm, w.baselines[c.id], snap, job)
		if r.state == checkUnhealthy {
			if lr, err := ex.Run(ctx, remoteShell(helmJobLogScript(c.ns, c.name))); err == nil && strings.TrimSpace(lr.Stdout) != "" {
				r.msg += "; last log lines:\n" + indent(strings.TrimSpace(lr.Stdout), "    ")
			}
		}
		if r.state != checkDone || !needReady {
			return r, nil
		}
		wls := releaseWorkloads(snap.workloads, c.name)
		if len(wls) == 0 {
			if snap.workloadsErr != "" {
				return pending("applied; cannot list the release's workloads: %s", snap.workloadsErr), nil
			}
			return done("applied; the release has no Deployment, StatefulSet or DaemonSet"), nil
		}
		return w.readiness(&c.stab, now, evalPodsReady(wls, snap.pods, c.helm.wantImage)), nil
	}

	res, err := ex.Run(ctx, remoteShell(workloadProbeScript(c.kind, c.ns, c.name)))
	if err != nil {
		return checkResult{}, err
	}
	secs := parseSections(res.Stdout)
	o, errText := parseObj(secs["obj"])
	if o == nil && sudoProblem(errText) {
		return checkResult{checkFatal, sudoMessage(errText)}, nil
	}
	if o == nil && errText != "" {
		return pending("cannot read %s on the device: %s", c.kind, errText), nil
	}
	r := evalWorkloadApplied(*c.wl, o)
	if r.state != checkDone || !needReady {
		return r, nil
	}
	var pods []obj
	if p, _ := parseObj(secs["pods"]); p != nil {
		for _, it := range digList(p, "items") {
			if po, ok := it.(obj); ok {
				pods = append(pods, po)
			}
		}
	}
	switch c.kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		o["kind"] = c.kind
		return w.readiness(&c.stab, now, evalPodsReady([]obj{o}, pods, c.wl.wantImages...)), nil
	case "Pod":
		if ok, why := podReady(o); !ok {
			c.stab.reset()
			return pending("pod %s: %s", c.name, why), nil
		}
		return w.readiness(&c.stab, now, readyResult{checkResult: done("pod ready"), sig: fmt.Sprint(podRestarts(o))}), nil
	}
	return done("%s applied (jobs have no readiness to wait for)", c.kind), nil
}

// readiness turns a ready verdict into done only after the signature stayed unchanged for readyStableFor.
func (w *waiter) readiness(st *stability, now time.Time, r readyResult) checkResult {
	if r.state != checkDone {
		st.reset()
		return r.checkResult
	}
	if for_ := st.observe(r.sig, now); for_ < readyStableFor {
		return pending("%s; checking for restarts (%s of %s)", r.msg, for_.Round(time.Second), readyStableFor)
	}
	return done("%s, no restarts for %s", r.msg, readyStableFor)
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
