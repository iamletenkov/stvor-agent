// Package telemetry is the client of the Stvor robot telemetry protocol v1
// (Stvor spec "robot telemetry" §5, plan Task 15): batches of metrics from
// the buffer to POST /robot-api/v1/metrics, agent events to POST /events and
// the inventory to PUT /inventory — strictly one request at a time, gzip
// JSON, Bearer telemetry token.
//
// The contract is the portal's backend/app/modules/robot_telemetry/contract/
// v1.json; a byte copy is compiled in (contract/v1.json, pinned by
// tools/telemetry-contract.sha256). The copy is the only source of the
// metric whitelist, the limits and the safe ranges of the configuration the
// portal sends back: a value outside them is ignored, so the portal can never
// make the agent read another path or run a command — the configuration only
// turns known metrics on and off and moves intervals and limits inside the
// compiled-in ranges.
package telemetry

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

//go:embed contract/v1.json
var contractV1 []byte

// Contract is what the agent takes from contract v1.
type Contract struct {
	Schema       int
	Metrics      map[string]string // name → "gauge" | "counter"
	Capabilities map[string]bool
	EventKinds   map[string]bool
	// Events are events.kinds: the default severity and the allowed params.
	Events     map[string]EventKind
	Limits     Limits
	Defaults   Defaults
	Ranges     Ranges
	BackoffMin int // s
	BackoffMax int // s
	// DropCodes are the error codes of statuses."400": a 400 with one of them
	// drops its batch; any other 400 (a proxy in front of the portal) does not.
	DropCodes map[string]bool
}

// EventKind is one entry of events.kinds.
type EventKind struct {
	Severity string   `json:"severity"`
	Params   []string `json:"params"`
}

// Limits are the request limits of the contract.
type Limits struct {
	BodyGzipMax      int `json:"body_gzip_max_bytes"`
	BodyInflatedMax  int `json:"body_inflated_max_bytes"`
	SeriesPerBatch   int `json:"series_per_batch_max"`
	PointsPerBatch   int `json:"points_per_batch_max"`
	EventsPerRequest int `json:"events_per_request_max"`
	EventParamsMax   int `json:"event_params_max_bytes"`
	FailedUnitsMax   int `json:"failed_units_max"`
	InventoryBodyMax int `json:"inventory_body_max_bytes"`
	LabelValueMax    int `json:"label_value_max"`
}

// Defaults is config.defaults of the contract.
type Defaults struct {
	FastIntervalS   int  `json:"fast_interval_s"`
	SlowIntervalS   int  `json:"slow_interval_s"`
	InventoryCheckS int  `json:"inventory_check_s"`
	PerCore         bool `json:"per_core"`
	BufferQuotaMiB  int  `json:"buffer_quota_mib"`
	AutonomyDays    int  `json:"autonomy_days"`
	DetailSampleS   int  `json:"detail_sample_s"`
	DetailSendS     int  `json:"detail_send_s"`
}

// Ranges is config.ranges of the contract: inclusive [min, max].
type Ranges struct {
	FastIntervalS   [2]int `json:"fast_interval_s"`
	SlowIntervalS   [2]int `json:"slow_interval_s"`
	InventoryCheckS [2]int `json:"inventory_check_s"`
	BufferQuotaMiB  [2]int `json:"buffer_quota_mib"`
	AutonomyDays    [2]int `json:"autonomy_days"`
	DetailMaxS      int    `json:"detail_max_s"`
	NextAfterMS     [2]int `json:"next_after_ms"`
}

type contractFile struct {
	Contract string `json:"contract"`
	Version  int    `json:"version"`
	Schema   int    `json:"schema"`
	Retry    struct {
		BackoffMinS int `json:"backoff_min_s"`
		BackoffMaxS int `json:"backoff_max_s"`
	} `json:"retry"`
	Limits  Limits `json:"limits"`
	Metrics []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"metrics"`
	Capabilities []string `json:"capabilities"`
	Events       struct {
		Kinds map[string]EventKind `json:"kinds"`
	} `json:"events"`
	Statuses map[string]string `json:"statuses"`
	Config   struct {
		Defaults Defaults `json:"defaults"`
		Ranges   Ranges   `json:"ranges"`
	} `json:"config"`
}

// ContractV1 parses the compiled-in contract.
func ContractV1() (*Contract, error) { return parseContract(contractV1) }

func parseContract(data []byte) (*Contract, error) {
	var f contractFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("контракт телеметрии: %w", err)
	}
	if f.Contract != "stvor-robot-telemetry" || f.Version != 1 || f.Schema != 1 {
		return nil, errors.New("контракт телеметрии: не stvor-robot-telemetry v1, схема 1")
	}
	c := &Contract{
		Schema: f.Schema, Metrics: map[string]string{}, Capabilities: map[string]bool{},
		EventKinds: map[string]bool{}, Events: f.Events.Kinds, Limits: f.Limits, Defaults: f.Config.Defaults, Ranges: f.Config.Ranges,
		BackoffMin: f.Retry.BackoffMinS, BackoffMax: f.Retry.BackoffMaxS,
	}
	for _, m := range f.Metrics {
		c.Metrics[m.Name] = m.Type
	}
	for _, k := range f.Capabilities {
		c.Capabilities[k] = true
	}
	for k := range f.Events.Kinds {
		c.EventKinds[k] = true
	}
	c.DropCodes = dropCodes(f.Statuses["400"])
	r, l := c.Ranges, c.Limits
	ok := len(c.Metrics) > 0 && c.BackoffMin > 0 && c.BackoffMax >= c.BackoffMin &&
		l.BodyGzipMax > 0 && l.BodyInflatedMax > 0 && l.SeriesPerBatch > 0 && l.PointsPerBatch > 0 &&
		l.EventsPerRequest > 0 && l.EventParamsMax > 0 && l.FailedUnitsMax > 0 && l.InventoryBodyMax > 0 &&
		l.LabelValueMax > 0 && len(c.DropCodes) > 0 && r.DetailMaxS > 0 && c.Defaults.DetailSampleS > 0 && c.Defaults.DetailSendS > 0
	for _, rg := range [][2]int{r.FastIntervalS, r.SlowIntervalS, r.InventoryCheckS, r.BufferQuotaMiB, r.AutonomyDays, r.NextAfterMS} {
		ok = ok && rg[0] >= 0 && rg[1] > 0 && rg[0] <= rg[1]
	}
	d := c.Defaults
	ok = ok && in(d.FastIntervalS, r.FastIntervalS) && in(d.SlowIntervalS, r.SlowIntervalS) &&
		in(d.InventoryCheckS, r.InventoryCheckS) && in(d.BufferQuotaMiB, r.BufferQuotaMiB) && in(d.AutonomyDays, r.AutonomyDays)
	if !ok {
		return nil, errors.New("контракт телеметрии: нет лимитов, диапазонов или белого списка")
	}
	return c, nil
}

// reDropCodes is the "(code: a | b | …)" list of statuses."400".
var reDropCodes = regexp.MustCompile(`\(code:([^)]*)\)`)

// dropCodes reads the error codes out of the text of statuses."400".
func dropCodes(text string) map[string]bool {
	m := reDropCodes.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	out := map[string]bool{}
	for _, c := range strings.Split(m[1], "|") {
		if c = strings.TrimSpace(c); c != "" {
			out[c] = true
		}
	}
	return out
}

func in(v int, r [2]int) bool { return v >= r[0] && v <= r[1] }
