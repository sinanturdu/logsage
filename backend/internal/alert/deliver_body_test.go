package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
)

// The markers a receiver puts in its answer; none may reach the result, the
// JSON the API would show or the logs (ADR 0024 addendum: a public host that
// proxies to an internal one must not turn blind SSRF into visible SSRF).
const (
	htmlMarker   = `<html lang="tr-TR"><title>Internal admin 10.0.0.7</title>`
	secretMarker = `lsk_ingest_LEAKED0123456789abcdef`
	hugeMarker   = "HUGEBODYMARKER"
)

func receiver(t *testing.T, code int, body func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code >= 300 && code < 400 {
			w.Header().Set("Location", "http://10.0.0.7/admin?token="+secretMarker)
		}
		w.WriteHeader(code)
		body(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReceiverBodiesAreNeverKept(t *testing.T) {
	bodies := map[string]func(w http.ResponseWriter){
		"html": func(w http.ResponseWriter) { _, _ = w.Write([]byte(htmlMarker + strings.Repeat(" padding", 100))) },
		"json with a secret": func(w http.ResponseWriter) {
			_, _ = fmt.Fprintf(w, `{"error":"bad","token":%q,"path":"/internal/v1/secrets?key=%s"}`, secretMarker, secretMarker)
		},
		"huge": func(w http.ResponseWriter) {
			chunk := []byte(strings.Repeat(hugeMarker, 4096))
			for i := 0; i < 64; i++ { // about 3.5 MiB
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		},
	}
	codes := map[int]string{400: ErrHTTP4xx, 404: ErrHTTP4xx, 500: ErrHTTP5xx, 503: ErrHTTP5xx, 302: ErrUnknown, 429: ErrRateLimited}
	for _, kind := range []string{KindWebhook, KindSlack} {
		for name, body := range bodies {
			for code, category := range codes {
				t.Run(fmt.Sprintf("%s %s %d", kind, name, code), func(t *testing.T) {
					srv := receiver(t, code, body)
					var logs bytes.Buffer
					log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
					row := Claimed{ID: 1, ProjectID: "p1", Event: EventFiring, IdempotencyKey: "k", Payload: payloadBytes(t), Channel: sealedChannel(t, kind, srv.URL+"/hook?x=1", "lsw_s")}
					store := &fakeOutbox{rows: []Claimed{row}, results: map[int64]DeliveryResult{}}
					sealer, _ := secret.NewSealer(testKey)
					client := outbound.New(outbound.Options{AllowInsecureLocal: true})
					d := NewDeliverer(store, client, sealer, &fakeClock{now: t0}, "w1", DeliverOptions{MaxAttempts: 8}, func() float64 { return 0.5 }, log)
					if n, err := d.RunOnce(context.Background()); n != 1 || err != nil {
						t.Fatal(n, err)
					}
					r := store.results[1]
					if r.ResponseStatus == nil || *r.ResponseStatus != code || r.ErrorCategory != category {
						t.Fatalf("result %+v, want status %d and %s", r, code, category)
					}
					rec := NotificationRecord{ResponseStatus: r.ResponseStatus, ErrorCategory: r.ErrorCategory, Status: r.Status}
					shown, _ := json.Marshal(rec)
					stored, _ := json.Marshal(r)
					for _, out := range []string{string(shown), string(stored), logs.String()} {
						for _, m := range []string{"Internal admin", "10.0.0.7", secretMarker, hugeMarker, "/hook", "x=1", "/internal"} {
							if strings.Contains(out, m) {
								t.Fatalf("%q leaked into %s", m, out)
							}
						}
					}
				})
			}
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyIsAClosedList(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{outbound.ErrAddress, ErrBlockedAddress},
		{fmt.Errorf("wrapped: %w", outbound.ErrScheme), ErrBlockedAddress},
		{&outbound.Redacted{Target: "https://h", Cause: outbound.ErrHost}, ErrBlockedAddress},
		{context.Canceled, ErrCanceled},
		{context.DeadlineExceeded, ErrTimeout},
		{&outbound.Redacted{Target: "https://h", Cause: timeoutErr{}}, ErrTimeout},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused")}, ErrConnect},
		{outbound.ErrResolve, ErrConnect},
		{errors.New(`HTTP 400 <html lang="tr-TR">`), ErrUnknown},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Errorf("Classify(%v) = %s, want %s", c.err, got, c.want)
		}
	}
	for code, want := range map[int]string{400: ErrHTTP4xx, 408: ErrTimeout, 429: ErrRateLimited, 451: ErrHTTP4xx, 500: ErrHTTP5xx, 599: ErrHTTP5xx, 302: ErrUnknown} {
		if got := StatusCategory(code); got != want {
			t.Errorf("StatusCategory(%d) = %s, want %s", code, got, want)
		}
	}
	seen := map[string]bool{}
	for _, c := range ErrorCategories {
		seen[c] = true
	}
	for _, c := range cases {
		if !seen[Classify(c.err)] {
			t.Errorf("%s is not in the closed list", Classify(c.err))
		}
	}
}

func TestUnreachableReceiverIsAConnectError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	row := Claimed{ID: 1, ProjectID: "p1", Payload: payloadBytes(t), Channel: sealedChannel(t, KindWebhook, "http://"+addr+"/hook", "s")}
	store := &fakeOutbox{rows: []Claimed{row}, results: map[int64]DeliveryResult{}}
	sealer, _ := secret.NewSealer(testKey)
	d := NewDeliverer(store, outbound.New(outbound.Options{AllowInsecureLocal: true}), sealer, &fakeClock{now: t0}, "w1", DeliverOptions{MaxAttempts: 8}, func() float64 { return 0.5 }, nil)
	_, _ = d.RunOnce(context.Background())
	if r := store.results[1]; r.Status != OutPending || r.ErrorCategory != ErrConnect || r.ResponseStatus != nil {
		t.Fatalf("%+v", r)
	}
}
