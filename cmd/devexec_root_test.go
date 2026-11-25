package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/config"
)

// ---------- the pieces ----------

func TestRootFirstLine(t *testing.T) {
	for _, c := range []struct {
		password, line string
		unusable       bool
	}{
		{"", "\n", false},
		{"s3cret", "s3cret\n", false},
		{` sp ace\t'"$HOME -n `, ` sp ace\t'"$HOME -n ` + "\n", false},
		{"two\nlines", "\n", true},
		{"nul\x00byte", "\n", true},
		{"trailing\n", "\n", true},
	} {
		line, unusable := rootFirstLine(c.password)
		if line != c.line || unusable != c.unusable {
			t.Errorf("%q: got %q %v, want %q %v", c.password, line, unusable, c.line, c.unusable)
		}
		if strings.Count(line, "\n") != 1 {
			t.Errorf("%q: the first line must be exactly one line: %q", c.password, line)
		}
	}
}

// The wrapper's exit statuses and markers are the ones the client reads.
func TestRootWrapperCodes(t *testing.T) {
	for reason, code := range rootExitOf {
		frag := fmt.Sprintf(`echo "%s%s" >&2; exit %d;`, rootMark, reason, code)
		if !strings.Contains(rootWrapper, frag) {
			t.Errorf("the wrapper lacks %q", frag)
		}
	}
	// the last sudo is neither exec'ed nor the last command: its ticket belongs to this sh
	if strings.Contains(rootWrapper, "exec sudo") || !strings.HasSuffix(rootWrapper, "sudo -n sh -c \"$1\"\nexit $?") {
		t.Errorf("the wrapper must end in the sudo of the script and an exit:\n%s", rootWrapper)
	}
	if strings.Contains(rootWrapper, "'") {
		t.Error("the wrapper has no single quote, so that its quoting stays one pair")
	}
	cmdline := rootCommand("k3s kubectl get pods -A")
	if !strings.HasPrefix(cmdline, "sh -c '") || !strings.HasSuffix(cmdline, " tnctl-root 'k3s kubectl get pods -A'") {
		t.Errorf("command line: %s", cmdline)
	}
}

func TestRootFailureOf(t *testing.T) {
	for _, c := range []struct {
		code     int
		token    string
		unusable bool
		want     rootReason
		frag     string
	}{
		{rootExitNoPassword, "no-password", false, rootNoPassword, "the platform has none"},
		{rootExitNoPassword, "no-password", true, rootNoPassword, "line break"},
		{rootExitRejected, "rejected", false, rootRejected, "did not accept the platform's password"},
		{rootExitRejected, "rejected", true, rootRejected, "did not accept"},
		{rootExitNoTicket, "no-ticket", false, rootNoTicket, "asks for it again"},
		{rootExitNoSudo, "no-sudo", false, rootNoSudo, "sudo is not installed"},
		{rootExitNoSudo, "no-printf", false, rootNoPrintf, "built-in printf"},
		// the command's own exit status: no marker, a marker of another status, an unknown marker
		{rootExitRejected, "", false, "", ""},
		{rootExitRejected, "no-password", false, "", ""},
		{1, "rejected", false, "", ""},
		{0, "rejected", false, "", ""},
		{rootExitRejected, "something", false, "", ""},
	} {
		re := rootFailureOf("dev-1", c.code, c.token, c.unusable)
		if c.want == "" {
			if re != nil {
				t.Errorf("%d %q: %v, want the command's own exit status", c.code, c.token, re)
			}
			continue
		}
		if re == nil || re.reason != c.want || !strings.Contains(re.Error(), c.frag) || !strings.Contains(re.Error(), "dev-1") {
			t.Errorf("%d %q: %v, want %s with %q", c.code, c.token, re, c.want, c.frag)
		}
	}
}

