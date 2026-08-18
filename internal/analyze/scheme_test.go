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

// cbcs and cenc encrypt the same media, with the same key, and differ by a box
// field. Nothing about a stream looks different — the segments are the same
// size, the timing is identical, the manifest reads perfectly — so MPDs get
// copied between them, and cbcs content served as cenc plays nowhere at all.
//
// The container states the scheme twice over: the four-character code in `schm`
// and the pattern in `tenc`, where a crypt-to-clear pattern belongs to cbcs and
// cens and must not appear under cenc or cbc1. That second one is what lets the
// media be checked against itself when the manifest says nothing.

type schemeRung struct {
	id          string
	width       int
	height      int
	declared    string // the mp4protection @value
	inContainer string // the schm scheme
	crypt, skip int    // the tenc pattern
}

func newSchemeOrigin(t *testing.T, rungs []schemeRung) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		var sets strings.Builder
		for _, r := range rungs {
			fmt.Fprintf(&sets, `    <AdaptationSet mimeType="video/mp4" contentType="video">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value=%q/>
      <SegmentTemplate timescale="%d" duration="%d" media="%s-$Number$.m4s" initialization="%s-init.mp4" startNumber="0">
        <SegmentTimeline><S t="0" d="%d" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="%s" bandwidth="%d" width="%d" height="%d" codecs="avc1.640028"/>
    </AdaptationSet>
`, r.declared, drmTimescale, drmSegTicks, r.id, r.id, drmSegTicks, r.id, drmBandwidth, r.width, r.height)
		}
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
  <Period id="0" start="PT0S">
