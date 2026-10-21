package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/textdiff"
	"go.yaml.in/yaml/v3"
)

const configLong = `Read and override the configuration of a robot or device.

A robot's configuration is layered: platform < type < fleet < robot < local < override. The
override layer is yours: per-robot changes of chart values, chart version, container images and
the revision of an application, written through the platform, no git commit needed. An
override always wins, and 'reset' puts the robot back to what git says.

  <target>    a robot name or a device name (a device that backs a robot is the same as the robot)
  <resource>  Kind/namespace/name (HelmChart/default/notebook), Kind/name or a unique name (notebook)

Typical loop for developing on one robot:
  tnctl config get vision-station-01                                   what is configured
  tnctl config set vision-station-01 notebook --image :pr-123 --wait   try another image tag
  tnctl config set vision-station-01 notebook replicas=2 log.level=debug --wait=applied
  tnctl config diff vision-station-01                                   what differs from git
  tnctl config reset vision-station-01 --all                            back to git
  tnctl config remove vision-station-01 local-llm                       delete a local item (written by Devstudio)
  tnctl config promote vision-station-01 notebook                       the YAML to commit when it works

--wait[=LEVEL] on a write follows the change; without it the command returns as soon as the
platform has stored it:
  published  the platform evaluated the change and published it to the robot
  applied    the robot has it: HelmChart values/version on the device and the helm Job succeeded;
             workload: image and generation match
  ready      applied, and the pods are Ready on the new image and have not restarted for 20 s
A bare --wait means ready. applied and ready check the robot over WireGuard SSH. --timeout 5m.

Value paths are dot separated keys; escape a dot inside a key with a backslash:
  nodeSelector.teknoir\.org/node-role=vision
Values are read like YAML 1.1 scalars (what Helm does): on becomes true and 010 becomes 8, with a
warning. Keys that name text (tag, repository, registry, digest, version, name, image, host, url,
namespace) and values whose current value is a string stay strings. --string keeps every value as
text. "k=" sets the empty string. null and ~ write null (delete). Lists and maps are YAML flow:
  tolerations='[{key: gpu, operator: Exists}]'    (a list replaces the whole list)

Output: -o json or -o yaml prints the platform's answer; data goes to stdout, progress and
warnings to stderr. With -o json, errors are {"error":{"code","message"}} on stderr.

Exit codes:
  0  ok
  1  error
  2  usage: bad arguments or flags, ambiguous resource name
  3  not found: no such robot, device or resource
  4  timeout: --wait ran out of time (the change itself is written)
  5  rejected: the platform refused it (invalid values, unknown revision, no such target, not overridable)
  6  not logged in, token rejected, or not allowed (run tnctl login)
  7  applied but unhealthy: the helm Job failed or pods crash or cannot pull the image
  8  conflict: someone else changed the same thing; run the command again
  9  rate limited: too many writes; wait the Retry-After time (-o json: error.retryAfter, seconds)`

func newConfigCmd(e *cliEnv) *cobra.Command {
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "config",
		Short:        "Read and override the configuration of a robot or device (values, images, versions, revisions)",
		Long:         configLong,
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(
		newConfigGetCmd(e), newConfigSetCmd(e), newConfigUnsetCmd(e), newConfigDeleteCmd(e),
		newConfigResetCmd(e), newConfigRemoveCmd(e), newConfigTryCmd(e), newConfigUntryCmd(e), newConfigDiffCmd(e),
		newConfigWaitCmd(e), newConfigLinksCmd(e), newConfigPromoteCmd(e),
	)
	return c
}

func init() { rootCmd.AddCommand(newConfigCmd(defaultEnv)) }

// argsRange validates the number of arguments with a usage error (exit code 2).
func argsRange(min, max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || (max >= 0 && len(args) > max) {
			hint := ""
			for _, a := range args {
				if h := lookupHint(a); h != "" {
					hint = fmt.Sprintf("\n%q: %s", a, strings.TrimPrefix(h, "; "))
					break
				}
			}
			return usageErr("%s%s\nusage: %s", usage, hint, cmd.UseLine())
		}
		return nil
	}
}

// ---------- config get ----------

type getFlags struct {
	layers, provenance, rendered, overrides, defaults bool
}

