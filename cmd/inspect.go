package cmd

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// terminalOff is the code of the inspect error that a robot's Shell is off.
const terminalOff = "terminal_off"

// inspectErr gives err an exit code, and shows the platform's own words as they are when the robot's
// Shell is off: that is a refusal ("ask a person to turn it on"), not a conflict to retry.
func inspectErr(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Code == terminalOff {
		return newErr(codeRejected, "%s", ae.Message)
	}
	return classify(err)
}

// parseK8sPair reads "<namespace>/<name>": both are Kubernetes names.
func parseK8sPair(what, arg string) (ns, name string, err error) {
	parts := strings.Split(arg, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", usageErr("%s needs <namespace>/<name>, for example default/whoami-7d9f-x2k4p, not %q", what, arg)
	}
	if !k8sName.MatchString(parts[0]) || !k8sName.MatchString(parts[1]) {
		return "", "", usageErr("%q is not <namespace>/<name>", arg)
	}
	return parts[0], parts[1], nil
}

func newInspectCmd(e *cliEnv) *cobra.Command {
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "inspect <pod|logs|release> ...",
		Short:        "Read a robot's logs, pods and installed Helm releases without opening a session",
		Long: `Read what a robot runs, as a person or from inside a TeknoirBot: the pod of a failing app, its log
and the objects of an installed Helm release. It is read-only.

tnctl asks the platform not to turn the robot's Shell on: when the Shell is off it answers "The Shell
of <robot> is off; ask a person to turn it on." As a person it needs the same access as opening a
terminal in it; inside a TeknoirBot the bot must be set to Act as me by a creator who has that access.`,
		Example: `  tnctl inspect pod vision-station-01 default/whoami-7d9f-x2k4p
  tnctl inspect logs vision-station-01 default/whoami-7d9f-x2k4p --tail 50
  tnctl inspect logs vision-station-01 default/notebook-0 -c notebook --previous
  tnctl inspect release vision-station-01 default/notebook`,
	}
	c.AddCommand(newInspectPodCmd(e), newInspectLogsCmd(e), newInspectReleaseCmd(e))
	return c
}

func newInspectPodCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "pod <robot> <podNamespace>/<pod>",
		Short:        "Show a pod of a robot: phase, containers and recent events",
		Long: `Show a pod of a robot: its phase, its containers with ready state, restarts and state, the
conditions that are not healthy and the recent events.

-o json prints the platform's answer unchanged.`,
		Example: `  tnctl inspect pod vision-station-01 default/whoami-7d9f-x2k4p
  tnctl inspect pod vision-station-01 default/whoami-7d9f-x2k4p -o json`,
		Args: argsRange(2, 2, "inspect pod needs a robot and <namespace>/<pod>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			ns, pod, err := parseK8sPair("inspect pod", args[1])
			if err != nil {
				return err
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			res, raw, err := fl.InspectPod(cmd.Context(), args[0], ns, pod)
			if err != nil {
				return inspectErr(err)
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			printInspectPod(out, res)
			return nil
		},
	}
}

// containerState says how a container is: running, waiting: CrashLoopBackOff, terminated: Error.
func containerState(st api.InspectContainerState) string {
	kind := st.Kind
	if kind == "" {
		kind = "unknown"
	}
	if st.Reason != "" && st.Reason != kind {
		kind += ": " + st.Reason
	}
	if st.Kind == "terminated" && st.ExitCode != nil && *st.ExitCode != 0 {
		kind += fmt.Sprintf(" (exit %d)", *st.ExitCode)
	}
	return kind
}

