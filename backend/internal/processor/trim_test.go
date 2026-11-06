package processor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeTrimBoundary(t *testing.T) {
	tests := []struct {
		name   string
		groups []GroupState
		want   string // "" means: must not trim
		reason string // substring of the reason when not trimming
	}{
		{
			name:   "no groups: nobody declared what they need",
			groups: nil,
			reason: "no consumer groups",
		},
		{
			name:   "caught up: trim everything up to and including last delivered",
			groups: []GroupState{{Name: "p", Pending: 0, LastDelivered: "100-5"}},
			want:   "100-6",
		},
		{
			name:   "pending entries: keep from the lowest pending on",
			groups: []GroupState{{Name: "p", Pending: 3, LowestPending: "90-0", LastDelivered: "100-5"}},
			want:   "90-0",
		},
		{
			name:   "undelivered entries after last delivered are kept (boundary stops at last delivered)",
			groups: []GroupState{{Name: "p", Pending: 0, LastDelivered: "50-0"}},
			want:   "50-1",
		},
		{
			name: "two groups: the slower one wins",
			groups: []GroupState{
				{Name: "processor", Pending: 0, LastDelivered: "500-0"},
				{Name: "alerting", Pending: 2, LowestPending: "120-3", LastDelivered: "130-0"},
			},
			want: "120-3",
		},
		{
			name: "two groups: second caught up but behind",
			groups: []GroupState{
				{Name: "processor", Pending: 1, LowestPending: "400-0", LastDelivered: "500-0"},
				{Name: "alerting", Pending: 0, LastDelivered: "300-9"},
			},
			want: "300-10",
		},
		{
			name: "a group that has read nothing blocks trimming",
			groups: []GroupState{
				{Name: "processor", Pending: 0, LastDelivered: "500-0"},
				{Name: "alerting", Pending: 0, LastDelivered: "0-0"},
			},
			reason: `"alerting" has not read anything`,
		},
		{
			name:   "pending without a lowest ID is not understood",
			groups: []GroupState{{Name: "p", Pending: 4, LowestPending: "", LastDelivered: "100-0"}},
			reason: "lowest pending ID unknown",
		},
		{
			name:   "malformed last delivered",
			groups: []GroupState{{Name: "p", Pending: 0, LastDelivered: "garbage"}},
			reason: "invalid stream ID",
		},
		{
			name:   "malformed lowest pending",
			groups: []GroupState{{Name: "p", Pending: 1, LowestPending: "12-x", LastDelivered: "100-0"}},
			reason: "invalid stream ID",
		},
		{
			name:   "negative pending",
			groups: []GroupState{{Name: "p", Pending: -1, LastDelivered: "100-0"}},
			reason: "negative pending",
		},
		{
			name:   "seq overflow rolls to the next millisecond",
			groups: []GroupState{{Name: "p", Pending: 0, LastDelivered: "7-18446744073709551615"}},
			want:   "8-0",
		},
		{
			name:   "IDs compare numerically, not as strings",
			groups: []GroupState{{Name: "a", Pending: 1, LowestPending: "99-0", LastDelivered: "99-0"}, {Name: "b", Pending: 1, LowestPending: "100-0", LastDelivered: "100-0"}},
			want:   "99-0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, reason := SafeTrimBoundary(tt.groups)
			if tt.want == "" {
				if ok {
					t.Fatalf("boundary = %q, want no trimming", got)
				}
				if !strings.Contains(reason, tt.reason) {
					t.Errorf("reason = %q, want it to contain %q", reason, tt.reason)
				}
				return
			}
			if !ok || got != tt.want {
				t.Fatalf("boundary = %q, %v (%s), want %q", got, ok, reason, tt.want)
			}
		})
	}
}

// fakeTrimStore records XTRIM calls.
type fakeTrimStore struct {
	mu        sync.Mutex
	groups    []GroupState
	groupsErr error
	trimErr   error
	removed   int64
	trims     []string
}

func (f *fakeTrimStore) GroupStates(context.Context) ([]GroupState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groups, f.groupsErr
}

