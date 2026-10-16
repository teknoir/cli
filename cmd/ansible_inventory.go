package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
	wgapi "github.com/teknoir/wg-tunnel/pkg/api"
)

const (
	envAnsibleTransport = "TNCTL_ANSIBLE_TRANSPORT"
	wgAnnotation        = "ztna.teknoir.org/wireguard"

	transportAuto      = "auto"
	transportWireGuard = "wireguard"
	transportTunnel    = "tunnel"
)

var (
	ansibleList bool
	ansibleHost string
)

// inventoryOpts are the inputs of buildInventory; everything environmental is
// passed in so the builder needs no network and no globals.
type inventoryOpts struct {
	Domain     string
	Namespace  string // fallback when a device carries no namespace
	Transport  string
	Exe        string // absolute path of this tnctl
	ConfigFile string
	CacheDir   string // base: <cache>/tnctl/ansible
	HubURL     string
	TURNServer string
	P2P        bool
	GOOS       string
}

// parseAnsibleTransport reads TNCTL_ANSIBLE_TRANSPORT.
func parseAnsibleTransport(v string) (string, error) {
	switch t := strings.ToLower(strings.TrimSpace(v)); t {
	case "", transportAuto:
		return transportAuto, nil
	case transportWireGuard, transportTunnel:
		return t, nil
	default:
		return "", fmt.Errorf("%s must be auto, wireguard or tunnel, not %q", envAnsibleTransport, v)
	}
}

