package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/api"
)

// ---------- fixtures ----------

const robotFixture = `{"name":"robot-1","namespace":"teknoir-demo","type":"vision","device":"dev-1","phase":"Online","nodes":[],"layers":[],"fleets":[],"labels":{}}`

const robotOwnedFixture = `{"name":"robot-1","namespace":"teknoir-demo","type":"vision","device":"dev-1","deviceOwner":"robot-2","phase":"Online","nodes":[],"layers":[],"fleets":[],"labels":{}}`

const deviceFixture = `{"name":"dev-1","namespace":"teknoir-demo","tunnel":{"kind":"wireguard","label":"wireguard","wireguard":{"enabled":true,"annotation":"enabled","state":"active","active":true},"legacy":{"active":false}}}`

const deviceOffFixture = `{"name":"dev-1","namespace":"teknoir-demo","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}}}`

const disabledFixture = `{"name":"dev-1","namespace":"teknoir-demo","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"disabled","at":"2026-11-02T10:00:00Z","by":"anna@example.com","reason":"Lost or stolen","sessionsClosed":3,"message":"all sessions closed"}}`

const disablingFixture = `{"name":"dev-1","namespace":"teknoir-demo","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"disabling","at":"2026-11-02T10:00:00Z","by":"anna@example.com","reason":"Lost or stolen"}}`

const enablingFixture = `{"name":"dev-1","namespace":"teknoir-demo","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"enabling"}}`

// lifecycleEnv is a fake platform with the robot and device the lifecycle tests use.
func lifecycleEnv(t *testing.T) (*fakePlatform, *cliEnv) {
	t.Helper()
	p := newFakePlatform(t)
	p.on("GET", "/robots/robot-1", 200, robotFixture)
	p.on("GET", "/devices/dev-1", 200, deviceFixture)
	e, _ := testEnv(t, p, nil)
	return p, e
}

func mustExit(t *testing.T, err error, want int) {
	t.Helper()
	if got := exitCodeOf(err); got != want {
		t.Fatalf("exit code %d, want %d: %v", got, want, err)
	}
}

func unmarshalOut(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("output is not JSON (%v):\n%s", err, out)
	}
}

// ---------- 1: robot disable with a reason ----------

func TestRobotDisableWithReason(t *testing.T) {
	p, e := lifecycleEnv(t)
	p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling","by":"me@example.com","reason":"Lost or stolen"}}`)

	out, _, err := runCLI(t, e, "robot", "disable", "robot-1", "--reason", "Lost or stolen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Disabled robot robot-1 (device dev-1). Within about a minute its MQTT key is withdrawn") {
		t.Errorf("table output:\n%s", out)
	}
	posts := p.seen("POST", "/devices/dev-1/disable")
	if len(posts) != 1 || posts[0].Body != `{"reason":"Lost or stolen"}` {
		t.Errorf("posts %+v", posts)
	}

	out, _, err = runCLI(t, e, "robot", "disable", "robot-1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Robot    string             `json:"robot"`
		Device   string             `json:"device"`
		Result   string             `json:"result"`
		Disabled *api.FleetDisabled `json:"disabled"`
	}
	unmarshalOut(t, out, &got)
	if got.Robot != "robot-1" || got.Device != "dev-1" || got.Result != "ok" || got.Disabled == nil || got.Disabled.State != "disabling" {
		t.Errorf("json %+v", got)
	}
}

// ---------- 2: a device disable without a reason sends no body ----------

func TestDeviceDisableWithoutReasonSendsNoBody(t *testing.T) {
	p, e := lifecycleEnv(t)
	p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
	if _, _, err := runCLI(t, e, "device", "disable", "dev-1"); err != nil {
		t.Fatal(err)
	}
	posts := p.seen("POST", "/devices/dev-1/disable")
	if len(posts) != 1 || posts[0].Body != "" {
		t.Errorf("posts %+v", posts)
	}
}

// ---------- 3: an already disabled device ----------

func TestDisableAlreadyDisabledMakesNoPost(t *testing.T) {
	p, e := lifecycleEnv(t)
	p.on("GET", "/devices/dev-1", 200, disabledFixture)

	out, errb, err := runCLI(t, e, "device", "disable", "dev-1", "--reason", "A new reason")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "device dev-1 is already disabled by anna@example.com on 2026-11-02: Lost or stolen. Enable it again with: tnctl device enable dev-1") {
		t.Errorf("table output:\n%s", out)
	}
	if !strings.Contains(errb, "tnctl: the reason was not changed (enable and disable again to change it)") {
		t.Errorf("stderr:\n%s", errb)
	}
	if posts := p.seen("POST", "/devices/dev-1/disable"); len(posts) != 0 {
		t.Errorf("posts %+v", posts)
	}

	out, _, err = runCLI(t, e, "device", "disable", "dev-1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Device   string             `json:"device"`
		Result   string             `json:"result"`
		Disabled *api.FleetDisabled `json:"disabled"`
	}
	unmarshalOut(t, out, &got)
	if got.Result != "noop" || got.Device != "dev-1" || got.Disabled == nil || got.Disabled.State != "disabled" {
		t.Errorf("json %+v", got)
	}
}

