package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/textdiff"
	"github.com/teknoir/cli/pkg/valuepath"
	"go.yaml.in/yaml/v3"
)

// session is one write command: the client, the output format and the --wait settings.
type session struct {
	e      *cliEnv
	cmd    *cobra.Command
	fl     *api.Fleet
	domain string
	ns     string
	target string
	format string
	level  waitLevel
	limit  time.Duration
	dryRun bool
}

// begin validates the shared flags and opens the fleet client.
func (e *cliEnv) begin(cmd *cobra.Command, target string, wf *waitFlags, dryRun bool) (*session, error) {
	format, err := outputFormat()
	if err != nil {
		return nil, err
	}
	level, err := wf.level()
	if err != nil {
		return nil, err
	}
	if dryRun && level != waitNone {
		return nil, usageErr("--dry-run writes nothing, so there is nothing to --wait for")
	}
	cmd.SilenceUsage = true
	fl, domain, ns, err := e.targetClient()
	if err != nil {
		return nil, err
	}
	return &session{e: e, cmd: cmd, fl: fl, domain: domain, ns: ns, target: target, format: format,
		level: level, limit: wf.timeout, dryRun: dryRun}, nil
}

func (s *session) waiter() *waiter {
	return s.e.newWaiter(s.fl, s.domain, s.ns, s.target, s.level, s.limit, s.cmd.ErrOrStderr())
}

