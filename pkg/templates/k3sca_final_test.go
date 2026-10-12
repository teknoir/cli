package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests of the findings of the final review: an activating unit, the unknown data dir, the checked
// rebuild of the bundles, leftover work dirs, and a restart that cannot hang.

// fakeSystemctl puts a systemctl first in PATH. `is-active` prints state (and exits 3 unless it is
// "active"), stop/start change the state, restart sleeps for sleep seconds. Every call is logged.
func fakeSystemctl(t *testing.T, state string, restartSleep int) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "systemctl.log")
	st := filepath.Join(dir, "state")
	write(t, st, state+"\n")
	script := `#!/bin/sh
echo "$*" >> '` + log + `'
case "$1" in
  is-active) s=$(/bin/cat '` + st + `'); echo "$s"; [ "$s" = active ] ;;
  stop) echo inactive > '` + st + `' ;;
  start) echo active > '` + st + `' ;;
  restart) sleep ` + itoa(restartSleep) + ` ;;
  cat) exit 0 ;;
esac
`
	write(t, filepath.Join(dir, "systemctl"), script)
	if err := os.Chmod(filepath.Join(dir, "systemctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func readLog(path string) string { b, _ := os.ReadFile(path); return string(b) }

// ---------------------------------------------------------------- activating

func TestAnActivatingUnitIsStoppedBeforeTheRegeneration(t *testing.T) {
	for _, state := range []string{"activating", "reloading"} {
		t.Run(state, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
			p.legacyPKI(false) // hung first start
			write(t, p.unit, unitText())
			log := fakeSystemctl(t, state, 0)
			out := p.mustSh("tn_k3s_ca_setup")
			if !strings.Contains(out, "Stopping k3s") {
				t.Errorf("an %s unit was not stopped:\n%s", state, out)
			}
			if got := readLog(log); !strings.Contains(got, "stop k3s.service") || !strings.Contains(got, "start k3s.service") {
				t.Errorf("systemctl calls:\n%s", got)
			}
			p.assertGoodPKI(orig)
		})
	}
	// an inactive or failed unit is not stopped (and so not started either)
	for _, state := range []string{"inactive", "failed"} {
		p := newPKI(t)
		p.legacyCert()
		p.legacyPKI(false)
		write(t, p.unit, unitText())
		log := fakeSystemctl(t, state, 0)
		out := p.mustSh("tn_k3s_ca_setup")
		if strings.Contains(out, "Stopping k3s") || strings.Contains(readLog(log), "stop") {
			t.Errorf("a %s unit was stopped:\n%s\n%s", state, out, readLog(log))
		}
	}
}

// ---------------------------------------------------------------- unknown data dir

func TestReadyK3sWithAnUninitialisedDataDirStopsTheSetup(t *testing.T) {
	// k3s is up and ready, but the data dir we resolved was never initialised: k3s runs somewhere else
	p := newPKI(t)
	p.legacyCert()
	write(t, p.unit, unitText())
	log := fakeSystemctl(t, "active", 0)
	out, err := p.sh("tn_k3s_ca_setup")
	if exitCode(err) != 1 {
		t.Fatalf("rc=%d\n%s", exitCode(err), out)
	}
	for _, want := range []string{"active and ready", "never initialised", "another data dir", "DATA_DIR=", "The CA and k3s were not changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("message lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(p.tls("root-ca.pem")); err == nil {
		t.Error("a CA was generated in the wrong data dir")
	}
	if strings.Contains(readLog(log), "stop") {
		t.Error("k3s was stopped")
	}

	// the same with a (useless) PKI already in that dir: setup and remediate both refuse
	q := newPKI(t)
	q.legacyCert()
	q.legacyPKI(false)
	write(t, q.unit, unitText())
	fakeSystemctl(t, "active", 0)
	before := sum(t, q.tls("root-ca.pem"))
	if out, err := q.sh("tn_k3s_ca_setup"); exitCode(err) != 1 || !strings.Contains(out, "another data dir") {
		t.Errorf("setup: rc=%d\n%s", exitCode(err), out)
	}
	out, err = q.sh("tn_ca_remediate prepare")
	if exitCode(err) != 4 || !strings.Contains(out, "another data dir") {
		t.Errorf("remediate: rc=%d\n%s", exitCode(err), out)
	}
	if sum(t, q.tls("root-ca.pem")) != before {
		t.Error("server/tls was changed")
	}
	if m, _ := filepath.Glob(filepath.Join(q.data, "server", "tls-backup-tn-*")); len(m) != 0 {
		t.Errorf("a backup was made: %v", m)
	}
}

func TestTheDataDirGuardOnlyFiresForAReadyUnit(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		initialised bool
		wantRC      int
	}{
		{"activating: a hung first start is the case to repair", "activating", false, 0},
		{"inactive", "inactive", false, 0},
		{"ready and initialised: this is the right data dir", "active", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			if tc.initialised {
				// a device whose k3s started once with a good CA: setup leaves it alone
				p.mustSh("tn_k3s_ca_setup")
				write(t, filepath.Join(p.data, "server", "token"), "K10x::server:y")
			} else {
				p.legacyPKI(false)
			}
			write(t, p.unit, unitText())
			fakeSystemctl(t, tc.state, 0)
			out, err := p.sh("tn_k3s_ca_setup", "K3S_BIN="+fakeK3sPath(t), "TN_K3S_RESTART_CMD=true")
			if exitCode(err) != tc.wantRC {
				t.Errorf("rc=%d\n%s", exitCode(err), out)
			}
			if strings.Contains(out, "another data dir") {
				t.Errorf("the guard fired:\n%s", out)
			}
		})
	}
	// no unit at all (a new device): not a guard case
	p := newPKI(t)
	p.legacyCert()
	fakeSystemctl(t, "inactive", 0)
	if out, err := p.sh("tn_k3s_ca_setup"); err != nil {
		t.Errorf("new device: %v\n%s", err, out)
	}
}