// ---------- 4: enable of an enabled, enabling and disabled device ----------

func TestEnable(t *testing.T) {
	t.Run("already enabled", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		out, _, err := runCLI(t, e, "device", "enable", "dev-1")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "device dev-1 is already enabled.") {
			t.Errorf("out:\n%s", out)
		}
		if posts := p.seen("POST", "/devices/dev-1/enable"); len(posts) != 0 {
			t.Errorf("posts %+v", posts)
		}
		out, _, err = runCLI(t, e, "device", "enable", "dev-1", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Result   string             `json:"result"`
			Disabled *api.FleetDisabled `json:"disabled"`
		}
		unmarshalOut(t, out, &got)
		if got.Result != "noop" || got.Disabled != nil {
			t.Errorf("json %+v", got)
		}
	})

	t.Run("already being enabled", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices/dev-1", 200, enablingFixture)
		out, _, err := runCLI(t, e, "device", "enable", "dev-1")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "device dev-1 is already being enabled.") {
			t.Errorf("out:\n%s", out)
		}
		if posts := p.seen("POST", "/devices/dev-1/enable"); len(posts) != 0 {
			t.Errorf("posts %+v", posts)
		}
		out, _, err = runCLI(t, e, "device", "enable", "dev-1", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Result   string             `json:"result"`
			Disabled *api.FleetDisabled `json:"disabled"`
		}
		unmarshalOut(t, out, &got)
		if got.Result != "noop" || got.Disabled == nil || got.Disabled.State != "enabling" {
			t.Errorf("json %+v", got)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices/dev-1", 200, disabledFixture)
		p.on("POST", "/devices/dev-1/enable", 200, `{"device":"dev-1","disabled":{"state":"enabling"}}`)
		out, _, err := runCLI(t, e, "device", "enable", "dev-1")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Enabled device dev-1. Its MQTT key is registered again") {
			t.Errorf("out:\n%s", out)
		}
		if posts := p.seen("POST", "/devices/dev-1/enable"); len(posts) != 1 {
			t.Errorf("posts %+v", posts)
		}
		out, _, err = runCLI(t, e, "device", "enable", "dev-1", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Result   string             `json:"result"`
			Disabled *api.FleetDisabled `json:"disabled"`
		}
		unmarshalOut(t, out, &got)
		if got.Result != "ok" || got.Disabled == nil || got.Disabled.State != "enabling" {
			t.Errorf("json %+v", got)
		}
	})
}

// ---------- 5: disable while enabling makes the POST ----------

func TestDisableWhileEnablingPosts(t *testing.T) {
	p, e := lifecycleEnv(t)
	p.on("GET", "/devices/dev-1", 200, enablingFixture)
	p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
	if _, _, err := runCLI(t, e, "device", "disable", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if posts := p.seen("POST", "/devices/dev-1/disable"); len(posts) != 1 {
		t.Errorf("posts %+v", posts)
	}
}

// ---------- 6: a robot whose device belongs to another robot ----------

func TestRobotWithDeviceOwnerRefuses(t *testing.T) {
	for _, sub := range []string{"disable", "enable"} {
		t.Run(sub, func(t *testing.T) {
			p, e := lifecycleEnv(t)
			p.on("GET", "/robots/robot-1", 200, robotOwnedFixture)
			_, _, err := runCLI(t, e, "robot", sub, "robot-1")
			mustExit(t, err, codeRejected)
			want := "robot robot-1 runs on device dev-1, which belongs to robot robot-2: disable or enable robot robot-2 instead"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("err %v, want %q", err, want)
			}
			if posts := p.seen("POST", "/devices/dev-1/"+sub); len(posts) != 0 {
				t.Errorf("posts %+v", posts)
			}
		})
	}
}

// ---------- 7: robot remove ----------

func TestRobotRemove(t *testing.T) {
	t.Run("without --yes", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("DELETE", "/robots/robot-1", 200, `{"deleted":true,"deviceDeleted":true}`)
		_, errb, err := runCLI(t, e, "robot", "remove", "robot-1")
		mustExit(t, err, codeUsage)
		want := "Removing robot robot-1 deletes the robot and its device dev-1: its configuration overrides, local items, keys, MQTT access and tunnels. It cannot be undone."
		if !strings.Contains(errb, want) {
			t.Errorf("stderr:\n%s", errb)
		}
		if dels := p.seen("DELETE", "/robots/robot-1"); len(dels) != 0 {
			t.Errorf("deletes %+v", dels)
		}
	})

	t.Run("with --yes", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("DELETE", "/robots/robot-1", 200, `{"deleted":true,"deviceDeleted":true}`)
		out, _, err := runCLI(t, e, "robot", "remove", "robot-1", "--yes")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Removed robot robot-1 and its device dev-1.") {
			t.Errorf("out:\n%s", out)
		}
		if !strings.Contains(out, "k3s and the apps keep running on the hardware until you uninstall them.") {
			t.Errorf("out:\n%s", out)
		}
		dels := p.seen("DELETE", "/robots/robot-1")
		if len(dels) != 1 || dels[0].Query != "deleteDevice=true" {
			t.Errorf("deletes %+v", dels)
		}

		out, _, err = runCLI(t, e, "robot", "remove", "robot-1", "--yes", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Robot         string `json:"robot"`
			Device        string `json:"device"`
			DeviceDeleted bool   `json:"deviceDeleted"`
			Result        string `json:"result"`
		}
		unmarshalOut(t, out, &got)
		if got.Robot != "robot-1" || got.Device != "dev-1" || !got.DeviceDeleted || got.Result != "ok" {
			t.Errorf("json %+v", got)
		}
	})

	t.Run("device owner", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/robots/robot-1", 200, robotOwnedFixture)
		p.on("DELETE", "/robots/robot-1", 200, `{"deleted":true,"deviceDeleted":false}`)
		out, _, err := runCLI(t, e, "robot", "remove", "robot-1", "--yes")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Removed robot robot-1. Device dev-1 stays: it belongs to robot robot-2.") {
			t.Errorf("out:\n%s", out)
		}
		dels := p.seen("DELETE", "/robots/robot-1")
		if len(dels) != 1 || dels[0].Query != "deleteDevice=false" {
			t.Errorf("deletes %+v", dels)
		}
	})
}