func TestRootMarkFilter(t *testing.T) {
	for _, c := range []struct{ in, out, token string }{
		{"", "", ""},
		{"plain\n", "plain\n", ""},
		{rootMark + "rejected\n", "", "rejected"},
		{"before\n" + rootMark + "no-password\nafter\n", "before\nafter\n", "no-password"},
		{"no line end", "no line end", ""},
		{"@@tnctl-ro", "@@tnctl-ro", ""},
		{"@@tnctl-rope\n", "@@tnctl-rope\n", ""},
		{"mid line " + rootMark + "rejected\n", "mid line " + rootMark + "rejected\n", ""},
		{"\n\n" + rootMark + "no-sudo\n", "\n\n", "no-sudo"},
		{rootMark + strings.Repeat("x", 100) + "\n", rootMark + strings.Repeat("x", 100) + "\n", ""},
	} {
		// whole, and byte by byte: the marker may arrive in pieces
		for _, step := range []int{len(c.in) + 1, 1, 3} {
			var b bytes.Buffer
			f := &rootMarkFilter{w: &b}
			for i := 0; i < len(c.in); i += step {
				_, _ = f.Write([]byte(c.in[i:min(i+step, len(c.in))]))
			}
			f.flush()
			if b.String() != c.out || f.token != c.token {
				t.Errorf("%q in steps of %d: got %q token %q, want %q token %q", c.in, step, b.String(), f.token, c.out, c.token)
			}
		}
	}
	// output without a line end is passed on at once, not held until the line ends
	var b bytes.Buffer
	f := &rootMarkFilter{w: &b}
	_, _ = f.Write([]byte("50%"))
	if b.String() != "50%" {
		t.Errorf("held back: %q", b.String())
	}
	if clean, token := cutRootMark("sudo: lecture\n" + rootMark + "rejected\n"); clean != "sudo: lecture\n" || token != "rejected" {
		t.Errorf("cutRootMark: %q %q", clean, token)
	}
}

// ---------- wgExec against a stand-in for ssh and sudo ----------

// standInSSH is ssh for these tests: it records its arguments and runs the remote command line
// the way sshd does, with the login shell.
const standInSSH = `#!/bin/sh
printf '%s\n' "$@" >> "$FAKE_DIR/ssh-argv"
echo run >> "$FAKE_DIR/ssh-runs"
for a in "$@"; do last=$a; done
exec "$FAKE_LOGIN_SHELL" -c "$last"
`

// standInSudo behaves like sudo without a terminal as far as the wrapper can tell: -n never asks,
// -S reads the password from stdin (and, like sudo, takes every further line as another attempt),
// -v only checks the password, and a ticket is kept for the parent process. Modes: nopasswd,
// passwd, noticket (the password is accepted but no ticket is kept, as with timestamp_timeout=0).
const standInSudo = `#!/bin/sh
d=$FAKE_DIR
printf '%s\n' "$*" >> "$d/sudo-argv"
mode=$(cat "$d/mode")
n=0 S=0 v=0
while [ $# -gt 0 ]; do
  case $1 in
    -n) n=1 ;;
    -S) S=1 ;;
    -v) v=1 ;;
    -p) shift ;;
    *) break ;;
  esac
  shift
done
ticket="$d/ticket.$PPID"
open=0
[ "$mode" = nopasswd ] && open=1
[ -e "$ticket" ] && open=1
if [ $v = 1 ]; then
  [ $open = 1 ] && exit 0
  if [ $S = 0 ] || [ $n = 1 ]; then echo "sudo: a password is required" >&2; exit 1; fi
  IFS= read -r pw || { echo "sudo: no password was provided" >&2; exit 1; }
  echo x >> "$d/attempts"
  if [ "$pw" = "$(cat "$d/password")" ]; then
    [ "$mode" = noticket ] || : > "$ticket"
    exit 0
  fi
  echo "Sorry, try again." >&2
  while IFS= read -r pw; do echo x >> "$d/attempts"; done
  echo "sudo: incorrect password attempts" >&2
  exit 1
fi
if [ $open = 0 ]; then echo "sudo: a password is required" >&2; exit 1; fi
if [ $S = 1 ]; then echo "fake sudo: -S with a command is the mistake this design avoids" >&2; exit 97; fi
exec "$@"
`

// fakeDevice is a directory that plays a device: a PATH with only the stand-ins and the few
// tools the scripts use, and the state of its sudo.
type fakeDevice struct {
	t   *testing.T
	dir string
	bin string
}

