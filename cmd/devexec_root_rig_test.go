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

// tryCost is what one `sudo -n` that needs a password costs the login with pam_faillock: 1 failed
// login with sudo 1.9.9 (Ubuntu 22.04), which takes even that to PAM, and 0 with every other
// sudo. Every tnctl run starts with one such `sudo -n` (the session then knows that sudo asks),
// so this is also what a run costs that sudo does not end up accepting a password from.
func (r *sudoRig) tryCost() int {
	if r.features["faillock"] && r.features["sudo-1.9.9"] {
		return 1
	}
	return 0
}

// faillockDeny is the number of failed logins at which pam_faillock locks a login (its default,
// and what setup.sh spells out). A locked login gets no more of them: the count stays there.
const faillockDeny = 3

// failedAfter is pam_faillock's count after so many tnctl runs that left a failed login each.
func (r *sudoRig) failedAfter(runs int) int { return min(runs*r.tryCost(), faillockDeny) }

// noHelper takes PAM's helper from the login users for the rest of the test (root, so sshd and
// sudo's PAM, still runs it): the wrapper skips a helper it may not execute.
func (r *sudoRig) noHelper() {
	r.t.Helper()
	helper := strings.TrimSpace(r.docker("sh", "-c", `readlink -f "$(command -v unix_chkpwd)"`))
	mode := strings.TrimSpace(r.docker("stat", "-c", "%a", helper))
	r.docker("chmod", "o-rwx", helper)
	r.t.Cleanup(func() { r.docker("chmod", mode, helper) })
}

// wantLocked checks that the login is locked for password logins: tnctl with the right password
// is refused, and says "locked" (PAM's helper found the password right), never "rejected". The
// login's ssh key still works, which is how the session got there.
func (r *sudoRig) wantLocked(user, why string) {
	r.t.Helper()
	ex := r.shell(user, r.password)
	code, out, errs, err := rigStream(r.t, ex, "touch /tmp/ran-locked-"+user, rigManifest)
	rigRefused(r.t, why, err, rootLocked, 0, out, errs)
	if code != rootExitLocked {
		r.t.Errorf("%s: code %d, want %d", why, code, rootExitLocked)
	}
	if res, err := ex.Run(rigCtx(r.t), "id -un"); err != nil || res.Stdout != user+"\n" {
		r.t.Errorf("%s: the ssh key login of a locked login: %+v %v", why, res, err)
	}
}