// ---------- 8: device remove ----------

func TestDeviceRemove(t *testing.T) {
	t.Run("refused with 409", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		const refusal = `Device dev-1 backs robot robot-1. Remove the robot instead: deleting only the device makes the robot create it again with new keys.`
		p.on("DELETE", "/devices/dev-1", 409, `{"error":{"name":"ConflictError","message":`+jsonString(refusal)+`}}`)
		_, _, err := runCLI(t, e, "device", "remove", "dev-1", "--yes")
		mustExit(t, err, codeRejected)
		if err == nil || !strings.Contains(err.Error(), refusal) {
			t.Errorf("err %v, want the server's message", err)
		}
	})
	t.Run("removes it", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("DELETE", "/devices/dev-1", 200, `{"deleted":true}`)
		out, _, err := runCLI(t, e, "device", "remove", "dev-1", "--yes")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Removed device dev-1.") {
			t.Errorf("out:\n%s", out)
		}
		out, _, err = runCLI(t, e, "device", "remove", "dev-1", "--yes", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Device        string `json:"device"`
			DeviceDeleted bool   `json:"deviceDeleted"`
			Result        string `json:"result"`
		}
		unmarshalOut(t, out, &got)
		if got.Device != "dev-1" || !got.DeviceDeleted || got.Result != "ok" {
			t.Errorf("json %+v", got)
		}
	})
	t.Run("without --yes", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("DELETE", "/devices/dev-1", 200, `{"deleted":true}`)
		_, errb, err := runCLI(t, e, "device", "remove", "dev-1")
		mustExit(t, err, codeUsage)
		if !strings.Contains(errb, "Removing device dev-1 deletes its keys, MQTT access and tunnels. It cannot be undone.") {
			t.Errorf("stderr:\n%s", errb)
		}
		if dels := p.seen("DELETE", "/devices/dev-1"); len(dels) != 0 {
			t.Errorf("deletes %+v", dels)
		}
	})
}

// jsonString quotes s for a JSON body.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------- 9: 403 and a platform without the lifecycle routes ----------

func TestLifecycleErrors(t *testing.T) {
	t.Run("403 is exit 6", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		const msg = "Only a teamspace admin can disable or enable devices in teamspace teknoir-demo."
		p.on("POST", "/devices/dev-1/disable", 403, `{"error":{"name":"NotAllowedError","message":`+jsonString(msg)+`}}`)
		_, _, err := runCLI(t, e, "device", "disable", "dev-1")
		mustExit(t, err, codeAuth)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("bare 404 is an old platform", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("POST", "/devices/dev-1/disable", 404, "")
		_, _, err := runCLI(t, e, "device", "disable", "dev-1")
		mustExit(t, err, codeUsage)
		var up *api.UnsupportedPlatformError
		if !errors.As(err, &up) {
			t.Fatalf("want *UnsupportedPlatformError, got %T %v", err, err)
		}
		if up.Feature != api.FeatureLifecycle || !strings.Contains(err.Error(), "disable, enable or remove robots and devices") {
			t.Errorf("err %v", err)
		}
	})
}

// ---------- 9b: a robot without a device, and a --timeout that is not positive ----------

func TestRobotWithoutDeviceRefuses(t *testing.T) {
	const noDevice = `{"name":"robot-1","namespace":"teknoir-demo","type":"vision","phase":"Online","nodes":[],"layers":[],"fleets":[],"labels":{}}`
	p, e := lifecycleEnv(t)
	p.on("GET", "/robots/robot-1", 200, noDevice)
	for _, sub := range []string{"disable", "enable"} {
		_, _, err := runCLI(t, e, "robot", sub, "robot-1")
		mustExit(t, err, codeError)
		if err == nil || !strings.Contains(err.Error(), "robot robot-1 has no device") {
			t.Errorf("%s: err %v", sub, err)
		}
	}
	_, _, err := runCLI(t, e, "robot", "remove", "robot-1", "--yes")
	mustExit(t, err, codeError)
	if err == nil || !strings.Contains(err.Error(), "robot robot-1 has no device") {
		t.Errorf("remove: err %v", err)
	}
	if posts := p.seen("POST", "/devices/dev-1/disable"); len(posts) != 0 {
		t.Errorf("posts %+v", posts)
	}
	if posts := p.seen("POST", "/devices/dev-1/enable"); len(posts) != 0 {
		t.Errorf("posts %+v", posts)
	}
	if dels := p.seen("DELETE", "/robots/robot-1"); len(dels) != 0 {
		t.Errorf("deletes %+v", dels)
	}
}

