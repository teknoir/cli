package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
)

// cliEnv is what the robots, config, exec and logs commands reach the outside world
// through, so tests can replace it: the fleet API, the device shell, time.
type cliEnv struct {
	// fleet returns the fleet API client plus the domain and namespace it talks to.
	fleet func() (client *api.Fleet, domain, namespace string, err error)
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
	newExec:      newWGExec,
	sleep:        realSleep,
	now:          time.Now,
	pollInterval: 2 * time.Second,
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
