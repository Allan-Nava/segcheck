package analyze

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// A CDN that is not caching a live edge is an origin serving every viewer
// directly, and it is invisible from the media: every segment arrives, parses and
// lines up perfectly. The only evidence is in the response headers, and every CDN
// spells it differently.

type cacheSeg struct {
	// headers are set verbatim on the response, so a test can plant one vendor's
	// vocabulary at a time.
	headers map[string]string
}

func newCacheOrigin(t *testing.T, live bool, segs []cacheSeg) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := range segs {
			fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d.ts\n", segSeconds, i)
		}
		if !live {
			b.WriteString("#EXT-X-ENDLIST\n")
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})
	for i, s := range segs {
		i, s := i, s
		mux.HandleFunc(fmt.Sprintf("/seg%d.ts", i), func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range s.headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(mediatest.TSWithSPS(int64(i)*segTicks, frameDur, segFrames, mediatest.SPSFor(1280, 720)))
		})
	}
	return srv.URL + "/index.m3u8"
}

func runCache(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.From = FromStart
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// Every segment a miss: the CDN is not caching this stream at all, and the origin
// is serving every viewer directly.
func TestRun_FindsAnEdgeThatIsAlwaysAMiss(t *testing.T) {
	miss := cacheSeg{headers: map[string]string{"X-Cache": "MISS from edge-1", "Age": "0"}}
	url := newCacheOrigin(t, true, []cacheSeg{miss, miss, miss, miss})

	res := runCache(t, url)

	f, ok := findFinding(res, "cache", finding.BAD)
	if !ok {
		t.Fatalf("a live edge served entirely from the origin was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "4") {
		t.Errorf("the cache finding does not count the misses: %q", f.Message)
	}
}

// The vocabularies differ by vendor, and a checker that knew only one would call
// a perfectly warm Cloudflare or Akamai edge a miss.
func TestRun_ReadsEveryCDNsCacheVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header map[string]string
	}{
		{"Cloudflare", map[string]string{"CF-Cache-Status": "HIT", "Age": "120"}},
		{"Akamai", map[string]string{"X-Cache": "TCP_HIT from a1.akamai.net", "Age": "60"}},
		{"Varnish and friends", map[string]string{"X-Cache": "HIT", "X-Cache-Hits": "9"}},
		{"Fastly", map[string]string{"X-Cache": "HIT, HIT", "Age": "30"}},
		{"an Age alone, which is a hit by arithmetic", map[string]string{"Age": "45"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seg := cacheSeg{headers: tc.header}
			url := newCacheOrigin(t, true, []cacheSeg{seg, seg, seg, seg})

			res := runCache(t, url)
			for _, f := range res.Findings {
				if f.Check == "cache" && f.Status != finding.OK {
					t.Errorf("a warm %s edge produced %s: %s", tc.name, f.Status, f.Message)
				}
			}
			if !hasCheck(res, "cache") {
				t.Errorf("no cache finding at all:\n%s", dump(res))
			}
		})
	}
}

// A segment the origin tells caches not to store guarantees every viewer reaches
// the origin, whatever the CDN in front of it does.
func TestRun_FindsSegmentsMarkedUncacheable(t *testing.T) {
	nostore := cacheSeg{headers: map[string]string{
		"Cache-Control": "no-store, must-revalidate",
		"X-Cache":       "MISS",
	}}
	url := newCacheOrigin(t, true, []cacheSeg{nostore, nostore, nostore, nostore})

	res := runCache(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "cache" && strings.Contains(f.Message, "not to store") {
			said = true
			if f.Status != finding.BAD {
				t.Errorf("no-store segments were reported %s, want BAD", f.Status)
			}
		}
	}
	if !said {
		t.Errorf("segments marked uncacheable were not reported:\n%s", dump(res))
	}
}