// unlock ends the lock the way pam_faillock's unlock_time does (600 s in the rig).
func (r *sudoRig) unlock(user string) { r.docker("faillock", "--user", user, "--reset") }

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
			if _, asks := rootVerdict(user, rootAttempt{code: 1, sudo: res.Stderr}, rootTry); err != nil || !asks || strings.TrimSpace(res.Stdout) == "0" {
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
// helper says so: sudo is never given the password, the script does not run, and the session
// does not ask again.
//
// What that costs the login is the one `sudo -n` with which every tnctl run finds out that sudo
// asks. With every sudo but 1.9.9 that is nothing, however often it happens. With sudo 1.9.9 and
// pam_faillock it is one failed login per run, and nothing clears it (sudo never accepts a
// password): the count goes 1, 2, 3, the third run locks the login, and the right password is
// then refused as well (exit 95) until the lock ends.
func TestSudoRigWrongPassword(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t)
			user := "pw-" + shell
			// the payload even has the right password in it, as its first line
			payload := r.password + "\nline 2\nline 3\nline 4\n"
			script := "touch /tmp/ran-" + shell + "; cat"
			// five tnctl runs, more than the 3 failed logins that lock an account
			const runs = 5
			for run := 1; run <= runs; run++ {
				ex := r.shell(user, "not-the-password")
				code, out, errs, err := rigStream(t, ex, script, payload)
				rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
				if code != rootExitRejected {
					t.Errorf("code %d", code)
				}
				// PAM's helper refused it: this is a wrong password for certain, not a locked login
				if strings.Contains(fmt.Sprint(err), "locked") {
					t.Errorf("run %d: %v", run, err)
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
				// sudo was asked once per run, without a password, and never given one
				r.wantInLog(rigAsked, run)
				r.wantInLog("COMMAND=validate", 0)
				r.wantInLog("incorrect password", 0)
				// The wrong password itself never reached sudo. What adds up is the `sudo -n` of
				// each run where sudo counts that (1.9.9): 1, 2, 3, and a locked login gets no more.
				r.wantFailedLogins(user, r.failedAfter(run), fmt.Sprintf("after %d tnctl runs with a wrong password", run))
			}
			if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
				t.Errorf("the script ran: %s", ls)
			}
			if r.failedAfter(runs) >= faillockDeny {
				// sudo 1.9.9 with pam_faillock: the runs have locked the login
				r.wantLocked(user, "the right password after 3 runs with a wrong one")
				r.wantFailedLogins(user, faillockDeny, "a refused login adds nothing to a lock")
				r.unlock(user)
			}
			// not locked (any more): the right password works at once
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
// its input has ended) and as 1 with sudo-rs. A second such run locks the login, and with sudo
// 1.9.9, where the `sudo -n` before it is a failed login too, this one run does (2 + 1 = 3).
// Without the helper tnctl cannot tell that lock from a wrong password, and says so.
func TestSudoRigWithoutThePAMHelper(t *testing.T) {
	r := newSudoRig(t)
	r.noHelper()

	user := "fl-a"
	ex := r.shell(user, "not-the-password")
	payload := r.password + "\n" + r.password + "\nline 3\n"
	code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-nohelper; cat", payload)
	rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
	if code != rootExitRejected || !strings.Contains(fmt.Sprint(err), "or the login is locked after failed attempts") {
		t.Errorf("code %d err %v", code, err)
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
	failed := r.failedLogins(user)
	if failed != -1 {
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
	if failed >= faillockDeny {
		// sudo 1.9.9: 3 failed logins, the login is locked by this one run. The right password is
		// refused by sudo, and without the helper that reads like a wrong one: "or locked".
		_, out, errs, err = rigStream(t, r.shell(user, r.password), "touch /tmp/ran-nohelper", rigManifest)
		rigRefused(t, "the right password for a locked login, no helper", err, rootRejected, 0, out, errs)
		if !strings.Contains(fmt.Sprint(err), "or the login is locked after failed attempts") {
			t.Errorf("a locked login without the helper: %v", err)
		}
		r.wantFailedLogins(user, faillockDeny, "a refused login adds nothing to a lock")
		r.unlock(user)
	}
	// not locked (any more): the right password works, through sudo alone
	ex2 := r.shell(user, r.password)
	code, out, errs, err = rigStream(t, ex2, "id -u; cat", rigManifest)
	if err != nil || code != 0 || out != "0\n"+rigManifest || errs != "" {
		t.Errorf("the right password without the helper: code %d err %v %q %q", code, err, out, errs)
	}
	r.wantFailedLogins(user, 0, "the right password clears the count")
}

// pam_faillock has locked the login (someone mistyped the password in a shell). The platform's
// password is right and sudo refuses it all the same: that is not "the password was not
// accepted", and it costs the login nothing more: a lock is not made longer by tnctl. Without
// PAM's helper the same lock cannot be told from a wrong password: the message names both.
func TestSudoRigLockedLogin(t *testing.T) {
	r := newSudoRig(t, "faillock")
	user := "fl-b"
	plain := r.shell(user, "")
	for i := 0; i < 4 && r.failedLogins(user) < faillockDeny; i++ {
		if _, err := plain.Stream(rigCtx(t), `LC_ALL=C sudo -S -p "" -v`, strings.NewReader("mistyped\n"), io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	locked := r.failedLogins(user)
	if locked < faillockDeny {
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
		r.wantFailedLogins(user, locked, "tnctl adds nothing to a lock")
	}
	// What the lock stops is the password of the login: sudo with it, also typed by a person.
	// The ssh key is not a password login, and a command as the login user still runs.
	if code, err := plain.Stream(rigCtx(t), `LC_ALL=C sudo -S -p "" -v`, strings.NewReader(r.password+"\n"), io.Discard, io.Discard); err != nil || code == 0 {
		t.Errorf("sudo took the right password of a locked login: code %d err %v", code, err)
	}
	if res, err := plain.Run(rigCtx(t), "id -un"); err != nil || res.Stdout != user+"\n" {
		t.Errorf("the ssh key login of a locked login: %+v %v", res, err)
	}
	t.Run("without PAM's helper", func(t *testing.T) {
		r := &sudoRig{t: t, container: r.container, port: r.port, dir: r.dir, password: r.password, features: r.features}
		r.noHelper()
		ex := r.shell(user, r.password)
		code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-locked; cat", rigManifest)
		rigRefused(t, "locked login, no helper", err, rootRejected, 0, out, errs)
		if code != rootExitRejected || !strings.Contains(fmt.Sprint(err), "the password on the device is another one, or the login is locked after failed attempts") {
			t.Errorf("code %d err %v", code, err)
		}
		r.wantFailedLogins(user, locked, "tnctl adds nothing to a lock")
	})
	if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
		t.Errorf("the script ran: %s", ls)
	}
	// the lock has ended: the same password works
	r.unlock(user)
	if res, err := r.shell(user, r.password).RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
		t.Errorf("after the lock: %+v %v", res, err)
	}
}

// What a `sudo -n` that needs a password costs with pam_faillock, measured: nothing, except with
// sudo 1.9.9 (Ubuntu 22.04), which takes it to PAM as a failed login. A session asks that way
// once however many root commands it runs, so the cost is per tnctl run, and only a password
// that sudo accepts clears it (TestSudoRigSudo199EveryRunIsAFailedLoginAndThreeLock).
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

// The lock-out arithmetic of sudo 1.9.9 (Ubuntu 22.04) with pam_faillock, where every tnctl run
// that sudo does not accept a password from is one failed login (its first `sudo -n`), and 3
// lock the login for password logins. With every other sudo of the matrix the same runs cost
// nothing and lock nothing, which is checked just the same.
func TestSudoRigSudo199EveryRunIsAFailedLoginAndThreeLock(t *testing.T) {
	// No password on the platform: sudo is given nothing, and the third run still locks the
	// login, for the person who does have the password too.
	t.Run("3 runs without a platform password lock", func(t *testing.T) {
		r := newSudoRig(t, "faillock")
		user := "fl-a"
		for run := 1; run <= 4; run++ {
			ex := r.shell(user, "")
			for i := 0; i < 2; i++ {
				_, out, errs, err := rigStream(t, ex, "touch /tmp/ran-nopw", rigManifest)
				rigRefused(t, "no password", err, rootNoPassword, 0, out, errs)
			}
			r.wantFailedLogins(user, r.failedAfter(run), fmt.Sprintf("after %d tnctl runs without a password", run))
		}
		r.wantInLog("COMMAND=validate", 0)
		r.wantInLog("incorrect password", 0)
		if r.failedAfter(4) >= faillockDeny {
			r.wantLocked(user, "the right password after 3 runs without one")
			r.unlock(user)
		}
		if res, err := r.shell(user, r.password).RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
			t.Errorf("the right password: %+v %v", res, err)
		}
		r.wantFailedLogins(user, 0, "the right password clears the count")
	})

	// Three runs with the right password that start at the same time: each asks `sudo -n` before
	// any of them has given the password, and that is 3 failed logins before the first password
	// arrives. The order is fixed here (the first step of all three, then the rest of each); it
	// is the order three tnctl started together were seen to take.
	t.Run("3 runs started at once with the right password lock", func(t *testing.T) {
		r := newSudoRig(t, "faillock")
		user := "fl-b"
		var sessions []*wgExec
		for i := 0; i < 3; i++ {
			ex := r.shell(user, r.password)
			// the first step of a run, as asRoot takes it: the command with `sudo -n` alone
			var errb bytes.Buffer
			filter := &rootStderr{w: &errb}
			code, err := ex.Stream(rigCtx(t), rootCommand("id -u", rootTry), nil, io.Discard, filter)
			a := filter.finish()
			a.code = code
			if _, asks := rootVerdict(user, a, rootTry); err != nil || !asks {
				t.Fatalf("run %d: sudo does not ask: code %d err %v %+v", i, code, err, a)
			}
			ex.rootAsks = true // what the session knows after that step
			sessions = append(sessions, ex)
		}
		r.wantFailedLogins(user, r.failedAfter(3), "three runs have asked sudo, none has given the password")
		locked := r.failedAfter(3) >= faillockDeny
		for i, ex := range sessions {
			res, err := ex.RunRoot(rigCtx(t), "id -u")
			var re *rootError
			if locked && (!errors.As(err, &re) || re.reason != rootLocked || res.Stdout != "") {
				t.Errorf("run %d: %+v %v, want a locked login", i, res, err)
			}
			if !locked && (err != nil || res.Stdout != "0\n") {
				t.Errorf("run %d: %+v %v", i, res, err)
			}
		}
		if locked {
			// sudo cannot tell either: it logs the right password of a locked login as incorrect
			r.wantInLog("incorrect password", 3)
			r.wantFailedLogins(user, faillockDeny, "the right password of a locked login adds nothing")
			r.unlock(user)
		} else {
			r.wantInLog("incorrect password", 0)
		}
		// One after the other the same three runs are no problem: each clears its own count.
		for i := 0; i < 3; i++ {
			if res, err := r.shell(user, r.password).RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
				t.Errorf("run %d, one after the other: %+v %v", i, res, err)
			}
			r.wantFailedLogins(user, 0, "sudo accepted the password")
		}
		// Not asserted, for the record: three runs that really start together. Whether they lock
		// depends on which `sudo -n` comes before whose password.
		refused := make(chan bool, 3)
		ctx := rigCtx(t)
		for i := 0; i < 3; i++ {
			ex := r.shell(user, r.password)
			go func() {
				_, err := ex.RunRoot(ctx, "id -u")
				refused <- err != nil
			}()
		}
		n := 0
		for i := 0; i < 3; i++ {
			if <-refused {
				n++
			}
		}
		t.Logf("three tnctl runs started together with the right password: %d of 3 were refused, pam_faillock holds %d failed logins", n, r.failedLogins(user))
		r.unlock(user)
	})

	// pam_faillock as its manual sets it up has one more line, `account required pam_faillock.so`,
	// which clears the count at every login that gets in, the ssh key login of tnctl included.
	// Then runs one after the other never add up: the count is that of the last run alone. (The
	// probes of a run share one ssh login, so its `sudo -n` is still there afterwards; a run of
	// exec --root or logs logs in again for the password and leaves 0.)
	t.Run("with the account line of pam_faillock runs do not add up", func(t *testing.T) {
		r := newSudoRig(t, "faillock")
		user := "fl-c"
		r.docker("sh", "-c", `cp -p /etc/pam.d/common-account /etc/pam.d/common-account.tnctl-rig && echo "account required pam_faillock.so" >> /etc/pam.d/common-account`)
		t.Cleanup(func() { r.docker("mv", "/etc/pam.d/common-account.tnctl-rig", "/etc/pam.d/common-account") })
		for run := 1; run <= 5; run++ {
			ex := r.shell(user, "not-the-password")
			var re *rootError
			if res, err := ex.RunRoot(rigCtx(t), "touch /tmp/ran-account"); !errors.As(err, &re) || re.reason != rootRejected || res.Stdout != "" {
				t.Errorf("run %d: %+v %v", run, res, err)
			}
			r.wantFailedLogins(user, r.tryCost(), fmt.Sprintf("after %d tnctl runs with a wrong password, each a new ssh login", run))
			_, out, errs, err := rigStream(t, r.shell(user, "not-the-password"), "touch /tmp/ran-account", rigManifest)
			rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
			r.wantFailedLogins(user, 0, "the ssh login that carries the password cleared the count")
		}
		if res, err := r.shell(user, r.password).RunRoot(rigCtx(t), "id -u"); err != nil || res.Stdout != "0\n" {
			t.Errorf("the right password after five wrong ones: %+v %v", res, err)
		}
		r.wantFailedLogins(user, 0, "the right password clears the count")
	})
	r := newSudoRig(t)
	if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
		t.Errorf("a script ran: %s", ls)
	}
}

