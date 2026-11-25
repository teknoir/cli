package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// This file is how tnctl runs a command as root on a device. The login user reaches root through
// sudo, and on most devices sudo asks for the user's password (the bootstrap gives passwordless
// sudo only to a login without a password). The platform holds that password next to the login
// key, so tnctl hands it to sudo itself.
//
// One wrapper runs as the login user and decides per call what sudo needs. tnctl always sends
// exactly one first line on stdin (the password, or an empty line), and the wrapper always reads
// exactly that line itself. So the payload on stdin never meets sudo's password reader, whatever
// state sudo is in: passwordless, a ticket that is still valid, or a password that is wrong.

// Exit statuses of the wrapper when sudo cannot be used. They are only read together with the
// marker line the wrapper prints on stderr, so a command that happens to exit with one of them
// keeps its own exit status.
const (
	rootExitNoPassword = 91 // sudo needs a password and none was sent
	rootExitRejected   = 92 // sudo did not accept the password
	rootExitNoTicket   = 93 // sudo accepted the password but asks again for the next command
	rootExitNoSudo     = 94 // no sudo, or no way to hand it the password safely
)

// rootMark starts the stderr line with which the wrapper says why it gave up.
const rootMark = "@@tnctl-root:"

type rootReason string

const (
	rootNoPassword rootReason = "no-password"
	rootRejected   rootReason = "rejected"
	rootNoTicket   rootReason = "no-ticket"
	rootNoSudo     rootReason = "no-sudo"
	rootNoPrintf   rootReason = "no-printf"
	// rootRefused is sudo refusing the command itself, read from its own message (see rootProbe).
	rootRefused rootReason = "refused"
)

var rootExitOf = map[rootReason]int{
	rootNoPassword: rootExitNoPassword,
	rootRejected:   rootExitRejected,
	rootNoTicket:   rootExitNoTicket,
	rootNoSudo:     rootExitNoSudo,
	rootNoPrintf:   rootExitNoSudo,
}

// rootWrapper is the script the login user's sh runs; $1 is the script to run as root.
//
//   - read takes the first line of stdin and nothing more (sh reads a pipe byte by byte).
//   - `sudo -n true` is the question "does sudo work without a password right now?". It is not
//     `sudo -n -v`: a login with a passwordless rule and the sudo group needs a password for -v.
//   - The password goes to `sudo -S -v` on a pipe of its own, one line and then EOF: a wrong
//     password is one failed attempt, and no line of the payload can be taken as a second guess.
//     An empty line is never sent: sudo counts it as a failed attempt. printf must be the shell's
//     builtin, or the password would be in a process's arguments.
//   - Without a terminal sudo keeps the ticket for the parent process. Every sudo here is a child
//     of this one sh, so the ticket of -v is the one the last sudo finds. That is why the last
//     sudo is not exec'ed and why `exit` follows it: bash and zsh exec the last command of a -c
//     script, which makes the ticket unreachable ("sudo: a password is required").
//   - Every sudo but the last reads /dev/null or the password pipe, never the payload.
const rootWrapper = `IFS= read -r p || :
command -v sudo >/dev/null 2>&1 || { echo "` + rootMark + `no-sudo" >&2; exit 94; }
if ! sudo -n true </dev/null 2>/dev/null; then
  [ -n "$p" ] || { echo "` + rootMark + `no-password" >&2; exit 91; }
  [ "$(command -v printf)" = printf ] || { echo "` + rootMark + `no-printf" >&2; exit 94; }
  printf "%s\n" "$p" | sudo -S -p "" -v 2>/dev/null || { echo "` + rootMark + `rejected" >&2; exit 92; }
  sudo -n true </dev/null 2>/dev/null || { echo "` + rootMark + `no-ticket" >&2; exit 93; }
fi
unset p
sudo -n sh -c "$1"
exit $?`

// rootCommand is the remote command line that runs script as root. It holds no secret: the
// password travels on stdin only.
func rootCommand(script string) string {
	return "sh -c " + shq(rootWrapper) + " tnctl-root " + shq(script)
}