func TestTimeoutWithoutWaitIsIgnored(t *testing.T) {
	p, e := lifecycleEnv(t)
	p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
	if _, _, err := runCLI(t, e, "device", "disable", "dev-1", "--timeout", "0s"); err != nil {
		t.Fatalf("err %v", err)
	}
	if posts := p.seen("POST", "/devices/dev-1/disable"); len(posts) != 1 {
		t.Errorf("posts %+v", posts)
	}
}

func TestWaitNeedsAPositiveTimeout(t *testing.T) {
	for _, sub := range []string{"disable", "enable"} {
		t.Run(sub, func(t *testing.T) {
			p, e := lifecycleEnv(t)
			_, _, err := runCLI(t, e, "device", sub, "dev-1", "--wait", "--timeout", "0s")
			mustExit(t, err, codeUsage)
			if err == nil || !strings.Contains(err.Error(), "--timeout must be positive") {
				t.Errorf("err %v", err)
			}
			if gets := p.seen("GET", "/devices/dev-1"); len(gets) != 0 {
				t.Errorf("requests %+v", gets)
			}
			if posts := p.seen("POST", "/devices/dev-1/"+sub); len(posts) != 0 {
				t.Errorf("posts %+v", posts)
			}
		})
	}
}

// ---------- 10: the reason is validated on the client ----------

func TestReasonValidation(t *testing.T) {
	const refused = "--reason must be one line of at most 200 characters"
	cases := []struct {
		name   string
		reason string
		ok     bool
	}{
		{"200 å is 200 UTF-16 units", strings.Repeat("å", 200), true},
		{"201 a", strings.Repeat("a", 201), false},
		{"101 emoji are 202 UTF-16 units", strings.Repeat("😀", 101), false},
		{"DEL", "a\x7fb", false},
		{"NEL", "a\u0085b", false},
		{"unicode line separator", "a b", false},
		{"line break", "one\ntwo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, e := lifecycleEnv(t)
			p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
			_, _, err := runCLI(t, e, "device", "disable", "dev-1", "--reason", tc.reason)
			posts := p.seen("POST", "/devices/dev-1/disable")
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				if len(posts) != 1 {
					t.Errorf("posts %+v", posts)
				}
				return
			}
			mustExit(t, err, codeUsage)
			if err == nil || !strings.Contains(err.Error(), refused) {
				t.Errorf("reason %q: err %v", tc.reason, err)
			}
			if len(posts) != 0 {
				t.Errorf("posts %+v", posts)
			}
		})
	}
}

// ---------- 11: --wait ----------

