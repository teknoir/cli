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
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/config"
)

// The TestSudoRig tests run wgExec against a real sshd and a real sudo: a throwaway container
// with one login user per sudo state (test/sudo-root, started by its run.sh). They are skipped
// unless run.sh set the TN_SUDO_RIG_* variables, so the unit suite needs no Docker. The stand-ins
// in devexec_root_test.go cover the same cases without it; these tests are the proof that real
// sudo, PAM, real shells and a real multiplexed ssh behave the way those assume.

type sudoRig struct {
	t         *testing.T
	container string
	port      string
	dir       string
	password  string
	features  map[string]bool
}

func newSudoRig(t *testing.T, needs ...string) *sudoRig {
	t.Helper()
	r := &sudoRig{t: t, container: os.Getenv("TN_SUDO_RIG_CONTAINER"), port: os.Getenv("TN_SUDO_RIG_PORT"),
		dir: os.Getenv("TN_SUDO_RIG_DIR"), password: os.Getenv("TN_SUDO_RIG_PASSWORD"), features: map[string]bool{}}
	if r.container == "" || r.port == "" || r.dir == "" || r.password == "" {
		t.Skip("needs the sudo rig: run test/sudo-root/run.sh")
	}
	for _, f := range strings.Fields(r.docker("cat", "/etc/tnctl-rig-features")) {
		r.features[f] = true
	}
	for _, f := range needs {
		if !r.features[f] {
			t.Skipf("this device has no %s", f)
		}
	}
	// A new test starts with no ticket, an empty sudo log and no failed logins.
	r.docker("sh", "-c", ": > /var/log/sudo.log; rm -rf /run/sudo/ts/* /run/sudo-rs/ts/* /var/log/sudo-io /run/faillock/* /var/run/faillock/*; rm -f /tmp/ran-*")
	return r
}

