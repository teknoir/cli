package k3sca

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// All tests here are offline: they syntax-check the shell and unit-test the
// pure shell functions of the harness (version maths, expectations, results).

func need(t *testing.T, bin string) string {
	t.Helper()
	p, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("%s not installed", bin)
	}
	return p
}

func TestShellSyntax(t *testing.T) {
	bash := need(t, "bash")
	sh := need(t, "sh")
	bashFiles, _ := filepath.Glob("lib/*.sh")
	bashFiles = append(bashFiles, "run.sh")
	for _, f := range bashFiles {
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("bash -n %s: %v\n%s", f, err, out)
		}
	}
	// these run on the devices / in the distro containers: POSIX sh only
	for _, f := range []string{"legacy-ca.sh", "proper-ca.sh", "shim.sh", "compat/assert.sh"} {
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", f, err, out)
		}
	}
}

// legacy-ca.sh is the CONTROL: a frozen copy of the pre-fix template block.
func TestLegacyBlockIsFrozen(t *testing.T) {
	const want = "ac2ebe7ad13d4b6f09c79d7b8e05561769ae838bb69f2b968e77a116b73154ed"
	b, err := os.ReadFile("legacy-ca.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "# ---- begin:")
	j := strings.Index(s, "    # ---- end:")
	if i < 0 || j < 0 {
		t.Fatal("markers missing")
	}
	blk := s[strings.Index(s[i:], "\n")+i+1 : j]
	sum := sha256.Sum256([]byte(blk))
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("legacy block changed (sha256 %s); it must stay a verbatim copy of the old server.sh.tmpl CA block", got)
	}
	// the defining bug of the block
	if !strings.Contains(blk, "cp ${CONFIG_PATH}/rsa_public.pem ${DATA_DIR}/server/tls/root-ca.pem") {
		t.Error("block no longer copies rsa_public.pem verbatim to root-ca.pem")
	}
}

// bashEval runs snippet with lib/common.sh sourced and returns trimmed stdout.
func bashEval(t *testing.T, snippet string) string {
	t.Helper()
	bash := need(t, "bash")
	cmd := exec.Command(bash, "-c", "set -u; . lib/common.sh; "+snippet)
	cmd.Env = append(os.Environ(), "OUT="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", snippet, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestVersionHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"v1.34.11":         "v1.34.11-k3s1",
		"1.36.5":           "v1.36.5-k3s1",
		"v1.36.5+k3s1":     "v1.36.5-k3s1",
		"v1.36.5-k3s1":     "v1.36.5-k3s1",
		"v1.35.9+k3s2":     "v1.35.9-k3s2",
		"v1.37.1-rc1+k3s1": "v1.37.1-rc1-k3s1",
	} {
		if got := bashEval(t, `ver_tag `+in); got != want {
			t.Errorf("ver_tag %s = %s, want %s", in, got, want)
		}
	}
	if got := bashEval(t, `ver_slug v1.34.11-k3s1`); got != "1-34-11" {
		t.Errorf("ver_slug = %s", got)
	}
}

func TestPostfixTable(t *testing.T) {
	post := []string{"v1.34.12", "v1.34.13", "v1.35.9", "v1.36.5", "v1.36.6", "v1.37.1", "v1.38.0", "v1.34.12-k3s1"}
	pre := []string{"v1.34.11", "v1.34.5", "v1.35.8", "v1.35.5", "v1.36.4", "v1.37.0"}
	for _, v := range post {
		if got := bashEval(t, `ver_is_postfix `+v+`; echo $?`); got != "0" {
			t.Errorf("%s should be post-fix (rc=%s)", v, got)
		}
	}
	for _, v := range pre {
		if got := bashEval(t, `ver_is_postfix `+v+`; echo $?`); got != "1" {
			t.Errorf("%s should be pre-fix (rc=%s)", v, got)
		}
	}
	if got := bashEval(t, `ver_is_postfix v1.33.14; echo $?`); got != "2" {
		t.Errorf("v1.33 should be unknown (rc=%s)", got)
	}
	for in, want := range map[string]string{"v1.34.12": "v1.34.11", "v1.35.9": "v1.35.8", "v1.36.5": "v1.36.4", "v1.37.1": "v1.37.0"} {
		if got := bashEval(t, `ver_prefix_of `+in); got != want {
			t.Errorf("ver_prefix_of %s = %s, want %s", in, got, want)
		}
	}
}

