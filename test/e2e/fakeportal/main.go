// Command fakeportal is the portal of the end-to-end test (plan Task 20, spec
// §15.3): one HTTPS server with its own CA that answers both the robot API of
// Stvor (/robot-api/v1/labels) and the Teleport proxy probe (/webapi/find).
//
// It lives in the test container only. On start it makes a fresh CA and a
// leaf certificate for -host, writes the CA to <dir>/ca.pem (the scenario
// puts it into the system roots), then serves -addr. The labels are the file
// <dir>/labels.json ({"env":…,"space":…}), read on every request, so the
// scenario moves the robot by rewriting it; the revision is the first 16 hex
// of sha256 over "env\nspace". The bearer token is <dir>/token. Every labels
// request is one JSON line in <dir>/requests.log: status, If-None-Match,
// X-Applied-Revision — what the scenario checks.
//
// Telemetry (Stvor plan Task 18): POST /robot-api/v1/telemetry-token (bearer —
// the labels token) issues a fresh token, writes it to <dir>/telemetry-token
// and revokes the previous one; POST /metrics, POST /events and PUT /inventory
// take the issued token only, a gzip JSON body, and answer 200 {"status":
// "accepted"}. An inventory without "body" (hash only) is answered 409, as by
// a portal that does not know the hash, so the agent sends the full body. Every
// telemetry request is one JSON line in <dir>/telemetry.log. A file
// <dir>/detail-until (RFC 3339) puts "detail_until" into every 200 — the 5 s
// mode of the budget run (test/bench/telemetry, Stvor plan Task 21).
package main

import (
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Labels is the content of labels.json.
type Labels struct {
	Env   string `json:"env"`
	Space string `json:"space"`
}

// Revision is the revision the portal gives these labels.
func (l Labels) Revision() string {
	sum := sha256.Sum256([]byte(l.Env + "\n" + l.Space))
	return hex.EncodeToString(sum[:])[:16]
}

// Request is one line of requests.log.
type Request struct {
	At              string `json:"at"`
	Status          int    `json:"status"`
	IfNoneMatch     string `json:"if_none_match"`
	AppliedRevision string `json:"x_applied_revision"`
	UserAgent       string `json:"user_agent"`
}

type server struct {
	dir string
	mu  sync.Mutex
}

// TelemetryRequest is one line of telemetry.log.
type TelemetryRequest struct {
	At            string `json:"at"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Status        int    `json:"status"`
	Encoding      string `json:"encoding"`
	UserAgent     string `json:"user_agent"`
	Schema        int    `json:"schema,omitempty"`
	BatchID       string `json:"batch_id,omitempty"`
	Series        int    `json:"series"`
	Events        int    `json:"events"`
	MachineIDHash string `json:"machine_id_hash,omitempty"`
	InventoryBody bool   `json:"inventory_body"`
	Error         string `json:"error,omitempty"`
}

// telemetryBody is what telemetry.log keeps of a request body.
type telemetryBody struct {
	Schema        int               `json:"schema"`
	BatchID       string            `json:"batch_id"`
	MachineIDHash string            `json:"machine_id_hash"`
	Series        []json.RawMessage `json:"series"`
	Events        []json.RawMessage `json:"events"`
	Body          json.RawMessage   `json:"body"`
}

func (s *server) appendLine(name string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line, _ := json.Marshal(v)
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("%s: %v", name, err)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

// bearer tells whether the request carries the token of the file name.
func (s *server) bearer(r *http.Request, name string) bool {
	want, err := os.ReadFile(filepath.Join(s.dir, name))
	tok := strings.TrimSpace(string(want))
	return err == nil && tok != "" && r.Header.Get("Authorization") == "Bearer "+tok
}

func (s *server) handleTelemetryToken(w http.ResponseWriter, r *http.Request) {
	rec := TelemetryRequest{At: time.Now().UTC().Format(time.RFC3339Nano), Method: r.Method, Path: r.URL.Path,
		UserAgent: r.Header.Get("User-Agent")}
	defer func() { s.appendLine("telemetry.log", rec) }()
	if !s.bearer(r, "token") {
		rec.Status = http.StatusUnauthorized
		w.WriteHeader(rec.Status)
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		rec.Status = http.StatusInternalServerError
		w.WriteHeader(rec.Status)
		return
	}
	tok := hex.EncodeToString(raw) // 64 of [0-9a-f]
	if err := os.WriteFile(filepath.Join(s.dir, "telemetry-token"), []byte(tok+"\n"), 0o600); err != nil {
		rec.Status = http.StatusInternalServerError
		w.WriteHeader(rec.Status)
		return
	}
	rec.Status = http.StatusOK
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"token":%q}`, tok)
}

