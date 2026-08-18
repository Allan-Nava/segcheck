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

// A period boundary is where an encoder change lands: a new Period is how an ad
// break, a programme junction and a re-encode are all expressed. It is also
// where every timeline in the presentation restarts, so a defect there looks
// like a clean stream from either side — each period checks out perfectly on its
// own, and the join is the thing nobody looked at.

const (
	perTimescale = uint32(90000)
	perSegTicks  = int64(180000) // 2s
	perSamples   = 50
	perSampleDur = uint32(3600)
)

type periodSpec struct {
	id       string
	start    float64
	duration float64
	width    int
	height   int
	codecs   string
	// pto is the presentationTimeOffset the MPD states, in seconds. A packager
	// that forgets it after a restart states zero here and puts every segment a
	// whole period-start away from where a seek expects it.
	pto float64
	// mediaStart is where the segments' own timeline really begins, in seconds.
	mediaStart float64
}

func newPeriodOrigin(t *testing.T, periods []periodSpec) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
		fmt.Fprintf(&b, `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%.0fS">`+"\n",
			periods[len(periods)-1].start+periods[len(periods)-1].duration)
		for _, p := range periods {
			// @duration rather than a SegmentTimeline, deliberately. A timeline
			// states each segment's media time outright, so a segment landing
			// somewhere else is a hole `continuity` already reports; with
			// @duration the manifest states only how far into the period each
			// index sits, and the segment's own timestamp is the only thing that
			// can say whether the period is where the MPD puts it.
			fmt.Fprintf(&b, `  <Period id=%q start="PT%.3fS" duration="PT%.3fS">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="%d" duration="%d" media="%s-$Number$.m4s" initialization="%s-init.mp4" startNumber="0" presentationTimeOffset="%d"/>
      <Representation id="v" bandwidth="%d" width="%d" height="%d" codecs=%q/>
    </AdaptationSet>
  </Period>`+"\n",
				p.id, p.start, p.duration,
				perTimescale, perSegTicks, p.id, p.id,
				int64(p.pto*float64(perTimescale)),
				drmBandwidth, p.width, p.height, p.codecs)
		}
		b.WriteString("</MPD>")
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(b.String()))
	})

	for _, p := range periods {
		p := p
		mux.HandleFunc("/"+p.id+"-init.mp4", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4Init(1, perTimescale, "video", p.width, p.height))
		})
		for i := 0; i < 2; i++ {
			i := i
			mux.HandleFunc(fmt.Sprintf("/%s-%d.m4s", p.id, i), func(w http.ResponseWriter, _ *http.Request) {
				tfdt := int64(p.mediaStart*float64(perTimescale)) + int64(i)*perSegTicks
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = w.Write(mediatest.MP4Segment(1, uint32(i), tfdt, perSampleDur, perSamples, 12000))
			})
		}
	}
	return srv.URL + "/manifest.mpd"
}

