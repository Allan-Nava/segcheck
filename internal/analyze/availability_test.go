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

// A dynamic MPD does not list what exists: it states availabilityStartTime and
// leaves the client to work out, by arithmetic against "now", which segment is
// the newest. Which "now" is the entire question. A machine thirty seconds fast
// asks for segments the packager has not made yet and gets 404s that read as a
// CDN fault — which is why the MPD names its own time source, and why a checker
// that ignores it is measuring its own clock rather than the stream.

const (
	availTimescale = uint32(90000)
	availSegTicks  = int64(180000) // 2s
	availSegDur    = 2
	availSamples   = 50
	availSampleDur = uint32(3600)
	availPayload   = 12000
	availBandwidth = 60_000
)

// availOrigin is a packager whose published segments and whose clock can each
// be set apart from the checker's, because the incident this check exists for
// is exactly the two disagreeing.
type availOrigin struct {
	mu sync.Mutex
	// availableUpTo is the highest segment number the origin actually has, and
	// availableFrom the lowest: a DVR window that promises more than the origin
	// kept is the defect SC-53 exists for.
	availableUpTo int
	availableFrom int
	// utcTiming, when non-empty, is written into the MPD; serverNow is what that
	// source answers.
	utcTiming string
	serverNow time.Time
}

// availEpoch is availabilityStartTime, and availClock the checker's own clock:
// 100 seconds later, so the MPD's arithmetic makes 50 two-second segments.
var (
	availEpoch = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	availClock = time.Date(2026, 8, 10, 12, 1, 40, 0, time.UTC)
)

func newAvailOrigin(t *testing.T, o *availOrigin) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		o.mu.Lock()
		utc := o.utcTiming
		o.mu.Unlock()
		timing := ""
		if utc != "" {
			timing = fmt.Sprintf("  <UTCTiming schemeIdUri=%q value=%q/>\n", utc, srv.URL+"/time")
		}
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic" availabilityStartTime="%s"
     minimumUpdatePeriod="PT2S" timeShiftBufferDepth="PT1M">
