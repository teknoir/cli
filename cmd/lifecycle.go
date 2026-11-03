package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

// lifecyclePollInterval is how often --wait asks the platform for the device.
var lifecyclePollInterval = 3 * time.Second

// lifecycleErrorGrace is how long `disable --wait` lets a device read `disabling` with an error before
// it gives up with exit 7. Right after the write, that error can still be one from before this disable
// (a failed enable, an MQTT error the device already had); device-controller replaces it once it acts.
// A `disabled` with an error is this disable's own and ends the wait at once.
var lifecycleErrorGrace = 15 * time.Second

// lifecycleFlags are the flags of disable, enable and remove. Not every command defines every flag.
type lifecycleFlags struct {
	reason  string
	wait    bool
	timeout time.Duration
	yes     bool
}

// lifecycleKind is whether a command works on a robot (and its device) or on a device.
type lifecycleKind int

const (
	lifecycleRobot lifecycleKind = iota
	lifecycleDevice
)

// lifecycleTarget is what the command read before it changes anything.
type lifecycleTarget struct {
	kind     lifecycleKind
	name     string // the robot or device name as the platform spelled it
	robot    string // the robot name, "" for a device command
	device   string
	owner    string // robot whose device this robot runs on, "" when it is its own
	disabled *api.FleetDisabled
}

// thing is how the tables name it: "robot r (device d)" or "device d".
func (t *lifecycleTarget) thing() string {
	if t.kind == lifecycleRobot {
		return fmt.Sprintf("robot %s (device %s)", t.robot, t.device)
	}
	return "device " + t.device
}

// noun is "robot" or "device", for messages that name the command.
func (t *lifecycleTarget) noun() string {
	if t.kind == lifecycleRobot {
		return "robot"
	}
	return "device"
}

// enableHint is the command that undoes a disable.
func (t *lifecycleTarget) enableHint() string {
	return "tnctl " + t.noun() + " enable " + t.name
}

// disableSentence is what a disable does on the robot.
func (t *lifecycleTarget) disableSentence() string {
	return "Within about a minute its MQTT key is withdrawn, its MQTT sessions are closed and Tunneling is turned off. " +
		"Its apps keep running. Enable it again with: " + t.enableHint()
}

// oneLifecycleArg is the Args rule of every lifecycle command: exactly one name.
func oneLifecycleArg(what, example string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usageErr("%s needs exactly one name, for example: %s", what, example)
		}
		return nil
	}
}

func addWaitFlags(c *cobra.Command, f *lifecycleFlags) {
	c.Flags().BoolVar(&f.wait, "wait", false, "wait until device-controller confirms the change")
	c.Flags().DurationVar(&f.timeout, "timeout", 3*time.Minute, "how long --wait may take (exit code 4 when it runs out)")
}

// cleanReason trims the --reason text and refuses a value the platform would refuse.
func cleanReason(r string) (string, error) {
	r = strings.TrimSpace(r)
	if r == "" {
		return "", nil
	}
	// The server counts in UTF-16 code units (JavaScript string.length), not in runes.
	if len(utf16.Encode([]rune(r))) > 200 {
		return "", usageErr("--reason must be one line of at most 200 characters")
	}
	for _, ru := range r {
		if ru < 0x20 || (ru >= 0x7f && ru <= 0x9f) || ru == 0x2028 || ru == 0x2029 {
			return "", usageErr("--reason must be one line of at most 200 characters")
		}
	}
	return r, nil
}

// lifecycleGet reads the robot or device before the command changes it. A missing name is exit 3.
func (e *cliEnv) lifecycleGet(ctx context.Context, fl *api.Fleet, kind lifecycleKind, name string) (*lifecycleTarget, error) {
	if kind == lifecycleRobot {
		r, _, err := fl.Robot(ctx, name)
		if err != nil {
			return nil, classify(err)
		}
		rn := r.Name
		if rn == "" {
			rn = name
		}
		if r.Device == "" {
			return nil, newErr(codeError, "robot %s has no device", rn)
		}
		return &lifecycleTarget{kind: kind, name: rn, robot: rn, device: r.Device, owner: r.DeviceOwner, disabled: r.Disabled}, nil
	}
	d, err := fl.Device(ctx, name)
	if err != nil {
		return nil, classify(err)
	}
	dn := d.Name
	if dn == "" {
		dn = name
	}
	return &lifecycleTarget{kind: kind, name: dn, device: dn, disabled: d.Disabled}, nil
}