// rootFirstLine is the line tnctl sends before the payload: the password, or an empty line when
// there is none. A password with a line break or a NUL byte cannot travel as one line; it is not
// sent (unusable), and sudo then gets no password at all.
func rootFirstLine(password string) (line string, unusable bool) {
	if strings.ContainsAny(password, "\n\x00") {
		return "\n", true
	}
	return password + "\n", false
}

// rootError says that the login user cannot become root on the device. It is final for the
// command: nothing is tried again (a rejected password must not be sent a second time, each
// attempt counts against the login on the device).
type rootError struct {
	reason rootReason
	device string
	// unusable: the platform has a password, but it cannot be sent as one line.
	unusable bool
	// detail is sudo's own message, for rootRefused.
	detail string
}

func (e *rootError) Error() string {
	switch e.reason {
	case rootNoPassword:
		if e.unusable {
			return fmt.Sprintf("sudo on %s asks for a password, and the platform's password for the device login has a line break or a NUL byte in it, which cannot be sent to sudo", e.device)
		}
		return fmt.Sprintf("sudo on %s asks for a password, and the platform has none for the device login", e.device)
	case rootRejected:
		return fmt.Sprintf("sudo on %s did not accept the platform's password for the device login (the password on the device is another one, or the login user may not use sudo); it is not tried again", e.device)
	case rootNoTicket:
		return fmt.Sprintf("sudo on %s accepted the password but asks for it again for the next command (timestamp_timeout=0 in its sudoers?), so tnctl cannot run commands as root there", e.device)
	case rootNoPrintf:
		return fmt.Sprintf("the sh on %s has no built-in printf, so the password cannot be handed to sudo without showing it in the process list", e.device)
	case rootRefused:
		return fmt.Sprintf("sudo on %s refused to run the command: %s", e.device, e.detail)
	}
	return fmt.Sprintf("sudo is not installed on %s (or not on the login user's PATH)", e.device)
}

// isRootError reports whether err is the login user not getting root on the device.
func isRootError(err error) bool {
	var re *rootError
	return errors.As(err, &re)
}

// rootFailureOf turns a wrapper exit status and its marker into the error; nil when the exit
// status is the command's own.
func rootFailureOf(device string, code int, token string, unusable bool) *rootError {
	reason := rootReason(token)
	if want, ok := rootExitOf[reason]; !ok || want != code {
		return nil
	}
	return &rootError{reason: reason, device: device, unusable: unusable && reason == rootNoPassword}
}

// rootMarkFilter passes stderr through and takes the wrapper's marker line out of it. Only the
// start of a line that may still become the marker is held back, so progress output without a
// line end is not delayed.
type rootMarkFilter struct {
	w     io.Writer
	state int
	held  []byte
	token string
}

const (
	markLineStart = iota
	markPassing
	markToken
)

// maxRootToken bounds a marker's token; a longer line is not the wrapper's.
const maxRootToken = 32

func (f *rootMarkFilter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch f.state {
		case markLineStart:
			f.held = append(f.held, b)
			if string(f.held) != rootMark[:len(f.held)] {
				out = append(out, f.held...)
				f.held = f.held[:0]
				if b != '\n' {
					f.state = markPassing
				}
			} else if len(f.held) == len(rootMark) {
				f.state = markToken
			}
		case markPassing:
			out = append(out, b)
			if b == '\n' {
				f.state = markLineStart
			}
		case markToken:
			switch {
			case b == '\n':
				f.token = string(f.held[len(rootMark):])
				f.held = f.held[:0]
				f.state = markLineStart
			case len(f.held) >= len(rootMark)+maxRootToken:
				out = append(append(out, f.held...), b)
				f.held = f.held[:0]
				f.state = markPassing
			default:
				f.held = append(f.held, b)
			}
		}
	}
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// flush writes what is still held back at the end of the stream.
func (f *rootMarkFilter) flush() {
	if len(f.held) > 0 {
		_, _ = f.w.Write(f.held)
		f.held = f.held[:0]
	}
}

