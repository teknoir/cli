package templates

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Node roles. A role is a set of k3s node labels: always teknoir.org/node-role=<role> and, when the
// node belongs to a robot, teknoir.org/robot=<robot>, plus any extra labels and taints the robot type
// asks for. k3s applies node-label and node-taint only when a node registers for the first time, so the
// bootstrap scripts write them before the k3s installer runs (see scripts/tn-k3s-node.sh).
const (
	// LabelNodeRole is the label every node with a role carries.
	LabelNodeRole = "teknoir.org/node-role"
	// LabelRobot is the label of the robot (the multi-node cluster) a node belongs to.
	LabelRobot = "teknoir.org/robot"
)

var (
	dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// hostLabelRE is a label of a host name: the case of a name is not significant, so a user's
	// "Orin.local" is let through (and written as typed).
	hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9]*[A-Za-z0-9])?$`)
	// labelNameRE is the name part of a Kubernetes label key and a label value.
	labelNameRE = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	taintRE     = regexp.MustCompile(`^([^=:]+)(?:=([^:]*))?:([A-Za-z]+)$`)
)

// TaintEffects are the effects Kubernetes knows for a taint.
var TaintEffects = []string{"NoSchedule", "PreferNoSchedule", "NoExecute"}

// NodeConfig is what one node of a robot is told at bootstrap: its role, its robot, its name, and extra
// labels and taints. The zero value means "nothing": the script then behaves as it always did.
type NodeConfig struct {
	// Role is the node role (a DNS label), for example vision or io.
	Role string
	// Robot is the robot the node belongs to (a DNS label).
	Robot string
	// Name is the node name asked for. Empty: <Robot>-<Role> when both are set, else the default of the
	// script (a server: teknoir-master, an agent: the host name).
	Name string
	// Labels are extra labels, "key=value" (the value may be empty), without the two Teknoir labels.
	Labels []string
	// Taints are node taints, "key[=value]:Effect".
	Taints []string
}

// Configured reports whether the node asks for anything at all.
func (n NodeConfig) Configured() bool {
	return n.Role != "" || n.Robot != "" || n.Name != "" || len(n.Labels) > 0 || len(n.Taints) > 0
}

// EffectiveName is the node name the script sets, or "" when it keeps its own default.
func (n NodeConfig) EffectiveName() string {
	if n.Name != "" {
		return n.Name
	}
	if n.Robot != "" && n.Role != "" {
		return n.Robot + "-" + n.Role
	}
	return ""
}

// AllLabels is every label of the node in the order they are written: the role, the robot, then the
// extra labels as given.
func (n NodeConfig) AllLabels() []string {
	var l []string
	if n.Role != "" {
		l = append(l, LabelNodeRole+"="+n.Role)
	}
	if n.Robot != "" {
		l = append(l, LabelRobot+"="+n.Robot)
	}
	return append(l, n.Labels...)
}

// HasLabelsOrTaints reports whether there is anything for the labels and taints file.
func (n NodeConfig) HasLabelsOrTaints() bool {
	return len(n.AllLabels()) > 0 || len(n.Taints) > 0
}

// Validate checks every part of the node configuration. The values end up in shell and YAML, so
// nothing is let through that is not Kubernetes syntax.
func (n NodeConfig) Validate() error {
	if n.Role != "" {
		if err := validateDNSLabel("role", n.Role); err != nil {
			return err
		}
	}
	if n.Robot != "" {
		if err := validateDNSLabel("robot", n.Robot); err != nil {
			return err
		}
	}
	if n.Name != "" {
		if err := validateDNSSubdomain("node name", n.Name); err != nil {
			return err
		}
	}
	if n.Robot != "" && n.Role == "" && n.Name == "" {
		return fmt.Errorf("--robot %q needs a --role (the node is named <robot>-<role>), or an explicit --node-name", n.Robot)
	}
	seen := map[string]bool{}
	if n.Role != "" {
		seen[LabelNodeRole] = true
	}
	if n.Robot != "" {
		seen[LabelRobot] = true
	}
	for _, l := range n.Labels {
		key, err := ValidateLabel(l)
		if err != nil {
			return err
		}
		switch key {
		case LabelNodeRole:
			return fmt.Errorf("label %q: %s is set by --role, do not pass it as a label", l, LabelNodeRole)
		case LabelRobot:
			return fmt.Errorf("label %q: %s is set by --robot, do not pass it as a label", l, LabelRobot)
		}
		if seen[key] {
			return fmt.Errorf("label key %q is given more than once", key)
		}
		seen[key] = true
	}
	seenTaint := map[string]bool{}
	for _, t := range n.Taints {
		if err := ValidateTaint(t); err != nil {
			return err
		}
		if seenTaint[t] {
			return fmt.Errorf("taint %q is given more than once", t)
		}
		seenTaint[t] = true
	}
	return nil
}

func validateDNSLabel(what, v string) error {
	if len(v) > 63 || !dnsLabelRE.MatchString(v) {
		return fmt.Errorf("invalid %s %q: want a DNS label (lowercase letters, digits and '-', starting and ending with a letter or digit, at most 63 characters)", what, v)
	}
	return nil
}

func validateDNSSubdomain(what, v string) error {
	if len(v) > 253 {
		return fmt.Errorf("invalid %s %q: longer than 253 characters", what, v)
	}
	for _, p := range strings.Split(v, ".") {
		if len(p) > 63 || !dnsLabelRE.MatchString(p) {
			return fmt.Errorf("invalid %s %q: want a DNS name (lowercase letters, digits, '-' and '.', every part starting and ending with a letter or digit, at most 63 characters)", what, v)
		}
	}
	return nil
}

// restrictedDomain reports whether a label or taint key prefix belongs to Kubernetes itself
// (kubernetes.io, k8s.io and any subdomain of them). The kubelet refuses to register with such labels,
// except a few it sets itself, and k3s would not start the node.
func restrictedDomain(prefix string) bool {
	for _, d := range []string{"kubernetes.io", "k8s.io"} {
		if prefix == d || strings.HasSuffix(prefix, "."+d) {
			return true
		}
	}
	return false
}

// validateKey checks a label or taint key (an optional DNS subdomain prefix and '/', then a name).
func validateKey(what, key string) error {
	prefix, name := "", key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		prefix, name = key[:i], key[i+1:]
		if err := validateDNSSubdomain(what+" key prefix", prefix); err != nil {
			return err
		}
		if restrictedDomain(prefix) {
			return fmt.Errorf("%s key %q: the kubernetes.io/ and k8s.io/ prefixes are reserved for Kubernetes; use your own prefix, like teknoir.org/", what, key)
		}
	}
	if len(name) > 63 || !labelNameRE.MatchString(name) {
		return fmt.Errorf("invalid %s key %q: the name part (after any '/') must be 1-63 characters of letters, digits, '-', '_' and '.', starting and ending with a letter or digit", what, key)
	}
	return nil
}

// ValidateLabel checks "key=value" (the value may be empty) and returns the key.
func ValidateLabel(l string) (string, error) {
	key, val, ok := strings.Cut(l, "=")
	if !ok {
		return "", fmt.Errorf("invalid label %q: want key=value", l)
	}
	if err := validateKey("label", key); err != nil {
		return "", err
	}
	if val != "" && (len(val) > 63 || !labelNameRE.MatchString(val)) {
		return "", fmt.Errorf("invalid label %q: the value must be at most 63 characters of letters, digits, '-', '_' and '.', starting and ending with a letter or digit", l)
	}
	return key, nil
}

// ValidateTaint checks "key[=value]:Effect".
func ValidateTaint(t string) error {
	m := taintRE.FindStringSubmatch(t)
	if m == nil {
		return fmt.Errorf("invalid taint %q: want key[=value]:Effect, with Effect one of %s", t, strings.Join(TaintEffects, ", "))
	}
	if err := validateKey("taint", m[1]); err != nil {
		return err
	}
	if v := m[2]; v != "" && (len(v) > 63 || !labelNameRE.MatchString(v)) {
		return fmt.Errorf("invalid taint %q: the value must be at most 63 characters of letters, digits, '-', '_' and '.', starting and ending with a letter or digit", t)
	}
	for _, e := range TaintEffects {
		if m[3] == e {
			return nil
		}
	}
	return fmt.Errorf("invalid taint %q: the effect must be one of %s", t, strings.Join(TaintEffects, ", "))
}

// ValidateHost checks the address of a k3s server: a DNS name, an IPv4 or an IPv6 address, without
// scheme, port or path.
func ValidateHost(what, h string) error {
	if h == "" {
		return fmt.Errorf("%s must not be empty", what)
	}
	if strings.ContainsAny(h, "/@ ?#") || strings.Contains(h, "://") {
		return fmt.Errorf("invalid %s %q: give a host name or an IP address only, without scheme, port or path", what, h)
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return nil
	}
	if strings.Contains(h, ":") {
		return fmt.Errorf("invalid %s %q: give a host name or an IP address only (the port is always 6443)", what, h)
	}
	if len(h) > 253 {
		return fmt.Errorf("invalid %s %q: longer than 253 characters", what, h)
	}
	for _, p := range strings.Split(h, ".") {
		if len(p) > 63 || !hostLabelRE.MatchString(p) {
			return fmt.Errorf("invalid %s %q: want a host name or an IP address", what, h)
		}
	}
	return nil
}

// bareHost is h without the brackets of an IPv6 literal.
func bareHost(h string) string { return strings.Trim(h, "[]") }

// K3SServerURL is the URL an agent joins: https://<host>:6443 (an IPv6 address in brackets).
func K3SServerURL(host string) string {
	h := bareHost(host)
	if ip := net.ParseIP(h); ip != nil && ip.To4() == nil {
		h = "[" + h + "]"
	}
	return "https://" + h + ":6443"
}

// ServerURL is K3SServerURL of the Server field, or "" when none is set.
func (d TemplateData) ServerURL() string {
	if d.Server == "" {
		return ""
	}
	return K3SServerURL(d.Server)
}

// MergeTLSSANs returns the --tls-san list of a server script: the server address (--server) first,
// then the explicit ones, without duplicates and without the two names the script always adds.
func MergeTLSSANs(server string, explicit []string, skip ...string) []string {
	seen := map[string]bool{}
	for _, s := range skip {
		seen[strings.ToLower(s)] = true
	}
	var out []string
	add := func(h string) {
		h = bareHost(h)
		if h == "" || seen[strings.ToLower(h)] {
			return
		}
		seen[strings.ToLower(h)] = true
		out = append(out, h)
	}
	add(server)
	for _, h := range explicit {
		add(h)
	}
	return out
}
