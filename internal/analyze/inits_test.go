package analyze

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// Initialisation segments, and the last of the sampling paths.
//
// An fMP4 rendition is unreadable without its init segment: the timescale, codec
// and resolution all live there. When it cannot be fetched the segments still
// download, so the temptation is to report what little they say — but a fragment
// with no timescale would make every duration unmeasurable and every codec
// unknown, and reporting that as a defect blames the packager for a fetch
// failure. The init failure has to be named instead.

// An EXT-X-MAP with a BYTERANGE addresses a sub-range of a larger resource. The
// Range header has to travel with it, or the whole file is fetched and the bytes
// parsed as an init segment are the wrong ones.
func TestInitFor_ByteRangeMap(t *testing.T) {
	rd := rend("720p")
	sd := segmentData{seg: manifest.Segment{
		Sequence:  1,
		InitURI:   "https://cdn.example.com/hls/all.mp4",
		InitRange: &manifest.ByteRange{Offset: 0, Length: 800},
	}}

	ref := initFor(rd, sd)
	if ref.empty {
		t.Fatal("an EXT-X-MAP with a byte range was treated as absent")
	}
	if ref.uri != "https://cdn.example.com/hls/all.mp4" {
		t.Errorf("uri = %q", ref.uri)
	}
	if ref.rng != "bytes=0-799" {
		t.Errorf("range = %q, want bytes=0-799", ref.rng)
	}
}

// A segment with no init of its own falls back to the rendition's, which is how
// DASH states it: once per representation rather than per segment. The byte range
// does not carry across, because it addressed the segment's own resource.
func TestInitFor_FallsBackToTheRendition(t *testing.T) {
	rd := rend("720p")
	rd.r.InitURI = "https://cdn.example.com/dash/v0/init.mp4"

	ref := initFor(rd, segmentData{seg: manifest.Segment{Sequence: 1}})
	if ref.empty || ref.uri != rd.r.InitURI {
		t.Errorf("ref = %+v, want the rendition's init URI", ref)
	}
	if ref.rng != "" {
		t.Errorf("range = %q, want none", ref.rng)
	}

	// MPEG-TS needs no init segment at all.
	if ref := initFor(rend("720p"), segmentData{seg: manifest.Segment{Sequence: 1}}); !ref.empty {
		t.Errorf("ref = %+v, want empty for a rendition with no init segment", ref)
	}
}

// An init segment that 404s is attributed back to the rendition that needed it, as
// an ERROR naming the URI. Every check that depends on the init then stays quiet.
func TestRun_InitSegmentThatCannotBeFetched(t *testing.T) {
	segBytes := mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000)

	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n" +
			"#EXT-X-MAP:URI=\"init.mp4\"\n" +
			"#EXTINF:2.0,\nseg1.m4s\n" +
			"#EXTINF:2.0,\nseg2.m4s\n" +
			"#EXT-X-ENDLIST\n"))
	})
	// The init segment is gone.
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/seg1.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(segBytes)
	})
	mux.HandleFunc("/seg2.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Segment(1, 2, 180000, 3600, 50, 2000))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/index.m3u8")

	f, ok := findFinding(res, "init", finding.ERROR)
	if !ok {
		t.Fatalf("a missing init segment was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "init.mp4") {
		t.Errorf("message = %q, want it to name the init segment", f.Message)
	}

	// And nothing blames the media for it: no BAD anywhere.
	for _, fd := range res.Findings {
		if fd.Status == finding.BAD {
			t.Errorf("a missing init segment produced a BAD about the media: %s — %s", fd.Check, fd.Message)
		}
	}
}

// A DASH representation that states no addressing has no URI and no inline
// segments, so there is nothing to sample. It is reported against that rendition
// rather than crashing the run or being silently dropped.
func TestRun_DASHRepresentationWithNothingToSample(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="unaddressable" bandwidth="800000" width="1280" height="720"/>
    </AdaptationSet>
  </Period>
</MPD>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	}))
	defer srv.Close()

	res := runOn(t, srv.URL+"/manifest.mpd")
	if len(res.Findings) == 0 {
		t.Fatal("a manifest with an unaddressable representation produced no findings")
	}
	var said bool
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "neither a URI nor inline segments") ||
			strings.Contains(f.Message, "SegmentTemplate") {
			said = true
		}
	}
	if !said {
		t.Errorf("an unaddressable representation was not explained:\n%s", dump(res))
	}
}

// checkBitrate needs a timeline to weigh the bytes against. A segment whose tracks
// carry no timestamps has none, so there is nothing to divide by — and a bitrate
// derived from a zero duration would be meaningless.
func TestCheckBitrate_SkipsSegmentsWithNoTimeline(t *testing.T) {
	noPTS := media.Track{ID: 1, Kind: media.Video, Codec: "h264", Timescale: 90000}
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, noPTS),
		okSeg(2, media.ContainerTS, noPTS),
	))
	rd.r.Bandwidth = 800000

	if got := checkBitrate([]*renditionData{rd}, Defaults()); len(got) != 0 {
		t.Errorf("checkBitrate reported on segments with no timeline:\n%s", dumpFindings(got))
	}
}

// Alignment needs at least two renditions whose start is measurable. One of each
// leaves nothing to compare, and the check has to stay silent rather than compare
// a start against nothing.
func TestCheckAlignment_NeedsTwoMeasurableStarts(t *testing.T) {
	measurable := rend("720p", withSegs(okSeg(1, media.ContainerTS, videoTrack())))
	// Timestamps present but no timescale: the start cannot be converted.
	noScale := media.Track{ID: 1, Kind: media.Video, Codec: "h264", HasPTS: true, MinPTS: 90000, Samples: 100}
	unmeasurableStart := rend("1080p", withSegs(okSeg(1, media.ContainerTS, noScale)))

	got := checkAlignment([]*renditionData{measurable, unmeasurableStart}, Defaults())
	if len(got) != 0 {
		t.Errorf("checkAlignment reported with only one measurable start:\n%s", dumpFindings(got))
	}
}
