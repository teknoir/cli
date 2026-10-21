package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

// ---------- config remove ----------

func newConfigRemoveCmd(e *cliEnv) *cobra.Command {
	var f struct {
		dryRun bool
		wait   waitFlags
	}
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "remove <target> <resource> [--dry-run] [--wait=published|applied]",
		Short:        "Remove a local item (written straight to the robot, e.g. by Devstudio) from a robot or device",
		Long: `Remove a local item from a robot or device. A local item is an entry that a tool such as Devstudio
wrote straight to the device's manifest, not through git; 'tnctl config get <target>' shows it in the
"local" layer.

The robot deletes the item: a Deployment, Service or other object is deleted, and a HelmChart's release
is uninstalled. The tool that wrote the item may write it again (a running Devstudio, for example).

Items that come from git are not removed here: remove them in git, or from the robot's apps, a fleet or
the robot type, whichever the platform names when you try (it answers with exit 8 and says where).
Overrides are removed with 'tnctl config unset' and 'tnctl config reset', not with this command.

  <resource>      Kind/namespace/name, Kind/name or a unique name, as in the other config commands
  --dry-run       show what would be removed, remove nothing
  --wait=LEVEL    published: the robot's published configuration no longer has the item
                  applied: the object is gone on the robot, and for a HelmChart its release is
                  uninstalled (checked over WireGuard SSH). Write --wait=applied, not --wait applied.
  --timeout T     how long --wait may take (default 5m; exit code 4 when it runs out)

Exit 8 also means the item is not local, so it cannot be removed here; the message says where to remove it.`,
		Example: `  tnctl config remove vision-station-01 HelmChart/default/local-llm --dry-run
  tnctl config remove vision-station-01 local-llm --wait=applied`,
		Args: argsRange(2, 2, "remove needs a robot or device name and a local resource"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, resArg := args[0], args[1]
			if err := f.wait.misplaced(args[1:]); err != nil {
				return err
			}
			s, err := e.begin(cmd, target, &f.wait, f.dryRun)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			id, _, err := resolveTarget(ctx, s.fl, target, resArg)
			if err != nil {
				return classify(err)
			}
			kind, ns, name, err := checkID(id)
			if err != nil {
				return err
			}
			w := s.waiter()
			if err := w.prepareRemove(ctx); err != nil {
				w.close()
				return err
			}
			res, raw, err := s.fl.RemoveLocalItem(ctx, target, id, f.dryRun)
			if err != nil {
				w.close()
				return classify(err)
			}
			out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
			for _, wn := range res.Warnings {
				fmt.Fprintln(errw, "warning:", wn)
			}
			if s.format == "table" {
				printRemoveResult(out, res, id, target)
			}
			waiting := !f.dryRun && s.level != waitNone
			var werr error
			start := e.clock()
			if waiting {
				defer w.close()
				werr = w.wait(ctx, writeInfo{gone: &goneItem{id: id, kind: kind, ns: ns, name: name, targetNS: removedTargetNS(res, ns)}})
			} else {
				w.close()
			}
			if s.format != "table" {
				if waiting {
					ws := map[string]any{"level": s.level, "reached": werr == nil, "seconds": int(e.clock().Sub(start).Seconds())}
					if werr != nil {
						ws["error"] = werr.Error()
					}
					raw = withFields(raw, map[string]any{"wait": ws})
				}
				if err := writeStructured(out, s.format, raw); err != nil {
					return err
				}
			}
			return werr
		},
	}
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "show what would be removed, remove nothing")
	f.wait.add(c)
	return c
}

// printRemoveResult is the table-mode answer of a removal.
func printRemoveResult(out io.Writer, res *api.LocalRemoveResult, id, target string) {
	switch {
	case res.Noop && res.DryRun:
		fmt.Fprintf(out, "Nothing to remove: %s is not in %s.\n", id, target)
	case res.Noop:
		fmt.Fprintf(out, "%s was already gone from %s.\n", id, target)
	case res.DryRun:
		fmt.Fprintf(out, "Would remove %s from %s.\n", id, target)
	default:
		fmt.Fprintf(out, "Removed %s from %s.\n", id, target)
	}
	if res.Noop {
		return
	}
	for _, l := range itemSummary(res) {
		fmt.Fprintf(out, "  %s\n", l)
	}
}