// docker runs a command in the container as root: how the tests look at what sudo recorded.
func (r *sudoRig) docker(args ...string) string {
	r.t.Helper()
	out, err := exec.Command("docker", append([]string{"exec", r.container}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("docker exec %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// sudoLog is sudo's own log: one entry per command it ran or refused, and per failed password.
// sudo-rs has none.
func (r *sudoRig) sudoLog() string { return r.docker("cat", "/var/log/sudo.log") }

// failedPasswords is how many failed password attempts sudo counted, over all entries.
func (r *sudoRig) failedPasswords() int {
	n := 0
	for _, l := range strings.Split(r.sudoLog(), "\n") {
		var k int
		if i := strings.Index(l, " incorrect password attempt"); i > 0 {
			f := strings.Fields(l[:i])
			_, _ = fmt.Sscanf(f[len(f)-1], "%d", &k)
			n += k
		}
	}
	return n
}

// failedLogins is how many failed logins pam_faillock holds against the user (3 lock the
// account); -1 on a device without pam_faillock.
func (r *sudoRig) failedLogins(user string) int {
	r.t.Helper()
	if !r.features["faillock"] {
		return -1
	}
	n := 0
	for _, l := range strings.Split(r.docker("faillock", "--user", user), "\n") {
		if strings.HasSuffix(strings.TrimRight(l, " "), " V") {
			n++
		}
	}
	return n
}

// tryCost is what one `sudo -n` that needs a password costs the login with pam_faillock: 1 with
// sudo 1.9.9 (Ubuntu 22.04), which takes even that to PAM, and 0 with every other sudo.
func (r *sudoRig) tryCost() int {
	if r.features["faillock"] && r.features["sudo-1.9.9"] {
		return 1
	}
	return 0
}

// wantFailedLogins checks pam_faillock's count for the user, where the device has it.
func (r *sudoRig) wantFailedLogins(user string, want int, why string) {
	r.t.Helper()
	if got := r.failedLogins(user); got != -1 && got != want {
		r.t.Errorf("pam_faillock holds %d failed logins against %s, want %d: %s", got, user, want, why)
	}
}

// wantInLog checks how often sudo's log has the text; nothing is checked where sudo has no log.
func (r *sudoRig) wantInLog(text string, want int) {
	r.t.Helper()
	if !r.features["sudo"] {
		return
	}
	if log := r.sudoLog(); strings.Count(log, text) != want {
		r.t.Errorf("sudo's log has %q %d times, want %d:\n%s", text, strings.Count(log, text), want, log)
	}
}

// shell is a wgExec to the container as user, with what the platform has as its password. The
// ssh arguments are those of a device session minus the WireGuard ProxyCommand.
func (r *sudoRig) shell(user, platformPassword string) *wgExec {
	r.t.Helper()
	tmp, err := os.MkdirTemp("", "tn-rig-") // short: the control socket path has a length limit
	if err != nil {
		r.t.Fatal(err)
	}
	ex := &wgExec{
		sess: &wgSSHSession{
			baseArgs: []string{
				"-F", "/dev/null",
				"-o", "UserKnownHostsFile=" + filepath.Join(r.dir, "known_hosts"),
				"-o", "StrictHostKeyChecking=yes",
				"-o", "IdentitiesOnly=yes",
				"-o", "IdentityAgent=none",
				"-i", filepath.Join(r.dir, "id"),
				"-p", r.port,
				user + "@127.0.0.1",
			},
			password: platformPassword,
			tmpDir:   tmp,
			device:   user,
		},
		controlPath: filepath.Join(tmp, "cm"),
	}
	r.t.Cleanup(ex.Close)
	return ex
}

// clean fails the test when the password shows up in any of the texts.
func (r *sudoRig) clean(what string, texts ...string) {
	r.t.Helper()
	for _, text := range texts {
		if strings.Contains(text, r.password) || strings.Contains(text, r.password[:19]) {
			r.t.Errorf("%s: the password is in the output:\n%s", what, text)
		}
	}
}

func rigCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// stream runs StreamRoot and returns everything it gave back.
func rigStream(t *testing.T, ex *wgExec, script, stdin string) (code int, stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	code, err = ex.StreamRoot(rigCtx(t), script, in, &out, &errb)
	return code, out.String(), errb.String(), err
}

var rigShells = []string{"bash", "zsh"}

// rigManifest is a payload for stdin: several lines, quotes, and no line end at the end.
const rigManifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: 'a \"b\" $c'\ndata:\n  k: |\n    line 1\n    line 2\n---\n# no line end"

// rigAsked is the entry in sudo's log of a `sudo -n` that needed a password.
const rigAsked = " : a password is required ; "

// rigRefused checks that a root call ended with this reason, without running the script.
func rigRefused(t *testing.T, what string, err error, want rootReason, said sudoWord, out, errs string) {
	t.Helper()
	var re *rootError
	if !errors.As(err, &re) || re.reason != want || re.said != said || out != "" || errs != "" {
		t.Errorf("%s: err %v, want %s (said %d)\nstdout %q\nstderr %q", what, err, want, said, out, errs)
	}
}

// The normal device: sudo asks for the login user's password.
func TestSudoRigPasswordSudo(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t)
			user := "pw-" + shell
			ex := r.shell(user, r.password)

			// The login user itself is not root and has no passwordless sudo: the state under test.
			res, err := ex.Run(rigCtx(t), "id -u; LC_ALL=C sudo -n true")
			if _, asks := rootVerdict(user, rootAttempt{code: 1, pre: res.Stderr}, rootTry); err != nil || !asks || strings.TrimSpace(res.Stdout) == "0" {
				t.Fatalf("precondition: %+v %v", res, err)
			}
			r.docker("sh", "-c", ": > /var/log/sudo.log; rm -rf /run/faillock/* /var/run/faillock/*")

			config.Debug = true
			var res2 execResult
			debug := captureStderr(t, func() { res2, err = ex.RunRoot(rigCtx(t), `id -u; echo "stdin=[$(cat)]"; echo warn >&2; exit 3`) })
			config.Debug = false
			if err != nil || res2.Stdout != "0\nstdin=[]\n" || res2.Stderr != "warn\n" || res2.Code != 3 {
				t.Errorf("run as root: %+v %v", res2, err)
			}
			r.clean("run", res2.Stdout, res2.Stderr, debug)
			if !strings.Contains(debug, "DEBUG: Executing command: ssh") {
				t.Errorf("no debug line: %q", debug)
			}

			// More calls over the same multiplexed connection: the ssh master is the parent of
			// every one of them on the device. Each gets its own answer; nothing leaks.
			if out, cerr := exec.Command("ssh", "-F", "/dev/null", "-o", "ControlPath="+ex.controlPath, "-O", "check", user+"@127.0.0.1").CombinedOutput(); cerr != nil {
				t.Errorf("no ssh master is running: %v %s", cerr, out)
			}
			for i := 0; i < 3; i++ {
				res, err := ex.RunRoot(rigCtx(t), fmt.Sprintf(`echo "call %d as $(id -un), stdin=[$(cat)]"`, i))
				if err != nil || res.Stdout != fmt.Sprintf("call %d as root, stdin=[]\n", i) || res.Stderr != "" || res.Code != 0 {
					t.Errorf("call %d: %+v %v", i, res, err)
				}
				r.clean("multiplexed call", res.Stdout, res.Stderr)
			}

			// The manifest case: the payload follows the password on the same stdin, and all of
			// it, and only it, reaches the command.
			code, out, errs, err := rigStream(t, ex, "cat", rigManifest)
			if err != nil || code != 0 || out != rigManifest || errs != "" {
				t.Errorf("manifest: code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
			}
			r.clean("manifest", out, errs)
			// and when the script reads nothing, or there is no payload
			code, out, errs, err = rigStream(t, ex, "echo quiet; exit 5", rigManifest)
			if err != nil || code != 5 || out != "quiet\n" || errs != "" {
				t.Errorf("unread payload: code %d err %v %q %q", code, err, out, errs)
			}
			code, out, errs, err = rigStream(t, ex, `echo "stdin=[$(cat)]"`, "")
			if err != nil || code != 0 || out != "stdin=[]\n" || errs != "" {
				t.Errorf("no payload: code %d err %v %q %q", code, err, out, errs)
			}

			if r.features["sudo"] {
				if n := r.failedPasswords(); n != 0 {
					t.Errorf("%d failed password attempts, want none:\n%s", n, r.sudoLog())
				}
				r.clean("sudo log", r.sudoLog())
			}
			// Seven root commands, and sudo was asked without a password once: by the first. Every
			// later one carried the password. (sudo 1.9.9 lets pam_faillock count each such
			// question, and the accepted password clears the count.)
			r.wantInLog(rigAsked, 1)
			r.wantFailedLogins(user, 0, "the password was right every time")

			ex.Close()
			if _, err := os.Stat(ex.controlPath); !os.IsNotExist(err) {
				t.Errorf("the ssh master outlives the session: %v", err)
			}
		})
	}
}

// A device whose sh is bash. bash execs the last command of a -c script: without the `exit` after
// the last sudo that sudo would be a child of sshd's shell, not of the wrapper's, and would not
// find the ticket of `sudo -v` (dash, the sh of the other logins here, does not do that).
func TestSudoRigBashAsSh(t *testing.T) {
	r := newSudoRig(t)
	ex := r.shell("bs", r.password)
	if res, err := ex.Run(rigCtx(t), `sh -c 'echo "${BASH_VERSION:+bash}"'`); err != nil || res.Stdout != "bash\n" {
		t.Fatalf("precondition: sh is not bash for this login: %+v %v", res, err)
	}
	for i := 0; i < 2; i++ {
		res, err := ex.RunRoot(rigCtx(t), `id -u; echo "stdin=[$(cat)]"; exit 3`)
		if err != nil || res.Stdout != "0\nstdin=[]\n" || res.Stderr != "" || res.Code != 3 {
			t.Errorf("call %d: %+v %v", i, res, err)
		}
		code, out, errs, err := rigStream(t, ex, "cat", rigManifest)
		if err != nil || code != 0 || out != rigManifest || errs != "" {
			t.Errorf("manifest %d: code %d err %v\nstdout %q\nstderr %q", i, code, err, out, errs)
		}
		r.clean("bash as sh", res.Stdout, out, errs)
	}
	r.wantInLog(rigAsked, 1)
	_, out, errs, err := rigStream(t, r.shell("bs", "not-the-password"), "id -u", rigManifest)
	rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
	r.wantInLog("incorrect password", 0)
}

// A login with passwordless sudo (the bootstrap's drop-in): it must work as before the password
// existed in this path, whatever password the platform has. The device never gets the password:
// sudo answers the first `sudo -n`, and nothing else is run.
func TestSudoRigPasswordlessSudo(t *testing.T) {
	for _, shell := range rigShells {
		for name, platform := range map[string]string{"the right password": "", "a wrong password": "not-the-password", "no password": "-"} {
			t.Run(shell+"/"+name, func(t *testing.T) {
				r := newSudoRig(t)
				switch platform {
				case "":
					platform = r.password
				case "-":
					platform = ""
				}
				ex := r.shell("np-"+shell, platform)
				res, err := ex.RunRoot(rigCtx(t), `id -u; echo "stdin=[$(cat)]"`)
				if err != nil || res.Stdout != "0\nstdin=[]\n" || res.Stderr != "" || res.Code != 0 {
					t.Errorf("run: %+v %v", res, err)
				}
				code, out, errs, err := rigStream(t, ex, "cat", rigManifest)
				if err != nil || code != 0 || out != rigManifest || errs != "" {
					t.Errorf("manifest: code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
				}
				r.clean("passwordless", res.Stdout, res.Stderr, out, errs, r.sudoLog())
				// sudo ran the two scripts and nothing else: no password was checked, no question asked
				r.wantInLog("COMMAND=", 2)
				r.wantInLog("COMMAND=validate", 0)
				r.wantInLog("incorrect password", 0)
				r.wantInLog(rigAsked, 0)
				r.wantFailedLogins("np-"+shell, 0, "sudo needs no password")
				if ex.rootAsks {
					t.Error("the session thinks that sudo asks for a password")
				}
			})
		}
	}
}

// The platform's password is not the device's, with a payload of several lines behind it. PAM's
// helper says so: sudo is never given the password, nothing counts as a failed login however
// often it happens, the script does not run, and the session does not ask again.
func TestSudoRigWrongPassword(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t)
			user := "pw-" + shell
			// the payload even has the right password in it, as its first line
			payload := r.password + "\nline 2\nline 3\nline 4\n"
			script := "touch /tmp/ran-" + shell + "; cat"
			// more sessions than the 3 failed logins that lock an account
			for session := 1; session <= 5; session++ {
				ex := r.shell(user, "not-the-password")
				code, out, errs, err := rigStream(t, ex, script, payload)
				rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
				if code != rootExitRejected {
					t.Errorf("code %d", code)
				}
				// the session is done with root: more calls do not reach the device
				var re *rootError
				for i := 0; i < 3; i++ {
					if _, err := ex.RunRoot(rigCtx(t), "id -u"); !errors.As(err, &re) || re.reason != rootRejected {
						t.Errorf("call %d after the refusal: %v", i, err)
					}
				}
				if _, _, _, err := rigStream(t, ex, script, payload); !isRootError(err) {
					t.Errorf("stream after the refusal: %v", err)
				}
				r.clean("wrong password", out, errs, fmt.Sprint(err))
				// as the login user the session still works
				if res, err := ex.Run(rigCtx(t), "id -un"); err != nil || res.Stdout != user+"\n" {
					t.Errorf("plain: %+v %v", res, err)
				}
				// sudo was asked once, without a password, and never given one
				r.wantInLog(rigAsked, session)
				r.wantInLog("COMMAND=validate", 0)
				r.wantInLog("incorrect password", 0)
				r.wantFailedLogins(user, r.tryCost(), "the wrong password never reached sudo")
				// (sudo 1.9.9: the one `sudo -n` of each session is counted; start the next at 0)
				if r.tryCost() != 0 {
					r.docker("faillock", "--user", user, "--reset")
				}
			}
			if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
				t.Errorf("the script ran: %s", ls)
			}
			// not locked: the right password works at once
			if res, err := r.shell(user, r.password).RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
				t.Errorf("the right password after five wrong ones: %+v %v", res, err)
			}
			r.wantFailedLogins(user, 0, "the right password clears the count")
		})
	}
}