func newConfigGetCmd(e *cliEnv) *cobra.Command {
	var f getFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "get <target> [<resource>]",
		Short:        "Show the effective configuration of a robot or device, or one resource with provenance",
		Long: `Show the effective configuration of a target: one row per resource with the layer that wins.
With a resource: its merged values, chart, images, links and override.

  --layers       every layer's copy of each resource instead of only the winner
  --provenance   (resource) one row per value with the layer it comes from
  --rendered     (resource) the item exactly as published to the device
  --overrides    only the overrides of the target (and the state the platform reports for them)
  --defaults     (resource) include the chart's own defaults at the pinned version

-o json prints the platform's full answer, which includes everything above.`,
		Example: `  tnctl config get vision-station-01
  tnctl config get vision-station-01 --overrides
  tnctl config get vision-station-01 HelmChart/default/notebook --provenance
  tnctl config get vision-station-01 notebook --rendered
  tnctl config get vision-station-01 notebook -o json`,
		Args: argsRange(1, 2, "get needs a robot or device name and optionally a resource, for example: tnctl config get vision-station-01 notebook"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			if len(args) == 1 && (f.provenance || f.rendered || f.defaults) {
				return usageErr("--provenance, --rendered and --defaults need a resource: tnctl config get %s <resource>", args[0])
			}
			cmd.SilenceUsage = true
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			target := args[0]
			if len(args) == 1 {
				v, raw, err := fl.ConfigTarget(ctx, target)
				if err != nil {
					return classify(err)
				}
				if format != "table" {
					return writeStructured(out, format, raw)
				}
				printTargetView(out, v, f)
				return nil
			}
			id, _, err := resolveTarget(ctx, fl, target, args[1])
			if err != nil {
				return classify(err)
			}
			d, raw, err := fl.ConfigResource(ctx, target, id, f.defaults)
			if err != nil {
				return classify(err)
			}
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			return printResourceDetail(out, d, f)
		},
	}
	c.Flags().BoolVar(&f.layers, "layers", false, "show every layer's copy of each resource")
	c.Flags().BoolVar(&f.provenance, "provenance", false, "(resource) show where each value comes from")
	c.Flags().BoolVar(&f.rendered, "rendered", false, "(resource) show the item as published to the device")
	c.Flags().BoolVar(&f.overrides, "overrides", false, "show only the overrides")
	c.Flags().BoolVar(&f.defaults, "defaults", false, "(resource) include the chart's defaults")
	return c
}

func targetHeader(w io.Writer, v *api.ConfigTargetView) {
	t := v.Target
	what := t.Kind + " " + t.Name
	if t.Kind == "robot" && t.Device != "" && t.Device != t.Name {
		what += " (device " + t.Device + ")"
	}
	if t.Kind == "device" && t.Robot != "" {
		what += " (backs robot " + t.Robot + ")"
	}
	fmt.Fprintf(w, "Target: %s\n", what)
	if v.Published != nil && v.Published.Hash != "" {
		fmt.Fprintf(w, "Published: %s", v.Published.Hash)
		if v.Published.LastChange != "" {
			fmt.Fprintf(w, " (%s)", v.Published.LastChange)
		}
		fmt.Fprintln(w)
	}
	if v.OverrideCount > 0 {
		fmt.Fprintf(w, "Overrides: %d (revision %s, evaluated %s)\n", v.OverrideCount, orDash(v.Overrides.Revision), orDash(v.OverridesRevision))
	} else {
		fmt.Fprintln(w, "Overrides: none")
	}
	for _, warn := range v.Configuration.Warnings {
		fmt.Fprintf(w, "Warning: %s\n", warn)
	}
	fmt.Fprintln(w)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// what is the short description of a resource's content: chart@version or images.
func resourceWhat(r api.FleetConfigResource) string {
	if r.Chart != nil && r.Chart.Chart != "" {
		if r.Chart.Version != "" {
			return r.Chart.Chart + "@" + r.Chart.Version
		}
		return r.Chart.Chart
	}
	if len(r.Images) > 0 {
		imgs := make([]string, len(r.Images))
		for i, im := range r.Images {
			imgs[i] = im.Image
		}
		return strings.Join(imgs, ",")
	}
	return "-"
}

func overrideState(r api.FleetConfigResource) string {
	if r.Override == nil {
		return "-"
	}
	if r.Override.State != "" {
		return r.Override.State
	}
	return "set"
}

func printTargetView(w io.Writer, v *api.ConfigTargetView, f getFlags) {
	targetHeader(w, v)
	switch {
	case f.overrides:
		printOverrides(w, v)
	case f.layers:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RESOURCE\tLAYER\tSOURCE\tSTATUS\tFILE")
		for _, r := range v.Configuration.Resources {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID(), r.Layer, r.Source, r.Status, orDash(r.File))
		}
		_ = tw.Flush()
	default:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RESOURCE\tLAYER\tSOURCE\tCONTENT\tOVERRIDE")
		for _, r := range v.Configuration.Effective {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID(), r.Layer, r.Source, resourceWhat(r), overrideState(r))
		}
		_ = tw.Flush()
	}
}

