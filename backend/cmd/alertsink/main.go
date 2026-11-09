// Command alertsink is a tiny local webhook receiver for trying alerts
// (ADR 0024). It verifies the X-LogSage-Signature with the channel secret
// (ALERTSINK_SECRET, never a flag), rejects old timestamps, counts repeated
// deliveries and lists what it got at GET /deliveries. It listens on
// loopback only; it is a development tool, not a production receiver.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
)

// Delivery is one received request.
type Delivery struct {
	At        time.Time       `json:"at"`
	Event     string          `json:"event"`
	Key       string          `json:"delivery"`
	Verified  bool            `json:"verified"`
	Error     string          `json:"error,omitempty"`
	Duplicate bool            `json:"duplicate"`
	Payload   json.RawMessage `json:"payload"`
}

// Sink records deliveries; failFirst answers 500 to that many requests to
// show retries.
type Sink struct {
	secret    string
	now       func() time.Time
	mu        sync.Mutex
	got       []Delivery
	seen      map[string]bool
	failFirst int
}

// NewSink returns a sink that checks signatures with secret (empty: no check).
func NewSink(secret string, failFirst int, now func() time.Time) *Sink {
	return &Sink{secret: secret, now: now, seen: map[string]bool{}, failFirst: failFirst}
}

func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/deliveries" {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.got)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	d := Delivery{At: s.now(), Event: r.Header.Get("X-LogSage-Event"), Key: r.Header.Get("X-LogSage-Delivery")}
	if json.Valid(body) {
		d.Payload = body
	} else {
		// Slack-style or broken bodies are kept as a JSON string.
		d.Payload, _ = json.Marshal(string(body))
	}
	if s.secret != "" {
		if err := secret.Verify(s.secret, r.Header.Get("X-LogSage-Signature"), body, s.now(), 5*time.Minute); err != nil {
			d.Error = err.Error()
		} else {
			d.Verified = true
		}
	}
	s.mu.Lock()
	d.Duplicate = d.Key != "" && s.seen[d.Key]
	if d.Key != "" {
		s.seen[d.Key] = true
	}
	s.got = append(s.got, d)
	fail := s.failFirst > 0
	if fail {
		s.failFirst--
	}
	s.mu.Unlock()
	switch {
	case s.secret != "" && !d.Verified:
		http.Error(w, "bad signature", http.StatusUnauthorized)
	case fail:
		http.Error(w, "failing on purpose", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9099", "listen address (loopback only)")
	fail := flag.Int("fail-first", 0, "answer 500 to this many requests first (to see retries)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *addr, os.Getenv("ALERTSINK_SECRET"), *fail, nil); err != nil {
		fmt.Fprintln(os.Stderr, "alertsink:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr, sec string, fail int, ready chan<- string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("listen on a loopback address only (127.0.0.1, ::1 or localhost)")
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	sink := NewSink(sec, fail, func() time.Time { return time.Now().UTC() })
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sink.ServeHTTP(w, r)
		if r.Method == http.MethodPost {
			sink.mu.Lock()
			d := sink.got[len(sink.got)-1]
			sink.mu.Unlock()
			log.Info("delivery", "event", d.Event, "delivery", d.Key, "verified", d.Verified, "duplicate", d.Duplicate, "error", d.Error)
		}
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	if sec == "" {
		log.Warn("ALERTSINK_SECRET is not set: signatures are not checked")
	}
	log.Info("alertsink listening", "addr", ln.Addr().String())
	if ready != nil {
		ready <- ln.Addr().String()
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
