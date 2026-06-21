package main

import (
	"fmt"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

// InfluxWriter writes Shelly metrics to InfluxDB.
type InfluxWriter struct {
	client   influxdb2.Client
	writeAPI api.WriteAPI
	cfg      InfluxDBConfig
	log      *appLogger
	errCh    <-chan error
}

// newInfluxWriter creates a new InfluxWriter. Call Close when done.
func newInfluxWriter(cfg InfluxDBConfig, l *appLogger) *InfluxWriter {
	opts := influxdb2.DefaultOptions().
		SetLogLevel(0) // suppress influxdb client's own logging

	client := influxdb2.NewClientWithOptions(cfg.URL, cfg.Token, opts)
	wapi := client.WriteAPI(cfg.Org, cfg.Bucket)

	iw := &InfluxWriter{
		client:   client,
		writeAPI: wapi,
		cfg:      cfg,
		log:      l,
		errCh:    wapi.Errors(),
	}

	// Drain async write errors in the background.
	go iw.drainErrors()

	return iw
}

func (iw *InfluxWriter) drainErrors() {
	for err := range iw.errCh {
		if err != nil {
			iw.log.Error("InfluxDB write error", "error", err)
		}
	}
}

// Write sends a batch of metrics for a device as a single InfluxDB point.
// Tags: device=<device name>
// Fields: one field per Metric using Metric.FieldName() as the field key.
func (iw *InfluxWriter) Write(deviceName string, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}

	measurement := iw.cfg.measurement()
	fields := make(map[string]interface{}, len(metrics))
	for _, m := range metrics {
		fields[m.FieldName()] = m.Value
	}

	p := write.NewPoint(
		measurement,
		map[string]string{"device": deviceName},
		fields,
		time.Now(),
	)

	iw.writeAPI.WritePoint(p)
	iw.log.Debug("wrote metrics to InfluxDB",
		"device", deviceName,
		"measurement", measurement,
		"fields", fmt.Sprintf("%d", len(fields)),
	)
	return nil
}

// Flush forces any buffered writes to be sent.
func (iw *InfluxWriter) Flush() {
	iw.writeAPI.Flush()
}

// Close flushes pending writes and releases resources.
func (iw *InfluxWriter) Close() {
	iw.writeAPI.Flush()
	iw.client.Close()
}
