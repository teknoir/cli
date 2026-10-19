package cmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

type logsFlags struct {
	container     string
	follow        bool
	tail          int
	since         string
	previous      bool
	allContainers bool
}

func newLogsCmd(e *cliEnv) *cobra.Command {
	var f logsFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "logs <target> <resource|pod> [-c container] [-f] [--tail N]",
		Short:        "Show the logs of a workload on a robot (sudo k3s kubectl logs over WireGuard)",
		Long: `Show the logs of a workload on a robot. <resource> is a Deployment, StatefulSet, DaemonSet, Job
or Pod of the robot's configuration (Kind/namespace/name, Kind/name or a unique name), or a HelmChart:
then the logs of the release's workload. Anything that is not in the configuration is taken as the
name of a pod on the robot.

The last 200 lines are shown (--tail -1 for all). -f follows until you stop it. Needs the robot's
WireGuard tunnel; the exit status is kubectl's.`,
		Example: `  tnctl logs vision-station-01 HelmChart/default/notebook --tail 50
  tnctl logs vision-station-01 Deployment/default/whoami -f
  tnctl logs vision-station-01 notebook-77f875b6c-45s8v -c notebook --previous`,
		Args: argsRange(2, 2, "logs needs a robot or device name and a resource or pod"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return e.runLogs(cmd, args[0], args[1], &f)
		},
	}
	c.Flags().StringVarP(&f.container, "container", "c", "", "container name")
	c.Flags().BoolVarP(&f.follow, "follow", "f", false, "follow the log")
	c.Flags().IntVar(&f.tail, "tail", 200, "lines to show from the end (-1 for all)")
	c.Flags().StringVar(&f.since, "since", "", "only logs newer than this, for example 10m")
	c.Flags().BoolVar(&f.previous, "previous", false, "logs of the previous container instance")
	c.Flags().BoolVar(&f.allContainers, "all-containers", false, "logs of all containers")
	return c
}

// logTarget is what to pass to kubectl logs.
type logTarget struct{ ns, ref string }

func (e *cliEnv) runLogs(cmd *cobra.Command, target, arg string, f *logsFlags) error {
	if f.since != "" && !durationArg.MatchString(f.since) {
		return usageErr("--since %q: use a duration like 30s, 10m or 2h", f.since)
	}
	if f.container != "" && !k8sName.MatchString(f.container) {
		return usageErr("--container %q is not a container name", f.container)
	}
	fl, domain, ns, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	// Connecting and looking up the target use the device key from a temporary directory: a
	// signal must return through the cleanup. Streaming forwards signals to the command instead.
	sctx, stop := signalContext(ctx)
	defer stop()
	view, _, err := fl.ConfigTarget(sctx, target)
	if err != nil {
		return classify(err)
	}
	ex, err := e.newExec(sctx, domain, ns, view.Target.Device)
	if err != nil {
		return classify(err)
	}
	defer ex.Close()

	lt, err := resolveLogTarget(sctx, ex, view, arg)
	if err != nil {
		return err
	}
	line := kubectlOnDevice + " -n " + shq(lt.ns) + " logs " + shq(lt.ref)
	if f.container != "" {
		line += " -c " + shq(f.container)
	}
	if f.allContainers {
		line += " --all-containers"
	}
	if f.follow {
		line += " -f"
	}
	if f.previous {
		line += " --previous"
	}
	if f.since != "" {
		line += " --since=" + shq(f.since)
	}
	line += " --tail=" + strconv.Itoa(f.tail)
	code, err := ex.Stream(ctx, line, nil, cmd.OutOrStdout(), cmd.ErrOrStderr())
	if err != nil {
		return classify(fmt.Errorf("cannot read the logs on %s: %w", view.Target.Device, err))
	}
	if code == sshConnectFailed {
		return newErr(codeError, "ssh could not reach %s over WireGuard (see: tnctl wg status %s)", view.Target.Device, view.Target.Device)
	}
	if code != 0 {
		return &exitError{code: code}
	}
	return nil
}