// A device without PAM's helper: the decision is sudo's. A wrong password is then one guess for
// sudo however long the payload is, and it is the case the session's rule is for (a rejection is
// final): pam_faillock counts that one guess as 2 failed logins with sudo (it asks PAM again when
// its input has ended) and as 1 with sudo-rs. A second such command would lock the account.
func TestSudoRigWithoutThePAMHelper(t *testing.T) {
	r := newSudoRig(t)
	// Taken from the login users only (root, so sshd and sudo's PAM, still runs it): the
	// wrapper skips a helper it may not execute.
	helper := strings.TrimSpace(r.docker("sh", "-c", `readlink -f "$(command -v unix_chkpwd)"`))
	mode := strings.TrimSpace(r.docker("stat", "-c", "%a", helper))
	r.docker("chmod", "o-rwx", helper)
	t.Cleanup(func() { r.docker("chmod", mode, helper) })

	user := "fl-a"
	ex := r.shell(user, "not-the-password")
	payload := r.password + "\n" + r.password + "\nline 3\n"
	code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-nohelper; cat", payload)
	rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
	if code != rootExitRejected {
		t.Errorf("code %d", code)
	}
	for i := 0; i < 3; i++ {
		if _, err := ex.RunRoot(rigCtx(t), "id -u"); !isRootError(err) {
			t.Errorf("call %d after the refusal: %v", i, err)
		}
	}
	if r.features["sudo"] {
		if n := r.failedPasswords(); n != 1 {
			t.Errorf("%d failed password attempts, want exactly 1:\n%s", n, r.sudoLog())
		}
	}
	r.wantInLog("COMMAND=validate", 1)
	if failed := r.failedLogins(user); failed != -1 {
		guess := failed - r.tryCost()
		t.Logf("one wrong password given to sudo is %d failed logins for pam_faillock (plus %d for the `sudo -n` before it)", guess, r.tryCost())
		if want := map[bool]int{true: 1, false: 2}[r.features["sudo-rs"]]; guess != want {
			t.Errorf("one wrong password given to sudo is %d failed logins, want %d", guess, want)
		}
	}
	if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
		t.Errorf("the script ran: %s", ls)
	}
	r.clean("no helper", out, errs, fmt.Sprint(err), r.sudoLog())
	if r.tryCost() != 0 {
		// sudo 1.9.9 counted the `sudo -n` before it too: 3 failed logins, the login is locked
		// by this one command (a known limit, see the README). Start again at 0.
		r.docker("faillock", "--user", user, "--reset")
	}
	// not locked: the right password works, through sudo alone
	ex2 := r.shell(user, r.password)
	code, out, errs, err = rigStream(t, ex2, "id -u; cat", rigManifest)
	if err != nil || code != 0 || out != "0\n"+rigManifest || errs != "" {
		t.Errorf("the right password without the helper: code %d err %v %q %q", code, err, out, errs)
	}
	r.wantFailedLogins(user, 0, "the right password clears the count")
}

