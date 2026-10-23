package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

// ---------- config off / config on ----------

// offFlags are the flags of config off and config on.
type offFlags struct {
	app     string
	repo    string
	note    string
	confirm string
	dryRun  bool
	wait    waitFlags
}

const offLong = `Turn a resource off on this robot only. The resource stays in the place it comes from (the
robot type, a fleet, the robot's own apps) on every other robot; on this one the platform leaves it
out of the configuration and the robot deletes it.

A HelmChart's release is uninstalled and the data in its volumes is deleted. Other objects are
deleted. For items that come from git, use this instead of editing git when one robot should not run
them; a local item is removed with 'tnctl config remove' instead.

  <resource>      Kind/namespace/name, Kind/name or a unique name, as in the other config commands
  --app <path>    turn off every item of an application (its path in its repository), instead of
                  one resource; --repo picks the repository when the path exists in several
  --note TEXT     why; shown to everyone who looks at the robot
  --dry-run       show what turning it off does on the robot, write nothing
  --confirm TEXT  the text a dry run asked for, when turning it off deletes data
  --wait[=LEVEL]  published: the controller published a configuration without the item
                  applied (a bare --wait): also the object is gone on the robot, and for a
                  HelmChart its release is uninstalled (checked over WireGuard SSH)
  --timeout T     how long --wait may take (default 5m; exit code 4 when it runs out)

Unless --dry-run, the command first asks the platform what turning the item off does. When that
deletes data it prints the impact and exits 2 with the --confirm text to repeat it with; nothing is
written until the confirmation matches.

Local items cannot be turned off: they are removed with 'tnctl config remove'. A resource that an
application adds later is not off (an application's items are turned off together with it).`

const onLong = `Turn a resource that is off on this robot on again. The copy that git (or the robot type, a fleet or
the robot's own apps) yields is installed fresh, with this robot's override (values, chart version,
images) applied: data the item had before it was turned off is not restored.

  <resource>      Kind/namespace/name, Kind/name or a unique name, as in the other config commands
  --app <path>    turn on every item of an application, instead of one resource; --repo picks the
                  repository when the path exists in several
  --note TEXT     why; shown to everyone who looks at the robot
  --dry-run       show what turning it on does, write nothing
  --wait[=LEVEL]  published: the controller published a configuration with the item in it again
                  applied: the robot has it (a HelmChart's helm Job succeeded, a release exists)
                  ready (a bare --wait): also the pods are Ready and have not restarted for 20 s
  --timeout T     how long --wait may take (default 5m; exit code 4 when it runs out)`

const offExample = `  tnctl config off vision-station-01 notebook --dry-run
  tnctl config off vision-station-01 HelmChart/default/notebook --wait
  tnctl config off vision-station-01 notebook --confirm "delete notebook volumes"
  tnctl config off vision-station-01 --app apps/mock-vision --note "not used at this site"

  tnctl config on vision-station-01 notebook --wait=applied`

func newConfigOffCmd(e *cliEnv) *cobra.Command {
	var f offFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "off <target> <resource> | --app <path> [--dry-run] [--confirm TEXT] [--wait=published|applied]",
		Short:        "Turn a resource off on one robot: the robot deletes it, its data included",
		Long:         offLong,
		Example:      offExample,
		Args:         argsRange(1, 2, "off needs a robot or device name and a resource (or --app <path>)"),
		RunE:         func(cmd *cobra.Command, args []string) error { return e.runOffOn(cmd, args, &f, true) },
	}
	addOffFlags(c, &f, waitApplied)
	return c
}

func newConfigOnCmd(e *cliEnv) *cobra.Command {
	var f offFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "on <target> <resource> | --app <path> [--dry-run] [--wait[=published|applied|ready]]",
		Short:        "Turn a resource that is off on this robot on again (a fresh install, its data is not restored)",
		Long:         onLong,
		Example: `  tnctl config on vision-station-01 notebook
  tnctl config on vision-station-01 --app apps/mock-vision --wait=applied`,
		Args: argsRange(1, 2, "on needs a robot or device name and a resource (or --app <path>)"),
		RunE: func(cmd *cobra.Command, args []string) error { return e.runOffOn(cmd, args, &f, false) },
	}
	addOffFlags(c, &f, waitReady)
	return c
}