// resolveLogTarget turns the argument into a kubectl logs reference.
func resolveLogTarget(ctx context.Context, ex deviceExec, view *api.ConfigTargetView, arg string) (logTarget, error) {
	id, rerr := resolveResourceArg(arg, knownIDs(view))
	known := false
	for _, k := range knownIDs(view) {
		if k == id {
			known = true
		}
	}
	var ce *cliError
	if errors.As(rerr, &ce) && ce.code == codeUsage {
		return logTarget{}, rerr // ambiguous or malformed
	}
	if rerr == nil && known {
		kind, ns, name, err := checkID(id)
		if err != nil {
			return logTarget{}, err
		}
		switch kind {
		case "Deployment", "StatefulSet", "DaemonSet", "Job", "Pod":
			return logTarget{ns, kubectlResource(kind) + "/" + name}, nil
		case "CronJob":
			return logTarget{}, usageErr("a CronJob has no logs of its own: name the pod of one of its Jobs")
		case "HelmChart":
			return helmLogTarget(ctx, ex, view, ns, name)
		}
		return logTarget{}, usageErr("%s has no logs", id)
	}
	if strings.Contains(arg, "/") || !k8sName.MatchString(arg) {
		if rerr != nil {
			return logTarget{}, rerr
		}
		return logTarget{}, newErr(codeNotFound, "no resource %q on this target", arg)
	}
	return podLogTarget(ctx, ex, arg)
}

func helmLogTarget(ctx context.Context, ex deviceExec, view *api.ConfigTargetView, ns, name string) (logTarget, error) {
	targetNS := ns
	for _, r := range view.Configuration.Effective {
		if r.Kind == "HelmChart" && r.Name == name && r.Namespace == ns && r.Chart != nil && r.Chart.TargetNamespace != "" {
			targetNS = r.Chart.TargetNamespace
		}
	}
	res, err := ex.Run(ctx, remoteShell(helmProbeScript(ns, name, targetNS, true)))
	if err != nil {
		return logTarget{}, err
	}
	snap := parseHelmSnapshot(res.Stdout)
	wls := releaseWorkloads(snap.workloads, name)
	switch len(wls) {
	case 0:
		return logTarget{}, newErr(codeNotFound, "the release %s has no Deployment, StatefulSet or DaemonSet on the robot (is it installed yet?)", name)
	case 1:
		return logTarget{targetNS, kubectlResource(digStr(wls[0], "kind")) + "/" + digStr(wls[0], "metadata", "name")}, nil
	}
	var names []string
	for _, w := range wls {
		names = append(names, digStr(w, "kind")+"/"+targetNS+"/"+digStr(w, "metadata", "name"))
	}
	return logTarget{}, usageErr("the release %s has several workloads (%s): name one of them", name, strings.Join(names, ", "))
}

func podLogTarget(ctx context.Context, ex deviceExec, name string) (logTarget, error) {
	res, err := ex.Run(ctx, remoteShell(kubectlOnDevice+" get pods -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name --no-headers 2>&1; true"))
	if err != nil {
		return logTarget{}, err
	}
	var exact, prefix []logTarget
	for _, l := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		switch {
		case f[1] == name:
			exact = append(exact, logTarget{f[0], "pod/" + f[1]})
		case strings.HasPrefix(f[1], name):
			prefix = append(prefix, logTarget{f[0], "pod/" + f[1]})
		}
	}
	cands := exact
	if len(cands) == 0 {
		cands = prefix
	}
	switch len(cands) {
	case 0:
		return logTarget{}, newErr(codeNotFound, "no resource or pod called %q on the robot", name)
	case 1:
		return cands[0], nil
	}
	var names []string
	for _, c := range cands {
		names = append(names, c.ns+"/"+strings.TrimPrefix(c.ref, "pod/"))
	}
	return logTarget{}, usageErr("%q matches several pods: %s", name, strings.Join(names, ", "))
}

func init() { rootCmd.AddCommand(newLogsCmd(defaultEnv)) }

var durationArg = regexp.MustCompile(`^[0-9]+(ms|s|m|h)$`)