func TestWaitDisabled(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		old := lifecyclePollInterval
		lifecyclePollInterval = time.Millisecond
		t.Cleanup(func() { lifecyclePollInterval = old })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, deviceFixture},
			seqAnswer{200, disablingFixture},
			seqAnswer{200, disabledFixture})
		p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
		out, _, err := runCLI(t, e, "device", "disable", "dev-1", "--wait")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "device-controller confirmed: all sessions closed") {
			t.Errorf("out:\n%s", out)
		}
	})
	t.Run("not fully disabled", func(t *testing.T) {
		old := lifecyclePollInterval
		lifecyclePollInterval = time.Millisecond
		t.Cleanup(func() { lifecyclePollInterval = old })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		const failed = `{"name":"dev-1","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"disabled","error":true,"message":"Tunneling is still on"}}`
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, deviceFixture},
			seqAnswer{200, disablingFixture},
			seqAnswer{200, failed})
		p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
		_, _, err := runCLI(t, e, "device", "disable", "dev-1", "--wait")
		mustExit(t, err, codeUnhealthy)
		if err == nil || !strings.Contains(err.Error(), "device dev-1 is not fully disabled: Tunneling is still on") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("times out", func(t *testing.T) {
		old := lifecyclePollInterval
		lifecyclePollInterval = 2 * time.Millisecond
		t.Cleanup(func() { lifecyclePollInterval = old })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		p.on("GET", "/devices/dev-1", 200, disablingFixture)
		_, _, err := runCLI(t, e, "device", "disable", "dev-1", "--wait", "--timeout", "20ms")
		mustExit(t, err, codeTimeout)
		if err == nil || !strings.Contains(err.Error(), "timed out after 20ms waiting for device-controller; the change is made and continues: check with tnctl device dev-1") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("disabling with an error", func(t *testing.T) {
		old, oldGrace := lifecyclePollInterval, lifecycleErrorGrace
		lifecyclePollInterval, lifecycleErrorGrace = time.Millisecond, 5*time.Millisecond
		t.Cleanup(func() { lifecyclePollInterval, lifecycleErrorGrace = old, oldGrace })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		const failed = `{"name":"dev-1","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"disabling","error":true,"message":"Tunneling is still on"}}`
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, deviceFixture},
			seqAnswer{200, failed})
		p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
		_, _, err := runCLI(t, e, "device", "disable", "dev-1", "--wait")
		mustExit(t, err, codeUnhealthy)
		if err == nil || !strings.Contains(err.Error(), "device dev-1 is not fully disabled: Tunneling is still on (device-controller keeps trying; check with tnctl device dev-1)") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("an earlier error is not final at once", func(t *testing.T) {
		old, oldGrace := lifecyclePollInterval, lifecycleErrorGrace
		lifecyclePollInterval, lifecycleErrorGrace = time.Millisecond, time.Hour
		t.Cleanup(func() { lifecyclePollInterval, lifecycleErrorGrace = old, oldGrace })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		const stale = `{"name":"dev-1","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"disabling","error":true,"message":"Could not register the device key"}}`
		const done = `{"name":"dev-1","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"disabled","by":"anna@example.com","at":"2026-11-02T10:00:00Z","message":"Device key removed from JWKS."}}`
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, deviceFixture},
			seqAnswer{200, stale},
			seqAnswer{200, done})
		p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
		out, _, err := runCLI(t, e, "device", "disable", "dev-1", "--wait")
		if err != nil {
			t.Fatalf("err %v", err)
		}
		if !strings.Contains(out, "device-controller confirmed: Device key removed from JWKS.") {
			t.Errorf("out:\n%s", out)
		}
	})
	t.Run("disabled with an error is final at once", func(t *testing.T) {
		old, oldGrace := lifecyclePollInterval, lifecycleErrorGrace
		lifecyclePollInterval, lifecycleErrorGrace = time.Millisecond, time.Hour
		t.Cleanup(func() { lifecyclePollInterval, lifecycleErrorGrace = old, oldGrace })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		const failed = `{"name":"dev-1","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":true,"active":false},"legacy":{"active":false}},"disabled":{"state":"disabled","error":true,"message":"Tunneling is still on; device-controller keeps trying."}}`
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, deviceFixture},
			seqAnswer{200, failed})
		p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
		_, _, err := runCLI(t, e, "device", "disable", "dev-1", "--wait")
		mustExit(t, err, codeUnhealthy)
	})
	t.Run("enabled again while waiting", func(t *testing.T) {
		old := lifecyclePollInterval
		lifecyclePollInterval = time.Millisecond
		t.Cleanup(func() { lifecyclePollInterval = old })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, deviceFixture},
			seqAnswer{200, deviceFixture})
		p.on("POST", "/devices/dev-1/disable", 200, `{"device":"dev-1","disabled":{"state":"disabling"}}`)
		_, _, err := runCLI(t, e, "device", "disable", "dev-1", "--wait")
		mustExit(t, err, codeConflict)
		if err == nil || !strings.Contains(err.Error(), "device dev-1 was enabled again while waiting") {
			t.Errorf("err %v", err)
		}
	})
}

