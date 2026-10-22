package telemetry

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
)

// metricsBody skips non-finite points (raw and rolled-up): the JSON encoder
// would fail on them on every retry. A series left without points is dropped.
func TestMetricsBodySkipsNonFinite(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ok)
	nan, inf := math.NaN(), math.Inf(1)
	c := &buffer.Chunk{Kind: buffer.ChunkFresh, Stream: "s", Seq: 1, BatchID: "b", Segment: &buffer.Segment{
		BootID: testBoot, StartAt: e.clk.Now(), RollupS: 0,
		Series: []buffer.Series{
			{Name: "system.cpu.load_average.1m", Kind: buffer.Gauge, Points: []buffer.Point{
				{Off: 0, V: nan}, {Off: 10, V: 0.5}, {Off: 20, V: inf}, {Off: 30, V: math.Inf(-1)}}},
			{Name: "system.memory.usage", Kind: buffer.Gauge, Labels: []buffer.Label{{K: "state", V: "used"}},
				Points: []buffer.Point{
					{Off: 0, V: 1, Agg: &buffer.Agg{Min: nan, Avg: 1, Max: 2, N: 2}},
					{Off: 10, V: 1, Agg: &buffer.Agg{Min: 0, Avg: 1, Max: inf, N: 2}},
					{Off: 20, V: inf, Agg: &buffer.Agg{Min: 0, Avg: inf, Max: 2, N: 2}}}},
		},
	}}
	raw, over, err := e.s.metricsBody(c, e.clk.Now())
	if err != nil || over != "" {
		t.Fatalf("metricsBody: over %q err %v", over, err)
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	series := b["series"].([]any)
	if len(series) != 1 {
		t.Fatalf("series %v, want only the load average (memory has no finite point)", series)
	}
	pts := series[0].(map[string]any)["points"].([]any)
	if len(pts) != 1 || pts[0].([]any)[0] != 10.0 || pts[0].([]any)[1] != 0.5 {
		t.Errorf("points %v, want [[10, 0.5]]", pts)
	}
}

// An empty or malformed machine_id_hash is not sent: absent rather than invalid.
func TestMetricsBodyMachineIDHash(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mid string
		sent      bool
	}{
		{"valid", testMID, true},
		{"empty", "", false},
		{"not_hex", "zz" + testMID[2:], false},
		{"short", testMID[:63], false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, ok)
			e.s.opt.Meta = func() Meta { return Meta{AgentVersion: "4.1.0", MachineIDHash: tc.mid} }
			e.tick(t)
			if r := e.step(t); r.Outcome != OutcomeSent {
				t.Fatalf("step %+v", r)
			}
			body := e.portal.requests()[0].Body
			got, has := body["machine_id_hash"]
			if has != tc.sent || tc.sent && got != tc.mid {
				t.Errorf("machine_id_hash = %v (present %t), want present %t", got, has, tc.sent)
			}
		})
	}
}
