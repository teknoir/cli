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
	"strconv"
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
	// k3sCfg is the TN_K3S_CONFIG_DIR (/etc/rancher/k3s), unit the TN_K3S_UNIT_FILE (k3s.service);
	// neither exists until a test writes it
	k3sCfg string
	unit   string
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
	// never read the real /etc/rancher/k3s or the systemd units of the machine running the tests
	p.k3sCfg = filepath.Join(p.dir, "rancher-k3s")
	p.unit = filepath.Join(p.dir, "k3s.service")
	t.Setenv("TN_K3S_CONFIG_DIR", p.k3sCfg)
	t.Setenv("TN_K3S_UNIT_FILE", p.unit)
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

// ---------------------------------------------------------------- re-review fixes

// failingOpenssl puts a wrapper first in PATH that runs the real openssl, except that the
// failN-th call of subcommand sub (counting from 1; the library's own capability probe
// `ecparam ... -out /dev/null` is not counted) exits 1 without doing anything.
func failingOpenssl(t *testing.T, sub string, failN int) {
	t.Helper()
	real, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in *" -out /dev/null"*) exec ` + real + ` "$@" ;; esac
if [ "$1" = "` + sub + `" ]; then
  n=$(cat "` + dir + `/n" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "` + dir + `/n"
  if [ "$n" -ge ` + strconv.Itoa(failN) + ` ]; then echo "wrapper: failing openssl $1 (call $n)" >&2; exit 1; fi
fi
exec ` + real + ` "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "openssl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// A failing openssl step must fail the setup (the first bug: the status of the PKI subshell came
// from its last command, `rm -rf .ca`, because errexit is ignored in an `if`/`||` context), and
// must not leave a half PKI behind or change an existing one.
func TestSetupFailsWhenAnOpensslStepFails(t *testing.T) {
	for _, tc := range []struct {
		sub string
		n   int
	}{{"genrsa", 1}, {"genrsa", 2}, {"req", 1}, {"req", 3}, {"ca", 1}, {"ca", 2}, {"ca", 6}, {"ecparam", 1}, {"ecparam", 5}} {
		t.Run(tc.sub+"-"+strconv.Itoa(tc.n), func(t *testing.T) {
			p := newPKI(t)
			p.legacyCert()
			failingOpenssl(t, tc.sub, tc.n)
			out, err := p.sh("tn_k3s_ca_setup")
			if err == nil {
				t.Fatalf("setup succeeded although openssl %s failed:\n%s", tc.sub, out)
			}
			if !strings.Contains(out, "Cannot generate the k3s CA") && !strings.Contains(out, "PKI generation failed") {
				t.Errorf("no error message:\n%s", out)
			}
			if _, err := os.Stat(p.tls("server-ca.crt")); err == nil {
				t.Error("a (partial) server/tls was installed")
			}
			if m, _ := filepath.Glob(filepath.Join(p.data, "server", ".tn-tls-new.*")); len(m) != 0 {
				t.Errorf("work dir left behind: %v", m)
			}
		})
	}
}

// the same failure while replacing the PKI of a hung fresh bootstrap: the old tls dir stays
func TestRemediateKeepsTheOldPKIWhenGenerationFails(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.legacyPKI(false)
	before := sum(t, p.tls("server-ca.crt"))
	failingOpenssl(t, "ca", 2)
	if out, err := p.sh("tn_ca_remediate prepare"); err == nil {
		t.Fatalf("prepare succeeded although openssl ca failed:\n%s", out)
	}
	if sum(t, p.tls("server-ca.crt")) != before {
		t.Error("server/tls was replaced")
	}
	if m, _ := filepath.Glob(filepath.Join(p.data, "server", "tls-backup-tn-*.tar")); len(m) != 0 {
		t.Errorf("backup made although nothing was replaced: %v", m)
	}
}

// tn_ca_validate_pki is the second line of defence: it catches what the generator missed
func TestValidatePKI(t *testing.T) {
	p := newPKI(t)
	p.legacyCert()
	p.mustSh("tn_k3s_ca_setup")
	p.mustSh("tn_ca_openssl; tn_ca_validate_pki " + filepath.Join(p.data, "server", "tls"))
	for _, tc := range []struct{ name, cmd string }{
		{"missing leaf", "rm etcd/peer-ca.pem"},
		{"empty bundle", ": > server-ca.crt"},
		{"root only bundle", "cat root-ca.pem > client-ca.crt"},
		{"foreign key", "cp intermediate-ca.key server-ca.key"},
		{"foreign leaf", "cp client-ca.pem server-ca.pem"},
	} {
		d := t.TempDir()
		if out, err := exec.Command("cp", "-a", filepath.Join(p.data, "server", "tls")+"/.", d).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		if out, err := exec.Command("sh", "-c", "cd "+d+" && "+tc.cmd).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", tc.name, err, out)
		}
		if out, err := p.sh("tn_ca_openssl; tn_ca_validate_pki " + d); err == nil {
			t.Errorf("%s: validation passed:\n%s", tc.name, out)
		}
	}
}

// ---- foreign CAs on an initialised data dir

