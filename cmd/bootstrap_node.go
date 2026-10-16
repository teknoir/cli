package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/templates"
)

// The node flags of `tnctl bootstrap`: the role of a computer in a robot, chosen when it is bootstrapped.
//
// A robot is one device in the platform (one set of device keys) that may have several computers: one k3s
// server and any number of k3s agents. A single run produces the server script and the agent script of
// the robot, with one token, so the flags come in two sets: --role, --node-name, --label and --taint
// are for the server script; the --agent-* flags are for the agent script. --robot, --server and
// --tls-san are about the robot as a whole.

// nodeOptions is what the node flags say, before validation.
type nodeOptions struct {
	Robot  string
	Server string
	SANs   []string

	Role, NodeName     string
	Labels, Taints     []string
	AgentRole          string
	AgentNodeName      string
	AgentLabels        []string
	AgentTaints        []string
	serverFlagsPresent bool
	agentFlagsPresent  bool
}

// bootstrapNodes is the validated result: what each script is told.
type bootstrapNodes struct {
	Server        templates.NodeConfig
	Agent         templates.NodeConfig
	ServerAddress string
	TLSSANs       []string
}

func addNodeFlags(c *cobra.Command) {
	f := c.Flags()
	f.String("robot", "", "name of the robot this computer belongs to; becomes the label "+templates.LabelRobot+" and, with --role, the node name <robot>-<role>")
	f.String("role", "", "node role of the SERVER script (a DNS label, like vision or io); becomes the label "+templates.LabelNodeRole+". k3s applies labels at the first registration of a node, so it is chosen here")
	f.String("node-name", "", "node name of the server script (default: <robot>-<role> with --robot and --role, else teknoir-master)")
	f.StringArray("label", nil, "extra node label key=value for the server script (repeatable). The kubernetes.io/ and k8s.io/ prefixes are refused")
	f.StringArray("taint", nil, "node taint key[=value]:Effect for the server script (repeatable; Effect is NoSchedule, PreferNoSchedule or NoExecute)")
	f.String("agent-role", "", "node role of the AGENT script, for a second computer of the robot (see --role)")
	f.String("agent-node-name", "", "node name of the agent script (default: <robot>-<agent-role> with --robot and --agent-role, else the host name)")
	f.StringArray("agent-label", nil, "extra node label key=value for the agent script (repeatable)")
	f.StringArray("agent-taint", nil, "node taint key[=value]:Effect for the agent script (repeatable)")
	f.String("server", "", "host name or IP address of the k3s server that the agent script joins; baked in as K3S_URL=https://<host>:6443 (K3S_URL in the environment still wins) and added to the server's TLS SANs")
	f.StringArray("tls-san", nil, "extra host name or IP address in the server certificate (--tls-san of k3s; repeatable)")
}

func nodeOptionsFromFlags(c *cobra.Command) (nodeOptions, error) {
	f := c.Flags()
	var o nodeOptions
	var err error
	str := func(name string) string {
		if err != nil {
			return ""
		}
		var v string
		v, err = f.GetString(name)
		return v
	}
	arr := func(name string) []string {
		if err != nil {
			return nil
		}
		var v []string
		v, err = f.GetStringArray(name)
		return v
	}
	o.Robot, o.Server = str("robot"), str("server")
	o.Role, o.NodeName, o.Labels, o.Taints = str("role"), str("node-name"), arr("label"), arr("taint")
	o.AgentRole, o.AgentNodeName, o.AgentLabels, o.AgentTaints = str("agent-role"), str("agent-node-name"), arr("agent-label"), arr("agent-taint")
	o.SANs = arr("tls-san")
	o.serverFlagsPresent = f.Changed("role") || f.Changed("node-name") || f.Changed("label") || f.Changed("taint")
	o.agentFlagsPresent = f.Changed("agent-role") || f.Changed("agent-node-name") || f.Changed("agent-label") || f.Changed("agent-taint")
	return o, err
}

// resolve validates the options and builds the node configuration of the server and the agent script.
func (o nodeOptions) resolve() (bootstrapNodes, error) {
	var n bootstrapNodes
	if o.Robot != "" && !o.serverFlagsPresent && !o.agentFlagsPresent {
		return n, fmt.Errorf("--robot has no effect without --role (or --agent-role): it names the robot the node belongs to")
	}
	if o.serverFlagsPresent {
		n.Server = templates.NodeConfig{Role: o.Role, Robot: o.Robot, Name: o.NodeName, Labels: o.Labels, Taints: o.Taints}
		if err := n.Server.Validate(); err != nil {
			return n, fmt.Errorf("server node: %w", err)
		}
	}
	if o.agentFlagsPresent {
		n.Agent = templates.NodeConfig{Role: o.AgentRole, Robot: o.Robot, Name: o.AgentNodeName, Labels: o.AgentLabels, Taints: o.AgentTaints}
		if err := n.Agent.Validate(); err != nil {
			return n, fmt.Errorf("agent node: %w", err)
		}
	}
	if s, a := n.Server.EffectiveName(), n.Agent.EffectiveName(); s != "" && s == a {
		return n, fmt.Errorf("the server and the agent node would both be named %q: give one of them another --node-name / --agent-node-name, or another role", s)
	}
	if o.Server != "" {
		if err := templates.ValidateHost("--server", o.Server); err != nil {
			return n, err
		}
		n.ServerAddress = strings.Trim(o.Server, "[]")
	}
	for _, san := range o.SANs {
		if err := templates.ValidateHost("--tls-san", san); err != nil {
			return n, err
		}
	}
	n.TLSSANs = o.SANs
	return n, nil
}

// describe tells what the scripts were generated with, so that a typo in a role is seen before the script is
// copied to a device. It is empty when no node flag was given.
func (n bootstrapNodes) describe(agentFile, serverFile string) string {
	var b strings.Builder
	node := func(file, kind string, c templates.NodeConfig) {
		if !c.Configured() {
			return
		}
		name := c.EffectiveName()
		if name == "" {
			name = "(default)"
		}
		fmt.Fprintf(&b, "%s: %s node %s, labels %s", file, kind, name, strings.Join(c.AllLabels(), ","))
		if len(c.Taints) > 0 {
			fmt.Fprintf(&b, ", taints %s", strings.Join(c.Taints, ","))
		}
		b.WriteString("\n")
	}
	node(serverFile, "server", n.Server)
	node(agentFile, "agent", n.Agent)
	if n.ServerAddress != "" {
		fmt.Fprintf(&b, "%s joins %s\n", agentFile, templates.K3SServerURL(n.ServerAddress))
	}
	if n.Server.HasLabelsOrTaints() || n.Agent.HasLabelsOrTaints() {
		b.WriteString("Note: k3s applies node labels and taints when a node registers for the first time; on a computer that is already part of a cluster the script prints the kubectl commands instead.\n")
	}
	return b.String()
}

// apply gives the server and the agent script their own data from the common data of one run: the scripts
// share the device, its keys and the token, but are for different computers of the robot. The docker
// server script keeps the common data (it takes no node roles).
func (n bootstrapNodes) apply(base templates.TemplateData) (server, agent templates.TemplateData) {
	server, agent = base, base
	server.Node = n.Server
	server.TLSSANs = templates.MergeTLSSANs(n.ServerAddress, n.TLSSANs, base.DeviceID+"."+base.Domain, "10.0.0.10")
	agent.Node = n.Agent
	agent.Server = n.ServerAddress
	return server, agent
}
