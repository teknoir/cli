package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
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
		unusable, ok   bool
	}{
		{"", "", false, false},
		{"s3cret", "s3cret\n", false, true},
		{` sp ace\t'"$HOME -n `, ` sp ace\t'"$HOME -n ` + "\n", false, true},
		{"two\nlines", "", true, false},
		{"nul\x00byte", "", true, false},
		{"trailing\n", "", true, false},
	} {
		line, unusable, ok := rootFirstLine(c.password)
		if line != c.line || unusable != c.unusable || ok != c.ok {
			t.Errorf("%q: got %q %v %v, want %q %v %v", c.password, line, unusable, ok, c.line, c.unusable, c.ok)
		}
		if ok && (strings.Count(line, "\n") != 1 || line == "\n") {
			t.Errorf("%q: the first line must be exactly one line, and never an empty one: %q", c.password, line)
		}
	}
}

// The wrapper's exit statuses and markers are the ones the client reads, and its shape is the
// one sudo's ticket and sudo's input log need.
func TestRootWrapperShape(t *testing.T) {
	for reason, code := range rootExitOf {
		frag := fmt.Sprintf(`{ echo "%s%s" >&2; exit %d; }`, rootMark, reason, code)
		if !strings.Contains(rootWrapper, frag) {
			t.Errorf("the wrapper lacks %q", frag)
		}
	}
	// The last sudo is neither exec'ed nor the last command: its ticket belongs to this sh.
	if !strings.HasSuffix(rootWrapper, "\nLC_ALL=C sudo -n sh -c \"$2\" tnctl-root \"$1\" \"$L\"\nexit $?") {
		t.Errorf("the wrapper must end in the sudo of the script and an exit:\n%s", rootWrapper)
	}
	sudos := 0
	for _, line := range strings.Split(rootWrapper, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "sudo -") {
			continue
		}
		sudos++
		// every sudo speaks English, and none is exec'ed or run in a $( ), which is another parent
		if !strings.Contains(line, "LC_ALL=C sudo -") || strings.Contains(line, "exec ") || strings.Contains(line, "$(") {
			t.Errorf("sudo must run as `LC_ALL=C sudo`, as a child of the wrapper's own sh: %s", line)
		}
		// only the sudo that runs the script may read the wrapper's stdin (Defaults log_input)
		last := strings.HasPrefix(line, "LC_ALL=C sudo -n sh -c")
		if !last && !strings.Contains(line, "</dev/null") && !strings.Contains(line, "| LC_ALL=C sudo -S") {
			t.Errorf("this sudo could read the payload: %s", line)
		}
	}
	if sudos != 3 {
		t.Errorf("%d sudo calls in the wrapper, want 3 (-S -v, -n -v, the script): there is no probe before them\n%s", sudos, rootWrapper)
	}
	if strings.Contains(rootWrapper, "sudo -n true") {
		t.Error("the wrapper probes with `sudo -n true`")
	}
	// PAM's helper is asked before sudo, and a 7 from it ends the wrapper
	helper, sudo := strings.Index(rootWrapper, `"$c" "$u" nullok`), strings.Index(rootWrapper, "sudo -S")
	if helper < 0 || sudo < helper || !strings.Contains(rootWrapper[helper:sudo], `[ "$k" != 7 ] || { echo "`+rootMark+`rejected" >&2; exit 92; }`) {
		t.Errorf("the wrapper does not ask PAM's helper before sudo:\n%s", rootWrapper)
	}
	for _, p := range []string{"/usr/sbin/unix_chkpwd", "/sbin/unix_chkpwd", "</etc/passwd"} {
		if !strings.Contains(rootWrapper, p) {
			t.Errorf("the wrapper lacks %s", p)
		}
	}
	if strings.Contains(rootWrapper, "'") || strings.Contains(rootStub, "'") {
		t.Error("the wrapper has no single quote, so that its quoting stays one pair")
	}
	if !strings.HasPrefix(rootStub, `echo "`+rootMark+`run" >&2; `) || strings.Contains(rootStub, "\n") {
		t.Errorf("the root shell must say that it runs before anything else:\n%s", rootStub)
	}
	cmdline := rootCommand("k3s kubectl get pods -A", rootTry)
	if !strings.HasPrefix(cmdline, "sh -c '") || !strings.Contains(cmdline, "' tnctl-root 'k3s kubectl get pods -A' '") || !strings.HasSuffix(cmdline, "' try") {
		t.Errorf("command line: %s", cmdline)
	}
	if !strings.HasSuffix(rootCommand("id", rootWithPassword), "' password") {
		t.Error("the mode is the last word of the command line")
	}
}

func TestSudoSaid(t *testing.T) {
	for _, c := range []struct {
		stderr string
		word   sudoWord
		line   string
	}{
		{"", sudoSilent, ""},
		{"bash: warning: setlocale: LC_ALL: cannot change locale\n", sudoSilent, ""},
		{"sudo: a password is required\n", sudoAsks, ""},
		{"sudo: interactive authentication is required\n", sudoAsks, ""}, // sudo-rs
		{"sudo: unable to resolve host dev-1: Name or service not known\nsudo: a password is required\n", sudoAsks, "sudo: unable to resolve host dev-1: Name or service not known"},
		{"sudo: sorry, you must have a tty to run sudo\n", sudoNoTTY, ""},
		{"tn is not in the sudoers file.\n", sudoNotInSudoers, ""},
		{"Sorry, user tn may not run sudo on dev-1.\n", sudoNotInSudoers, ""},
		{"Sorry, user tn is not allowed to execute '/usr/bin/sh -c id' as root on dev-1.\n", sudoNotAllowed, ""},
		{"sudo: I'm sorry tn. I'm afraid I can't do that\n", sudoNotAllowed, ""}, // sudo-rs
		{"sudo: account validation failure, is your account locked?\n", sudoAccount, ""},
		{"Sorry, try again.\n\nsudo: no password was provided\nsudo: 1 incorrect password attempt\n", sudoOther, "Sorry, try again."},
		{"sudo: unable to resolve host dev-1\n", sudoOther, "sudo: unable to resolve host dev-1"},
		{"sudo: " + strings.Repeat("x", 500) + "\n", sudoOther, "sudo: " + strings.Repeat("x", maxSudoLine-6) + "..."},
	} {
		if word, line := sudoSaid(c.stderr); word != c.word || line != c.line {
			t.Errorf("%q: got %d %q, want %d %q", c.stderr, word, line, c.word, c.line)
		}
	}
}

