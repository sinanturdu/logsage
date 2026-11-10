package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeResolver answers from a script: call i gets answers[min(i, last)].
type fakeResolver struct {
	mu      sync.Mutex
	answers [][]string
	calls   int
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(f.calls, len(f.answers)-1)
	f.calls++
	if i < 0 || f.answers[i] == nil {
		return nil, errors.New("no such host " + host)
	}
	out := make([]netip.Addr, 0, len(f.answers[i]))
	for _, s := range f.answers[i] {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

func (f *fakeResolver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func resolves(addrs ...string) *fakeResolver { return &fakeResolver{answers: [][]string{addrs}} }

func TestBlocked(t *testing.T) {
	for _, c := range []struct {
		addr    string
		blocked bool
	}{
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"2606:4700:4700::1111", false},
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"10.0.0.1", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"169.254.169.254", true}, // cloud metadata
		{"169.254.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.0.0.8", true},
		{"192.0.2.1", true},
		{"192.88.99.1", true},
		{"192.168.1.1", true},
		{"198.18.0.1", true},
		{"198.51.100.7", true},
		{"203.0.113.9", true},
		{"224.0.0.1", true},
		{"239.255.255.250", true},
		{"240.0.0.1", true},
		{"255.255.255.255", true},
		{"::", true},
		{"::1", true},
		{"::127.0.0.1", true},      // IPv4-compatible
		{"::ffff:127.0.0.1", true}, // IPv4-mapped loopback
		{"::ffff:169.254.169.254", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:8.8.8.8", false},    // mapped public address: checked as 8.8.8.8
		{"64:ff9b::7f00:1", true},    // NAT64 of 127.0.0.1
		{"64:ff9b::a9fe:a9fe", true}, // NAT64 of 169.254.169.254
		{"64:ff9b::808:808", false},  // NAT64 of 8.8.8.8
		{"64:ff9b:1::1", true},       // NAT64 local use
		{"2002:7f00:1::1", true},     // 6to4 embedding 127.0.0.1
		{"2002:c0a8:101::1", true},   // 6to4 embedding 192.168.1.1
		{"2002:808:808::1", false},   // 6to4 embedding 8.8.8.8
		{"2001::1", true},            // Teredo
		{"2001:db8::1", true},        // documentation
		{"3fff::1", true},
		{"2001:10::1", true},
		{"100::1", true},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"fe80::1", true},
		{"fec0::1", true},
		{"ff02::1", true},
	} {
		got, why := Blocked(netip.MustParseAddr(c.addr))
		if got != c.blocked {
			t.Errorf("Blocked(%s) = %v (%s), want %v", c.addr, got, why, c.blocked)
		}
	}
	if bad, _ := Blocked(netip.MustParseAddr("fe80::1%eth0")); !bad {
		t.Error("a zoned address must be blocked")
	}
	if bad, _ := Blocked(netip.Addr{}); !bad {
		t.Error("the zero address must be blocked")
	}
}

func TestCheckURL(t *testing.T) {
	c := New(Options{AllowedPorts: []int{8443}})
	ok := []string{
		"https://hooks.example.com/services/T0/B0/xyz",
		"https://hooks.example.com:443/x",
		"https://hooks.example.com:8443/x",
		"https://93.184.216.34/x",
		"https://[2606:4700:4700::1111]/x",
		"https://cafe.be/x", // hex-looking labels are still names
	}
	for _, u := range ok {
		if _, err := c.CheckURL(u); err != nil {
			t.Errorf("CheckURL(%q) = %v, want ok", u, err)
		}
	}
	bad := []struct {
		url  string
		want error
	}{
		{"http://hooks.example.com/x", ErrScheme},
		{"ftp://hooks.example.com/x", ErrScheme},
		{"file:///etc/passwd", ErrURL},
		{"gopher://x", ErrScheme},
		{"https:hooks.example.com", ErrURL},
		{"https:///x", ErrURL},
		{"https://user:pass@hooks.example.com/x", ErrUser},
		{"https://user@hooks.example.com/x", ErrUser},
		{"https://hooks.example.com/x#frag", ErrFrag},
		{"https://hooks.example.com:22/x", ErrPort},
		{"https://hooks.example.com:0/x", ErrPort},
		{"https://hooks.example.com:0443/x", ErrPort},
		{"https://hooks.example.com:99999/x", ErrPort},
		{"https://2130706433/x", ErrHost}, // decimal 127.0.0.1
		{"https://0177.0.0.1/x", ErrHost}, // octal
		{"https://0x7f.0.0.1/x", ErrHost}, // hex
		{"https://0x7f000001/x", ErrHost}, // hex, one part
		{"https://127.1/x", ErrHost},      // short form
		{"https://127.0.1/x", ErrHost},
		{"https://1.2.3.04/x", ErrHost},         // leading zero
		{"https://[fe80::1%25eth0]/x", ErrHost}, // zone
		{"https://[::ffff:7f00:1]/x", ErrHost},  // non-canonical mapped spelling
		{"https://hooks.example.com/x y", ErrURL},
		{"https://hooks.example.com\\@evil/x", ErrURL},
		{"https://" + strings.Repeat("a", 2100) + ".com/", ErrURL},
		{"https://" + strings.Repeat("a", 64) + ".com/", ErrHost},
		{"https://a..b/x", ErrHost},
	}
	for _, b := range bad {
		if _, err := c.CheckURL(b.url); !errors.Is(err, b.want) {
			t.Errorf("CheckURL(%q) = %v, want %v", b.url, err, b.want)
		}
	}
	// Plain http only to loopback names, only when allowed.
	local := New(Options{AllowInsecureLocal: true})
	for _, u := range []string{"http://localhost:9000/hook", "http://127.0.0.1:9000/hook", "http://[::1]:9000/hook"} {
		if _, err := local.CheckURL(u); err != nil {
			t.Errorf("insecure local %q = %v", u, err)
		}
		if _, err := c.CheckURL(u); !errors.Is(err, ErrScheme) {
			t.Errorf("without the flag %q = %v", u, err)
		}
	}
	if _, err := local.CheckURL("http://example.com/x"); !errors.Is(err, ErrScheme) {
		t.Errorf("insecure local must stay local: %v", err)
	}
	if _, err := local.CheckURL("http://169.254.169.254/latest/meta-data/"); !errors.Is(err, ErrScheme) {
		t.Errorf("metadata over http: %v", err)
	}
}

func TestValidateChecksEveryAnswer(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		answers []string
		want    error
	}{
		{"public", []string{"93.184.216.34"}, nil},
		{"public v6", []string{"2606:4700:4700::1111"}, nil},
		{"loopback", []string{"127.0.0.1"}, ErrAddress},
		{"metadata", []string{"169.254.169.254"}, ErrAddress},
		{"private behind a public answer", []string{"93.184.216.34", "10.0.0.5"}, ErrAddress},
		{"mapped loopback", []string{"::ffff:127.0.0.1"}, ErrAddress},
		{"nat64 metadata", []string{"64:ff9b::a9fe:a9fe"}, ErrAddress},
		{"no answer", nil, ErrResolve},
	} {
		cl := New(Options{Resolver: &fakeResolver{answers: [][]string{c.answers}}})
		if err := cl.Validate(ctx, "https://hooks.example.com/x"); !errors.Is(err, c.want) {
			t.Errorf("%s: Validate = %v, want %v", c.name, err, c.want)
		}
	}
	// IP literals are checked without DNS.
	cl := New(Options{Resolver: &fakeResolver{}})
	for _, u := range []string{"https://169.254.169.254/", "https://127.0.0.1/", "https://[::1]/", "https://10.1.2.3/", "https://[fd00::1]/"} {
		if err := cl.Validate(ctx, u); !errors.Is(err, ErrAddress) {
			t.Errorf("Validate(%s) = %v", u, err)
		}
	}
	if err := cl.Validate(ctx, "https://localhost/"); !errors.Is(err, ErrAddress) {
		t.Errorf("localhost without the flag: %v", err)
	}
}