func (f *fakeTrimStore) TrimMinID(_ context.Context, minID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trims = append(f.trims, minID)
	return f.removed, f.trimErr
}

func (f *fakeTrimStore) trimCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.trims...)
}

func TestTrimOnce(t *testing.T) {
	tests := []struct {
		name      string
		store     *fakeTrimStore
		wantTrims []string
		wantErr   string
		wantTotal int64
	}{
		{
			name:      "trims below the safe boundary",
			store:     &fakeTrimStore{groups: []GroupState{{Name: "p", Pending: 2, LowestPending: "10-0", LastDelivered: "12-0"}}, removed: 42},
			wantTrims: []string{"10-0"},
			wantTotal: 42,
		},
		{
			name:    "group read error: no trim",
			store:   &fakeTrimStore{groupsErr: errors.New("connection refused")},
			wantErr: "read consumer groups",
		},
		{
			name:    "no groups: no trim",
			store:   &fakeTrimStore{},
			wantErr: "no safe trim boundary",
		},
		{
			name:    "unexpected state: no trim",
			store:   &fakeTrimStore{groups: []GroupState{{Name: "p", Pending: 1}}},
			wantErr: "no safe trim boundary",
		},
		{
			name:      "XTRIM error is reported and not counted",
			store:     &fakeTrimStore{groups: []GroupState{{Name: "p", LastDelivered: "5-0"}}, trimErr: errors.New("READONLY"), removed: 9},
			wantTrims: []string{"5-1"},
			wantErr:   "xtrim",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testOptions())
			_, err := h.p.TrimOnce(context.Background(), tt.store)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("TrimOnce: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("TrimOnce error = %v, want %q", err, tt.wantErr)
			}
			if got := tt.store.trimCalls(); strings.Join(got, ",") != strings.Join(tt.wantTrims, ",") {
				t.Errorf("XTRIM calls = %v, want %v", got, tt.wantTrims)
			}
			if got := h.p.Stats(context.Background()).Trimmed; got != tt.wantTotal {
				t.Errorf("trimmed_total = %d, want %d", got, tt.wantTotal)
			}
		})
	}
}

func TestRunTrimmerLogsAndSkipsOnError(t *testing.T) {
	var logs bytes.Buffer
	var mu sync.Mutex
	rec := &recorder{}
	p, err := New(&fakeStream{rec: rec}, &fakeStore{rec: rec}, testOptions(), slog.New(slog.NewTextHandler(syncWriter{&mu, &logs}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeTrimStore{groupsErr: errors.New("unexpected reply")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.RunTrimmer(ctx, store, 5*time.Millisecond); close(done) }()
	waitFor(t, "warning", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(logs.String(), "stream not trimmed")
	})
	cancel()
	<-done
	if len(store.trimCalls()) != 0 {
		t.Error("XTRIM called although the group state could not be read")
	}
}

type syncWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (s syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(b)
}

func TestLagWarning(t *testing.T) {
	tests := []struct {
		name     string
		age      time.Duration
		wantWarn bool
	}{
		{name: "caught up", age: 0},
		{name: "below threshold", age: 29 * time.Second},
		{name: "above threshold", age: 31 * time.Second, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			rec := &recorder{}
			opts := testOptions()
			opts.LagWarnAge = 30 * time.Second
			stream := &fakeStream{rec: rec, info: StreamInfo{Length: 5, Pending: 1, Lag: 4, OldestUnprocessedAge: tt.age}}
			p, err := New(stream, &fakeStore{rec: rec}, opts, slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			p.logStatsOnce(context.Background())
			if got := strings.Contains(logs.String(), "falling behind"); got != tt.wantWarn {
				t.Errorf("warned = %v, want %v; logs:\n%s", got, tt.wantWarn, logs.String())
			}
			if s := p.Stats(context.Background()); s.OldestUnprocessedAgeS == nil || *s.OldestUnprocessedAgeS != float64(int(tt.age.Seconds())) {
				t.Errorf("oldest_unprocessed_age_s = %v, want %v", s.OldestUnprocessedAgeS, tt.age.Seconds())
			}
		})
	}
}
