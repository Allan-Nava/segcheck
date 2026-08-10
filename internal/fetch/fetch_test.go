package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The HTTP layer decides what the parsers are handed. A body cut at the cap and
// passed on as if it were whole is the worst failure available here: every check
// downstream then reports a truncated segment as a defective one, and the
// operator goes looking at a packager that is fine.

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestGet_SendsIdentifyingHeaders(t *testing.T) {
	var got http.Header
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("ok"))
	})

	c := New(Options{Headers: map[string]string{"Authorization": "Bearer s3cret"}})
	if _, err := c.Get(context.Background(), srv.URL, ""); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// A recognisable agent is what lets an operator tell a check apart from
	// real traffic when reading access logs afterwards.
	if ua := got.Get("User-Agent"); !strings.HasPrefix(ua, "segcheck/") {
		t.Errorf("User-Agent is %q, want it to start with segcheck/", ua)
	}
	if got.Get("Authorization") != "Bearer s3cret" {
		t.Errorf("custom header was not sent: %q", got.Get("Authorization"))
	}
	if _, ok := got["Range"]; ok {
		t.Errorf("Range was sent for a whole-resource request: %q", got.Get("Range"))
	}
}

func TestGet_SendsRangeOnlyWhenAsked(t *testing.T) {
	var got string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("partial"))
	})

	c := New(Options{})
	resp, err := c.Get(context.Background(), srv.URL, "bytes=0-99")
	if err != nil {
		t.Fatalf("a 206 is a success, not an error: %v", err)
	}
	if got != "bytes=0-99" {
		t.Errorf("Range header was %q", got)
	}
	if resp.Status != http.StatusPartialContent {
		t.Errorf("status %d, want 206", resp.Status)
	}
}

// The boundary is the whole point: a body exactly at the cap is complete, one
// byte more is not.
func TestGet_TruncationBoundary(t *testing.T) {
	const cap = 1024
	for _, tc := range []struct {
		name      string
		size      int
		wantLen   int
		truncated bool
	}{
		{"under the cap", cap - 1, cap - 1, false},
		{"exactly at the cap", cap, cap, false},
		{"one byte over", cap + 1, cap, true},
		{"far over", cap * 4, cap, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(make([]byte, tc.size))
			})
			c := New(Options{MaxBytes: cap})
			resp, err := c.Get(context.Background(), srv.URL, "")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if len(resp.Body) != tc.wantLen {
				t.Errorf("body is %d bytes, want %d", len(resp.Body), tc.wantLen)
			}
			if resp.Truncated != tc.truncated {
				t.Errorf("Truncated is %v, want %v — a body cut short must never be handed on as a whole one", resp.Truncated, tc.truncated)
			}
		})
	}
}

func TestNew_ZeroMaxBytesMeansTheDefault(t *testing.T) {
	if c := New(Options{}); c.maxBytes != DefaultMaxBytes {
		t.Errorf("maxBytes is %d, want the %d default", c.maxBytes, DefaultMaxBytes)
	}
	if c := New(Options{MaxBytes: -1}); c.maxBytes != DefaultMaxBytes {
		t.Errorf("a negative cap must fall back to the default, got %d", c.maxBytes)
	}
}

// An origin that answers 404 with an HTML error page is a delivery problem, and
// the caller needs both the status and the body to say so precisely.
func TestGet_ErrorStatusStillReturnsTheResponse(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html>nope</html>"))
	})

	c := New(Options{})
	resp, err := c.Get(context.Background(), srv.URL, "")
	if err == nil {
		t.Fatal("a 404 must be reported as an error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error does not name the status: %v", err)
	}
	if resp.Status != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "nope") {
		t.Errorf("body was dropped, so the caller cannot show what the origin actually served")
	}
	if ct := resp.ContentType(); ct != "text/html" {
		t.Errorf("ContentType is %q, want the media type without parameters", ct)
	}
}

func TestGet_TimesOut(t *testing.T) {
	srv := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	})

	c := New(Options{Timeout: 100 * time.Millisecond})
	started := time.Now()
	if _, err := c.Get(context.Background(), srv.URL, ""); err == nil {
		t.Fatal("a hung origin must produce an error, not a wait")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("took %s: the timeout was not applied", elapsed)
	}
}

func TestGet_HonoursContextCancellation(t *testing.T) {
	srv := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	c := New(Options{Timeout: 30 * time.Second})
	if _, err := c.Get(ctx, srv.URL, ""); err == nil {
		t.Fatal("Ctrl-C must cancel an in-flight download instead of waiting for the timeout")
	}
}

func TestResponse_CacheStatus(t *testing.T) {
	mk := func(kv ...string) Response {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return Response{Header: h}
	}
	cases := []struct {
		name string
		resp Response
		want string
	}{
		{"nothing to report", mk(), ""},
		{"X-Cache", mk("X-Cache", "HIT"), "HIT"},
		{"Cloudflare", mk("CF-Cache-Status", "MISS"), "MISS"},
		{"nginx", mk("X-Cache-Status", "EXPIRED"), "EXPIRED"},
		{"Age is labelled so a bare number cannot be mistaken for a verdict", mk("Age", "42"), "Age: 42"},
		{"the first vendor header wins over Age", mk("Age", "42", "X-Cache", "HIT"), "HIT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.resp.CacheStatus(); got != tc.want {
				t.Errorf("CacheStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultUserAgent_CarriesTheVersion(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "9.9.9"
	if ua := DefaultUserAgent(); !strings.Contains(ua, "9.9.9") {
		t.Errorf("User-Agent %q does not carry the build version", ua)
	}
}
