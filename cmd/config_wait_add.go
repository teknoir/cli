package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/teknoir/cli/pkg/api"
)

// This file is the --wait of `config add` and `config edit`: how a written local item is followed
// in the platform's published view and on the robot.

// changedAtAnnotation is stamped by the platform on every local item it writes; the robot's copy
// has the same value once the agent has applied this version.
const changedAtAnnotation = "teknoir.org/config-changed-at"

// addedItem is a local item that --wait follows.
type addedItem struct {
	id, kind, ns, name string
	action             string
	// item is the item as the platform stored it (secret values hidden).
	item obj
}

// newAddedItem reads what to follow from the platform's answer; the document fills what it lacks.
func newAddedItem(res *api.LocalWriteResult, d localDoc) (*addedItem, error) {
	a := &addedItem{action: res.Action, item: obj(res.Item)}
	a.kind = digStr(a.item, "kind")
	a.name = digStr(a.item, "metadata", "name")
	a.ns = digStr(a.item, "metadata", "namespace")
	if k, ns, n := splitID(res.ID); a.kind == "" || a.name == "" {
		if a.kind == "" {
			a.kind = k
		}
		if a.name == "" {
			a.name = n
		}
		if a.ns == "" {
			a.ns = ns
		}
	}
	if a.kind == "" {
		a.kind = d.kind
	}
	if a.name == "" {
		a.name = d.name
	}
	if a.ns == "" {
		a.ns = "default" // a cluster-scoped kind ignores it
	}
	a.id = a.kind + "/" + a.ns + "/" + a.name
	if _, _, _, err := checkID(a.id); err != nil {
		return nil, newErr(codeError, "the platform stored %s, which tnctl cannot follow on the robot; use --wait=published", orDash(res.ID))
	}
	return a, nil
}

// stamp is the config-changed-at value the robot's copy must carry; "" when the platform sent none.
func (a *addedItem) stamp() string {
	return digStr(a.item, "metadata", "annotations", changedAtAnnotation)
}

// prepareAdd runs before the first write of an add: it records the config hash that
// device-controller has published, and for applied and ready it makes sure the robot can be
// reached and takes the baseline of the HelmCharts, so nothing is written for a wait that cannot work.
func (w *waiter) prepareAdd(ctx context.Context, target string, helmIDs []string) (string, error) {
	v, _, err := w.fl.ConfigTarget(ctx, target)
	if err != nil {
		return "", classify(err)
	}
	w.device = v.Target.Device
	hash := ""
	if v.Published != nil {
		hash = v.Published.Hash
	}
	if w.level != waitApplied && w.level != waitReady {
		return hash, nil
	}
	ctx = w.interruptible(ctx)
	if _, err := w.shell(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", newErr(codeError, "cannot check the robot before the change (%v); --wait=%s needs WireGuard SSH to %s. Use --wait=published, or see: tnctl wg status %s", err, w.level, orDash(w.device), orDash(w.device))
	}
	return hash, w.prepare(ctx, helmIDs)
}

// waitAddedPublished waits until device-controller has published a config in which every added
// item is the winner in the robot's local layer with the content that was written. When something
// changed, the published hash must also differ from the one before the write.
func (w *waiter) waitAddedPublished(ctx context.Context, info writeInfo) error {
	changed := false
	for _, a := range info.added {
		if a.action != api.LocalActionUnchanged {
			changed = true
		}
	}
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
			w.lastView = v
			hash := ""
			if v.Published != nil {
				hash = v.Published.Hash
			}
			if changed && hash == info.hashBefore {
				w.progress("", "published: waiting for device-controller to publish a new config (still "+orDash(hash)+")")
			} else if msg, err := w.addedPublished(ctx, v, info.added); err != nil {
				if ferr := tr.fail(err); ferr != nil {
					return ferr
				}
				w.progress("", "published: retrying after "+err.Error())
			} else if msg != "" {
				w.progress("", "published: "+msg)
			} else {
				return nil
			}
		}
		if err := w.pause(ctx); err != nil {
			return err
		}
	}
}

// addedPublished says what is missing in a view for the items to be published ("" when nothing is).
func (w *waiter) addedPublished(ctx context.Context, v *api.ConfigTargetView, added []*addedItem) (string, error) {
	for _, a := range added {
		if l := winnerLayer(v, a.id); l != api.LayerLocal {
			return fmt.Sprintf("waiting for %s to win in the robot's local layer (it is %s)", a.id, orDash(l)), nil
		}
		d, _, err := w.fl.ConfigResource(ctx, w.target, a.id, false)
		if err != nil {
			return "", err
		}
		if why := renderedDiffers(a, d.Rendered); why != "" {
			return fmt.Sprintf("%s is in the local layer, but the published item %s", a.id, why), nil
		}
	}
	return "", nil
}