// pam_faillock has locked the login (someone mistyped the password in a shell). The platform's
// password is right and sudo refuses it all the same: that is not "the password was not
// accepted", and it costs the login nothing more.
func TestSudoRigLockedLogin(t *testing.T) {
	r := newSudoRig(t, "faillock")
	user := "fl-b"
	plain := r.shell(user, "")
	for i := 0; i < 4 && r.failedLogins(user) < 3; i++ {
		if _, err := plain.Stream(rigCtx(t), `LC_ALL=C sudo -S -p "" -v`, strings.NewReader("mistyped\n"), io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	locked := r.failedLogins(user)
	if locked < 3 {
		t.Fatalf("the login is not locked: %d failed logins", locked)
	}
	r.docker("sh", "-c", ": > /var/log/sudo.log")
	for session := 0; session < 2; session++ {
		ex := r.shell(user, r.password)
		code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-locked; cat", rigManifest)
		rigRefused(t, "locked login", err, rootLocked, 0, out, errs)
		if code != rootExitLocked {
			t.Errorf("code %d", code)
		}
		if _, err := ex.RunRoot(rigCtx(t), "id -u"); !isRootError(err) {
			t.Errorf("after the refusal: %v", err)
		}
		r.clean("locked", out, errs, fmt.Sprint(err))
	}
	if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
		t.Errorf("the script ran: %s", ls)
	}
	// the lock has ended: the same password works
	r.docker("faillock", "--user", user, "--reset")
	if res, err := r.shell(user, r.password).RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
		t.Errorf("after the lock: %+v %v", res, err)
	}
}

