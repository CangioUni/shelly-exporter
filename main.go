package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

func main() {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "/etc/shelly-exporter/config.json"
	}

	// Bootstrap logger at info level; replaced once config is loaded.
	log := newLogger("info")
	log.Info("starting shelly-exporter", "config", configPath)

	// Load initial config.
	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	log = newLogger(cfg.LogLevel)
	log.Info("config loaded", "devices", len(cfg.Devices), "log_level", cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// scrapeCtx is cancelled and recreated each time the config is reloaded.
	scrapeCtx, cancelScrapers := context.WithCancel(ctx)
	influxWriter := newInfluxWriter(cfg.InfluxDB, log)
	var scraperWg sync.WaitGroup
	startScrapers(scrapeCtx, cfg, influxWriter, log, &scraperWg)

	// Watch the config file for changes.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error("failed to create file watcher", "error", err)
	} else {
		defer watcher.Close()
		// Watch the directory so we catch atomic writes (rename-into-place).
		if watchErr := watcher.Add(filepath.Dir(configPath)); watchErr != nil {
			log.Warn("cannot watch config directory", "error", watchErr)
		}
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("shutdown signal received, stopping scrapers…")
			cancelScrapers()
			scraperWg.Wait()
			influxWriter.Close()
			log.Info("shelly-exporter stopped")
			return

		case event, ok := <-watcher.Events:
			if !ok {
				continue
			}
			if !isConfigEvent(event, configPath) {
				continue
			}
			log.Info("config file changed, reloading…", "event", event.Op.String())
			newCfg, loadErr := LoadConfig(configPath)
			if loadErr != nil {
				log.Error("reload failed, keeping current config", "error", loadErr)
				continue
			}

			// Stop existing scrapers.
			cancelScrapers()
			scraperWg.Wait()
			influxWriter.Close()

			// Apply new config.
			cfg = newCfg
			log = newLogger(cfg.LogLevel)
			log.Info("config reloaded", "devices", len(cfg.Devices), "log_level", cfg.LogLevel)

			influxWriter = newInfluxWriter(cfg.InfluxDB, log)
			scrapeCtx, cancelScrapers = context.WithCancel(ctx)
			scraperWg = sync.WaitGroup{}
			startScrapers(scrapeCtx, cfg, influxWriter, log, &scraperWg)

		case watchErr, ok := <-watcher.Errors:
			if !ok {
				continue
			}
			log.Warn("file watcher error", "error", watchErr)
		}
	}
}

// startScrapers launches one goroutine per device in cfg.
func startScrapers(ctx context.Context, cfg *Config, iw *InfluxWriter, log *appLogger, wg *sync.WaitGroup) {
	for _, d := range cfg.Devices {
		d := d // capture loop variable
		wg.Add(1)
		go func() {
			defer wg.Done()
			runScraper(ctx, d, iw, log)
		}()
	}
}

// runScraper polls a single Shelly device until ctx is cancelled.
func runScraper(ctx context.Context, device DeviceConfig, iw *InfluxWriter, log *appLogger) {
	client := newShellyClient(device, log)
	interval := time.Duration(device.interval()) * time.Second
	log.Info("scraper started", "device", device.Name, "address", device.Address, "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Scrape once immediately before the first tick.
	scrapeAndWrite(ctx, client, iw, device.Name, log)

	for {
		select {
		case <-ctx.Done():
			log.Info("scraper stopped", "device", device.Name)
			return
		case <-ticker.C:
			scrapeAndWrite(ctx, client, iw, device.Name, log)
		}
	}
}

func scrapeAndWrite(ctx context.Context, client *ShellyClient, iw *InfluxWriter, deviceName string, log *appLogger) {
	metrics, err := client.Scrape(ctx)
	if err != nil {
		log.Error("scrape failed", "device", deviceName, "error", err)
		return
	}
	log.Debug("scraped metrics", "device", deviceName, "count", len(metrics))
	if writeErr := iw.Write(deviceName, metrics); writeErr != nil {
		log.Error("write failed", "device", deviceName, "error", writeErr)
	}
}

// isConfigEvent returns true when an fsnotify event is a write/create/rename
// targeting the config file itself.
func isConfigEvent(e fsnotify.Event, configPath string) bool {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		abs = configPath
	}
	evAbs, err := filepath.Abs(e.Name)
	if err != nil {
		evAbs = e.Name
	}
	if evAbs != abs {
		return false
	}
	return e.Has(fsnotify.Write) || e.Has(fsnotify.Create) || e.Has(fsnotify.Rename)
}