// addOffFlags adds the shared flags; bare is what a bare --wait means (applied for off, ready for on).
func addOffFlags(c *cobra.Command, f *offFlags, bare waitLevel) {
	c.Flags().StringVar(&f.app, "app", "", "turn off (or on) every item of an application, by its path in its repository")
	c.Flags().StringVar(&f.repo, "repo", "", "repository URL of the application, when its path exists in several")
	c.Flags().StringVar(&f.note, "note", "", "why this change exists")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "show what it does on the robot, write nothing")
	usage := "wait until the change is published, applied or ready on the robot (a bare --wait means ready)"
	if bare == waitApplied {
		// off never asks for a confirmation, and has no ready to wait for: the item is gone.
		usage = "wait until the change is published or applied on the robot (a bare --wait means applied)"
		c.Flags().StringVar(&f.confirm, "confirm", "", "the text a dry run asked for (turning off deletes data)")
	}
	f.wait.add(c)
	c.Flags().Lookup("wait").NoOptDefVal = string(bare)
	c.Flags().Lookup("wait").Usage = usage
}

// waitHints turns "--wait applied" (a bare --wait, then the level as an argument) into a usage error.
func waitHints(wf *waitFlags, bare waitLevel, args []string) error {
	if wf.wait != string(bare) {
		return nil
	}
	for _, a := range args {
		if h := lookupHint(a); h != "" {
			return usageErr("%q looks like a --wait level, not an argument%s", a, h)
		}
	}
	return nil
}

func (e *cliEnv) runOffOn(cmd *cobra.Command, args []string, f *offFlags, off bool) error {
	target, verb := args[0], "off"
	if !off {
		verb = "on"
	}
	var resArg string
	switch {
	case f.app != "" && len(args) > 1:
		return usageErr("give either a resource or --app <path>, not both")
	case f.app == "" && len(args) < 2:
		return usageErr("%s needs a resource (Kind/namespace/name, Kind/name or a name), or --app <path>\nusage: %s", verb, cmd.UseLine())
	case f.app == "":
		resArg = args[1]
	}
	if f.repo != "" && f.app == "" {
		return usageErr("--repo names the repository of an application: use it with --app <path>")
	}
	bare := waitApplied
	if !off {
		bare = waitReady
	}
	if err := waitHints(&f.wait, bare, args[1:]); err != nil {
		return err
	}
	s, err := e.begin(cmd, target, &f.wait, f.dryRun)
	if err != nil {
		return err
	}
	if off && s.level == waitReady {
		return usageErr("off follows published or applied, not ready: the item is gone, there is nothing to be ready")
	}
	ctx := cmd.Context()

	var view *api.ConfigTargetView
	var id, repo, path string
	if f.app != "" {
		path = normAppPath(f.app)
		view, _, err = s.fl.ConfigTarget(ctx, target)
		if err != nil {
			return classify(err)
		}
		if repo, err = resolveAppRepo(view, f.repo, f.app); err != nil {
			return err
		}
	} else {
		id, _, err = resolveTarget(ctx, s.fl, target, resArg)
		if err != nil {
			return classify(err)
		}
		if _, _, _, err := checkID(id); err != nil {
			return err
		}
	}

	// do is one request on either route: the resource's own override, or every item of an application.
	do := func(dryRun bool, confirm string) (*api.OverrideWriteResult, *api.ApplicationOffResult, json.RawMessage, error) {
		if f.app != "" {
			r, raw, err := s.fl.SetApplicationOff(ctx, target, repo, path, off, f.note, dryRun, confirm)
			if err != nil {
				return nil, nil, nil, classify(err)
			}
			return &r.OverrideWriteResult, r, raw, nil
		}
		r, raw, err := s.fl.SetResourceOff(ctx, target, id, off, f.note, dryRun, confirm)
		if err != nil {
			return nil, nil, nil, classify(err)
		}
		return r, nil, raw, nil
	}

	w := s.waiter()
	defer w.close()

	var res *api.OverrideWriteResult
	var appRes *api.ApplicationOffResult
	var raw json.RawMessage

	if off {
		// What turning it off does is a dry run's answer, and so is whether it needs a confirmation.
		dr, dapp, draw, err := do(true, "")
		if err != nil {
			return err
		}
		if f.dryRun {
			res, appRes, raw = dr, dapp, draw
		} else {
			confirm := offConfirm(dr, dapp)
			if confirm != "" && confirm != f.confirm {
				return e.refuseOffConfirmation(cmd, s, dr, dapp, draw)
			}
			// The robot has to be reachable before anything is written (--wait=applied).
			if err := w.prepareOffOn(ctx, nil); err != nil {
				return err
			}
			writeConfirm := ""
			if confirm != "" {
				writeConfirm = confirm
			}
			if res, appRes, raw, err = do(false, writeConfirm); err != nil {
				return err
			}
		}
	} else {
		if !f.dryRun {
			// HelmCharts to take a baseline of: the item, or the application's items.
			ids := []string{id}
			if f.app != "" {
				ids = resourcesOfApp(view, repo, path)
			}
			if err := w.prepareOffOn(ctx, ids); err != nil {
				return err
			}
		}
		if res, appRes, raw, err = do(f.dryRun, ""); err != nil {
			return err
		}
	}

	ids := offIDs(res, appRes, id)
	if f.app != "" && len(ids) == 0 {
		ids = resourcesOfApp(view, repo, path)
	}
	impacts := offImpacts(res, appRes)
	info := writeInfo{off: &offWait{ids: ids, on: !off}, stamps: map[string]string{}}
	if off && s.level == waitApplied {
		for _, i := range ids {
			if g := goneFor(i, impacts); g != nil {
				info.off.gone = append(info.off.gone, g)
			}
		}
	}
	if !off {
		for _, i := range ids {
			if _, _, _, err := checkID(i); err == nil {
				info.resources = append(info.resources, i)
			}
		}
	}

	return s.finishOff(res, raw, impactWarnings(impacts), info, w, func(out io.Writer) {
		if off {
			printOffWrite(out, ids, target, res != nil && res.DryRun, impacts)
		} else {
			printOnWrite(out, ids, target, res != nil && res.DryRun, impacts)
		}
		for _, sk := range skippedOf(appRes) {
			fmt.Fprintf(out, "skipped: %s (%s)\n", sk.Identity, sk.Reason)
		}
	})
}