func printInspectPod(w io.Writer, res *api.InspectPodResponse) {
	p := res.Pod
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	row("Pod", p.Namespace+"/"+p.Name)
	phase := p.Phase
	if p.Reason != "" {
		phase += ": " + p.Reason
	}
	if p.Message != "" {
		phase += " (" + p.Message + ")"
	}
	row("Phase", phase)
	row("Node", p.Node)
	row("QoS", p.QOSClass)
	row("Started", p.StartTime)
	if p.Owner != nil {
		row("Owner", p.Owner.Kind+"/"+p.Owner.Name)
	}
	row("Restarts", fmt.Sprint(p.Restarts))
	for _, c := range p.Conditions {
		if strings.EqualFold(c.Type, "Ready") && strings.EqualFold(c.Status, "True") {
			continue // the healthy one says nothing
		}
		v := c.Type + " " + c.Status
		if c.Reason != "" {
			v += ": " + c.Reason
		}
		if c.Message != "" {
			v += " (" + c.Message + ")"
		}
		row("Condition", v)
	}
	containers := append(append([]api.InspectContainer{}, p.InitContainers...), p.Containers...)
	for i, c := range containers {
		label := ""
		if i == 0 {
			label = "Containers"
		}
		name := c.Name
		if c.Init {
			name += " (init)"
		}
		ready := "not ready"
		if c.Ready {
			ready = "ready"
		}
		fmt.Fprintf(tw, "%s\t%s  %s, %d restarts, %s\n", label, name, ready, c.Restarts, containerState(c.State))
	}
	_ = tw.Flush()
	if len(res.Events) > 0 {
		fmt.Fprintln(w, "\nEvents:")
		events := append([]api.InspectEvent{}, res.Events...)
		// Warnings first: they are what the caller came for.
		sort.SliceStable(events, func(i, j int) bool {
			return strings.EqualFold(events[i].Type, "Warning") && !strings.EqualFold(events[j].Type, "Warning")
		})
		etw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(etw, "TYPE\tREASON\tCOUNT\tLAST\tMESSAGE")
		for _, ev := range events {
			count := "-"
			if ev.Count > 0 {
				count = fmt.Sprint(ev.Count)
			}
			fmt.Fprintf(etw, "%s\t%s\t%s\t%s\t%s\n", ev.Type, ev.Reason, count, ev.Last, ev.Message)
		}
		_ = etw.Flush()
	}
	if res.EventsTruncated {
		fmt.Fprintln(w, "\n(older events are not shown)")
	}
}

// inspectTailMax is the most lines the platform's log route hands out.
const inspectTailMax = 2000

// inspectSince matches the --since of inspect logs: one to six digits and s, m or h.
var inspectSince = regexp.MustCompile(`^[1-9][0-9]{0,5}[smh]$`)

// inspectSinceMax is the longest --since the platform's log route accepts.
const inspectSinceMax = 168 * time.Hour

// parseInspectSince checks --since against what the platform's log route takes: a whole number of
// seconds, minutes or hours, at most 168h. It is stricter than durationArg (which tnctl logs keeps).
func parseInspectSince(s string) error {
	bad := usageErr("--since must be like 30s, 10m or 2h, at most 168h")
	if !inspectSince.MatchString(s) {
		return bad
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return bad
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 's':
		unit = time.Second
	case 'm':
		unit = time.Minute
	default: // 'h'
		unit = time.Hour
	}
	if time.Duration(n)*unit > inspectSinceMax {
		return bad
	}
	return nil
}