func TestExpectedOutcome(t *testing.T) {
	cases := []struct{ ca, gen, ver, want string }{
		{"legacy", "bs-o111", "v1.34.11-k3s1", "READY"},
		{"legacy", "bs-o111", "v1.34.12-k3s1", "FAIL"},
		{"legacy", "go-sha1", "v1.36.5-k3s1", "FAIL"},
		{"legacy", "expired", "v1.37.1-k3s1", "FAIL"},
		{"legacy", "fixed-bs", "v1.36.5-k3s1", "READY"}, // C alone rescues new devices
		{"legacy", "fixed-go", "v1.37.1-k3s1", "READY"},
		{"proper", "bs-o3", "v1.36.5-k3s1", "READY"},
		{"proper", "go-sha256", "v1.34.11-k3s1", "READY"},
		{"lib", "expired", "v1.36.5-k3s1", "READY"},
		{"legacy", "bs-o111", "v1.33.5-k3s1", "ANY"},
	}
	for _, c := range cases {
		if got := bashEval(t, "expected_outcome "+c.ca+" "+c.gen+" "+c.ver); got != c.want {
			t.Errorf("expected_outcome %s %s %s = %s, want %s", c.ca, c.gen, c.ver, got, c.want)
		}
	}
	for _, c := range [][3]string{{"READY", "READY", "PASS"}, {"FAIL", "READY", "MISMATCH"}, {"ANY", "READY", "INFO"}} {
		if got := bashEval(t, "verdict_for "+c[0]+" "+c[1]); got != c[2] {
			t.Errorf("verdict_for %s %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestResolveVersionsDeduplicates(t *testing.T) {
	got := bashEval(t, `resolve_versions v1.36.5 1.36.5 v1.36.5+k3s1 v1.34.11 | tr '\n' ' '`)
	if got != "v1.36.5-k3s1 v1.34.11-k3s1" {
		t.Errorf("got %q", got)
	}
}

func TestResultsMerge(t *testing.T) {
	got := bashEval(t, `
mkdir -p "$OUT/results.d"
result_write control c1 v1.34.11-k3s1 bs-o111 legacy READY READY 9 "k3sverify=reject"
result_write control c2 v1.36.5-k3s1 bs-o111 legacy FAIL READY 9 "should mismatch"
result_write control c3 - - lib READY SKIP 0 "no lib"
result_write control c4 - - lib READY ERROR 0 "tab	and
newline"
results_merge
cat "$OUT/results.tsv"`)
	lines := strings.Split(got, "\n")
	// the log line of result_write goes to stderr and is merged by CombinedOutput; keep TSV rows only
	var rows []string
	for _, l := range lines {
		if strings.Count(l, "\t") == 9 {
			rows = append(rows, l)
		}
	}
	if len(rows) != 5 { // header + 4
		t.Fatalf("rows = %d:\n%s", len(rows), got)
	}
	verdicts := map[string]string{}
	for _, r := range rows[1:] {
		f := strings.Split(r, "\t")
		verdicts[f[1]] = f[7]
	}
	want := map[string]string{"c1": "PASS", "c2": "MISMATCH", "c3": "SKIP", "c4": "ERROR"}
	for k, v := range want {
		if verdicts[k] != v {
			t.Errorf("verdict %s = %s, want %s", k, verdicts[k], v)
		}
	}
}

// --list must work without docker or network: it only prints the plan.
func TestListPlan(t *testing.T) {
	bash := need(t, "bash")
	cmd := exec.Command(bash, "run.sh", "--list", "--suite", "control,legacy-with-cert", "--versions", "v1.34.11,v1.36.5", "--gens", "bs-o111", "--out", t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run.sh --list: %v\n%s", err, out)
	}
	s := string(out)
	for _, w := range []string{"ctl-bs-o111-legacy-1-34-11", "ctl-bs-o111-proper-1-36-5", "lwc-bs-o111-legacy-1-36-5"} {
		if !strings.Contains(s, w) {
			t.Errorf("plan lacks %s:\n%s", w, s)
		}
	}
}