func (s *server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	rec := TelemetryRequest{At: time.Now().UTC().Format(time.RFC3339Nano), Method: r.Method, Path: r.URL.Path,
		Encoding: r.Header.Get("Content-Encoding"), UserAgent: r.Header.Get("User-Agent")}
	defer func() { s.appendLine("telemetry.log", rec) }()
	answer := func(status int, body string) {
		rec.Status = status
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	if !s.bearer(r, "telemetry-token") {
		answer(http.StatusUnauthorized, `{"code":"unauthorized"}`)
		return
	}
	zr, err := gzip.NewReader(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		rec.Error = "gzip: " + err.Error()
		answer(http.StatusBadRequest, `{"code":"bad_encoding"}`)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 8<<20))
	var b telemetryBody
	if err == nil {
		err = json.Unmarshal(raw, &b)
	}
	if err != nil {
		rec.Error = "body: " + err.Error()
		answer(http.StatusBadRequest, `{"code":"bad_body"}`)
		return
	}
	rec.Schema, rec.BatchID, rec.MachineIDHash = b.Schema, b.BatchID, b.MachineIDHash
	rec.Series, rec.Events = len(b.Series), len(b.Events)
	rec.InventoryBody = len(b.Body) > 0 && string(b.Body) != "null"
	if r.URL.Path == "/robot-api/v1/inventory" && !rec.InventoryBody {
		answer(http.StatusConflict, `{"code":"need_body","message":"Инвентарь изменился — пришлите тело целиком","status":"need_body"}`)
		return
	}
	// detail-until (RFC 3339, the Task 21 budget run) turns the 5 s mode on.
	if until, err := os.ReadFile(filepath.Join(s.dir, "detail-until")); err == nil {
		answer(http.StatusOK, fmt.Sprintf(`{"status":"accepted","detail_until":%q}`,
			strings.TrimSpace(string(until))))
		return
	}
	answer(http.StatusOK, `{"status":"accepted"}`)
}

func (s *server) labels() (Labels, error) {
	var l Labels
	data, err := os.ReadFile(filepath.Join(s.dir, "labels.json"))
	if err != nil {
		return l, err
	}
	err = json.Unmarshal(data, &l)
	return l, err
}

func (s *server) record(r Request) { s.appendLine("requests.log", r) }

func (s *server) handleLabels(w http.ResponseWriter, r *http.Request) {
	rec := Request{
		At:              time.Now().UTC().Format(time.RFC3339Nano),
		IfNoneMatch:     r.Header.Get("If-None-Match"),
		AppliedRevision: r.Header.Get("X-Applied-Revision"),
		UserAgent:       r.Header.Get("User-Agent"),
	}
	defer func() { s.record(rec) }()
	want, err := os.ReadFile(filepath.Join(s.dir, "token"))
	if err != nil || r.Header.Get("Authorization") != "Bearer "+strings.TrimSpace(string(want)) {
		rec.Status = http.StatusUnauthorized
		w.WriteHeader(rec.Status)
		return
	}
	l, err := s.labels()
	if err != nil {
		rec.Status = http.StatusInternalServerError
		w.WriteHeader(rec.Status)
		return
	}
	rev := l.Revision()
	w.Header().Set("ETag", `"`+rev+`"`)
	if rec.IfNoneMatch == `"`+rev+`"` {
		rec.Status = http.StatusNotModified
		w.WriteHeader(rec.Status)
		return
	}
	rec.Status = http.StatusOK
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"labels":{"env":%q,"space":%q},"revision":%q}`, l.Env, l.Space, rev)
}

func handleFind(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"proxy":{},"server_version":"18.10.0"}`)
}

// certificates makes a CA and a leaf for host; the CA goes to caPath as PEM.
func certificates(host, caPath string) (tls.Certificate, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fakeportal e2e CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := os.WriteFile(caPath, caPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: key}, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:443", "listen address")
	host := flag.String("host", "portal.e2e", "DNS name of the certificate")
	dir := flag.String("dir", "/var/lib/fakeportal",
		"directory: labels.json, token, ca.pem, requests.log, telemetry-token, telemetry.log")
	flag.Parse()

	cert, err := certificates(*host, filepath.Join(*dir, "ca.pem"))
	if err != nil {
		log.Fatalf("certificates: %v", err)
	}
	s := &server{dir: *dir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /robot-api/v1/labels", s.handleLabels)
	mux.HandleFunc("GET /webapi/find", handleFind)
	mux.HandleFunc("POST /robot-api/v1/telemetry-token", s.handleTelemetryToken)
	mux.HandleFunc("POST /robot-api/v1/metrics", s.handleTelemetry)
	mux.HandleFunc("POST /robot-api/v1/events", s.handleTelemetry)
	mux.HandleFunc("PUT /robot-api/v1/inventory", s.handleTelemetry)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	log.Printf("fakeportal: %s (%s)", *addr, *host)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}
