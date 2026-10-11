package templates

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Offline tests of scripts/tn-k3s-ca.sh (setup, check, remediate) with the local openssl and a
// fake k3s. The docker harness in test/k3s-ca runs the same library against real k3s releases.

const libPath = "scripts/tn-k3s-ca.sh"

func needTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
}

func TestK3sCALibraryIsPlainShell(t *testing.T) {
	needTool(t, "sh")
	b, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("{{")) {
		t.Fatal("the library is embedded as a template: it must not contain '{{'")
	}
	if out, err := exec.Command("sh", "-n", libPath).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	if string(b) != TnK3sCAScript {
		t.Error("embedded TnK3sCAScript differs from the file")
	}
	// the dangers of a file that is pasted into a bootstrap script
	s := string(b)
	for _, bad := range []string{"\ntrap ", "\n  trap ", "set +o noglob", "set -x"} {
		if strings.Contains(s, bad) {
			t.Errorf("library must not contain %q (it would change the caller's traps/options)", bad)
		}
	}
}

func TestServerTemplateUsesTheLibrary(t *testing.T) {
	var buf bytes.Buffer
	if err := ServerTemplate.Execute(&buf, TemplateData{Domain: "test.cloud", DeviceID: "dev-1", K3SToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"tn_k3s_ca_setup()", "TN_K3S_CA_NO_MAIN=1", "\ntn_k3s_ca_setup\n", "tn_ca_reissue_root()"} {
		if !strings.Contains(out, want) {
			t.Errorf("server script lacks %q", want)
		}
	}
	// the defining bug: the device certificate must not be copied verbatim to root-ca.pem
	re := regexp.MustCompile(`cp\s+\S*rsa_public\.pem\s+\S*root-ca\.pem`)
	if re.MatchString(out) {
		t.Error("server script still copies rsa_public.pem to root-ca.pem")
	}
	if strings.Index(out, "tn_k3s_ca_setup\n") > strings.Index(out, "k3s_installer.sh") {
		t.Error("the CA must be set up before the k3s installer runs")
	}
	// the agent and docker variants do not create a CA
	for name, tpl := range map[string]string{"agent": AgentTemplateStr, "docker": DockerServerTemplateStr} {
		if strings.Contains(tpl, "tn-k3s-ca.sh") {
			t.Errorf("%s template unexpectedly embeds the CA library", name)
		}
	}
	if _, err := exec.LookPath("sh"); err == nil {
		f := filepath.Join(t.TempDir(), "server.sh")
		if err := os.WriteFile(f, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if o, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
			t.Errorf("rendered server script: sh -n: %v\n%s", err, o)
		}
	}
}

// ---------------------------------------------------------------- the PKI

type pki struct {
	t    *testing.T
	dir  string // work dir
	cfg  string // CONFIG_PATH
	data string // DATA_DIR
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	needTool(t, "sh")
	needTool(t, "openssl")
	// every test runs with a failing `info` binary first in PATH, like a Debian/Ubuntu machine
	// with the GNU info reader: nothing may call it
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "info"), []byte("#!/bin/sh\necho GNU-INFO-WAS-CALLED \"$*\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+":"+os.Getenv("PATH"))
	p := &pki{t: t, dir: t.TempDir()}
	p.cfg = filepath.Join(p.dir, "etc")
	p.data = filepath.Join(p.dir, "data")
	for _, d := range []string{p.cfg, p.data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// legacyCert writes the pair Backstage generates (openssl req -x509, no keyUsage).
func (p *pki) legacyCert() { p.legacyCertArgs() }

// legacyCertArgs adds openssl req arguments, e.g. -addext keyUsage=... for the device-controller kind.
func (p *pki) legacyCertArgs(extra ...string) {
	p.t.Helper()
	args := []string{"req", "-x509", "-nodes", "-newkey", "rsa:2048",
		"-keyout", filepath.Join(p.cfg, "rsa_private.pem"), "-out", filepath.Join(p.cfg, "rsa_public.pem"),
		"-days", "365", "-subj", "/C=US/ST=Texas/L=SHouston/O=Teknoir LLC/CN=teknoir.ai", "-sha256", "-batch"}
	cmd := exec.Command("openssl", append(args, extra...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		p.t.Fatalf("openssl req: %v\n%s", err, out)
	}
}

// sh runs script with the bootstrap script shell options and the library sourced.
func (p *pki) sh(script string, env ...string) (string, error) {
	p.t.Helper()
	full := "set -e; set -o noglob; umask 027; " +
		"info() { echo '[INFO] ' \"$@\"; }; warn() { echo '[WARN] ' \"$@\" >&2; }; fatal() { echo '[ERROR] ' \"$@\" >&2; exit 1; }; " +
		". " + libPath + "; " + script
	cmd := exec.Command("sh", "-c", full)
	cmd.Env = append(os.Environ(), "CONFIG_PATH="+p.cfg, "DATA_DIR="+p.data, "PRODUCT=k3s", "_DEVICE_ID=dev1", "_DOMAIN=test.local")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if bytes.Contains(out, []byte("PRIVATE KEY")) {
		p.t.Errorf("a private key was printed:\n%s", out)
	}
	if bytes.Contains(out, []byte("GNU-INFO-WAS-CALLED")) {
		p.t.Errorf("the info binary was called:\n%s", out)
	}
	return string(out), err
}

func (p *pki) mustSh(script string, env ...string) string {
	p.t.Helper()
	out, err := p.sh(script, env...)
	if err != nil {
		p.t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return out
}

// check runs `check` and returns its exit code
func (p *pki) check() (int, string) {
	p.t.Helper()
	out, err := p.sh("tn_ca_check")
	if err == nil {
		return 0, out
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out
	}
	p.t.Fatal(err)
	return -1, ""
}

func (p *pki) tls(name string) string { return filepath.Join(p.data, "server", "tls", name) }

func readCerts(t *testing.T, path string) []*x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var certs []*x509.Certificate
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		t.Fatalf("%s: no certificates", path)
	}
	return certs
}

func sum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// k3sPool builds the trust pool like k3s >= v1.36.5: certificates without keyCertSign are
// dropped from a multi-certificate bundle, roots are the self-issued ones.
func k3sPool(certs []*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, c := range certs {
		if c.KeyUsage&x509.KeyUsageCertSign != 0 && (len(c.AuthorityKeyId) == 0 || bytes.Equal(c.AuthorityKeyId, c.SubjectKeyId)) {
			pool.AddCert(c)
		}
	}
	return pool
}

func (p *pki) assertGoodPKI(orig []byte) {
	t := p.t
	t.Helper()
	oc := readCerts(t, filepath.Join(p.cfg, "rsa_public.pem"))[0]
	root := readCerts(t, p.tls("root-ca.pem"))[0]
	if !root.IsCA || root.KeyUsage&x509.KeyUsageCertSign == 0 || root.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Errorf("root: IsCA=%v KeyUsage=%b", root.IsCA, root.KeyUsage)
	}
	if len(root.ExtKeyUsage) != 0 {
		t.Error("root has extendedKeyUsage")
	}
	if !bytes.Equal(root.RawSubject, oc.RawSubject) || !bytes.Equal(root.RawIssuer, root.RawSubject) {
		t.Error("root subject differs from the device certificate, or is not self-issued")
	}
	if len(oc.SubjectKeyId) > 0 && !bytes.Equal(root.SubjectKeyId, oc.SubjectKeyId) {
		t.Error("root SKI differs from the device certificate")
	}
	if len(root.AuthorityKeyId) > 0 && !bytes.Equal(root.AuthorityKeyId, root.SubjectKeyId) {
		t.Error("root AKI != SKI: k3s would treat it as an intermediate")
	}
	if !root.PublicKey.(interface{ Equal(crypto.PublicKey) bool }).Equal(oc.PublicKey) {
		t.Error("root public key differs from the device certificate")
	}
	if d := time.Until(root.NotAfter); d < 7000*24*time.Hour || d > 7400*24*time.Hour {
		t.Errorf("root valid for %v, want about 7300 days", d)
	}
	seen := map[string]bool{}
	for _, e := range root.Extensions {
		if seen[e.Id.String()] {
			t.Errorf("duplicate extension %s", e.Id)
		}
		seen[e.Id.String()] = true
	}
	for _, b := range []string{"server-ca.crt", "client-ca.crt", "request-header-ca.crt", "etcd/peer-ca.crt", "etcd/server-ca.crt", "intermediate-ca.crt"} {
		certs := readCerts(t, p.tls(b))
		for i, c := range certs {
			if c.KeyUsage&x509.KeyUsageCertSign == 0 {
				t.Errorf("%s cert %d (%s) has no keyCertSign: k3s would prune it", b, i, c.Subject.CommonName)
			}
		}
		inter := x509.NewCertPool()
		for _, c := range certs[1 : len(certs)-1] {
			inter.AddCert(c)
		}
		if _, err := certs[0].Verify(x509.VerifyOptions{Roots: k3sPool(certs), Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			t.Errorf("%s: %v", b, err)
		}
		if last := certs[len(certs)-1]; !bytes.Equal(last.Raw, root.Raw) && b != "root-ca.crt" {
			t.Errorf("%s does not end in the root", b)
		}
	}
	if got := sum(t, filepath.Join(p.cfg, "rsa_public.pem")); got != hashOf(orig) {
		t.Error("rsa_public.pem was modified")
	}
}

func hashOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestSetupCreatesAProperRoot(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	out := p.mustSh("tn_k3s_ca_setup")
	if !strings.Contains(out, "keyCertSign") {
		t.Errorf("no message about the re-issued root:\n%s", out)
	}
	p.assertGoodPKI(orig)
	if rc, o := p.check(); rc != 0 {
		t.Errorf("check rc=%d: %s", rc, o)
	}

	// a second run while k3s never started keeps the PKI
	before := sum(t, p.tls("server-ca.crt"))
	p.mustSh("tn_k3s_ca_setup")
	if sum(t, p.tls("server-ca.crt")) != before {
		t.Error("second setup regenerated a valid PKI")
	}

	// a run on an initialised data dir never touches it
	if err := os.WriteFile(filepath.Join(p.data, "server", "token"), []byte("K10x::server:y"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.mustSh("tn_k3s_ca_setup")
	if sum(t, p.tls("server-ca.crt")) != before {
		t.Error("setup changed the PKI of an initialised data dir")
	}
}

func TestSetupLongDeviceName(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.mustSh("tn_k3s_ca_setup", "_DEVICE_ID=a-device-with-a-really-long-name-for-an-edge-computer", "_DOMAIN=customer-teamspace.teknoir.cloud")
	for _, c := range readCerts(t, p.tls("request-header-ca.crt"))[:1] {
		if n := len(c.Subject.CommonName); n == 0 || n > 64 {
			t.Errorf("CN %q has %d characters", c.Subject.CommonName, n)
		}
	}
}

// the old, broken layout: the device certificate itself as the root
func (p *pki) legacyPKI(initialised bool) {
	p.t.Helper()
	legacy, err := filepath.Abs("../../test/k3s-ca/legacy-ca.sh")
	if err != nil {
		p.t.Fatal(err)
	}
	tls := filepath.Join(p.data, "server", "tls")
	if err := os.MkdirAll(tls, 0o750); err != nil {
		p.t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "set -e; set -o noglob; umask 027; . "+legacy+"; tn_legacy_ca "+tls+" "+p.cfg+"/rsa_public.pem "+p.cfg+"/rsa_private.pem dev1.test.local")
	cmd.Env = append(os.Environ(), "PRODUCT=k3s")
	if out, err := cmd.CombinedOutput(); err != nil {
		p.t.Fatalf("legacy PKI: %v\n%s", err, out)
	}
	if initialised {
		if err := os.WriteFile(filepath.Join(p.data, "server", "token"), []byte("K10x::server:y"), 0o600); err != nil {
			p.t.Fatal(err)
		}
	}
}

func fakeK3s(t *testing.T, dir string, ok bool) (bin, log string) {
	t.Helper()
	log = filepath.Join(dir, "k3s-calls")
	bin = filepath.Join(dir, "fake-k3s")
	rc := "0"
	if !ok {
		rc = "1"
	}
	script := "#!/bin/sh\necho \"SSL_CERT_FILE=$SSL_CERT_FILE $*\" >> '" + log + "'\nexit " + rc + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestRemediateInitialisedLegacyDevice(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(true)
	if rc, o := p.check(); rc != 2 || !strings.Contains(o, "needs-rotation") {
		t.Fatalf("check on a legacy initialised device: rc=%d %s", rc, o)
	}
	oldRoot := sum(t, p.tls("root-ca.pem"))
	if oldRoot != hashOf(orig) {
		t.Fatal("legacy fixture does not copy the device certificate")
	}

	// prepare stages the rotation and changes nothing on disk
	before := sum(t, p.tls("server-ca.crt"))
	p.mustSh("tn_ca_openssl; tn_ca_remediate prepare")
	if sum(t, p.tls("server-ca.crt")) != before || sum(t, p.tls("root-ca.pem")) != oldRoot {
		t.Error("prepare modified server/tls")
	}
	stages, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*"))
	if len(stages) != 1 {
		t.Fatalf("staging dirs: %v", stages)
	}
	rotate, err := os.ReadFile(filepath.Join(stages[0], "ROTATE_CMD"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rotate), "certificate rotate-ca") || strings.Contains(string(rotate), "--force") || strings.Contains(string(rotate), "PRIVATE") {
		t.Errorf("ROTATE_CMD: %s", rotate)
	}
	if fi, _ := os.Stat(stages[0]); fi.Mode().Perm() != 0o700 {
		t.Errorf("staging dir mode %v", fi.Mode().Perm())
	}
	if _, err := filepath.Glob(filepath.Join(p.data, "server", "tls-backup-tn-*.tar")); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", "tls-backup-tn-*.tar")); len(m) != 1 {
		t.Errorf("backups: %v", m)
	}

	// finalize before the rotation happened must refuse
	if out, err := p.sh("tn_ca_openssl; tn_ca_remediate finalize"); err == nil {
		t.Errorf("finalize without a completed rotation succeeded:\n%s", out)
	}
	if sum(t, p.tls("root-ca.pem")) != oldRoot {
		t.Error("finalize without ROTATED changed the root")
	}

	// a failing rotate-ca leaves everything as it was and says what to do
	badBin, _ := fakeK3s(t, t.TempDir(), false)
	if out, err := p.sh("tn_ca_openssl; tn_ca_remediate rotate", "K3S_BIN="+badBin); err == nil || !strings.Contains(out, "v1.36.4") {
		t.Errorf("failed rotate: err=%v\n%s", err, out)
	}
	if sum(t, p.tls("root-ca.pem")) != oldRoot {
		t.Error("failed rotate changed the root")
	}

	// the full run with a fake k3s that "succeeds"
	bin, calls := fakeK3s(t, t.TempDir(), true)
	restarted := filepath.Join(p.dir, "restarted")
	p.mustSh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=touch "+restarted)
	if _, err := os.Stat(restarted); err != nil {
		t.Error("k3s was not restarted")
	}
	c, _ := os.ReadFile(calls)
	if !strings.Contains(string(c), "certificate rotate-ca --data-dir "+p.data+" --path ") || !strings.Contains(string(c), "SSL_CERT_FILE=") {
		t.Errorf("k3s was called with: %s", c)
	}
	p.assertGoodPKI(orig)
	if rc, o := p.check(); rc != 0 {
		t.Errorf("check after remediate rc=%d %s", rc, o)
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*")); len(m) != 0 {
		t.Errorf("staging left behind: %v", m)
	}

	// idempotent: a second remediate and a setup change nothing
	after := sum(t, p.tls("server-ca.crt")) + sum(t, p.tls("root-ca.pem")) + sum(t, p.tls("intermediate-ca.crt"))
	p.mustSh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=false")
	p.mustSh("tn_k3s_ca_setup", "K3S_BIN="+bin)
	if got := sum(t, p.tls("server-ca.crt")) + sum(t, p.tls("root-ca.pem")) + sum(t, p.tls("intermediate-ca.crt")); got != after {
		t.Error("a second remediate/setup changed the PKI")
	}
}

// A root with a keyUsage that lacks keyCertSign (the device-controller kind) cannot vouch for the
// chain in k3s's own check of the new CA bundle, so rotate-ca needs --force there. Only there.
func TestRotateCmdForcesOnlyWhenTheOldRootCannotVouch(t *testing.T) {
	p := newPKI(t)
	p.legacyCertArgs("-addext", "keyUsage=digitalSignature,keyEncipherment")
	p.legacyPKI(true)
	out := p.mustSh("tn_ca_openssl; tn_ca_remediate prepare")
	if !strings.Contains(out, "--force") {
		t.Errorf("no word about --force:\n%s", out)
	}
	stages, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*"))
	if len(stages) != 1 {
		t.Fatalf("staging dirs: %v", stages)
	}
	rotate, _ := os.ReadFile(filepath.Join(stages[0], "ROTATE_CMD"))
	if !strings.Contains(string(rotate), "--path '"+stages[0]+"/server' --force && touch") {
		t.Errorf("ROTATE_CMD: %s", rotate)
	}

	// no keyUsage at all (Backstage kind): k3s can verify, no --force
	q := newPKI(t)
	q.legacyCert()
	q.legacyPKI(true)
	q.mustSh("tn_ca_openssl; tn_ca_remediate prepare")
	stages, _ = filepath.Glob(filepath.Join(q.data, "server", ".tn-ca-rotate-*"))
	rotate, _ = os.ReadFile(filepath.Join(stages[0], "ROTATE_CMD"))
	if strings.Contains(string(rotate), "--force") {
		t.Errorf("unexpected --force: %s", rotate)
	}
}

func TestSetupRepairsAnInitialisedLegacyDevice(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(true)
	bin, calls := fakeK3s(t, t.TempDir(), true)

	// opt out: stops before anything is touched
	before := sum(t, p.tls("root-ca.pem"))
	if out, err := p.sh("tn_k3s_ca_setup", "TN_K3S_CA_AUTO_REMEDIATE=false", "K3S_BIN="+bin); err == nil || !strings.Contains(out, "remediate") {
		t.Errorf("auto-remediate=false: err=%v\n%s", err, out)
	}
	if sum(t, p.tls("root-ca.pem")) != before {
		t.Error("changed despite TN_K3S_CA_AUTO_REMEDIATE=false")
	}

	// default: repaired before the installer would run
	p.mustSh("tn_k3s_ca_setup", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=true")
	if c, _ := os.ReadFile(calls); !strings.Contains(string(c), "rotate-ca") {
		t.Errorf("setup did not rotate: %s", c)
	}
	p.assertGoodPKI(orig)

	// a failing rotation stops the bootstrap (no installer run that would brick the device)
	p2 := newPKI(t)
	p2.legacyCert()
	p2.legacyPKI(true)
	badBin, _ := fakeK3s(t, t.TempDir(), false)
	if _, err := p2.sh("tn_k3s_ca_setup", "K3S_BIN="+badBin, "TN_K3S_RESTART_CMD=true"); err == nil {
		t.Error("setup continued after a failed rotation")
	}
}

func TestRemediateHungFreshBootstrap(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	orig, _ := os.ReadFile(filepath.Join(p.cfg, "rsa_public.pem"))
	p.legacyPKI(false) // old script ran, post-fix k3s never finished its start: no server/token
	if rc, o := p.check(); rc != 3 || !strings.Contains(o, "needs-regeneration") {
		t.Fatalf("check: rc=%d %s", rc, o)
	}
	// no k3s is needed: there is nothing to rotate
	out := p.mustSh("tn_ca_remediate prepare", "K3S_BIN=/nonexistent/k3s")
	if !strings.Contains(out, "Nothing to rotate") {
		t.Errorf("message:\n%s", out)
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-ca-rotate-*")); len(m) != 0 {
		t.Errorf("unexpected staging %v", m)
	}
	p.assertGoodPKI(orig)
	if rc, o := p.check(); rc != 0 {
		t.Errorf("check after: rc=%d %s", rc, o)
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", "tls-backup-tn-*.tar")); len(m) != 1 {
		t.Errorf("the old tls dir was not backed up: %v", m)
	}
	// `remediate` (all phases) on such a device regenerates and restarts, without calling k3s
	p3 := newPKI(t)
	p3.legacyCert()
	orig3, _ := os.ReadFile(filepath.Join(p3.cfg, "rsa_public.pem"))
	p3.legacyPKI(false)
	bin, calls := fakeK3s(t, t.TempDir(), true)
	restarted := filepath.Join(p3.dir, "restarted")
	p3.mustSh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=touch "+restarted)
	if _, err := os.Stat(restarted); err != nil {
		t.Error("k3s was not restarted")
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("k3s was called although there is nothing to rotate")
	}
	p3.assertGoodPKI(orig3)
	// re-running the bootstrap script on such a device is the other way to the same result
	p2 := newPKI(t)
	p2.legacyCert()
	orig2, _ := os.ReadFile(filepath.Join(p2.cfg, "rsa_public.pem"))
	p2.legacyPKI(false)
	p2.mustSh("tn_k3s_ca_setup")
	p2.assertGoodPKI(orig2)
}

func TestCheckStates(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	// an agent / fresh machine: nothing to check
	if rc, o := p.check(); rc != 0 || !strings.Contains(o, "not-a-server") {
		t.Errorf("no tls dir: rc=%d %s", rc, o)
	}
	// a legacy PKI whose key we do not hold (device files of another device)
	p.legacyPKI(true)
	if err := os.Remove(p.tls("root-ca.key")); err != nil {
		t.Fatal(err)
	}
	other := newPKI(t)
	other.legacyCert()
	p.cfg = other.cfg
	if rc, o := p.check(); rc != 4 || !strings.Contains(o, "foreign") {
		t.Errorf("foreign CA: rc=%d %s", rc, o)
	}
	if out, err := p.sh("tn_ca_openssl; tn_ca_remediate prepare"); err == nil {
		t.Errorf("prepare on a foreign CA succeeded:\n%s", out)
	}
}

// On Debian and Ubuntu `info` is the GNU info reader. The library must not depend on a caller's
// info/warn/fatal, which a stand-alone run does not have.
func TestStandAloneDoesNotCallTheInfoBinary(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "info"), []byte("#!/bin/sh\necho GNU-INFO-WAS-CALLED \"$*\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", libPath, "check", "--data-dir", p.data, "--config-dir", p.cfg)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "GNU-INFO-WAS-CALLED") || !strings.Contains(string(out), "needs-rotation") {
		t.Errorf("check output: %s", out)
	}
	// and the setup path that logs with [INFO] lines
	q := newPKI(t)
	q.legacyCert()
	cmd = exec.Command("sh", "-c", "set -e; set -o noglob; umask 027; . "+libPath+"; tn_k3s_ca_setup")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CONFIG_PATH="+q.cfg, "DATA_DIR="+q.data, "_DEVICE_ID=dev1", "_DOMAIN=test.local")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "GNU-INFO-WAS-CALLED") || !strings.Contains(string(out), "[INFO]") {
		t.Errorf("setup without info/warn/fatal in the caller: %v\n%s", err, out)
	}
}

func TestProgramMode(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(true)
	cmd := exec.Command("sh", libPath, "check", "--data-dir", p.data, "--config-dir", p.cfg)
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "needs-rotation") {
		t.Errorf("sh tn-k3s-ca.sh check: %v\n%s", err, out)
	}
	// sourcing with the caller's own arguments must not start the program
	cmd = exec.Command("sh", "-c", ". "+libPath+"; echo sourced")
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "sourced" {
		t.Errorf("sourcing printed: %v %q", err, out)
	}
}