// What a `sudo -n` that needs a password costs with pam_faillock, measured: nothing, except with
// sudo 1.9.9 (Ubuntu 22.04), which takes it to PAM as a failed login. A session asks that way
// once, so a login without a password on the platform is counted once per tnctl command there.
func TestSudoRigRefusedTryAndFaillock(t *testing.T) {
	r := newSudoRig(t, "faillock")
	user := "fl-c"
	ex := r.shell(user, "")
	for i := 0; i < 4; i++ {
		res, err := ex.RunRoot(rigCtx(t), "id -u")
		var re *rootError
		if !errors.As(err, &re) || re.reason != rootNoPassword || res.Stdout != "" {
			t.Errorf("call %d: %+v %v", i, res, err)
		}
	}
	got := r.failedLogins(user)
	t.Logf("a `sudo -n` that needs a password is %d failed logins for pam_faillock here", got)
	if got != r.tryCost() {
		t.Errorf("four root calls of a session without a password are %d failed logins, want %d (one `sudo -n`)", got, r.tryCost())
	}
	r.wantInLog(rigAsked, 1)
	// with the password: the first command asks once, and sudo accepting the password clears it
	ex2 := r.shell(user, r.password)
	for i := 0; i < 4; i++ {
		if res, err := ex2.RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
			t.Errorf("call %d with the password: %+v %v", i, res, err)
		}
	}
	r.wantInLog(rigAsked, 2)
	r.wantFailedLogins(user, 0, "sudo accepted the password")
}

// sudo asks for a password and the platform has none, or one that cannot be sent as a line:
// nothing is sent at all (an empty line would count as a failed attempt).
func TestSudoRigNoPassword(t *testing.T) {
	for _, shell := range rigShells {
		for name, platform := range map[string]string{"none": "", "with a line break": "-"} {
			t.Run(shell+"/"+name, func(t *testing.T) {
				r := newSudoRig(t)
				if platform == "-" {
					platform = r.password + "\n"
				}
				ex := r.shell("pw-"+shell, platform)
				code, out, errs, err := rigStream(t, ex, "id -u", rigManifest)
				rigRefused(t, "no password", err, rootNoPassword, 0, out, errs)
				var re *rootError
				if errors.As(err, &re) && re.unusable != (platform != "") || code != rootExitNoPassword {
					t.Errorf("code %d err %v", code, err)
				}
				r.wantInLog("COMMAND=validate", 0)
				r.wantInLog("incorrect password", 0)
				r.wantFailedLogins("pw-"+shell, r.tryCost(), "sudo was given nothing to check")
			})
		}
	}
}

// timestamp_timeout=0: sudo takes the password and forgets it at once. The wrapper says so with
// its own exit status instead of failing with sudo's "a password is required".
func TestSudoRigNoTicket(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t)
			ex := r.shell("nt-"+shell, r.password)
			code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-nt; id -u", rigManifest)
			rigRefused(t, "no ticket", err, rootNoTicket, 0, out, errs)
			if code != rootExitNoTicket {
				t.Errorf("code %d, want %d", code, rootExitNoTicket)
			}
			if r.features["sudo"] {
				if n := r.failedPasswords(); n != 0 {
					t.Errorf("%d failed password attempts: the password was right:\n%s", n, r.sudoLog())
				}
			}
			if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
				t.Errorf("the script ran: %s", ls)
			}
			r.clean("no ticket", out, errs, fmt.Sprint(err))
		})
	}
}