// itemSummary describes the removed item in a few lines.
func itemSummary(res *api.LocalRemoveResult) []string {
	var lines []string
	if res.Owner != "" {
		lines = append(lines, "written by: "+res.Owner)
	}
	it := obj(res.Removed)
	if chart := digStr(it, "spec", "chart"); chart != "" {
		l := "chart: " + chart
		if v := digStr(it, "spec", "version"); v != "" {
			l += " " + v
		}
		if t := digStr(it, "spec", "targetNamespace"); t != "" {
			l += " in namespace " + t
		}
		lines = append(lines, l)
	}
	if imgs := containerImages(podSpecOf(digStr(it, "kind"), it)); len(imgs) > 0 {
		names := make([]string, 0, len(imgs))
		for c := range imgs {
			names = append(names, c)
		}
		sort.Strings(names)
		var parts []string
		for _, c := range names {
			parts = append(parts, c+"="+imgs[c])
		}
		lines = append(lines, "images: "+strings.Join(parts, ", "))
	}
	if res.HadOverride {
		lines = append(lines, "it also had an override on this robot")
	}
	return lines
}

// removedTargetNS is the namespace a HelmChart's release lives in: spec.targetNamespace of the
// removed item, else the namespace of the HelmChart.
func removedTargetNS(res *api.LocalRemoveResult, ns string) string {
	if t := digStr(obj(res.Removed), "spec", "targetNamespace"); k8sName.MatchString(t) {
		return t
	}
	return ns
}

// ---------- waiting for the removal ----------

// goneItem is a local item that --wait follows until it is gone.
type goneItem struct {
	id, kind, ns, name string
	// targetNS is where a HelmChart's release secrets live.
	targetNS string
}

// stillThereError ends --wait=applied of a removal that ran out of time with the item still on the robot.
type stillThereError struct{ msg string }

func (e *stillThereError) Error() string { return e.msg }

// prepareRemove runs before the delete: applied needs the robot, and it must be reachable before
// anything is removed.
func (w *waiter) prepareRemove(ctx context.Context) error {
	if w.level != waitApplied && w.level != waitReady {
		return nil
	}
	ctx = w.interruptible(ctx)
	if _, err := w.shell(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return newErr(codeError, "cannot check the robot before the change (%v); --wait=%s needs WireGuard SSH to %s. Use --wait=published, or see: tnctl wg status %s", err, w.level, orDash(w.device), orDash(w.device))
	}
	return nil
}

// stillLocal reports whether a view of the target still has the item in the local layer.
func stillLocal(v *api.ConfigTargetView, id string) bool {
	for _, list := range [][]api.FleetConfigResource{v.Configuration.Resources, v.Configuration.Effective} {
		for _, r := range list {
			if r.Layer == api.LayerLocal && r.ID() == id {
				return true
			}
		}
	}
	return false
}

// waitGonePublished waits until the platform's configuration of the target no longer has the item
// in its local layer.
func (w *waiter) waitGonePublished(ctx context.Context, g *goneItem) error {
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
			if !stillLocal(v, g.id) {
				return nil
			}
			w.progress("", "published: "+g.id+" is still in the robot's local configuration")
		}
		if err := w.pause(ctx); err != nil {
			return err
		}
	}
}

