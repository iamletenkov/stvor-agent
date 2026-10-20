// Command genman writes the manual page stvor-agent.8.gz into a directory.
// The page is rendered by internal/cli from the same command table as --help;
// the date comes from SOURCE_DATE_EPOCH and gzip carries no name and no time,
// so the output is reproducible. Used by make deb:
// go run ./tools/genman -out DIR -version V [-plain].
package main

import (
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/cli"
)

const page = "stvor-agent.8"

func main() {
	out := flag.String("out", "", "output directory")
	version := flag.String("version", "dev", "version for the page footer")
	plain := flag.Bool("plain", false, "also write the uncompressed page")
	flag.Parse()
	if err := run(*out, *version, *plain); err != nil {
		fmt.Fprintln(os.Stderr, "genman:", err)
		os.Exit(1)
	}
}

func run(out, version string, plain bool) error {
	if out == "" {
		return errors.New("-out is required")
	}
	epoch, err := strconv.ParseInt(os.Getenv("SOURCE_DATE_EPOCH"), 10, 64)
	if err != nil {
		return fmt.Errorf("SOURCE_DATE_EPOCH: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	text := cli.ManPage(version, time.Unix(epoch, 0))
	if plain {
		if err := os.WriteFile(filepath.Join(out, page), []byte(text), 0o644); err != nil {
			return err
		}
	}
	path := filepath.Join(out, page+".gz")
	if err := writeGzip(path, text); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// writeGzip writes s gzipped at the best compression, with no name and no
// modification time in the header.
func writeGzip(path, s string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err == nil {
		_, err = zw.Write([]byte(s))
	}
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
