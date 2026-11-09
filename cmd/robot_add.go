package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// dnsLabel is what a robot and its device are named: a DNS label, lower case.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const dnsLabelMax = 63

// isDNSLabel reports whether s is a DNS label.
func isDNSLabel(s string) bool { return len(s) <= dnsLabelMax && dnsLabel.MatchString(s) }

// labelFlags collects --label key=value pairs.
type labelFlags struct {
	labels map[string]string
}

func (l *labelFlags) String() string {
	keys := make([]string, 0, len(l.labels))
	for k := range l.labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + l.labels[k]
	}
	return strings.Join(parts, ",")
}

// Set parses one --label key=value.
func (l *labelFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("invalid label %q: expected key=value", v)
	}
	if l.labels == nil {
		l.labels = map[string]string{}
	}
	l.labels[k] = val
	return nil
}

// Type is the pflag.Value type name.
func (l *labelFlags) Type() string { return "key=value" }

type robotAddFlags struct {
	typ    string
	device string
	unit   string
	labels labelFlags
}

func newRobotAddCmd(e *cliEnv) *cobra.Command {
	var f robotAddFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "add <name> --type <robot type> [--device D] [--unit U] [--label k=v]...",
		Short:        "Create a robot (and its device) in the teamspace",
		Long: `Create a robot in the teamspace, with the device that backs it: a device named like the robot,
unless --device adopts an existing one.

--type is the robot type it is created from (tnctl robot types lists them, where the platform has
that route). --label sets a label of the robot and may be repeated. --unit places its device in an
organisation unit; without one the platform uses the caller's own.

The name and the device are DNS labels: lower-case letters, digits and "-", at most 63 characters.
Prints "robot <name> created (device <device>)" or, with -o json, the robot the platform answered.`,
		Example: `  tnctl robot add amr-02 --type amr
  tnctl robot add vision-station-02 --type vision --label site=karlstad --label env=prod
  tnctl robot add amr-03 --type amr --device amr-02 --unit karlstad`,
		Args: argsRange(1, 1, "robot add needs a robot name, for example: tnctl robot add amr-02 --type amr"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			name := args[0]
			if !isDNSLabel(name) {
				return usageErr("robot name %q is not a DNS label: lower-case letters, digits and \"-\", at most 63 characters", name)
			}
			if f.typ == "" {
				return usageErr("--type is required: the robot type the robot is created from")
			}
			if f.device != "" && !isDNSLabel(f.device) {
				return usageErr("device name %q is not a DNS label: lower-case letters, digits and \"-\", at most 63 characters", f.device)
			}
			for k, v := range f.labels.labels {
				if !isLabelKey(k) || !isLabelValue(v) {
					return usageErr("label %q is not a Kubernetes label: a key of letters, digits, \"-\", \"_\" and \".\" (a / prefix is allowed) and a value of at most 63 such characters", k+"="+v)
				}
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			req := api.CreateRobotRequest{Name: name, Type: f.typ, Device: f.device, OrgUnit: f.unit}
			if len(f.labels.labels) > 0 {
				req.Labels = f.labels.labels
			}
			robot, raw, err := fl.CreateRobot(cmd.Context(), req)
			if err != nil {
				return classify(err)
			}
			out := cmd.OutOrStdout()
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			rn := robot.Name
			if rn == "" {
				rn = name
			}
			dev := robot.Device
			if dev == "" {
				dev = f.device
			}
			if dev == "" {
				dev = name
			}
			fmt.Fprintf(out, "robot %s created (device %s)\n", rn, dev)
			return nil
		},
	}
	c.Flags().StringVar(&f.typ, "type", "", "robot type the robot is created from (required)")
	c.Flags().StringVar(&f.device, "device", "", "adopt this existing device instead of creating one")
	c.Flags().StringVar(&f.unit, "unit", "", "organisation unit to place its device in")
	c.Flags().Var(&f.labels, "label", "a label of the robot, key=value (repeatable)")
	return c
}

// isLabelKey reports whether k is a Kubernetes label key (an optional DNS subdomain prefix and a
// name of letters, digits, "-", "_" and ".").
func isLabelKey(k string) bool {
	name := k
	if prefix, rest, ok := strings.Cut(k, "/"); ok {
		if !isDNSSubdomain(prefix) {
			return false
		}
		name = rest
	}
	if name == "" || len(name) > 63 {
		return false
	}
	return labelNameRE.MatchString(name)
}

// isLabelValue reports whether v is a Kubernetes label value: empty, or up to 63 characters of
// letters, digits, "-", "_" and ".".
func isLabelValue(v string) bool {
	if v == "" {
		return true
	}
	return len(v) <= 63 && labelNameRE.MatchString(v)
}

// isDNSSubdomain reports whether s is a DNS subdomain (lower-case labels joined by dots).
func isDNSSubdomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !isDNSLabel(part) {
			return false
		}
	}
	return true
}

var labelNameRE = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