// A ticket that is still valid when the next call comes (here: one ticket for all sessions of
// the user). The first `sudo -n` finds sudo open, so the password is not sent: even a wrong one
// does no harm.
func TestSudoRigValidTicket(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t, "sudo")
			first := r.shell("gl-"+shell, r.password)
			if res, err := first.RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
				t.Fatalf("first call: %+v %v", res, err)
			}
			second := r.shell("gl-"+shell, "not-the-password")
			code, out, errs, err := rigStream(t, second, "cat", rigManifest)
			if err != nil || code != 0 || out != rigManifest || errs != "" {
				t.Errorf("with a ticket: code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
			}
			res, err := second.RunRoot(rigCtx(t), `echo "stdin=[$(cat)]"`)
			if err != nil || res.Stdout != "stdin=[]\n" {
				t.Errorf("with a ticket: %+v %v", res, err)
			}
			if n := r.failedPasswords(); n != 0 {
				t.Errorf("%d failed password attempts, want none (sudo was open):\n%s", n, r.sudoLog())
			}
			r.clean("valid ticket", out, errs, res.Stdout, res.Stderr, r.sudoLog())
		})
	}
}

// rigLong is what a large release sends to kubectl: 2000 lines that must all arrive.
var rigLong = func() string {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "line %04d of the manifest\n", i)
	}
	return b.String()
}()

// `Defaults log_input`: sudo copies its stdin for the log, so any sudo that runs before the
// script with the wrapper's stdin takes what waits there for the script (a `sudo -n true` probe
// at that place left 0 of 2000 lines, sudo 1.9.16). No sudo in the wrapper does, in either mode.
func TestSudoRigInputLog(t *testing.T) {
	for _, user := range []string{"li-pw", "li-np"} {
		t.Run(user, func(t *testing.T) {
			r := newSudoRig(t, "sudo")
			// through a session: the first call of li-pw is refused once (`sudo -n`) before the
			// password is sent, and the payload must survive that too
			ex := r.shell(user, r.password)
			for i := 0; i < 3; i++ {
				code, out, errs, err := rigStream(t, ex, "cat", rigLong)
				if err != nil || code != 0 || out != rigLong || errs != "" {
					t.Errorf("call %d: code %d err %v, %d of 2000 lines, stderr %q", i, code, err, strings.Count(out, "\n"), errs)
				}
			}
			if want := map[string]bool{"li-pw": true, "li-np": false}[user]; ex.rootAsks != want {
				t.Errorf("the session thinks sudo asks: %v", ex.rootAsks)
			}
			// Straight at the wrapper, as one pipe: the payload already waits on stdin while the
			// wrapper runs, without tnctl holding it back until the script has started.
			mode, first := rootTry, ""
			if user == "li-pw" {
				mode, first = rootWithPassword, r.password+"\n"
			}
			for i := 0; i < 3; i++ {
				var out, errb bytes.Buffer
				code, err := ex.Stream(rigCtx(t), rootCommand("cat", mode), strings.NewReader(first+rigLong), &out, &errb)
				if err != nil || code != 0 || out.String() != rigLong {
					t.Errorf("wrapper call %d: code %d err %v, %d of 2000 lines, stderr %q", i, code, err, strings.Count(out.String(), "\n"), errb.String())
				}
			}
			// sudo did log the input, and the password is not in it
			iolog := r.docker("sh", "-c", `find /var/log/sudo-io -type f -name stdin | wc -l; find /var/log/sudo-io -type f -exec zcat -f {} + 2>/dev/null`)
			if strings.HasPrefix(iolog, "0\n") || !strings.Contains(iolog, "line 1999 of the manifest") {
				t.Errorf("sudo logged no input: %.200q", iolog)
			}
			r.clean("input log", iolog, r.sudoLog())
			if n := r.failedPasswords(); n != 0 {
				t.Errorf("%d failed password attempts:\n%s", n, r.sudoLog())
			}
		})
	}
}

