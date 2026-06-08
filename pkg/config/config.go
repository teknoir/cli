package config

import (
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
}

const ClientID = "teknoir-cli"

// Config defines the structure for tnctl configuration.
type Config struct {
	Domain    string                `mapstructure:"domain" yaml:"domain"`
	Namespace string                `mapstructure:"namespace" yaml:"namespace"`
	Device    string                `mapstructure:"device" yaml:"device"`
	Auths     map[string]AuthConfig `mapstructure:"auths" yaml:"auths"`
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	return &Config{
		Domain:    "teknoir.cloud",
		Namespace: "default",
		Auths:     make(map[string]AuthConfig),
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