func TestRootVerdict(t *testing.T) {
	const wrong = "Sorry, try again.\n\nsudo: no password was provided\nsudo: 1 incorrect password attempt\n"
	for _, c := range []struct {
		name string
		a    rootAttempt
		mode string
		want rootReason // "" = not a root failure
		said sudoWord
		asks bool
		frag string
	}{
		{"sudo asks, nothing was sent yet", rootAttempt{code: 1, pre: "sudo: a password is required\n"}, rootTry, "", 0, true, ""},
		{"sudo-rs asks", rootAttempt{code: 1, pre: "sudo: interactive authentication is required\n"}, rootTry, "", 0, true, ""},
		{"requiretty", rootAttempt{code: 1, pre: "sudo: sorry, you must have a tty to run sudo\n"}, rootTry, rootRefused, sudoNoTTY, false, "only works on a terminal"},
		{"sudo-rs: not allowed", rootAttempt{code: 1, pre: "sudo: I'm sorry tn. I'm afraid I can't do that\n"}, rootTry, rootRefused, sudoNotAllowed, false, "do not let the device login user run sh as root"},
		{"a sentence tnctl does not know", rootAttempt{code: 1, pre: "sudo: unable to open /etc/sudoers: Permission denied\n"}, rootTry, rootRefused, sudoOther, false, "did not run the command: sudo: unable to open /etc/sudoers"},
		{"not sudo's: ssh", rootAttempt{code: 255, pre: "ssh: connect to host dev-1: Connection refused\n"}, rootTry, "", 0, false, ""},
		{"not sudo's: nothing said", rootAttempt{code: 127}, rootTry, "", 0, false, ""},
		{"exit 0 is no refusal", rootAttempt{pre: "sudo: a password is required\n"}, rootTry, "", 0, false, ""},
		{"the script ran: its output is its own", rootAttempt{code: 1, ran: true, pre: "sudo: a password is required\n"}, rootTry, "", 0, false, ""},
		{"the script ran: its exit status is its own", rootAttempt{code: rootExitRejected, ran: true, token: "rejected"}, rootWithPassword, "", 0, false, ""},

		{"no sudo", rootAttempt{code: rootExitNoSudo, token: "no-sudo"}, rootTry, rootNoSudo, 0, false, "sudo is not installed"},
		{"no printf", rootAttempt{code: rootExitNoSudo, token: "no-printf"}, rootWithPassword, rootNoPrintf, 0, false, "built-in printf"},
		{"no password", rootAttempt{code: rootExitNoPassword, token: "no-password"}, rootWithPassword, rootNoPassword, 0, false, "the platform has none"},
		{"PAM's helper said no", rootAttempt{code: rootExitRejected, token: "rejected"}, rootWithPassword, rootRejected, 0, false, "did not accept the platform's password"},
		{"sudo said no", rootAttempt{code: rootExitRejected, token: "rejected", pre: wrong}, rootWithPassword, rootRejected, 0, false, "did not accept the platform's password"},
		{"the helper said yes, sudo no", rootAttempt{code: rootExitLocked, token: "locked", pre: wrong}, rootWithPassword, rootLocked, 0, false, "may be locked"},
		{"not in the sudoers, no helper", rootAttempt{code: rootExitRejected, token: "rejected", pre: "Sorry, user tn may not run sudo on dev-1.\n"}, rootWithPassword, rootRefused, sudoNotInSudoers, false, "may not use sudo on dev-1"},
		{"not in the sudoers, the helper said yes", rootAttempt{code: rootExitLocked, token: "locked", pre: "Sorry, user tn may not run sudo on dev-1.\n"}, rootWithPassword, rootRefused, sudoNotInSudoers, false, "may not use sudo on dev-1"},
		{"an expired login, no helper", rootAttempt{code: rootExitRejected, token: "rejected", pre: "sudo: account validation failure, is your account locked?\n"}, rootWithPassword, rootLocked, 0, false, "may be locked"},
		{"no ticket", rootAttempt{code: rootExitNoTicket, token: "no-ticket"}, rootWithPassword, rootNoTicket, 0, false, "asks for it again"},
		{"sudoers do not allow sh", rootAttempt{code: 1, pre: "Sorry, user tn is not allowed to execute '/usr/bin/sh -c ...' as root on dev-1.\n"}, rootWithPassword, rootRefused, sudoNotAllowed, false, "do not let the device login user run sh as root"},
		{"-v was content, the command still needs a password", rootAttempt{code: 1, pre: "sudo: a password is required\n"}, rootWithPassword, rootRefused, sudoAsks, false, "still asks for a password for the command"},
		// a marker with another exit status, or one that is not the wrapper's, is no reason
		{"marker and status disagree", rootAttempt{code: 1, token: "rejected"}, rootWithPassword, "", 0, false, ""},
		{"an unknown marker", rootAttempt{code: rootExitRejected, token: "something"}, rootWithPassword, "", 0, false, ""},
	} {
		re, asks := rootVerdict("dev-1", c.a, c.mode)
		if asks != c.asks {
			t.Errorf("%s: asks %v, want %v", c.name, asks, c.asks)
		}
		if c.want == "" {
			if re != nil {
				t.Errorf("%s: %v, want no root failure", c.name, re)
			}
			continue
		}
		if re == nil || re.reason != c.want || re.said != c.said || !strings.Contains(re.Error(), c.frag) || !strings.Contains(re.Error(), "dev-1") {
			t.Errorf("%s: %v (%+v), want %s with %q", c.name, re, re, c.want, c.frag)
		}
	}
}

