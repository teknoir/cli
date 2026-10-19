package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newExecCmd(e *cliEnv) *cobra.Command {
	var timeout time.Duration
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "exec <device> -- <command...>",
		Short:        "Run a command on a device over its WireGuard tunnel and pass its exit code through",
		Long: `Run one command on a device (the robot's server computer) over its WireGuard tunnel and print
its output. Not interactive: no terminal, ssh BatchMode, so it never waits for a password. The exit
status of the command is the exit status of tnctl; if the device cannot be reached, tnctl exits 1.

The command goes after -- . Several words are passed as separate arguments (quoted for you);
one word is a shell command line, so pipes and && work:
  tnctl exec vision-station-01 -- sudo k3s kubectl get pods -A
  tnctl exec vision-station-01 -- 'df -h / | tail -1'

The login user has passwordless sudo. The robot's k3s is 'sudo k3s kubectl'.`,
		Example: `  tnctl exec vision-station-01 -- uname -a
  tnctl exec vision-station-01 -- sudo k3s kubectl -n kube-system get helmcharts
  tnctl exec vision-station-01 -- 'journalctl -u k3s -n 20 --no-pager'
  tnctl --device vision-station-01 exec -- hostname`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 {
				return usageErr("put -- before the command, for example: tnctl exec vision-station-01 -- uname -a")
			}
			dev, command := "", args[dash:]
			switch dash {
			case 1:
				dev = args[0]
			case 0:
				dev = device // --device / TNCTL_DEVICE
			default:
				return usageErr("exec takes one device before --")
			}
			if dev == "" {
				return usageErr("which device? tnctl exec <device> -- <command...>")
			}
			if len(command) == 0 {
				return usageErr("no command after --")
			}
			line := command[0]
			if len(command) > 1 {
				line = shJoin(command)
			}
			cmd.SilenceUsage = true
			_, domain, ns, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			ex, err := e.newExec(ctx, domain, ns, dev)
			if err != nil {
				return classify(err)
			}
			defer ex.Close()
			code, err := ex.Stream(ctx, line, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return newErr(codeTimeout, "the command did not finish within %s", timeout)
				}
				return classify(fmt.Errorf("cannot run the command on %s: %w", dev, err))
			}
			if code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	}
	c.Flags().DurationVar(&timeout, "timeout", 0, "stop the command after this long (exit code 4); 0 means no limit")
	return c
}

func init() { rootCmd.AddCommand(newExecCmd(defaultEnv)) }
