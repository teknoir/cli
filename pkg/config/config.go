package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/viper"
)

// viperMu guards the global viper between Save, which writes its maps, and Load and GetNamespace,
// which read them; tool calls of `tnctl mcp` run concurrently.
var viperMu sync.RWMutex

// AuthConfig defines the authentication credentials for a domain. Save writes it into the config
// file with viper's encoder of the file's format, so every format has a tag: without one, the JSON
// and TOML encoders write the Go field names, which mapstructure does not read back.
type AuthConfig struct {
	AccessToken  string `mapstructure:"access_token" yaml:"access_token,omitempty" json:"access_token,omitempty" toml:"access_token,omitempty"`
	RefreshToken string `mapstructure:"refresh_token" yaml:"refresh_token,omitempty" json:"refresh_token,omitempty" toml:"refresh_token,omitempty"`
	Expiry       string `mapstructure:"expiry" yaml:"expiry,omitempty" json:"expiry,omitempty" toml:"expiry,omitempty"`
	Realm        string `mapstructure:"realm" yaml:"realm,omitempty" json:"realm,omitempty" toml:"realm,omitempty"`
	ClientID     string `mapstructure:"client_id" yaml:"client_id,omitempty" json:"client_id,omitempty" toml:"client_id,omitempty"`
	ClientSecret string `mapstructure:"client_secret" yaml:"client_secret,omitempty" json:"client_secret,omitempty" toml:"client_secret,omitempty"`
	Namespace    string `mapstructure:"namespace" yaml:"namespace,omitempty" json:"namespace,omitempty" toml:"namespace,omitempty"`
}

const ClientID = "teknoir-cli"

var Debug bool

// Config defines the structure for tnctl configuration.
type Config struct {
	Domain string                `mapstructure:"domain" yaml:"domain" json:"domain" toml:"domain"`
	Auths  map[string]AuthConfig `mapstructure:"auths" yaml:"auths" json:"auths" toml:"auths"`
	// namespaceChosen says SetNamespace chose the teamspace (tnctl ns): Save then keeps that choice
	// instead of the flag_namespace an older tnctl wrote.
	namespaceChosen bool
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	return &Config{
		Domain: "teknoir.cloud",
		Auths:  make(map[string]AuthConfig),
	}
}

// GetNamespace returns the active namespace.
// Precedence: Flags/Env > Domain-specific Config > Top-level Config > Default ("default").
func (c *Config) GetNamespace() string {
	viperMu.RLock()
	defer viperMu.RUnlock()
	if ns := viper.GetString("flag_namespace"); ns != "" {
		return ns
	}
	if c.Domain != "" {
		if auth, ok := c.Auths[SanitizeDomain(c.Domain)]; ok && auth.Namespace != "" {
			return auth.Namespace
		}
	}
	if ns := viper.GetString("namespace"); ns != "" {
		return ns
	}
	return "default"
}

// Load reads the configuration from Viper.
func Load() (*Config, error) {
	var cfg Config
	viperMu.RLock()
	err := viper.Unmarshal(&cfg)
	viperMu.RUnlock()
	if err != nil {
		return nil, err
	}
	if cfg.Auths == nil {
		cfg.Auths = make(map[string]AuthConfig)
	}
	return &cfg, nil
}

// notSaved are the keys Save never writes, and removes from the file when an older tnctl wrote them
// there: the flags and environment variables root.go binds (flag_namespace and flag_device from -n,
// --device, TNCTL_NAMESPACE and TNCTL_DEVICE; device and debug through AutomaticEnv) belong to one
// run. The teamspace a user chooses lives under auths (SetNamespace). A top-level namespace is not
// among them: tnctl v0.0.1-beta and v0.0.2-beta wrote the chosen teamspace there, and GetNamespace
// still reads it for a login without one of its own. Save removes it only when an older Save copied
// it from TNCTL_NAMESPACE (see Save).
var notSaved = []string{"device", "debug", "flag_namespace", "flag_device"}