// newFakeDevice sets up the stand-ins. shAs is the program that `sh` is on the device (dash on
// Debian, bash elsewhere), loginShell the login shell of the device user; both are looked up on
// the host and the test is skipped when one is missing.
func newFakeDevice(t *testing.T, shAs, loginShell, mode, devicePassword string) *fakeDevice {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(name, as string) {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("no %s on this host", name)
		}
		if err := os.Symlink(p, filepath.Join(bin, as)); err != nil {
			t.Fatal(err)
		}
	}
	link(shAs, "sh")
	link(loginShell, "login-shell")
	link("cat", "cat")
	link("true", "true")
	for name, text := range map[string]string{"ssh": standInSSH, "sudo": standInSudo} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDevice{t: t, dir: dir, bin: bin}
	d.write("mode", mode)
	d.write("password", devicePassword)
	t.Setenv("PATH", bin)
	t.Setenv("FAKE_DIR", dir)
	t.Setenv("FAKE_LOGIN_SHELL", filepath.Join(bin, "login-shell"))
	return d
}

func (d *fakeDevice) write(name, text string) {
	d.t.Helper()
	if err := os.WriteFile(filepath.Join(d.dir, name), []byte(text), 0o600); err != nil {
		d.t.Fatal(err)
	}
}

func (d *fakeDevice) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(d.dir, name))
	return string(b)
}

// lines is how many lines the record has: ssh runs, password attempts.
func (d *fakeDevice) lines(name string) int { return strings.Count(d.read(name), "\n") }

// shell is a wgExec whose session holds the platform's password for the device login.
func (d *fakeDevice) shell(platformPassword string) *wgExec {
	tmp := filepath.Join(d.dir, "session")
	_ = os.MkdirAll(tmp, 0o700)
	return &wgExec{
		sess:        &wgSSHSession{baseArgs: []string{"-o", "StrictHostKeyChecking=yes", "tn@dev-1"}, password: platformPassword, tmpDir: tmp, device: "dev-1"},
		controlPath: filepath.Join(tmp, "cm"),
	}
}

// captureStderr returns what fn writes to the process's stderr (the --debug lines).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = old
	_ = w.Close()
	return <-done
}

// shells are the (sh, login shell) pairs the wrapper must work with. bash as sh matters: it
// exec's the last command of a -c script, which loses sudo's ticket unless the wrapper prevents it.
var shells = [][2]string{{"sh", "sh"}, {"bash", "bash"}, {"sh", "zsh"}, {"bash", "zsh"}, {"dash", "bash"}}

const manifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\n# the last line has no line end"

// The mode table: what the wrapper does for each state of sudo and each password the platform has.
func TestRootModes(t *testing.T) {
	const right = `Corr3ct 'horse' "battery" $HOME \n -n %s`
	cases := []struct {
		name     string
		mode     string
		platform string
		want     rootReason // "" = the script runs
		unusable bool
		attempts int
	}{
		{"passwordless sudo, a password is sent anyway", "nopasswd", right, "", false, 0},
		{"passwordless sudo, a wrong password is sent", "nopasswd", "wrong", "", false, 0},
		{"passwordless sudo, no password", "nopasswd", "", "", false, 0},
		{"passwordless sudo, a password that cannot be sent", "nopasswd", "a\nb", "", false, 0},
		{"password sudo, the right password", "passwd", right, "", false, 1},
		{"password sudo, no password", "passwd", "", rootNoPassword, false, 0},
		{"password sudo, a password that cannot be sent", "passwd", right + "\n", rootNoPassword, true, 0},
		{"password sudo, a password with a NUL", "passwd", "a\x00b", rootNoPassword, true, 0},
		{"password sudo, a wrong password", "passwd", "wrong", rootRejected, false, 1},
		{"password sudo that keeps no ticket", "noticket", right, rootNoTicket, false, 1},
	}
	for _, sh := range shells {
		for _, c := range cases {
			t.Run(sh[0]+"+"+sh[1]+"/"+c.name, func(t *testing.T) {
				d := newFakeDevice(t, sh[0], sh[1], c.mode, right)
				ex := d.shell(c.platform)
				config.Debug = true
				defer func() { config.Debug = false }()

				var out, errb bytes.Buffer
				var code int
				var err error
				debug := captureStderr(t, func() {
					// the manifest case: the payload follows the first line on the same stdin
					code, err = ex.StreamRoot(context.Background(), "cat; echo; echo 'it'\\''s root' >&2; exit 7", strings.NewReader(manifest), &out, &errb)
				})
				if c.want == "" {
					if err != nil || code != 7 || out.String() != manifest+"\n" || errb.String() != "it's root\n" {
						t.Errorf("code %d err %v\nstdout %q\nstderr %q", code, err, out.String(), errb.String())
					}
				} else {
					var re *rootError
					if !errors.As(err, &re) || re.reason != c.want || re.unusable != c.unusable || code != rootExitOf[c.want] {
						t.Errorf("code %d err %v, want %s (unusable %v)", code, err, c.want, c.unusable)
					}
					if out.Len() != 0 || errb.Len() != 0 {
						t.Errorf("the script must not run, and the marker is not shown:\nstdout %q\nstderr %q", out.String(), errb.String())
					}
				}
				if got := d.lines("attempts"); got != c.attempts {
					t.Errorf("%d password attempts, want %d", got, c.attempts)
				}
				if !strings.Contains(debug, "DEBUG: Executing command: ssh") {
					t.Errorf("no debug line: %q", debug)
				}
				// The password is on stdin only: not in ssh's arguments, sudo's arguments, the
				// debug line or the output. (A password with a line break is checked by its parts.)
				for _, secret := range strings.FieldsFunc(c.platform, func(r rune) bool { return r == '\n' || r == 0 }) {
					if len(secret) < 4 {
						continue
					}
					for where, text := range map[string]string{"ssh argv": d.read("ssh-argv"), "sudo argv": d.read("sudo-argv"),
						"debug": debug, "stdout": out.String(), "stderr": errb.String(), "error": fmt.Sprint(err)} {
						if strings.Contains(text, secret) {
							t.Errorf("the password is in %s:\n%s", where, text)
						}
					}
				}
			})
		}
	}
}