func TestWaitEnabled(t *testing.T) {
	pollMillisecond := func(t *testing.T) {
		t.Helper()
		old := lifecyclePollInterval
		lifecyclePollInterval = time.Millisecond
		t.Cleanup(func() { lifecyclePollInterval = old })
	}

	t.Run("confirmed", func(t *testing.T) {
		pollMillisecond(t)
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, disabledFixture},
			seqAnswer{200, enablingFixture},
			seqAnswer{200, deviceFixture})
		p.on("POST", "/devices/dev-1/enable", 200, `{"device":"dev-1","disabled":{"state":"enabling"}}`)
		out, _, err := runCLI(t, e, "device", "enable", "dev-1", "--wait")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "device-controller confirmed: enabled") {
			t.Errorf("out:\n%s", out)
		}
	})
	t.Run("enabling failed", func(t *testing.T) {
		pollMillisecond(t)
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		const failed = `{"name":"dev-1","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}},"disabled":{"state":"enabling","error":true,"message":"key registration failed"}}`
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, disabledFixture},
			seqAnswer{200, failed})
		p.on("POST", "/devices/dev-1/enable", 200, `{"device":"dev-1","disabled":{"state":"enabling"}}`)
		_, _, err := runCLI(t, e, "device", "enable", "dev-1", "--wait")
		mustExit(t, err, codeUnhealthy)
		if err == nil || !strings.Contains(err.Error(), "enabling device dev-1 failed: key registration failed") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("times out", func(t *testing.T) {
		old := lifecyclePollInterval
		lifecyclePollInterval = 2 * time.Millisecond
		t.Cleanup(func() { lifecyclePollInterval = old })
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		p.on("GET", "/devices/dev-1", 200, enablingFixture)
		_, _, err := runCLI(t, e, "device", "enable", "dev-1", "--wait", "--timeout", "20ms")
		mustExit(t, err, codeTimeout)
		if err == nil || !strings.Contains(err.Error(), "timed out after 20ms waiting for device-controller") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("disabled again while waiting", func(t *testing.T) {
		pollMillisecond(t)
		p, e := lifecycleEnv(t)
		e.sleep = realSleep
		p.onSeq("GET", "/devices/dev-1",
			seqAnswer{200, disabledFixture},
			seqAnswer{200, enablingFixture},
			seqAnswer{200, disabledFixture})
		p.on("POST", "/devices/dev-1/enable", 200, `{"device":"dev-1","disabled":{"state":"enabling"}}`)
		_, _, err := runCLI(t, e, "device", "enable", "dev-1", "--wait")
		mustExit(t, err, codeConflict)
		if err == nil || !strings.Contains(err.Error(), "device dev-1 was disabled again while waiting") {
			t.Errorf("err %v", err)
		}
	})
}

// ---------- 12: a disabled device blocks the connecting commands ----------

func TestDisabledDeviceBlocksConnections(t *testing.T) {
	const want = "device dev-1 is disabled by anna@example.com on 2026-11-02: Lost or stolen. It cannot be reached until a teamspace admin enables it: tnctl device enable dev-1"

	t.Run("ssh", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices/dev-1", 200, disabledFixture)
		_, _, err := runGlobal(t, e, "ssh", "dev-1")
		mustExit(t, err, codeRejected)
		if err == nil || err.Error() != want {
			t.Errorf("err %q, want %q", err, want)
		}
		var buf bytes.Buffer
		writeError(&buf, err, "json")
		if !strings.Contains(buf.String(), `"code":"device_disabled"`) {
			t.Errorf("json error:\n%s", buf.String())
		}
		if posts := p.seen("POST", "/devices/dev-1/credentials"); len(posts) != 0 {
			t.Errorf("asked for credentials: %+v", posts)
		}
	})

	t.Run("port-forward", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices/dev-1", 200, disabledFixture)
		_, _, err := runGlobal(t, e, "port-forward", "dev-1", "grafana")
		mustExit(t, err, codeRejected)
		if err == nil || err.Error() != want {
			t.Errorf("err %q", err)
		}
		if posts := p.seen("POST", "/devices/dev-1/credentials"); len(posts) != 0 {
			t.Errorf("asked for credentials: %+v", posts)
		}
	})

	t.Run("exec", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices/dev-1", 200, disabledFixture)
		p.on("GET", "/config/dev-1", 200, `{"target":{"kind":"device","name":"dev-1","device":"dev-1"},"configuration":{"resources":[],"effective":[],"deviations":[],"warnings":[]},"overrides":{},"overrideStatus":[],"overrideCount":0}`)
		fl, _, _, _ := e.fleet()
		e.newExec = func(ctx context.Context, domain, ns, d string) (deviceExec, error) {
			return newWGExecWith(ctx, fl, domain, ns, d)
		}
		_, _, err := runCLI(t, e, "exec", "dev-1", "--", "true")
		mustExit(t, err, codeRejected)
		if err == nil || err.Error() != want {
			t.Errorf("err %q", err)
		}
		if posts := p.seen("POST", "/devices/dev-1/credentials"); len(posts) != 0 {
			t.Errorf("asked for credentials: %+v", posts)
		}
	})

	t.Run("socks-proxy", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices/dev-1", 200, disabledFixture)
		_, _, err := runGlobal(t, e, "socks-proxy", "dev-1")
		mustExit(t, err, codeRejected)
		if err == nil || err.Error() != want {
			t.Errorf("err %q", err)
		}
		if posts := p.seen("POST", "/devices/dev-1/credentials"); len(posts) != 0 {
			t.Errorf("asked for credentials: %+v", posts)
		}
	})

	t.Run("logs", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices/dev-1", 200, disabledFixture)
		p.on("GET", "/config/dev-1", 200, `{"target":{"kind":"device","name":"dev-1","device":"dev-1"},"configuration":{"resources":[],"effective":[],"deviations":[],"warnings":[]},"overrides":{},"overrideStatus":[],"overrideCount":0}`)
		fl, _, _, _ := e.fleet()
		e.newExec = func(ctx context.Context, domain, ns, d string) (deviceExec, error) {
			return newWGExecWith(ctx, fl, domain, ns, d)
		}
		_, _, err := runCLI(t, e, "logs", "dev-1", "Deployment/default/whoami")
		mustExit(t, err, codeRejected)
		if err == nil || err.Error() != want {
			t.Errorf("err %q", err)
		}
		if posts := p.seen("POST", "/devices/dev-1/credentials"); len(posts) != 0 {
			t.Errorf("asked for credentials: %+v", posts)
		}
	})

	t.Run("enabling with no tunnel", func(t *testing.T) {
		pp, ee := lifecycleEnv(t)
		pp.on("GET", "/devices/dev-1", 200, enablingFixture)
		_, _, err := runGlobal(t, ee, "ssh", "dev-1")
		mustExit(t, err, codeError)
		if err == nil || !strings.Contains(err.Error(), "device dev-1 is being enabled again; Tunneling comes back within a few minutes. See: tnctl device dev-1") {
			t.Errorf("err %v", err)
		}
	})
}

// ---------- 13: the state in the tables ----------

