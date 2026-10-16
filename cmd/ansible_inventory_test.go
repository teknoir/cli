package cmd

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
	wgapi "github.com/teknoir/wg-tunnel/pkg/api"
)

// shlexSplit is a POSIX shlex.split: whitespace separates words; '...' is
// literal; "..." allows \" and \\ escapes; \x outside quotes escapes x.
func shlexSplit(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case c == '\'':
			in = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			in = true
			i++
			for ; ; i++ {
				if i >= len(s) {
					return nil, false
				}
				if s[i] == '"' {
					break
				}
				if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					i++
				}
				cur.WriteByte(s[i])
			}
		case c == '\\':
			if i+1 >= len(s) {
				return nil, false
			}
			in = true
			i++
			cur.WriteByte(s[i])
		default:
			in = true
			cur.WriteByte(c)
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out, true
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mkDevice(name, ns string, labels map[string]string, user, pass, key string, wg bool, tunnelPort string) api.BackstageDevice {
	var d api.BackstageDevice
	d.Metadata.Name, d.Metadata.Namespace, d.Metadata.Labels = name, ns, labels
	if user != "" {
		d.Spec.Settings.Username = b64(user)
	}
	if pass != "" {
		d.Spec.Settings.Password = b64(pass)
	}
	if key != "" {
		d.Spec.Settings.RSAPrivate = b64(key)
	}
	if wg {
		d.Metadata.Annotations = map[string]string{wgAnnotation: "enabled"}
		d.Spec.Subresources.Status.WireGuard.State = "active"
	}
	if tunnelPort != "" {
		d.Spec.Subresources.Status.RemoteAccess.Active = true
		d.Spec.Subresources.Status.RemoteAccess.Port = tunnelPort
	}
	return d
}

func testOpts(t *testing.T, transport string) inventoryOpts {
	return inventoryOpts{
		Domain: "teknoir.cloud", Namespace: "teknoir-demo", Transport: transport,
		Exe: "/opt/my tools/tnctl", ConfigFile: "", CacheDir: t.TempDir(),
		HubURL: "https://wg.teknoir.cloud", P2P: true, GOOS: "linux",
	}
}

func hostvars(inv map[string]interface{}) map[string]interface{} {
	return inv["_meta"].(map[string]interface{})["hostvars"].(map[string]interface{})
}

func TestBuildInventoryTransports(t *testing.T) {
	devs := []api.BackstageDevice{
		mkDevice("wg1", "teknoir-demo", map[string]string{"device-group": "test", "site.name": "a-b"}, "u", "pw", "KEY1", true, ""),
		mkDevice("tun1", "teknoir-demo", map[string]string{"device-group": "test"}, "u", "", "KEY2", false, "40022"),
		mkDevice("both", "teknoir-demo", nil, "u", "", "KEY3", true, "40023"),
		mkDevice("none", "teknoir-demo", nil, "u", "", "KEY4", false, ""),
		mkDevice("nokey", "teknoir-demo", nil, "u", "", "", true, ""),
	}
	cases := []struct {
		transport string
		want      map[string]string // host -> transport; absent = no hostvars
	}{
		{"auto", map[string]string{"wg1": "wireguard", "tun1": "tunnel", "both": "wireguard"}},
		{"wireguard", map[string]string{"wg1": "wireguard", "both": "wireguard"}},
		{"tunnel", map[string]string{"tun1": "tunnel", "both": "tunnel"}},
	}
	for _, c := range cases {
		t.Run(c.transport, func(t *testing.T) {
			inv, err := buildInventory(devs, testOpts(t, c.transport), os.Stderr)
			if err != nil {
				t.Fatal(err)
			}
			hv := hostvars(inv)
			if len(hv) != len(c.want) {
				t.Fatalf("hostvars for %d hosts, want %d: %v", len(hv), len(c.want), hv)
			}
			for h, tr := range c.want {
				if got := hv[h].(map[string]interface{})["ansible_teknoir_transport"]; got != tr {
					t.Errorf("%s transport = %v, want %s", h, got, tr)
				}
			}
			// groups: every device listed regardless of transport
			g := inv["teknoir_demo"].(map[string]interface{})["hosts"].([]string)
			if len(g) != 5 {
				t.Errorf("namespace group hosts = %v", g)
			}
			if !reflect.DeepEqual(inv["device_group_test"].(map[string]interface{})["hosts"], []string{"wg1", "tun1"}) {
				t.Errorf("label group = %v", inv["device_group_test"])
			}
			if _, ok := inv["site_name_a_b"]; !ok {
				t.Errorf("sanitised label group missing: %v", inv)
			}
		})
	}
}

