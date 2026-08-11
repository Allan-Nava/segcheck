package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The three failure modes below the happy path: a URL that cannot become a
// request, a body that stops arriving part way through, and the opt-in that turns
// certificate verification off.
//
// All three have to be reported as errors rather than as an empty body, because
// an empty body parses as an unknown container and would be reported as a defect
// in the stream instead of a failure to fetch it.

// A URL that http.NewRequestWithContext rejects never reaches the network, and
// the error has to come back rather than a zero Response the caller reads as a
// successful empty fetch.
func TestGet_UnusableURL(t *testing.T) {
	c := New(Options{})
	for _, rawurl := range []string{
		"http://exa\x7fmple.com/x", // a control character in the host
		"://missing-scheme",        // no scheme at all
		"http://[::1/unclosed",     // a malformed IPv6 literal
	} {
		resp, err := c.Get(context.Background(), rawurl, "")
		if err == nil {
			t.Errorf("Get(%q) succeeded with status %d", rawurl, resp.Status)
		}
		if len(resp.Body) != 0 {
			t.Errorf("Get(%q) returned a body alongside its error", rawurl)
		}
	}
}

// An origin that declares a Content-Length and then closes early is a real CDN
// failure mode. The status and headers did arrive, so they are reported, but the
// read error has to travel with them — a short body silently accepted would be
// parsed as a truncated segment and blamed on the packager.
func TestGet_BodyThatStopsEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "8192")
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("only a few bytes"))
		// Closing the underlying connection mid-body: the client sees an
		// unexpected EOF while reading.
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	c := New(Options{Timeout: 5 * time.Second})
	resp, err := c.Get(context.Background(), srv.URL+"/seg.ts", "")
	if err == nil {
		t.Fatalf("a body that stopped early was accepted as complete (%d bytes)", len(resp.Body))
	}
	// The response line did arrive, so what is known is still reported.
	if resp.Status != http.StatusOK {
		t.Errorf("status = %d, want 200 reported alongside the read error", resp.Status)
	}
	if resp.Duration <= 0 {
		t.Error("the elapsed time was not reported alongside the read error")
	}
}

// --insecure exists for origins behind a self-signed certificate, which is
// ordinary in a staging environment. Without it the fetch must fail; with it the
// same fetch must succeed — that is the whole contract of the flag.
func TestNew_InsecureSkipsCertificateVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer srv.Close()

	// The default client does not trust the test server's certificate.
	strict := New(Options{Timeout: 5 * time.Second})
	if _, err := strict.Get(context.Background(), srv.URL+"/master.m3u8", ""); err == nil {
		t.Fatal("an untrusted certificate was accepted without --insecure")
	} else if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Logf("the strict client failed for another reason: %v", err)
	}

	lax := New(Options{Timeout: 5 * time.Second, Insecure: true})
	resp, err := lax.Get(context.Background(), srv.URL+"/master.m3u8", "")
	if err != nil {
		t.Fatalf("--insecure did not accept the self-signed certificate: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	if !strings.HasPrefix(string(resp.Body), "#EXTM3U") {
		t.Errorf("body = %q", resp.Body)
	}
}

// The insecure transport really is the one configured, rather than the flag being
// accepted and quietly ignored.
func TestNew_InsecureIsSetOnTheTransport(t *testing.T) {
	c := New(Options{Insecure: true})
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.hc.Transport)
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("Insecure did not set InsecureSkipVerify on the transport")
	}

	plain := New(Options{})
	ptr := plain.hc.Transport.(*http.Transport)
	if ptr.TLSClientConfig != nil && ptr.TLSClientConfig.InsecureSkipVerify {
		t.Error("verification is skipped by default")
	}
}
