// Package config loads and validates the ns-mining YAML configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a YAML string like "20s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the top-level configuration.
type Config struct {
	DevicePollInterval Duration   `yaml:"device_poll_interval"`
	PoolPollInterval   Duration   `yaml:"pool_poll_interval"`
	HeartbeatTime      string     `yaml:"heartbeat_time"` // "HH:MM" local; empty disables
	StateFile          string     `yaml:"state_file"`
	Slack              Slack      `yaml:"slack"`
	Miners             []Miner    `yaml:"miners"`
	Thresholds         Thresholds `yaml:"thresholds"`
}

// Slack holds notifier settings. The token is injected from $SLACK_BOT_TOKEN,
// never read from the file.
type Slack struct {
	Channel string `yaml:"channel"`
	Token   string `yaml:"-"`
}

// Miner is one Bitaxe plus its pool identity.
type Miner struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Pool Pool   `yaml:"pool"`
}

// Pool identifies where a miner's shares land, for pool-side polling.
type Pool struct {
	Type    string `yaml:"type"`    // "ckpool" (only type for now)
	Address string `yaml:"address"` // payout / worker address
}

// Thresholds tune when alerts fire.
type Thresholds struct {
	TempWarnC        float64 `yaml:"temp_warn_c"`
	VRTempWarnC      float64 `yaml:"vr_temp_warn_c"`
	HashrateFloorPct float64 `yaml:"hashrate_floor_pct"` // % of expectedHashrate
	WorkStoppageMin  int     `yaml:"work_stoppage_min"`
	PoolSilenceMin   int     `yaml:"pool_silence_min"`
}

// Load reads, parses, defaults, and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	c.Slack.Token = os.Getenv("SLACK_BOT_TOKEN")
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.DevicePollInterval == 0 {
		c.DevicePollInterval = Duration(20 * time.Second)
	}
	if c.PoolPollInterval == 0 {
		c.PoolPollInterval = Duration(60 * time.Second)
	}
	if c.StateFile == "" {
		c.StateFile = "/var/lib/ns-mining/state.json"
	}
	if c.Thresholds.TempWarnC == 0 {
		c.Thresholds.TempWarnC = 68
	}
	if c.Thresholds.VRTempWarnC == 0 {
		c.Thresholds.VRTempWarnC = 95
	}
	if c.Thresholds.HashrateFloorPct == 0 {
		c.Thresholds.HashrateFloorPct = 50
	}
	if c.Thresholds.WorkStoppageMin == 0 {
		c.Thresholds.WorkStoppageMin = 5
	}
	if c.Thresholds.PoolSilenceMin == 0 {
		c.Thresholds.PoolSilenceMin = 15
	}
}

func (c *Config) validate() error {
	if len(c.Miners) == 0 {
		return fmt.Errorf("config: no miners defined")
	}
	if c.HeartbeatTime != "" {
		if _, err := time.Parse("15:04", c.HeartbeatTime); err != nil {
			return fmt.Errorf("config: heartbeat_time %q must be HH:MM: %w", c.HeartbeatTime, err)
		}
	}
	seen := map[string]bool{}
	for i, m := range c.Miners {
		if m.Name == "" {
			return fmt.Errorf("config: miner[%d]: name is required", i)
		}
		if seen[m.Name] {
			return fmt.Errorf("config: duplicate miner name %q", m.Name)
		}
		seen[m.Name] = true
		if m.URL == "" {
			return fmt.Errorf("config: miner %q: url is required", m.Name)
		}
	}
	return nil
}
