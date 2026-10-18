package daemon

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// Knobs of the service (spec §5.6): the cream domain stvor-agent, read by the
// unit as EnvironmentFile=-/etc/cream/stvor-agent/config.
const (
	EnvInterval = "STVOR_AGENT_INTERVAL"
	EnvLogLevel = "STVOR_AGENT_LOG_LEVEL"

	DefaultInterval = 60 * time.Second
	MinInterval     = 60 * time.Second // Traefik: 6 requests a minute per Authorization
	MaxInterval     = 600 * time.Second
)

// Config is the service configuration from the environment.
type Config struct {
	Interval time.Duration
	LogLevel slog.Level
}

// LoadConfig reads the knobs through getenv (os.Getenv in the service). A
// value out of its schema falls back to the default and adds one problem —
// a journal line for the caller, who builds the logger from LogLevel first.
func LoadConfig(getenv func(string) string) (Config, []string) {
	cfg := Config{Interval: DefaultInterval, LogLevel: slog.LevelInfo}
	var problems []string

	if v := getenv(EnvInterval); v != "" {
		// Atoi accepts a sign; the schema is plain digits.
		sec, err := strconv.Atoi(v)
		d := time.Duration(sec) * time.Second
		if err != nil || v[0] < '0' || v[0] > '9' || d < MinInterval || d > MaxInterval {
			problems = append(problems, fmt.Sprintf("%s=%q вне 60–600 с — интервал 60 с", EnvInterval, v))
		} else {
			cfg.Interval = d
		}
	}

	switch v := getenv(EnvLogLevel); v {
	case "", "info":
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	default:
		problems = append(problems, fmt.Sprintf("%s=%q не info и не debug — уровень info", EnvLogLevel, v))
	}
	return cfg, problems
}