func runPeriod(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 2
	opts.Concurrency = 4
	opts.From = FromStart
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// Two periods that join cleanly: the second's media, mapped through its own
// presentation-time offset, begins exactly where the first ends.
func TestRun_PeriodsThatJoinCleanlyAreFine(t *testing.T) {
	url := newPeriodOrigin(t, []periodSpec{
		{id: "main", start: 0, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 0, mediaStart: 0},
		{id: "ad", start: 4, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 4, mediaStart: 4},
	})

	res := runPeriod(t, url)

	for _, f := range res.Findings {
		if f.Check == "period" && f.Status != finding.OK {
			t.Errorf("a clean period join produced %s: %s", f.Status, f.Message)
		}
	}
	if !hasCheck(res, "period") {
		t.Errorf("no period finding at all: the check did not run:\n%s", dump(res))
	}
	// Consecutive periods are not competing rungs of one ladder. Before the
	// ladder-wide comparisons were scoped to a period, two identical periods
	// reported as a duplicate rung and as a four-second misalignment at every
	// segment index — on a presentation that is exactly what a multi-period MPD
	// is supposed to look like.
	for _, f := range res.Findings {
		if (f.Check == "ladder" || f.Check == "alignment") && f.Status != finding.OK {
			t.Errorf("%s compared one period against the next: %s", f.Check, f.Message)
		}
	}
}

// A period boundary does not divide an encoder's segment grid, so a period's
// first segment may begin before the period does: the player trims the head.
// nomor's own DASH-IF vector does exactly this and reading it as drift reported
// a correctly built presentation as broken.
func TestRun_APeriodWhoseFirstSegmentStraddlesTheBoundaryIsFine(t *testing.T) {
	url := newPeriodOrigin(t, []periodSpec{
		{id: "main", start: 0, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 0, mediaStart: 0},
		// Half a segment early: the period starts at media 4s and its first
		// segment carries the 3.5s mark onwards.
		{id: "ad", start: 4, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 4, mediaStart: 3.5},
	})

	res := runPeriod(t, url)

	for _, f := range res.Findings {
		if f.Check == "period" && f.Status != finding.OK {
			t.Errorf("a first segment straddling the boundary produced %s: %s", f.Status, f.Message)
		}
	}
}

// More than a whole segment early is a different thing: the period is showing
// media that belongs to the one before it.
func TestRun_FindsAPeriodShowingTheMediaBeforeIt(t *testing.T) {
	url := newPeriodOrigin(t, []periodSpec{
		{id: "main", start: 0, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 0, mediaStart: 0},
		{id: "ad", start: 4, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 4, mediaStart: 1},
	})

	res := runPeriod(t, url)

	if _, ok := findFinding(res, "period", finding.BAD); !ok {
		t.Fatalf("a period three seconds of media early was not reported:\n%s", dump(res))
	}
}

// The classic multi-period defect: the media timeline runs straight across the
// boundary — the second period's segments carry on from four seconds — and the
// MPD leaves presentationTimeOffset at zero. Subtracting nothing puts every
// segment of that period a whole period-start away from where a seek into it
// expects to land. Playback from the beginning never notices.
func TestRun_FindsAPeriodWhosePresentationTimeOffsetIsMissing(t *testing.T) {
	url := newPeriodOrigin(t, []periodSpec{
		{id: "main", start: 0, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 0, mediaStart: 0},
		// The media continues from 4s and the MPD says nothing about it.
		{id: "ad", start: 4, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 0, mediaStart: 4},
	})

	res := runPeriod(t, url)

	f, ok := findFinding(res, "period", finding.BAD)
	if !ok {
		t.Fatalf("a period whose media does not land where it says was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "presentation") {
		t.Errorf("the period finding does not name the timeline: %q", f.Message)
	}
}

// The other multi-period defect: an encoder change at the join. Every period is
// internally perfect and a player that cannot switch resolution mid-presentation
// stops at the boundary.
func TestRun_FindsAnEncoderChangeAcrossAPeriodBoundary(t *testing.T) {
	url := newPeriodOrigin(t, []periodSpec{
		{id: "main", start: 0, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 0, mediaStart: 0},
		{id: "ad", start: 4, duration: 4, width: 640, height: 360, codecs: "avc1.640028", pto: 4, mediaStart: 4},
	})

	res := runPeriod(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "period" && strings.Contains(f.Message, "1280x720") {
			said = true
		}
	}
	if !said {
		t.Errorf("a resolution change across a period boundary was not reported:\n%s", dump(res))
	}
}

// A single-period MPD has no boundary and must gain no row.
func TestRun_SinglePeriodHasNoPeriodFinding(t *testing.T) {
	url := newPeriodOrigin(t, []periodSpec{
		{id: "only", start: 0, duration: 4, width: 1280, height: 720, codecs: "avc1.640028", pto: 0, mediaStart: 0},
	})

	res := runPeriod(t, url)

	if hasCheck(res, "period") {
		t.Errorf("a single-period MPD produced a period finding:\n%s", dump(res))
	}
}

// HLS has no periods at all.
func TestRun_HLSHasNoPeriodFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "period") {
		t.Errorf("an HLS playlist produced a period finding:\n%s", dump(res))
	}
}

// A Period held in another document states nothing here, so the Period after it
// has no derivable start. Placing it anyway would print a number segcheck made
// up, and the whole `period` check is about where a period sits — so the honest
// answer is that this one could not be placed. nomor's DASH-IF vector 5_1a has
// exactly this shape.
func TestRun_APeriodThatCannotBePlacedIsReportedAsALimit(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:xlink="http://www.w3.org/1999/xlink" type="static" mediaPresentationDuration="PT8S">
  <Period id="main" start="PT0S" duration="PT4S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="%d" duration="%d" media="main-$Number$.m4s" initialization="main-init.mp4" startNumber="0"/>
      <Representation id="v" bandwidth="%d" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period xlink:href="%s/ad.period" xlink:actuate="onLoad"></Period>
  <Period id="after" duration="PT4S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="%d" duration="%d" media="after-$Number$.m4s" initialization="after-init.mp4" startNumber="0"/>
      <Representation id="v" bandwidth="%d" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`, perTimescale, perSegTicks, drmBandwidth, srv.URL, perTimescale, perSegTicks, drmBandwidth)
	})
	for _, id := range []string{"main", "after"} {
		id := id
		mux.HandleFunc("/"+id+"-init.mp4", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4Init(1, perTimescale, "video", 1280, 720))
		})
		for i := 0; i < 2; i++ {
			i := i
			mux.HandleFunc(fmt.Sprintf("/%s-%d.m4s", id, i), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = w.Write(mediatest.MP4Segment(1, uint32(i), int64(i)*perSegTicks, perSampleDur, perSamples, 12000))
			})
		}
	}

	res := runPeriod(t, srv.URL+"/manifest.mpd")

	f, ok := findFinding(res, "period", finding.ERROR)
	if !ok {
		t.Fatalf("a period segcheck cannot place was not reported as a limit:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "xlink") && !strings.Contains(f.Message, "another document") {
		t.Errorf("the finding does not say why it could not be placed: %q", f.Message)
	}
	// And nothing may claim a position for it.
	for _, x := range res.Findings {
		if x.Check == "period" && x.Status == finding.BAD {
			t.Errorf("a period nobody could place produced a verdict about where it plays: %s", x.Message)
		}
	}
}