// The checked address is the one dialed, and DNS is asked once per
// request: a rebinding answer on a later lookup cannot reach the dial.
func TestDialsTheCheckedAddressOnce(t *testing.T) {
	res := &fakeResolver{answers: [][]string{{"93.184.216.34"}, {"127.0.0.1"}}}
	var dialed []string
	cl := New(Options{Resolver: res, dial: func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return nil, errors.New("test dial stops here")
	}})
	_, err := cl.Post(context.Background(), "https://hooks.example.com/x", nil, []byte("{}"))
	if err == nil {
		t.Fatal("expected the stub dial to fail")
	}
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" || res.count() != 1 {
		t.Fatalf("dialed %v after %d lookups", dialed, res.count())
	}
	// The next request sees the rebinding answer and is refused before dialing.
	_, err = cl.Post(context.Background(), "https://hooks.example.com/x", nil, []byte("{}"))
	if !errors.Is(err, ErrAddress) || len(dialed) != 1 {
		t.Fatalf("rebound request: err %v, dialed %v", err, dialed)
	}
}

func localClient(opts Options) *Client {
	opts.AllowInsecureLocal = true
	return New(opts)
}

func TestNoRedirectsNoCredentials(t *testing.T) {
	var internalHits int
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { internalHits++ }))
	defer internal.Close()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		http.Redirect(w, r, internal.URL+"/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	h := http.Header{"Authorization": {"Bearer x"}, "Cookie": {"a=b"}, "X-Logsage-Event": {"alert.test"}}
	res, err := localClient(Options{}).Post(context.Background(), srv.URL+"/hook", h, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusFound || internalHits != 0 {
		t.Fatalf("status %d, internal hits %d: a redirect must not be followed", res.Status, internalHits)
	}
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" || got.Get("X-Logsage-Event") != "alert.test" {
		t.Fatalf("headers sent: %v", got)
	}
}

func TestHugeResponseIsCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := []byte(strings.Repeat("x", 64<<10))
		for i := 0; i < 160; i++ { // 10 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	res, err := localClient(Options{}).Post(context.Background(), srv.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Read up to MaxResponse and discarded: only the status is kept.
	if res != (Response{Status: 200}) {
		t.Fatalf("response %+v", res)
	}
}

func TestSlowServerTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	cl := localClient(Options{HeaderTimeout: 50 * time.Millisecond, TotalTimeout: time.Minute})
	_, err := cl.Post(context.Background(), srv.URL+"/secret-token-path", nil, nil)
	var r *Redacted
	if !errors.As(err, &r) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token-path") {
		t.Fatalf("the error leaks the path: %v", err)
	}
}

func TestErrorsNeverContainPathOrQuery(t *testing.T) {
	cl := New(Options{Resolver: resolves("10.0.0.1")})
	_, err := cl.Post(context.Background(), "https://hooks.example.com/services/T0/B0/SECRET?token=abc", nil, nil)
	if !errors.Is(err, ErrAddress) {
		t.Fatalf("err = %v", err)
	}
	if s := err.Error(); strings.Contains(s, "SECRET") || strings.Contains(s, "token") || !strings.Contains(s, "https://hooks.example.com") {
		t.Fatalf("error text %q", s)
	}
}