%s  </Period>
</MPD>`, sets.String())
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})

	for _, r := range rungs {
		r := r
		mux.HandleFunc("/"+r.id+"-init.mp4", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4InitCENCTenc(1, drmTimescale, r.width, r.height, "avc1",
				r.inContainer, "9eb4050d-e44b-4802-932e-27d75083e266", r.crypt, r.skip))
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		n := segNumberIn("/seg" + lastDashNumber(req.URL.Path) + ".ts")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Segment(1, uint32(n), int64(n)*drmSegTicks, drmSampleDur, drmSamples, drmPayload))
	})
	return srv.URL + "/manifest.mpd"
}

// lastDashNumber pulls the N out of ".../<id>-N.m4s".
func lastDashNumber(path string) string {
	s := strings.TrimSuffix(path, ".m4s")
	if i := strings.LastIndex(s, "-"); i >= 0 {
		return s[i+1:]
	}
	return "0"
}

func runScheme(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 2
	opts.Concurrency = 4
	opts.MaxRenditions = 0
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// The incident: the MPD was copied from a cenc presentation and the media is
// cbcs. Nothing else in the stream differs, and it plays nowhere.
func TestRun_FindsASchemeTheManifestGetsWrong(t *testing.T) {
	url := newSchemeOrigin(t, []schemeRung{
		{id: "v0", width: 1280, height: 720, declared: "cenc", inContainer: "cbcs", crypt: 1, skip: 9},
	})

	res := runScheme(t, url)

	f, ok := findFinding(res, "scheme", finding.BAD)
	if !ok {
		t.Fatalf("a manifest declaring the wrong encryption scheme was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "cbcs") || !strings.Contains(f.Message, "cenc") {
		t.Errorf("the scheme finding does not name both schemes: %q", f.Message)
	}
}

// A ladder that mixes schemes is its own failure whatever the manifest says: a
// player that negotiated one scheme cannot switch into a rung using the other.
func TestRun_FindsALadderThatMixesSchemes(t *testing.T) {
	url := newSchemeOrigin(t, []schemeRung{
		{id: "v0", width: 640, height: 360, declared: "cbcs", inContainer: "cbcs", crypt: 1, skip: 9},
		{id: "v1", width: 1280, height: 720, declared: "cenc", inContainer: "cenc"},
	})

	res := runScheme(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "scheme" && strings.Contains(f.Message, "ladder") {
			said = true
			if f.Status != finding.BAD {
				t.Errorf("a ladder mixing schemes was reported %s, want BAD", f.Status)
			}
		}
	}
	if !said {
		t.Errorf("a ladder mixing cenc and cbcs was not reported:\n%s", dump(res))
	}
}

// The container contradicting itself, with the manifest agreeing with neither
// half: a crypt pattern belongs to cbcs and cens, and cannot appear under cenc.
func TestRun_FindsACryptPatternUnderAPatternlessScheme(t *testing.T) {
	url := newSchemeOrigin(t, []schemeRung{
		{id: "v0", width: 1280, height: 720, declared: "cenc", inContainer: "cenc", crypt: 1, skip: 9},
	})

	res := runScheme(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "scheme" && strings.Contains(f.Message, "pattern") {
			said = true
		}
	}
	if !said {
		t.Errorf("a crypt pattern under cenc was not reported:\n%s", dump(res))
	}
}

// A manifest and a container that agree are healthy, and the check says which
// scheme it verified so the answer can be quoted.
func TestRun_ASchemeThatMatchesIsClean(t *testing.T) {
	url := newSchemeOrigin(t, []schemeRung{
		{id: "v0", width: 1280, height: 720, declared: "cbcs", inContainer: "cbcs", crypt: 1, skip: 9},
	})

	res := runScheme(t, url)

	f, ok := findFinding(res, "scheme", finding.OK)
	if !ok {
		t.Fatalf("a matching scheme produced no finding:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "cbcs") {
		t.Errorf("the scheme finding does not name what it verified: %q", f.Message)
	}
	for _, f := range res.Findings {
		if f.Check == "scheme" && f.Status != finding.OK {
			t.Errorf("a matching scheme produced %s: %s", f.Status, f.Message)
		}
	}
}

// Under cbcs, video is encrypted in a 1:9 pattern and *audio is not*: common
// encryption applies pattern encryption to video and full-sample encryption to
// audio, so a cbcs audio track states no pattern and is right not to. Requiring
// one reported Axinom's own cbcs vector as broken on its audio rung.
func TestRun_CbcsAudioWithNoPatternIsNotADefect(t *testing.T) {
	url := newSchemeOriginAudio(t, "cbcs", "cbcs")

	res := runScheme(t, url)

	for _, f := range res.Findings {
		if f.Check == "scheme" && f.Status != finding.OK {
			t.Errorf("a cbcs audio track with no crypt pattern was reported %s: %s", f.Status, f.Message)
		}
	}
}

// A pattern under cenc is wrong whatever the track carries: cenc encrypts end
// to end and has no pattern to state.
func TestRun_CencAudioWithAPatternIsStillADefect(t *testing.T) {
	url := newSchemeOriginAudioPattern(t, "cenc", "cenc", 1, 9)

	res := runScheme(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "scheme" && strings.Contains(f.Message, "pattern") {
			said = true
		}
	}
	if !said {
		t.Errorf("a crypt pattern on a cenc audio track was not reported:\n%s", dump(res))
	}
}

// Unprotected media has no scheme and must gain no row.
func TestRun_UnprotectedMediaHasNoSchemeFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "scheme") {
		t.Errorf("unprotected media produced a scheme finding:\n%s", dump(res))
	}
}

// newSchemeOriginAudio serves an audio-only protected AdaptationSet, which is
// where the pattern rule differs: common encryption gives video a pattern and
// audio full-sample encryption.
func newSchemeOriginAudio(t *testing.T, declared, inContainer string) string {
	return newSchemeOriginAudioPattern(t, declared, inContainer, 0, 0)
}

func newSchemeOriginAudioPattern(t *testing.T, declared, inContainer string, crypt, skip int) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value=%q/>
      <SegmentTemplate timescale="%d" duration="%d" media="a-$Number$.m4s" initialization="a-init.mp4" startNumber="0">
        <SegmentTimeline><S t="0" d="%d" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a0" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>`, declared, drmTimescale, drmSegTicks, drmSegTicks)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/a-init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4InitCENCAudioTenc(1, drmTimescale, 2, 48000,
			inContainer, "9eb4050d-e44b-4802-932e-27d75083e266", crypt, skip))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		n := segNumberIn("/seg" + lastDashNumber(req.URL.Path) + ".ts")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Segment(1, uint32(n), int64(n)*drmSegTicks, drmSampleDur, drmSamples, drmPayload))
	})
	return srv.URL + "/manifest.mpd"
}