func printOverrides(w io.Writer, v *api.ConfigTargetView) {
	state := map[string]api.OverrideStatus{}
	for _, s := range v.OverrideStatus {
		state[s.Target] = s
	}
	ids := make([]string, 0, len(v.Overrides.Resources))
	for id := range v.Overrides.Resources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tSTATE\tOVERRIDE\tSET BY\tNOTE")
	for _, id := range ids {
		o := v.Overrides.Resources[id]
		st := state[id]
		s := st.State
		if s == "" {
			s = api.StatePending
		}
		if st.Message != "" {
			s += ": " + st.Message
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", id, s, describeOverride(o), orDash(o.SetBy), orDash(o.Note))
	}
	for _, a := range v.Overrides.Applications {
		st := state[appKey(a.RepoURL, a.Path)]
		s := st.State
		if s == "" {
			s = api.StatePending
		}
		if st.Message != "" {
			s += ": " + st.Message
		}
		fmt.Fprintf(tw, "%s\t%s\trevision %s\t%s\t%s\n", appKey(a.RepoURL, a.Path), s, a.Revision, orDash(a.SetBy), orDash(a.Note))
	}
	_ = tw.Flush()
	if len(ids) == 0 && len(v.Overrides.Applications) == 0 {
		fmt.Fprintln(w, "(no overrides in this target)")
	}
}

// describeOverride is a one-line summary of what an override changes.
func describeOverride(o api.ResourceOverride) string {
	var parts []string
	if o.ValuesContent != "" {
		var m map[string]any
		if yaml.Unmarshal([]byte(o.ValuesContent), &m) == nil {
			parts = append(parts, "values "+strings.Join(leafPaths(m, ""), ","))
		} else {
			parts = append(parts, "values")
		}
	}
	if o.Version != "" {
		parts = append(parts, "version "+o.Version)
	}
	if len(o.Images) > 0 {
		names := make([]string, 0, len(o.Images))
		for k := range o.Images {
			names = append(names, k+"="+o.Images[k])
		}
		sort.Strings(names)
		parts = append(parts, "images "+strings.Join(names, ","))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}

// leafPaths lists the leaf paths of a decoded YAML map in the escaped path syntax.
func leafPaths(m map[string]any, prefix string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		p := strings.ReplaceAll(strings.ReplaceAll(k, `\`, `\\`), ".", `\.`)
		if prefix != "" {
			p = prefix + "." + p
		}
		if sub, ok := m[k].(map[string]any); ok && len(sub) > 0 {
			out = append(out, leafPaths(sub, p)...)
		} else {
			out = append(out, p)
		}
	}
	return out
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func yamlString(v any) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	_ = enc.Close()
	return buf.String()
}

func printResourceDetail(w io.Writer, d *api.ConfigResourceDetail, f getFlags) error {
	r := d.Resource
	if f.rendered {
		if d.Rendered == nil {
			return newErr(codeNotFound, "%s is not in the device's rendered configuration yet", d.ID)
		}
		fmt.Fprint(w, yamlString(d.Rendered))
		return nil
	}
	fmt.Fprintf(w, "Resource: %s\n", d.ID)
	fmt.Fprintf(w, "Layer:    %s (%s)\n", r.Layer, r.Source)
	if len(r.MergedFrom) > 0 {
		fmt.Fprintf(w, "Merged:   %s\n", strings.Join(r.MergedFrom, " < "))
	}
	if r.Chart != nil {
		fmt.Fprintf(w, "Chart:    %s\n", resourceWhat(r))
	}
	if r.Application != nil {
		fmt.Fprintf(w, "From:     %s %s @ %s\n", r.Application.RepoURL, r.Application.Path, orDash(r.Application.Revision))
	}
	if d.Override != nil || r.Override != nil {
		printOverrideBox(w, d)
	}
	if !d.Editable.Values && !d.Editable.Images && !d.Editable.Version && d.Editable.Reason != "" {
		fmt.Fprintf(w, "Not overridable: %s\n", d.Editable.Reason)
	}
	fmt.Fprintln(w)
	switch {
	case f.layers:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "LAYER\tSOURCE\tSTATUS\tFILE")
		for _, c := range d.Contributions {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Layer, c.Source, c.Status, orDash(c.File))
		}
		_ = tw.Flush()
	case f.overrides:
		if d.Override == nil {
			fmt.Fprintln(w, "(no override on this resource)")
		}
	case f.provenance && r.Values != nil:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PATH\tVALUE\tLAYER\tSOURCE")
		for _, p := range r.Values.Provenance {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Path, compactJSON(p.Value), p.Layer, p.Source)
		}
		_ = tw.Flush()
	case r.Values != nil:
		fmt.Fprint(w, yamlString(r.Values.Merged))
	case len(r.Images) > 0:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CONTAINER\tIMAGE")
		for _, im := range r.Images {
			name := im.Container
			if im.Init {
				name += " (init)"
			}
			fmt.Fprintf(tw, "%s\t%s\n", name, im.Image)
		}
		_ = tw.Flush()
	}
	if f.defaults && d.ChartDefaults != nil {
		fmt.Fprintf(w, "\nChart defaults:\n%s", yamlString(d.ChartDefaults))
	}
	return nil
}

func printOverrideBox(w io.Writer, d *api.ConfigResourceDetail) {
	if d.Override != nil {
		o := d.Override.ResourceOverride
		fmt.Fprintf(w, "Override: %s (stored as %s", describeOverride(o), d.Override.Storage)
		if o.SetBy != "" {
			fmt.Fprintf(w, ", set by %s", o.SetBy)
		}
		if o.SetAt != "" {
			fmt.Fprintf(w, " at %s", o.SetAt)
		}
		fmt.Fprintln(w, ")")
		if o.Note != "" {
			fmt.Fprintf(w, "Note:     %s\n", o.Note)
		}
	}
	if s := d.OverrideStatus; s != nil {
		line := "State:    " + s.State
		if s.Message != "" {
			line += ": " + s.Message
		}
		fmt.Fprintln(w, line)
	}
}

// ---------- config diff ----------

// effectiveSummary is the part of a resource a user cares about when comparing, as YAML.
func effectiveSummary(r *api.FleetConfigResource) string {
	if r == nil {
		return ""
	}
	m := map[string]any{}
	if r.Chart != nil && r.Chart.Version != "" {
		m["version"] = r.Chart.Version
	}
	if r.Values != nil {
		m["values"] = r.Values.Merged
	}
	if len(r.Images) > 0 {
		imgs := map[string]string{}
		for _, im := range r.Images {
			imgs[im.Container] = im.Image
		}
		m["images"] = imgs
	}
	if len(m) == 0 {
		return ""
	}
	return yamlString(m)
}

type diffEntry struct {
	ID      string `json:"id"`
	Diff    string `json:"diff"`
	Changed bool   `json:"changed"`
	// Stale: the override is still stored, but git no longer yields the resource.
	Stale bool `json:"stale,omitempty"`
}

func newConfigDiffCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "diff <target> [<resource>]",
		Short:        "Show what the overrides change: git result against effective, as a unified diff",
		Long: `Show the difference between what git yields and what the robot is configured with, for one
resource, or for every resource that has an override. Application revision overrides are listed
with the revision they replace.

Exit code 0 whether or not there is a difference.`,
		Example: `  tnctl config diff vision-station-01
  tnctl config diff vision-station-01 HelmChart/default/notebook
  tnctl config diff vision-station-01 -o json`,
		Args: argsRange(1, 2, "diff needs a robot or device name and optionally a resource"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			target := args[0]
			var ids []string
			var view *api.ConfigTargetView
			if len(args) == 2 {
				id, v, err := resolveTarget(ctx, fl, target, args[1])
				if err != nil {
					return classify(err)
				}
				ids, view = []string{id}, v
			} else {
				view, _, err = fl.ConfigTarget(ctx, target)
				if err != nil {
					return classify(err)
				}
				for id := range view.Overrides.Resources {
					ids = append(ids, id)
				}
				// In-place changes of local items are overrides too, stored with the item.
				for _, r := range view.Configuration.Effective {
					if r.Override != nil && r.Override.Storage == api.StorageLocalItem {
						ids = append(ids, r.ID())
					}
				}
				ids = dedupe(ids)
				sort.Strings(ids)
			}
			effective := map[string]bool{}
			if view != nil {
				for _, r := range view.Configuration.Effective {
					effective[r.ID()] = true
				}
			}
			var entries []diffEntry
			for _, id := range ids {
				// An override can outlive its resource (git dropped it): say so for that one and go on.
				listing := len(args) == 1
				if listing && !effective[id] {
					entries = append(entries, diffEntry{ID: id, Stale: true})
					continue
				}
				d, _, err := fl.ConfigResource(ctx, target, id, false)
				if err != nil {
					var ae *api.Error
					if listing && errors.As(err, &ae) && ae.Status == http.StatusNotFound {
						entries = append(entries, diffEntry{ID: id, Stale: true})
						continue
					}
					return classify(err)
				}
				entries = append(entries, diffEntry{ID: id, Diff: resourceDiff(d)})
				entries[len(entries)-1].Changed = entries[len(entries)-1].Diff != ""
			}
			var apps []api.ApplicationOverride
			if len(args) == 1 && view != nil {
				apps = view.Overrides.Applications
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				return writeStructured(out, format, marshalRaw(map[string]any{
					"target": target, "resources": nonNil(entries), "applications": nonNilApps(apps),
				}))
			}
			if len(entries) == 0 && len(apps) == 0 {
				fmt.Fprintf(out, "No overrides on %s: the effective configuration is what git yields.\n", target)
				return nil
			}
			for _, en := range entries {
				fmt.Fprintf(out, "# %s\n", en.ID)
				if en.Stale {
					fmt.Fprintf(out, "(stale override: git no longer yields this resource; remove it with: tnctl config reset %s %s)\n", target, en.ID)
				} else if en.Diff == "" {
					fmt.Fprintln(out, "(no difference from git)")
				} else {
					fmt.Fprint(out, en.Diff)
				}
				fmt.Fprintln(out)
			}
			for _, a := range apps {
				fmt.Fprintf(out, "# application %s %s\ntry revision: %s\n\n", a.RepoURL, orDash(a.Path), a.Revision)
			}
			return nil
		},
	}
}

func nonNil(e []diffEntry) []diffEntry {
	if e == nil {
		return []diffEntry{}
	}
	return e
}

func nonNilApps(a []api.ApplicationOverride) []api.ApplicationOverride {
	if a == nil {
		return []api.ApplicationOverride{}
	}
	return a
}

// resourceDiff diffs the git result (base) against the rendered item.
func resourceDiff(d *api.ConfigResourceDetail) string {
	if d.Base == nil && d.Rendered == nil {
		return ""
	}
	return textdiff.Unified("git", "effective", yamlOrEmpty(d.Base), yamlOrEmpty(d.Rendered))
}

func yamlOrEmpty(m map[string]any) string {
	if m == nil {
		return ""
	}
	return yamlString(prettyItem(m))
}

// prettyItem shows spec.valuesContent of a HelmChart item as a YAML map instead of one text
// block, so a diff shows the changed value and not a whole changed string.
func prettyItem(m map[string]any) map[string]any {
	spec, ok := m["spec"].(map[string]any)
	if !ok {
		return m
	}
	vc, ok := spec["valuesContent"].(string)
	if !ok {
		return m
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(vc), &values); err != nil {
		return m
	}
	cp := map[string]any{}
	for k, v := range m {
		cp[k] = v
	}
	sp := map[string]any{}
	for k, v := range spec {
		sp[k] = v
	}
	sp["valuesContent"] = values
	cp["spec"] = sp
	return cp
}

// ---------- config links ----------

func newConfigLinksCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "links <target> <resource>",
		Short:        "Print the GitHub links of a resource: its manifest file, history, chart source and values.yaml",
		Long: `Print the GitHub links for a resource: the file in git that defines it (view, permalink, history,
edit when the revision is a branch) and, for a HelmChart, the chart's source, its values.yaml at the
pinned version and the release. Links are only known for resources that come from github.com
repositories; "guessed" means derived by convention, not from the Helm repo index.`,
		Example: `  tnctl config links vision-station-01 HelmChart/default/notebook
  tnctl config links vision-station-01 notebook -o json`,
		Args: argsRange(2, 2, "links needs a robot or device name and a resource"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			id, _, err := resolveTarget(ctx, fl, args[0], args[1])
			if err != nil {
				return classify(err)
			}
			d, _, err := fl.ConfigResource(ctx, args[0], id, false)
			if err != nil {
				return classify(err)
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				type copyLinks struct {
					Layer  string           `json:"layer"`
					Source string           `json:"source"`
					File   string           `json:"file,omitempty"`
					Links  *api.ConfigLinks `json:"links,omitempty"`
				}
				var copies []copyLinks
				for _, c := range d.Contributions {
					if c.Links != nil {
						copies = append(copies, copyLinks{c.Layer, c.Source, c.File, c.Links})
					}
				}
				return writeStructured(out, format, marshalRaw(map[string]any{
					"id": id, "files": copies, "chart": d.Resource.ChartLinks,
				}))
			}
			printed := false
			for _, c := range d.Contributions {
				if c.Links == nil {
					continue
				}
				printed = true
				fmt.Fprintf(out, "%s %s: %s\n", c.Layer, c.Source, orDash(c.File))
				printLinks(out, [][2]string{
					{"view", c.Links.View}, {"permalink", c.Links.Permalink}, {"history", c.Links.History},
					{"edit", c.Links.Edit}, {"edit on default branch", c.Links.EditOnDefault},
				})
				if c.Links.Pinned {
					fmt.Fprintln(out, "  (the revision is a tag or commit: editing the default branch does not change this robot)")
				}
			}
			if cl := d.Resource.ChartLinks; cl != nil {
				printed = true
				title := "chart " + cl.Owner + "/" + cl.Repo
				if cl.Guessed {
					title += " (guessed by convention)"
				}
				fmt.Fprintln(out, title)
				printLinks(out, [][2]string{
					{"source", cl.Source}, {"values.yaml", cl.Values}, {"edit values.yaml", cl.ValuesEdit}, {"release", cl.Release},
				})
			}
			if !printed {
				fmt.Fprintf(out, "No GitHub links for %s (it does not come from a github.com repository).\n", id)
			}
			return nil
		},
	}
}

func printLinks(w io.Writer, rows [][2]string) {
	for _, r := range rows {
		if r[1] != "" {
			fmt.Fprintf(w, "  %-22s %s\n", r[0], r[1])
		}
	}
}

// ---------- config promote ----------

func newConfigPromoteCmd(e *cliEnv) *cobra.Command {
	var to string
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "promote <target> <resource> [--to robot|fleet:<name>|type]",
		Short:        "Print the YAML to commit to git so the override becomes permanent",
		Long: `Turn the override of a resource into the YAML you commit to git, with the suggested file and a
GitHub link to create or edit it. Nothing is written to git or to the platform. The YAML goes to
stdout (redirect it to a file); the path, repository and link go to stderr. When the change is
in git, reset the override: tnctl config reset <target> <resource>.

--to picks the layer to commit to: robot (default), fleet:<name> or type.`,
		Example: `  tnctl config promote vision-station-01 HelmChart/default/notebook > notebook.yaml
  tnctl config promote vision-station-01 notebook --to fleet:test
  tnctl config promote vision-station-01 notebook -o json`,
		Args: argsRange(2, 2, "promote needs a robot or device name and a resource"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			id, _, err := resolveTarget(ctx, fl, args[0], args[1])
			if err != nil {
				return classify(err)
			}
			p, raw, err := fl.Promote(ctx, args[0], id, to)
			if err != nil {
				return classify(err)
			}
			if format != "table" {
				return writeStructured(cmd.OutOrStdout(), format, raw)
			}
			fmt.Fprint(cmd.OutOrStdout(), p.YAML)
			if !strings.HasSuffix(p.YAML, "\n") {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			errw := cmd.ErrOrStderr()
			if p.Path != "" {
				fmt.Fprintf(errw, "Commit as: %s", p.Path)
				if p.RepoURL != "" {
					fmt.Fprintf(errw, " in %s", p.RepoURL)
				}
				if p.Branch != "" {
					fmt.Fprintf(errw, " (branch %s)", p.Branch)
				}
				fmt.Fprintln(errw)
			}
			if p.NewFileURL != "" {
				fmt.Fprintf(errw, "New file:  %s\n", p.NewFileURL)
			}
			if p.EditURL != "" {
				fmt.Fprintf(errw, "Edit file: %s\n", p.EditURL)
			}
			if p.Note != "" {
				fmt.Fprintln(errw, p.Note)
			}
			return nil
		},
	}
	c.Flags().StringVar(&to, "to", "", "layer to commit to: robot (default), fleet:<name> or type")
	return c
}