// finishOff prints the result of an off or on write, follows it with --wait and prints the structured
// answer. A write that changed nothing (the platform's noop) is not waited for.
func (s *session) finishOff(res *api.OverrideWriteResult, raw json.RawMessage, warnings []string, info writeInfo, w *waiter, human func(io.Writer)) error {
	out, errw := s.cmd.OutOrStdout(), s.cmd.ErrOrStderr()
	for _, wn := range warnings {
		fmt.Fprintln(errw, "warning:", wn)
	}
	if res != nil {
		for _, wn := range res.Warnings {
			fmt.Fprintln(errw, "warning:", wn)
		}
	}
	if s.format == "table" {
		human(out)
	}
	waiting := w != nil && !s.dryRun && s.level != waitNone && (res == nil || !res.Noop)
	var werr error
	start := s.e.clock()
	switch {
	case waiting:
		defer w.close()
		werr = w.wait(s.cmd.Context(), info)
	case w != nil:
		w.close()
	}
	if s.format != "table" {
		raw = addWarnings(raw, warnings)
		if !s.dryRun && s.level != waitNone {
			ws := map[string]any{"level": s.level, "reached": werr == nil, "seconds": int(s.e.clock().Sub(start).Seconds())}
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
}

// ---------- what was written ----------

// offIDs are the items a write touched: the platform's answer, else the resource that was asked for.
func offIDs(res *api.OverrideWriteResult, app *api.ApplicationOffResult, id string) []string {
	if app != nil {
		if len(app.IDs) > 0 {
			return dedupe(app.IDs)
		}
		return nil
	}
	switch {
	case res != nil && res.ID != "":
		return []string{res.ID}
	case id != "":
		return []string{id}
	}
	return nil
}

// offImpacts are the impacts of a write, whatever route it took.
func offImpacts(res *api.OverrideWriteResult, app *api.ApplicationOffResult) []api.OffImpact {
	if app != nil {
		return app.Impacts
	}
	if res != nil && res.Impact != nil {
		return []api.OffImpact{*res.Impact}
	}
	return nil
}

// offConfirm is the text the write needs, "" when it needs none.
func offConfirm(res *api.OverrideWriteResult, app *api.ApplicationOffResult) string {
	if app != nil {
		return app.Confirm
	}
	if res != nil && res.Impact != nil {
		return res.Impact.Confirm
	}
	return ""
}

func skippedOf(app *api.ApplicationOffResult) []api.OffSkipped {
	if app == nil {
		return nil
	}
	return app.Skipped
}

// impactFor finds the impact of an item: by identity, else by kind and release name.
func impactFor(id string, impacts []api.OffImpact) *api.OffImpact {
	kind, _, name, err := checkID(id)
	if err != nil {
		return nil
	}
	for i := range impacts {
		im := &impacts[i]
		switch {
		case im.Identity == id:
			return im
		case im.Release != nil && im.Release.Name == name:
			return im
		case im.Kind == kind && strings.Contains(im.Identity, "-"+name+"-"):
			return im
		}
	}
	return nil
}

// goneFor builds the item to watch disappear from the robot after an off.
func goneFor(id string, impacts []api.OffImpact) *goneItem {
	kind, ns, name, err := checkID(id)
	if err != nil {
		return nil
	}
	g := &goneItem{id: id, kind: kind, ns: ns, name: name, targetNS: ns}
	if im := impactFor(id, impacts); im != nil && im.Release != nil {
		if k8sName.MatchString(im.Release.TargetNamespace) {
			g.targetNS = im.Release.TargetNamespace
		}
		if k8sName.MatchString(im.Release.Name) {
			g.releaseName = im.Release.Name
		}
	}
	return g
}

// ---------- the confirmation ----------

// refuseOffConfirmation prints what turning the item off does and returns the usage error (exit 2)
// that asks for --confirm. Nothing has been written: the answer printed is the dry run's.
func (e *cliEnv) refuseOffConfirmation(cmd *cobra.Command, s *session, res *api.OverrideWriteResult, app *api.ApplicationOffResult, raw json.RawMessage) error {
	errw := cmd.ErrOrStderr()
	impacts := offImpacts(res, app)
	if s.format == "table" {
		for _, im := range impacts {
			printImpact(errw, im)
		}
	} else if err := writeStructured(errw, s.format, raw); err != nil {
		// stderr, not stdout: a refusal must not leave an answer on stdout that a script could take
		// for the result of the write
		return err
	}
	ids := offIDs(res, app, "")
	return usageErr("%s", offConfirmMessage(ids, impacts, offConfirm(res, app)))
}

// offConfirmMessage is the line that asks for --confirm.
func offConfirmMessage(ids []string, impacts []api.OffImpact, confirm string) string {
	var reasons []string
	for _, im := range impacts {
		reasons = append(reasons, im.DataReasons...)
	}
	why := "its data is deleted"
	if r := dedupe(reasons); len(r) > 0 {
		why = strings.Join(r, "; ")
	}
	return fmt.Sprintf("Turning off %s deletes data on the robot (%s). Run it again with --confirm %s to go ahead.", whatIDs(ids), why, confirm)
}

// ---------- table output ----------

// whatIDs names what a write touched in one phrase.
func whatIDs(ids []string) string {
	switch len(ids) {
	case 0:
		return "the application"
	case 1:
		return shownID(ids[0])
	}
	return fmt.Sprintf("%d resources", len(ids))
}

func printOffWrite(out io.Writer, ids []string, target string, dry bool, impacts []api.OffImpact) {
	if dry {
		fmt.Fprintf(out, "Would turn off %s on %s.\n", whatIDs(ids), target)
	} else {
		fmt.Fprintf(out, "Turned off %s on %s: sent to the robot.\n", whatIDs(ids), target)
	}
	printIDs(out, ids)
	for _, im := range impacts {
		printImpact(out, im)
	}
}

func printOnWrite(out io.Writer, ids []string, target string, dry bool, impacts []api.OffImpact) {
	if dry {
		fmt.Fprintf(out, "Would turn on %s on %s.\n", whatIDs(ids), target)
	} else {
		fmt.Fprintf(out, "Turned on %s on %s: sent to the robot.\n", whatIDs(ids), target)
	}
	printIDs(out, ids)
	for _, im := range impacts {
		printOnImpact(out, im)
	}
}

// printIDs lists the items of a write that touched several, one per line.
func printIDs(out io.Writer, ids []string) {
	if len(ids) < 2 {
		return
	}
	for _, id := range ids {
		fmt.Fprintf(out, "  %s\n", shownID(id))
	}
}

// sourceLine is "from <layer> <source> (url)".
func sourceLine(prefix string, s *api.OffSource) string {
	if s == nil || (s.Layer == "" && s.Source == "" && s.URL == "") {
		return ""
	}
	line := prefix + " " + strings.TrimSpace(s.Layer+" "+s.Source)
	if s.URL != "" {
		line += " (" + s.URL + ")"
	}
	return strings.TrimRight(line, " ")
}

// printImpact prints what turning one item off does on the robot.
func printImpact(out io.Writer, im api.OffImpact) {
	if l := sourceLine("  from", im.Source); l != "" {
		fmt.Fprintln(out, l)
	}
	if im.Kind == "HelmChart" && im.Release != nil {
		fmt.Fprintf(out, "  uninstalls release %s in %s\n", orDash(im.Release.Name), orDash(im.Release.TargetNamespace))
	}
	switch im.DeletesData {
	case "yes", "unknown":
		line := "  deletes data: " + im.DeletesData
		if len(im.DataReasons) > 0 {
			line += " (" + strings.Join(im.DataReasons, "; ") + ")"
		}
		fmt.Fprintln(out, line)
	}
	for _, d := range im.Dependents {
		fmt.Fprintf(out, "  uses it: %s\n", d)
	}
	for _, k := range im.Keeps {
		fmt.Fprintf(out, "  stays: %s\n", k)
	}
}

// printOnImpact prints where an item turned on again is installed from.
func printOnImpact(out io.Writer, im api.OffImpact) {
	if l := sourceLine("  reinstalls from", im.Source); l != "" {
		fmt.Fprintln(out, l)
	}
	if im.Kind == "HelmChart" && im.Release != nil {
		line := fmt.Sprintf("  installs release %s in %s", orDash(im.Release.Name), orDash(im.Release.TargetNamespace))
		if im.Release.Chart != "" {
			line += " (" + strings.TrimSpace(im.Release.Chart+" "+im.Release.Version) + ")"
		}
		fmt.Fprintln(out, line)
	}
	for _, k := range im.Keeps {
		fmt.Fprintf(out, "  stays: %s\n", k)
	}
}

// impactWarnings collects the warnings of the impacts, to print to stderr like the platform's own.
func impactWarnings(impacts []api.OffImpact) []string {
	var out []string
	for _, im := range impacts {
		out = append(out, im.Warnings...)
	}
	return out
}

// ---------- waiting ----------

// prepareOffOn runs before an off or on write: for applied and ready it makes sure the robot is
// reachable before anything is written, and takes the baseline of the HelmCharts (so that a new
// helm Job can be told from the old one).
func (w *waiter) prepareOffOn(ctx context.Context, helmIDs []string) error {
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
	return w.prepare(ctx, helmIDs)
}

// waitOffPublished waits until the controller reports the off (or on) of every written key.
func (w *waiter) waitOffPublished(ctx context.Context, o *offWait) error {
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
			ok, msg, perr := offStates(v, o)
			if perr != nil {
				return perr
			}
			if ok {
				return nil
			}
			w.progress("", "published: "+msg)
		}
		if err := w.pause(ctx); err != nil {
			return err
		}
	}
}