// renderedDiffers compares the item as published with the one that was written: the stamp and
// the fields the robot's agent keeps. "" means equal, or nothing to compare.
func renderedDiffers(a *addedItem, rendered map[string]any) string {
	if rendered == nil || a.item == nil {
		return ""
	}
	if st := a.stamp(); st != "" {
		if got := digStr(rendered, "metadata", "annotations", changedAtAnnotation); got != st {
			return "is an older version"
		}
	}
	for _, f := range []string{"spec", "data", "binaryData", "type"} {
		want, have := a.item[f], rendered[f]
		if want == nil && have == nil {
			continue
		}
		wm, wok := want.(map[string]any)
		hm, hok := have.(map[string]any)
		if wok && hok {
			if !sameValues(wm, hm) {
				return "differs in " + f
			}
			continue
		}
		if fmt.Sprint(normalizeYAML(want)) != fmt.Sprint(normalizeYAML(have)) {
			return "differs in " + f
		}
	}
	return ""
}

// addedCheck builds what the robot must have for an added item.
func (w *waiter) addedCheck(a *addedItem) *devCheck {
	c := &devCheck{id: a.id, kind: a.kind, ns: a.ns, name: a.name, added: true, stamp: a.stamp()}
	if a.kind == "HelmChart" {
		h := &expectedHelm{targetNS: a.ns, values: obj{}}
		if vc := digStr(a.item, "spec", "valuesContent"); vc != "" {
			if v := parseValuesYAML(vc); v != nil {
				h.values = v
			}
		}
		h.version = digStr(a.item, "spec", "version")
		if t := digStr(a.item, "spec", "targetNamespace"); t != "" && k8sName.MatchString(t) {
			h.targetNS = t
		}
		c.helm = h
		return c
	}
	c.wl = &expectedWorkload{kind: a.kind, images: containerImages(podSpecOf(a.kind, a.item))}
	return c
}

// releaseProbeScript lists the release secrets of a helm release.
func releaseProbeScript(targetNS, name string) string {
	return fmt.Sprintf("echo %s; %s -n %s get secrets -l %s -o name 2>&1\ntrue\n",
		shq(sectionMark+"release"), kubectlOnDevice, shq(targetNS), shq("owner=helm,name="+name))
}

// evalReleaseInstalled judges the output of releaseProbeScript: a release secret must exist.
func evalReleaseInstalled(name, targetNS, out string) checkResult {
	rel := parseSections(out)["release"]
	if sudoProblem(rel) {
		return checkResult{checkFatal, sudoMessage(rel)}
	}
	prefix := "sh.helm.release.v1." + name + "."
	for _, line := range strings.Split(rel, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, prefix) {
			return done("helm Job succeeded and release %s is installed in namespace %s", name, targetNS)
		}
		if strings.HasPrefix(strings.ToLower(line), "error") {
			return pending("cannot read the helm release of %s: %s", name, line)
		}
	}
	return pending("the helm Job succeeded, waiting for the release %s in namespace %s", name, targetNS)
}

// generationKinds are the workloads whose controller reports observedGeneration.
var generationKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true}

// evalAddedApplied is the check of an added item on top of evalWorkloadApplied: the robot's copy
// is this version, and a workload's controller has seen its latest generation.
func evalAddedApplied(c *devCheck, o obj) checkResult {
	if c.stamp != "" && digStr(o, "metadata", "annotations", changedAtAnnotation) != c.stamp {
		return pending("the robot has an older version of %s", c.id)
	}
	if generationKinds[c.kind] {
		if gen, observed := digInt(o, "metadata", "generation"), digInt(o, "status", "observedGeneration"); gen > observed {
			return pending("the controller has not seen generation %d yet (at %d)", gen, observed)
		}
	}
	return done("%s is on the robot", c.id)
}

// evalJobComplete judges a Job for ready: complete is done, failed is unhealthy.
func evalJobComplete(o obj) checkResult {
	for _, c := range digList(o, "status", "conditions") {
		co, _ := c.(obj)
		if digStr(co, "status") != "True" {
			continue
		}
		switch digStr(co, "type") {
		case "Complete":
			return done("job complete")
		case "Failed":
			why := digStr(co, "reason")
			if m := digStr(co, "message"); m != "" {
				why += ": " + m
			}
			return unhealthy("job failed (%s)", strings.TrimPrefix(why, ": "))
		}
	}
	want := int64(1)
	if dig(o, "spec", "completions") != nil {
		want = digInt(o, "spec", "completions")
	}
	if digInt(o, "status", "succeeded") >= want {
		return done("job complete")
	}
	return pending("job running (%d active, %d of %d succeeded, %d failed)", digInt(o, "status", "active"), digInt(o, "status", "succeeded"), want, digInt(o, "status", "failed"))
}