// errDeviceOwner refuses to disable or enable a robot whose device belongs to another robot.
func errDeviceOwner(t *lifecycleTarget) error {
	return newErr(codeRejected, "robot %s runs on device %s, which belongs to robot %s: disable or enable robot %s instead",
		t.robot, t.device, t.owner, t.owner)
}

// lifecycleResultJSON is the -o json shape of disable and enable.
type lifecycleResultJSON struct {
	Robot    string             `json:"robot,omitempty"`
	Device   string             `json:"device"`
	Result   string             `json:"result"`
	Disabled *api.FleetDisabled `json:"disabled,omitempty"`
}

// removeRobotJSON is the -o json shape of removing a robot.
type removeRobotJSON struct {
	Robot         string `json:"robot"`
	Device        string `json:"device"`
	DeviceDeleted bool   `json:"deviceDeleted"`
	Result        string `json:"result"`
}

// removeDeviceJSON is the -o json shape of removing a device.
type removeDeviceJSON struct {
	Device        string `json:"device"`
	DeviceDeleted bool   `json:"deviceDeleted"`
	Result        string `json:"result"`
}

// removeLifecycleErr maps a 409 of a removal to the refusal exit code (5), not the conflict one (8).
func removeLifecycleErr(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == http.StatusConflict {
		return &cliError{code: codeRejected, err: err}
	}
	return classify(err)
}

// lifecycleTimeout is the error of --wait that ran out of time.
func lifecycleTimeout(device string, d time.Duration) error {
	return newErr(codeTimeout, "timed out after %s waiting for device-controller; the change is made and continues: check with tnctl device %s", d, device)
}

// messageOr is device-controller's message, or fallback when it sent none.
func messageOr(message, fallback string) string {
	if message != "" {
		return message
	}
	return fallback
}

// endSentence ends s with a period unless it already ends with one (or with ! or ?), the way the
// platform's endsSentence does: appending sentences never doubles a period.
func endSentence(s string) string {
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// alreadyDisabledSentence is the answer to a disable of a device that is already disabled or being
// disabled: its state with who, when and why, and how to enable it.
func alreadyDisabledSentence(t *lifecycleTarget) string {
	state := "disabled"
	if t.disabled.State == api.DisabledStateDisabling {
		state = "being disabled"
	}
	s := endSentence(fmt.Sprintf("%s is already %s%s", t.thing(), state, t.disabled.Detail()))
	if t.disabled.Error {
		s += " It is not fully cut off yet: device-controller keeps trying."
	}
	return s + " Enable it again with: " + t.enableHint()
}

// waitDisabled follows a disable until device-controller confirms it.
func (e *cliEnv) waitDisabled(ctx context.Context, fl *api.Fleet, device string, timeout time.Duration) (*api.FleetDisabled, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	for {
		d, err := fl.Device(ctx, device)
		if err != nil {
			if ctx.Err() != nil {
				return nil, lifecycleTimeout(device, timeout)
			}
			return nil, classify(err)
		}
		dis := d.Disabled
		failed := dis != nil && dis.Error &&
			(dis.State == api.DisabledStateDisabled ||
				(dis.State == api.DisabledStateDisabling && time.Since(start) >= lifecycleErrorGrace))
		if failed {
			return dis, newErr(codeUnhealthy, "device %s is not fully disabled: %s (device-controller keeps trying; check with tnctl device %s)",
				device, messageOr(dis.Message, "device-controller reported an error"), device)
		}
		if dis != nil && dis.State == api.DisabledStateDisabled {
			return dis, nil
		}
		if dis == nil || dis.State == api.DisabledStateEnabling {
			// Someone enabled it while we waited: this disable is no longer what the platform does.
			return dis, newErr(codeConflict, "device %s was enabled again while waiting", device)
		}
		if err := e.sleepFor(ctx, lifecyclePollInterval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return dis, lifecycleTimeout(device, timeout)
			}
			return dis, classify(err)
		}
	}
}

// waitEnabled follows an enable until device-controller has removed the disabled state, or reports
// that enabling failed.
func (e *cliEnv) waitEnabled(ctx context.Context, fl *api.Fleet, device string, timeout time.Duration) (*api.FleetDisabled, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		d, err := fl.Device(ctx, device)
		if err != nil {
			if ctx.Err() != nil {
				return nil, lifecycleTimeout(device, timeout)
			}
			return nil, classify(err)
		}
		dis := d.Disabled
		if dis == nil {
			return nil, nil
		}
		if dis.State == api.DisabledStateDisabling || dis.State == api.DisabledStateDisabled {
			// Someone disabled it again while we waited: this enable is no longer what the platform does.
			return dis, newErr(codeConflict, "device %s was disabled again while waiting", device)
		}
		if dis.State == api.DisabledStateEnabling && dis.Error {
			return dis, newErr(codeUnhealthy, "enabling device %s failed: %s",
				device, messageOr(dis.Message, "device-controller reported an error"))
		}
		if err := e.sleepFor(ctx, lifecyclePollInterval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return dis, lifecycleTimeout(device, timeout)
			}
			return dis, classify(err)
		}
	}
}