// ansibleGroup maps a namespace or label part to an Ansible group name part.
func ansibleGroup(s string) string {
	// Ansible group names are [A-Za-z0-9_]; label keys carry '/', '.' and '-'.
	return strings.Map(func(r rune) rune {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// dqQuote quotes s for use inside a double-quoted word of a POSIX shlex split
// (which is how Ansible splits ansible_ssh_args): only '\' and '"' are special.
func dqQuote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// shlexWord quotes one word so a POSIX shlex split returns it unchanged.
func shlexWord(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeFileIfDifferent writes data to path (dir 0700, file mode perm) unless it
// already holds exactly data; the mode is always enforced.
func writeFileIfDifferent(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(data) {
		return os.Chmod(path, perm)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func deviceWireGuardActive(d *api.BackstageDevice) bool {
	if d.Metadata.Annotations[wgAnnotation] != "enabled" {
		return false
	}
	wg := d.Spec.Subresources.Status.WireGuard
	if wg.Enabled != nil && !*wg.Enabled {
		return false
	}
	return wg.State == "active"
}

func withBecome(hv map[string]interface{}, password string) map[string]interface{} {
	hv["ansible_become_user"] = "root"
	hv["ansible_become_flags"] = "-E"
	if password != "" {
		hv["ansible_sudo_pass"] = password
		hv["ansible_become_pass"] = password
	}
	return hv
}

// buildInventory builds the Ansible dynamic inventory (--list form) for the
// devices and writes the key files. Devices without login data or without a
// usable transport are listed in their groups but get no host vars.
func buildInventory(devices []api.BackstageDevice, o inventoryOpts, warn io.Writer) (map[string]interface{}, error) {
	if o.Transport == "" {
		o.Transport = transportAuto
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	hubErr := error(nil)
	if o.Transport != transportTunnel {
		if hubErr = validateHubURL(o.HubURL); hubErr == nil {
			hubErr = validateHubHost(o.HubURL, o.Domain)
		}
		if hubErr != nil && o.Transport == transportWireGuard {
			return nil, hubErr
		}
	}

	groups := map[string][]string{}
	add := func(g, h string) {
		for _, x := range groups[g] {
			if x == h {
				return
			}
		}
		groups[g] = append(groups[g], h)
	}
	hostvars := map[string]interface{}{}

	for i := range devices {
		d := &devices[i]
		name := d.Metadata.Name
		ns := d.Metadata.Namespace
		if ns == "" {
			ns = o.Namespace
		}
		if name == "" {
			continue
		}
		if err := validateWGTarget(ns, name, 22); err != nil {
			fmt.Fprintf(warn, "tnctl: skipping device %q: %v\n", name, err)
			continue
		}
		add(ansibleGroup(ns), name)
		for k, v := range d.Metadata.Labels {
			add(ansibleGroup(k)+"_"+ansibleGroup(v), name)
		}

		if d.Spec.Settings.Username == "" || d.Spec.Settings.RSAPrivate == "" {
			continue
		}
		username, password, key, err := decodeDeviceLogin(d)
		if err != nil {
			fmt.Fprintf(warn, "tnctl: device %q: %v\n", name, err)
			continue
		}
		if username == "" || len(key) == 0 {
			continue
		}
		if err := validLoginName(username); err != nil {
			fmt.Fprintf(warn, "tnctl: device %q: %v\n", name, err)
			continue
		}

		useWG := o.Transport != transportTunnel && hubErr == nil && deviceWireGuardActive(d)
		port := d.Spec.Subresources.Status.RemoteAccess.Port
		tunnelOpen := d.Spec.Subresources.Status.RemoteAccess.Active && port != ""
		if !useWG && !(o.Transport != transportWireGuard && tunnelOpen) {
			continue
		}

		dir := filepath.Join(o.CacheDir, o.Domain, ns)
		keyFile := filepath.Join(dir, name+".pem")
		if err := writeFileIfDifferent(keyFile, key, 0600); err != nil {
			return nil, fmt.Errorf("cannot write the key of device %s: %w", name, err)
		}

		if useWG {
			knownHosts := filepath.Join(dir, name+".wg_known_hosts")
			proxy := buildProxyCommand(proxyArgs{
				Exe: o.Exe, ConfigFile: o.ConfigFile, Domain: o.Domain, Namespace: ns, Device: name, Port: 22,
				KnownHostsFile: knownHosts, TURNServer: o.TURNServer, HubURL: o.HubURL, P2P: o.P2P,
			}, o.GOOS)
			sshArgs := strings.Join([]string{
				"-o ControlMaster=auto",
				"-o ControlPersist=120s",
				`-o ProxyCommand="` + dqQuote(proxy) + `"`,
				"-o UserKnownHostsFile=" + shlexWord(knownHosts),
				"-o StrictHostKeyChecking=yes",
				"-o HostKeyAlias=" + shlexWord(wgapi.KnownHostsAlias(ns, name)),
				"-o ServerAliveInterval=30",
				"-o IdentitiesOnly=yes",
			}, " ")
			hostvars[name] = withBecome(map[string]interface{}{
				"ansible_namespace":            ns,
				"ansible_host":                 name,
				"ansible_port":                 22,
				"ansible_user":                 username,
				"ansible_ssh_private_key_file": keyFile,
				"ansible_ssh_args":             sshArgs,
				"ansible_python_interpreter":   "/usr/bin/python3",
				"ansible_ssh_retries":          5,
				"ansible_kubectl_namespace":    ns,
				"ansible_teknoir_namespace":    ns,
				"ansible_teknoir_device":       name,
				"ansible_teknoir_login_user":   username,
				"ansible_teknoir_domain":       o.Domain,
				"ansible_teknoir_transport":    transportWireGuard,
			}, password)
			continue
		}

		deadend := fmt.Sprintf("deadend-%s.%s", ns, o.Domain)
		pcmd := fmt.Sprintf("ssh -o ProxyCommand='ncat --ssl %s 2222' -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ExitOnForwardFailure=yes -o ServerAliveInterval=60 -i %s -N -W %%h:%%p teknoir@%s -p 2222",
			deadend, keyFile, deadend)
		var portVal interface{} = port
		if n, err := strconv.Atoi(port); err == nil {
			portVal = n
		}
		hostvars[name] = withBecome(map[string]interface{}{
			"ansible_namespace":            ns,
			"ansible_port":                 portVal,
			"ansible_host":                 "127.0.0.1",
			"ansible_user":                 username,
			"ansible_ssh_private_key_file": keyFile,
			"ansible_ssh_args":             `-o ForwardAgent=yes -o ProxyCommand="` + dqQuote(pcmd) + `"`,
			"ansible_python_interpreter":   "/usr/bin/python3",
			"ansible_ssh_retries":          20,
			"ansible_kubectl_namespace":    ns,
			"ansible_teknoir_namespace":    ns,
			"ansible_teknoir_tunnel_port":  portVal,
			"ansible_teknoir_tunnel_open":  true,
			"ansible_teknoir_device":       name,
			"ansible_teknoir_login_user":   username,
			"ansible_teknoir_domain":       o.Domain,
			"ansible_teknoir_transport":    transportTunnel,
		}, password)
	}

	inv := map[string]interface{}{"_meta": map[string]interface{}{"hostvars": hostvars}}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		// Only hosts Ansible can reach: a device with no transport (no tunnel,
		// no active WireGuard, no login) would be resolved by name and fail.
		var hosts []string
		for _, h := range groups[g] {
			if _, ok := hostvars[h]; ok {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) == 0 {
			continue
		}
		inv[g] = map[string]interface{}{"hosts": hosts, "vars": map[string]interface{}{}}
	}
	return inv, nil
}

// resolveCommon returns domain and namespace like the other commands.
func resolveCommon() (string, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", "", err
	}
	d, ns := cfg.Domain, cfg.GetNamespace()
	if d == "" || ns == "" {
		return "", "", fmt.Errorf("domain and namespace are required. Please set them via flags or config")
	}
	return d, ns, nil
}

func tnctlExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the tnctl executable: %w", err)
	}
	return filepath.Abs(exe)
}

func ansibleCacheDir() (string, error) {
	c, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the user cache directory: %w", err)
	}
	return filepath.Join(c, "tnctl", "ansible"), nil
}

var ansibleInventoryCmd = &cobra.Command{
	Use:   "ansible-inventory (--list | --host <name>)",
	Short: "Ansible dynamic inventory of the devices in the namespace",
	Long: `Ansible dynamic inventory script protocol: prints JSON on stdout.
Use it through 'tnctl ansible' / 'tnctl ansible-playbook', or as an inventory
executable. See docs/commands/ansible.md.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		if ansibleList == (ansibleHost != "") {
			return fmt.Errorf("exactly one of --list or --host is required")
		}
		transport, err := parseAnsibleTransport(os.Getenv(envAnsibleTransport))
		if err != nil {
			return err
		}
		dom, ns, err := resolveCommon()
		if err != nil {
			return err
		}
		exe, err := tnctlExe()
		if err != nil {
			return err
		}
		cache, err := ansibleCacheDir()
		if err != nil {
			return err
		}
		devices, err := api.FetchDeviceDetailsList(cmd.Context(), dom, ns, nil)
		if err != nil {
			return fmt.Errorf("failed to fetch devices: %w", err)
		}
		inv, err := buildInventory(devices, inventoryOpts{
			Domain: dom, Namespace: ns, Transport: transport, Exe: exe, ConfigFile: cfgFile,
			CacheDir: cache, HubURL: resolveWGOption("", envWGHub, defaultHubURL(dom)),
			TURNServer: resolveWGOption("", envWGTurnServer, ""), P2P: true,
		}, os.Stderr)
		if err != nil {
			return err
		}
		var out interface{} = inv
		if ansibleHost != "" {
			hv := inv["_meta"].(map[string]interface{})["hostvars"].(map[string]interface{})
			if v, ok := hv[ansibleHost]; ok {
				out = v
			} else {
				out = map[string]interface{}{}
			}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	},
}

func init() {
	rootCmd.AddCommand(ansibleInventoryCmd)
	ansibleInventoryCmd.Flags().BoolVar(&ansibleList, "list", false, "print the whole inventory")
	ansibleInventoryCmd.Flags().StringVar(&ansibleHost, "host", "", "print the variables of one host")
}