// finish prints the result of a write, follows it with --wait and returns the wait's error.
// human prints the table-mode result.
func (s *session) finish(res *api.OverrideWriteResult, raw json.RawMessage, warnings []string, info writeInfo, w *waiter, human func(io.Writer)) error {
	out, errw := s.cmd.OutOrStdout(), s.cmd.ErrOrStderr()
	for _, wn := range warnings {
		fmt.Fprintln(errw, "warning:", wn)
	}
	for _, wn := range res.Warnings {
		fmt.Fprintln(errw, "warning:", wn)
	}
	if s.format == "table" {
		human(out)
	}
	var werr error
	start := s.e.clock()
	if w != nil && !s.dryRun && s.level != waitNone {
		defer w.close()
		info.revision, info.storage = res.Revision, res.Storage
		if info.revision == "" {
			if v, _, err := s.fl.ConfigTarget(s.cmd.Context(), s.target); err == nil {
				info.revision = v.Overrides.Revision
			}
		}
		werr = w.wait(s.cmd.Context(), info)
	}
	if s.format != "table" {
		raw = addWarnings(raw, warnings)
		if w != nil && !s.dryRun && s.level != waitNone {
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

// ---------- config set ----------

type setFlags struct {
	images  []string
	version string
	str     bool
	note    string
	file    string
	dryRun  bool
	wait    waitFlags
}

func newConfigSetCmd(e *cliEnv) *cobra.Command {
	var f setFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "set <target> <resource> [k.path=value...] [--image [ctr=]repo:tag] [--version X] [-f values.yaml]",
		Short:        "Override chart values, the chart version or a container image on one robot",
		Long: `Override a resource on one robot. The change wins over every git layer, is published to the
robot by the platform and stays until you 'unset' or 'reset' it.

  k.path=value     a chart value (HelmChart). Dot separated path, \. for a dot in a key.
                   Values are YAML 1.1 scalars like Helm reads them (on is true, 010 is 8; you get a
                   warning). Keys that name text (tag, repository, version, name, ...) and values
                   whose current value is a string stay strings; --string keeps all of them as text.
                   k= is the empty string. null writes null, which deletes the key from git and the
                   chart default. Lists and maps are YAML flow, a list replaces the whole list.
  --image X        change an image. X is repo:tag, :tag (only the tag), repo (keeps the tag) or
                   repo@sha256:...; prefix path= or container= to pick one (several images). The
                   platform finds the chart's image keys (HelmChart) or sets the container's image.
  --version V      chart version (HelmChart)
  -f values.yaml   replace the value overrides of this resource with the file (- reads stdin)
  --note TEXT      why; shown to everyone who looks at the robot
  --dry-run        show what would change in the effective resource, write nothing
  --wait[=LEVEL]   published (the platform has it), applied (the robot has it), ready (pods Ready
                   and stable); a bare --wait is ready. Write --wait=applied, not --wait applied.

Exit 8 means someone changed the same robot's overrides at the same time: run the command again.`,
		Example: `  tnctl config set vision-station-01 HelmChart/default/notebook image.tag=pr-123 --wait
  tnctl config set vision-station-01 notebook --image :pr-123 --note "try pr-123" --wait=applied
  tnctl config set vision-station-01 notebook replicas=2 resources.limits.memory=2Gi --dry-run
  tnctl config set vision-station-01 notebook 'tolerations=[{key: gpu, operator: Exists}]'
  tnctl config set vision-station-01 notebook -f my-values.yaml
  tnctl config set vision-station-01 Deployment/default/whoami --image traefik/whoami:v1.11.0`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return usageErr("set needs a robot or device name and a resource, then what to change, for example: tnctl config set vision-station-01 notebook image.tag=pr-123\nusage: %s", cmd.UseLine())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error { return e.runSet(cmd, args, &f) },
	}
	c.Flags().StringArrayVar(&f.images, "image", nil, "change an image: [path|container=]repo:tag, :tag or repo@sha256:...")
	c.Flags().StringVar(&f.version, "version", "", "chart version (HelmChart)")
	c.Flags().BoolVar(&f.str, "string", false, "keep every k=v value as text")
	c.Flags().StringVar(&f.note, "note", "", "why this override exists")
	c.Flags().StringVarP(&f.file, "file", "f", "", "replace the value overrides with this YAML file (- for stdin)")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "show the effective change, write nothing")
	f.wait.add(c)
	return c
}

// lookupHint explains a mistyped --wait value that ended up as an argument.
func lookupHint(arg string) string {
	switch arg {
	case "published", "applied", "ready":
		return fmt.Sprintf("; did you mean --wait=%s? (a value after --wait needs the =)", arg)
	}
	return ""
}

func (e *cliEnv) runSet(cmd *cobra.Command, args []string, f *setFlags) error {
	target, resArg, pairs := args[0], args[1], args[2:]
	for _, p := range pairs {
		if !strings.Contains(p, "=") {
			return usageErr("%q is not k.path=value%s", p, lookupHint(p))
		}
	}
	if len(pairs) == 0 && len(f.images) == 0 && f.version == "" && f.file == "" && f.note == "" {
		return usageErr("nothing to set: give k.path=value, --image, --version, -f or --note")
	}
	if f.file != "" && len(f.images) > 0 {
		return usageErr("-f replaces the value overrides and cannot be combined with --image; run two commands")
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
	kind, _, _, err := checkID(id)
	if err != nil {
		return err
	}
	if kind != "HelmChart" && (len(pairs) > 0 || f.file != "" || f.version != "") {
		return usageErr("%s is a %s: values and version apply to HelmCharts. Use --image to change the image of a workload", id, kind)
	}

	var detail *api.ConfigResourceDetail
	var fetched bool
	getDetail := func() *api.ConfigResourceDetail {
		if !fetched {
			fetched = true
			detail, _, _ = s.fl.ConfigResource(ctx, target, id, false)
		}
		return detail
	}
	existing := func(segs []string) (any, bool) {
		d := getDetail()
		if d == nil || d.Resource.Values == nil {
			return nil, false
		}
		return valuepath.GetIn(d.Resource.Values.Merged, segs)
	}
	var assigns []valuepath.Assignment
	var warnings []string
	scratch := map[string]any{}
	for _, p := range pairs {
		a, err := valuepath.ParseAssignment(p, valuepath.Options{String: f.str, Existing: existing})
		if err != nil {
			return usageErr("%v", err)
		}
		// Catch "a=1 a.b=2" before the platform does.
		if a.Value != nil {
			if err := valuepath.SetIn(scratch, a.Segments, a.Value); err != nil {
				return usageErr("%s: %v", p, err)
			}
		}
		if a.Warning != "" {
			warnings = append(warnings, a.Warning)
		}
		assigns = append(assigns, a)
	}

	info := writeInfo{statusTargets: []string{id}, resources: []string{id}}
	var res *api.OverrideWriteResult
	var raw json.RawMessage

	w := s.waiter()
	if !f.dryRun {
		if err := w.prepare(ctx, []string{id}); err != nil {
			w.close()
			return err
		}
	}

	if f.file != "" {
		body, err := e.fileOverride(cmd, f, getDetail(), assigns)
		if err != nil {
			w.close()
			return err
		}
		res, raw, err = s.fl.PutResourceOverride(ctx, target, id, body, f.dryRun)
		if err != nil {
			w.close()
			return classify(err)
		}
	} else {
		patch := api.OverrideResourcePatch{Version: f.version, Note: f.note}
		for _, a := range assigns {
			if a.Value == nil {
				patch.Ops = append(patch.Ops, api.ValueOp{Op: "delete", Path: a.Path})
			} else {
				patch.Ops = append(patch.Ops, api.ValueOp{Op: "set", Path: a.Path, Value: a.Value})
			}
		}
		if err := applyImageFlags(&patch, kind, f.images, &info); err != nil {
			w.close()
			return err
		}
		res, raw, err = s.fl.PatchResourceOverride(ctx, target, id, patch, f.dryRun)
		if err != nil {
			w.close()
			return classify(err)
		}
	}
	if res.ID != "" {
		id = res.ID
	}
	return s.finish(res, raw, warnings, info, w, func(out io.Writer) {
		printWriteResult(out, res, id, s.target, "override set")
	})
}

// applyImageFlags fills the image part of a patch from the --image flags.
func applyImageFlags(p *api.OverrideResourcePatch, kind string, flags []string, info *writeInfo) error {
	if len(flags) == 0 {
		return nil
	}
	type img struct{ path, value string }
	var imgs []img
	for _, fl := range flags {
		path, value := "", fl
		if i := strings.Index(fl, "="); i >= 0 {
			path, value = fl[:i], fl[i+1:]
		}
		if value == "" || strings.ContainsAny(value, " \t\n") {
			return usageErr("--image %q: expected [path|container=]repo:tag, :tag or repo@sha256:...", fl)
		}
		imgs = append(imgs, img{path, value})
	}
	if len(imgs) == 1 {
		p.Image = &api.ImageChange{Path: imgs[0].path, Value: imgs[0].value}
		info.image = map[string]string{}
		for _, id := range info.resources {
			info.image[id] = imgs[0].path
		}
		return nil
	}
	if kind == "HelmChart" {
		return usageErr("a HelmChart takes one --image per command; set the others as values, for example discoveryServer.image.tag=v2")
	}
	p.Images = map[string]*string{}
	for _, im := range imgs {
		if im.path == "" || strings.HasPrefix(im.value, ":") {
			return usageErr("with several --image give container=repo:tag for each, with the full image")
		}
		v := im.value
		p.Images[im.path] = &v
	}
	info.image = map[string]string{}
	for _, id := range info.resources {
		info.image[id] = imgs[0].path
	}
	return nil
}

// fileOverride builds the whole override for `set -f`: the file (with k=v applied) as
// valuesContent, keeping the version and note of the current override unless flags change them.
func (e *cliEnv) fileOverride(cmd *cobra.Command, f *setFlags, cur *api.ConfigResourceDetail, assigns []valuepath.Assignment) (api.ResourceOverride, error) {
	var data []byte
	var err error
	if f.file == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(f.file)
	}
	if err != nil {
		return api.ResourceOverride{}, usageErr("cannot read %s: %v", f.file, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return api.ResourceOverride{}, usageErr("%s is not valid YAML: %v", f.file, firstLine(err))
	}
	if m == nil && strings.TrimSpace(string(data)) != "" {
		return api.ResourceOverride{}, usageErr("%s must be a YAML map of chart values", f.file)
	}
	content := string(data)
	if len(assigns) > 0 {
		// Edit the file's YAML in place: comments and the spelling of scalars (yes, on) are
		// the user's, and Helm reads them as YAML 1.1.
		b, err := valuepath.ApplyToYAML(data, assigns)
		if err != nil {
			return api.ResourceOverride{}, usageErr("%s: %v", f.file, firstLine(err))
		}
		content = string(b)
	}
	body := api.ResourceOverride{ValuesContent: content, Version: f.version, Note: f.note}
	if cur != nil && cur.Override != nil {
		if body.Version == "" {
			body.Version = cur.Override.Version
		}
		if body.Note == "" {
			body.Note = cur.Override.Note
		}
	}
	return body, nil
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// printWriteResult is the table-mode answer of a write.
func printWriteResult(out io.Writer, res *api.OverrideWriteResult, id, target, what string) {
	if res.DryRun {
		fmt.Fprintf(out, "Dry run, nothing written: %s on %s\n", id, target)
		d := textdiff.Unified("before", "after", effectiveSummary(res.Before), effectiveSummary(res.After))
		if d == "" {
			fmt.Fprintln(out, "No change to the effective resource.")
		} else {
			fmt.Fprint(out, d)
		}
		return
	}
	fmt.Fprintf(out, "%s: %s on %s", what, id, target)
	if res.Revision != "" {
		fmt.Fprintf(out, " (revision %s)", res.Revision)
	}
	fmt.Fprintln(out)
}

// ---------- config unset ----------

type unsetFlags struct {
	images  []string
	version bool
	dryRun  bool
	wait    waitFlags
}

func newConfigUnsetCmd(e *cliEnv) *cobra.Command {
	var f unsetFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "unset <target> <resource> [k.path...] [--image container] [--version]",
		Short:        "Remove parts of an override: the git value comes back",
		Long: `Remove keys from the override of a resource, so the value git yields comes back. With no keys and
no flags, the whole override of the resource is removed (like 'reset <target> <resource>').

  k.path           a chart value override to remove
  --image C        remove the image override of container C (workloads)
  --version        remove the chart version override

To make a value disappear from the robot (also the chart's own default), use 'tnctl config delete'.`,
		Example: `  tnctl config unset vision-station-01 notebook image.tag
  tnctl config unset vision-station-01 Deployment/default/whoami --image whoami
  tnctl config unset vision-station-01 notebook --version
  tnctl config unset vision-station-01 notebook`,
		Args: argsRange(2, -1, "unset needs a robot or device name and a resource"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, resArg, keys := args[0], args[1], args[2:]
			if err := f.wait.misplaced(keys); err != nil {
				return err
			}
			whole := len(keys) == 0 && len(f.images) == 0 && !f.version
			if whole && f.dryRun {
				return usageErr("removing the whole override has no --dry-run; see what it changes with: tnctl config diff %s %s", target, resArg)
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
			kind, _, _, err := checkID(id)
			if err != nil {
				return err
			}
			info := writeInfo{statusTargets: []string{id}, resources: []string{id}, removed: whole, versionCleared: f.version || whole}
			w := s.waiter()
			if err := w.prepare(ctx, []string{id}); err != nil {
				w.close()
				return err
			}
			var res *api.OverrideWriteResult
			var raw json.RawMessage
			if whole {
				res, raw, err = s.fl.DeleteResourceOverride(ctx, target, id)
			} else {
				if kind != "HelmChart" && (len(keys) > 0 || f.version) {
					w.close()
					return usageErr("%s is a %s: values and version apply to HelmCharts; remove an image override with --image <container>", id, kind)
				}
				if kind == "HelmChart" && len(f.images) > 0 {
					w.close()
					return usageErr("a HelmChart's image is a value: unset its key, for example: tnctl config unset %s %s image.tag", target, resArg)
				}
				patch := api.OverrideResourcePatch{ClearVersion: f.version}
				for _, k := range keys {
					segs, perr := valuepath.ParsePath(k)
					if perr != nil {
						w.close()
						return usageErr("%v", perr)
					}
					patch.Ops = append(patch.Ops, api.ValueOp{Op: "unset", Path: valuepath.EscapePath(segs)})
				}
				if len(f.images) > 0 {
					patch.Images = map[string]*string{}
					for _, c := range f.images {
						patch.Images[c] = nil
					}
				}
				res, raw, err = s.fl.PatchResourceOverride(ctx, target, id, patch, f.dryRun)
			}
			if err != nil {
				w.close()
				return classify(err)
			}
			return s.finish(res, raw, nil, info, w, func(out io.Writer) {
				printWriteResult(out, res, id, target, "override removed")
			})
		},
	}
	c.Flags().StringArrayVar(&f.images, "image", nil, "container whose image override to remove")
	c.Flags().BoolVar(&f.version, "version", false, "remove the chart version override")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "show the effective change, write nothing")
	f.wait.add(c)
	return c
}

// ---------- config delete ----------

func newConfigDeleteCmd(e *cliEnv) *cobra.Command {
	var f struct {
		dryRun bool
		note   string
		wait   waitFlags
	}
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "delete <target> <resource> k.path...",
		Short:        "Remove a value from the robot: writes null, which also drops the chart's default",
		Long: `Delete chart values on this robot only. The override is a null: the key disappears from every git
layer and Helm then also drops the chart's own default for it. To take back an override of yours
(the git value returns), use 'tnctl config unset'.`,
		Example: `  tnctl config delete vision-station-01 notebook nodeSelector.teknoir\.org/node-role
  tnctl config delete vision-station-01 HelmChart/default/notebook resources.limits --dry-run`,
		Args: argsRange(3, -1, "delete needs a robot or device name, a resource and at least one key"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, resArg, keys := args[0], args[1], args[2:]
			if err := f.wait.misplaced(keys); err != nil {
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
			kind, _, _, err := checkID(id)
			if err != nil {
				return err
			}
			if kind != "HelmChart" {
				return usageErr("%s is a %s: values apply to HelmCharts", id, kind)
			}
			patch := api.OverrideResourcePatch{Note: f.note}
			for _, k := range keys {
				segs, perr := valuepath.ParsePath(k)
				if perr != nil {
					return usageErr("%v", perr)
				}
				patch.Ops = append(patch.Ops, api.ValueOp{Op: "delete", Path: valuepath.EscapePath(segs)})
			}
			w := s.waiter()
			if !f.dryRun {
				if err := w.prepare(ctx, []string{id}); err != nil {
					w.close()
					return err
				}
			}
			res, raw, err := s.fl.PatchResourceOverride(ctx, target, id, patch, f.dryRun)
			if err != nil {
				w.close()
				return classify(err)
			}
			info := writeInfo{statusTargets: []string{id}, resources: []string{id}}
			return s.finish(res, raw, nil, info, w, func(out io.Writer) {
				printWriteResult(out, res, id, target, "values deleted")
			})
		},
	}
	c.Flags().StringVar(&f.note, "note", "", "why this override exists")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "show the effective change, write nothing")
	f.wait.add(c)
	return c
}

// ---------- config reset ----------

func newConfigResetCmd(e *cliEnv) *cobra.Command {
	var f struct {
		all  bool
		wait waitFlags
	}
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "reset <target> [<resource> | --all]",
		Short:        "Put a resource, or the whole robot, back to what git says",
		Long: `Remove the override of one resource, or with --all every override of the robot: values, versions,
images and tried revisions. What git yields is applied again. 'tnctl config diff' shows beforehand
what a reset changes.`,
		Example: `  tnctl config reset vision-station-01 HelmChart/default/notebook
  tnctl config reset vision-station-01 --all --wait=applied`,
		Args: argsRange(1, 2, "reset needs a robot or device name and a resource, or --all"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if err := f.wait.misplaced(args[1:]); err != nil {
				return err
			}
			if (len(args) == 2) == f.all {
				return usageErr("reset needs either a resource or --all (it will not guess)")
			}
			s, err := e.begin(cmd, target, &f.wait, false)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			info := writeInfo{removed: true, versionCleared: true}
			var res *api.OverrideWriteResult
			var raw json.RawMessage
			var w *waiter
			var what string
			if f.all {
				view, _, err := s.fl.ConfigTarget(ctx, target)
				if err != nil {
					return classify(err)
				}
				info.statusTargets, info.resources = overrideTargets(view)
				w = s.waiter()
				if err := w.prepare(ctx, info.resources); err != nil {
					w.close()
					return err
				}
				res, raw, err = s.fl.DeleteAllOverrides(ctx, target)
				if err != nil {
					w.close()
					return classify(err)
				}
				what = "all overrides"
			} else {
				id, _, err := resolveTarget(ctx, s.fl, target, args[1])
				if err != nil {
					return classify(err)
				}
				if _, _, _, err := checkID(id); err != nil {
					return err
				}
				info.statusTargets, info.resources = []string{id}, []string{id}
				w = s.waiter()
				if err := w.prepare(ctx, info.resources); err != nil {
					w.close()
					return err
				}
				res, raw, err = s.fl.DeleteResourceOverride(ctx, target, id)
				if err != nil {
					w.close()
					return classify(err)
				}
				what = id
			}
			return s.finish(res, raw, nil, info, w, func(out io.Writer) {
				fmt.Fprintf(out, "Reset to git: %s on %s", what, target)
				if res.Revision != "" {
					fmt.Fprintf(out, " (revision %s)", res.Revision)
				}
				fmt.Fprintln(out)
			})
		},
	}
	c.Flags().BoolVar(&f.all, "all", false, "reset every override of the robot")
	f.wait.add(c)
	return c
}