// goneProbeScript lists the object and, for a HelmChart, the release secrets of its helm release.
func goneProbeScript(g *goneItem) string {
	var b strings.Builder
	k := kubectlOnDevice
	res := kubectlResource(g.kind)
	if g.kind == "HelmChart" {
		res = "helmcharts.helm.cattle.io"
	}
	fmt.Fprintf(&b, "echo %s; %s -n %s get %s %s -o name --ignore-not-found 2>&1\n", shq(sectionMark+"obj"), k, shq(g.ns), shq(res), shq(g.name))
	if g.kind == "HelmChart" {
		fmt.Fprintf(&b, "echo %s; %s -n %s get secrets -l %s -o name 2>&1\n", shq(sectionMark+"release"), k, shq(g.targetNS), shq("owner=helm,name="+g.name))
	}
	b.WriteString("true\n")
	return b.String()
}

// evalGone judges the output of goneProbeScript.
func evalGone(g *goneItem, out string) checkResult {
	secs := parseSections(out)
	have := strings.TrimSpace(secs["obj"])
	if sudoProblem(have) {
		return checkResult{checkFatal, sudoMessage(have)}
	}
	if have != "" && !strings.Contains(have, "NotFound") && !strings.Contains(have, "doesn't have a resource type") {
		if strings.HasPrefix(strings.ToLower(have), "error") {
			return pending("cannot read %s on the device: %s", g.kind, firstNonEmptyLine(have))
		}
		return pending("%s is still on the robot", g.id)
	}
	if g.kind != "HelmChart" {
		return done("%s is gone from the robot", g.id)
	}
	rel := strings.TrimSpace(secs["release"])
	if sudoProblem(rel) {
		return checkResult{checkFatal, sudoMessage(rel)}
	}
	prefix := "sh.helm.release.v1." + g.name + "."
	var found []string
	for _, line := range strings.Split(rel, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, prefix) {
			found = append(found, line)
		} else if strings.HasPrefix(strings.ToLower(line), "error") {
			return pending("cannot read the helm release of %s: %s", g.name, line)
		}
	}
	if len(found) > 0 {
		return pending("the HelmChart is gone, helm release %s is still installed in namespace %s (%d release secret(s)); waiting for helm-controller to uninstall it", g.name, g.targetNS, len(found))
	}
	return done("%s is gone from the robot and its helm release is uninstalled", g.id)
}

// waitGoneDevice waits until the item is gone on the robot. When it stays, the error says how to
// delete it by hand.
func (w *waiter) waitGoneDevice(ctx context.Context, g *goneItem) error {
	ex, err := w.shell(ctx)
	if err != nil {
		return newErr(codeError, "cannot check the robot: %v (see: tnctl wg status %s)", err, orDash(w.device))
	}
	var tr transient
	for {
		res, err := ex.Run(ctx, remoteShell(goneProbeScript(g)))
		if err == nil {
			tr.ok()
			r := evalGone(g, res.Stdout)
			switch r.state {
			case checkDone:
				w.progress(g.id, r.msg)
				return nil
			case checkFatal:
				return newErr(codeError, "%s: %s", g.id, r.msg)
			}
			w.progress(g.id, r.msg)
		} else {
			if ferr := tr.fail(err); ferr != nil {
				if errors.Is(ferr, context.DeadlineExceeded) {
					return w.stillThere(g)
				}
				return newErr(codeError, "cannot check the robot: %v", ferr)
			}
			w.progress(g.id, "retrying after "+err.Error())
		}
		if perr := w.pause(ctx); perr != nil {
			if errors.Is(perr, context.DeadlineExceeded) {
				return w.stillThere(g)
			}
			return perr
		}
	}
}

func (w *waiter) stillThere(g *goneItem) error {
	return &stillThereError{fmt.Sprintf("still on the robot after %s: the robot's agent did not delete it (%s); delete it on the robot with: tnctl exec %s -- sudo k3s kubectl -n %s delete %s %s",
		w.limit, orDash(w.lastLine), w.target, g.ns, deleteResource(g.kind), g.name)}
}

// deleteResource is the kubectl resource to name in the manual delete command.
func deleteResource(kind string) string {
	if kind == "HelmChart" {
		return "helmcharts.helm.cattle.io"
	}
	return kubectlResource(kind)
}
