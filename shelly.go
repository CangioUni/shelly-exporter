package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var validComponentLabel = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

// Metric is a single named numeric value scraped from a Shelly device.
type Metric struct {
	// Key identifies the metric type (e.g. "power", "voltage").
	Key string
	// Label is an optional sub-label for multi-channel devices (e.g. "ch0", "phase_a").
	Label string
	// Value is the numeric reading.
	Value float64
}

// FieldName builds the InfluxDB field name from the metric key and label.
func (m Metric) FieldName() string {
	if m.Label != "" {
		return m.Key + "_" + m.Label
	}
	return m.Key
}

// shellyInfo is the response of GET /shelly (device identification).
type shellyInfo struct {
	Type string `json:"type"`
	Gen  int    `json:"gen"` // 0 / absent → Gen1; 2 or 3 → Gen2+
}

// ─── Gen1 status ─────────────────────────────────────────────────────────────

type gen1Status struct {
	Relays []struct {
		IsOn bool `json:"ison"`
	} `json:"relays"`
	Inputs []struct {
		Input int `json:"input"`
	} `json:"inputs"`
	Meters []struct {
		Power   float64 `json:"power"`
		Voltage float64 `json:"voltage"`
		Current float64 `json:"current"`
		// Total is the cumulative energy counter in Watt-minutes (not Wh).
		Total float64 `json:"total"`
	} `json:"meters"`
	EMeters []struct {
		Power   float64 `json:"power"`
		Voltage float64 `json:"voltage"`
		Current float64 `json:"current"`
		PF      float64 `json:"pf"`
		Total   float64 `json:"total"`
	} `json:"emeters"`
	Temperature     float64 `json:"temperature"`
	OverTemperature bool    `json:"overtemperature"`
	Tmp             struct {
		TC      float64 `json:"tC"`
		IsValid bool    `json:"is_valid"`
	} `json:"tmp"`
	Lights []struct {
		IsOn       bool    `json:"ison"`
		Brightness float64 `json:"brightness"`
	} `json:"lights"`
	Rollers []struct {
		State      string  `json:"state"`
		CurrentPos float64 `json:"current_pos"`
	} `json:"rollers"`
	Bat struct {
		Value   float64 `json:"value"`
		Voltage float64 `json:"voltage"`
	} `json:"bat"`
	Hum struct {
		Value float64 `json:"value"`
	} `json:"hum"`
	Sensor struct {
		TmpC float64 `json:"tmp"`
		Hum  float64 `json:"hum"`
	} `json:"sensor"`
}

// ─── Gen2 status ─────────────────────────────────────────────────────────────

// gen2Status is a loosely typed map so we can handle the variable "switch:0",
// "switch:1", "temperature:0" etc. keys without needing a rigid struct.
type gen2Status map[string]json.RawMessage

type gen2Switch struct {
	Output      bool    `json:"output"`
	APower      float64 `json:"apower"`
	Voltage     float64 `json:"voltage"`
	Current     float64 `json:"current"`
	PF          float64 `json:"pf"`
	Temperature struct {
		TC float64 `json:"tC"`
	} `json:"temperature"`
	AEnergy struct {
		Total float64 `json:"total"`
	} `json:"aenergy"`
}

type gen2Temperature struct {
	TC float64 `json:"tC"`
}

type gen2Humidity struct {
	RH float64 `json:"rh"`
}

type gen2DevicePower struct {
	Battery struct {
		Percent float64 `json:"percent"`
	} `json:"battery"`
}

type gen2Cover struct {
	State      string  `json:"state"`
	CurrentPos float64 `json:"current_pos"`
}

type gen2Light struct {
	Output     bool    `json:"output"`
	Brightness float64 `json:"brightness"`
}

type gen2Input struct {
	State bool `json:"state"`
}

type gen2PM struct {
	APower  float64 `json:"apower"`
	Voltage float64 `json:"voltage"`
	Current float64 `json:"current"`
	PF      float64 `json:"pf"`
	AEnergy struct {
		Total float64 `json:"total"`
	} `json:"aenergy"`
}

// gen2EM1 represents a single-phase energy-meter channel (em1:N) found on
// Shelly Pro EM and Pro 3EM Gen2 devices.
type gen2EM1 struct {
	APower  float64 `json:"act_power"`
	Voltage float64 `json:"voltage"`
	Current float64 `json:"current"`
	PF      float64 `json:"pf"`
	AEnergy struct {
		Total float64 `json:"total"`
	} `json:"aenergy"`
}

// ─── ShellyClient ─────────────────────────────────────────────────────────────

