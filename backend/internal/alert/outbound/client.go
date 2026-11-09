package outbound

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Resolver looks up a host's addresses (net.DefaultResolver in production,
// a fake in tests).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Options configures a Client. Zero durations and sizes take the defaults.
type Options struct {
	// AllowInsecureLocal permits plain http to localhost, 127.0.0.1 and ::1
	// on any port (development and tests only; off by default).
	AllowInsecureLocal bool
	// AllowedPorts are https ports besides 443.
	AllowedPorts   []int
	Resolver       Resolver
	ConnectTimeout time.Duration // 3 s
	TLSTimeout     time.Duration // 5 s
	HeaderTimeout  time.Duration // 5 s
	TotalTimeout   time.Duration // 10 s per attempt
	MaxResponse    int64         // bytes of the response body kept (4 KiB)
	// TLS is cloned for every connection; tests set RootCAs. The server name
	// always comes from the URL's host.
	TLS *tls.Config

	// For tests in this package: allow an address Blocked would refuse, and
	// replace the TCP dial.
	allowAddr func(netip.Addr) bool
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Client posts to user-configured URLs safely. Safe for concurrent use.
type Client struct {
	opts      Options
	http      *http.Client
	transport *http.Transport
}

// Errors returned by CheckURL and Validate (wrapped with details).
var (
	ErrURL     = errors.New("url: not a valid URL")
	ErrScheme  = errors.New("url: must use https")
	ErrUser    = errors.New("url: must not contain a user name or password")
	ErrFrag    = errors.New("url: must not contain a fragment")
	ErrPort    = errors.New("url: port is not allowed")
	ErrHost    = errors.New("url: host is not allowed")
	ErrAddress = errors.New("url: host resolves to a private or reserved address")
	ErrResolve = errors.New("url: host cannot be resolved")
)

const maxURL = 2048

// New returns a Client.
func New(opts Options) *Client {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.ConnectTimeout, 3*time.Second)
	def(&opts.TLSTimeout, 5*time.Second)
	def(&opts.HeaderTimeout, 5*time.Second)
	def(&opts.TotalTimeout, 10*time.Second)
	if opts.MaxResponse <= 0 {
		opts.MaxResponse = 4 << 10
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	c := &Client{opts: opts}
	tlsCfg := &tls.Config{}
	if opts.TLS != nil {
		tlsCfg = opts.TLS.Clone()
	}
	// Whatever a caller passes: TLS 1.2 or newer, and certificates are
	// always verified.
	tlsCfg.MinVersion = max(tlsCfg.MinVersion, tls.VersionTLS12)
	tlsCfg.InsecureSkipVerify = false
	tlsCfg.VerifyPeerCertificate = nil
	tlsCfg.VerifyConnection = nil
	tr := &http.Transport{
		Proxy:                  nil, // never an environment proxy
		DialContext:            c.dialContext,
		TLSClientConfig:        tlsCfg,
		TLSHandshakeTimeout:    opts.TLSTimeout,
		ResponseHeaderTimeout:  opts.HeaderTimeout,
		DisableKeepAlives:      true, // a connection never outlives its check
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 16 << 10,
	}
	c.transport = tr
	c.http = &http.Client{
		Transport: tr,
		Timeout:   opts.TotalTimeout,
		Jar:       nil,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // a 3xx is returned as the result, never followed
		},
	}
	return c
}

var localHosts = []string{"localhost", "127.0.0.1", "::1"}

// CheckURL applies the rules that need no DNS: scheme, userinfo, fragment,
// port, canonical IP literals and length.
func (c *Client) CheckURL(raw string) (*url.URL, error) {
	if len(raw) > maxURL || strings.ContainsAny(raw, " \t\r\n\\") {
		return nil, ErrURL
	}
	if strings.Contains(raw, "#") {
		return nil, ErrFrag
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return nil, ErrURL
	}
	if u.User != nil {
		return nil, ErrUser
	}
	host := strings.ToLower(u.Hostname())
	scheme := strings.ToLower(u.Scheme)
	local := slices.Contains(localHosts, host)
	switch {
	case scheme == "https":
	case scheme == "http" && c.opts.AllowInsecureLocal && local:
	default:
		return nil, ErrScheme
	}
	if err := checkHost(host); err != nil {
		return nil, err
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return nil, ErrPort
		}
		insecureLocal := scheme == "http" && c.opts.AllowInsecureLocal && local
		if n != 443 && !insecureLocal && !slices.Contains(c.opts.AllowedPorts, n) {
			return nil, fmt.Errorf("%w: %d", ErrPort, n)
		}
	}
	return u, nil
}