// runDisable is `robot disable` and `device disable`.
func (e *cliEnv) runDisable(cmd *cobra.Command, kind lifecycleKind, name string, f *lifecycleFlags) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if f.wait && f.timeout <= 0 {
		return usageErr("--timeout must be positive")
	}
	reason, err := cleanReason(f.reason)
	if err != nil {
		return err
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	t, err := e.lifecycleGet(ctx, fl, kind, name)
	if err != nil {
		return err
	}
	if t.owner != "" {
		return errDeviceOwner(t)
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()

	result := "ok"
	disabled := t.disabled
	already := disabled != nil && (disabled.State == api.DisabledStateDisabling || disabled.State == api.DisabledStateDisabled)
	if already {
		result = "noop"
		if format == "table" {
			fmt.Fprintln(out, alreadyDisabledSentence(t))
		}
		if reason != "" {
			fmt.Fprintln(errw, "tnctl: the reason was not changed (enable and disable again to change it)")
		}
	} else {
		res, _, err := fl.DisableDevice(ctx, t.device, reason)
		if err != nil {
			return classify(err)
		}
		disabled = res.Disabled
		if format == "table" {
			fmt.Fprintf(out, "Disabled %s. %s\n", t.thing(), t.disableSentence())
		}
	}

	if f.wait {
		waited, werr := e.waitDisabled(ctx, fl, t.device, f.timeout)
		if waited != nil {
			disabled = waited
		}
		if werr != nil {
			return werr
		}
		if format == "table" {
			fmt.Fprintf(out, "device-controller confirmed: %s\n", messageOr(disabledMessage(disabled), "disabled"))
		}
	}

	if format != "table" {
		o := lifecycleResultJSON{Device: t.device, Result: result, Disabled: disabled}
		if t.kind == lifecycleRobot {
			o.Robot = t.robot
		}
		return writeStructured(out, format, marshalRaw(o))
	}
	return nil
}

// disabledMessage is the message of a disabled state, "" when there is none.
func disabledMessage(d *api.FleetDisabled) string {
	if d == nil {
		return ""
	}
	return d.Message
}

// runEnable is `robot enable` and `device enable`.
func (e *cliEnv) runEnable(cmd *cobra.Command, kind lifecycleKind, name string, f *lifecycleFlags) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if f.wait && f.timeout <= 0 {
		return usageErr("--timeout must be positive")
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	t, err := e.lifecycleGet(ctx, fl, kind, name)
	if err != nil {
		return err
	}
	if t.owner != "" {
		return errDeviceOwner(t)
	}
	out := cmd.OutOrStdout()

	result := "ok"
	disabled := t.disabled
	wait := f.wait
	switch {
	case disabled == nil:
		// Already enabled: there is nothing to wait for.
		result, wait = "noop", false
		if format == "table" {
			fmt.Fprintf(out, "%s is already enabled.\n", t.thing())
		}
	case disabled.State == api.DisabledStateEnabling:
		result = "noop"
		if format == "table" {
			fmt.Fprintf(out, "%s is already being enabled.\n", t.thing())
		}
	default:
		res, _, err := fl.EnableDevice(ctx, t.device)
		if err != nil {
			return classify(err)
		}
		disabled = res.Disabled
		if format == "table" {
			fmt.Fprintf(out, "Enabled %s. Its MQTT key is registered again and Tunneling comes back on; it reconnects by itself within a few minutes.\n", t.thing())
		}
	}

	if wait {
		waited, werr := e.waitEnabled(ctx, fl, t.device, f.timeout)
		if waited != nil {
			disabled = waited
		} else if werr == nil {
			disabled = nil
		}
		if werr != nil {
			return werr
		}
		if format == "table" {
			fmt.Fprintln(out, "device-controller confirmed: enabled")
		}
	}

	if format != "table" {
		o := lifecycleResultJSON{Device: t.device, Result: result, Disabled: disabled}
		if t.kind == lifecycleRobot {
			o.Robot = t.robot
		}
		return writeStructured(out, format, marshalRaw(o))
	}
	return nil
}

// runRemoveRobot is `robot remove`.
func (e *cliEnv) runRemoveRobot(cmd *cobra.Command, name string, f *lifecycleFlags) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	t, err := e.lifecycleGet(ctx, fl, lifecycleRobot, name)
	if err != nil {
		return err
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()

	if !f.yes {
		if format == "table" {
			if t.owner != "" {
				fmt.Fprintf(errw, "Removing robot %s deletes the robot; device %s stays (it belongs to robot %s). It cannot be undone.\n", t.robot, t.device, t.owner)
			} else {
				fmt.Fprintf(errw, "Removing robot %s deletes the robot and its device %s: its configuration overrides, local items, keys, MQTT access and tunnels. It cannot be undone.\n", t.robot, t.device)
			}
		}
		return usageErr("repeat with --yes to remove robot %s", t.robot)
	}

	res, _, err := fl.DeleteRobot(ctx, t.robot, t.owner == "")
	if err != nil {
		return removeLifecycleErr(err)
	}

	if format != "table" {
		return writeStructured(out, format, marshalRaw(removeRobotJSON{
			Robot: t.robot, Device: t.device, DeviceDeleted: res.DeviceDeleted, Result: "ok",
		}))
	}
	switch {
	case t.owner != "":
		fmt.Fprintf(out, "Removed robot %s. Device %s stays: it belongs to robot %s.\n", t.robot, t.device, t.owner)
	case res.DeviceDeleted:
		fmt.Fprintf(out, "Removed robot %s and its device %s.\n", t.robot, t.device)
	default:
		fmt.Fprintf(out, "Removed robot %s.\n", t.robot)
	}
	fmt.Fprintln(out, "k3s and the apps keep running on the hardware until you uninstall them.")
	return nil
}

// runRemoveDevice is `device remove`.
func (e *cliEnv) runRemoveDevice(cmd *cobra.Command, name string, f *lifecycleFlags) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	fl, _, _, err := e.targetClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	t, err := e.lifecycleGet(ctx, fl, lifecycleDevice, name)
	if err != nil {
		return err
	}
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()

	if !f.yes {
		if format == "table" {
			fmt.Fprintf(errw, "Removing device %s deletes its keys, MQTT access and tunnels. It cannot be undone.\n", t.device)
		}
		return usageErr("repeat with --yes to remove device %s", t.device)
	}

	if _, _, err := fl.DeleteDevice(ctx, t.device); err != nil {
		return removeLifecycleErr(err)
	}

	if format != "table" {
		return writeStructured(out, format, marshalRaw(removeDeviceJSON{
			Device: t.device, DeviceDeleted: true, Result: "ok",
		}))
	}
	fmt.Fprintf(out, "Removed device %s.\n", t.device)
	return nil
}

