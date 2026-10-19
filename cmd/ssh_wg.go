package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
	wgapi "github.com/teknoir/wg-tunnel/pkg/api"
)

// proxyArgs are the arguments of the `tnctl wg-proxy` ProxyCommand.
type proxyArgs struct {
	Exe            string // absolute path of this tnctl executable
	ConfigFile     string // --config, only when the user set it
	Domain         string
	Namespace      string
	Device         string
	Port           int
	KnownHostsFile string
	TURNServer     string // optional
	P2P            bool
	HubURL         string
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@+=:,./-]+$`)

// quoteArg quotes one word for the shell ssh runs a ProxyCommand with: sh on
// unix, cmd.exe on windows. It does NOT escape '%' (see buildProxyCommand).
func quoteArg(s, goos string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	if goos == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildProxyCommand returns the value of ssh's ProxyCommand option. ssh
// expands %-tokens (%h, %p, ...) in it, so every literal '%' is doubled.
func buildProxyCommand(a proxyArgs, goos string) string {
	words := []string{a.Exe, "wg-proxy"}
	if a.ConfigFile != "" {
		words = append(words, "--config", a.ConfigFile)
	}
	words = append(words,
		"--domain", a.Domain,
		"--namespace", a.Namespace,
		"--device", a.Device,
		"--port", strconv.Itoa(a.Port),
		"--known-hosts-file", a.KnownHostsFile,
	)
	if a.TURNServer != "" {
		words = append(words, "--turn-server", a.TURNServer)
	}
	words = append(words, "--hub-url", a.HubURL)
	if a.P2P {
		words = append(words, "--p2p")
	}

	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quoteArg(w, goos)
	}
	return strings.ReplaceAll(strings.Join(quoted, " "), "%", "%%")
}

// buildSSHArgs returns the argument list for the system ssh. The device host
// key is pinned: wg-proxy writes the key the hub vouches for to knownHosts
// before ssh sees any byte, and ssh refuses any other key.
func buildSSHArgs(proxyCommand, knownHosts, hostKeyAlias, keyFile, username, deviceName string) []string {
	return []string{
		"-o", "ProxyCommand=" + proxyCommand,
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "HostKeyAlias=" + hostKeyAlias,
		"-o", "ServerAliveInterval=30",
		"-o", "IdentitiesOnly=yes",
		"-i", keyFile,
		username + "@" + deviceName,
	}
}

// validLoginName guards the user@host argument against option injection.
func validLoginName(u string) error {
	if u == "" || strings.HasPrefix(u, "-") || strings.ContainsAny(u, "@ \t\r\n\x00") {
		return errors.New("the device login name is not usable for ssh")
	}
	return nil
}

// wgSSHSession is what ssh needs to reach a device over its WireGuard tunnel: the argument
// list (ending in user@device) and a 0700 directory with the private key (0600) and the
// known_hosts file that wg-proxy fills in. Close removes the directory.
type wgSSHSession struct {
	baseArgs []string
	password string
	tmpDir   string
	device   string
}

// openWGSSH prepares an ssh to the device over WireGuard (the same device login as the
// legacy path, carried by a WireGuard session that wg-proxy brokers). The caller must Close it.
func openWGSSH(domain, ns, deviceName string, login *api.FleetDeviceCredentials) (*wgSSHSession, error) {
	if err := validateWGTarget(ns, deviceName, 22); err != nil {
		return nil, err
	}
	username, password, privateKey, err := decodeDeviceLogin(login)
	if err != nil {
		return nil, err
	}
	if err := validLoginName(username); err != nil {
		return nil, err
	}

	hubURL := resolveWGOption(sshWGHubURL, envWGHub, defaultHubURL(domain))
	if err := validateHubURL(hubURL); err != nil {
		return nil, err
	}
	if err := validateHubHost(hubURL, domain); err != nil {
		return nil, err
	}
	turn := resolveWGOption(sshTURNServer, envWGTurnServer, "")

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate the tnctl executable: %w", err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return nil, fmt.Errorf("cannot locate the tnctl executable: %w", err)
	}

	// 0700 directory with the private key (0600) and the known_hosts file that
	// wg-proxy fills in; removed when ssh ends.
	tmpDir, err := os.MkdirTemp("", "tnctl-wg-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}

	keyFile := filepath.Join(tmpDir, "id")
	if err := os.WriteFile(keyFile, privateKey, 0600); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to write private key to temporary file: %w", err)
	}
	knownHosts := filepath.Join(tmpDir, "known_hosts")

	proxy := buildProxyCommand(proxyArgs{
		Exe: exe, ConfigFile: cfgFile, Domain: domain, Namespace: ns, Device: deviceName, Port: 22,
		KnownHostsFile: knownHosts, TURNServer: turn, HubURL: hubURL, P2P: !sshNoP2P,
	}, runtime.GOOS)

	return &wgSSHSession{
		baseArgs: buildSSHArgs(proxy, knownHosts, wgapi.KnownHostsAlias(ns, deviceName), keyFile, username, deviceName),
		password: password,
		tmpDir:   tmpDir,
		device:   deviceName,
	}, nil
}

// Close removes the temporary directory.
func (s *wgSSHSession) Close() { _ = os.RemoveAll(s.tmpDir) }

// runSSHWireGuard is `tnctl ssh` over the WireGuard tunnel.
func runSSHWireGuard(domain, ns, deviceName string, login *api.FleetDeviceCredentials) error {
	sess, err := openWGSSH(domain, ns, deviceName, login)
	if err != nil {
		return err
	}
	defer sess.Close()

	sshExec := exec.Command("ssh", sess.baseArgs...)
	sshExec.Stdin = os.Stdin
	sshExec.Stdout = os.Stdout
	sshExec.Stderr = os.Stderr

	if config.Debug {
		fmt.Printf("DEBUG: Executing command: %s %v\n", sshExec.Path, sshExec.Args)
	}

	printRemoteShellBanner(deviceName, sess.password)

	return runWithSignalForwarding(sshExec)
}

// runWithSignalForwarding runs cmd, forwarding SIGINT/SIGTERM/SIGHUP to it and
// waiting for it, so the caller's deferred cleanup (the private key directory)
// still runs when tnctl is terminated or its terminal closes.
func runWithSignalForwarding(cmd *exec.Cmd) error {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigs:
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	return err
}
