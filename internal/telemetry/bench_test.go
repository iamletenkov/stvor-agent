package telemetry

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

// sampleBody is a JSON body of the size of a metrics batch on a Jetson Nano
// (≈ 100 series, a minute of fast ticks).
func sampleBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"schema":1,"series":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"system.network.io","labels":{"iface":"eth%d","direction":"receive"},"points":[[0,%d],[10000,%d]]}`,
			i, 1000*i, 1000*i+17)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func gunzip(t *testing.T, gz []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return raw
}

// The budget run on a Jetson Nano (docs/research/2026-10-22-telemetry-budget-jetson.md):
// a fresh BestCompression writer per request allocated and zeroed ~1 MB of
// fresh pages each time — a third of all bytes allocated and an eighth of the
// CPU. The writer is reused: every body still round-trips, a failed or empty
// body does not spoil the next one, and a call allocates little.
func TestGzipperReusesWriter(t *testing.T) {
	var g gzipper
	bodies := [][]byte{sampleBody(100), []byte(`{}`), nil, sampleBody(3), sampleBody(400), sampleBody(100)}
	for i, raw := range bodies {
		gz, err := g.gzip(raw)
		if err != nil {
			t.Fatalf("body %d: %v", i, err)
		}
		if got := gunzip(t, gz); !bytes.Equal(got, raw) {
			t.Fatalf("body %d: round trip differs", i)
		}
	}

	raw := sampleBody(100)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const calls = 20
	for range calls {
		if _, err := g.gzip(raw); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	// A new writer at BestCompression is ~1 MB; the reused one leaves the
	// output buffer and little else.
	if per := (after.TotalAlloc - before.TotalAlloc) / calls; per > 128<<10 {
		t.Fatalf("gzip allocates %d bytes a call, want ≤ 128 KiB (writer not reused?)", per)
	}
}

func BenchmarkGzipper(b *testing.B) {
	var g gzipper
	raw := sampleBody(100)
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		if _, err := g.gzip(raw); err != nil {
			b.Fatal(err)
		}
	}
}