func TestRootStderr(t *testing.T) {
	run := rootMark + "run\n"
	for _, c := range []struct {
		in, out, pre, token string
		ran                 bool
	}{
		{"", "", "", "", false},
		// before the script starts everything is held back, and the markers are taken out
		{"sudo: a password is required\n", "", "sudo: a password is required\n", "", false},
		{rootMark + "rejected\n", "", "", "rejected", false},
		{"noise\n" + rootMark + "no-password\nmore\n", "", "noise\nmore\n", "no-password", false},
		{"no line end", "", "no line end", "", false},
		{"mid line " + rootMark + "rejected\n", "", "mid line " + rootMark + "rejected\n", "", false},
		// what `sudo -v` printed is dropped when it accepted, and kept when it did not
		{"rc noise\n" + rootMark + "ask\n\nsudo: lecture\n" + rootMark + "auth\n" + run + "out\n", "rc noise\nout\n", "", "", true},
		{"rc noise\n" + rootMark + "ask\nSorry, try again.\n" + rootMark + "rejected\n", "", "rc noise\nSorry, try again.\n", "rejected", false},
		{rootMark + "auth\n" + "x\n", "", "x\n", "", false},
		// from the run line on every byte is the script's, markers included, and nothing waits
		{run, "", "", "", true},
		{"warn\n" + run + "50%", "warn\n50%", "", "", true},
		{run + rootMark + "rejected\n" + rootMark + "run\nsudo: a password is required\n", rootMark + "rejected\n" + rootMark + "run\nsudo: a password is required\n", "", "", true},
		{rootMark + "no-ticket\n" + run + "x", "x", "", "no-ticket", true},
	} {
		// whole, and in pieces: a marker may arrive split
		for _, step := range []int{len(c.in) + 1, 1, 3} {
			var b bytes.Buffer
			runs := 0
			f := &rootStderr{w: &b, onRun: func() { runs++ }}
			for i := 0; i < len(c.in); i += step {
				_, _ = f.Write([]byte(c.in[i:min(i+step, len(c.in))]))
			}
			pre := f.finish()
			if b.String() != c.out || pre != c.pre || f.token != c.token || f.ran != c.ran || runs != map[bool]int{true: 1}[c.ran] {
				t.Errorf("%q in steps of %d: got out %q pre %q token %q ran %v (%d), want %q %q %q %v", c.in, step, b.String(), pre, f.token, f.ran, runs, c.out, c.pre, c.token, c.ran)
			}
		}
	}
	// output of the script without a line end is passed on at once
	var b bytes.Buffer
	f := &rootStderr{w: &b}
	_, _ = f.Write([]byte(run + "50%"))
	if b.String() != "50%" {
		t.Errorf("held back: %q", b.String())
	}
	// more than the wrapper could ever say before the script starts is not kept in memory
	b.Reset()
	f = &rootStderr{w: &b}
	big := strings.Repeat(strings.Repeat("x", 99)+"\n", 2*maxRootPre/100)
	_, _ = f.Write([]byte(big + "sudo: a password is required\n"))
	if pre := f.finish(); len(pre) > maxRootPre || !strings.HasSuffix(pre, "sudo: a password is required\n") || b.String()+pre != big+"sudo: a password is required\n" {
		t.Errorf("held %d bytes, passed %d", len(pre), b.Len())
	}
}

// ---------- wgExec against a stand-in for ssh and sudo ----------

// standInSSH is ssh for these tests: it records its arguments and runs the remote command line
// the way sshd does, with the login shell.
const standInSSH = `#!/bin/sh
printf '%s\n' "$@" >> "$FAKE_DIR/ssh-argv"
for a in "$@"; do last=$a; done
echo "${last##* }" >> "$FAKE_DIR/ssh-runs"
exec "$FAKE_LOGIN_SHELL" -c "$last"
`