// RunRoot captures: the output and exit status are the script's, the first line is not in them,
// and a script that reads stdin finds it empty.
func TestRootRun(t *testing.T) {
	for _, mode := range []string{"nopasswd", "passwd"} {
		d := newFakeDevice(t, "sh", "sh", mode, "pw-12345")
		ex := d.shell("pw-12345")
		res, err := ex.RunRoot(context.Background(), `echo "stdin=[$(cat)]"; echo warn >&2; exit 3`)
		if err != nil || res.Code != 3 || res.Stdout != "stdin=[]\n" || res.Stderr != "warn\n" {
			t.Errorf("%s: %+v %v", mode, res, err)
		}
		// a second call from the same session: nothing of the first one is left over
		res, err = ex.RunRoot(context.Background(), "echo again")
		if err != nil || res.Code != 0 || res.Stdout != "again\n" {
			t.Errorf("%s: second call %+v %v", mode, res, err)
		}
		if got, want := d.lines("attempts"), map[string]int{"nopasswd": 0, "passwd": 2}[mode]; got != want {
			t.Errorf("%s: %d password attempts, want %d", mode, got, want)
		}
		ex.Close()
		if _, err := os.Stat(ex.sess.tmpDir); !os.IsNotExist(err) {
			t.Errorf("%s: the session directory is left behind", mode)
		}
	}
}

// A rejected password is final for the session: it is sent once, whatever is called afterwards,
// and the device is not contacted again.
func TestRootRejectedPasswordIsSentOnce(t *testing.T) {
	d := newFakeDevice(t, "sh", "sh", "passwd", "the-real-one")
	ex := d.shell("an-old-one")
	_, err := ex.RunRoot(context.Background(), "echo never")
	var re *rootError
	if !errors.As(err, &re) || re.reason != rootRejected {
		t.Fatalf("got %v", err)
	}
	for i := 0; i < 3; i++ {
		if res, err := ex.RunRoot(context.Background(), "echo never"); !errors.As(err, &re) || re.reason != rootRejected || res.Stdout != "" {
			t.Errorf("call %d: %+v %v", i, res, err)
		}
	}
	var out bytes.Buffer
	if _, err := ex.StreamRoot(context.Background(), "echo never", strings.NewReader("line 1\nline 2\nline 3\n"), &out, io.Discard); !errors.As(err, &re) || out.Len() != 0 {
		t.Errorf("stream: %v %q", err, out.String())
	}
	if got := d.lines("attempts"); got != 1 {
		t.Errorf("%d password attempts, want exactly 1", got)
	}
	if got := d.lines("ssh-runs"); got != 1 {
		t.Errorf("%d ssh runs, want 1: nothing is sent after the refusal", got)
	}
	// a command as the login user still works
	if res, err := ex.Run(context.Background(), "echo plain"); err != nil || res.Stdout != "plain\n" {
		t.Errorf("plain: %+v %v", res, err)
	}
}

