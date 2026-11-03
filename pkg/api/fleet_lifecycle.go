package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// FeatureLifecycle is what a platform that predates the lifecycle routes cannot do yet.
const FeatureLifecycle = "disable, enable or remove robots and devices"

// DeviceLifecycleResult is the answer of disabling or enabling a device.
type DeviceLifecycleResult struct {
	Device   string         `json:"device"`
	Disabled *FleetDisabled `json:"disabled,omitempty"`
}

// DeleteRobotResult is the answer of removing a robot.
type DeleteRobotResult struct {
	Deleted       bool `json:"deleted"`
	DeviceDeleted bool `json:"deviceDeleted"`
}

// DeleteDeviceResult is the answer of removing a device.
type DeleteDeviceResult struct {
	Deleted bool `json:"deleted"`
}

// noLifecycleAPI turns the bare 404 of a missing lifecycle route (a device backend that predates it)
// into an *UnsupportedPlatformError. A 404 that carries the platform's own message (an unknown robot
// or device) is kept.
func noLifecycleAPI(err error) error {
	var ae *Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Message == "" {
		return &UnsupportedPlatformError{Version: Version, Feature: FeatureLifecycle}
	}
	return err
}

// DisableDevice disables a device. The reason is optional: "" sends no body.
func (f *Fleet) DisableDevice(ctx context.Context, name, reason string) (*DeviceLifecycleResult, json.RawMessage, error) {
	var body any
	if reason != "" {
		body = map[string]string{"reason": reason}
	}
	r, raw, err := decode[DeviceLifecycleResult](f.Do(ctx, "POST", "devices/"+url.PathEscape(name)+"/disable", nil, body))
	if err != nil {
		return nil, raw, noLifecycleAPI(err)
	}
	return &r, raw, nil
}

// EnableDevice enables a device again.
func (f *Fleet) EnableDevice(ctx context.Context, name string) (*DeviceLifecycleResult, json.RawMessage, error) {
	r, raw, err := decode[DeviceLifecycleResult](f.Do(ctx, "POST", "devices/"+url.PathEscape(name)+"/enable", nil, nil))
	if err != nil {
		return nil, raw, noLifecycleAPI(err)
	}
	return &r, raw, nil
}

// DeleteRobot removes a robot. deleteDevice also removes its device when the device backs no other
// robot; the platform refuses it (409) otherwise.
func (f *Fleet) DeleteRobot(ctx context.Context, name string, deleteDevice bool) (*DeleteRobotResult, json.RawMessage, error) {
	q := url.Values{"deleteDevice": {strconv.FormatBool(deleteDevice)}}
	r, raw, err := decode[DeleteRobotResult](f.Do(ctx, "DELETE", "robots/"+url.PathEscape(name), q, nil))
	if err != nil {
		return nil, raw, noLifecycleAPI(err)
	}
	return &r, raw, nil
}

// DeleteDevice removes a device that backs no robot; the platform refuses it (409) otherwise.
func (f *Fleet) DeleteDevice(ctx context.Context, name string) (*DeleteDeviceResult, json.RawMessage, error) {
	r, raw, err := decode[DeleteDeviceResult](f.Do(ctx, "DELETE", "devices/"+url.PathEscape(name), nil, nil))
	if err != nil {
		return nil, raw, noLifecycleAPI(err)
	}
	return &r, raw, nil
}