// ShellyClient handles HTTP communication with a single Shelly device.
type ShellyClient struct {
	device DeviceConfig
	http   *http.Client
	log    *appLogger
}

func newShellyClient(d DeviceConfig, l *appLogger) *ShellyClient {
	return &ShellyClient{
		device: d,
		http:   &http.Client{Timeout: 10 * time.Second},
		log:    l,
	}
}

// Scrape detects the device generation and returns the requested metrics.
func (c *ShellyClient) Scrape(ctx context.Context) ([]Metric, error) {
	gen, err := c.detectGen(ctx)
	if err != nil {
		return nil, fmt.Errorf("device gen detection: %w", err)
	}
	c.log.Debug("detected device generation", "device", c.device.Name, "gen", gen)

	if gen >= 2 {
		return c.scrapeGen2(ctx)
	}
	return c.scrapeGen1(ctx)
}

func (c *ShellyClient) detectGen(ctx context.Context) (int, error) {
	var info shellyInfo
	if err := c.getJSON(ctx, "/shelly", &info); err != nil {
		return 0, err
	}
	if info.Gen >= 2 {
		return info.Gen, nil
	}
	return 1, nil
}

// ─── Gen1 scraping ────────────────────────────────────────────────────────────

func (c *ShellyClient) scrapeGen1(ctx context.Context) ([]Metric, error) {
	var status gen1Status
	if err := c.getJSON(ctx, "/status", &status); err != nil {
		return nil, err
	}

	var metrics []Metric

	// Relays → relay_state
	for i, r := range status.Relays {
		if c.device.wantsMetric("relay_state") {
			v := 0.0
			if r.IsOn {
				v = 1.0
			}
			metrics = append(metrics, Metric{Key: "relay_state", Label: fmt.Sprintf("ch%d", i), Value: v})
		}
	}

	// Inputs → input_state
	for i, inp := range status.Inputs {
		if c.device.wantsMetric("input_state") {
			metrics = append(metrics, Metric{Key: "input_state", Label: fmt.Sprintf("ch%d", i), Value: float64(inp.Input)})
		}
	}

	// Standard meters (Shelly1PM, ShellyPlug, etc.)
	for i, m := range status.Meters {
		lbl := fmt.Sprintf("ch%d", i)
		if c.device.wantsMetric("power") {
			metrics = append(metrics, Metric{Key: "power", Label: lbl, Value: m.Power})
		}
		if c.device.wantsMetric("voltage") && m.Voltage != 0 {
			metrics = append(metrics, Metric{Key: "voltage", Label: lbl, Value: m.Voltage})
		}
		if c.device.wantsMetric("current") && m.Current != 0 {
			metrics = append(metrics, Metric{Key: "current", Label: lbl, Value: m.Current})
		}
		if c.device.wantsMetric("energy") && m.Total != 0 {
			metrics = append(metrics, Metric{Key: "energy", Label: lbl, Value: m.Total})
		}
	}

	// Energy meters (ShellyEM, Shelly3EM)
	for i, em := range status.EMeters {
		lbl := fmt.Sprintf("phase%d", i)
		if c.device.wantsMetric("power") {
			metrics = append(metrics, Metric{Key: "power", Label: lbl, Value: em.Power})
		}
		if c.device.wantsMetric("voltage") {
			metrics = append(metrics, Metric{Key: "voltage", Label: lbl, Value: em.Voltage})
		}
		if c.device.wantsMetric("current") {
			metrics = append(metrics, Metric{Key: "current", Label: lbl, Value: em.Current})
		}
		if c.device.wantsMetric("pf") {
			metrics = append(metrics, Metric{Key: "pf", Label: lbl, Value: em.PF})
		}
		if c.device.wantsMetric("energy") {
			metrics = append(metrics, Metric{Key: "energy", Label: lbl, Value: em.Total})
		}
	}

	// Temperature: prefer tmp.tC (more reliable); fall back to bare temperature field.
	if c.device.wantsMetric("temperature") {
		if status.Tmp.IsValid {
			metrics = append(metrics, Metric{Key: "temperature", Value: status.Tmp.TC})
		} else if status.Temperature != 0 {
			metrics = append(metrics, Metric{Key: "temperature", Value: status.Temperature})
		} else if status.Sensor.TmpC != 0 {
			metrics = append(metrics, Metric{Key: "temperature", Value: status.Sensor.TmpC})
		}
	}

	// Humidity (Shelly H&T Gen1)
	if c.device.wantsMetric("humidity") {
		if status.Hum.Value != 0 {
			metrics = append(metrics, Metric{Key: "humidity", Value: status.Hum.Value})
		} else if status.Sensor.Hum != 0 {
			metrics = append(metrics, Metric{Key: "humidity", Value: status.Sensor.Hum})
		}
	}

	// Lights (ShellyBulb, ShellyRGBW)
	for i, l := range status.Lights {
		lbl := fmt.Sprintf("ch%d", i)
		if c.device.wantsMetric("relay_state") {
			v := 0.0
			if l.IsOn {
				v = 1.0
			}
			metrics = append(metrics, Metric{Key: "relay_state", Label: lbl, Value: v})
		}
		if c.device.wantsMetric("brightness") {
			metrics = append(metrics, Metric{Key: "brightness", Label: lbl, Value: l.Brightness})
		}
	}

	// Rollers (Shelly2.5 in roller mode)
	for i, r := range status.Rollers {
		lbl := fmt.Sprintf("ch%d", i)
		if c.device.wantsMetric("roller_position") {
			metrics = append(metrics, Metric{Key: "roller_position", Label: lbl, Value: r.CurrentPos})
		}
		if c.device.wantsMetric("relay_state") {
			v := 0.0
			if r.State == "open" || r.State == "close" {
				v = 1.0
			}
			metrics = append(metrics, Metric{Key: "roller_state", Label: lbl, Value: v})
		}
	}

	// Battery (Shelly Door/Window, H&T, Flood, etc.)
	if c.device.wantsMetric("battery") && status.Bat.Value != 0 {
		metrics = append(metrics, Metric{Key: "battery", Value: status.Bat.Value})
	}

	return metrics, nil
}