// overrideTargets lists the status targets of all overrides of a view and the effective
// resources that carry one (only those can be checked on the device). A resource that is turned
// off on the robot is not on it: applied and ready must not wait for that one.
func overrideTargets(v *api.ConfigTargetView) (targets, resources []string) {
	effective := map[string]string{} // id -> status of the effective row
	for _, r := range v.Configuration.Effective {
		effective[r.ID()] = r.Status
	}
	on := func(id string) bool { st, ok := effective[id]; return ok && st != api.StatusOff }
	for id := range v.Overrides.Resources {
		targets = append(targets, id)
		if on(id) {
			resources = append(resources, id)
		}
	}
	sort.Strings(targets)
	sort.Strings(resources)
	for _, a := range v.Overrides.Applications {
		targets = append(targets, appKey(a.RepoURL, a.Path))
		for _, id := range resourcesOfApp(v, a.RepoURL, a.Path) {
			if on(id) {
				resources = append(resources, id)
			}
		}
	}
	return targets, dedupe(resources)
}

// appResourcesAfter recomputes the resources of an application from the view published after
// a change of its revision: the new revision may add, rename or drop manifests. Resources the
// change started with that are still effective stay (an effective row with a resource override has
// no application of its own, so resourcesOfApp cannot find it).
func appResourcesAfter(repo, path string, before []string) func(*api.ConfigTargetView) []string {
	return func(v *api.ConfigTargetView) []string {
		ids := resourcesOfApp(v, repo, path)
		effective := map[string]bool{}
		for _, r := range v.Configuration.Effective {
			effective[r.ID()] = true
		}
		for _, id := range before {
			if effective[id] {
				ids = append(ids, id)
			}
		}
		out := dedupe(ids)
		sort.Strings(out)
		return out
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------- try / untry ----------

func normRepo(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimSuffix(u, "/")
	return strings.TrimSuffix(u, ".git")
}

func normAppPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "." {
		return ""
	}
	return p
}

// knownApps lists the applications of a target: from layers, sync status and overrides.
func knownApps(v *api.ConfigTargetView) []api.GitOpsApplicationRef {
	var apps []api.GitOpsApplicationRef
	seen := map[string]bool{}
	add := func(a api.GitOpsApplicationRef) {
		k := normRepo(a.RepoURL) + "#" + normAppPath(a.Path)
		if !seen[k] {
			seen[k] = true
			apps = append(apps, a)
		}
	}
	for _, r := range v.Configuration.Resources {
		if r.Application != nil {
			add(r.Application.GitOpsApplicationRef)
		}
	}
	for _, s := range v.Configuration.Sync {
		add(s.Application.GitOpsApplicationRef)
	}
	for _, a := range v.Overrides.Applications {
		add(api.GitOpsApplicationRef{RepoURL: a.RepoURL, Path: a.Path})
	}
	return apps
}

// resourcesOfApp lists the effective resources that come from an application.
func resourcesOfApp(v *api.ConfigTargetView, repo, path string) []string {
	var ids []string
	for _, r := range v.Configuration.Effective {
		if r.Application == nil || normRepo(r.Application.RepoURL) != normRepo(repo) {
			continue
		}
		if p := normAppPath(path); p != "" && normAppPath(r.Application.Path) != p {
			continue
		}
		ids = append(ids, r.ID())
	}
	return ids
}

func resolveAppRepo(v *api.ConfigTargetView, repoFlag, path string) (string, error) {
	if repoFlag != "" {
		return repoFlag, nil
	}
	p := normAppPath(path)
	if p == "" {
		return "", usageErr("an empty application path means every application of a repo: give the repo with --repo")
	}
	repos := map[string]string{}
	var paths []string
	for _, a := range knownApps(v) {
		paths = append(paths, normAppPath(a.Path))
		if normAppPath(a.Path) == p {
			repos[normRepo(a.RepoURL)] = a.RepoURL
		}
	}
	switch len(repos) {
	case 1:
		for _, r := range repos {
			return r, nil
		}
	case 0:
		sort.Strings(paths)
		return "", newErr(codeNotFound, "no application with path %q on this target; its applications are %s (or give the repository with --repo)", path, strings.Join(dedupe(paths), ", "))
	}
	urls := make([]string, 0, len(repos))
	for _, r := range repos {
		urls = append(urls, r)
	}
	sort.Strings(urls)
	return "", usageErr("the path %q exists in several repositories (%s): choose with --repo", path, strings.Join(urls, ", "))
}

func newConfigTryCmd(e *cliEnv) *cobra.Command {
	var f struct {
		repo   string
		note   string
		dryRun bool
		wait   waitFlags
	}
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "try <target> <app-path> <revision> [--repo URL]",
		Short:        "Try another branch, tag or commit of an application on this robot",
		Long: `Run another revision of a git application on one robot, whichever layer the application comes
from. The application is replaced in place, so values of the old revision do not leak into the
new one. 'untry' goes back. <app-path> is the application's path in its repository (for example
apps/mock-vision); the repository is found from the robot's applications, or given with --repo.
Use "." as the path with --repo to try a revision of every application from that repository.

The revision must exist in the repository (branch, tag, or a commit SHA of 7 to 40 hex characters);
if it does not, the platform rejects it (exit 5) and the robot keeps its current revision.`,
		Example: `  tnctl config try vision-station-01 apps/mock-vision feature-x
  tnctl config try vision-station-01 apps/mock-vision v1.2.0 --note "release candidate" --wait=applied
  tnctl config try vision-station-01 . main --repo https://github.com/teknoir/teknoir-demo-gitops`,
		Args: argsRange(3, 3, "try needs a robot or device name, an application path and a revision"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, path, rev := args[0], args[1], args[2]
			s, err := e.begin(cmd, target, &f.wait, f.dryRun)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			view, _, err := s.fl.ConfigTarget(ctx, target)
			if err != nil {
				return classify(err)
			}
			repo, err := resolveAppRepo(view, f.repo, path)
			if err != nil {
				return err
			}
			p := normAppPath(path)
			info := writeInfo{statusTargets: []string{appKey(repo, p)}, resources: resourcesOfApp(view, repo, p)}
			info.resourcesAfter = appResourcesAfter(repo, p, info.resources)
			w := s.waiter()
			if !f.dryRun {
				if err := w.prepare(ctx, info.resources); err != nil {
					w.close()
					return err
				}
			}
			res, raw, err := s.fl.PutApplicationOverride(ctx, target, api.ApplicationOverride{RepoURL: repo, Path: p, Revision: rev, Note: f.note}, f.dryRun)
			if err != nil {
				w.close()
				return classify(err)
			}
			return s.finish(res, raw, nil, info, w, func(out io.Writer) {
				if res.DryRun {
					fmt.Fprintf(out, "Dry run, nothing written: %s %s would run revision %s on %s\n", repo, orDash(p), rev, target)
					return
				}
				fmt.Fprintf(out, "Trying revision %s of %s %s on %s", rev, repo, orDash(p), target)
				if res.Revision != "" {
					fmt.Fprintf(out, " (revision %s)", res.Revision)
				}
				fmt.Fprintln(out)
			})
		},
	}
	c.Flags().StringVar(&f.repo, "repo", "", "repository URL (default: found from the robot's applications)")
	c.Flags().StringVar(&f.note, "note", "", "why this override exists")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "check the revision, write nothing")
	f.wait.add(c)
	return c
}