// standInSudo behaves like sudo without a terminal as far as the wrapper can tell: -n never asks,
// -S reads the password from stdin (and, like sudo, takes every further line as another attempt),
// -v only checks the password, and a ticket is kept for the parent process. The mode file says
// what its sudoers are:
//
//	nopasswd    NOPASSWD: ALL
//	passwd      the password, then a ticket
//	noticket    the password, and no ticket (timestamp_timeout=0)
//	locked      the login is locked: the right password is refused like a wrong one
//	requiretty  Defaults requiretty
//	notsudoer   the login is not in the sudoers (sudo asks for the password before it says so)
//	restricted  a rule with the password for other commands than sh
//	single      NOPASSWD rules for other commands than sh: -v needs no password and keeps no ticket
//
// With a file "loginput" it reads its stdin for the log when it runs a command (log_input), with
// a file "warn" it warns before it does anything (a device that cannot resolve its own name).
// Every call that goes to PAM without a password is in "refused-n": sudo 1.9.9 lets pam_faillock
// count those. Every call that is not under LC_ALL=C is in "sudo-locale".
const standInSudo = `#!/bin/sh
d=$FAKE_DIR
printf '%s\n' "$*" >> "$d/sudo-argv"
[ "${LC_ALL-}" = C ] || printf '%s\n' "$*" >> "$d/sudo-locale"
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
[ -e "$d/warn" ] && echo "sudo: unable to resolve host dev-1: Name or service not known" >&2
asks() { echo x >> "$d/refused-n"; echo "sudo: a password is required" >&2; exit 1; }
if [ "$mode" = requiretty ]; then echo "sudo: sorry, you must have a tty to run sudo" >&2; exit 1; fi
ticket="$d/ticket.$PPID"
open=0
[ "$mode" = nopasswd ] && open=1
[ -e "$ticket" ] && open=1
if [ $v = 1 ]; then
  [ "$mode" = single ] && exit 0
  [ $open = 1 ] && exit 0
  if [ $S = 0 ] || [ $n = 1 ]; then asks; fi
  IFS= read -r pw || { echo "sudo: no password was provided" >&2; exit 1; }
  echo x >> "$d/attempts"
  if [ "$pw" = "$(cat "$d/password")" ] && [ "$mode" != locked ]; then
    if [ "$mode" = notsudoer ]; then echo "Sorry, user tn may not run sudo on dev-1." >&2; exit 1; fi
    [ "$mode" = noticket ] || : > "$ticket"
    echo >&2; echo "sudo: what sudo says while it takes the password" >&2
    exit 0
  fi
  echo "Sorry, try again." >&2
  while IFS= read -r pw; do echo x >> "$d/attempts"; done
  echo "sudo: no password was provided" >&2
  echo "sudo: 1 incorrect password attempt" >&2
  exit 1
fi
if [ $open = 0 ]; then asks; fi
if [ "$mode" = restricted ]; then echo "Sorry, user tn is not allowed to execute '$*' as root on dev-1." >&2; exit 1; fi
if [ $S = 1 ]; then echo "fake sudo: -S with a command is the mistake this design avoids" >&2; exit 97; fi
if [ -e "$d/loginput" ]; then
  # like sudo: it passes on what it reads while the command runs, and stops when the command ends
  f="$d/in.$$"; mkfifo "$f"
  exec 3<&0
  cat <&3 > "$f" &
  c=$!
  "$@" < "$f" 3<&-; rc=$?
  kill "$c" 2>/dev/null; rm -f "$f"
  exit $rc
fi
exec "$@"
`

// standInHelper is PAM's unix_chkpwd: the password and a NUL on stdin, 0 when it is the user's
// and 7 when it is not. A file "helper-rc" makes it answer something else (9: it cannot tell).
const standInHelper = `#!/bin/sh
d=$FAKE_DIR
printf '%s\n' "$*" >> "$d/helper-runs"
in=$(od -An -c | tr -d ' \n')
pw=$(od -An -c < "$d/password" | tr -d ' \n')
[ -e "$d/helper-rc" ] && exit "$(cat "$d/helper-rc")"
[ "$in" = "$pw\\0" ] && exit 0
exit 7
`

// hostPATH is the PATH of the test process before a fake device replaced it.
var hostPATH = os.Getenv("PATH")

