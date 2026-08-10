package analyze

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The DASH path exercises what HLS cannot: a SegmentTimeline states each
// segment's start on the media timeline, which is the same timeline a fragment's
// tfdt counts on. That makes "the MPD says this segment starts at 4s and the
// media says 4.5s" a directly checkable claim.

const (
	dashTimescale = uint32(90000)
	dashSegTicks  = int64(180000) // 2s
	dashSamples   = 50
	dashSampleDur = uint32(3600)
	// dashPayload sizes the mdat so the measured bitrate is close to the
	// declared bandwidth below.
	dashPayload   = 12000
	dashBandwidth = 60_000
)

type dashSeg struct {
	// declaredT is the SegmentTimeline @t written into the MPD.
	declaredT int64
	// actualTFDT is the baseMediaDecodeTime written into the fragment. Setting
	// it apart from declaredT is how a timeline defect is planted.
	actualTFDT int64
}

func newDASHOrigin(t *testing.T, segs []dashSeg) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		var tl strings.Builder
		for i, s := range segs {
			if i == 0 {
				fmt.Fprintf(&tl, `<S t="%d" d="%d"/>`, s.declaredT, dashSegTicks)
				continue
			}
			fmt.Fprintf(&tl, `<S d="%d"/>`, dashSegTicks)
		}
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%dS">
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="%d" media="seg-$Number$.m4s" initialization="init.mp4" startNumber="0">
        <SegmentTimeline>%s</SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="%d" width="1280" height="720" codecs="avc1.4d401f"/>
    </AdaptationSet>
  </Period>
</MPD>`, len(segs)*2, dashTimescale, tl.String(), dashBandwidth)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})

	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Init(1, dashTimescale, "video", 1280, 720))
	})

	for i, s := range segs {
		i, s := i, s
		mux.HandleFunc(fmt.Sprintf("/seg-%d.m4s", i), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4Segment(1, uint32(i), s.actualTFDT, dashSampleDur, dashSamples, dashPayload))
		})
	}
	return srv
}

func cleanDASHSegs(count int) []dashSeg {
	out := make([]dashSeg, count)
	for i := range out {
		t := int64(i) * dashSegTicks
		out[i] = dashSeg{declaredT: t, actualTFDT: t}
	}
	return out
}

func TestRun_DASHCleanStream(t *testing.T) {
	srv := newDASHOrigin(t, cleanDASHSegs(4))
	res := runOn(t, srv.URL+"/manifest.mpd")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("clean DASH stream produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if res.Segments != 4 {
		t.Errorf("sampled %d segments, want 4", res.Segments)
	}
	// The MPD-versus-media timeline comparison is the point of this path.
	if !hasCheck(res, "timeline") {
		t.Errorf("no timeline finding: the SegmentTimeline check did not run.\n%s", dump(res))
	}
	if !hasCheck(res, "resolution") {
		t.Error("no resolution finding: the init segment's sample entry was not read")
	}
}

func TestRun_DASHFindsTimelineMismatch(t *testing.T) {
	segs := cleanDASHSegs(4)
	// The fragment really begins 400ms after where the MPD says it does. A
	// player seeking to the declared time lands outside the segment.
	segs[2].actualTFDT += 36000
	segs[3].actualTFDT += 36000

	srv := newDASHOrigin(t, segs)
	res := runOn(t, srv.URL+"/manifest.mpd")

	f, ok := findFinding(res, "timeline", finding.BAD)
	if !ok {
		t.Fatalf("the planted 400ms timeline mismatch was not reported.\n%s", dump(res))
	}
	if f.Value == nil || *f.Value < 390 || *f.Value > 410 {
		t.Errorf("reported drift = %v ms, want ~400", f.Value)
	}
	// The same shift also breaks continuity against the previous segment.
	if _, ok := findFinding(res, "continuity", finding.BAD); !ok {
		t.Error("the shift left the continuity check silent, though segment 1 no longer joins segment 2")
	}
}

func TestRun_DASHUnsupportedRepresentationIsReported(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <Representation id="v" bandwidth="800000" width="640" height="360"><SegmentBase indexRange="0-800"/></Representation>
  </AdaptationSet></Period>
</MPD>`))
	})

	res := runOn(t, srv.URL+"/manifest.mpd")
	f, ok := findFinding(res, "fetch", finding.ERROR)
	if !ok {
		t.Fatalf("a representation segcheck cannot expand was passed over silently.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "SegmentBase") {
		t.Errorf("finding does not say what is unsupported: %q", f.Message)
	}
}
