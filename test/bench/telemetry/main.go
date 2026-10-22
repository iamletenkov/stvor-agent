// Command telemetry is the budget harness of the telemetry service (Stvor plan
// Task 21): the very loop of `stvor-agent telemetry` (cli.App, the same
// collectors, buffer and sender), but with its files under -dir instead of
// /etc/stvor-agent-telemetry, /var/lib and /run — so it runs as an ordinary
// user on a real machine without a package, a system user or a change of
// system config. With test/e2e/fakeportal as the portal:
//
//	<dir>/token   url=https://localhost:8443/robot-api/v1, token=… (the
//	              fakeportal file telemetry-token holds the same token)
//	<dir>/state/  buffer, boot-id, token-rejected marker
//	<dir>/run/    status.json
//
// The fakeportal CA goes in via SSL_CERT_FILE. -cpuprofile records the CPU
// profile of the whole run, -heapprofile writes the heap profile on exit
// (SIGTERM/SIGINT); both for `go tool pprof`.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"

	"github.com/iamletenkov/stvor-agent/internal/cli"
	"github.com/iamletenkov/stvor-agent/internal/version"
)

func main() {
	dir := flag.String("dir", "", "directory of the harness: token, state/, run/")
	heap := flag.String("heapprofile", "", "write the heap profile here on exit")
	cpu := flag.String("cpuprofile", "", "record the CPU profile of the run here")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "telemetry: -dir is required")
		os.Exit(2)
	}
	a := cli.New(version.Version, os.Stderr)
	a.TelemetryTokenPath = filepath.Join(*dir, "token")
	a.TelemetryStateDir = filepath.Join(*dir, "state")
	a.TelemetryRunDir = filepath.Join(*dir, "run")
	for _, d := range []string{a.TelemetryStateDir, a.TelemetryRunDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "telemetry:", err)
			os.Exit(1)
		}
	}
	stop, err := startCPU(*cpu)
	if err != nil {
		fmt.Fprintln(os.Stderr, "telemetry: cpu profile:", err)
		os.Exit(1)
	}
	code := a.Run([]string{"telemetry"}, os.Stdin, os.Stdout, os.Stderr)
	stop()
	if *heap != "" {
		if err := writeHeap(*heap); err != nil {
			fmt.Fprintln(os.Stderr, "telemetry: heap profile:", err)
		}
	}
	os.Exit(code)
}

func writeHeap(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// startCPU starts the CPU profile into path ("" — none) and returns its stop.
func startCPU(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		pprof.StopCPUProfile()
		_ = f.Close()
	}, nil
}