// lookHost finds a tool of the host, also for the second fake device of a test.
func lookHost(name string) (string, error) {
	for _, dir := range filepath.SplitList(hostPATH) {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// fakeDevice is a directory that plays a device: a PATH with only the stand-ins and the few
// tools the scripts use, and the state of its sudo.
type fakeDevice struct {
	t   *testing.T
	dir string
	bin string
}

// newFakeDevice sets up the stand-ins. shAs is the program that `sh` is on the device (dash on
// Debian, bash elsewhere), loginShell the login shell of the device user; both are looked up on
// the host and the test is skipped when one is missing. The wrapper is pointed at the stand-in
// for PAM's helper and at a passwd file of the device's own: it must never ask the helper of the
// machine the tests run on.
func newFakeDevice(t *testing.T, shAs, loginShell, mode, devicePassword string) *fakeDevice {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	sbin := filepath.Join(dir, "sbin")
	for _, p := range []string{bin, sbin} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(name, as string) {
		p, err := lookHost(name)
		if err != nil {
			t.Skipf("no %s on this host", name)
		}
		if err := os.Symlink(p, filepath.Join(bin, as)); err != nil {
			t.Fatal(err)
		}
	}
	link(shAs, "sh")
	link(loginShell, "login-shell")
	for _, tool := range []string{"cat", "true", "id", "od", "tr", "mkfifo", "rm"} {
		link(tool, tool)
	}
	for path, text := range map[string]string{
		filepath.Join(bin, "ssh"): standInSSH, filepath.Join(bin, "sudo"): standInSudo, filepath.Join(sbin, "unix_chkpwd"): standInHelper,
	} {
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDevice{t: t, dir: dir, bin: bin}
	d.write("mode", mode)
	d.write("password", devicePassword)
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	d.write("passwd", "root:x:0:0:root:/root:/bin/sh\n"+me.Username+":x:1000:1000::/home/tn:/bin/sh\n")
	// the first helper path does not exist, as /usr/sbin on a device that has it in /sbin
	oldHelpers, oldPasswd, oldWrapper := rootPAMHelpers, rootPasswdFile, rootWrapper
	rootPAMHelpers, rootPasswdFile = []string{filepath.Join(dir, "usr-sbin", "unix_chkpwd"), filepath.Join(sbin, "unix_chkpwd")}, filepath.Join(dir, "passwd")
	rootWrapper = rootWrapperFor(rootPAMHelpers, rootPasswdFile)
	t.Cleanup(func() { rootPAMHelpers, rootPasswdFile, rootWrapper = oldHelpers, oldPasswd, oldWrapper })
	t.Setenv("PATH", bin)
	t.Setenv("FAKE_DIR", dir)
	t.Setenv("FAKE_LOGIN_SHELL", filepath.Join(bin, "login-shell"))
	t.Setenv("LC_ALL", "")
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

// noHelper takes PAM's helper away from the login user, as on a device without it.
func (d *fakeDevice) noHelper() {
	d.t.Helper()
	if err := os.Remove(filepath.Join(d.dir, "sbin", "unix_chkpwd")); err != nil {
		d.t.Fatal(err)
	}
}

// lines is how many lines the record has: ssh runs, password attempts.
func (d *fakeDevice) lines(name string) int { return strings.Count(d.read(name), "\n") }

// modes are the modes the wrapper ran in, one per ssh run.
func (d *fakeDevice) modes() string { return strings.Join(strings.Fields(d.read("ssh-runs")), " ") }

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

// The mode table: what a session does for each state of sudo, each password the platform has,
// and a device with and without PAM's helper.
func TestRootModes(t *testing.T) {
	const right = `Corr3ct 'horse' "battery" $HOME \n -n %s`
	const wrong = "0ld-pa55word" // not a word of a test name: those are in the paths of the debug line
	const (
		helper   = "helper"    // the device has unix_chkpwd, and the login is in /etc/passwd
		noHelper = "no helper" // it has none
		notLocal = "not local" // it has one, but the login is not in /etc/passwd (LDAP, ...)
		cannot   = "cannot"    // it has one that cannot tell (no access to the shadow file)
	)
	cases := []struct {
		name     string
		mode     string
		platform string
		helper   string
		want     rootReason // "" = the script runs
		said     sudoWord
		unusable bool
		modes    string // the ssh runs: what was sent to the device
		helped   int    // times PAM's helper was asked
		attempts int    // passwords sudo was given to check
	}{
		// sudo needs no password: the device never gets one, whatever the platform has
		{"passwordless sudo, the right password", "nopasswd", right, helper, "", 0, false, "try", 0, 0},
		{"passwordless sudo, a wrong password", "nopasswd", wrong, helper, "", 0, false, "try", 0, 0},
		{"passwordless sudo, no password", "nopasswd", "", helper, "", 0, false, "try", 0, 0},
		{"passwordless sudo, a password that cannot be sent", "nopasswd", "a\nb", helper, "", 0, false, "try", 0, 0},
		// sudo asks
		{"password sudo, the right password", "passwd", right, helper, "", 0, false, "try password", 1, 1},
		{"password sudo, the right password, no helper", "passwd", right, noHelper, "", 0, false, "try password", 0, 1},
		{"password sudo, the right password, a helper that cannot tell", "passwd", right, cannot, "", 0, false, "try password", 1, 1},
		{"password sudo, no password", "passwd", "", helper, rootNoPassword, 0, false, "try", 0, 0},
		{"password sudo, a password that cannot be sent", "passwd", right + "\n", helper, rootNoPassword, 0, true, "try", 0, 0},
		{"password sudo, a password with a NUL", "passwd", "a\x00b", helper, rootNoPassword, 0, true, "try", 0, 0},
		// a wrong password: PAM's helper says so and sudo is never asked; without it sudo decides
		{"password sudo, a wrong password", "passwd", wrong, helper, rootRejected, 0, false, "try password", 1, 0},
		{"password sudo, a wrong password, no helper", "passwd", wrong, noHelper, rootRejected, 0, false, "try password", 0, 1},
		{"password sudo, a wrong password, a login that is not local", "passwd", wrong, notLocal, rootRejected, 0, false, "try password", 0, 1},
		{"password sudo, a wrong password, a helper that cannot tell", "passwd", wrong, cannot, rootRejected, 0, false, "try password", 1, 1},
		{"password sudo that keeps no ticket", "noticket", right, helper, rootNoTicket, 0, false, "try password", 1, 1},
		// sudo refuses for a reason of its own: that reason, not "the password was not accepted"
		{"a locked login", "locked", right, helper, rootLocked, 0, false, "try password", 1, 1},
		{"a locked login, no helper: it cannot be told from a wrong password", "locked", right, noHelper, rootRejected, 0, false, "try password", 0, 1},
		{"requiretty", "requiretty", right, helper, rootRefused, sudoNoTTY, false, "try", 0, 0},
		{"not in the sudoers", "notsudoer", right, helper, rootRefused, sudoNotInSudoers, false, "try password", 1, 1},
		{"not in the sudoers, no helper", "notsudoer", right, noHelper, rootRefused, sudoNotInSudoers, false, "try password", 0, 1},
		{"not in the sudoers, a wrong password", "notsudoer", wrong, helper, rootRejected, 0, false, "try password", 1, 0},
		{"sudoers that do not allow sh", "restricted", right, helper, rootRefused, sudoNotAllowed, false, "try password", 1, 1},
		{"NOPASSWD for single commands only", "single", right, helper, rootRefused, sudoAsks, false, "try password", 1, 0},
	}
	for _, sh := range shells {
		for _, c := range cases {
			for _, flags := range []string{"", "loginput warn"} {
				t.Run(sh[0]+"+"+sh[1]+"/"+c.name+"/"+flags, func(t *testing.T) {
					d := newFakeDevice(t, sh[0], sh[1], c.mode, right)
					for _, flag := range strings.Fields(flags) {
						d.write(flag, "")
					}
					switch c.helper {
					case noHelper:
						d.noHelper()
					case notLocal:
						d.write("passwd", "root:x:0:0:root:/root:/bin/sh\n")
					case cannot:
						d.write("helper-rc", "9")
					}
					ex := d.shell(c.platform)
					config.Debug = true
					defer func() { config.Debug = false }()

					var out, errb bytes.Buffer
					var code int
					var err error
					debug := captureStderr(t, func() {
						// the manifest case: a payload on stdin, which must reach the script whole
						code, err = ex.StreamRoot(context.Background(), "cat; echo; echo 'it'\\''s root' >&2; exit 7", strings.NewReader(manifest), &out, &errb)
					})
					if c.want == "" {
						wantErr := "it's root\n"
						if strings.Contains(flags, "warn") {
							// sudo's warning is shown once, by the sudo that ran the script
							wantErr = "sudo: unable to resolve host dev-1: Name or service not known\n" + wantErr
						}
						if err != nil || code != 7 || out.String() != manifest+"\n" || errb.String() != wantErr {
							t.Errorf("code %d err %v\nstdout %q\nstderr %q", code, err, out.String(), errb.String())
						}
					} else {
						var re *rootError
						if !errors.As(err, &re) || re.reason != c.want || re.said != c.said || re.unusable != c.unusable {
							t.Errorf("code %d err %v, want %s (said %d, unusable %v)", code, err, c.want, c.said, c.unusable)
						}
						if want, ok := rootExitOf[c.want]; ok && code != want {
							t.Errorf("code %d, want %d", code, want)
						}
						if out.Len() != 0 || errb.Len() != 0 {
							t.Errorf("the script must not run, and what sudo and the wrapper said is in the error only:\nstdout %q\nstderr %q", out.String(), errb.String())
						}
						if ex.rootErr != re {
							t.Errorf("the session did not keep the failure: %v", ex.rootErr)
						}
					}
					if got := d.modes(); got != c.modes {
						t.Errorf("ssh runs %q, want %q", got, c.modes)
					}
					if got := d.lines("helper-runs"); got != c.helped {
						t.Errorf("PAM's helper was asked %d times, want %d", got, c.helped)
					}
					if got := d.lines("attempts"); got != c.attempts {
						t.Errorf("sudo checked %d passwords, want %d", got, c.attempts)
					}
					if miss := d.read("sudo-locale"); miss != "" {
						t.Errorf("sudo ran without LC_ALL=C:\n%s", miss)
					}
					if !strings.Contains(debug, "DEBUG: Executing command: ssh") {
						t.Errorf("no debug line: %q", debug)
					}
					// The password is on stdin only: not in ssh's arguments, sudo's or the
					// helper's arguments, the debug line or the output. (A password with a line
					// break is checked by its parts.)
					for _, secret := range strings.FieldsFunc(c.platform, func(r rune) bool { return r == '\n' || r == 0 }) {
						if len(secret) < 4 {
							continue
						}
						for where, text := range map[string]string{"ssh argv": d.read("ssh-argv"), "sudo argv": d.read("sudo-argv"), "helper argv": d.read("helper-runs"),
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
}

// RunRoot captures: the output and exit status are the script's, the password line is not in
// them, and a script that reads stdin finds it empty. sudo is asked without a password once per
// session: after it said that it needs one, every command carries it.
func TestRootRun(t *testing.T) {
	for mode, want := range map[string]struct {
		modes    string
		attempts int
	}{"nopasswd": {"try try try", 0}, "passwd": {"try password password password", 3}} {
		d := newFakeDevice(t, "sh", "sh", mode, "pw-12345")
		ex := d.shell("pw-12345")
		res, err := ex.RunRoot(context.Background(), `echo "stdin=[$(cat)]"; echo warn >&2; exit 3`)
		if err != nil || res.Code != 3 || res.Stdout != "stdin=[]\n" || res.Stderr != "warn\n" {
			t.Errorf("%s: %+v %v", mode, res, err)
		}
		// more calls from the same session: nothing of the first one is left over
		for i := 0; i < 2; i++ {
			res, err = ex.RunRoot(context.Background(), "echo again")
			if err != nil || res.Code != 0 || res.Stdout != "again\n" || res.Stderr != "" {
				t.Errorf("%s: call %d: %+v %v", mode, i, res, err)
			}
		}
		if got := d.modes(); got != want.modes {
			t.Errorf("%s: ssh runs %q, want %q", mode, got, want.modes)
		}
		if got := d.lines("attempts"); got != want.attempts {
			t.Errorf("%s: %d password attempts, want %d", mode, got, want.attempts)
		}
		// sudo 1.9.9 lets pam_faillock count every `sudo -n` that needs a password: one per session
		if got := d.lines("refused-n"); got != want.attempts/3 {
			t.Errorf("%s: sudo was asked %d times without a password, want %d", mode, got, want.attempts/3)
		}
		ex.Close()
		if _, err := os.Stat(ex.sess.tmpDir); !os.IsNotExist(err) {
			t.Errorf("%s: the session directory is left behind", mode)
		}
	}
}

// A password the device does not accept is final for the session: it is sent in one command,
// whatever is called afterwards, and the device is not contacted again. With PAM's helper sudo
// never sees it; without, sudo sees it once.
func TestRootRejectedPasswordIsSentOnce(t *testing.T) {
	for _, helper := range []bool{true, false} {
		d := newFakeDevice(t, "sh", "sh", "passwd", "the-real-one")
		if !helper {
			d.noHelper()
		}
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
		if got, want := d.lines("attempts"), map[bool]int{true: 0, false: 1}[helper]; got != want {
			t.Errorf("helper %v: sudo checked %d passwords, want %d", helper, got, want)
		}
		if got, want := d.lines("helper-runs"), map[bool]int{true: 1, false: 0}[helper]; got != want {
			t.Errorf("helper %v: PAM's helper was asked %d times, want %d", helper, got, want)
		}
		if got := d.modes(); got != "try password" {
			t.Errorf("helper %v: ssh runs %q: nothing is sent after the refusal", helper, got)
		}
		// a command as the login user still works
		if res, err := ex.Run(context.Background(), "echo plain"); err != nil || res.Stdout != "plain\n" {
			t.Errorf("plain: %+v %v", res, err)
		}
	}
}

// Root commands of one session that start side by side still ask sudo one at a time: a password
// the device does not accept is in one command, not in each of them.
func TestRootWrongPasswordIsInOneCommandOfMany(t *testing.T) {
	d := newFakeDevice(t, "sh", "sh", "passwd", "the-real-one")
	d.noHelper()
	ex := d.shell("an-old-one")
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() {
			_, err := ex.RunRoot(context.Background(), "echo never")
			errs <- err
		}()
	}
	for i := 0; i < 6; i++ {
		var re *rootError
		if err := <-errs; !errors.As(err, &re) || re.reason != rootRejected {
			t.Errorf("got %v", err)
		}
	}
	if got := d.lines("attempts"); got != 1 {
		t.Errorf("sudo checked %d passwords, want 1", got)
	}
	if got := d.modes(); got != "try password" {
		t.Errorf("ssh runs %q", got)
	}
}

// Every reason for which sudo does not run the command is final for the session, not only a
// rejected password: a poll loop must not ask sudo again and again.
func TestRootRefusalIsFinal(t *testing.T) {
	for mode, want := range map[string]string{"requiretty": "try", "restricted": "try password", "single": "try password", "notsudoer": "try password", "locked": "try password", "noticket": "try password"} {
		d := newFakeDevice(t, "sh", "sh", mode, "pw-12345")
		ex := d.shell("pw-12345")
		var first error
		for i := 0; i < 4; i++ {
			res, err := ex.RunRoot(context.Background(), "echo never")
			if !isRootError(err) || res.Stdout != "" || res.Stderr != "" || (first != nil && err != first) {
				t.Errorf("%s call %d: %+v %v", mode, i, res, err)
			}
			first = err
		}
		if got := d.modes(); got != want {
			t.Errorf("%s: ssh runs %q, want %q", mode, got, want)
		}
	}
}

// Without the helper a wrong password goes to sudo, and then the lines of the payload are not
// taken as further attempts: sudo's password reader sees one line and then the end of its input.
func TestRootWrongPasswordDoesNotGuessWithThePayload(t *testing.T) {
	for _, sh := range shells {
		t.Run(sh[0]+"+"+sh[1], func(t *testing.T) {
			d := newFakeDevice(t, sh[0], sh[1], "passwd", "the-real-one")
			d.noHelper()
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

// What a script that ran prints and exits with is its own: one of the wrapper's exit statuses,
// sudo's very sentence, the wrapper's markers. It is never taken for sudo's refusal, so the
// script is not run a second time with the password, and the session stays usable.
func TestRootOutputOfTheScriptIsItsOwn(t *testing.T) {
	for _, mode := range []string{"nopasswd", "passwd"} {
		d := newFakeDevice(t, "sh", "sh", mode, "pw-12345")
		ex := d.shell("pw-12345")
		for _, code := range []int{rootExitNoPassword, rootExitRejected, rootExitNoTicket, rootExitNoSudo, rootExitLocked} {
			res, err := ex.RunRoot(context.Background(), fmt.Sprintf("echo mine >&2; exit %d", code))
			if err != nil || res.Code != code || res.Stderr != "mine\n" {
				t.Errorf("%s run %d: %+v %v", mode, code, res, err)
			}
			var errb bytes.Buffer
			got, err := ex.StreamRoot(context.Background(), fmt.Sprintf("echo mine >&2; exit %d", code), nil, io.Discard, &errb)
			if err != nil || got != code || errb.String() != "mine\n" {
				t.Errorf("%s stream %d: %d %v %q", mode, code, got, err, errb.String())
			}
		}
		ran := filepath.Join(d.dir, "ran")
		script := "echo x >> " + shq(ran) + `; echo "sudo: a password is required" >&2; echo "` + rootMark + `rejected" >&2; echo "Sorry, user tn is not allowed to execute this" >&2; exit `
		for _, code := range []int{1, rootExitRejected} {
			want := "sudo: a password is required\n" + rootMark + "rejected\nSorry, user tn is not allowed to execute this\n"
			res, err := ex.RunRoot(context.Background(), script+fmt.Sprint(code))
			if err != nil || res.Code != code || res.Stderr != want {
				t.Errorf("%s run: %+v %v", mode, res, err)
			}
			var errb bytes.Buffer
			got, err := ex.StreamRoot(context.Background(), script+fmt.Sprint(code), strings.NewReader(manifest), io.Discard, &errb)
			if err != nil || got != code || errb.String() != want {
				t.Errorf("%s stream: %d %v %q", mode, got, err, errb.String())
			}
			// rootProbe: an error of the check, which a poll may see again; not sudo's refusal
			if _, err := rootProbe(context.Background(), ex, script+fmt.Sprint(code)); err == nil || isRootError(err) || !strings.Contains(err.Error(), fmt.Sprintf("exit status %d: sudo: a password is required", code)) {
				t.Errorf("%s probe: %v", mode, err)
			}
		}
		if got := strings.Count(d.read("ran"), "x"); got != 6 {
			t.Errorf("%s: the script ran %d times, want 6: once per call", mode, got)
		}
		if ex.rootErr != nil {
			t.Errorf("%s: the session gave up: %v", mode, ex.rootErr)
		}
	}
}

// The script is one argument of the wrapper: quotes, $1 and line breaks in it are the script's.
func TestRootScriptQuoting(t *testing.T) {
	d := newFakeDevice(t, "sh", "zsh", "passwd", "pw-12345")
	ex := d.shell("pw-12345")
	script := "set -- one two\nprintf '%s|' \"it's $1\" 'a \"b\" $2' \"$#\" \"$0\"\necho\n"
	res, err := ex.RunRoot(context.Background(), script)
	if err != nil || res.Stdout != "it's one|a \"b\" $2|2|sh|\n" {
		t.Errorf("%+v %v", res, err)
	}
	// and it starts without arguments of the wrapper's
	if res, err := ex.RunRoot(context.Background(), `echo "$#[$*]"`); err != nil || res.Stdout != "0[]\n" {
		t.Errorf("%+v %v", res, err)
	}
}

// sudo runs under LC_ALL=C so that tnctl can read what it says; the script gets the LC_ALL of
// the device login back.
func TestRootScriptKeepsTheLocale(t *testing.T) {
	for _, mode := range []string{"nopasswd", "passwd"} {
		for _, lc := range []string{"", "sv_SE.UTF-8", "C"} {
			d := newFakeDevice(t, "sh", "bash", mode, "pw-12345")
			t.Setenv("LC_ALL", lc)
			if lc == "" {
				_ = os.Unsetenv("LC_ALL")
			}
			res, err := d.shell("pw-12345").RunRoot(context.Background(), `echo "[${LC_ALL-unset}]"`)
			want := "[" + lc + "]\n"
			if lc == "" {
				want = "[unset]\n"
			}
			if err != nil || res.Stdout != want {
				t.Errorf("%s LC_ALL=%q: %+v %v", mode, lc, res, err)
			}
			if miss := d.read("sudo-locale"); miss != "" {
				t.Errorf("sudo ran without LC_ALL=C:\n%s", miss)
			}
		}
	}
}

// The payload is not sent before the script has started. An attempt that sudo refuses has then
// read nothing of it, so a stdin that cannot be read twice still reaches the script whole.
func TestRootPayloadWaitsForTheScript(t *testing.T) {
	d := newFakeDevice(t, "sh", "sh", "passwd", "pw-12345")
	ex := d.shell("pw-12345")
	r, w := io.Pipe()
	go func() {
		for i := 0; i < 2000; i++ {
			_, _ = fmt.Fprintf(w, "line %04d of the manifest\n", i)
		}
		_ = w.Close()
	}()
	var out bytes.Buffer
	code, err := ex.StreamRoot(context.Background(), "cat", r, &out, io.Discard)
	if err != nil || code != 0 || strings.Count(out.String(), "\n") != 2000 || !strings.HasPrefix(out.String(), "line 0000 ") || !strings.HasSuffix(out.String(), "line 1999 of the manifest\n") {
		t.Errorf("code %d err %v, %d lines", code, err, strings.Count(out.String(), "\n"))
	}
	if got := d.modes(); got != "try password" {
		t.Errorf("ssh runs %q", got)
	}
}

// tnctl exec --root on a terminal: nobody types, and the command must still end when the remote
// command does (the copy of stdin must not hold it back).
func TestRootStreamDoesNotWaitForStdin(t *testing.T) {
	for _, mode := range []string{"passwd", "nopasswd", "requiretty"} {
		d := newFakeDevice(t, "sh", "sh", mode, "pw-12345")
		ex := d.shell("pw-12345")
		silent, w := io.Pipe()
		done := make(chan error, 1)
		var out bytes.Buffer
		go func() {
			_, err := ex.StreamRoot(context.Background(), "echo done", silent, &out, io.Discard)
			done <- err
		}()
		select {
		case err := <-done:
			if mode == "requiretty" {
				if !isRootError(err) {
					t.Errorf("%s: %v", mode, err)
				}
			} else if err != nil || out.String() != "done\n" {
				t.Errorf("%s: %v %q", mode, err, out.String())
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s: StreamRoot waits for a stdin that never ends", mode)
		}
		_ = w.Close()
	}
}

// ---------- rootProbe: a sudo failure is never an empty answer ----------

func TestRootProbe(t *testing.T) {
	script := helmProbeScript("default", "notebook", "default", false)
	noTTY := &rootError{reason: rootRefused, device: "dev-1", said: sudoNoTTY}
	for _, c := range []struct {
		name    string
		res     execResult
		rootErr error
		final   bool   // a *rootError: not retried
		frag    string // "" = no error
	}{
		{"answers", execResult{Stdout: probeOutput(map[string]string{"chart": "", "job": ""})}, nil, false, ""},
		{"sudo refuses the command", execResult{}, noTTY, true, "sudo on dev-1 only works on a terminal"},
		// The script ran: what it printed is its own, whatever it looks like. Only RunRoot knows
		// whether sudo refused.
		{"the check itself prints sudo's sentence", execResult{Code: 1, Stderr: "sudo: a password is required\n"}, nil, false, "exit status 1: sudo: a password is required"},
		{"the check itself prints a Sorry", execResult{Code: 1, Stderr: "Sorry, user tn is not allowed to execute '/bin/sh -c ...' as root on dev-1.\n"}, nil, false, "exit status 1: Sorry, user tn"},
		{"the shell died", execResult{Code: 137}, nil, false, "exit status 137"},
		{"another failure", execResult{Code: 2, Stderr: "sh: 1: Syntax error\n"}, nil, false, "exit status 2: sh: 1: Syntax error"},
		{"no output at all", execResult{}, nil, false, "printed nothing"},
	} {
		ex := &fakeExec{run: func(string) (execResult, error) { return c.res, nil }, rootErr: c.rootErr}
		_, err := rootProbe(context.Background(), ex, script)
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
	if _, err := rootProbe(context.Background(), ex, "k3s kubectl get pods -A 2>&1; true"); err != nil {
		t.Errorf("no sections: %v", err)
	}
}

// config remove --wait=applied asks "is it gone?". A sudo that fails prints nothing on stdout,
// which is also what "gone" looks like: it must end the wait as an error, never as success.
func TestRemoveWaitNeverReadsASudoFailureAsGone(t *testing.T) {
	for name, c := range map[string]struct {
		res     execResult
		rootErr error
		calls   int
		frag    string
	}{
		"sudo refuses":   {execResult{}, &rootError{reason: rootRefused, device: "vision-station-01", said: sudoNotAllowed}, 1, "the sudoers on vision-station-01 do not let the device login user run sh as root"},
		"exit status":    {execResult{Code: 1}, nil, 6, "exit status 1"},
		"nothing at all": {execResult{}, nil, 6, "printed nothing"},
	} {
		p := removeAppliedPlatform(t)
		ex := &fakeExec{run: func(string) (execResult, error) { return c.res, nil }, rootErr: c.rootErr}
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
	ex := &fakeExec{rootErr: &rootError{reason: rootRefused, device: "vision-station-01", said: sudoNoTTY}}
	e, _ := testEnv(t, p, ex)
	_, errs, err := setWait(t, e, "--wait=applied")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "only works on a terminal") || !strings.Contains(err.Error(), "--wait=published") {
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
		rootLocked:     "although the platform's password for it is right",
		rootRefused:    "sudo on vision-station-01 did not run the command: ",
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