// A mix is what a healthy stream looks like: the newest segment is often a miss
// because nobody has asked for it yet, and the ones behind it are warm.
func TestRun_AMixOfHitsAndMissesIsHealthy(t *testing.T) {
	hit := cacheSeg{headers: map[string]string{"X-Cache": "HIT", "Age": "40"}}
	miss := cacheSeg{headers: map[string]string{"X-Cache": "MISS", "Age": "0"}}
	url := newCacheOrigin(t, true, []cacheSeg{hit, hit, hit, miss})

	res := runCache(t, url)

	for _, f := range res.Findings {
		if f.Check == "cache" && f.Status != finding.OK {
			t.Errorf("a warm edge with one cold segment produced %s: %s", f.Status, f.Message)
		}
	}
	f, ok := findFinding(res, "cache", finding.OK)
	if !ok {
		t.Fatalf("no cache finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "3") {
		t.Errorf("the cache finding does not count the hits: %q", f.Message)
	}
}

// An origin that states no cache status at all cannot be judged, and saying so is
// the honest answer: segcheck could not look.
func TestRun_NoCacheHeadersSaysSo(t *testing.T) {
	bare := cacheSeg{}
	url := newCacheOrigin(t, true, []cacheSeg{bare, bare})

	res := runCache(t, url)

	f, ok := findFinding(res, "cache", finding.OK)
	if !ok {
		t.Fatalf("no cache finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "no cache status") {
		t.Errorf("an origin stating nothing did not say so: %q", f.Message)
	}
}

// A VOD stream served entirely from the origin is worth noting and is not the
// same incident: nobody is watching it yet, or it is genuinely cold.
func TestRun_AllMissOnVODIsNotAnIncident(t *testing.T) {
	miss := cacheSeg{headers: map[string]string{"X-Cache": "MISS", "Age": "0"}}
	url := newCacheOrigin(t, false, []cacheSeg{miss, miss, miss, miss})

	res := runCache(t, url)

	for _, f := range res.Findings {
		if f.Check == "cache" && f.Status == finding.BAD {
			t.Errorf("an all-miss VOD stream was reported BAD: %s", f.Message)
		}
	}
}

// The header readers, called directly: a nil header set, the hit-count form, and
// the multi-tier values that a single-vocabulary reader gets wrong.
func TestCacheHeaderReaders(t *testing.T) {
	if got := cacheStatusOf(nil); got != cacheUnknown {
		t.Errorf("a nil header set was classified as %v", got)
	}
	if _, ok := cacheAge(nil); ok {
		t.Error("a nil header set reported an age")
	}
	if got := uncacheableReason(nil); got != "" {
		t.Errorf("a nil header set reported %q", got)
	}

	// Built with Set rather than as a map literal: Get canonicalises its key, so
	// a literal "CF-Cache-Status" is a header no lookup ever finds — which a real
	// response never is, because parsing canonicalises on the way in.
	header := func(pairs ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(pairs); i += 2 {
			h.Set(pairs[i], pairs[i+1])
		}
		return h
	}
	for _, tc := range []struct {
		name string
		h    http.Header
		want cacheStatus
	}{
		{"a hit count above zero", header("X-Cache-Hits", "4"), cacheHit},
		{"a hit count of zero", header("X-Cache-Hits", "0"), cacheMiss},
		{"an age alone", header("Age", "90"), cacheHit},
		{"an age of zero alone states nothing", header("Age", "0"), cacheUnknown},
		{"an age that is not a number", header("Age", "soon"), cacheUnknown},
		{"nothing at all", http.Header{}, cacheUnknown},
		{"a miss at the first tier and a hit at the second", header("X-Cache", "MISS, HIT"), cacheHit},
		{"expired counts as a miss", header("CF-Cache-Status", "EXPIRED"), cacheMiss},
		{"dynamic counts as a miss", header("CF-Cache-Status", "DYNAMIC"), cacheMiss},
		{"an Akamai status", header("Akamai-Cache-Status", "Hit from child"), cacheHit},
		{"a vendor word nothing recognises", header("X-Cache", "WARM"), cacheUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheStatusOf(tc.h); got != tc.want {
				t.Errorf("cacheStatusOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// A sample where some segments state a status and some do not: the count has to
// add up, and the ages have to span what was actually seen.
func TestRun_MixedVocabularyAndAges(t *testing.T) {
	url := newCacheOrigin(t, true, []cacheSeg{
		{headers: map[string]string{"X-Cache": "HIT", "Age": "10"}},
		{headers: map[string]string{"X-Cache": "HIT", "Age": "200"}},
		{}, // this one states nothing
	})

	res := runCache(t, url)

	f, ok := findFinding(res, "cache", finding.OK)
	if !ok {
		t.Fatalf("no cache finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "10–200s") {
		t.Errorf("the finding does not span the ages seen: %q", f.Message)
	}
	if !strings.Contains(f.Message, "1 stated no status") {
		t.Errorf("the finding does not account for the silent segment: %q", f.Message)
	}
}