// TLS verification uses the URL's host name, not the dialed address.
func TestTLSVerifiesTheOriginalHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	loop := netip.MustParseAddr("127.0.0.1")
	opts := Options{
		Resolver:     resolves("127.0.0.1"),
		AllowedPorts: []int{atoi(t, port)},
		TLS:          &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		allowAddr:    func(a netip.Addr) bool { return a == loop }, // the test server is on loopback
	}
	cl := New(opts)
	// httptest's certificate is valid for example.com.
	res, err := cl.Post(context.Background(), "https://example.com:"+port+"/hook", nil, []byte("{}"))
	if err != nil || res.Status != 204 {
		t.Fatalf("example.com: %v %v", res.Status, err)
	}
	_, err = cl.Post(context.Background(), "https://hooks.example.net:"+port+"/hook", nil, []byte("{}"))
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a name the certificate does not cover must fail: %v", err)
	}
	// Without the test hook the loopback answer is refused.
	opts.allowAddr = nil
	if _, err := New(opts).Post(context.Background(), "https://example.com:"+port+"/hook", nil, nil); !errors.Is(err, ErrAddress) {
		t.Fatalf("loopback answer: %v", err)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func TestZonesAreRejectedEverywhere(t *testing.T) {
	cl := New(Options{AllowInsecureLocal: true, Resolver: resolves("fe80::1%eth0")})
	for _, u := range []string{"https://[fe80::1%25eth0]/x", "http://[::1%25lo]:9000/x", "https://[fe80::1%eth0]/x"} {
		if _, err := cl.CheckURL(u); err == nil {
			t.Errorf("CheckURL(%s) accepted a zone", u)
		}
	}
	// A resolver answer with a zone is refused, even for loopback in dev mode.
	if err := cl.Validate(context.Background(), "https://hooks.example.com/x"); !errors.Is(err, ErrAddress) {
		t.Errorf("zoned answer: %v", err)
	}
	if cl.allowed(netip.MustParseAddr("::1%lo")) {
		t.Error("zoned loopback allowed")
	}
}

func TestTLSIsAlwaysVerifiedAndModern(t *testing.T) {
	weak := &tls.Config{MinVersion: tls.VersionTLS10, InsecureSkipVerify: true} //nolint:gosec // the client must override this
	cl := New(Options{TLS: weak})
	cfg := cl.transport.TLSClientConfig
	if cfg.MinVersion < tls.VersionTLS12 || cfg.InsecureSkipVerify {
		t.Fatalf("TLS config %v / skip %v", cfg.MinVersion, cfg.InsecureSkipVerify)
	}
	if !weak.InsecureSkipVerify {
		t.Fatal("the caller's config must not be changed")
	}
	// A server limited to TLS 1.1 is refused.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	old := New(Options{Resolver: resolves("127.0.0.1"), AllowedPorts: []int{atoi(t, port)}, TLS: &tls.Config{RootCAs: pool},
		allowAddr: func(a netip.Addr) bool { return a.IsLoopback() }})
	if _, err := old.Post(context.Background(), "https://example.com:"+port+"/", nil, nil); err == nil {
		t.Fatal("TLS 1.1 accepted")
	}
	// A self-signed certificate is refused even if the caller asked to skip verification.
	srv2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv2.Close()
	_, port2, _ := net.SplitHostPort(srv2.Listener.Addr().String())
	skip := New(Options{Resolver: resolves("127.0.0.1"), AllowedPorts: []int{atoi(t, port2)}, TLS: weak,
		allowAddr: func(a netip.Addr) bool { return a.IsLoopback() }})
	if _, err := skip.Post(context.Background(), "https://example.com:"+port2+"/", nil, nil); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("unverified certificate accepted: %v", err)
	}
}

// Every delivery opens its own connection, so a connection checked for one
// address is never reused after DNS changed.
func TestNoConnectionReuse(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			mu.Lock()
			conns++
			mu.Unlock()
		}
	}
	srv.Start()
	defer srv.Close()
	cl := localClient(Options{})
	if !cl.transport.DisableKeepAlives {
		t.Fatal("keep-alives must be off")
	}
	for i := 0; i < 3; i++ {
		if res, err := cl.Post(context.Background(), srv.URL, nil, nil); err != nil || res.Status != 204 {
			t.Fatalf("post %d: %v %v", i, res.Status, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != 3 {
		t.Fatalf("%d connections for 3 deliveries", conns)
	}
}