// With a wrong password the lines of the payload are not taken as further attempts: sudo's
// password reader sees one line and then the end of its input.
func TestRootWrongPasswordDoesNotGuessWithThePayload(t *testing.T) {
	for _, sh := range shells {
		t.Run(sh[0]+"+"+sh[1], func(t *testing.T) {
			d := newFakeDevice(t, sh[0], sh[1], "passwd", "the-real-one")
			ex := d.shell("wrong")
			var out, errb bytes.Buffer
			code, err := ex.StreamRoot(context.Background(), "cat", strings.NewReader("the-real-one\nline 2\nline 3\n"), &out, &errb)
			if !isRootError(err) || code != rootExitRejected || out.Len() != 0 {
				t.Errorf("code %d err %v stdout %q", code, err, out.String())
			}
			if got := d.lines("attempts"); got != 1 {
				t.Errorf("%d password attempts, want 1 (the payload even has the right password in it)", got)
			}
		})
	}
}

func TestRootWithoutSudo(t *testing.T) {
	d := newFakeDevice(t, "sh", "sh", "nopasswd", "pw")
	if err := os.Remove(filepath.Join(d.bin, "sudo")); err != nil {
		t.Fatal(err)
	}
	res, err := d.shell("pw").RunRoot(context.Background(), "echo never")
	var re *rootError
	if !errors.As(err, &re) || re.reason != rootNoSudo || res.Stdout != "" || !strings.Contains(err.Error(), "sudo is not installed on dev-1") {
		t.Errorf("%+v %v", res, err)
	}
}

// A script may exit with one of the wrapper's statuses: without the wrapper's marker it is the
// script's own, and the session stays usable.
func TestRootExitStatusOfTheScriptIsItsOwn(t *testing.T) {
	d := newFakeDevice(t, "sh", "sh", "passwd", "pw-12345")
	ex := d.shell("pw-12345")
	for _, code := range []int{rootExitNoPassword, rootExitRejected, rootExitNoTicket, rootExitNoSudo} {
		res, err := ex.RunRoot(context.Background(), fmt.Sprintf("echo mine >&2; exit %d", code))
		if err != nil || res.Code != code || res.Stderr != "mine\n" {
			t.Errorf("run %d: %+v %v", code, res, err)
		}
		var errb bytes.Buffer
		got, err := ex.StreamRoot(context.Background(), fmt.Sprintf("echo mine >&2; exit %d", code), nil, io.Discard, &errb)
		if err != nil || got != code || errb.String() != "mine\n" {
			t.Errorf("stream %d: %d %v %q", code, got, err, errb.String())
		}
	}
}

// The script is one argument of the wrapper: quotes, $1 and line breaks in it are the script's.
func TestRootScriptQuoting(t *testing.T) {
	d := newFakeDevice(t, "sh", "zsh", "passwd", "pw-12345")
	ex := d.shell("pw-12345")
	script := "set -- one two\nprintf '%s|' \"it's $1\" 'a \"b\" $2' \"$#\"\necho\n"
	res, err := ex.RunRoot(context.Background(), script)
	if err != nil || res.Stdout != "it's one|a \"b\" $2|2|\n" {
		t.Errorf("%+v %v", res, err)
	}
}

