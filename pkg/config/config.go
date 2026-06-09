package config

import (
	"github.com/spf13/viper"
	"os"
	"path/filepath"
	"strings"
)

// AuthConfig defines the authentication credentials for a domain.
type AuthConfig struct {
	AccessToken  string `mapstructure:"access_token" yaml:"access_token,omitempty"`
	RefreshToken string `mapstructure:"refresh_token" yaml:"refresh_token,omitempty"`
	Expiry       string `mapstructure:"expiry" yaml:"expiry,omitempty"`
	Realm        string `mapstructure:"realm" yaml:"realm,omitempty"`
	ClientID     string `mapstructure:"client_id" yaml:"client_id,omitempty"`
	ClientSecret string `mapstructure:"client_secret" yaml:"client_secret,omitempty"`
	Namespace    string `mapstructure:"namespace" yaml:"namespace,omitempty"`
	Device       string `mapstructure:"device" yaml:"device,omitempty"`
}

const ClientID = "teknoir-cli"

// Config defines the structure for tnctl configuration.
type Config struct {
	Domain string                `mapstructure:"domain" yaml:"domain"`
	Auths  map[string]AuthConfig `mapstructure:"auths" yaml:"auths"`
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

// GetDevice returns the active device.
// Precedence: Flags/Env > Domain-specific Config > Top-level Config > Empty.
func (c *Config) GetDevice() string {
	if dev := viper.GetString("flag_device"); dev != "" {
		return dev
	}
	if c.Domain != "" {
		if auth, ok := c.Auths[SanitizeDomain(c.Domain)]; ok && auth.Device != "" {
			return auth.Device
		}
	}
	if dev := viper.GetString("device"); dev != "" {
		return dev
	}
	return ""
}

// Load reads the configuration from Viper.
func Load() (*Config, error) {
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	if cfg.Auths == nil {
		cfg.Auths = make(map[string]AuthConfig)
	}
	return &cfg, nil
}

// Save writes the configuration back to Viper and the config file.
func (c *Config) Save() error {
	viper.Set("domain", c.Domain)
	viper.Set("auths", c.Auths)
	// Clear top-level namespace/device to ensure they are only in Auths
	viper.Set("namespace", nil)
	viper.Set("device", nil)
	return viper.WriteConfig()
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
	}
}

// SetDevice updates the active device and stores it in the current domain's context.
func (c *Config) SetDevice(dev string) {
	if c.Domain != "" {
		if c.Auths == nil {
			c.Auths = make(map[string]AuthConfig)
		}
		key := SanitizeDomain(c.Domain)
		auth := c.Auths[key]
		auth.Device = dev
		c.Auths[key] = auth
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