func TestDisabledStateInTheViews(t *testing.T) {
	const robotDisabled = `{"name":"robot-1","namespace":"teknoir-demo","type":"vision","device":"dev-1","phase":"Online","phaseMessage":"Disabled by anna@example.com on 2026-11-02: Lost or stolen. It keeps running its apps but cannot reach the platform.","nodes":[],"layers":[],"fleets":[],"labels":{},"disabled":{"state":"disabled"}}`

	t.Run("robots", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/robots", 200, "["+robotDisabled+"]")
		out, _, err := runCLI(t, e, "robots")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Disabled") {
			t.Errorf("robots:\n%s", out)
		}
	})

	t.Run("robot", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/robots/robot-1", 200, robotDisabled)
		out, _, err := runCLI(t, e, "robot", "robot-1")
		if err != nil {
			t.Fatal(err)
		}
		// The server's phase message is the whole sentence: no "Disabled: " label in front of it.
		if !strings.Contains(out, "Disabled by anna@example.com on 2026-11-02: Lost or stolen. It keeps running its apps but cannot reach the platform.") {
			t.Errorf("robot phase:\n%s", out)
		}
		if strings.Contains(out, "Disabled: Disabled by") {
			t.Errorf("the phase message is repeated after a label:\n%s", out)
		}
	})

	t.Run("robot with a device owner", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/robots/robot-1", 200, robotOwnedFixture)
		out, _, err := runCLI(t, e, "robot", "robot-1")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Device owner") || !strings.Contains(out, "dev-1 belongs to robot robot-2; disable, enable and remove its device there") {
			t.Errorf("robot owner:\n%s", out)
		}
	})

	t.Run("devices and device", func(t *testing.T) {
		p, e := lifecycleEnv(t)
		p.on("GET", "/devices", 200, "["+disabledFixture+"]")
		p.on("GET", "/devices/dev-1", 200, disabledFixture)
		bufDevices := capturePterm(t)
		if _, _, err := runCLI(t, e, "devices"); err != nil {
			t.Fatal(err)
		}
		devicesOut := bufDevices.String()
		row := ""
		for _, line := range strings.Split(devicesOut, "\n") {
			if strings.Contains(line, "dev-1") {
				row = line
				break
			}
		}
		if !strings.Contains(row, "disabled") || strings.Contains(row, " off ") {
			t.Errorf("devices tunnel label, row %q:\n%s", row, devicesOut)
		}
		bufDevice := capturePterm(t)
		if _, _, err := runCLI(t, e, "device", "dev-1"); err != nil {
			t.Fatal(err)
		}
		deviceOut := bufDevice.String()
		if !strings.Contains(deviceOut, "Access") || !strings.Contains(deviceOut, "disabled by anna@example.com on 2026-11-02: Lost or stolen") {
			t.Errorf("device access row:\n%s", deviceOut)
		}
	})
}

// ---------- 14: the existing commands still work ----------

func TestExistingRobotAndDeviceCommands(t *testing.T) {
	p, e := lifecycleEnv(t)
	out, _, err := runCLI(t, e, "robot", "robot-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Robot") || !strings.Contains(out, "robot-1") {
		t.Errorf("robot:\n%s", out)
	}
	capturePterm(t)
	if _, _, err := runCLI(t, e, "device", "dev-1"); err != nil {
		t.Fatal(err)
	}
	_, _, err = runCLI(t, e, "robot")
	mustExit(t, err, codeUsage)

	// A robot named disable is shadowed by the subcommand: with no argument it is a usage error,
	// not the robot.
	_, _, err = runCLI(t, e, "robot", "disable")
	mustExit(t, err, codeUsage)
	_ = p
}

// ---------- 15: a name the subcommands shadow is reached with -- ----------

func TestDashDashReachesAnUnluckyName(t *testing.T) {
	p, e := lifecycleEnv(t)
	const robotNamedDisable = `{"name":"disable","namespace":"teknoir-demo","type":"vision","device":"dev-9","phase":"Online","nodes":[],"layers":[],"fleets":[],"labels":{}}`
	const deviceNamedDisable = `{"name":"disable","namespace":"teknoir-demo","tunnel":{"kind":"off","label":"off","wireguard":{"enabled":false,"active":false},"legacy":{"active":false}}}`

	p.on("GET", "/robots/disable", 200, robotNamedDisable)
	out, _, err := runCLI(t, e, "robot", "--", "disable")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Robot") || !strings.Contains(out, "disable") {
		t.Errorf("robot -- disable:\n%s", out)
	}
	if gets := p.seen("GET", "/robots/disable"); len(gets) != 1 {
		t.Errorf("robot gets %+v", gets)
	}

	p.on("GET", "/devices/disable", 200, deviceNamedDisable)
	buf := capturePterm(t)
	if _, _, err := runCLI(t, e, "device", "--", "disable"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "disable") {
		t.Errorf("device -- disable:\n%s", buf.String())
	}
	if gets := p.seen("GET", "/devices/disable"); len(gets) != 1 {
		t.Errorf("device gets %+v", gets)
	}
	if posts := p.seen("POST", "/devices/dev-1/disable"); len(posts) != 0 {
		t.Errorf("the subcommand ran: %+v", posts)
	}
}