// tnctl exec --root on a terminal: nobody types, and the command must still end when the remote
// command does (the copy of stdin must not hold it back).
func TestRootStreamDoesNotWaitForStdin(t *testing.T) {
	d := newFakeDevice(t, "sh", "sh", "passwd", "pw-12345")
	ex := d.shell("pw-12345")
	silent, w := io.Pipe()
	defer func() { _ = w.Close() }()
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		_, err := ex.StreamRoot(context.Background(), "echo done", silent, &out, io.Discard)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil || out.String() != "done\n" {
			t.Errorf("%v %q", err, out.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("StreamRoot waits for a stdin that never ends")
	}
}

// ---------- rootProbe: a sudo failure is never an empty answer ----------

func TestRootProbe(t *testing.T) {
	script := helmProbeScript("default", "notebook", "default", false)
	for _, c := range []struct {
		name  string
		res   execResult
		final bool   // a *rootError: not retried
		frag  string // "" = no error
	}{
		{"answers", execResult{Stdout: probeOutput(map[string]string{"chart": "", "job": ""})}, false, ""},
		{"sudo refuses the command", execResult{Code: 1, Stderr: "sudo: a password is required\n"}, true, "sudo on dev-1 refused to run the command: sudo: a password is required"},
		{"sudoers does not allow it", execResult{Code: 1, Stderr: "Sorry, user tn is not allowed to execute '/bin/sh -c ...' as root on dev-1.\n"}, true, "not allowed to execute"},
		{"the shell died", execResult{Code: 137}, false, "exit status 137"},
		{"another failure", execResult{Code: 2, Stderr: "sh: 1: Syntax error\n"}, false, "exit status 2: sh: 1: Syntax error"},
		{"no output at all", execResult{}, false, "printed nothing"},
	} {
		ex := &fakeExec{run: func(string) (execResult, error) { return c.res, nil }}
		_, err := rootProbe(context.Background(), ex, "dev-1", script)
		switch {
		case c.frag == "":
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
		case err == nil || !strings.Contains(err.Error(), c.frag) || isRootError(err) != c.final:
			t.Errorf("%s: %v (final %v), want %q (final %v)", c.name, err, isRootError(err), c.frag, c.final)
		}
	}
	// a script without sections (the pod list) may print nothing
	ex := &fakeExec{}
	if _, err := rootProbe(context.Background(), ex, "dev-1", "k3s kubectl get pods -A 2>&1; true"); err != nil {
		t.Errorf("no sections: %v", err)
	}
}

// config remove --wait=applied asks "is it gone?". A sudo that fails prints nothing on stdout,
// which is also what "gone" looks like: it must end the wait as an error, never as success.
func TestRemoveWaitNeverReadsASudoFailureAsGone(t *testing.T) {
	for name, c := range map[string]struct {
		res   execResult
		calls int
		frag  string
	}{
		"sudo refuses":   {execResult{Code: 1, Stderr: "sudo: a password is required\n"}, 1, "sudo on vision-station-01 refused to run the command"},
		"exit status":    {execResult{Code: 1}, 6, "exit status 1"},
		"nothing at all": {execResult{}, 6, "printed nothing"},
	} {
		p := removeAppliedPlatform(t)
		ex := &fakeExec{run: func(string) (execResult, error) { return c.res, nil }}
		e, _ := testEnv(t, p, ex)
		_, errs, err := runCLI(t, e, "config", "remove", "vision-station-01", llmID, "--wait=applied")
		if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), c.frag) {
			t.Errorf("%s: exit %d: %v", name, exitCodeOf(err), err)
		}
		if strings.Contains(errs, "is gone from the robot") || strings.Contains(errs, "applied after") {
			t.Errorf("%s: reported as gone:\n%s", name, errs)
		}
		if n := len(ex.rootList()); n != c.calls {
			t.Errorf("%s: %d probes, want %d", name, n, c.calls)
		}
	}
}

// The same for --wait=applied of a change: not "the HelmChart is not on the device yet" until
// the timeout, but the cause, before anything is written.
func TestWaitNeverReadsASudoFailureAsNotFound(t *testing.T) {
	p := appliedPlatform(t)
	ex := &fakeExec{run: func(string) (execResult, error) {
		return execResult{Code: 1, Stderr: "sudo: a password is required\n"}, nil
	}}
	e, _ := testEnv(t, p, ex)
	_, errs, err := setWait(t, e, "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "refused to run the command") || !strings.Contains(err.Error(), "--wait=published") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if strings.Contains(errs, "not on the device yet") || len(p.seen("PATCH", patchPath)) != 0 {
		t.Errorf("read as not found, or written:\n%s", errs)
	}
}

