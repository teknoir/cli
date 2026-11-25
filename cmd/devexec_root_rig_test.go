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
// with one login user per sudo state and login shell (test/sudo-root, started by its run.sh).
// They are skipped unless run.sh set the TN_SUDO_RIG_* variables, so the unit suite needs no
// Docker. The stand-ins in devexec_root_test.go cover the same cases without it; these tests are
// the proof that real sudo, real shells and a real multiplexed ssh behave the way those assume.

type sudoRig struct {
	t         *testing.T
	container string
	port      string
	dir       string
	password  string
}

func newSudoRig(t *testing.T) *sudoRig {
	t.Helper()
	r := &sudoRig{t: t, container: os.Getenv("TN_SUDO_RIG_CONTAINER"), port: os.Getenv("TN_SUDO_RIG_PORT"),
		dir: os.Getenv("TN_SUDO_RIG_DIR"), password: os.Getenv("TN_SUDO_RIG_PASSWORD")}
	if r.container == "" || r.port == "" || r.dir == "" || r.password == "" {
		t.Skip("needs the sudo rig: run test/sudo-root/run.sh")
	}
	// A new test starts with no ticket and an empty sudo log.
	r.docker("sh", "-c", ": > /var/log/sudo.log; rm -rf /run/sudo/ts/*; rm -f /tmp/ran-*")
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

// The normal device: sudo asks for the login user's password.
func TestSudoRigPasswordSudo(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t)
			user := "pw-" + shell
			ex := r.shell(user, r.password)

			// The login user itself is not root and has no passwordless sudo: the state under test.
			res, err := ex.Run(rigCtx(t), "id -u; sudo -n true")
			if err != nil || !strings.Contains(res.Stderr, "a password is required") || strings.TrimSpace(res.Stdout) == "0" {
				t.Fatalf("precondition: %+v %v", res, err)
			}

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

			if n := r.failedPasswords(); n != 0 {
				t.Errorf("%d failed password attempts, want none:\n%s", n, r.sudoLog())
			}
			r.clean("sudo log", r.sudoLog())

			ex.Close()
			if _, err := os.Stat(ex.controlPath); !os.IsNotExist(err) {
				t.Errorf("the ssh master outlives the session: %v", err)
			}
		})
	}
}

// A login with passwordless sudo (the bootstrap's drop-in): it must work as before the password
// existed in this path, whatever password the platform has. The password line is still sent, and
// must not reach the command.
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
				if strings.Contains(out+res.Stdout, "not-the-password") {
					t.Errorf("the first line reached the command: %q %q", res.Stdout, out)
				}
				// sudo was never given a password to check
				if log := r.sudoLog(); strings.Contains(log, "incorrect password") || strings.Contains(log, "COMMAND=validate") {
					t.Errorf("sudo was asked to check a password:\n%s", log)
				}
			})
		}
	}
}

// The platform's password is not the device's. With a payload of several lines behind it: sudo
// counts exactly one failed attempt, no line of the payload is taken as another guess, the
// script does not run, and the session does not ask sudo again.
func TestSudoRigWrongPassword(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t)
			ex := r.shell("pw-"+shell, "not-the-password")
			// the payload even has the right password in it, as its first line
			payload := r.password + "\nline 2\nline 3\nline 4\n"
			script := "touch /tmp/ran-" + shell + "; cat"
			code, out, errs, err := rigStream(t, ex, script, payload)
			var re *rootError
			if !errors.As(err, &re) || re.reason != rootRejected || code != rootExitRejected || out != "" || errs != "" {
				t.Errorf("code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
			}
			if n := r.failedPasswords(); n != 1 {
				t.Errorf("%d failed password attempts, want exactly 1:\n%s", n, r.sudoLog())
			}
			if ls := r.docker("sh", "-c", "ls /tmp/ran-* 2>/dev/null || true"); ls != "" {
				t.Errorf("the script ran: %s", ls)
			}
			// the session is done with root: more calls do not reach sudo
			for i := 0; i < 3; i++ {
				if _, err := ex.RunRoot(rigCtx(t), "id -u"); !errors.As(err, &re) || re.reason != rootRejected {
					t.Errorf("call %d after the refusal: %v", i, err)
				}
			}
			if _, _, _, err := rigStream(t, ex, script, payload); !isRootError(err) {
				t.Errorf("stream after the refusal: %v", err)
			}
			if n := r.failedPasswords(); n != 1 {
				t.Errorf("%d failed password attempts after more calls, want still 1:\n%s", n, r.sudoLog())
			}
			r.clean("wrong password", out, errs, fmt.Sprint(err))
			// as the login user the session still works
			if res, err := ex.Run(rigCtx(t), "id -un"); err != nil || res.Stdout != "pw-"+shell+"\n" {
				t.Errorf("plain: %+v %v", res, err)
			}
		})
	}
}