// Save writes the configuration to the config file. The file is built from the file itself plus the
// domain and auths of c, never from the global viper: viper's settings also hold the bound flags and
// environment variables, and a -n or TNCTL_NAMESPACE of one run must not end up in the file. Other
// top-level keys of the file are kept, and the file is replaced atomically, readable by the user
// alone (writeConfigAtomic). Afterwards
// the global viper reads the file again and keeps no domain or auths of its own, as after Reload, so
// --domain, TNCTL_DOMAIN and the file decide on the next Load, and this run's -n and
// TNCTL_NAMESPACE still apply.
func (c *Config) Save() error {
	viperMu.Lock()
	defer viperMu.Unlock()
	path := viper.ConfigFileUsed()
	if path == "" {
		// No config file named: viper's own write (it fails without a file to write to).
		viper.Set("domain", c.Domain)
		viper.Set("auths", c.Auths)
		viper.Set("namespace", nil)
		viper.Set("device", nil)
		viper.Set("debug", nil)
		return viper.WriteConfig()
	}
	m := map[string]any{}
	fv := newFileViper(path)
	if err := fv.ReadInConfig(); err == nil { // a missing or broken file is overwritten, as before
		m = fv.AllSettings()
	}
	// An older Save wrote TNCTL_NAMESPACE (AutomaticEnv) as both namespace and flag_namespace: such a
	// namespace is one run's environment. It wrote flag_namespace "" without it, and -n only as
	// flag_namespace, so a top-level namespace of the user's own differs from a non-empty one.
	if fns, ok := m["flag_namespace"].(string); ok && fns != "" && m["namespace"] == fns {
		delete(m, "namespace")
	}
	auths := c.Auths
	// An older Save also wrote a run's -n or TNCTL_NAMESPACE as flag_namespace, and GetNamespace has
	// read it since as the teamspace of every run, ahead of the login's own. It is not kept (notSaved),
	// but the teamspace it chose is: it becomes the login's own, unless this save chose one (tnctl ns),
	// so a token refresh never switches the teamspace under the user.
	if fns, ok := m["flag_namespace"].(string); ok && fns != "" && !c.namespaceChosen {
		key := SanitizeDomain(c.Domain)
		if a, ok := auths[key]; ok {
			auths = make(map[string]AuthConfig, len(c.Auths))
			for k, v := range c.Auths {
				auths[k] = v
			}
			a.Namespace = fns
			auths[key] = a
		}
	}
	for _, k := range notSaved {
		delete(m, k)
	}
	m["domain"] = c.Domain
	m["auths"] = auths
	wv := newFileViper(path)
	if err := wv.MergeConfigMap(m); err != nil {
		return err
	}
	if err := writeConfigAtomic(wv, path); err != nil {
		return err
	}
	if err := viper.ReadInConfig(); err != nil {
		// The file was written but the global viper cannot read it back: it holds c instead.
		viper.Set("domain", c.Domain)
		viper.Set("auths", c.Auths)
		return nil
	}
	viper.Set("domain", nil)
	viper.Set("auths", nil)
	viper.Set("namespace", nil)
	viper.Set("device", nil)
	viper.Set("debug", nil)
	return nil
}

// newFileViper is a viper of the config file alone, with no flags, environment or defaults. A file
// without extension is YAML, as initConfig reads it; a file it creates is the user's alone (0600).
func newFileViper(path string) *viper.Viper {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigPermissions(0o600)
	v.SetConfigType(FileType(path))
	return v
}

// FileType is the format of the config file named path: its extension, or YAML when it has none (a
// dotfile such as .tnctlrc has none either: filepath.Ext calls its whole name the extension).
func FileType(path string) string {
	if ext := filepath.Ext(path); ext != "" && ext != filepath.Base(path) {
		return strings.ToLower(ext[1:])
	}
	return "yaml"
}