func newConfigUntryCmd(e *cliEnv) *cobra.Command {
	var f struct {
		repo string
		wait waitFlags
	}
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "untry <target> <app-path> [--repo URL]",
		Short:        "Go back to the application's own revision",
		Long:         `Remove a 'try' of another revision: the application runs the revision its layer says again.`,
		Example: `  tnctl config untry vision-station-01 apps/mock-vision
  tnctl config untry vision-station-01 apps/mock-vision --wait=published`,
		Args: argsRange(2, 2, "untry needs a robot or device name and an application path"),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, path := args[0], args[1]
			s, err := e.begin(cmd, target, &f.wait, false)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			view, _, err := s.fl.ConfigTarget(ctx, target)
			if err != nil {
				return classify(err)
			}
			var matches []api.ApplicationOverride
			for _, a := range view.Overrides.Applications {
				if normAppPath(a.Path) != normAppPath(path) {
					continue
				}
				if f.repo != "" && normRepo(a.RepoURL) != normRepo(f.repo) {
					continue
				}
				matches = append(matches, a)
			}
			switch len(matches) {
			case 0:
				return newErr(codeNotFound, "%s has no tried revision for %q", target, path)
			case 1:
			default:
				urls := make([]string, len(matches))
				for i, m := range matches {
					urls[i] = m.RepoURL
				}
				return usageErr("%q is tried in several repositories (%s): choose with --repo", path, strings.Join(urls, ", "))
			}
			a := matches[0]
			info := writeInfo{statusTargets: []string{appKey(a.RepoURL, a.Path)}, removed: true, resources: resourcesOfApp(view, a.RepoURL, a.Path)}
			info.resourcesAfter = appResourcesAfter(a.RepoURL, a.Path, info.resources)
			w := s.waiter()
			if err := w.prepare(ctx, info.resources); err != nil {
				w.close()
				return err
			}
			res, raw, err := s.fl.DeleteApplicationOverride(ctx, target, a.RepoURL, a.Path)
			if err != nil {
				w.close()
				return classify(err)
			}
			return s.finish(res, raw, nil, info, w, func(out io.Writer) {
				fmt.Fprintf(out, "Back to the application's own revision: %s %s on %s\n", a.RepoURL, orDash(a.Path), target)
			})
		},
	}
	c.Flags().StringVar(&f.repo, "repo", "", "repository URL, when the path exists in several")
	f.wait.add(c)
	return c
}

