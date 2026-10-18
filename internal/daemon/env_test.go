package daemon

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		env          map[string]string
		wantInterval time.Duration
		wantLevel    slog.Level
		wantProblems int
	}{
		{"defaults", nil, 60 * time.Second, slog.LevelInfo, 0},
		{"interval 300", map[string]string{"STVOR_AGENT_INTERVAL": "300"}, 300 * time.Second, slog.LevelInfo, 0},
		{"interval min", map[string]string{"STVOR_AGENT_INTERVAL": "60"}, 60 * time.Second, slog.LevelInfo, 0},
		{"interval max", map[string]string{"STVOR_AGENT_INTERVAL": "600"}, 600 * time.Second, slog.LevelInfo, 0},
		{"interval below", map[string]string{"STVOR_AGENT_INTERVAL": "59"}, 60 * time.Second, slog.LevelInfo, 1},
		{"interval above", map[string]string{"STVOR_AGENT_INTERVAL": "601"}, 60 * time.Second, slog.LevelInfo, 1},
		{"interval not a number", map[string]string{"STVOR_AGENT_INTERVAL": "1m"}, 60 * time.Second, slog.LevelInfo, 1},
		{"interval with spaces", map[string]string{"STVOR_AGENT_INTERVAL": " 120"}, 60 * time.Second, slog.LevelInfo, 1},
		{"interval signed", map[string]string{"STVOR_AGENT_INTERVAL": "+120"}, 60 * time.Second, slog.LevelInfo, 1},
		{"debug", map[string]string{"STVOR_AGENT_LOG_LEVEL": "debug"}, 60 * time.Second, slog.LevelDebug, 0},
		{"info", map[string]string{"STVOR_AGENT_LOG_LEVEL": "info"}, 60 * time.Second, slog.LevelInfo, 0},
		{"unknown level", map[string]string{"STVOR_AGENT_LOG_LEVEL": "DEBUG"}, 60 * time.Second, slog.LevelInfo, 1},
		{"both bad", map[string]string{"STVOR_AGENT_INTERVAL": "5", "STVOR_AGENT_LOG_LEVEL": "trace"}, 60 * time.Second, slog.LevelInfo, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, problems := LoadConfig(func(k string) string { return tc.env[k] })
			if cfg.Interval != tc.wantInterval || cfg.LogLevel != tc.wantLevel {
				t.Fatalf("LoadConfig = %+v, want interval %v level %v", cfg, tc.wantInterval, tc.wantLevel)
			}
			if len(problems) != tc.wantProblems {
				t.Fatalf("problems = %q, want %d", problems, tc.wantProblems)
			}
		})
	}
}
