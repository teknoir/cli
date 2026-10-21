package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

// ---------- config remove ----------

func newConfigRemoveCmd(e *cliEnv) *cobra.Command {
	var f struct {
		dryRun bool
		yes    bool
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
is uninstalled. If git has a copy with the same identity (a fleet, the robot type, ...), that copy applies
again instead and nothing is uninstalled; the command says so, and says what happens to an override of the
item on this robot. The tool that wrote the item may write it again (a running Devstudio, for example).

Items that come from git are not removed here: remove them in git, or from the robot's apps, a fleet or
the robot type, whichever the platform names when you try (it answers with exit 8 and says where).
Overrides are removed with 'tnctl config unset' and 'tnctl config reset', not with this command.

  <resource>      Kind/namespace/name, Kind/name or a unique name, as in the other config commands
  --dry-run       show what would be removed, remove nothing
  --yes           confirm the removal of a Namespace (see below); other items need no confirmation
  --wait=LEVEL    published: device-controller published a new config (its hash changed) without
                  the item
                  applied: also the object is gone on the robot, and for a HelmChart its release is
                  uninstalled (checked over WireGuard SSH). Write --wait=applied, not --wait applied.
                  When a git copy takes over (the answer says "applies again"), nothing is deleted:
                  published already means the git copy wins, and applied needs no check on the robot.
  --timeout T     how long --wait may take (default 5m; exit code 4 when it runs out)

Removing a Namespace deletes everything in it on the robot, not only what you added. Without --yes the
command prints the platform's warning and the list of what would go (from a dry run), removes nothing and
exits with 2; repeat it with --yes to confirm. --dry-run never needs --yes.

Exit 8 also means the item is not local, so it cannot be removed here; the message says where to remove it.`,
		Example: `  tnctl config remove vision-station-01 HelmChart/default/local-llm --dry-run
  tnctl config remove vision-station-01 local-llm --wait=applied
  tnctl config remove vision-station-01 Namespace/llm-ns --yes`,
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
			if strings.EqualFold(kind, "Namespace") {
				// the platform may match the kind case-sensitively: send its own spelling
				kind = "Namespace"
				id = kind + "/" + ns + "/" + name
			}
			if kind == "Namespace" && !f.dryRun && !f.yes {
				if err := e.confirmNamespaceRemoval(cmd, s, target, id, name); err != nil {
					return err
				}
			}
			w := s.waiter()
			gone := &goneItem{id: id, kind: kind, ns: ns, name: name}
			if !f.dryRun && s.level != waitNone {
				if err := w.prepareRemove(ctx, target, id, gone); err != nil {
					w.close()
					return err
				}
			}
			res, raw, err := s.fl.RemoveLocalItem(ctx, target, id, f.dryRun)
			if err != nil {
				w.close()
				return classify(removeNotFoundHint(err, target))
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
			switch {
			case waiting && !res.Noop:
				defer w.close()
				gone.targetNS = removedTargetNS(res, ns)
				if res.RevertsTo != nil {
					gone.revertsTo = res.RevertsTo.Layer
				}
				werr = w.wait(ctx, writeInfo{gone: gone})
			default:
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
	c.Flags().BoolVar(&f.yes, "yes", false, "confirm the removal of a Namespace, which deletes everything in it on the robot")
	f.wait.add(c)
	return c
}

// confirmNamespaceRemoval stops the removal of a Namespace that was not confirmed with --yes. It asks
// the platform for a dry run, prints what it says would go, and returns the usage error (exit 2).
// A Namespace that is not local is not removable at all: the platform's own answer (exit 8, 404) is
// returned instead, as the removal would give it. An item that is already gone needs no confirmation.
func (e *cliEnv) confirmNamespaceRemoval(cmd *cobra.Command, s *session, target, id, name string) error {
	res, raw, err := s.fl.RemoveLocalItem(cmd.Context(), target, id, true)
	if err != nil {
		return classify(removeNotFoundHint(err, target))
	}
	if res.Noop {
		return nil
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	for _, wn := range res.Warnings {
		fmt.Fprintln(errw, "warning:", wn)
	}
	if s.format == "table" {
		printRemoveResult(out, res, id, target)
	} else if err := writeStructured(errw, s.format, raw); err != nil {
		// stderr, not stdout: a refusal must not leave an answer on stdout that a script could take
		// for the result of a removal
		return err
	}
	return usageErr("Removing Namespace %s deletes everything in it on the robot; repeat with --yes to confirm", name)
}

// printRemoveResult is the table-mode answer of a removal.
func printRemoveResult(out io.Writer, res *api.LocalRemoveResult, id, target string) {
	if res.ID != "" {
		id = res.ID // the platform's spelling, Namespace//name for a cluster-scoped item
	}
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
	if rt := res.RevertsTo; rt != nil {
		what := rt.Layer
		if rt.Source != "" {
			what += " " + rt.Source
		}
		verb := "applies again"
		if res.DryRun {
			verb = "would apply again"
		}
		lines = append(lines, fmt.Sprintf("The %s version (git) %s.", what, verb))
	}
	switch {
	case res.OverrideRemoved && res.DryRun:
		lines = append(lines, "Its override on this robot would be removed too.")
	case res.OverrideRemoved:
		lines = append(lines, "Its override on this robot was removed too.")
	case res.KeptOverride:
		lines = append(lines, "This robot's override stays and applies to the git version.")
	case res.HadOverride:
		lines = append(lines, "It also had an override on this robot.")
	}
	return lines
}

// removeNotFoundHint says, for a 404, that a concurrent removal may have beaten us to it.
func removeNotFoundHint(err error, target string) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return fmt.Errorf("%w (it may already have been removed: tnctl config get %s)", err, target)
	}
	return err
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
	// hash is the config hash device-controller had published before the delete; device is the device's name.
	hash, device string
	// revertsTo is the layer of the git copy that takes over; "" when the item is just gone.
	revertsTo string
}

// stillThereError ends --wait of a removal that ran out of time: the config was not published, or the
// item is still on the robot.
type stillThereError struct{ msg string }

func (e *stillThereError) Error() string { return e.msg }

// prepareRemove runs before the delete. It records the config hash device-controller has published,
// and for applied it makes sure the robot is reachable before anything is removed. A removal that
// hands the item to a git copy (a dry run says so) needs no robot, it is followed in the published view.
func (w *waiter) prepareRemove(ctx context.Context, target, id string, g *goneItem) error {
	v, _, err := w.fl.ConfigTarget(ctx, target)
	if err != nil {
		return classify(err)
	}
	g.device, w.device = v.Target.Device, v.Target.Device
	if v.Published != nil {
		g.hash = v.Published.Hash
	}
	if w.level != waitApplied && w.level != waitReady {
		return nil
	}
	pre, _, err := w.fl.RemoveLocalItem(ctx, target, id, true)
	if err != nil {
		return classify(removeNotFoundHint(err, target))
	}
	if pre.RevertsTo != nil || pre.Noop {
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

// winnerLayer is the layer of the effective resource id, "" when it is not effective.
func winnerLayer(v *api.ConfigTargetView, id string) string {
	for _, r := range v.Configuration.Effective {
		if r.ID() == id {
			return r.Layer
		}
	}
	return ""
}

// waitGonePublished waits until device-controller has published a new config without the item: the
// published hash differs from the one before the delete, and the item is no longer in the local
// layer (with a git copy taking over, its layer wins).
func (w *waiter) waitGonePublished(ctx context.Context, g *goneItem) error {
	timeout := func() error {
		return &stillThereError{fmt.Sprintf("device-controller has not published a new config for %s yet (after %s; last state: %s)", orDash(g.device), w.limit, orDash(w.lastLine))}
	}
	var tr transient
	for {
		v, _, err := w.fl.ConfigTarget(ctx, w.target)
		if err != nil {
			if ferr := tr.fail(err); ferr != nil {
				if errors.Is(ferr, context.DeadlineExceeded) {
					return timeout()
				}
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
			switch {
			case hash == g.hash:
				w.progress("", "published: waiting for device-controller to publish a new config (still "+orDash(hash)+")")
			case stillLocal(v, g.id):
				w.progress("", "published: "+g.id+" is still in the robot's local configuration")
			case g.revertsTo != "" && winnerLayer(v, g.id) != g.revertsTo:
				w.progress("", fmt.Sprintf("published: waiting for the %s copy of %s to win (it is %s)", g.revertsTo, g.id, orDash(winnerLayer(v, g.id))))
			default:
				return nil
			}
		}
		if err := w.pause(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return timeout()
			}
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
