package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDisableDevice(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("POST /teknoir-demo/devices/dev/disable", 200, `{"device":"dev","disabled":{"state":"disabling","sessionsClosed":3}}`)
	res, _, err := f.DisableDevice(context.Background(), "dev", "Lost or stolen")
	if err != nil {
		t.Fatal(err)
	}
	if res.Device != "dev" || res.Disabled == nil || res.Disabled.State != DisabledStateDisabling || res.Disabled.SessionsClosed != 3 {
		t.Errorf("result %+v", res)
	}
	c := s.seen()[0]
	if c.Method != "POST" || c.Path != "/teknoir-demo/devices/dev/disable" {
		t.Errorf("call %+v", c)
	}
	if c.Body != `{"reason":"Lost or stolen"}` || !strings.HasPrefix(c.ContentType, "application/json") {
		t.Errorf("body %q, content type %q", c.Body, c.ContentType)
	}

	// no reason: no body and no Content-Type
	s.on("POST /teknoir-demo/devices/dev/disable", 200, `{"device":"dev","disabled":{"state":"disabling"}}`)
	if _, _, err := f.DisableDevice(context.Background(), "dev", ""); err != nil {
		t.Fatal(err)
	}
	c = s.seen()[1]
	if c.Body != "" || c.ContentType != "" {
		t.Errorf("empty reason sent body %q, content type %q", c.Body, c.ContentType)
	}
}