// cutRootMark returns stderr without the wrapper's marker line, and the marker's token.
func cutRootMark(stderr string) (clean, token string) {
	var b bytes.Buffer
	f := &rootMarkFilter{w: &b}
	_, _ = f.Write([]byte(stderr))
	f.flush()
	return b.String(), f.token
}

// feedStdin gives cmd the first line and then stdin. It returns the function to call once cmd
// has started. With a stdin of the caller the copy runs outside cmd: Wait must not wait for a
// terminal that nobody types on after the remote command has ended.
func feedStdin(cmd *exec.Cmd, first string, stdin io.Reader) (func(), error) {
	if stdin == nil {
		cmd.Stdin = strings.NewReader(first)
		return nil, nil
	}
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	return func() {
		go func() {
			defer func() { _ = pipe.Close() }()
			if _, err := io.WriteString(pipe, first); err != nil {
				return
			}
			_, _ = io.Copy(pipe, stdin)
		}()
	}, nil
}

// rootFailed returns the root failure this session already ran into.
func (w *wgExec) rootFailed() error {
	w.rootMu.Lock()
	defer w.rootMu.Unlock()
	if w.rootErr != nil {
		return w.rootErr
	}
	return nil
}

func (w *wgExec) rootFailure(code int, token string, unusable bool) error {
	re := rootFailureOf(w.sess.device, code, token, unusable)
	if re == nil {
		return nil
	}
	w.rootMu.Lock()
	defer w.rootMu.Unlock()
	if w.rootErr == nil {
		w.rootErr = re
	}
	return w.rootErr
}

// RunRoot runs a sh script as root and captures its output.
func (w *wgExec) RunRoot(ctx context.Context, script string) (execResult, error) {
	if err := w.rootFailed(); err != nil {
		return execResult{}, err
	}
	first, unusable := rootFirstLine(w.sess.password)
	cmd := w.command(ctx, rootCommand(script), true)
	cmd.Stdin = strings.NewReader(first)
	res, err := w.capture(ctx, cmd)
	if err != nil {
		return res, err
	}
	var token string
	res.Stderr, token = cutRootMark(res.Stderr)
	if rerr := w.rootFailure(res.Code, token, unusable); rerr != nil {
		return res, rerr
	}
	return res, nil
}

// StreamRoot runs a sh script as root with the given streams; stdin (may be nil) is what the
// script reads.
func (w *wgExec) StreamRoot(ctx context.Context, script string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if err := w.rootFailed(); err != nil {
		return 0, err
	}
	first, unusable := rootFirstLine(w.sess.password)
	cmd := w.command(ctx, rootCommand(script), false)
	filter := &rootMarkFilter{w: stderr}
	cmd.Stdout, cmd.Stderr = stdout, filter
	started, err := feedStdin(cmd, first, stdin)
	if err != nil {
		return 0, err
	}
	err = runForwarding(cmd, started)
	filter.flush()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		if rerr := w.rootFailure(ee.ExitCode(), filter.token, unusable); rerr != nil {
			return ee.ExitCode(), rerr
		}
		return ee.ExitCode(), nil
	}
	return 0, err
}

// rootProbe runs a probe script as root. The probe scripts end in `true` and print a section
// marker before each answer, so any other exit status, or no marker at all, means that the
// script did not run: that is an error, never an empty answer (which would read as "the object
// is not on the robot"). sudo's own refusal is final.
func rootProbe(ctx context.Context, ex deviceExec, device, script string) (execResult, error) {
	res, err := ex.RunRoot(ctx, script)
	if err != nil {
		return res, err
	}
	line := ""
	for _, l := range strings.Split(res.Stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if res.Code != 0 {
		if strings.HasPrefix(line, "sudo:") || strings.HasPrefix(line, "Sorry,") {
			return res, &rootError{reason: rootRefused, device: device, detail: line}
		}
		if line == "" {
			line = "no error message"
		}
		return res, fmt.Errorf("the check on the robot ended with exit status %d: %s", res.Code, line)
	}
	if strings.Contains(script, sectionMark) && !strings.Contains(res.Stdout, sectionMark) {
		return res, errors.New("the check on the robot printed nothing")
	}
	return res, nil
}