func newInspectLogsCmd(e *cliEnv) *cobra.Command {
	var f logsFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "logs <robot> <podNamespace>/<pod> [-c container] [--previous] [--tail N] [--since 10m]",
		Short:        "Show the log of a pod on a robot, as a person or inside the robot's TeknoirBot",
		Long: `Show the log of a pod on a robot. This is the command to use inside a TeknoirBot, which may not
run "tnctl logs".

--tail is how many lines from the end are shown: 500 lines by default, at most 2000. --since takes a
duration such as 30s, 10m or 2h, at most 168h. Lines the platform redacts (a password an app
printed) stay hidden.`,
		Example: `  tnctl inspect logs vision-station-01 default/whoami-7d9f-x2k4p
  tnctl inspect logs vision-station-01 default/whoami-7d9f-x2k4p --tail 50 --since 10m
  tnctl inspect logs vision-station-01 default/notebook-0 -c notebook --previous`,
		Args: argsRange(2, 2, "inspect logs needs a robot and <namespace>/<pod>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.since != "" {
				if err := parseInspectSince(f.since); err != nil {
					return err
				}
			}
			if f.container != "" && !k8sName.MatchString(f.container) {
				return usageErr("--container %q is not a container name", f.container)
			}
			if f.tail < 0 || f.tail > inspectTailMax {
				return usageErr("--tail must be from 1 to 2000")
			}
			ns, pod, err := parseK8sPair("inspect logs", args[1])
			if err != nil {
				return err
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			res, _, err := fl.InspectLogs(cmd.Context(), args[0], ns, pod, api.InspectLogsOptions{
				Container: f.container, Previous: f.previous, Tail: f.tail, Since: f.since,
			})
			if err != nil {
				return inspectErr(err)
			}
			out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
			text := res.Text
			if text != "" {
				if _, err := io.WriteString(out, text); err != nil {
					return err
				}
				if !strings.HasSuffix(text, "\n") {
					fmt.Fprintln(out)
				}
			}
			if res.Truncated.Lines || res.Truncated.Bytes {
				fmt.Fprintln(errw, "tnctl: the log is longer than the platform sends; only its end is shown")
			}
			if res.Redacted > 0 {
				fmt.Fprintf(errw, "tnctl: %d line(s) contained credentials and are redacted\n", res.Redacted)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&f.container, "container", "c", "", "container name")
	c.Flags().BoolVar(&f.previous, "previous", false, "log of the previous container instance")
	c.Flags().IntVar(&f.tail, "tail", 0, "lines to show from the end, at most 2000 (0: the platform's default of 500)")
	c.Flags().StringVar(&f.since, "since", "", "only lines newer than this, for example 10m (at most 168h)")
	return c
}

func newInspectReleaseCmd(e *cliEnv) *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "release <robot> <releaseNamespace>/<release>",
		Short:        "Show an installed Helm release of a robot: status, revision and its objects",
		Long: `Show an installed Helm release of a robot: its revision, status, chart and the objects it
installed, as one table.

-o json prints the platform's answer unchanged, including the manifest with Secret values hidden.`,
		Example: `  tnctl inspect release vision-station-01 default/notebook
  tnctl inspect release vision-station-01 default/notebook -o json`,
		Args: argsRange(2, 2, "inspect release needs a robot and <namespace>/<release>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			ns, rel, err := parseK8sPair("inspect release", args[1])
			if err != nil {
				return err
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			res, raw, err := fl.InspectRelease(cmd.Context(), args[0], ns, rel)
			if err != nil {
				return inspectErr(err)
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			printInspectRelease(out, res)
			return nil
		},
	}
}

func printInspectRelease(w io.Writer, res *api.InspectReleaseResponse) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	row("Release", res.Namespace+"/"+res.Name)
	row("Status", res.Status)
	row("Revision", fmt.Sprint(res.Revision))
	chart := res.Chart
	if res.ChartVersion != "" {
		chart += " " + res.ChartVersion
	}
	row("Chart", strings.TrimSpace(chart))
	row("App version", res.AppVersion)
	row("Deployed", res.LastDeployed)
	_ = tw.Flush()
	if len(res.Objects) == 0 {
		fmt.Fprintln(w, "\nThe release installed no object.")
		return
	}
	fmt.Fprintf(w, "\nObjects (%d):\n", len(res.Objects))
	otw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(otw, "KIND\tNAME\tNAMESPACE\tAPI VERSION\tNOTES")
	for _, o := range res.Objects {
		ns := o.Namespace
		if ns == "" {
			ns = "-"
		}
		notes := ""
		if o.Hidden {
			notes = "values hidden"
		}
		fmt.Fprintf(otw, "%s\t%s\t%s\t%s\t%s\n", o.Kind, o.Name, ns, o.APIVersion, notes)
	}
	_ = otw.Flush()
}
