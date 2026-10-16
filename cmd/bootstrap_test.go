package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/templates"
)

func TestBootstrapK3SVersionFlag(t *testing.T) {
	f := bootstrapCmd.Flags().Lookup("k3s-version")
	if f == nil {
		t.Fatal("no --k3s-version flag")
	}
	if f.DefValue != templates.DefaultK3SVersion {
		t.Errorf("default %q", f.DefValue)
	}
}

func TestBootstrapK3SUpgradeFlags(t *testing.T) {
	for _, name := range []string{"k3s-upgrade", "force"} {
		f := bootstrapCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("no --%s flag", name)
		}
		// a re-run keeps the installed k3s unless the operator asks otherwise
		if f.DefValue != "false" {
			t.Errorf("--%s defaults to %q", name, f.DefValue)
		}
	}
}

func newNodeFlagCmd(t *testing.T, args ...string) nodeOptions {
	t.Helper()
	c := &cobra.Command{Use: "x", Run: func(*cobra.Command, []string) {}}
	addNodeFlags(c)
	if err := c.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	o, err := nodeOptionsFromFlags(c)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestBootstrapNodeFlagsExist(t *testing.T) {
	for _, name := range []string{"role", "robot", "node-name", "label", "taint", "server", "tls-san", "agent-role", "agent-node-name", "agent-label", "agent-taint"} {
		if bootstrapCmd.Flags().Lookup(name) == nil {
			t.Errorf("no --%s flag", name)
		}
	}
}

// With none of the node flags nothing is configured: the scripts stay as they were.
func TestBootstrapNodes_NoFlagsNoConfig(t *testing.T) {
	n, err := newNodeFlagCmd(t).resolve()
	if err != nil {
		t.Fatal(err)
	}
	if n.Server.Configured() || n.Agent.Configured() || n.ServerAddress != "" || len(n.TLSSANs) != 0 {
		t.Errorf("%+v", n)
	}
}

func TestBootstrapNodes_Robot(t *testing.T) {
	n, err := newNodeFlagCmd(t,
		"--robot", "mobile-01", "--role", "vision", "--label", "teknoir.org/gpu=nvidia", "--taint", "nvidia.com/gpu=true:NoSchedule",
		"--agent-role", "io", "--server", "orin.local", "--tls-san", "10.0.0.5", "--tls-san", "orin").resolve()
	if err != nil {
		t.Fatal(err)
	}
	if n.Server.EffectiveName() != "mobile-01-vision" || n.Agent.EffectiveName() != "mobile-01-io" {
		t.Errorf("names %q %q", n.Server.EffectiveName(), n.Agent.EffectiveName())
	}
	if got := strings.Join(n.Server.AllLabels(), " "); got != "teknoir.org/node-role=vision teknoir.org/robot=mobile-01 teknoir.org/gpu=nvidia" {
		t.Errorf("server labels %s", got)
	}
	if got := strings.Join(n.Agent.AllLabels(), " "); got != "teknoir.org/node-role=io teknoir.org/robot=mobile-01" {
		t.Errorf("agent labels %s", got)
	}
	if len(n.Agent.Taints) != 0 {
		t.Errorf("the agent got the taints of the server: %v", n.Agent.Taints)
	}
	if n.ServerAddress != "orin.local" || len(n.TLSSANs) != 2 {
		t.Errorf("%+v", n)
	}
}

func TestBootstrapNodes_RoleWithoutRobotKeepsDefaultName(t *testing.T) {
	n, err := newNodeFlagCmd(t, "--role", "vision").resolve()
	if err != nil {
		t.Fatal(err)
	}
	if n.Server.EffectiveName() != "" || n.Agent.Configured() {
		t.Errorf("%+v", n)
	}
}

func TestBootstrapNodes_OnlyTheAgentIsConfigured(t *testing.T) {
	n, err := newNodeFlagCmd(t, "--robot", "r1", "--agent-role", "io").resolve()
	if err != nil {
		t.Fatal(err)
	}
	if n.Server.Configured() || n.Agent.EffectiveName() != "r1-io" {
		t.Errorf("%+v", n)
	}
}

func TestBootstrapNodes_Rejects(t *testing.T) {
	for name, args := range map[string][]string{
		"robot alone":             {"--robot", "r1"},
		"robot without role":      {"--robot", "r1", "--label", "a=b"},
		"bad role":                {"--role", "Vision"},
		"bad robot":               {"--role", "io", "--robot", "r_1"},
		"bad agent role":          {"--agent-role", "x y"},
		"reserved label":          {"--role", "io", "--label", "kubernetes.io/hostname=x"},
		"reserved agent label":    {"--agent-role", "io", "--agent-label", "foo.k8s.io/x=y"},
		"role label":              {"--role", "io", "--label", "teknoir.org/node-role=x"},
		"bad taint":               {"--role", "io", "--taint", "a=b"},
		"bad agent taint":         {"--agent-role", "io", "--agent-taint", "a=b:Nope"},
		"bad server":              {"--server", "https://orin"},
		"server with port":        {"--server", "orin:6443"},
		"bad san":                 {"--tls-san", "a b"},
		"same node name":          {"--robot", "r1", "--role", "io", "--agent-role", "io"},
		"same explicit node name": {"--node-name", "n1", "--agent-node-name", "n1"},
		"bad node name":           {"--node-name", "N_1"},
	} {
		if _, err := newNodeFlagCmd(t, args...).resolve(); err == nil {
			t.Errorf("%s: %v accepted", name, args)
		}
	}
}

func TestBootstrapNodes_ReservedPrefixError(t *testing.T) {
	_, err := newNodeFlagCmd(t, "--role", "io", "--label", "kubernetes.io/hostname=x").resolve()
	if err == nil || !strings.Contains(err.Error(), "kubernetes.io/ and k8s.io/") || !strings.Contains(err.Error(), "server node") {
		t.Errorf("%v", err)
	}
}

func TestBootstrapNodes_Describe(t *testing.T) {
	n, err := newNodeFlagCmd(t, "--robot", "m", "--role", "vision", "--agent-role", "io", "--agent-taint", "a=b:NoExecute", "--server", "orin").resolve()
	if err != nil {
		t.Fatal(err)
	}
	out := n.describe("agent.sh", "server.sh")
	for _, want := range []string{"server.sh: server node m-vision, labels teknoir.org/node-role=vision,teknoir.org/robot=m", "agent.sh: agent node m-io", "taints a=b:NoExecute", "agent.sh joins https://orin:6443"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if none := (bootstrapNodes{}).describe("a", "s"); none != "" {
		t.Errorf("without node flags nothing is printed: %q", none)
	}
}

func TestBootstrapNodes_ApplyKeepsOneToken(t *testing.T) {
	n, err := newNodeFlagCmd(t, "--robot", "m", "--role", "vision", "--agent-role", "io", "--server", "orin.local", "--tls-san", "orin.local", "--tls-san", "dev-1.test.cloud", "--tls-san", "10.0.0.5").resolve()
	if err != nil {
		t.Fatal(err)
	}
	base := templates.TemplateData{DeviceID: "dev-1", Domain: "test.cloud", K3SToken: "tok"}
	s, a := n.apply(base)
	if s.K3SToken != "tok" || a.K3SToken != "tok" {
		t.Error("the scripts of one run must share the token")
	}
	// the server address is first, nothing twice, nothing that the script adds itself
	if strings.Join(s.TLSSANs, ",") != "orin.local,10.0.0.5" {
		t.Errorf("tls-san %v", s.TLSSANs)
	}
	if s.Server != "" || a.Server != "orin.local" || len(a.TLSSANs) != 0 {
		t.Errorf("server %q / %q, agent sans %v", s.Server, a.Server, a.TLSSANs)
	}
	if s.Node.EffectiveName() != "m-vision" || a.Node.EffectiveName() != "m-io" {
		t.Errorf("%q %q", s.Node.EffectiveName(), a.Node.EffectiveName())
	}
	var out strings.Builder
	if err := templates.ServerTemplate.Execute(&out, s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--tls-san 10.0.0.10 --tls-san orin.local --tls-san 10.0.0.5") {
		t.Error("the server script lacks the SANs")
	}
	// no node flags: the data is the common data
	z, _ := newNodeFlagCmd(t).resolve()
	s, a = z.apply(base)
	if s.Node.Configured() || a.Node.Configured() || s.Server != "" || a.Server != "" || len(s.TLSSANs) != 0 {
		t.Errorf("%+v %+v", s, a)
	}
}
