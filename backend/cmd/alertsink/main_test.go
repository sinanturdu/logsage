package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
)

var t0 = time.Unix(1762600000, 0).UTC()

func post(t *testing.T, h http.Handler, key, sig, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-LogSage-Event", "alert.firing")
	req.Header.Set("X-LogSage-Delivery", key)
	req.Header.Set("X-LogSage-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func TestSinkVerifiesAndCountsDuplicates(t *testing.T) {
	sink := NewSink("lsw_s", 1, func() time.Time { return t0 })
	body := `{"version":1}`
	good := secret.Sign("lsw_s", t0, []byte(body))
	if code := post(t, sink, "k1", good, body); code != 500 { // the first request fails on purpose
		t.Fatalf("fail-first: %d", code)
	}
	if code := post(t, sink, "k1", good, body); code != 204 {
		t.Fatalf("retry: %d", code)
	}
	if code := post(t, sink, "k2", secret.Sign("other", t0, []byte(body)), body); code != 401 {
		t.Fatalf("bad signature: %d", code)
	}
	if code := post(t, sink, "k3", secret.Sign("lsw_s", t0.Add(-10*time.Minute), []byte(body)), body); code != 401 {
		t.Fatalf("old timestamp: %d", code)
	}
	w := httptest.NewRecorder()
	sink.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/deliveries", nil))
	var got []Delivery
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 4 || !got[0].Verified || got[0].Duplicate || !got[1].Duplicate || got[2].Verified || !strings.Contains(got[3].Error, "timestamp") {
		t.Fatalf("%+v", got)
	}
}

func TestSinkListensOnLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.5:9099", "example.com:9099"} {
		if err := run(context.Background(), addr, "", 0, nil); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("%s: %v", addr, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, "127.0.0.1:0", "", 0, ready) }()
	<-ready
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
