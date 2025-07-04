package main

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	// Matterbridge API settings
	MatterbridgeURL string `mapstructure:"matterbridge_url"`
	APIToken        string `mapstructure:"api_token"`
	Gateway         string `mapstructure:"gateway"`

	// Nostr settings
	PublicKey           string   `mapstructure:"public_key"`
	PrivateKey          string   `mapstructure:"private_key"`
	DefaultRelays       []string `mapstructure:"default_relays"`
	WatchNpub           string   `mapstructure:"watch_npub"`
	MaxRetryAttempts    int      `mapstructure:"max_retry_attempts"`
	RetryTimeoutMinutes int      `mapstructure:"retry_timeout_minutes"`
}

func LoadConfig(filename string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(filename)
	v.SetConfigType("toml")

	// Set defaults
	v.SetDefault("matterbridge_url", "http://localhost:4242")
	v.SetDefault("gateway", "nostr-gateway")
	v.SetDefault("max_retry_attempts", 3)
	v.SetDefault("retry_timeout_minutes", 10)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate required fields
	if config.PublicKey == "" {
		return nil, fmt.Errorf("public_key is required")
	}
	if config.PrivateKey == "" {
		return nil, fmt.Errorf("private_key is required")
	}
	if len(config.DefaultRelays) == 0 {
		return nil, fmt.Errorf("default_relays is required")
	}
	if config.WatchNpub == "" {
		return nil, fmt.Errorf("watch_npub is required")
	}

	// Ensure Matterbridge URL doesn't have trailing slash
	config.MatterbridgeURL = strings.TrimSuffix(config.MatterbridgeURL, "/")

	return &config, nil
}