%s  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="%d" duration="%d" media="seg-$Number$.m4s" initialization="init.mp4" startNumber="1"/>
      <Representation id="v0" bandwidth="%d" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`, availEpoch.Format(time.RFC3339), timing, availTimescale, availSegTicks, availBandwidth)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})

	mux.HandleFunc("/time", func(w http.ResponseWriter, _ *http.Request) {
		o.mu.Lock()
		now := o.serverNow
		o.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(now.UTC().Format(time.RFC3339Nano)))
	})

	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Init(1, availTimescale, "video", 1280, 720))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := segNumberIn(strings.Replace(strings.Replace(r.URL.Path, "/seg-", "/seg", 1), ".m4s", ".ts", 1))
		o.mu.Lock()
		upTo, from := o.availableUpTo, o.availableFrom
		o.mu.Unlock()
		if from < 1 {
			from = 1
		}
		if n < from || n > upTo {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not yet"))
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Segment(1, uint32(n), int64(n-1)*availSegTicks,
			availSampleDur, availSamples, availPayload))
	})
	return srv.URL + "/manifest.mpd"
}

func runAvail(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 3
	opts.Concurrency = 4
	opts.Now = func() time.Time { return availClock }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// The incident the check exists for, in the direction that breaks playback. The
// checker's clock is thirty seconds ahead of the packager's, so the MPD's own
// arithmetic — done against the wrong clock — points at segments that do not
// exist. Honouring the UTCTiming source the MPD names is the difference between
// checking the stream and checking this machine's clock.
func TestRun_HonoursTheTimeSourceTheMPDNames(t *testing.T) {
	o := &availOrigin{
		availableUpTo: 35, // what a packager 30s "behind" this machine has published
		utcTiming:     "urn:mpeg:dash:utc:http-iso:2014",
		serverNow:     availClock.Add(-30 * time.Second),
	}
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	// Against the local clock the run would ask for segments 48..50 and get
	// three 404s. Against the source the MPD names it asks for 33..35.
	for _, f := range res.Findings {
		if f.Check == "fetch" && f.Status != finding.OK {
			t.Errorf("the time source the MPD names was not honoured: %s", f.Message)
		}
	}
	f, ok := findFinding(res, "availability", finding.WARN)
	if !ok {
		t.Fatalf("a 30-second clock skew was not reported at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "30") {
		t.Errorf("the availability finding does not measure the skew: %q", f.Message)
	}
	// How long the run took is a fact about the real world, and adopting the
	// stream's clock must not reach it: a run against a packager thirty seconds
	// behind reported "-529ms" on a live reference stream before this was fixed.
	if res.Duration < 0 {
		t.Errorf("the run took %s: the availability clock correction leaked into the elapsed time", res.Duration)
	}
}

// The other direction: the packager is behind the clock the MPD is computed
// against, so the MPD promises segments the origin does not have. Every player
// hits the same 404s, and the report has to name the cause rather than leaving
// three unexplained fetch failures at the live edge.
func TestRun_ReportsSegmentsTheMPDPromisesThatDoNotExist(t *testing.T) {
	o := &availOrigin{availableUpTo: 45} // the MPD's arithmetic says 50
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	f, ok := findFinding(res, "availability", finding.BAD)
	if !ok {
		t.Fatalf("segments the MPD promises and the origin lacks were not diagnosed:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "availabilityStartTime") && !strings.Contains(f.Message, "clock") {
		t.Errorf("the availability finding does not name the cause: %q", f.Message)
	}
}

// The harmless direction, which is still worth saying: the packager is ahead of
// the MPD's arithmetic, so media exists that every spec-following player is
// waiting for. Nobody sees an error; everybody carries latency they need not.
func TestRun_ReportsMediaAvailableBeforeTheMPDSaysSo(t *testing.T) {
	o := &availOrigin{availableUpTo: 55} // the MPD's arithmetic says 50
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "availability" && strings.Contains(f.Message, "ahead") {
			said = true
			if f.Status != finding.WARN {
				t.Errorf("media arriving early was reported %s, want WARN: it costs latency, not playback", f.Status)
			}
		}
	}
	if !said {
		t.Errorf("a packager running ahead of its own availability window was not reported:\n%s", dump(res))
	}
}

// An MPD that names no time source leaves the checker with its own clock, which
// is the thing the element exists to distrust. That is a limit of the check, not
// a defect in the stream, and it has to be said out loud rather than passed off
// as a verdict.
func TestRun_NoUTCTimingSaysTheClockIsUnverified(t *testing.T) {
	o := &availOrigin{availableUpTo: 50} // exactly what the MPD's arithmetic says
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	f, ok := findFinding(res, "availability", finding.OK)
	if !ok {
		t.Fatalf("no availability finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "UTCTiming") {
		t.Errorf("the availability finding does not say the clock went unverified: %q", f.Message)
	}
	for _, f := range res.Findings {
		if f.Check == "availability" && f.Status != finding.OK {
			t.Errorf("a packager exactly in step produced %s: %s", f.Status, f.Message)
		}
	}
}

// HLS lists what exists rather than computing it, so none of this applies and
// none of it may appear in the report.
func TestRun_HLSHasNoAvailabilityFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "availability") {
		t.Errorf("an HLS playlist produced an availability finding:\n%s", dump(res))
	}
}

// timeShiftBufferDepth is a promise about the past, and the only person who
// ever collects it is a viewer scrubbing back — which is to say it fails in a
// complaint rather than in monitoring. The MPD here claims a minute of DVR and
// the origin has kept thirty seconds of it.
func TestRun_FindsADVRWindowTheOriginDoesNotHave(t *testing.T) {
	// 100s in, 2s segments: the MPD's 60s window reaches back to segment 21.
	o := &availOrigin{availableUpTo: 50, availableFrom: 31}
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	f, ok := findFinding(res, "dvr", finding.BAD)
	if !ok {
		t.Fatalf("a DVR window the origin cannot honour was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "60") {
		t.Errorf("the dvr finding does not quote the window it disproved: %q", f.Message)
	}
}

// Saying the window is short is only half an answer. The number an operator
// needs in order to change the origin's retention is how much of it is really
// there, and it is not in the manifest — the manifest is the thing that turned
// out to be wrong. Here the MPD claims a minute and the origin has kept the last
// forty seconds of it.
func TestRun_ReportsHowMuchOfTheDVRWindowTheOriginReallyHolds(t *testing.T) {
	// 100s in, 2s segments, startNumber 1: the edge is segment 50 and the MPD's
	// 60s window reaches back to segment 21. The origin holds 31 upwards, which
	// is segment 31's start at 60s against the edge at 100s: forty seconds.
	o := &availOrigin{availableUpTo: 50, availableFrom: 31}
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	f, ok := findFinding(res, "dvr", finding.BAD)
	if !ok {
		t.Fatalf("a DVR window the origin cannot honour was not reported:\n%s", dump(res))
	}
	if f.Value == nil {
		t.Fatalf("the dvr finding carries no measurement of what is really there: %q", f.Message)
	}
	// Four probes bound the answer to an eighth of the window, which is 7.5s
	// here, and the bound is one-sided: the bisection lands on a probe point and
	// the real boundary is somewhere before it, so the figure understates what
	// the origin holds and must never overstate it.
	if *f.Value < 32 || *f.Value > 40 {
		t.Errorf("the origin holds 40s and the finding says %.1f%s: %q", *f.Value, f.Unit, f.Message)
	}
	if !strings.Contains(f.Message, "60") {
		t.Errorf("the dvr finding does not quote the window it disproved: %q", f.Message)
	}
}

// An origin that has kept nothing at all behind the live edge is a different
// answer from one that has kept most of the window, and the difference is the
// whole point of measuring: there is no retention to adjust, there is retention
// that is not working.
func TestRun_ReportsADVRWindowWithNothingBehindTheEdge(t *testing.T) {
	o := &availOrigin{availableUpTo: 50, availableFrom: 50}
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	f, ok := findFinding(res, "dvr", finding.BAD)
	if !ok {
		t.Fatalf("an origin holding only the live edge was not reported:\n%s", dump(res))
	}
	if f.Value != nil && *f.Value > 12 {
		t.Errorf("an origin holding one segment was measured at %.1f%s: %q", *f.Value, f.Unit, f.Message)
	}
}

// A window the origin really holds is not a defect, and the check has to say so
// rather than stay silent: the whole value is knowing the promise was collected.
func TestRun_ADVRWindowTheOriginHonoursIsClean(t *testing.T) {
	o := &availOrigin{availableUpTo: 50, availableFrom: 1}
	url := newAvailOrigin(t, o)

	res := runAvail(t, url)

	f, ok := findFinding(res, "dvr", finding.OK)
	if !ok {
		t.Fatalf("no dvr finding at all: the promise went uncollected:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "fetch") && !strings.Contains(f.Message, "still") {
		t.Errorf("the dvr finding does not say what it verified: %q", f.Message)
	}
	for _, f := range res.Findings {
		if f.Check == "dvr" && f.Status != finding.OK {
			t.Errorf("an honoured DVR window produced %s: %s", f.Status, f.Message)
		}
	}
}

// A VOD manifest promises no window, and inventing one to check would be
// checking a number segcheck made up.
func TestRun_VODHasNoDVRFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "dvr") {
		t.Errorf("a VOD playlist produced a dvr finding:\n%s", dump(res))
	}
}