func TestWireGuardHostVars(t *testing.T) {
	o := testOpts(t, "auto")
	o.ConfigFile = "/home/u/my cfg.yaml"
	o.TURNServer = "turn.example.com:3478"
	inv, err := buildInventory([]api.BackstageDevice{
		mkDevice("wg1", "teknoir-demo", nil, "ubuntu", "pw", "KEY1", true, ""),
	}, o, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	hv := hostvars(inv)["wg1"].(map[string]interface{})
	dir := filepath.Join(o.CacheDir, "teknoir.cloud", "teknoir-demo")
	for k, want := range map[string]interface{}{
		"ansible_host": "wg1", "ansible_port": 22, "ansible_user": "ubuntu",
		"ansible_ssh_private_key_file": filepath.Join(dir, "wg1.pem"),
		"ansible_python_interpreter":   "/usr/bin/python3", "ansible_ssh_retries": 5,
		"ansible_become_user": "root", "ansible_become_flags": "-E",
		"ansible_become_pass": "pw", "ansible_sudo_pass": "pw",
		"ansible_teknoir_device": "wg1", "ansible_teknoir_namespace": "teknoir-demo",
		"ansible_teknoir_domain": "teknoir.cloud", "ansible_teknoir_transport": "wireguard",
	} {
		if hv[k] != want {
			t.Errorf("%s = %v, want %v", k, hv[k], want)
		}
	}
	kh := filepath.Join(dir, "wg1.wg_known_hosts")
	proxy := buildProxyCommand(proxyArgs{
		Exe: o.Exe, ConfigFile: o.ConfigFile, Domain: "teknoir.cloud", Namespace: "teknoir-demo", Device: "wg1", Port: 22,
		KnownHostsFile: kh, TURNServer: o.TURNServer, HubURL: o.HubURL, P2P: true,
	}, "linux")

	// Ansible splits ansible_ssh_args with shlex; ssh must get these -o args.
	words, ok := shlexSplit(hv["ansible_ssh_args"].(string))
	if !ok {
		t.Fatal("unbalanced quotes in ansible_ssh_args")
	}
	want := []string{
		"-o", "ControlMaster=auto", "-o", "ControlPersist=120s",
		"-o", "ProxyCommand=" + proxy,
		"-o", "UserKnownHostsFile=" + kh,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "HostKeyAlias=" + hostKeyAliasFor("teknoir-demo", "wg1"),
		"-o", "ServerAliveInterval=30", "-o", "IdentitiesOnly=yes",
	}
	if !reflect.DeepEqual(words, want) {
		t.Fatalf("split mismatch:\n got %q\nwant %q", words, want)
	}
	// The proxy itself must split back into its words (sh quoting).
	inner, ok := shlexSplit(strings.TrimPrefix(words[5], "ProxyCommand="))
	if !ok || inner[0] != "/opt/my tools/tnctl" || inner[1] != "wg-proxy" {
		t.Fatalf("proxy words = %q", inner)
	}
	if !strings.Contains(strings.Join(inner, " "), "--config /home/u/my cfg.yaml") ||
		!strings.Contains(strings.Join(inner, " "), "--turn-server turn.example.com:3478") ||
		!strings.Contains(strings.Join(inner, " "), "--hub-url https://wg.teknoir.cloud") ||
		inner[len(inner)-1] != "--p2p" {
		t.Errorf("proxy words = %q", inner)
	}
	if !strings.HasPrefix(words[11], "HostKeyAlias=") {
		t.Errorf("alias word %q", words[11])
	}
}

func hostKeyAliasFor(ns, dev string) string { return wgapi.KnownHostsAlias(ns, dev) }

func TestPasswordOptionalAndKeyFile(t *testing.T) {
	o := testOpts(t, "auto")
	devs := []api.BackstageDevice{mkDevice("wg1", "teknoir-demo", nil, "u", "", "KEY-A", true, "")}
	inv, err := buildInventory(devs, o, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	hv := hostvars(inv)["wg1"].(map[string]interface{})
	if _, ok := hv["ansible_become_pass"]; ok {
		t.Error("become pass set without password")
	}
	if _, ok := hv["ansible_sudo_pass"]; ok {
		t.Error("sudo pass set without password")
	}
	if hv["ansible_become_flags"] != "-E" || hv["ansible_become_user"] != "root" {
		t.Error("become vars missing")
	}
	key := hv["ansible_ssh_private_key_file"].(string)
	chk := func(want string) {
		t.Helper()
		b, _ := os.ReadFile(key)
		st, _ := os.Stat(key)
		dst, _ := os.Stat(filepath.Dir(key))
		if string(b) != want || st.Mode().Perm() != 0600 || dst.Mode().Perm() != 0700 {
			t.Fatalf("key=%q mode=%v dir=%v", b, st.Mode().Perm(), dst.Mode().Perm())
		}
	}
	chk("KEY-A")
	// stale key is replaced
	devs[0].Spec.Settings.RSAPrivate = b64("KEY-B")
	if _, err := buildInventory(devs, o, os.Stderr); err != nil {
		t.Fatal(err)
	}
	chk("KEY-B")
}

func TestTunnelHostVars(t *testing.T) {
	o := testOpts(t, "tunnel")
	inv, err := buildInventory([]api.BackstageDevice{mkDevice("tun1", "teknoir-demo", nil, "u", "pw", "K", false, "40022")}, o, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	hv := hostvars(inv)["tun1"].(map[string]interface{})
	if hv["ansible_host"] != "127.0.0.1" || hv["ansible_port"] != 40022 || hv["ansible_ssh_retries"] != 20 {
		t.Errorf("%v", hv)
	}
	words, ok := shlexSplit(hv["ansible_ssh_args"].(string))
	if !ok || len(words) != 4 || !strings.Contains(words[3], "ncat --ssl deadend-teknoir-demo.teknoir.cloud 2222") ||
		!strings.Contains(words[3], "-W %h:%p teknoir@deadend-teknoir-demo.teknoir.cloud -p 2222") {
		t.Errorf("tunnel args %q", words)
	}
}

func TestInvalidHubFallsBackOrFails(t *testing.T) {
	o := testOpts(t, "auto")
	o.HubURL = "http://wg.teknoir.cloud"
	devs := []api.BackstageDevice{mkDevice("d", "teknoir-demo", nil, "u", "", "K", true, "40022")}
	inv, err := buildInventory(devs, o, os.Stderr)
	if err != nil || hostvars(inv)["d"].(map[string]interface{})["ansible_teknoir_transport"] != "tunnel" {
		t.Fatalf("auto should fall back to the tunnel: %v %v", inv, err)
	}
	o.Transport = "wireguard"
	if _, err := buildInventory(devs, o, os.Stderr); err == nil {
		t.Fatal("wireguard with a bad hub URL must fail")
	}
}

func TestParseAnsibleTransport(t *testing.T) {
	for in, want := range map[string]string{"": "auto", "auto": "auto", "WireGuard": "wireguard", " tunnel ": "tunnel"} {
		if got, err := parseAnsibleTransport(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := parseAnsibleTransport("ssh"); err == nil {
		t.Error("invalid transport accepted")
	}
}

func TestInventoryShim(t *testing.T) {
	got := inventoryShim("/opt/it's here/tnctl", "/home/u/c f.yaml", "teknoir.cloud", "teknoir-demo")
	want := "#!/bin/sh\nexec '/opt/it'\\''s here/tnctl' --config '/home/u/c f.yaml' -d teknoir.cloud -n teknoir-demo ansible-inventory \"$@\"\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	line := strings.Split(got, "\n")[1]
	words, ok := shlexSplit(strings.TrimPrefix(line, "exec "))
	if !ok || words[0] != "/opt/it's here/tnctl" || words[2] != "/home/u/c f.yaml" {
		t.Errorf("shim words %q", words)
	}
	if strings.Contains(inventoryShim("/x/tnctl", "", "d.io", "n"), "--config") {
		t.Error("--config without a config file")
	}
	p := filepath.Join(t.TempDir(), "a", "inventory")
	if err := writeFileIfDifferent(p, []byte(got), 0700); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0700 {
		t.Errorf("shim mode %v", st.Mode())
	}
}

func TestShlexWordRoundTrip(t *testing.T) {
	for _, s := range []string{"a", "a b", "it's", `a"b\c`, "", "x$y`z"} {
		w, ok := shlexSplit(shlexWord(s))
		if !ok || len(w) != 1 || w[0] != s {
			t.Errorf("%q -> %q", s, w)
		}
	}
}

func TestAnsibleGroupNamesAreValid(t *testing.T) {
	for in, want := range map[string]string{
		"teknoir-demo":                    "teknoir_demo",
		"teknoir.org/device-kind_virtual": "teknoir_org_device_kind_virtual",
		"site_karlstad":                   "site_karlstad",
		"hw:nv jetson":                    "hw_nv_jetson",
	} {
		if got := ansibleGroup(in); got != want {
			t.Errorf("ansibleGroup(%q) = %q, want %q", in, got, want)
		}
	}
}