func fakeK3sPath(t *testing.T) string {
	bin, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	return bin
}

// ---------------------------------------------------------------- the rebuild of the bundles

// fakeCat first in PATH: the Nth call that has newroot.pem among its arguments either fails
// (mode fail) or does nothing and succeeds (mode skip).
func fakeCat(t *testing.T, n int, mode string) (counter string) {
	t.Helper()
	dir := t.TempDir()
	cnt := filepath.Join(dir, "count")
	script := `#!/bin/sh
case "$*" in
  *newroot.pem*)
    n=$(/bin/cat '` + cnt + `' 2>/dev/null || echo 0); n=$((n + 1)); echo $n > '` + cnt + `'
    if [ "$n" = ` + itoa(n) + ` ]; then
      case ` + mode + ` in fail) echo "cat: write error: No space left on device" >&2; exit 1 ;; skip) exit 0 ;; esac
    fi ;;
esac
exec /bin/cat "$@"
`
	write(t, filepath.Join(dir, "cat"), script)
	if err := os.Chmod(filepath.Join(dir, "cat"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return cnt
}

func TestPrepareChecksEveryWriteOfTheBundleRebuild(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		n          int
		want       string
	}{
		{"a write fails in the first bundle", "fail", 1, "cannot rebuild the CA bundles"},
		{"a write fails in a later bundle", "fail", 4, "cannot rebuild the CA bundles"},
		{"the last write fails", "fail", 7, "cannot rebuild the CA bundles"},
		{"a write is silently lost (too few certificates)", "skip", 3, "holds 2 certificates, the original 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			p.legacyPKI(true)
			bin, calls := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
			beforeRoot, beforeServer := sum(t, p.tls("root-ca.pem")), sum(t, p.tls("server-ca.crt"))
			counter := fakeCat(t, tc.n, tc.mode)
			for _, script := range []string{"tn_ca_remediate prepare", "tn_ca_remediate all", "tn_k3s_ca_setup"} {
				os.Remove(counter) // the Nth call of every run fails
				out, err := p.sh(script, "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=touch "+filepath.Join(p.dir, "restarted"))
				if err == nil {
					t.Fatalf("%s went on:\n%s", script, out)
				}
				if !strings.Contains(out, tc.want) {
					t.Errorf("%s: lacks %q:\n%s", script, tc.want, out)
				}
				if script != "tn_k3s_ca_setup" && !strings.Contains(out, "were not changed") {
					t.Errorf("%s: does not say that the CA and k3s were not changed:\n%s", script, out)
				}
			}
			if sum(t, p.tls("root-ca.pem")) != beforeRoot || sum(t, p.tls("server-ca.crt")) != beforeServer {
				t.Error("server/tls changed")
			}
			if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*")); len(m) != 0 {
				t.Errorf("staging left behind (it holds keys): %v", m)
			}
			if m, _ := filepath.Glob(filepath.Join(p.data, "server", "tls-backup-tn-*.tar")); len(m) != 0 {
				t.Errorf("a backup was made for a rotation that never started: %v", m)
			}
			if _, err := os.Stat(calls); err == nil {
				t.Errorf("rotate-ca was called:\n%s", readLog(calls))
			}
			if _, err := os.Stat(filepath.Join(p.dir, "restarted")); err == nil {
				t.Error("k3s was restarted")
			}
		})
	}
}