// offStates judges one view for an off (or on) wait: (reached, what is missing, error). An invalid or
// not-applicable state ends the wait with exit 5: the controller refused, the item stays as it was.
func offStates(v *api.ConfigTargetView, o *offWait) (bool, string, error) {
	states := map[string]api.OverrideStatus{}
	for _, s := range v.OverrideStatus {
		states[s.Target] = s
	}
	rows := map[string]string{}
	for _, r := range v.Configuration.Effective {
		rows[r.ID()] = r.Status
	}
	for _, id := range o.ids {
		st := states[id]
		isOff := st.State == api.StateOff || rows[id] == api.StatusOff
		if o.on {
			if isOff {
				return false, fmt.Sprintf("waiting for the controller to install %s again (it is off)", shownID(id)), nil
			}
			if st.State == api.StateInvalid || st.State == api.StateNotApplicable {
				return false, "", newErr(codeRejected, "%s was not turned on: the platform reports it as %s", shownID(id), stateMessage(st))
			}
			continue
		}
		if st.State == api.StateOff {
			continue
		}
		if st.State == api.StateInvalid || st.State == api.StateNotApplicable {
			return false, "", newErr(codeRejected, "%s was not turned off: the platform reports it as %s (it stays on the robot)", shownID(id), stateMessage(st))
		}
		return false, fmt.Sprintf("waiting for the controller to turn %s off (state: %s)", shownID(id), orDash(st.State)), nil
	}
	return true, "", nil
}

func stateMessage(s api.OverrideStatus) string {
	if s.Message != "" {
		return s.State + ": " + s.Message
	}
	return s.State
}

// stampExpectations reads the config-changed-at stamp of every item in the configuration published
// after an on: the robot's copy must carry it. An item without a stamp is checked on its content.
func (w *waiter) stampExpectations(ctx context.Context, info writeInfo) {
	for _, id := range info.resources {
		d, _, err := w.fl.ConfigResource(ctx, w.target, id, false)
		if err != nil {
			continue // the check on the device reports what is wrong with the item
		}
		if st := digStr(d.Rendered, "metadata", "annotations", changedAtAnnotation); st != "" {
			info.stamps[id] = st
		}
	}
}