// ---------- unit tests ----------

func TestEndSentence(t *testing.T) {
	for in, want := range map[string]string{
		"Lost or stolen":  "Lost or stolen.",
		"Lost or stolen.": "Lost or stolen.",
		"Lost or stolen!": "Lost or stolen!",
		"Lost or stolen?": "Lost or stolen?",
	} {
		if got := endSentence(in); got != want {
			t.Errorf("endSentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// A reason that already ends with a period must not give ".." in either message: both reasons
// produce the same sentence.
func TestDisabledMessagesEndTheSentenceOnce(t *testing.T) {
	const wantMsg = "device dev is disabled by anna@example.com on 2026-11-02: Lost or stolen. It cannot be reached until a teamspace admin enables it: tnctl device enable dev"
	const wantOut = "device dev-1 is already disabled by anna@example.com on 2026-11-02: Lost or stolen. Enable it again with: tnctl device enable dev-1"
	for _, reason := range []string{"Lost or stolen", "Lost or stolen."} {
		t.Run(reason, func(t *testing.T) {
			dis := &api.FleetDisabled{State: api.DisabledStateDisabled, At: "2026-11-02T10:00:00Z", By: "anna@example.com", Reason: reason}
			if msg := errDeviceDisabled(&api.FleetDevice{Disabled: dis}, "dev").Error(); msg != wantMsg {
				t.Errorf("transport error:\n got %q\nwant %q", msg, wantMsg)
			}

			p, e := lifecycleEnv(t)
			fixture := strings.Replace(disabledFixture, `"reason":"Lost or stolen"`, `"reason":`+jsonString(reason), 1)
			p.on("GET", "/devices/dev-1", 200, fixture)
			out, _, err := runCLI(t, e, "device", "disable", "dev-1")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, wantOut) {
				t.Errorf("already-disabled message:\n got %q\nwant containing %q", out, wantOut)
			}
		})
	}
}

func TestErrDeviceDisabled(t *testing.T) {
	full := &api.FleetDisabled{State: api.DisabledStateDisabled, At: "2026-11-02T10:00:00Z", By: "anna@example.com", Reason: "Lost or stolen"}
	tests := []struct {
		name string
		dev  *api.FleetDevice
		want string
	}{
		{"nil device", nil, ""},
		{"no disabled", &api.FleetDevice{Name: "dev"}, ""},
		{"disabling", &api.FleetDevice{Disabled: &api.FleetDisabled{State: api.DisabledStateDisabling}}, "device dev is disabled. It cannot be reached"},
		{"disabling with error", &api.FleetDevice{Disabled: &api.FleetDisabled{State: api.DisabledStateDisabling, Error: true}}, "device dev is disabled. It cannot be reached"},
		{"disabled", &api.FleetDevice{Disabled: full}, "device dev is disabled by anna@example.com on 2026-11-02: Lost or stolen. It cannot be reached until a teamspace admin enables it: tnctl device enable dev"},
		{"disabled with error", &api.FleetDevice{Disabled: &api.FleetDisabled{State: api.DisabledStateDisabled, Error: true}}, "device dev is disabled. It cannot be reached"},
		{"enabling", &api.FleetDevice{Disabled: &api.FleetDisabled{State: api.DisabledStateEnabling}}, ""},
		{"enabling with error", &api.FleetDevice{Disabled: &api.FleetDisabled{State: api.DisabledStateEnabling, Error: true}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := errDeviceDisabled(tc.dev, "dev")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want containing %q", err, tc.want)
			}
			mustExit(t, err, codeRejected)
			var buf bytes.Buffer
			writeError(&buf, err, "json")
			if !strings.Contains(buf.String(), `"code":"device_disabled"`) {
				t.Errorf("json error:\n%s", buf.String())
			}
		})
	}
}

func TestAlreadyDisabledSentence(t *testing.T) {
	at := "2026-11-02T10:00:00Z"
	for _, tc := range []struct {
		name string
		dis  api.FleetDisabled
		want string
	}{
		{"disabled", api.FleetDisabled{State: api.DisabledStateDisabled, By: "anna@example.com", At: at, Reason: "Lost or stolen."},
			"device dev-1 is already disabled by anna@example.com on 2026-11-02: Lost or stolen. Enable it again with: tnctl device enable dev-1"},
		{"being disabled", api.FleetDisabled{State: api.DisabledStateDisabling, By: "anna@example.com", At: at},
			"device dev-1 is already being disabled by anna@example.com on 2026-11-02. Enable it again with: tnctl device enable dev-1"},
		{"not fully", api.FleetDisabled{State: api.DisabledStateDisabled, By: "anna@example.com", At: at, Error: true, Message: "Tunneling is still on"},
			"device dev-1 is already disabled by anna@example.com on 2026-11-02. It is not fully cut off yet: device-controller keeps trying. Enable it again with: tnctl device enable dev-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dis := tc.dis
			got := alreadyDisabledSentence(&lifecycleTarget{kind: lifecycleDevice, name: "dev-1", device: "dev-1", disabled: &dis})
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}