// Defaults targetpw, rootpw: sudo asks for root's password, which the platform does not have.
// PAM's helper finds the login's password right, sudo refuses it, and that used to read as a
// locked login. sudo's prompt says whose password it wants, so tnctl says that instead, once.
// The cost cannot be avoided (whose password sudo wants is not known before it asks): the
// login's password is offered to sudo once, which pam_faillock counts against root, not against
// the login: 2 failed logins (1 with sudo-rs), and with sudo 1.9.9 one more for the `sudo -n`.
func TestSudoRigTargetpwAndRootpw(t *testing.T) {
	for _, user := range []string{"tp", "rp"} {
		t.Run(user, func(t *testing.T) {
			r := newSudoRig(t)
			ex := r.shell(user, r.password)
			for i := 0; i < 3; i++ {
				_, out, errs, err := rigStream(t, ex, "touch /tmp/ran-"+user, rigManifest)
				rigRefused(t, "root's password", err, rootRefused, sudoOtherPassword, out, errs)
				if !strings.Contains(fmt.Sprint(err), "asks for the password of root, not for the device login's") {
					t.Errorf("call %d: %v", i, err)
				}
				r.clean("root's password", fmt.Sprint(err))
			}
			// three root commands: sudo was asked once and given the login's password once
			r.wantInLog(rigAsked, 1)
			r.wantInLog("incorrect password", 1)
			r.wantFailedLogins(user, 0, "sudo asked for root's password, not the login's")
			guess := map[bool]int{true: 1, false: 2}[r.features["sudo-rs"]]
			r.wantFailedLogins("root", guess+r.tryCost(), "the login's password was offered to sudo once as root's")
			r.clean("sudo log", r.sudoLog())

			// a password that is not even the login's never reaches sudo
			r2 := newSudoRig(t)
			_, out, errs, err := rigStream(t, r2.shell(user, "not-the-password"), "touch /tmp/ran-"+user, rigManifest)
			rigRefused(t, "wrong password", err, rootRejected, 0, out, errs)
			r2.wantInLog("incorrect password", 0)
			r2.wantFailedLogins("root", r2.tryCost(), "PAM's helper refused the password")
			if ls := r2.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
				t.Errorf("the script ran: %s", ls)
			}
		})
	}
}