// ---------- config wait ----------

func newConfigWaitCmd(e *cliEnv) *cobra.Command {
	var f struct {
		forLevel string
		timeout  time.Duration
	}
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "wait <target> [<resource>] [--for published|applied|ready] [--timeout 5m]",
		Short:        "Wait until the overrides of a robot are published, applied or ready",
		Long: `Wait for the state of the robot's overrides, without changing anything. Without a resource it
follows every resource that has an override.

  published  the platform evaluated the latest override write and published the configuration
  applied    the robot has it: HelmChart values/version on the device and the helm Job succeeded, or
             the workload has the image and the latest generation
  ready      applied, and the pods are Ready on the new image and have not restarted for 20 s

applied and ready look at the robot over WireGuard SSH. Exit code 4 on timeout, 7 when the robot
applied it but it fails (the helm Job failed, a pod is in CrashLoopBackOff or ImagePullBackOff).`,
		Example: `  tnctl config wait vision-station-01
  tnctl config wait vision-station-01 notebook --for applied --timeout 2m`,
		Args: argsRange(1, 2, "wait needs a robot or device name and optionally a resource"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			level, err := parseWaitLevel(f.forLevel)
			if err != nil {
				return err
			}
			if level == waitNone {
				level = waitReady
			}
			cmd.SilenceUsage = true
			fl, domain, ns, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			target := args[0]
			view, _, err := fl.ConfigTarget(ctx, target)
			if err != nil {
				return classify(err)
			}
			info := writeInfo{revision: view.Overrides.Revision}
			if len(args) == 2 {
				id, _, err := resolveTarget(ctx, fl, target, args[1])
				if err != nil {
					return classify(err)
				}
				info.statusTargets, info.resources = []string{id}, []string{id}
			} else {
				info.statusTargets, info.resources = overrideTargets(view)
				for _, r := range view.Configuration.Effective {
					// an item that is off is not on the robot: there is nothing to check on it
					if r.Override != nil && r.Override.Storage == api.StorageLocalItem && r.Status != api.StatusOff {
						info.resources = append(info.resources, r.ID())
					}
				}
				info.resources = dedupe(info.resources)
			}
			w := e.newWaiter(fl, domain, ns, target, level, f.timeout, cmd.ErrOrStderr())
			defer w.close()
			start := e.clock()
			if err := w.wait(ctx, info); err != nil {
				return err
			}
			res := map[string]any{"target": target, "level": level, "reached": true, "seconds": int(e.clock().Sub(start).Seconds()), "revision": info.revision}
			if format != "table" {
				return writeStructured(cmd.OutOrStdout(), format, marshalRaw(res))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", target, level)
			return nil
		},
	}
	c.Flags().StringVar(&f.forLevel, "for", "ready", "what to wait for: published, applied or ready")
	c.Flags().DurationVar(&f.timeout, "timeout", defaultWaitTimeout, "how long to wait (exit code 4 when it runs out)")
	return c
}