func TestEnableDevice(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("POST /teknoir-demo/devices/dev/enable", 200, `{"device":"dev","disabled":{"state":"enabling"}}`)
	res, _, err := f.EnableDevice(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if res.Device != "dev" || res.Disabled == nil || res.Disabled.State != DisabledStateEnabling {
		t.Errorf("result %+v", res)
	}
	c := s.seen()[0]
	if c.Method != "POST" || c.Path != "/teknoir-demo/devices/dev/enable" || c.Body != "" || c.ContentType != "" {
		t.Errorf("call %+v", c)
	}
}

func TestDeleteRobot(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("DELETE /teknoir-demo/robots/r", 200, `{"deleted":true,"deviceDeleted":true}`)
	res, _, err := f.DeleteRobot(context.Background(), "r", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Deleted || !res.DeviceDeleted {
		t.Errorf("result %+v", res)
	}
	c := s.seen()[0]
	if c.Method != "DELETE" || c.Path != "/teknoir-demo/robots/r" || c.Query != "deleteDevice=true" || c.Body != "" {
		t.Errorf("call %+v", c)
	}

	// always sends the query, also when the device stays
	s.on("DELETE /teknoir-demo/robots/r", 200, `{"deleted":true,"deviceDeleted":false}`)
	res, _, err = f.DeleteRobot(context.Background(), "r", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeviceDeleted {
		t.Errorf("result %+v", res)
	}
	if q := s.seen()[1].Query; q != "deleteDevice=false" {
		t.Errorf("query %q", q)
	}
}

func TestDeleteDevice(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("DELETE /teknoir-demo/devices/d", 200, `{"deleted":true}`)
	res, _, err := f.DeleteDevice(context.Background(), "d")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Deleted {
		t.Errorf("result %+v", res)
	}
	c := s.seen()[0]
	if c.Method != "DELETE" || c.Path != "/teknoir-demo/devices/d" || c.Query != "" || c.Body != "" {
		t.Errorf("call %+v", c)
	}
}

func TestLifecycleEscapesTheName(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("POST /teknoir-demo/devices/a%20b%2Fc/disable", 200, `{"device":"a b/c"}`)
	s.on("POST /teknoir-demo/devices/a%20b%2Fc/enable", 200, `{"device":"a b/c"}`)
	s.on("DELETE /teknoir-demo/robots/a%20b%2Fc", 200, `{"deleted":true}`)
	s.on("DELETE /teknoir-demo/devices/a%20b%2Fc", 200, `{"deleted":true}`)
	ctx := context.Background()
	if _, _, err := f.DisableDevice(ctx, "a b/c", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.EnableDevice(ctx, "a b/c"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.DeleteRobot(ctx, "a b/c", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.DeleteDevice(ctx, "a b/c"); err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, c := range s.seen() {
		paths = append(paths, c.Path)
	}
	want := "/teknoir-demo/devices/a%20b%2Fc/disable /teknoir-demo/devices/a%20b%2Fc/enable /teknoir-demo/robots/a%20b%2Fc /teknoir-demo/devices/a%20b%2Fc"
	if got := strings.Join(paths, " "); got != want {
		t.Errorf("paths %q, want %q", got, want)
	}
}

func TestLifecycleBare404MeansUnsupportedPlatform(t *testing.T) {
	s, f := newFleetStub(t)
	for _, route := range []string{
		"POST /teknoir-demo/devices/d/disable",
		"POST /teknoir-demo/devices/d/enable",
		"DELETE /teknoir-demo/robots/r",
		"DELETE /teknoir-demo/devices/d",
	} {
		s.on(route, 404, "")
	}
	ctx := context.Background()
	_, _, e1 := f.DisableDevice(ctx, "d", "")
	_, _, e2 := f.EnableDevice(ctx, "d")
	_, _, e3 := f.DeleteRobot(ctx, "r", true)
	_, _, e4 := f.DeleteDevice(ctx, "d")
	for i, err := range []error{e1, e2, e3, e4} {
		var up *UnsupportedPlatformError
		if !errors.As(err, &up) {
			t.Errorf("route %d: want *UnsupportedPlatformError, got %T %v", i, err, err)
			continue
		}
		if up.Feature != FeatureLifecycle {
			t.Errorf("route %d: feature %q", i, up.Feature)
		}
		if !strings.Contains(err.Error(), "disable, enable or remove robots and devices") {
			t.Errorf("route %d: message %q", i, err)
		}
	}
}

func TestLifecycle404WithMessageIsKept(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("POST /teknoir-demo/devices/ghost/disable", 404, `{"error":{"name":"NotFoundError","message":"Device \"ghost\" was not found in teamspace teknoir-demo."}}`)
	_, _, err := f.DisableDevice(context.Background(), "ghost", "")
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Message != `Device "ghost" was not found in teamspace teknoir-demo.` {
		t.Fatalf("want the platform's 404, got %T %v", err, err)
	}
	var up *UnsupportedPlatformError
	if errors.As(err, &up) {
		t.Error("a 404 with a message must not look like an old platform")
	}
}

func TestFleetDisabledDecoding(t *testing.T) {
	s, f := newFleetStub(t)
	s.on("GET /teknoir-demo/devices/dev", 200, `{"name":"dev","namespace":"teknoir-demo","disabled":{"state":"disabled","at":"2026-11-02T10:00:00Z","by":"anna@example.com","reason":"Lost or stolen","sessionsClosed":4,"message":"done","error":true}}`)
	s.on("GET /teknoir-demo/robots/r", 200, `{"name":"r","device":"dev","deviceOwner":"other","disabled":{"state":"disabling"}}`)
	d, err := f.Device(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if d.Disabled == nil {
		t.Fatal("no disabled state")
	}
	got := *d.Disabled
	if got.State != DisabledStateDisabled || got.At != "2026-11-02T10:00:00Z" || got.By != "anna@example.com" || got.Reason != "Lost or stolen" || got.SessionsClosed != 4 || got.Message != "done" || !got.Error {
		t.Errorf("disabled %+v", got)
	}
	r, _, err := f.Robot(context.Background(), "r")
	if err != nil {
		t.Fatal(err)
	}
	if r.DeviceOwner != "other" || r.Disabled == nil || r.Disabled.State != DisabledStateDisabling {
		t.Errorf("robot %+v", r.RobotSummary)
	}
	s.on("DELETE /teknoir-demo/robots/r", 200, `{"deleted":true,"deviceDeleted":false}`)
	del, _, err := f.DeleteRobot(context.Background(), "r", true)
	if err != nil || !del.Deleted || del.DeviceDeleted {
		t.Errorf("delete %+v %v", del, err)
	}
}

func TestFleetDisabledJSONNames(t *testing.T) {
	b, err := json.Marshal(FleetDisabled{State: DisabledStateDisabled})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"state":"disabled"}` {
		t.Errorf("zero fields encoded: %s", b)
	}
	var d FleetDisabled
	if err := json.Unmarshal([]byte(`{"state":"disabled","error":true}`), &d); err != nil || !d.Error {
		t.Errorf("error field: %+v %v", d, err)
	}
}

func TestFleetDisabledLabel(t *testing.T) {
	tests := []struct {
		state string
		err   bool
		want  string
	}{
		{DisabledStateDisabling, false, "disabling"},
		{DisabledStateDisabling, true, "not fully disabled"},
		{DisabledStateDisabled, false, "disabled"},
		{DisabledStateDisabled, true, "not fully disabled"},
		{DisabledStateEnabling, false, "enabling"},
		{DisabledStateEnabling, true, "enabling failed"},
	}
	for _, tc := range tests {
		if got := (&FleetDisabled{State: tc.state, Error: tc.err}).Label(); got != tc.want {
			t.Errorf("%s error=%v: got %q, want %q", tc.state, tc.err, got, tc.want)
		}
	}
	var nilDisabled *FleetDisabled
	if got := nilDisabled.Label(); got != "" {
		t.Errorf("nil label %q", got)
	}
}

func TestFleetDisabledDetail(t *testing.T) {
	d := &FleetDisabled{State: DisabledStateDisabled, At: "2026-11-02T10:00:00Z", By: "anna@example.com", Reason: "Lost or stolen"}
	if got := d.Detail(); got != " by anna@example.com on 2026-11-02: Lost or stolen" {
		t.Errorf("detail %q", got)
	}
	if got := (&FleetDisabled{State: DisabledStateEnabling}).Detail(); got != "" {
		t.Errorf("empty detail %q", got)
	}
	if got := (&FleetDisabled{State: DisabledStateDisabled, At: "not a time"}).Detail(); got != "" {
		t.Errorf("bad time detail %q", got)
	}
}
