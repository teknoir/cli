package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
)

// execResult is the outcome of a command run on a device.
type execResult struct {
	Stdout string
	Stderr string
	// Code is the exit status of the remote command.
	Code int
}

// deviceExec runs commands on a device over its WireGuard SSH tunnel. The real one is wgExec;
// tests use a fake.
type deviceExec interface {
	// Run runs a shell command line and captures its output. A non-zero exit status of the
	// command is a result, not an error; an error means the device could not be reached.
	Run(ctx context.Context, cmdline string) (execResult, error)
	// Stream runs a shell command line with the given streams and returns its exit status.
	Stream(ctx context.Context, cmdline string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
	Close()
}

// signalContext returns a context that ends on SIGINT, SIGTERM or SIGHUP. Commands that put the
// device login key in a temporary directory use it while they connect and poll, so that a
// terminated tnctl returns through its deferred cleanup instead of leaving the key behind.
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

// sshConnectFailed is the exit status ssh itself uses when it cannot connect.
const sshConnectFailed = 255

// shq quotes s for a POSIX shell.
func shq(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shJoin quotes each word and joins them: an argv as a shell command line.
func shJoin(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = shq(w)
	}
	return strings.Join(q, " ")
}

// remoteShell wraps a script so that sh runs it, whatever login shell the device user has.
func remoteShell(script string) string { return "sh -c " + shq(script) }

// buildSSHExecArgs turns the base ssh arguments (ending in user@device, see buildSSHArgs)
// into those of a non-interactive command: no tty, never a password prompt and, when
// controlPath is set, one multiplexed connection shared by all polls of a --wait.
func buildSSHExecArgs(base []string, controlPath, remote string) []string {
	dest := base[len(base)-1]
	args := append([]string{}, base[:len(base)-1]...)
	args = append(args, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=60")
	if controlPath != "" {
		args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-o", "ControlPath="+controlPath)
	}
	return append(args, dest, remote)
}

// wgExec is deviceExec over `ssh` with the WireGuard ProxyCommand.
type wgExec struct {
	sess        *wgSSHSession
	controlPath string
}

// newWGExec opens the WireGuard SSH channel to a device. It needs an active WireGuard
// tunnel: the legacy SSH tunnel is not supported for exec.
func newWGExec(ctx context.Context, domain, ns, deviceName string) (deviceExec, error) {
	return newWGExecWith(ctx, api.NewFleet(domain, ns), domain, ns, deviceName)
}

// newWGExecWith is newWGExec with the fleet client given. It asks the platform for the device
// first (no secrets) and for its login only when the device can be reached.
func newWGExecWith(ctx context.Context, fl *api.Fleet, domain, ns, deviceName string) (deviceExec, error) {
	dev, err := fl.Device(ctx, deviceName)
	if err != nil {
		return nil, classify(err)
	}
	kind, err := selectTunnel(dev, deviceName, false, os.Stderr)
	if err != nil {
		return nil, err
	}
	if kind != tunnelWireGuard {
		return nil, fmt.Errorf("%s has no active WireGuard tunnel; running commands on a device needs it (see: tnctl wg status %s)", deviceName, deviceName)
	}
	login, err := loadLogin(ctx, fl, deviceName)
	if err != nil {
		return nil, err
	}
	sess, err := openWGSSH(domain, ns, deviceName, login)
	if err != nil {
		return nil, err
	}
	return &wgExec{sess: sess, controlPath: filepath.Join(sess.tmpDir, "cm")}, nil
}

func (w *wgExec) command(ctx context.Context, cmdline string, multiplex bool) *exec.Cmd {
	cp := ""
	if multiplex {
		cp = w.controlPath
	}
	args := buildSSHExecArgs(w.sess.baseArgs, cp, cmdline)
	if config.Debug {
		fmt.Fprintf(os.Stderr, "DEBUG: Executing command: ssh %v\n", args)
	}
	return exec.CommandContext(ctx, "ssh", args...)
}

func (w *wgExec) Run(ctx context.Context, cmdline string) (execResult, error) {
	cmd := w.command(ctx, cmdline, true)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := execResult{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return res, nil
	case ctx.Err() != nil:
		return res, ctx.Err()
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
		if res.Code == sshConnectFailed {
			return res, fmt.Errorf("cannot reach %s over WireGuard: %s", w.sess.device, firstNonEmptyLine(res.Stderr))
		}
		return res, nil
	}
	return res, err
}

func (w *wgExec) Stream(ctx context.Context, cmdline string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := w.command(ctx, cmdline, false)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := runWithSignalForwarding(cmd)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		return ee.ExitCode(), nil
	}
	return 0, err
}

// Close stops the multiplexed ssh master (so it does not outlive tnctl) and removes the key.
func (w *wgExec) Close() {
	if _, err := os.Stat(w.controlPath); err == nil {
		dest := w.sess.baseArgs[len(w.sess.baseArgs)-1]
		args := append([]string{}, w.sess.baseArgs[:len(w.sess.baseArgs)-1]...)
		args = append(args, "-o", "ControlPath="+w.controlPath, "-O", "exit", dest)
		_ = exec.Command("ssh", args...).Run()
	}
	w.sess.Close()
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "ssh failed"
}