// Rules that name commands. tnctl runs every root command as `sh -c`, so what counts is whether
// sudo wants a password for sh, not for some other command a probe might ask about.
func TestSudoRigCommandRules(t *testing.T) {
	// NOPASSWD for sh next to the password rule of the group: `sudo -n true` asks for a
	// password there, the real command does not, so no password is used, right or wrong.
	t.Run("no password for sh", func(t *testing.T) {
		for _, platform := range []string{"", "not-the-password", "-"} {
			r := newSudoRig(t)
			if platform == "-" {
				platform = r.password
			}
			ex := r.shell("sh-np", platform)
			res, err := ex.Run(rigCtx(t), "LC_ALL=C sudo -n true")
			if _, asks := rootVerdict("sh-np", rootAttempt{code: res.Code, pre: res.Stderr}, rootTry); err != nil || !asks {
				t.Fatalf("precondition: `sudo -n true` does not ask: %+v %v", res, err)
			}
			r.docker("sh", "-c", ": > /var/log/sudo.log; rm -rf /run/faillock/* /var/run/faillock/*")
			code, out, errs, err := rigStream(t, ex, "id -u; cat", rigManifest)
			if err != nil || code != 0 || out != "0\n"+rigManifest || errs != "" {
				t.Errorf("platform password %q: code %d err %v %q %q", platform, code, err, out, errs)
			}
			r.wantInLog("COMMAND=validate", 0)
			r.wantInLog(rigAsked, 0)
			r.wantFailedLogins("sh-np", 0, "sudo needs no password for sh")
			r.clean("sh-np", out, errs, r.sudoLog())
		}
	})
	// NOPASSWD for k3s alone: sh needs the password, and gets it.
	t.Run("no password for k3s alone", func(t *testing.T) {
		r := newSudoRig(t)
		ex := r.shell("k3-np", r.password)
		code, out, errs, err := rigStream(t, ex, "id -u; cat", rigManifest)
		if err != nil || code != 0 || out != "0\n"+rigManifest || errs != "" {
			t.Errorf("code %d err %v %q %q", code, err, out, errs)
		}
		_, out, errs, err = rigStream(t, r.shell("k3-np", "not-the-password"), "id -u", "")
		rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
	})
	// Only id, with the password: sudo takes the password and then says that sh is not allowed.
	// That is sudo's reason, not a rejected password, and it is final for the session.
	// (sudo-rs says so before it asks for anything.)
	t.Run("sh is not allowed", func(t *testing.T) {
		r := newSudoRig(t)
		ex := r.shell("id-pw", r.password)
		for i := 0; i < 3; i++ {
			code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-id-pw", rigManifest)
			rigRefused(t, "not allowed", err, rootRefused, sudoNotAllowed, out, errs)
			if (i == 0 && code != 1) || !strings.Contains(fmt.Sprint(err), "do not let the device login user run sh as root") {
				t.Errorf("code %d err %v", code, err)
			}
			r.clean("not allowed", fmt.Sprint(err))
		}
		r.wantInLog("command not allowed", 1)
		r.wantInLog("incorrect password", 0)
		r.wantFailedLogins("id-pw", 0, "the password was right")
	})
	// Only id, without a password: `sudo -v` has nothing to check, and sudo still wants a
	// password before it says anything about sh.
	t.Run("no password for id alone", func(t *testing.T) {
		r := newSudoRig(t)
		ex := r.shell("id-np", r.password)
		said := sudoAsks
		if r.features["sudo-rs"] {
			said = sudoNotAllowed
		}
		for i := 0; i < 3; i++ {
			_, out, errs, err := rigStream(t, ex, "touch /tmp/ran-id-np", rigManifest)
			rigRefused(t, "single commands", err, rootRefused, said, out, errs)
		}
		r.wantInLog("incorrect password", 0)
		// (sudo 1.9.9: the `sudo -n` before the password and the one after it are both counted)
		r.wantFailedLogins("id-np", 2*r.tryCost(), "sudo was given nothing to check")
		// what such a login can do still works, as the login user
		if res, err := ex.Run(rigCtx(t), "sudo -n id -u"); err != nil || res.Stdout != "0\n" {
			t.Errorf("sudo -n id: %+v %v", res, err)
		}
	})
	t.Run("not in the sudoers", func(t *testing.T) {
		r := newSudoRig(t)
		said := sudoNotInSudoers
		if r.features["sudo-rs"] {
			said = sudoNotAllowed
		}
		ex := r.shell("ns", r.password)
		for i := 0; i < 3; i++ {
			_, out, errs, err := rigStream(t, ex, "touch /tmp/ran-ns", rigManifest)
			rigRefused(t, "not in the sudoers", err, rootRefused, said, out, errs)
		}
		r.wantInLog("incorrect password", 0)
		r.wantFailedLogins("ns", r.tryCost(), "the password was right")
		if !r.features["sudo-rs"] {
			// a wrong password is still a wrong password: PAM's helper says so first
			_, out, errs, err := rigStream(t, r.shell("ns", "not-the-password"), "touch /tmp/ran-ns", "")
			rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
		}
	})
	// requiretty: sudo says so to the first `sudo -n`, so the password is never sent.
	t.Run("requiretty", func(t *testing.T) {
		r := newSudoRig(t, "sudo")
		ex := r.shell("rt", r.password)
		for i := 0; i < 3; i++ {
			code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-rt", rigManifest)
			rigRefused(t, "requiretty", err, rootRefused, sudoNoTTY, out, errs)
			if (i == 0 && code != 1) || !strings.Contains(fmt.Sprint(err), "only works on a terminal") {
				t.Errorf("code %d err %v", code, err)
			}
		}
		// three root commands, and sudo was asked once (sudo 1.8 does not log that refusal at all)
		if n := strings.Count(r.sudoLog(), " : rt : "); n > 1 {
			t.Errorf("sudo was asked %d times:\n%s", n, r.sudoLog())
		}
		r.wantInLog("COMMAND=validate", 0)
		r.wantFailedLogins("rt", 0, "sudo was given nothing to check")
	})
	r := newSudoRig(t)
	if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
		t.Errorf("a script ran: %s", ls)
	}
}

