package templates

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-roles-golden", false, "rewrite the testdata/*.roles.golden.sh files")

// ---------------------------------------------------------------- validation

func TestNodeConfigValidate_Accepts(t *testing.T) {
	ok := []NodeConfig{
		{},
		{Role: "vision"},
		{Role: "io", Robot: "mobile-01"},
		{Robot: "sensor-unit-01", Name: "sensor-1"},
		{Name: "my.node.example"},
		{Role: "vision", Robot: "vision-station-01", Labels: []string{"teknoir.org/gpu=nvidia", "plain=1", "empty=", "a.b-c/e_f.g=h"}, Taints: []string{"nvidia.com/gpu=true:NoSchedule", "dedicated:NoExecute", "x=y:PreferNoSchedule"}},
	}
	for i, n := range ok {
		if err := n.Validate(); err != nil {
			t.Errorf("case %d %+v: %v", i, n, err)
		}
	}
}

func TestNodeConfigValidate_Rejects(t *testing.T) {
	long := strings.Repeat("a", 64)
	bad := map[string]NodeConfig{
		"role upper case":        {Role: "Vision"},
		"role underscore":        {Role: "vi_sion"},
		"role slash":             {Role: "a/b"},
		"role leading dash":      {Role: "-x"},
		"role too long":          {Role: long},
		"role quote":             {Role: "a'b"},
		"robot dot":              {Role: "io", Robot: "mobile.01"},
		"robot without role":     {Robot: "mobile-01"},
		"name upper case":        {Name: "Orin"},
		"name underscore":        {Name: "or_in"},
		"name empty part":        {Name: "a..b"},
		"label no equals":        {Role: "x", Labels: []string{"foo"}},
		"label empty key":        {Labels: []string{"=v"}},
		"label key space":        {Labels: []string{"a b=c"}},
		"label value space":      {Labels: []string{"a=b c"}},
		"label value too long":   {Labels: []string{"a=" + long}},
		"label name too long":    {Labels: []string{long + "=v"}},
		"label prefix upper":     {Labels: []string{"Teknoir.org/x=1"}},
		"label quote":            {Labels: []string{"a=b'c"}},
		"label dollar":           {Labels: []string{"a=$b"}},
		"label kubernetes.io":    {Labels: []string{"kubernetes.io/foo=bar"}},
		"label sub kubernetes":   {Labels: []string{"node-role.kubernetes.io/worker=true"}},
		"label k8s.io":           {Labels: []string{"k8s.io/foo=bar"}},
		"label sub k8s.io":       {Labels: []string{"beta.k8s.io/foo=bar"}},
		"label role dup":         {Role: "io", Labels: []string{LabelNodeRole + "=vision"}},
		"label robot dup":        {Role: "io", Robot: "r", Labels: []string{LabelRobot + "=other"}},
		"label key twice":        {Labels: []string{"a=1", "a=2"}},
		"taint no effect":        {Taints: []string{"a=b"}},
		"taint bad effect":       {Taints: []string{"a=b:Never"}},
		"taint lower effect":     {Taints: []string{"a=b:noschedule"}},
		"taint empty key":        {Taints: []string{"=b:NoSchedule"}},
		"taint kubernetes.io":    {Taints: []string{"node.kubernetes.io/foo:NoSchedule"}},
		"taint value quote":      {Taints: []string{"a=b'c:NoSchedule"}},
		"taint twice":            {Taints: []string{"a=b:NoSchedule", "a=b:NoSchedule"}},
		"taint two colons":       {Taints: []string{"a:b:NoSchedule"}},
		"taint value with space": {Taints: []string{"a=b c:NoSchedule"}},
	}
	for name, n := range bad {
		if err := n.Validate(); err == nil {
			t.Errorf("%s: %+v was accepted", name, n)
		}
	}
}

func TestNodeConfigValidate_ReservedPrefixErrorIsClear(t *testing.T) {
	for _, l := range []string{"kubernetes.io/x=1", "foo.kubernetes.io/x=1", "k8s.io/x=1", "foo.k8s.io/x=1"} {
		err := NodeConfig{Labels: []string{l}}.Validate()
		if err == nil || !strings.Contains(err.Error(), "kubernetes.io/ and k8s.io/") {
			t.Errorf("%s: error %v", l, err)
		}
	}
	// a name that merely ends in the text is not the domain
	if err := (NodeConfig{Labels: []string{"mykubernetes.io/x=1", "notk8s.io/y=1"}}).Validate(); err != nil {
		t.Errorf("a different domain must be accepted: %v", err)
	}
}

func TestNodeConfigDerived(t *testing.T) {
	n := NodeConfig{Role: "vision", Robot: "mobile-01", Labels: []string{"teknoir.org/gpu=nvidia"}}
	if n.EffectiveName() != "mobile-01-vision" {
		t.Errorf("name %q", n.EffectiveName())
	}
	want := []string{"teknoir.org/node-role=vision", "teknoir.org/robot=mobile-01", "teknoir.org/gpu=nvidia"}
	if got := n.AllLabels(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("labels %v", got)
	}
	if (NodeConfig{Role: "io"}).EffectiveName() != "" {
		t.Error("a role without a robot must keep the default node name")
	}
	n.Name = "orin"
	if n.EffectiveName() != "orin" {
		t.Errorf("explicit name %q", n.EffectiveName())
	}
	if (NodeConfig{}).Configured() || !(NodeConfig{Taints: []string{"a:NoSchedule"}}).Configured() {
		t.Error("Configured")
	}
}

func TestValidateHost(t *testing.T) {
	for _, h := range []string{"orin.local", "Orin", "10.0.0.5", "192.168.2.181", "fd00::1", "[fd00::1]", "a-b.c.example.com"} {
		if err := ValidateHost("server", h); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	for _, h := range []string{"", "https://orin", "orin:6443", "orin/path", "o rin", "-orin", "orin_1", "a..b", "user@orin", "orin;rm", "$(x)", "'x'"} {
		if err := ValidateHost("server", h); err == nil {
			t.Errorf("%q was accepted", h)
		}
	}
}

func TestK3SServerURL(t *testing.T) {
	for in, want := range map[string]string{
		"orin.local": "https://orin.local:6443",
		"10.0.0.5":   "https://10.0.0.5:6443",
		"fd00::1":    "https://[fd00::1]:6443",
		"[fd00::1]":  "https://[fd00::1]:6443",
	} {
		if got := K3SServerURL(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestMergeTLSSANs(t *testing.T) {
	got := MergeTLSSANs("orin.local", []string{"10.0.0.5", "Orin.local", "dev-1.test.cloud", "x.example"}, "dev-1.test.cloud", "10.0.0.10")
	if strings.Join(got, ",") != "orin.local,10.0.0.5,x.example" {
		t.Errorf("%v", got)
	}
	if got := MergeTLSSANs("", nil); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestValidateTemplateData_NodeFields(t *testing.T) {
	for name, mut := range map[string]func(*TemplateData){
		"role":   func(d *TemplateData) { d.Node.Role = "a$b" },
		"robot":  func(d *TemplateData) { d.Node.Robot = "a`b" },
		"name":   func(d *TemplateData) { d.Node.Name = "a\"b" },
		"label":  func(d *TemplateData) { d.Node.Labels = []string{"a=b\\c"} },
		"taint":  func(d *TemplateData) { d.Node.Taints = []string{"a=b\n:NoSchedule"} },
		"server": func(d *TemplateData) { d.Server = "a$(x)" },
		"san":    func(d *TemplateData) { d.TLSSANs = []string{"a\"b"} },
	} {
		d := goldenData()
		mut(&d)
		if err := ValidateTemplateData(d); err == nil {
			t.Errorf("%s: unsafe value accepted", name)
		}
	}
}

// ---------------------------------------------------------------- rendering

func serverRolesData() TemplateData {
	d := goldenData()
	d.Node = NodeConfig{Role: "vision", Robot: "mobile-01", Labels: []string{"teknoir.org/gpu=nvidia"}, Taints: []string{"nvidia.com/gpu=true:NoSchedule"}}
	d.Server = "orin.example.test"
	d.TLSSANs = MergeTLSSANs("orin.example.test", []string{"10.0.0.5"}, "golden-device.example.test", "10.0.0.10")
	return d
}

func agentRolesData() TemplateData {
	d := goldenData()
	d.Node = NodeConfig{Role: "io", Robot: "mobile-01", Labels: []string{"teknoir.org/io=cameras"}}
	d.Server = "orin.example.test"
	return d
}

func TestGolden_Roles(t *testing.T) {
	for name, tc := range map[string]struct {
		render func() ([]byte, error)
	}{
		"server": {func() ([]byte, error) {
			var b bytes.Buffer
			err := ServerTemplate.Execute(&b, serverRolesData())
			return b.Bytes(), err
		}},
		"agent": {func() ([]byte, error) {
			var b bytes.Buffer
			err := AgentTemplate.Execute(&b, agentRolesData())
			return b.Bytes(), err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.render()
			if err != nil {
				t.Fatal(err)
			}
			path := "testdata/" + name + ".roles.golden.sh"
			if *updateGolden {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s differs from %s (got %d bytes, want %d); go test -run Golden_Roles -update-roles-golden rewrites it", name, path, len(got), len(want))
			}
		})
	}
}

// Without any node flag the scripts do not mention the node library at all (the golden files prove the
// bytes; this proves the reason).
func TestNoNodeFlagsNoNodeCode(t *testing.T) {
	for name, out := range map[string]string{"server": renderK3S(t, ServerTemplate, ""), "agent": renderK3S(t, AgentTemplate, "")} {
		for _, s := range []string{"tn_node_", "tn-k3s-node", "teknoir.org/node-role", "teknoir.org/robot", "98-teknoir-node"} {
			if strings.Contains(out, s) {
				t.Errorf("%s script without node flags contains %q", name, s)
			}
		}
	}
	if !strings.Contains(renderK3S(t, ServerTemplate, ""), `node-name: teknoir-master`) {
		t.Error("the server default node name is gone")
	}
}

func TestRolesRendering_Server(t *testing.T) {
	var b bytes.Buffer
	if err := ServerTemplate.Execute(&b, serverRolesData()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"tn_node_name 'mobile-01-vision'\n",
		"tn_node_label 'teknoir.org/node-role=vision'\n",
		"tn_node_label 'teknoir.org/robot=mobile-01'\n",
		"tn_node_label 'teknoir.org/gpu=nvidia'\n",
		"tn_node_taint 'nvidia.com/gpu=true:NoSchedule'\n",
		"tn_node_apply server\n",
		"--tls-san 10.0.0.10 --tls-san orin.example.test --tls-san 10.0.0.5${INSTALL_K3S_EXEC",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("server script lacks %q", want)
		}
	}
	// the role name replaces the default, it is not written next to it
	if strings.Contains(out, `"node-name: teknoir-master"`) {
		t.Error("the default node name is still written")
	}
	// the labels are written before the k3s installer runs
	if a, i := strings.Index(out, "tn_node_apply server"), strings.Index(out, "$SUDO ./k3s_installer.sh"); a < 0 || i < a {
		t.Errorf("tn_node_apply (%d) must come before the installer (%d)", a, i)
	}
}

func TestRolesRendering_ServerKeepsDefaultNameWithoutRobot(t *testing.T) {
	d := goldenData()
	d.Node = NodeConfig{Role: "vision"}
	var b bytes.Buffer
	if err := ServerTemplate.Execute(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `"node-name: teknoir-master"`) || strings.Contains(out, "tn_node_name '") {
		t.Error("a role without a robot must keep the default server node name")
	}
	if !strings.Contains(out, "tn_node_apply server") || !strings.Contains(out, "tn_node_label 'teknoir.org/node-role=vision'") {
		t.Error("the role label is missing")
	}
}

func TestRolesRendering_Agent(t *testing.T) {
	var b bytes.Buffer
	if err := AgentTemplate.Execute(&b, agentRolesData()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"tn_node_name 'mobile-01-io'\n",
		"tn_node_label 'teknoir.org/node-role=io'\n",
		"tn_node_label 'teknoir.org/io=cameras'\n",
		`K3S_URL="${K3S_URL:-https://orin.example.test:6443}"`,
		"tn_node_apply agent\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agent script lacks %q", want)
		}
	}
	if strings.Contains(out, "tn_node_taint '") {
		t.Error("no taint was asked for")
	}
	// K3S_URL is set before the question, and the labels are written after the release check and
	// before the installer
	u, q := strings.Index(out, `K3S_URL="${K3S_URL:-`), strings.Index(out, `if [ -z "${K3S_URL}" ]`)
	p, a, i := strings.Index(out, "\ntn_inst_prepare agent\n"), strings.Index(out, "tn_node_apply agent"), strings.Index(out, "$SUDO ./k3s_installer.sh")
	if !(u > 0 && u < q && p > 0 && p < a && a < i) {
		t.Errorf("order: url %d question %d prepare %d apply %d installer %d", u, q, p, a, i)
	}
}

// --server on its own: an agent without a role still joins the right server
func TestRolesRendering_AgentServerOnly(t *testing.T) {
	d := goldenData()
	d.Server = "fd00::7"
	var b bytes.Buffer
	if err := AgentTemplate.Execute(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `K3S_URL="${K3S_URL:-https://[fd00::7]:6443}"`) || strings.Contains(out, "tn_node_") {
		t.Errorf("unexpected agent script")
	}
}

// the rendered K3S_URL line: the environment wins
func TestAgentServerIsOverridableByEnvironment(t *testing.T) {
	needTool(t, "sh")
	for env, want := range map[string]string{"": "https://orin.example.test:6443", "K3S_URL=https://other:6443": "https://other:6443"} {
		cmd := exec.Command("sh", "-c", `K3S_URL="${K3S_URL:-https://orin.example.test:6443}"; echo "$K3S_URL"`)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		if env != "" {
			cmd.Env = append(cmd.Env, env)
		}
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("%q: %q %v", env, out, err)
		}
	}
	// and the line is the one in the template
	var b bytes.Buffer
	if err := AgentTemplate.Execute(&b, agentRolesData()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `K3S_URL="${K3S_URL:-https://orin.example.test:6443}"`) {
		t.Error("the template line differs from the one tested")
	}
}

// ---------------------------------------------------------------- the shell library

func TestNodeLibraryIsPlainShell(t *testing.T) {
	needTool(t, "sh")
	b, err := os.ReadFile("scripts/tn-k3s-node.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("{{")) {
		t.Fatal("the library is embedded as a template: it must not contain '{{'")
	}
	if out, err := exec.Command("sh", "-n", "scripts/tn-k3s-node.sh").CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	if string(b) != TnK3sNodeScript {
		t.Error("embedded TnK3sNodeScript differs from the file")
	}
	for _, bad := range []string{"\ntrap ", "set +o noglob", "set -x"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("library must not contain %q", bad)
		}
	}
}

type nodeEnv struct {
	t   *testing.T
	dir string
	cfg string
}

func newNodeEnv(t *testing.T) *nodeEnv {
	t.Helper()
	needTool(t, "sh")
	d := t.TempDir()
	return &nodeEnv{t: t, dir: d, cfg: filepath.Join(d, "rancher-k3s")}
}

// run sources both libraries under the options of the bootstrap scripts and runs script. awk is the
// first of awks (default: the system's).
func (n *nodeEnv) run(script string, env ...string) (string, error) {
	n.t.Helper()
	full := "set -e; set -o noglob; umask 027; . scripts/tn-k3s-install.sh; . scripts/tn-k3s-node.sh\n" + script
	cmd := exec.Command("sh", "-c", full)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TN_NODE_") || strings.HasPrefix(e, "TN_K3S_") || strings.HasPrefix(e, "DATA_DIR") {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "TN_K3S_CONFIG_DIR="+n.cfg, "DATA_DIR="+filepath.Join(n.dir, "data"))
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *nodeEnv) file(rel string) string {
	b, err := os.ReadFile(filepath.Join(n.cfg, rel))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

const visionNode = `tn_node_name 'mobile-01-vision'
tn_node_label 'teknoir.org/node-role=vision'
tn_node_label 'teknoir.org/robot=mobile-01'
tn_node_label 'teknoir.org/gpu=nvidia'
tn_node_taint 'nvidia.com/gpu=true:NoSchedule'
tn_node_apply server
`

const wantDropin = `# Written by the Teknoir bootstrap script (tnctl bootstrap --role ...). Do not edit: a re-run rewrites it.
# "+" appends to the node-label and node-taint lists of config.yaml and the other drop-ins.
# k3s applies them only when the node registers for the first time.
node-label+:
  - "teknoir.org/node-role=vision"
  - "teknoir.org/robot=mobile-01"
  - "teknoir.org/gpu=nvidia"
node-taint+:
  - "nvidia.com/gpu=true:NoSchedule"
`

// The config writer, run twice on an empty directory: the second run changes nothing.
func TestNodeWriter_IdempotentOnEmptyDir(t *testing.T) {
	n := newNodeEnv(t)
	out, err := n.run(visionNode)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := n.file("config.yaml"); got != "node-name: mobile-01-vision\n" {
		t.Errorf("config.yaml:\n%s", got)
	}
	if got := n.file("config.yaml.d/98-teknoir-node.yaml"); got != wantDropin {
		t.Errorf("drop-in:\n%s", got)
	}
	if strings.Contains(out, "registered") {
		t.Errorf("a new node must not get the registered warning:\n%s", out)
	}
	before := snapshot(t, n.cfg)

	out2, err := n.run(visionNode)
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, out2)
	}
	if after := snapshot(t, n.cfg); after != before {
		t.Errorf("the second run changed the configuration:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(out2, "already in place") || !strings.Contains(out2, "already sets 'node-name'") {
		t.Errorf("second run output:\n%s", out2)
	}
	if strings.Contains(out2, "[WARN]") {
		t.Errorf("an idempotent re-run must not warn:\n%s", out2)
	}
	if left, _ := filepath.Glob(filepath.Join(n.cfg, "config.yaml.d", ".*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		c, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(&b, "== %s (%s, %d)\n%s", rel, fi.Mode(), fi.Size(), c)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Keys the device owner set are never clobbered: the user's config.yaml is not rewritten, its
// node-label stays, ours are appended by the drop-in; a different node-name is kept and reported.
func TestNodeWriter_KeepsTheOwnersKeys(t *testing.T) {
	n := newNodeEnv(t)
	user := "kubelet-arg:\n  - max-pods=77\nnode-label:\n  - owner=1\nnode-name: owners-name # mine\ndisable:\n  - traefik"
	write(t, filepath.Join(n.cfg, "config.yaml"), user) // no trailing newline on purpose
	out, err := n.run(visionNode)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := n.file("config.yaml"); got != user {
		t.Errorf("config.yaml was changed:\n%s", got)
	}
	if got := n.file("config.yaml.d/98-teknoir-node.yaml"); got != wantDropin {
		t.Errorf("drop-in:\n%s", got)
	}
	if !strings.Contains(out, "sets node-name 'owners-name', not 'mobile-01-vision'") {
		t.Errorf("the different node-name is not reported:\n%s", out)
	}
}

// The user's config.yaml lacks node-name and does not end in a newline: the name is appended on a new line.
func TestNodeWriter_AppendsNodeNameToExistingConfig(t *testing.T) {
	n := newNodeEnv(t)
	write(t, filepath.Join(n.cfg, "config.yaml"), "disable:\n  - traefik")
	if out, err := n.run(visionNode); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := n.file("config.yaml"); got != "disable:\n  - traefik\nnode-name: mobile-01-vision\n" {
		t.Errorf("config.yaml:\n%q", got)
	}
}

// Same name set by the owner: kept silently (no warning).
func TestNodeWriter_SameNameNoWarning(t *testing.T) {
	n := newNodeEnv(t)
	write(t, filepath.Join(n.cfg, "config.yaml"), "node-name: \"mobile-01-vision\"\n")
	out, err := n.run(visionNode)
	if err != nil || strings.Contains(out, "[WARN]") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Labels and taints only: no node-name line is written (the script keeps its own default).
func TestNodeWriter_NoNameNoNodeName(t *testing.T) {
	n := newNodeEnv(t)
	out, err := n.run("tn_node_label 'teknoir.org/node-role=io'\ntn_node_apply agent")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(n.cfg, "config.yaml")); err == nil {
		t.Errorf("config.yaml was created: %s", n.file("config.yaml"))
	}
	if got := n.file("config.yaml.d/98-teknoir-node.yaml"); !strings.Contains(got, `  - "teknoir.org/node-role=io"`) || strings.Contains(got, "node-taint+") {
		t.Errorf("drop-in:\n%s", got)
	}
}

// Only a name (--node-name without labels): config.yaml gets it, no drop-in.
func TestNodeWriter_NameOnly(t *testing.T) {
	n := newNodeEnv(t)
	if out, err := n.run("tn_node_name pi-1\ntn_node_apply agent"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n.file("config.yaml") != "node-name: pi-1\n" {
		t.Errorf("config.yaml:\n%s", n.file("config.yaml"))
	}
	if _, err := os.Stat(filepath.Join(n.cfg, "config.yaml.d")); err == nil {
		t.Error("a drop-in directory was created for nothing")
	}
}

// A changed role on a re-run rewrites our file and leaves everything else alone.
func TestNodeWriter_ChangedRoleRewritesOurFileOnly(t *testing.T) {
	n := newNodeEnv(t)
	write(t, filepath.Join(n.cfg, "config.yaml.d", "50-owner.yaml"), "node-label+:\n  - mine=1\n")
	if out, err := n.run(visionNode); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	other := strings.ReplaceAll(visionNode, "vision", "io")
	other = strings.ReplaceAll(other, "tn_node_label 'teknoir.org/gpu=nvidia'\n", "")
	out, err := n.run(other)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := n.file("config.yaml.d/98-teknoir-node.yaml")
	if !strings.Contains(got, `"teknoir.org/node-role=io"`) || strings.Contains(got, "vision") || strings.Contains(got, "teknoir.org/gpu") {
		t.Errorf("drop-in not rewritten:\n%s", got)
	}
	if n.file("config.yaml.d/50-owner.yaml") != "node-label+:\n  - mine=1\n" {
		t.Error("the owner's drop-in was touched")
	}
	// the name stays the one that was registered first
	if !strings.Contains(out, "sets node-name 'mobile-01-vision', not 'mobile-01-io'") {
		t.Errorf("output:\n%s", out)
	}
}

// The labels do not reach a node that has registered: the script warns, with the kubectl commands.
func TestNodeWriter_WarnsWhenAlreadyRegistered(t *testing.T) {
	n := newNodeEnv(t)
	write(t, filepath.Join(n.dir, "data", "agent", "client-kubelet.crt"), "cert")
	out, err := n.run(visionNode)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"registered with the cluster already",
		"k3s kubectl label node mobile-01-vision teknoir.org/node-role=vision teknoir.org/robot=mobile-01 teknoir.org/gpu=nvidia --overwrite",
		"k3s kubectl taint node mobile-01-vision nvidia.com/gpu=true:NoSchedule --overwrite",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// once the file is in place the labels are what was asked for: no further warning
	out2, err := n.run(visionNode)
	if err != nil || strings.Contains(out2, "[WARN]") {
		t.Fatalf("second run: %v\n%s", err, out2)
	}
	// a change of the labels warns again
	out3, err := n.run(strings.Replace(visionNode, "nvidia'", "amd'", 1))
	if err != nil || !strings.Contains(out3, "registered with the cluster already") {
		t.Fatalf("third run: %v\n%s", err, out3)
	}
}

func TestNodeWriter_RegisteredOverride(t *testing.T) {
	n := newNodeEnv(t)
	out, err := n.run(visionNode, "TN_NODE_REGISTERED=true")
	if err != nil || !strings.Contains(out, "registered with the cluster already") {
		t.Fatalf("%v\n%s", err, out)
	}
	n2 := newNodeEnv(t)
	write(t, filepath.Join(n2.dir, "data", "agent", "client-kubelet.crt"), "cert")
	out, err = n2.run(visionNode, "TN_NODE_REGISTERED=false")
	if err != nil || strings.Contains(out, "registered with the cluster already") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The warning names the node as it is called in config.yaml, or by host name when nothing sets it.
func TestNodeWriter_RegisteredNodeNameFallbacks(t *testing.T) {
	n := newNodeEnv(t)
	write(t, filepath.Join(n.cfg, "config.yaml"), "node-name: owners-name\n")
	out, err := n.run("tn_node_label 'a=b'\ntn_node_apply server", "TN_NODE_REGISTERED=true")
	if err != nil || !strings.Contains(out, "label node owners-name a=b --overwrite") {
		t.Fatalf("%v\n%s", err, out)
	}
	n = newNodeEnv(t)
	out, err = n.run("tn_node_label 'a=b'\ntn_node_apply agent", "TN_NODE_REGISTERED=true")
	host, _ := os.Hostname()
	if err != nil || !strings.Contains(out, "label node "+strings.ToLower(host)+" a=b --overwrite") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The YAML reader of the library works with every awk that sh scripts meet.
func TestNodeWriter_YamlValueWithAllAwks(t *testing.T) {
	needTool(t, "sh")
	yaml := "a: 1\nnode-name: 'quoted-name'  # comment\nnode-name2: no\r\n"
	for _, awk := range []string{"awk", "gawk", "mawk", "busybox"} {
		if _, err := exec.LookPath(awk); err != nil {
			continue
		}
		t.Run(awk, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "c.yaml"), yaml)
			bin := filepath.Join(dir, "bin")
			target, _ := exec.LookPath(awk)
			if awk == "busybox" {
				write(t, filepath.Join(bin, "awk"), "#!/bin/sh\nexec "+target+" awk \"$@\"\n")
			} else {
				write(t, filepath.Join(bin, "awk"), "#!/bin/sh\nexec "+target+" \"$@\"\n")
			}
			if err := os.Chmod(filepath.Join(bin, "awk"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", ". scripts/tn-k3s-install.sh; . scripts/tn-k3s-node.sh; tn_node_yaml_value "+filepath.Join(dir, "c.yaml")+" node-name")
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "quoted-name" {
				t.Errorf("%q %v", out, err)
			}
		})
	}
}

// The whole rendered scripts, with the node part run for real: from the first line of the libraries to the
// call of tn_node_apply, in a temporary configuration directory.
func TestRenderedNodeBlock_RunsAndIsIdempotent(t *testing.T) {
	needTool(t, "sh")
	for name, d := range map[string]struct {
		data TemplateData
		role string
		want string
	}{
		"server": {serverRolesData(), "server", "mobile-01-vision"},
		"agent":  {agentRolesData(), "agent", "mobile-01-io"},
	} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			tpl := ServerTemplate
			if name == "agent" {
				tpl = AgentTemplate
			}
			if err := tpl.Execute(&b, d.data); err != nil {
				t.Fatal(err)
			}
			out := b.String()
			// the calls the template renders, between the library and the keys/k3s_version section
			start := strings.Index(out, "\ntn_node_name ")
			end := strings.Index(out, "tn_node_label 'teknoir.org/robot=")
			if start < 0 || end < 0 {
				t.Fatal("no node calls in the script")
			}
			lines := out[start:]
			var calls []string
			for _, l := range strings.Split(lines, "\n") {
				if strings.HasPrefix(l, "tn_node_name ") || strings.HasPrefix(l, "tn_node_label ") || strings.HasPrefix(l, "tn_node_taint ") {
					calls = append(calls, l)
				}
			}
			n := newNodeEnv(t)
			script := strings.Join(calls, "\n") + "\ntn_node_apply " + d.role + "\n"
			if o, err := n.run(script); err != nil {
				t.Fatalf("%v\n%s", err, o)
			}
			first := snapshot(t, n.cfg)
			if o, err := n.run(script); err != nil {
				t.Fatalf("%v\n%s", err, o)
			}
			if snapshot(t, n.cfg) != first {
				t.Error("not idempotent")
			}
			if !strings.Contains(first, "node-name: "+d.want+"\n") || !strings.Contains(first, `"teknoir.org/robot=mobile-01"`) {
				t.Errorf("unexpected configuration:\n%s", first)
			}
		})
	}
}
