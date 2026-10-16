package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// inventoryShim is the content of the executable inventory script that calls
// back into this tnctl with the domain and namespace pinned.
func inventoryShim(exe, configFile, dom, ns string) string {
	words := []string{shlexWord(exe)}
	if configFile != "" {
		words = append(words, "--config", shlexWord(configFile))
	}
	words = append(words, "-d", shlexWord(dom), "-n", shlexWord(ns), "ansible-inventory", `"$@"`)
	return "#!/bin/sh\nexec " + strings.Join(words, " ") + "\n"
}

// applyLeadingFlags consumes tnctl's own -d/-n/--config/--debug that precede
// the ansible arguments (flag parsing is disabled so ansible's flags pass
// through), and returns the rest.
func applyLeadingFlags(cmd *cobra.Command, args []string) ([]string, error) {
	pf := cmd.Root().PersistentFlags()
	reconfig := false
	for len(args) > 0 {
		a := args[0]
		name, val, hasVal := "", "", false
		switch {
		case a == "--debug":
			if err := pf.Set("debug", "true"); err != nil {
				return nil, err
			}
			args = args[1:]
			continue
		case a == "-d" || a == "--domain":
			name = "domain"
		case a == "-n" || a == "--namespace":
			name = "namespace"
		case a == "--config":
			name = "config"
		case strings.HasPrefix(a, "--domain="), strings.HasPrefix(a, "--namespace="), strings.HasPrefix(a, "--config="):
			kv := strings.SplitN(a[2:], "=", 2)
			name, val, hasVal = kv[0], kv[1], true
		default:
			return args, finishLeading(reconfig)
		}
		if !hasVal {
			if len(args) < 2 {
				return nil, fmt.Errorf("flag %s needs a value", a)
			}
			val = args[1]
			args = args[1:]
		}
		args = args[1:]
		if err := pf.Set(name, val); err != nil {
			return nil, err
		}
		if name == "config" {
			reconfig = true
		}
	}
	return args, finishLeading(reconfig)
}

func finishLeading(reconfig bool) error {
	if reconfig {
		initConfig()
	}
	return nil
}

// ensureInventoryShim writes the shim for domain/namespace and returns its path.
func ensureInventoryShim(cache, dom, ns string) (string, error) {
	exe, err := tnctlExe()
	if err != nil {
		return "", err
	}
	path := filepath.Join(cache, dom, ns, "inventory")
	if err := writeFileIfDifferent(path, []byte(inventoryShim(exe, cfgFile, dom, ns)), 0700); err != nil {
		return "", fmt.Errorf("cannot write the inventory script: %w", err)
	}
	return path, nil
}

func runAnsible(tool string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		args, err := applyLeadingFlags(cmd, args)
		if err != nil {
			return err
		}
		bin, err := exec.LookPath(tool)
		if err != nil {
			return fmt.Errorf("%s is not installed or not in PATH (install Ansible, e.g. 'pipx install ansible-core')", tool)
		}
		dom, ns, err := resolveCommon()
		if err != nil {
			return err
		}
		cache, err := ansibleCacheDir()
		if err != nil {
			return err
		}
		shim, err := ensureInventoryShim(cache, dom, ns)
		if err != nil {
			return err
		}
		c := exec.Command(bin, append([]string{"-i", shim}, args...)...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = runWithSignalForwarding(c)
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &exitError{code: ee.ExitCode()}
		}
		return err
	}
}

// quietExit keeps cobra from printing "Error: exit status N" for ansible's own
// exit status, while real tnctl errors are still printed.
func quietExit(f func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		err := f(cmd, args)
		var ee *exitError
		if err != nil && !errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return &exitError{code: 1}
		}
		return err
	}
}

func init() {
	for _, tool := range []string{"ansible", "ansible-playbook"} {
		rootCmd.AddCommand(&cobra.Command{
			Use:                tool + " [args...]",
			Short:              "Run " + tool + " with the devices of the namespace as inventory",
			Long:               "Runs " + tool + " with 'tnctl ansible-inventory' as inventory. All arguments are passed to " + tool + " untouched; tnctl's -d/-n/--config are accepted before them.",
			DisableFlagParsing: true,
			SilenceUsage:       true,
			SilenceErrors:      true,
			RunE:               quietExit(runAnsible(tool)),
		})
	}
}