// ---------- the commands ----------

func newRobotDisableCmd(e *cliEnv) *cobra.Command {
	var f lifecycleFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "disable <robot> [--reason TEXT] [--wait] [--timeout DUR]",
		Short:        "Disable a robot: withdraw its MQTT key, close its sessions and turn Tunneling off",
		Long: `Disable the device of a robot. Within about a minute its MQTT key is withdrawn, its MQTT
sessions are closed and Tunneling is turned off. Its apps and k3s keep running.

Only a teamspace admin can do this (otherwise exit code 6). Disabling a robot that is already
disabled changes nothing and exits 0.

--wait follows device-controller: it returns once the disable is confirmed (exit code 4 when
--timeout runs out, 7 when device-controller reports it could not finish, for example because
Tunneling is still on, 8 when another admin enabled the robot again while waiting).`,
		Example: `  tnctl robot disable vision-station-01 --reason "Lost or stolen"
  tnctl robot disable vision-station-01 --wait`,
		Args: oneLifecycleArg("robot disable", "tnctl robot disable vision-station-01"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.runDisable(cmd, lifecycleRobot, args[0], &f)
		},
	}
	c.Flags().StringVar(&f.reason, "reason", "", "why it is disabled (one line, at most 200 characters)")
	addWaitFlags(c, &f)
	return c
}

func newRobotEnableCmd(e *cliEnv) *cobra.Command {
	var f lifecycleFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "enable <robot> [--wait] [--timeout DUR]",
		Short:        "Enable a robot again: register its MQTT key and bring Tunneling back",
		Long: `Enable the device of a robot again. Its MQTT key is registered again and Tunneling comes
back on; it reconnects by itself within a few minutes.

Only a teamspace admin can do this (otherwise exit code 6). Enabling a robot that is already
enabled changes nothing and exits 0.

--wait follows device-controller: it returns once the disable state is gone (exit code 4 when
--timeout runs out, 7 when enabling failed, 8 when another admin disabled the robot again while
waiting).`,
		Example: `  tnctl robot enable vision-station-01
  tnctl robot enable vision-station-01 --wait`,
		Args: oneLifecycleArg("robot enable", "tnctl robot enable vision-station-01"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.runEnable(cmd, lifecycleRobot, args[0], &f)
		},
	}
	addWaitFlags(c, &f)
	return c
}

