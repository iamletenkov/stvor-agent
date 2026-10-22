package telemetry

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"
)

// Config is the configuration in effect: the contract defaults changed by
// the portal answers, only inside the compiled-in ranges.
type Config struct {
	Hash           string          // config_hash of the last applied answer; "" — defaults
	Metrics        map[string]bool // enabled metrics, a subset of the contract whitelist
	FastInterval   time.Duration
	SlowInterval   time.Duration
	InventoryCheck time.Duration
	PerCore        bool
	BufferQuota    int64 // bytes
	Autonomy       time.Duration
	// DetailUntil is the end of the 5 s mode (zero — off); never more than
	// detail_max_s ahead of the moment it was applied.
	DetailUntil  time.Time
	DetailSample time.Duration
	DetailSend   time.Duration
}

// Detail tells whether the detail mode is on at now.
func (c Config) Detail(now time.Time) bool { return now.Before(c.DetailUntil) }

// Enabled tells whether metric name is sent.
func (c Config) Enabled(name string) bool { return c.Metrics[name] }

// DefaultConfig is config.defaults of the contract with every metric of the
// whitelist enabled.
func (k *Contract) DefaultConfig() Config {
	d := k.Defaults
	m := make(map[string]bool, len(k.Metrics))
	for name := range k.Metrics {
		m[name] = true
	}
	return Config{
		Metrics:        m,
		FastInterval:   seconds(d.FastIntervalS),
		SlowInterval:   seconds(d.SlowIntervalS),
		InventoryCheck: seconds(d.InventoryCheckS),
		PerCore:        d.PerCore,
		BufferQuota:    int64(d.BufferQuotaMiB) << 20,
		Autonomy:       time.Duration(d.AutonomyDays) * 24 * time.Hour,
		DetailSample:   seconds(d.DetailSampleS),
		DetailSend:     seconds(d.DetailSendS),
	}
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

// wireConfig is the body of a 200/202 answer. Absent fields keep the value in
// effect; a field of a wrong JSON type spoils the whole body (ignored).
type wireConfig struct {
	Status          string    `json:"status"`
	ConfigHash      *string   `json:"config_hash"`
	Metrics         *[]string `json:"metrics"`
	FastIntervalS   *int64    `json:"fast_interval_s"`
	SlowIntervalS   *int64    `json:"slow_interval_s"`
	InventoryCheckS *int64    `json:"inventory_check_s"`
	PerCore         *bool     `json:"per_core"`
	BufferQuotaMiB  *int64    `json:"buffer_quota_mib"`
	AutonomyDays    *int64    `json:"autonomy_days"`
	DetailUntil     *string   `json:"detail_until"`
	NextAfterMS     *int64    `json:"next_after_ms"`
}

var reSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// maxConfigMetrics bounds the metric list a portal answer may carry.
const maxConfigMetrics = 256

// applied is the result of one answer: the new configuration, the pause the
// portal asked for before the next backlog batch, and what was ignored (for
// the journal: field=value, no secrets can be there).
type applied struct {
	cfg       Config
	nextAfter time.Duration
	ignored   []string
}

// apply lays an answer over cur. Every value outside the compiled-in range is
// ignored and listed; detail_until beyond detail_max_s is cut to it.
func (k *Contract) apply(cur Config, w wireConfig, now time.Time) applied {
	out := applied{cfg: cur}
	cfg := &out.cfg
	cfg.Metrics = maps.Clone(cur.Metrics)
	ignore := func(field string, v any) { out.ignored = append(out.ignored, fmt.Sprintf("%s=%v", field, v)) }
	r := k.Ranges

	if w.ConfigHash != nil {
		if reSHA256.MatchString(*w.ConfigHash) {
			cfg.Hash = *w.ConfigHash
		} else {
			ignore("config_hash", "не sha256")
		}
	}
	if w.Metrics != nil {
		if len(*w.Metrics) > maxConfigMetrics {
			ignore("metrics", fmt.Sprintf("%d имён", len(*w.Metrics)))
		} else {
			m, unknown := map[string]bool{}, 0
			for _, name := range *w.Metrics {
				if _, ok := k.Metrics[name]; ok {
					m[name] = true
				} else {
					unknown++
				}
			}
			if unknown > 0 {
				// The names are untrusted: only their count goes to the journal.
				ignore("metrics", fmt.Sprintf("%d вне белого списка", unknown))
			}
			cfg.Metrics = m
		}
	}
	setDur := func(field string, v *int64, rg [2]int, unit time.Duration, dst *time.Duration) {
		if v == nil {
			return
		}
		if *v < int64(rg[0]) || *v > int64(rg[1]) {
			ignore(field, *v)
			return
		}
		*dst = time.Duration(*v) * unit
	}
	setDur("fast_interval_s", w.FastIntervalS, r.FastIntervalS, time.Second, &cfg.FastInterval)
	setDur("slow_interval_s", w.SlowIntervalS, r.SlowIntervalS, time.Second, &cfg.SlowInterval)
	setDur("inventory_check_s", w.InventoryCheckS, r.InventoryCheckS, time.Second, &cfg.InventoryCheck)
	setDur("autonomy_days", w.AutonomyDays, r.AutonomyDays, 24*time.Hour, &cfg.Autonomy)
	setDur("next_after_ms", w.NextAfterMS, r.NextAfterMS, time.Millisecond, &out.nextAfter)
	if v := w.BufferQuotaMiB; v != nil {
		if *v < int64(r.BufferQuotaMiB[0]) || *v > int64(r.BufferQuotaMiB[1]) {
			ignore("buffer_quota_mib", *v)
		} else {
			cfg.BufferQuota = *v << 20
		}
	}
	if w.PerCore != nil {
		cfg.PerCore = *w.PerCore
	}
	if w.DetailUntil != nil {
		switch t, err := time.Parse(time.RFC3339Nano, *w.DetailUntil); {
		case *w.DetailUntil == "":
			cfg.DetailUntil = time.Time{}
		case err != nil:
			ignore("detail_until", "не RFC 3339")
		case !t.After(now):
			cfg.DetailUntil = time.Time{}
		default:
			if ceil := now.Add(seconds(r.DetailMaxS)); t.After(ceil) {
				ignore("detail_until", "дальше "+seconds(r.DetailMaxS).String()+" — обрезано")
				t = ceil
			}
			cfg.DetailUntil = t
		}
	}
	return out
}

// parseConfig reads an answer body; nil — no configuration in it.
func parseConfig(body []byte) (*wireConfig, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var w wireConfig
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("конфигурация в ответе портала: %w", err)
	}
	return &w, nil
}

// quoteCut quotes an untrusted short code for the journal, cut to 64 bytes.
func quoteCut(s string) string {
	b := []byte(s)
	if len(b) > 64 {
		b = b[:64]
	}
	return fmt.Sprintf("%q", b)
}

// enabledList is the sorted list of enabled metrics (status and tests).
func (c Config) enabledList() []string {
	return slices.Sorted(maps.Keys(c.Metrics))
}