// ssh itself fails (255) before anything runs on the device: the device was not reached. What
// ssh and its ProxyCommand said is shown as it came, and it is never read as sudo's answer, also
// when it has one of sudo's sentences in it: the session keeps no failure and sends no password.
// With the real ssh client; the ProxyCommand stands for wg-proxy, which says such things.
func TestSudoRigSSHItselfFails(t *testing.T) {
	for name, said := range map[string]string{
		"an expired login":        "Your tnctl login has expired. Run: tnctl login",
		"sudo's sentence":         "sudo: a password is required",
		"a sentence of sudoers":   "Sorry, user pw-bash is not allowed to execute this as root on dev-1.",
		"an account that expired": "sudo: account validation failure, is your account locked?",
	} {
		t.Run(name, func(t *testing.T) {
			r := newSudoRig(t)
			user := "pw-bash"
			ex := r.shell(user, r.password)
			args := ex.sess.baseArgs
			ex.sess.baseArgs = append(append([]string{}, args[:len(args)-1]...),
				"-o", "ProxyCommand=sh -c 'echo \""+said+"\" >&2; exit 1'", args[len(args)-1])

			code, out, errs, err := rigStream(t, ex, "touch /tmp/ran-ssh; cat", rigManifest)
			if err == nil || isRootError(err) || err.Error() != "cannot reach "+user+" over WireGuard: "+said || code != sshConnectFailed || out != "" || !strings.HasPrefix(errs, said+"\n") {
				t.Errorf("stream: code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
			}
			// (Over the shared connection of the probes OpenSSH sends a ProxyCommand's stderr to
			// /dev/null, so there only ssh's own line is left to show.)
			res, err := ex.RunRoot(rigCtx(t), "touch /tmp/ran-ssh")
			if err == nil || isRootError(err) || !strings.HasPrefix(err.Error(), "cannot reach "+user+" over WireGuard: ") || res.Stdout != "" {
				t.Errorf("run: %+v %v", res, err)
			}
			if ex.rootErr != nil || ex.rootAsks {
				t.Errorf("the session read ssh's failure as sudo's answer: rootErr %v, sudo asks %v", ex.rootErr, ex.rootAsks)
			}
			r.clean("ssh fails", out, errs, fmt.Sprint(err))
			// the connection is back: the same session works, and that is the first time the device hears of it
			r.wantInLog(rigAsked, 0)
			ex.sess.baseArgs = args
			code, out, errs, err = rigStream(t, ex, "id -u; cat", rigManifest)
			if err != nil || code != 0 || out != "0\n"+rigManifest || errs != "" {
				t.Errorf("after the failure: code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
			}
			r.wantInLog(rigAsked, 1)
			r.wantFailedLogins(user, 0, "the password was right")
			if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
				t.Errorf("the script ran: %s", ls)
			}
		})
	}
	// and a port nobody listens on: ssh's own words
	r := newSudoRig(t)
	ex := r.shell("pw-bash", r.password)
	for i, a := range ex.sess.baseArgs {
		if a == "-p" {
			ex.sess.baseArgs[i+1] = "1"
		}
	}
	code, out, errs, err := rigStream(t, ex, "id -u", "")
	if err == nil || isRootError(err) || !strings.Contains(err.Error(), "cannot reach pw-bash over WireGuard: ssh: connect to host 127.0.0.1 port 1") || code != sshConnectFailed || out != "" || errs != "" || ex.rootErr != nil || ex.rootAsks {
		t.Errorf("no listener: code %d err %v %q %q, rootErr %v, sudo asks %v", code, err, out, errs, ex.rootErr, ex.rootAsks)
	}
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
			if _, asks := rootVerdict("sh-np", rootAttempt{code: res.Code, sudo: res.Stderr}, rootTry); err != nil || !asks {
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
