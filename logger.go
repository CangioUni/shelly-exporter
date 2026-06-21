package main

import (
	"log/slog"
	"os"
	"strings"
)

// appLogger wraps slog.Logger and provides level-aware helpers.
type appLogger struct {
	l *slog.Logger
}

// newLogger creates an appLogger writing JSON to stderr at the given level string.
// Valid levels: "debug", "info", "warn", "error". Defaults to "info".
func newLogger(level string) *appLogger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return &appLogger{l: slog.New(h)}
}

func (a *appLogger) Debug(msg string, args ...any) { a.l.Debug(msg, args...) }
func (a *appLogger) Info(msg string, args ...any)  { a.l.Info(msg, args...) }
func (a *appLogger) Warn(msg string, args ...any)  { a.l.Warn(msg, args...) }
func (a *appLogger) Error(msg string, args ...any) { a.l.Error(msg, args...) }
