package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
)

// cliEnv is what the robots, config, exec, logs, ns, devices and device commands reach the outside world
// through, so tests can replace it: the fleet API, the device shell, time.
type cliEnv struct {
	// fleet returns the fleet API client plus the domain and namespace it talks to.
	fleet func() (client *api.Fleet, domain, namespace string, err error)
	// domainFleet returns a fleet API client for the routes that name no teamspace (the teamspace
	// list), so `tnctl ns` works before a namespace is chosen.
	domainFleet func() (client *api.Fleet, domain string, err error)
	// newExec opens a shell channel to a device (WireGuard SSH).
	newExec func(ctx context.Context, domain, namespace, device string) (deviceExec, error)
	// sleep waits d or until ctx ends.
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
	// pollInterval is the pause between two polls of --wait.
	pollInterval time.Duration
}

func realSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// defaultEnv is the env of the real tnctl.
var defaultEnv = &cliEnv{
	fleet: func() (*api.Fleet, string, string, error) {
		if on, apiBase, ns, keyFile, err := botMode(); err != nil {
			return nil, "", "", err
		} else if on {
			// A bot uses its own key and teamspace; it never reads the user's login or config.
			if err := checkBotNamespace(ns); err != nil {
				return nil, "", "", err
			}
			return botFleet(apiBase, ns, keyFile), botDomain(), ns, nil
		}
		cfg, err := config.Load()
		if err != nil {
			return nil, "", "", err
		}
		domain, ns := cfg.Domain, cfg.GetNamespace()
		if domain == "" || ns == "" {
			return nil, "", "", fmt.Errorf("domain and namespace are required. Please set them via flags or config")
		}
		return api.NewFleet(domain, ns), domain, ns, nil
	},
	domainFleet: func() (*api.Fleet, string, error) {
		if on, apiBase, ns, keyFile, err := botMode(); err != nil {
			return nil, "", err
		} else if on {
			if err := checkBotNamespace(ns); err != nil {
				return nil, "", err
			}
			return botFleet(apiBase, ns, keyFile), botDomain(), nil
		}
		cfg, err := config.Load()
		if err != nil {
			return nil, "", err
		}
		if cfg.Domain == "" {
			return nil, "", fmt.Errorf("a domain is required. Please set it via flag or config")
		}
		return api.NewFleet(cfg.Domain, ""), cfg.Domain, nil
	},
	sleep:        realSleep,
	now:          time.Now,
	pollInterval: 2 * time.Second,
}

// newExec is set in init: newDeviceExec reaches defaultEnv.fleet, and the compiler refuses a cycle
// through a variable's initializer.
func init() { defaultEnv.newExec = newDeviceExec }

// newDeviceExec is the real device shell. A TeknoirBot may not open one: it holds no device login,
// and the routes that hand one out are refused to it.
func newDeviceExec(ctx context.Context, domain, ns, device string) (deviceExec, error) {
	if on, _, _, _ := botEnabled(); on {
		return nil, botNoDeviceShell("")
	}
	return newWGExec(ctx, domain, ns, device)
}

func (e *cliEnv) sleepFor(ctx context.Context, d time.Duration) error {
	if e.sleep == nil {
		return realSleep(ctx, d)
	}
	return e.sleep(ctx, d)
}

func (e *cliEnv) clock() time.Time {
	if e.now == nil {
		return time.Now()
	}
	return e.now()
}

func (e *cliEnv) interval() time.Duration {
	if e.pollInterval <= 0 {
		return 2 * time.Second
	}
	return e.pollInterval
}