// ─── Gen2 scraping ────────────────────────────────────────────────────────────

func (c *ShellyClient) scrapeGen2(ctx context.Context) ([]Metric, error) {
	var raw gen2Status
	if err := c.getJSON(ctx, "/rpc/Shelly.GetStatus", &raw); err != nil {
		return nil, err
	}

	var metrics []Metric

	for key, val := range raw {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 {
			// Validate that the label part is alphanumeric to prevent injection from malicious payloads
			if !validComponentLabel.MatchString(parts[1]) {
				c.log.Warn("skipping component with invalid label", "key", key)
				continue
			}
		}

		switch {
		case strings.HasPrefix(key, "switch:"):
			var sw gen2Switch
			if err := json.Unmarshal(val, &sw); err != nil {
				c.log.Warn("cannot parse switch component", "key", key, "err", err)
				continue
			}
			lbl := strings.Replace(key, ":", "", 1) // "switch0"
			if c.device.wantsMetric("relay_state") {
				v := 0.0
				if sw.Output {
					v = 1.0
				}
				metrics = append(metrics, Metric{Key: "relay_state", Label: lbl, Value: v})
			}
			if c.device.wantsMetric("power") {
				metrics = append(metrics, Metric{Key: "power", Label: lbl, Value: sw.APower})
			}
			if c.device.wantsMetric("voltage") {
				metrics = append(metrics, Metric{Key: "voltage", Label: lbl, Value: sw.Voltage})
			}
			if c.device.wantsMetric("current") {
				metrics = append(metrics, Metric{Key: "current", Label: lbl, Value: sw.Current})
			}
			if c.device.wantsMetric("pf") {
				metrics = append(metrics, Metric{Key: "pf", Label: lbl, Value: sw.PF})
			}
			if c.device.wantsMetric("energy") {
				metrics = append(metrics, Metric{Key: "energy", Label: lbl, Value: sw.AEnergy.Total})
			}
			if c.device.wantsMetric("temperature") && sw.Temperature.TC != 0 {
				metrics = append(metrics, Metric{Key: "temperature", Label: lbl, Value: sw.Temperature.TC})
			}

		case strings.HasPrefix(key, "pm1:"):
			var pm gen2PM
			if err := json.Unmarshal(val, &pm); err != nil {
				continue
			}
			lbl := strings.Replace(key, ":", "", 1)
			if c.device.wantsMetric("power") {
				metrics = append(metrics, Metric{Key: "power", Label: lbl, Value: pm.APower})
			}
			if c.device.wantsMetric("voltage") {
				metrics = append(metrics, Metric{Key: "voltage", Label: lbl, Value: pm.Voltage})
			}
			if c.device.wantsMetric("current") {
				metrics = append(metrics, Metric{Key: "current", Label: lbl, Value: pm.Current})
			}
			if c.device.wantsMetric("pf") {
				metrics = append(metrics, Metric{Key: "pf", Label: lbl, Value: pm.PF})
			}
			if c.device.wantsMetric("energy") {
				metrics = append(metrics, Metric{Key: "energy", Label: lbl, Value: pm.AEnergy.Total})
			}

		case strings.HasPrefix(key, "em1:"):
			var em gen2EM1
			if err := json.Unmarshal(val, &em); err != nil {
				c.log.Warn("cannot parse em1 component", "key", key, "err", err)
				continue
			}
			lbl := strings.Replace(key, ":", "", 1)
			if c.device.wantsMetric("power") {
				metrics = append(metrics, Metric{Key: "power", Label: lbl, Value: em.APower})
			}
			if c.device.wantsMetric("voltage") {
				metrics = append(metrics, Metric{Key: "voltage", Label: lbl, Value: em.Voltage})
			}
			if c.device.wantsMetric("current") {
				metrics = append(metrics, Metric{Key: "current", Label: lbl, Value: em.Current})
			}
			if c.device.wantsMetric("pf") {
				metrics = append(metrics, Metric{Key: "pf", Label: lbl, Value: em.PF})
			}
			if c.device.wantsMetric("energy") {
				metrics = append(metrics, Metric{Key: "energy", Label: lbl, Value: em.AEnergy.Total})
			}

		case strings.HasPrefix(key, "cover:"):
			var cov gen2Cover
			if err := json.Unmarshal(val, &cov); err != nil {
				continue
			}
			lbl := strings.Replace(key, ":", "", 1)
			if c.device.wantsMetric("roller_position") {
				metrics = append(metrics, Metric{Key: "roller_position", Label: lbl, Value: cov.CurrentPos})
			}
			if c.device.wantsMetric("relay_state") {
				v := 0.0
				if cov.State == "opening" || cov.State == "closing" {
					v = 1.0
				}
				metrics = append(metrics, Metric{Key: "roller_state", Label: lbl, Value: v})
			}

		case strings.HasPrefix(key, "light:"):
			var l gen2Light
			if err := json.Unmarshal(val, &l); err != nil {
				continue
			}
			lbl := strings.Replace(key, ":", "", 1)
			if c.device.wantsMetric("relay_state") {
				v := 0.0
				if l.Output {
					v = 1.0
				}
				metrics = append(metrics, Metric{Key: "relay_state", Label: lbl, Value: v})
			}
			if c.device.wantsMetric("brightness") {
				metrics = append(metrics, Metric{Key: "brightness", Label: lbl, Value: l.Brightness})
			}

		case strings.HasPrefix(key, "input:"):
			var inp gen2Input
			if err := json.Unmarshal(val, &inp); err != nil {
				continue
			}
			lbl := strings.Replace(key, ":", "", 1)
			if c.device.wantsMetric("input_state") {
				v := 0.0
				if inp.State {
					v = 1.0
				}
				metrics = append(metrics, Metric{Key: "input_state", Label: lbl, Value: v})
			}

		case strings.HasPrefix(key, "temperature:"):
			var t gen2Temperature
			if err := json.Unmarshal(val, &t); err != nil {
				continue
			}
			lbl := strings.Replace(key, ":", "", 1)
			if c.device.wantsMetric("temperature") {
				metrics = append(metrics, Metric{Key: "temperature", Label: lbl, Value: t.TC})
			}

		case strings.HasPrefix(key, "humidity:"):
			var h gen2Humidity
			if err := json.Unmarshal(val, &h); err != nil {
				continue
			}
			lbl := strings.Replace(key, ":", "", 1)
			if c.device.wantsMetric("humidity") {
				metrics = append(metrics, Metric{Key: "humidity", Label: lbl, Value: h.RH})
			}

		case strings.HasPrefix(key, "devicepower:"):
			var dp gen2DevicePower
			if err := json.Unmarshal(val, &dp); err != nil {
				continue
			}
			if c.device.wantsMetric("battery") {
				metrics = append(metrics, Metric{Key: "battery", Value: dp.Battery.Percent})
			}
		}
	}

	return metrics, nil
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func (c *ShellyClient) getJSON(ctx context.Context, path string, dst any) error {
	u := &url.URL{
		Scheme: "http",
		Host:   c.device.Address,
		Path:   path,
	}
	reqURL := u.String()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if c.device.Username != "" {
		req.SetBasicAuth(c.device.Username, c.device.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP GET %s: %w", reqURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP GET %s: unexpected status %d", reqURL, resp.StatusCode)
	}

	// Read response body with a 1MB limit to prevent OOM attacks from fake devices
	limitReader := io.LimitReader(resp.Body, 1024*1024)
	body, err := io.ReadAll(limitReader)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("parsing JSON from %s: %w", reqURL, err)
	}
	return nil
}
