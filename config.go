package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the top-level configuration structure loaded from the JSON file.
type Config struct {
	InfluxDB InfluxDBConfig `json:"influxdb"`
	// LogLevel controls verbosity: "debug", "info", "warn", "error". Default: "info".
	LogLevel string         `json:"log_level"`
	Devices  []DeviceConfig `json:"devices"`
}

// InfluxDBConfig holds the connection parameters for InfluxDB 2.x (or 1.8+ compat).
type InfluxDBConfig struct {
	// URL is the InfluxDB base URL, e.g. "http://influxdb:8086".
	URL string `json:"url"`
	// Token is the InfluxDB authentication token (use "user:password" for v1 compat).
	Token string `json:"token"`
	// Org is the InfluxDB organisation (leave empty for v1 compat).
	Org string `json:"org"`
	// Bucket is the InfluxDB bucket / v1 database name.
	Bucket string `json:"bucket"`
	// Measurement is a legacy setting kept for backward compatibility and ignored.
	Measurement string `json:"measurement"`
}

// DeviceConfig describes a single Shelly device to scrape.
type DeviceConfig struct {
	// Name is the value written to the "device" tag in InfluxDB.
	Name string `json:"name"`
	// Address is the IP address or hostname of the device (no scheme, no path).
	Address string `json:"address"`
	// IntervalSeconds is how often to poll the device. Default: 30.
	IntervalSeconds int `json:"interval"`
	// Metrics is the list of metric keys to collect, or ["all"] to collect everything.
	// Valid keys: power, voltage, current, pf, energy, relay_state, input_state,
	//             temperature, humidity, brightness, battery, roller_position.
	Metrics []string `json:"metrics"`
	// Username / Password for devices with HTTP auth enabled (optional).
	Username string `json:"username"`
	Password string `json:"password"`
}

// interval returns the device polling interval in seconds, falling back to 30.
func (d *DeviceConfig) interval() int {
	if d.IntervalSeconds > 0 {
		return d.IntervalSeconds
	}
	return 30
}

// wantsMetric reports whether the device config requests the given metric key.
// If the metrics list contains "all" (or is empty), every metric is requested.
func (d *DeviceConfig) wantsMetric(key string) bool {
	if len(d.Metrics) == 0 {
		return true
	}
	for _, m := range d.Metrics {
		if m == "all" || m == key {
			return true
		}
	}
	return false
}

// LoadConfig reads and parses the JSON config file at path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.InfluxDB.URL == "" {
		return fmt.Errorf("influxdb.url is required")
	}
	if c.InfluxDB.Bucket == "" {
		return fmt.Errorf("influxdb.bucket is required")
	}
	for i, d := range c.Devices {
		if d.Address == "" {
			return fmt.Errorf("devices[%d].address is required", i)
		}
		if d.Name == "" {
			return fmt.Errorf("devices[%d].name is required", i)
		}
	}
	return nil
}
