package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var (
	validNamePattern      = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	hostnameLabelPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?$`)
	allowedMetrics        = map[string]bool{
		"all":             true,
		"power":           true,
		"voltage":         true,
		"current":         true,
		"pf":              true,
		"energy":          true,
		"relay_state":     true,
		"input_state":     true,
		"temperature":     true,
		"humidity":        true,
		"brightness":      true,
		"battery":         true,
		"roller_position": true,
	}
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
	if _, err := url.ParseRequestURI(c.InfluxDB.URL); err != nil {
		return fmt.Errorf("influxdb.url is invalid: %w", err)
	}

	if c.InfluxDB.Bucket == "" {
		return fmt.Errorf("influxdb.bucket is required")
	}

	for i, d := range c.Devices {
		if d.Name == "" {
			return fmt.Errorf("devices[%d].name is required", i)
		}
		if !validNamePattern.MatchString(d.Name) {
			return fmt.Errorf("devices[%d].name contains invalid characters. Only alphanumeric characters, dashes, and underscores are allowed", i)
		}

		if d.Address == "" {
			return fmt.Errorf("devices[%d].address is required", i)
		}
		if !isValidHostnameOrIP(d.Address) {
			return fmt.Errorf("devices[%d].address %q is not a valid IP address or hostname", i, d.Address)
		}

		interval := d.interval()
		if interval < 1 || interval > 90000 {
			return fmt.Errorf("devices[%d].interval must be between 1 and 90000", i)
		}

		for _, m := range d.Metrics {
			if !allowedMetrics[m] {
				return fmt.Errorf("devices[%d].metrics contains an invalid metric: %s", i, m)
			}
		}
	}
	return nil
}

func isValidHostnameOrIP(host string) bool {
	host = strings.TrimSpace(host)
	if net.ParseIP(host) != nil {
		return true
	}
	// Basic hostname validation (allowing multiple segments)
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		// A label should start and end with an alphanumeric character
		// and can contain hyphens.
		if !hostnameLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}