func newRobotRemoveCmd(e *cliEnv) *cobra.Command {
	var f lifecycleFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "remove <robot> --yes",
		Short:        "Remove a robot (and its device, when the device is its own)",
		Long: `Remove a robot and, unless its device belongs to another robot, its device. The device's
configuration overrides, local items, keys, MQTT access and tunnels go. k3s and the apps keep
running on the hardware until you uninstall them.

Without --yes nothing is removed: tnctl prints what would go and exits 2. Only a teamspace
admin can do this (otherwise exit code 6). A platform that refuses the removal (for example
because the device also backs another robot) answers with exit code 5 and its own message.`,
		Example: `  tnctl robot remove vision-station-01 --yes`,
		Args:    oneLifecycleArg("robot remove", "tnctl robot remove vision-station-01 --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.runRemoveRobot(cmd, args[0], &f)
		},
	}
	c.Flags().BoolVar(&f.yes, "yes", false, "remove it without asking again")
	return c
}

func newDeviceDisableCmd(e *cliEnv) *cobra.Command {
	var f lifecycleFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "disable <device> [--reason TEXT] [--wait] [--timeout DUR]",
		Short:        "Disable a device: withdraw its MQTT key, close its sessions and turn Tunneling off",
		Long: `Disable a device. Within about a minute its MQTT key is withdrawn, its MQTT sessions are
closed and Tunneling is turned off. Its apps and k3s keep running.

Only a teamspace admin can do this (otherwise exit code 6). Disabling a device that is already
disabled changes nothing and exits 0.

--wait follows device-controller: it returns once the disable is confirmed (exit code 4 when
--timeout runs out, 7 when device-controller reports it could not finish, for example because
Tunneling is still on, 8 when another admin enabled the device again while waiting).`,
		Example: `  tnctl device disable vision-01 --reason "Lost or stolen"
  tnctl device disable vision-01 --wait`,
		Args: oneLifecycleArg("device disable", "tnctl device disable vision-01"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.runDisable(cmd, lifecycleDevice, args[0], &f)
		},
	}
	c.Flags().StringVar(&f.reason, "reason", "", "why it is disabled (one line, at most 200 characters)")
	addWaitFlags(c, &f)
	return c
}

func newDeviceEnableCmd(e *cliEnv) *cobra.Command {
	var f lifecycleFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "enable <device> [--wait] [--timeout DUR]",
		Short:        "Enable a device again: register its MQTT key and bring Tunneling back",
		Long: `Enable a device again. Its MQTT key is registered again and Tunneling comes back on; it
reconnects by itself within a few minutes.

Only a teamspace admin can do this (otherwise exit code 6). Enabling a device that is already
enabled changes nothing and exits 0.

--wait follows device-controller: it returns once the disable state is gone (exit code 4 when
--timeout runs out, 7 when enabling failed, 8 when another admin disabled the device again while
waiting).`,
		Example: `  tnctl device enable vision-01
  tnctl device enable vision-01 --wait`,
		Args: oneLifecycleArg("device enable", "tnctl device enable vision-01"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.runEnable(cmd, lifecycleDevice, args[0], &f)
		},
	}
	addWaitFlags(c, &f)
	return c
}

func newDeviceRemoveCmd(e *cliEnv) *cobra.Command {
	var f lifecycleFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "remove <device> --yes",
		Short:        "Remove a device that backs no robot",
		Long: `Remove a device that backs no robot. Its keys, MQTT access and tunnels go. A device that
backs a robot is refused with exit code 5 and the platform's message; remove the robot instead.

Without --yes nothing is removed: tnctl prints what would go and exits 2. Only a teamspace
admin can do this (otherwise exit code 6).`,
		Example: `  tnctl device remove vision-01 --yes`,
		Args:    oneLifecycleArg("device remove", "tnctl device remove vision-01 --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.runRemoveDevice(cmd, args[0], &f)
		},
	}
	c.Flags().BoolVar(&f.yes, "yes", false, "remove it without asking again")
	return c
}

// disabledLabel is the capitalised Label() used in the tables, for example "Not fully disabled".
func disabledLabel(d *api.FleetDisabled) string {
	l := d.Label()
	if l == "" {
		return ""
	}
	return strings.ToUpper(l[:1]) + l[1:]
}
