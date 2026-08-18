package analyze

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// --watch is the one check that needs two looks at the same stream: a single
// shot cannot tell a live edge that is advancing from one that froze a minute
// ago, because both serve the same bytes. These tests drive a synthetic
// packager from a fake clock, so the loop can poll a dozen times without the
// test taking a dozen TARGETDURATIONs.

// liveOrigin is a packager: a sliding-window media playlist and the segments in
// it. publish appends one segment, which is precisely the event the watch loop
// exists to see happen — or to see not happen.
type liveOrigin struct {
	mu        sync.Mutex
	published int  // segments published so far
	window    int  // how many of them stay in the playlist
	endlist   bool // written as VOD instead
}

func newLiveOrigin(t *testing.T, published, window int) (*liveOrigin, string) {
	t.Helper()
	o := &liveOrigin{published: published, window: window}
	srv := httptest.NewServer(o.handler())
	t.Cleanup(srv.Close)
	return o, srv.URL + "/live.m3u8"
}

// publish appends one segment to the live edge.
func (o *liveOrigin) publish() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.published++
}

func (o *liveOrigin) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		o.mu.Lock()
		published, window, endlist := o.published, o.window, o.endlist
		o.mu.Unlock()

		first := 0
		if published > window {
			first = published - window
		}
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n")
		fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", first)
		for i := first; i < published; i++ {
			fmt.Fprintf(&b, "#EXTINF:2.000,\nseg%d.ts\n", i)
		}
		if endlist {
			b.WriteString("#EXT-X-ENDLIST\n")
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})
	// Every segment carries the timestamps its number implies, so the media
	// checks stay quiet and only the watch findings are under test.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := segNumberIn(r.URL.Path)
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(mediatest.TSWithSPS(int64(n)*segTicks, frameDur, segFrames, mediatest.SPSFor(1280, 720)))
	})
	return mux
}

// segNumberIn reads the N out of "/segN.ts".
func segNumberIn(path string) int {
	s := strings.TrimSuffix(strings.TrimPrefix(path, "/seg"), ".ts")
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// watchOn runs a watch with the clock under the test's control: every wait the
// loop asks for advances the clock by exactly that much and hands the test the
// chance to publish, so a twenty-second window costs no wall-clock time at all.
func watchOn(t *testing.T, url string, window time.Duration, onWait func(poll int)) finding.Result {
	t.Helper()
	var (
		mu    sync.Mutex
		clock = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
		polls int
	)
	opts := Defaults()
	opts.Segments = 3
	opts.Concurrency = 4
	opts.Watch = window
	opts.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	opts.Sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		clock = clock.Add(d)
		polls++
		n := polls
		mu.Unlock()
		if onWait != nil {
			onWait(n)
		}
		return nil
	}
	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	return Run(context.Background(), client, url, opts)
}

// A packager that has stopped publishing serves a perfectly valid playlist whose
// segments all parse. Nothing but a second look tells it apart from a healthy
// stream, and it is the single most common live incident there is.
func TestWatch_ReportsALiveEdgeThatStopsAdvancing(t *testing.T) {
	_, url := newLiveOrigin(t, 5, 5)

	res := watchOn(t, url, 20*time.Second, nil)

	f, ok := findFinding(res, "watch", finding.BAD)
	if !ok {
		t.Fatalf("a live edge that never advanced was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "did not advance") {
		t.Errorf("watch finding does not say the edge stood still: %q", f.Message)
	}
	if f.Value == nil || *f.Value < 20 {
		t.Errorf("watch finding carries no window measurement: value=%v unit=%q", f.Value, f.Unit)
	}
}

// A packager publishing one segment per TARGETDURATION is healthy, and a checker
// that cries wolf on it is worse than no checker.
func TestWatch_HealthyEdgeIsNotADefect(t *testing.T) {
	o, url := newLiveOrigin(t, 5, 5)

	res := watchOn(t, url, 20*time.Second, func(int) { o.publish() })

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("a healthy live edge produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "watch") {
		t.Errorf("no watch finding at all: the check did not run:\n%s", dump(res))
	}
}

// The edge advances, freezes for four TARGETDURATIONs, and advances again. The
// stream is fine before and after; only the gap in the middle is the incident,
// and a check that only compared the first and last look would miss it entirely.
func TestWatch_ReportsAStallInTheMiddleOfTheWindow(t *testing.T) {
	o, url := newLiveOrigin(t, 5, 5)

	res := watchOn(t, url, 40*time.Second, func(poll int) {
		if poll >= 4 && poll <= 8 {
			return // the packager is wedged
		}
		o.publish()
	})

	f, ok := findFinding(res, "watch", finding.BAD)
	if !ok {
		t.Fatalf("a stalled live edge was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "stalled") {
		t.Errorf("watch finding does not name the stall: %q", f.Message)
	}
	// Five waits of two seconds went by with nothing new.
	if f.Value == nil || *f.Value < 8 {
		t.Errorf("watch finding measures the stall as %v, want at least 8s", f.Value)
	}
}

// A VOD playlist has no live edge. Watching one is not a defect in the stream
// and must not be reported as one — segcheck says what it did instead.
func TestWatch_VODPlaylistHasNoEdgeToWatch(t *testing.T) {
	o, url := newLiveOrigin(t, 5, 5)
	o.endlist = true

	res := watchOn(t, url, 20*time.Second, nil)

	f, ok := findFinding(res, "watch", finding.OK)
	if !ok {
		t.Fatalf("watching a VOD playlist said nothing at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "no live edge") {
		t.Errorf("watch finding on VOD does not explain itself: %q", f.Message)
	}
	for _, f := range res.Findings {
		if f.Check == "watch" && f.Status != finding.OK {
			t.Errorf("watching VOD produced %s: %s", f.Status, f.Message)
		}
	}
}

// Without --watch nothing changes: the loop costs a request per poll and must
// never run unasked.
func TestWatch_OffByDefault(t *testing.T) {
	_, url := newLiveOrigin(t, 5, 5)

	res := runOn(t, url)

	if hasCheck(res, "watch") {
		t.Errorf("the watch loop ran without --watch:\n%s", dump(res))
	}
}