// writeConfigAtomic writes v next to the config file and renames it into place, so a concurrent
// reader never sees a half-written file. The file holds tokens: it is 0600 when it is new, and an
// existing file loses its group and other bits (tnctl created ~/.tnctl.yaml 0644 before). A config
// that is a symlink (a dotfiles repo) stays one: the file it points to is replaced.
func writeConfigAtomic(v *viper.Viper, path string) error {
	// The temp file is named with the configured file's type, never the link target's: viper picks
	// the encoder from the name, and a target may have another extension or none.
	ext := "." + FileType(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm() & 0o600
	}
	// inPlace writes the file itself, not atomically: for a directory tnctl cannot create files in,
	// and for a file that is a mount point (docker run -v ~/.tnctl.yaml:...), which rename cannot
	// replace (EBUSY).
	inPlace := func() error {
		if err := v.WriteConfigAs(path); err != nil {
			return err
		}
		_ = os.Chmod(path, mode)
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tnctl-*"+ext)
	if err != nil {
		return inPlace()
	}
	tmp := f.Name()
	_ = f.Close()
	defer os.Remove(tmp)
	if err := v.WriteConfigAs(tmp); err != nil {
		return err
	}
	_ = os.Chmod(tmp, mode)
	if err := os.Rename(tmp, path); err != nil {
		if errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV) {
			return inPlace()
		}
		return err
	}
	return nil
}

// LoadFresh reads the configuration file from disk again, ignoring viper's
// in-memory state, for long-running processes that must see token refreshes
// made by other tnctl processes. Without a config file named it behaves like Load. When viper names
// a file that is not there (a HOME where the default config could not be created, or a file removed
// while tnctl runs: the reset docs/troubleshooting.md gives), that is "not logged in", not an error
// of its own: Load's domain with no logins, never the global viper's copy of a removed file, so a
// token refresh neither redeems nor writes back the login that was removed.
func LoadFresh() (*Config, error) {
	path := viper.ConfigFileUsed()
	if path == "" {
		return Load()
	}
	v := newFileViper(path)
	if err := v.ReadInConfig(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cfg, err := Load()
			if err != nil {
				return nil, err
			}
			cfg.Auths = make(map[string]AuthConfig)
			return cfg, nil
		}
		return nil, err
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	if cfg.Auths == nil {
		cfg.Auths = make(map[string]AuthConfig)
	}
	return &cfg, nil
}

// Reload reads the config file into the global viper again and drops what Save set in memory, so a
// long-running process (tnctl mcp) sees the login and teamspace another tnctl saved since it
// started. When the file was removed (the reset docs/troubleshooting.md gives), the file's settings
// are dropped too: that is "not logged in", and a token refresh must not write the old login back.
// When the file cannot be read or parsed, viper keeps what it has. Without a config file named, it
// does nothing.
func Reload() {
	viperMu.Lock()
	defer viperMu.Unlock()
	if viper.ConfigFileUsed() == "" {
		return
	}
	if err := viper.ReadInConfig(); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return
		}
		// An empty document: "" for YAML and TOML, "{}" for JSON, which refuses "".
		if viper.ReadConfig(strings.NewReader("")) != nil && viper.ReadConfig(strings.NewReader("{}")) != nil {
			return
		}
	}
	viper.Set("domain", nil)
	viper.Set("auths", nil)
}

// UpdateDomain switches the active domain.
func (c *Config) UpdateDomain(newDomain string) {
	if c.Auths == nil {
		c.Auths = make(map[string]AuthConfig)
	}

	c.Domain = newDomain
	key := SanitizeDomain(newDomain)
	auth := c.Auths[key]

	// Ensure we have a default namespace if none is set
	if auth.Namespace == "" {
		auth.Namespace = "default"
		c.Auths[key] = auth
	}
}

// SetNamespace updates the active namespace and stores it in the current domain's context.
func (c *Config) SetNamespace(ns string) {
	if c.Domain != "" {
		if c.Auths == nil {
			c.Auths = make(map[string]AuthConfig)
		}
		key := SanitizeDomain(c.Domain)
		auth := c.Auths[key]
		auth.Namespace = ns
		c.Auths[key] = auth
		c.namespaceChosen = true
	}
}

// SanitizeDomain replaces dots with underscores for use as a map key in Viper.
func SanitizeDomain(domain string) string {
	return strings.ReplaceAll(domain, ".", "_")
}

// DesanitizeDomain replaces underscores with dots for displaying the domain name.
func DesanitizeDomain(key string) string {
	return strings.ReplaceAll(key, "_", ".")
}

// GetConfigPath returns the default path to the configuration file.
func GetConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tnctl.yaml"), nil
}