// checkHost refuses IP literals in any spelling but the canonical one
// (2130706433, 0177.0.0.1, 0x7f.1, 127.1) and zones. A name whose last
// label is numeric is treated as an IP literal: no top-level domain is.
func checkHost(host string) error {
	if host == "" || strings.Contains(host, "%") {
		return ErrHost
	}
	if strings.Contains(host, ":") { // IPv6 literal
		a, err := netip.ParseAddr(host)
		if err != nil || a.Zone() != "" || a.String() != host {
			return ErrHost
		}
		return nil
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	last := labels[len(labels)-1]
	if last == "" || numericLabel(last) {
		a, err := netip.ParseAddr(host)
		if err != nil || !a.Is4() || a.String() != host {
			return ErrHost
		}
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return ErrHost
		}
	}
	return nil
}

func numericLabel(s string) bool {
	s = strings.ToLower(s)
	if strings.HasPrefix(s, "0x") {
		s = s[2:]
		return s == "" || strings.Trim(s, "0123456789abcdef") == ""
	}
	return strings.Trim(s, "0123456789") == ""
}

// Validate checks the URL and resolves its host: every answer must be
// allowed. Used when a channel is saved, for a clear error.
func (c *Client) Validate(ctx context.Context, raw string) error {
	u, err := c.CheckURL(raw)
	if err != nil {
		return err
	}
	_, err = c.resolve(ctx, u.Hostname())
	return err
}

func (c *Client) allowed(a netip.Addr) bool {
	if a.Zone() != "" {
		return false // a zoned address (fe80::1%eth0) is never dialed
	}
	if c.opts.allowAddr != nil && c.opts.allowAddr(a) {
		return true
	}
	if c.opts.AllowInsecureLocal && a.Unmap().IsLoopback() {
		return true
	}
	bad, _ := Blocked(a)
	return !bad
}

// resolve returns the allowed addresses of host, or an error when any answer
// is blocked (a mixed answer is refused, not filtered).
func (c *Client) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.ToLower(host)
	if a, err := netip.ParseAddr(host); err == nil {
		if !c.allowed(a) {
			return nil, ErrAddress
		}
		return []netip.Addr{a}, nil
	}
	if host == "localhost" {
		if !c.opts.AllowInsecureLocal {
			return nil, ErrAddress
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	addrs, err := c.opts.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, ErrResolve
	}
	for _, a := range addrs {
		if !c.allowed(a) {
			return nil, ErrAddress
		}
	}
	return addrs, nil
}

// dialContext resolves once, checks every answer and dials the checked IP;
// TLS still verifies the certificate for the URL's host name.
func (c *Client) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, ErrURL
	}
	addrs, err := c.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	target := net.JoinHostPort(addrs[0].Unmap().String(), port)
	if c.opts.dial != nil {
		return c.opts.dial(ctx, network, target)
	}
	d := net.Dialer{Timeout: c.opts.ConnectTimeout}
	return d.DialContext(ctx, network, target)
}

// Response is what a delivery attempt learned.
type Response struct {
	Status int
	// Body is at most MaxResponse bytes of the response.
	Body []byte
}

// forbidden headers are never sent, whatever the caller passes.
var forbidden = []string{"Authorization", "Proxy-Authorization", "Cookie", "Host"}

// Post sends body to raw. The URL is checked again (DNS may have changed
// since the channel was saved). Errors never contain the URL's path, query
// or credentials, only the scheme and host.
func (c *Client) Post(ctx context.Context, raw string, header http.Header, body []byte) (Response, error) {
	u, err := c.CheckURL(raw)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return Response{}, ErrURL
	}
	for k, vs := range header {
		if slices.ContainsFunc(forbidden, func(f string) bool { return strings.EqualFold(f, k) }) {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		return Response{}, redact(err, u)
	}
	defer res.Body.Close()
	kept, _ := io.ReadAll(io.LimitReader(res.Body, c.opts.MaxResponse))
	return Response{Status: res.StatusCode, Body: kept}, nil
}

// Redacted is a delivery error that names only the scheme and host.
type Redacted struct {
	Target string // "https://hooks.example.com"
	Cause  error  // one of the Err values, or a sanitized transport error
	msg    string
}

func (e *Redacted) Error() string { return e.msg }
func (e *Redacted) Unwrap() error { return e.Cause }

func redact(err error, u *url.URL) error {
	target := u.Scheme + "://" + u.Host
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	for _, known := range []error{ErrURL, ErrScheme, ErrUser, ErrFrag, ErrPort, ErrHost, ErrAddress, ErrResolve} {
		if errors.Is(err, known) {
			return &Redacted{Target: target, Cause: known, msg: "post " + target + ": " + known.Error()}
		}
	}
	what := "request failed"
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		what = "timed out"
	case errors.Is(err, context.Canceled):
		what = "cancelled"
	default:
		var ce *tls.CertificateVerificationError
		if errors.As(err, &ce) {
			what = "TLS certificate not valid for the host"
		}
	}
	return &Redacted{Target: target, Cause: err, msg: "post " + target + ": " + what}
}
