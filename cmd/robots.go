package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

func newRobotsCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "robots [label=value...]",
		Aliases:      []string{"rbs"},
		Short:        "List the robots of the teamspace (-o json for scripts)",
		Long: `List the robots of the teamspace: name, type, phase, computers, overrides.

A robot is one edge computer or a k3s cluster of computers; every robot has a Device behind it.
Labels narrow the list (all must match). Use -o json or -o yaml for the full records.`,
		Example: `  tnctl robots
  tnctl robots site=karlstad -o json
  tnctl -n teknoir-demo robots`,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			labels := map[string]string{}
			for _, a := range args {
				k, v, ok := strings.Cut(a, "=")
				if !ok || k == "" {
					return usageErr("invalid label %q: expected key=value", a)
				}
				labels[k] = v
			}
			cmd.SilenceUsage = true
			fl, _, ns, err := e.targetClient()
			if err != nil {
				return err
			}
			robots, raw, err := fl.Robots(cmd.Context())
			if err != nil {
				return classify(err)
			}
			if len(labels) > 0 {
				robots, raw = filterRobots(robots, raw, labels)
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			if len(robots) == 0 {
				// Not an error, whatever the format: -o json prints [] and exits 0 as well.
				if len(labels) > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "no robots in teamspace %s with the specified labels\n", ns)
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "no robots in teamspace %s\n", ns)
				}
				return nil
			}
			printRobotTable(out, robots, e.clock())
			return nil
		},
	}
}

// filterRobots keeps the robots that have all labels, in the parsed and in the raw list (the
// raw JSON stays whole so -o json never loses fields).
func filterRobots(robots []api.RobotSummary, raw json.RawMessage, labels map[string]string) ([]api.RobotSummary, json.RawMessage) {
	var rawItems []json.RawMessage
	_ = json.Unmarshal(raw, &rawItems)
	out := []api.RobotSummary{}
	outRaw := []json.RawMessage{}
	for i, r := range robots {
		ok := true
		for k, v := range labels {
			if got, has := r.Labels[k]; !has || got != v {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		out = append(out, r)
		if i < len(rawItems) {
			outRaw = append(outRaw, rawItems[i])
		}
	}
	return out, marshalRaw(outRaw)
}

func readyNodes(nodes []api.FleetNode) string {
	ready := 0
	for _, n := range nodes {
		if n.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, len(nodes))
}

func printRobotTable(w io.Writer, robots []api.RobotSummary, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tPHASE\tNODES\tOVERRIDES\tFLEETS")
	for _, r := range robots {
		ov := "-"
		if r.Overrides > 0 {
			ov = fmt.Sprint(r.Overrides)
			if age := overrideAge(r.OldestOverrideAt, now); age != "" {
				ov += " (oldest " + age + ")"
			}
		}
		fleets := "-"
		if len(r.Fleets) > 0 {
			fleets = strings.Join(r.Fleets, ",")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Type, r.Phase, readyNodes(r.Nodes), ov, fleets)
	}
	_ = tw.Flush()
}

// overrideAge is "3d" or "5h" for an RFC 3339 time, "" when unknown.
func overrideAge(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func newRobotCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "robot <name>",
		Aliases:      []string{"rb"},
		Short:        "Show one robot: phase, computers, configuration layers (-o json for scripts)",
		Long: `Show one robot: its type, phase, computers (nodes), configuration layers and the number of
overrides. The effective configuration and the overrides are under 'tnctl config get <robot>'.`,
		Example: `  tnctl robot vision-station-01
  tnctl robot vision-station-01 -o json`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageErr("robot needs exactly one robot name, for example: tnctl robot vision-station-01")
			}
			return nil
		},
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
			r, raw, err := fl.Robot(cmd.Context(), args[0])
			if err != nil {
				return classify(err)
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			printRobot(out, r, e.clock())
			return nil
		},
	}
}

func printRobot(w io.Writer, r *api.RobotDetail, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, v) }
	row("Robot", r.Name)
	row("Namespace", r.Namespace)
	typ := r.Type
	if r.TypeDisplayName != "" && r.TypeDisplayName != r.Type {
		typ += " (" + r.TypeDisplayName + ")"
	}
	row("Type", typ)
	phase := r.Phase
	if r.PhaseMessage != "" {
		phase += ": " + r.PhaseMessage
	}
	row("Phase", phase)
	row("Device", r.Device)
	if len(r.Labels) > 0 {
		keys := make([]string, 0, len(r.Labels))
		for k := range r.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + r.Labels[k]
		}
		row("Labels", strings.Join(parts, " "))
	}
	if len(r.Fleets) > 0 {
		row("Fleets", strings.Join(r.Fleets, ", "))
	}
	for i, n := range r.Nodes {
		label := ""
		if i == 0 {
			label = "Nodes"
		}
		state := "not joined"
		switch {
		case n.Ready:
			state = "ready"
		case n.Joined:
			state = "joined, not ready"
		}
		row(label, fmt.Sprintf("%s (%s, %s) %s", n.NodeName, n.Role, n.K3s, state))
	}
	for i, l := range r.Layers {
		label := ""
		if i == 0 {
			label = "Layers"
		}
		row(label, fmt.Sprintf("%s %s: %d applications", l.Layer, l.Source, l.Applications))
	}
	ov := "none"
	if r.Overrides > 0 {
		ov = fmt.Sprint(r.Overrides)
		if age := overrideAge(r.OldestOverrideAt, now); age != "" {
			ov += " (oldest " + age + ")"
		}
	}
	row("Overrides", ov)
	row("Resources", fmt.Sprintf("%d effective", len(r.Configuration.Effective)))
	_ = tw.Flush()
	fmt.Fprintf(w, "\nConfiguration: tnctl config get %s\n", r.Name)
}

func init() {
	rootCmd.AddCommand(newRobotsCmd(defaultEnv), newRobotCmd(defaultEnv))
}