// foreignPKI: initialised legacy PKI whose root key is gone and whose device key is another one
func (p *pki) foreignPKI() {
	p.t.Helper()
	p.legacyPKI(true)
	if err := os.Remove(p.tls("root-ca.key")); err != nil {
		p.t.Fatal(err)
	}
	other := newPKI(p.t)
	other.legacyCert()
	p.cfg = other.cfg
}

// A root that k3s refuses and that we cannot repair must stop the bootstrap: the installer
// that runs next would upgrade k3s and brick the device.
func TestSetupStopsOnAnUnusableForeignRoot(t *testing.T) {
	p := newPKI(t)
	p.legacyCert() // no keyCertSign
	p.foreignPKI()
	before := sum(t, p.tls("server-ca.crt"))
	if rc, o := p.check(); rc != 4 {
		t.Fatalf("check rc=%d %s", rc, o)
	}
	out, err := p.sh("tn_k3s_ca_setup")
	if err == nil {
		t.Fatalf("setup returned 0 on a foreign unusable root:\n%s", out)
	}
	for _, want := range []string{"not the Teknoir device CA", "must not be upgraded"} {
		if !strings.Contains(out, want) {
			t.Errorf("message lacks %q:\n%s", want, out)
		}
	}
	if sum(t, p.tls("server-ca.crt")) != before {
		t.Error("the foreign CA was changed")
	}
	// the whole rendered server script stops before the installer
	var buf bytes.Buffer
	if err := ServerTemplate.Execute(&buf, TemplateData{Domain: "test.cloud", DeviceID: "dev-1", K3SToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\ntn_k3s_ca_setup\n") {
		t.Error("the template does not call tn_k3s_ca_setup as a plain command (set -e must stop on its exit)")
	}
}

func TestSetupStopsOnAnExpiredForeignRoot(t *testing.T) {
	p := newPKI(t)
	p.legacyCertArgs("-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign", "-not_before", "20240301000000Z", "-not_after", "20250301000000Z")
	p.foreignPKI()
	if rc, o := p.check(); rc != 4 {
		t.Fatalf("check rc=%d %s", rc, o)
	}
	out, err := p.sh("tn_k3s_ca_setup")
	if err == nil || !strings.Contains(out, "must not be upgraded") {
		t.Errorf("setup on an expired foreign root: %v\n%s", err, out)
	}
}

// only "expires soon", with keyCertSign: k3s accepts it today, so the bootstrap goes on
func TestSetupContinuesOnAForeignRootThatOnlyExpiresSoon(t *testing.T) {
	p := newPKI(t)
	p.legacyCertArgs("-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign", "-days", "30")
	if c := readCerts(t, filepath.Join(p.cfg, "rsa_public.pem"))[0]; time.Until(c.NotAfter) > 40*24*time.Hour {
		t.Fatalf("test certificate is valid until %v", c.NotAfter)
	}
	p.foreignPKI()
	before := sum(t, p.tls("server-ca.crt"))
	if rc, o := p.check(); rc != 4 || !strings.Contains(o, "expires within 90 days") {
		t.Fatalf("check rc=%d %s", rc, o)
	}
	out := p.mustSh("tn_k3s_ca_setup")
	if !strings.Contains(out, "expires within 90 days") {
		t.Errorf("no warning about the expiry:\n%s", out)
	}
	if sum(t, p.tls("server-ca.crt")) != before {
		t.Error("the foreign CA was changed")
	}
}

// ---- serial of the re-issued root

func serialOf(t *testing.T, path string) string {
	t.Helper()
	return readCerts(t, path)[0].SerialNumber.Text(16)
}

// The re-issued root must not share issuer and serial with the original certificate (RFC 5280
// 4.1.2.2; NSS: SEC_ERROR_REUSED_ISSUER_AND_SERIAL). OpenSSL 1.1.1 ignores -set_serial in
// `x509 -signkey`, which the library works around; the compat suite runs this on 1.1.1.
func TestReissuedRootHasANewSerial(t *testing.T) {
	needTool(t, "sh")
	needTool(t, "openssl")
	for _, kind := range []string{"backstage", "device-controller"} {
		t.Run(kind, func(t *testing.T) {
			p := newPKI(t)
			if kind == "backstage" {
				p.legacyCert()
			} else {
				p.legacyCertArgs("-addext", "keyUsage=critical,digitalSignature,keyEncipherment", "-set_serial", "1")
			}
			orig := serialOf(t, filepath.Join(p.cfg, "rsa_public.pem"))
			p.mustSh("tn_k3s_ca_setup")
			if got := serialOf(t, p.tls("root-ca.pem")); got == orig || got == "1" {
				t.Errorf("new root serial %s, original %s", got, orig)
			}
			// the remediation path re-issues too
			q := newPKI(t)
			q.legacyCert()
			q.legacyPKI(true)
			bin, _ := fakeK3s(t, t.TempDir(), true)
			qorig := serialOf(t, filepath.Join(q.cfg, "rsa_public.pem"))
			q.mustSh("tn_ca_remediate all", "K3S_BIN="+bin, "TN_K3S_RESTART_CMD=true")
			if got := serialOf(t, q.tls("root-ca.pem")); got == qorig {
				t.Errorf("remediated root keeps the original serial %s", got)
			}
		})
	}
}