func TestPrepareRefusesABundleThatDoesNotEndWithTheOldRoot(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	// a root-ca.crt with something after the root: the rebuilt one would not end with the new root
	b, _ := os.ReadFile(p.tls("root-ca.crt"))
	i, _ := os.ReadFile(p.tls("intermediate-ca.pem"))
	if err := os.WriteFile(p.tls("root-ca.crt"), append(b, i...), 0o640); err != nil {
		t.Fatal(err)
	}
	bin, calls := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	out, err := p.sh("tn_ca_remediate prepare", "K3S_BIN="+bin)
	if err == nil || !strings.Contains(out, "is not the new root") {
		t.Fatalf("err=%v\n%s", err, out)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("rotate-ca was called")
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*")); len(m) != 0 {
		t.Errorf("staging left behind: %v", m)
	}
}

func TestPrepareStillRebuildsEveryBundleOfARealDevice(t *testing.T) {
	// the happy path, with the strict checks on: all seven bundles end with the new root and keep their length
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(true)
	lens := map[string]int{}
	for _, b := range []string{"server-ca.crt", "client-ca.crt", "request-header-ca.crt", "etcd/peer-ca.crt", "etcd/server-ca.crt", "intermediate-ca.crt", "root-ca.crt"} {
		lens[b] = len(readCerts(t, p.tls(b)))
	}
	bin, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	p.mustSh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=true")
	p.assertGoodPKI(orig)
	for b, n := range lens {
		certs := readCerts(t, p.tls(b))
		if len(certs) != n {
			t.Errorf("%s: %d certificates, was %d", b, len(certs), n)
		}
		if certs[len(certs)-1].KeyUsage == 0 {
			t.Errorf("%s: the last certificate has no key usage", b)
		}
	}
}

// ---------------------------------------------------------------- leftovers of a regeneration