// The password is rejected in the middle of a wait (somebody changed it on the robot): the wait
// ends there. The poll loop does not send it again.
func TestWaitRejectedPasswordEndsThePolling(t *testing.T) {
	p := appliedPlatform(t)
	dev := &helmDevice{step: func(int) map[string]string {
		return map[string]string{"chart": chartJSON("image:\n  tag: main\nreplicas: 1\n", "0.0.5"), "job": jobJSON("u1", 1, 0)}
	}}
	ex := &fakeExec{password: "pw-12345"}
	ex.run = func(script string) (execResult, error) {
		res, err := dev.run(script)
		if dev.probes == 3 {
			ex.rootErr = &rootError{reason: rootRejected, device: "vision-station-01"}
		}
		return res, err
	}
	e, _ := testEnv(t, p, ex)
	_, errs, err := setWait(t, e, "--wait=applied", "--timeout", "2m")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "did not accept the platform's password") || !strings.Contains(err.Error(), "--wait=published") {
		t.Errorf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	roots := ex.rootList()
	if len(roots) != 4 {
		t.Errorf("%d root calls, want 4: three that worked and the one that was refused", len(roots))
	}
	for _, r := range roots {
		if r.firstLine != "pw-12345" || r.payload != "" || r.stream {
			t.Errorf("root call %+v", r)
		}
	}
	if strings.Contains(errs, "pw-12345") || strings.Contains(err.Error(), "pw-12345") {
		t.Error("the password is in the output")
	}
}

// ---------- tnctl exec --root, tnctl logs ----------

func TestExecRoot(t *testing.T) {
	p := execPlatform(t)
	ex := &fakeExec{password: "pw-12345", run: func(string) (execResult, error) { return execResult{Stdout: "ok\n"}, nil }}
	e, _ := testEnv(t, p, ex)
	cliStdin = strings.NewReader(manifest)
	defer func() { cliStdin = nil }()

	out, _, err := runCLI(t, e, "exec", "--root", "vision-station-01", "--", "k3s", "kubectl", "apply", "-f", "-")
	if err != nil || out != "ok\n" {
		t.Fatalf("%v %q", err, out)
	}
	roots := ex.rootList()
	if len(roots) != 1 || roots[0] != (rootCall{script: "k3s kubectl apply -f -", firstLine: "pw-12345", payload: manifest, stream: true}) {
		t.Errorf("root calls %+v", roots)
	}
	// one word is a shell command line, also as root
	cliStdin = strings.NewReader("")
	if _, _, err := runCLI(t, e, "exec", "--root", "vision-station-01", "--", "journalctl -u k3s -n 20 | tail -1"); err != nil {
		t.Fatal(err)
	}
	if roots = ex.rootList(); len(roots) != 2 || roots[1].script != "journalctl -u k3s -n 20 | tail -1" {
		t.Errorf("root calls %+v", roots)
	}
	// without --root nothing changes: the command runs as the login user, no first line
	if _, _, err := runCLI(t, e, "exec", "vision-station-01", "--", "uname", "-a"); err != nil {
		t.Fatal(err)
	}
	if len(ex.rootList()) != 2 || lastCall(ex) != "uname -a" {
		t.Errorf("plain exec: %v", ex.callList())
	}
	// the exit status of the command is ours, as without --root
	ex.stream = func(string) int { return 5 }
	_, _, err = runCLI(t, e, "exec", "--root", "vision-station-01", "--", "false")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 5 {
		t.Errorf("exit status: %v", err)
	}
}

func TestExecRootRefused(t *testing.T) {
	p := execPlatform(t)
	for reason, frag := range map[rootReason]string{
		rootNoPassword: "sudo on vision-station-01 asks for a password, and the platform has none",
		rootRejected:   "did not accept the platform's password",
		rootNoTicket:   "asks for it again",
	} {
		ex := &fakeExec{rootErr: &rootError{reason: reason, device: "vision-station-01"}}
		e, _ := testEnv(t, p, ex)
		out, _, err := runCLI(t, e, "exec", "--root", "vision-station-01", "--", "id")
		if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot run the command on vision-station-01: ") || !strings.Contains(err.Error(), frag) || out != "" {
			t.Errorf("%s: exit %d: %v", reason, exitCodeOf(err), err)
		}
	}
}

func TestLogsRefused(t *testing.T) {
	p := logsPlatform(t)
	ex := &fakeExec{rootErr: &rootError{reason: rootRejected, device: "vision-station-01"}}
	e, _ := testEnv(t, p, ex)
	// a workload: the stream itself is refused; a HelmChart or a pod: the lookup before it
	for _, arg := range []string{"Deployment/default/whoami", "HelmChart/default/notebook", "notebook-77f875b6c-45s8v"} {
		_, _, err := runCLI(t, e, "logs", "vision-station-01", arg)
		if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot read the logs on vision-station-01: sudo on vision-station-01 did not accept") {
			t.Errorf("%s: exit %d: %v", arg, exitCodeOf(err), err)
		}
	}
}
