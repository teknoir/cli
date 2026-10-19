package cmd

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/teknoir/cli/pkg/api"
)

// pickDevice returns preset when it is set, else lets the user choose one of the teamspace's devices.
func pickDevice(ctx context.Context, fl *api.Fleet, namespace, preset string) (string, error) {
	if preset != "" {
		return preset, nil
	}
	devices, err := fl.DeviceNames(ctx)
	if err != nil {
		return "", classify(err)
	}
	if len(devices) == 0 {
		return "", fmt.Errorf("no devices found in namespace %s", namespace)
	}
	idx, err := fuzzyfinder.Find(devices, func(i int) string {
		return devices[i]
	})
	if err != nil {
		return "", err
	}
	return devices[idx], nil
}

// presetDevice is the device named by the first argument or --device, empty when there is none.
func presetDevice(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return device
}

// loadLogin fetches the login of a device from the platform. The platform lets only developers who
// may open a terminal read it (a 403 with the reason); the values are never printed or logged.
func loadLogin(ctx context.Context, fl *api.Fleet, deviceName string) (*api.FleetDeviceCredentials, error) {
	c, err := fl.Credentials(ctx, deviceName)
	if err != nil {
		return nil, classify(err)
	}
	return c, nil
}

// errNoKeyPair is the answer while the platform has not generated the device's key pair yet.
func errNoKeyPair(namespace, deviceName string) error {
	return fmt.Errorf("device %s/%s has no key pair yet (the platform creates it a few minutes after the device is created); run the command again shortly", namespace, deviceName)
}

// decodeDeviceLogin decodes the base64 username, password and RSA private key of a device's
// login. password is empty when the device has none.
func decodeDeviceLogin(c *api.FleetDeviceCredentials) (username, password string, privateKey []byte, err error) {
	usernameBytes, err := base64.StdEncoding.DecodeString(c.Username)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to decode username: %w", err)
	}
	username = string(usernameBytes)

	if c.Password != "" {
		passwordBytes, err := base64.StdEncoding.DecodeString(c.Password)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to decode password: %w", err)
		}
		password = string(passwordBytes)
	}

	privateKey, err = base64.StdEncoding.DecodeString(c.RSAPrivate)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to decode RSA private key: %w", err)
	}
	return username, password, privateKey, nil
}