func TestLeftoverRegenerationDirsAreCleanedWhenTheCAIsOk(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.mustSh("tn_k3s_ca_setup") // never started: a valid PKI, no token yet
	leftovers := func() []string {
		return []string{
			filepath.Join(p.data, "server", ".tn-tls-old.20200101T000000.4242"),
			filepath.Join(p.data, "server", ".tn-tls-new.AbCdEf"),
			filepath.Join(p.data, "server", ".tn-ca-rotate-20200101T000000"),
		}
	}
	plant := func() {
		for _, d := range leftovers() {
			write(t, filepath.Join(d, "root-ca.key"), "not a key")
		}
	}
	gone := func(name string) {
		t.Helper()
		for _, d := range leftovers() {
			if _, err := os.Stat(d); err == nil {
				t.Errorf("%s: %s was not removed", name, filepath.Base(d))
			}
		}
	}
	// the PKI that exists and is valid, k3s has not started yet
	plant()
	out := p.mustSh("tn_k3s_ca_setup")
	gone("setup, valid PKI, not started")
	if !strings.Contains(out, "Removed the leftover staging dir") {
		t.Errorf("no message:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(p.data, "server", "token"), []byte("K10x::server:y"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"tn_k3s_ca_setup", "tn_ca_remediate prepare", "tn_ca_remediate all"} {
		plant()
		p.mustSh(script)
		gone(script)
	}
	// check is read-only
	plant()
	p.check()
	for _, d := range leftovers() {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("check removed %s", d)
		}
	}
	// a device that still needs its CA fixed keeps them until it is fine (they may be the only copy of
	// an interrupted run)
	q := newPKI(t)
	q.legacyCert()
	q.legacyPKI(true)
	old := filepath.Join(q.data, "server", ".tn-tls-old.20200101T000000.4242")
	write(t, filepath.Join(old, "root-ca.key"), "not a key")
	badBin, _ := fakeK3s(t, t.TempDir(), false)
	if _, err := q.sh("tn_ca_remediate all", "K3S_BIN="+badBin); err == nil {
		t.Fatal("rotate-ca was meant to fail")
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("the leftover was removed while the CA was not repaired")
	}
}

// ---------------------------------------------------------------- bounded restart

func TestARestartThatHangsIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(t *testing.T) []string
	}{
		{"systemctl restart", func(t *testing.T) []string {
			fakeSystemctl(t, "activating", 20)
			return []string{"TN_K3S_READY_GRACE=1", "TN_K3S_READY_POLL=1"}
		}},
		{"TN_K3S_RESTART_CMD", func(t *testing.T) []string { return []string{"TN_K3S_RESTART_CMD=sleep 20"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
			p.legacyPKI(true)
			write(t, p.unit, unitText())
			bin, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
			env := append(tc.env(t), "K3S_BIN="+bin, "TN_K3S_SERVICE_TIMEOUT=1")
			start := time.Now()
			out, err := p.sh("tn_ca_remediate all", env...)
			if d := time.Since(start); d > 12*time.Second {
				t.Errorf("took %s: the restart is not bounded", d)
			}
			if exitCode(err) != 5 {
				t.Fatalf("rc=%d\n%s", exitCode(err), out)
			}
			if tc.name == "systemctl restart" && !strings.Contains(out, "was not active") {
				t.Errorf("the message does not say that the unit was watched:\n%s", out)
			}
			for _, want := range []string{"did not finish within 1s", "datastore of k3s holds the new CA", "could not be restarted", "Do not roll back"} {
				if !strings.Contains(out, want) {
					t.Errorf("message lacks %q:\n%s", want, out)
				}
			}
			p.assertGoodPKI(orig) // the files were finalized before the restart
		})
	}
}

func TestAStartThatHangsAfterTheRegenerationIsReported(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(false)
	start := time.Now()
	out := p.mustSh("tn_ca_remediate prepare", "TN_K3S_STOP_CMD=true", "TN_K3S_START_CMD=sleep 20", "TN_K3S_SERVICE_TIMEOUT=1")
	if d := time.Since(start); d > 12*time.Second {
		t.Errorf("took %s: the start is not bounded", d)
	}
	if !strings.Contains(out, "the new CA is installed, but k3s could not be started") {
		t.Errorf("message:\n%s", out)
	}
	p.assertGoodPKI(orig)
}

func TestARestartThatWorksIsNotReportedAsAFailure(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	write(t, p.unit, unitText())
	log := fakeSystemctl(t, "active", 0)
	bin, _ := fakeK3sVersion(t, t.TempDir(), "v1.34.11+k3s1", true)
	out := p.mustSh("tn_ca_remediate all", "K3S_BIN="+bin)
	if !strings.Contains(readLog(log), "restart k3s.service") || strings.Contains(out, "did not finish") {
		t.Errorf("log:\n%s\nout:\n%s", readLog(log), out)
	}
}
