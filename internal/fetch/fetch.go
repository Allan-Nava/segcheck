// Package fetch is the HTTP layer: manifests and media segments, with the
// timeouts, headers and byte caps that keep a check from turning into an
// accidental download of a whole live stream.
package fetch

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Version is overwritten at build time via -ldflags.
var Version = "dev"

// DefaultUserAgent identifies segcheck in origin and CDN logs. A recognisable
// agent is what lets an operator tell a check apart from real traffic when
// reading access logs afterwards.
func DefaultUserAgent() string {
	return "segcheck/" + Version + " (+https://github.com/Allan-Nava/segcheck)"
}

// Options configures a Client.
type Options struct {
	Timeout  time.Duration
	Headers  map[string]string
	Insecure bool
	// MaxBytes caps a single response body. 0 means DefaultMaxBytes.
	MaxBytes int64
	// UserAgent overrides DefaultUserAgent when non-empty.
	UserAgent string
	// Resolve connects to this address instead of resolving the URL's host, while
	// still sending the original Host header and TLS server name. It is how the
	// same URL is asked of several edges: an override that rewrote the Host would
	// ask each one for a different site, and one that rewrote the server name
	// would fail the handshake.
	//
	// A value with no port keeps the one the URL implies, so an operator can pass
	// a bare address for either scheme.
	Resolve string
}

// DefaultMaxBytes caps one response at 64 MiB: larger than any sane segment,
// small enough that a misconfigured URL pointing at a full-length MP4 fails
// fast instead of filling memory.
const DefaultMaxBytes = 64 << 20

// Client fetches manifests and segments.
type Client struct {
	hc        *http.Client
	headers   map[string]string
	maxBytes  int64
	userAgent string
	// opts is kept so a client can be cloned onto another address without the
	// caller having to carry the options alongside it — which is what a POP
	// comparison needs, and what it would otherwise have to duplicate.
	opts Options
}

// WithResolve returns a client identical to this one but connecting to addr
// instead of resolving each URL's host. Everything else — the headers, the
// timeout, the byte cap, the user agent — is carried over, because a comparison
// between edges is only meaningful if the request is otherwise the same.
func (c *Client) WithResolve(addr string) *Client {
	opts := c.opts
	opts.Resolve = addr
	return New(opts)
}

// Response is one completed fetch.
type Response struct {
	Body     []byte
	Status   int
	Header   http.Header
	Duration time.Duration
	// Truncated is true when the body hit MaxBytes and was cut short.
	Truncated bool
}

// New builds a Client. Redirects are followed (CDNs rely on them) but the
// number is bounded by net/http's default of 10.
func New(opts Options) *Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConnsPerHost: 16,
		ForceAttemptHTTP2:   true,
	}
	if opts.Resolve != "" {
		dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, redirectAddr(addr, opts.Resolve))
		}
	}
	if opts.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via --insecure
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = DefaultUserAgent()
	}
	return &Client{
		hc:        &http.Client{Timeout: timeout, Transport: tr},
		headers:   opts.Headers,
		maxBytes:  maxBytes,
		userAgent: ua,
		opts:      opts,
	}
}

// redirectAddr replaces the host of addr with the override, keeping addr's port
// unless the override states one of its own.
//
// The port has to survive: an override of "203.0.113.7" against an https URL must
// still reach 443, and an operator asked for an edge rather than for a port.
func redirectAddr(addr, override string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return override
	}
	if _, _, err := net.SplitHostPort(override); err == nil {
		return override // the override states its own port
	}
	return net.JoinHostPort(override, port)
}

// Get fetches a URL. rangeHeader, when non-empty, is sent as the Range header
// for HLS byte-range segments.
func (c *Client) Get(ctx context.Context, rawurl, rangeHeader string) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "*/*")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	started := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return Response{Duration: time.Since(started)}, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	elapsed := time.Since(started)
	if err != nil {
		return Response{Status: resp.StatusCode, Header: resp.Header, Duration: elapsed}, err
	}
	out := Response{Status: resp.StatusCode, Header: resp.Header, Duration: elapsed, Body: body}
	if int64(len(body)) > c.maxBytes {
		out.Body = body[:c.maxBytes]
		out.Truncated = true
	}

	// A range request that the origin honoured answers 206; one it ignored
	// answers 200 with the whole resource, which the caller must know about
	// because the bytes are then not the segment it asked for.
	switch {
	case resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusPartialContent:
		return out, nil
	default:
		return out, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// ContentType is the response's media type, without parameters.
func (r Response) ContentType() string {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// CacheStatus reports what a CDN said about this response, for the report. The
// header name differs per vendor, so the first one present wins.
func (r Response) CacheStatus() string {
	for _, h := range []string{"X-Cache", "CF-Cache-Status", "X-Cache-Status", "Age"} {
		if v := r.Header.Get(h); v != "" {
			if h == "Age" {
				return "Age: " + v
			}
			return v
		}
	}
	return ""
}