// sudo asks for a password and the platform has none, or one that cannot be sent as a line: no
// attempt is made at all (an empty line would count as a failed one).
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
				var re *rootError
				if !errors.As(err, &re) || re.reason != rootNoPassword || re.unusable != (platform != "") || code != rootExitNoPassword || out != "" || errs != "" {
					t.Errorf("code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
				}
				if log := r.sudoLog(); r.failedPasswords() != 0 || strings.Contains(log, "COMMAND=validate") {
					t.Errorf("sudo was given a password to check:\n%s", log)
				}
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
			code, out, errs, err := rigStream(t, ex, "id -u", rigManifest)
			var re *rootError
			if !errors.As(err, &re) || re.reason != rootNoTicket || code != rootExitNoTicket || out != "" || errs != "" {
				t.Errorf("code %d err %v\nstdout %q\nstderr %q", code, err, out, errs)
			}
			if n := r.failedPasswords(); n != 0 {
				t.Errorf("%d failed password attempts: the password was right:\n%s", n, r.sudoLog())
			}
			r.clean("no ticket", out, errs, fmt.Sprint(err))
		})
	}
}

// A ticket that is still valid when the next call comes (here: one ticket for all sessions of
// the user). The wrapper finds sudo open, sends no password, and drops the first line: even a
// wrong one does no harm and does not reach the command.
func TestSudoRigValidTicket(t *testing.T) {
	for _, shell := range rigShells {
		t.Run(shell, func(t *testing.T) {
			r := newSudoRig(t)
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

// Two root calls from one parent process on the device, each with its own stdin. This is the
// shape in which one sudo call can leave a ticket for the next: with the normal per-parent
// ticket each call asks sudo itself, with a ticket for all sessions the second finds sudo open.
// Either way both payloads arrive whole and the password is in neither.
func TestSudoRigTwoCallsFromOneParent(t *testing.T) {
	for _, shell := range rigShells {
		for _, kind := range []string{"pw", "gl", "np"} {
			t.Run(kind+"-"+shell, func(t *testing.T) {
				r := newSudoRig(t)
				ex := r.shell(kind+"-"+shell, r.password)
				parent := `IFS= read -r pw
for i in 1 2; do
  printf "%s\npayload %s line 1\npayload %s line 2\n" "$pw" "$i" "$i" | ` + rootCommand(`echo "call as $(id -un)"; cat`) + ` || echo "call $i failed: $?"
done`
				var out, errb bytes.Buffer
				code, err := ex.Stream(rigCtx(t), "sh -c "+shq(parent), strings.NewReader(r.password+"\n"), &out, &errb)
				want := "call as root\npayload 1 line 1\npayload 1 line 2\ncall as root\npayload 2 line 1\npayload 2 line 2\n"
				if err != nil || code != 0 || out.String() != want || errb.String() != "" {
					t.Errorf("code %d err %v\nstdout %q\nstderr %q", code, err, out.String(), errb.String())
				}
				if n := r.failedPasswords(); n != 0 {
					t.Errorf("%d failed password attempts:\n%s", n, r.sudoLog())
				}
				r.clean("two calls", out.String(), errb.String(), r.sudoLog())
			})
		}
	}
}