// The script's own output is never read as sudo's: a script that prints sudo's sentence and
// exits 1 ran once, and is not run again with the password.
func TestSudoRigOutputOfTheScriptIsItsOwn(t *testing.T) {
	for _, user := range []string{"pw-bash", "np-zsh"} {
		r := newSudoRig(t)
		ex := r.shell(user, r.password)
		script := `echo x >> /tmp/ran-own; echo "sudo: a password is required" >&2; echo "` + rootMark + `rejected" >&2; exit `
		for _, code := range []int{1, rootExitRejected} {
			want := "sudo: a password is required\n" + rootMark + "rejected\n"
			res, err := ex.RunRoot(rigCtx(t), script+fmt.Sprint(code))
			if err != nil || res.Code != code || res.Stderr != want {
				t.Errorf("%s run: %+v %v", user, res, err)
			}
			got, _, errs, err := rigStream(t, ex, script+fmt.Sprint(code), rigManifest)
			if err != nil || got != code || errs != want {
				t.Errorf("%s stream: %d %v %q", user, got, err, errs)
			}
		}
		if ran := r.docker("cat", "/tmp/ran-own"); ran != "x\nx\nx\nx\n" {
			t.Errorf("%s: the script ran %d times, want 4", user, strings.Count(ran, "x"))
		}
		if ex.rootErr != nil {
			t.Errorf("%s: the session gave up: %v", user, ex.rootErr)
		}
	}
}

// The script gets the locale of the device login, although every sudo runs under LC_ALL=C.
func TestSudoRigLocale(t *testing.T) {
	for _, user := range []string{"pw-bash", "np-bash"} {
		r := newSudoRig(t)
		ex := r.shell(user, r.password)
		res, err := ex.RunRoot(rigCtx(t), `echo "[${LC_ALL-unset}]"`)
		if err != nil || res.Stdout != "[unset]\n" {
			t.Errorf("%s: %+v %v", user, res, err)
		}
	}
}

// Two wrapper calls from one parent process on the device, each with its own stdin. This is the
// shape in which one sudo call can leave a ticket for the next: with the normal per-parent
// ticket each call asks sudo itself, with a ticket for all sessions the second finds sudo open.
// Either way both payloads arrive whole and the password is in neither.
func TestSudoRigTwoCallsFromOneParent(t *testing.T) {
	for _, shell := range rigShells {
		for _, kind := range []string{"pw", "gl", "np"} {
			t.Run(kind+"-"+shell, func(t *testing.T) {
				var needs []string
				if kind == "gl" {
					needs = []string{"sudo"}
				}
				r := newSudoRig(t, needs...)
				ex := r.shell(kind+"-"+shell, r.password)
				// the wrapper with the password line, and for the passwordless login without it
				mode, send := rootWithPassword, `printf "%s\npayload %s line 1\npayload %s line 2\n" "$pw" "$i" "$i"`
				if kind == "np" {
					mode, send = rootTry, `printf "payload %s line 1\npayload %s line 2\n" "$i" "$i"`
				}
				parent := `IFS= read -r pw
for i in 1 2; do
  ` + send + ` | ` + rootCommand(`echo "call as $(id -un)"; cat`, mode) + ` || echo "call $i failed: $?"
done`
				var out, errb bytes.Buffer
				code, err := ex.Stream(rigCtx(t), "sh -c "+shq(parent), strings.NewReader(r.password+"\n"), &out, &errb)
				want := "call as root\npayload 1 line 1\npayload 1 line 2\ncall as root\npayload 2 line 1\npayload 2 line 2\n"
				if err != nil || code != 0 || out.String() != want || strings.Contains(errb.String(), "failed") {
					t.Errorf("code %d err %v\nstdout %q\nstderr %q", code, err, out.String(), errb.String())
				}
				if r.features["sudo"] {
					if n := r.failedPasswords(); n != 0 {
						t.Errorf("%d failed password attempts:\n%s", n, r.sudoLog())
					}
				}
				r.clean("two calls", out.String(), r.sudoLog())
			})
		}
	}
}
