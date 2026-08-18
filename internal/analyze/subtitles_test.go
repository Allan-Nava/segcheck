package analyze

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The defect SC-38 exists for: a subtitle rendition that is perfectly valid and
// points somewhere else. The cues parse, the segments are the right size, the
// manifest is impeccable — and the subtitles are four hours away from the picture,
// because X-TIMESTAMP-MAP was written from the wrong clock.

func TestRun_SubtitleCuesDriftFromTheSegment(t *testing.T) {
	srv := newSubtitleOrigin(t, subtitleSpec{
		// Every segment's map anchors it four hours off.
		mapOffset: 4 * 3600,
	})
	res := runOn(t, srv+"/master-subs.m3u8")

	f, ok := findFinding(res, "subtitles", finding.BAD)
	if !ok {
		t.Fatalf("cues four hours from their segment were not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "does not overlap") {
		t.Errorf("finding does not say what is wrong: %q", f.Message)
	}
}

// Cues where the manifest says they should be: the case that must stay quiet.
func TestRun_SubtitleCuesAlignedIsFine(t *testing.T) {
	srv := newSubtitleOrigin(t, subtitleSpec{})
	res := runOn(t, srv+"/master-subs.m3u8")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("aligned subtitles produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	f, ok := findFinding(res, "subtitles", finding.OK)
	if !ok {
		t.Fatalf("no subtitles finding at all: the check did not run.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "cues") {
		t.Errorf("finding does not report what was found: %q", f.Message)
	}
}

// A segment that is not a subtitle document at all — an origin serving an HTML
// error page with a 200, which is the single most common way a subtitle rendition
// breaks in production.
func TestRun_SubtitleSegmentThatIsNotSubtitles(t *testing.T) {
	srv := newSubtitleOrigin(t, subtitleSpec{brokenSegment: 2})
	res := runOn(t, srv+"/master-subs.m3u8")

	// An unparseable segment is reported by the container check as the unknown
	// container it is; the subtitles check must not then claim the cues are missing
	// from a segment nobody could read.
	f, ok := findFinding(res, "subtitles", finding.OK)
	if !ok {
		t.Fatalf("no subtitles finding at all.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "3/4") {
		t.Errorf("finding does not say how many segments were readable: %q", f.Message)
	}
	if _, ok := findFinding(res, "container", finding.BAD); !ok {
		t.Errorf("the unreadable segment was not reported at all.\n%s", dump(res))
	}
}

// Without X-TIMESTAMP-MAP there is nothing to anchor the cue clock to. The cues are
// readable and the rendition may be perfectly correct — segcheck simply cannot
// tell, and says so rather than guessing in either direction.
func TestRun_SubtitleWithoutTimestampMapIsNotVerified(t *testing.T) {
	srv := newSubtitleOrigin(t, subtitleSpec{noTimestampMap: true})
	res := runOn(t, srv+"/master-subs.m3u8")

	f, ok := findFinding(res, "subtitles", finding.WARN)
	if !ok {
		t.Fatalf("a rendition with no X-TIMESTAMP-MAP was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "X-TIMESTAMP-MAP") {
		t.Errorf("finding does not name the missing line: %q", f.Message)
	}
}

// A subtitle rendition whose every sampled segment is empty is a rendition that
// offers a language and says nothing in it. Four consecutive empty segments is not
// proof — a gap in the dialogue is legitimate — so it is a WARN, with the count.
func TestRun_SubtitleRenditionWithNoCuesAtAll(t *testing.T) {
	srv := newSubtitleOrigin(t, subtitleSpec{noCues: true})
	res := runOn(t, srv+"/master-subs.m3u8")

	f, ok := findFinding(res, "subtitles", finding.WARN)
	if !ok {
		t.Fatalf("a rendition with no cues anywhere was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "no cues") {
		t.Errorf("finding does not say what is missing: %q", f.Message)
	}
}

// A presentation with no subtitle rendition has nothing for this check to say.
func TestRun_NoSubtitleRenditionIsSilent(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		segments: cleanSegments(4, 1280, 720),
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "subtitles") {
		t.Errorf("a stream with no subtitles produced a subtitles finding.\n%s", dump(res))
	}
}

// An fMP4-wrapped subtitle track states its timing in the wrapper and its cues in the
// samples. Both are reported, and the placement of an individual cue is left to
// `timeline` — which is what actually checks the fragment's own clock.
func TestCheckSubtitles_FMP4WrappedTrack(t *testing.T) {
	wrapped := func(tr media.Track) *renditionData {
		return &renditionData{
			r: manifest.Rendition{Name: "en", Kind: manifest.Text},
			segs: []segmentData{{
				seg:    manifest.Segment{Duration: 4},
				parsed: true,
				info:   media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{tr}},
			}},
		}
	}
	base := media.Track{
		Kind: media.Text, Codec: "ttml", Timescale: 90000,
		HasPTS: true, MinPTS: 0, MaxPTS: 360000, Samples: 2,
	}

	// Cues read out of the samples.
	withCues := base
	withCues.Cues, withCues.CuesRead = 3, true
	out := checkSubtitles([]*renditionData{wrapped(withCues)}, Defaults())
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "3 cues") {
		t.Errorf("the finding does not report the cues: %q", out[0].Message)
	}

	// Samples nobody could read. That is not the same as no cues, and must not
	// produce the WARN an empty rendition gets — "nobody looked" and "nothing there"
	// lead to opposite verdicts.
	out = checkSubtitles([]*renditionData{wrapped(base)}, Defaults())
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding for unreadable samples, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "could not read") {
		t.Errorf("the finding does not say nobody looked: %q", out[0].Message)
	}

	// And a wrapped rendition whose samples *were* read and hold nothing is the WARN.
	empty := base
	empty.CuesRead = true
	out = checkSubtitles([]*renditionData{wrapped(empty)}, Defaults())
	if len(out) != 1 || out[0].Status != finding.WARN {
		t.Fatalf("want one WARN for a rendition that says nothing, got %+v", out)
	}
}

// ---------- harness ----------

type subtitleSpec struct {
	// mapOffset shifts every segment's X-TIMESTAMP-MAP by this many seconds,
	// which is how a subtitle rendition ends up valid and in the wrong place.
	mapOffset float64
	// noTimestampMap omits the line entirely.
	noTimestampMap bool
	// noCues writes segments with no cues in them at all.
	noCues bool
	// brokenSegment, when non-zero, serves an HTML error page for that 1-based
	// segment instead of a subtitle document.
	brokenSegment int
	// ttml serves TTML rather than WebVTT.
	ttml bool
}

// newSubtitleOrigin serves a master playlist with a video variant and a WebVTT
// subtitle rendition beside it, which is the shape HLS uses for subtitles.
func newSubtitleOrigin(t *testing.T, spec subtitleSpec) string {
	t.Helper()
	const segs = 4

	video := variantSpec{
		name: "720p", bandwidth: syntheticBandwidth,
		width: 1280, height: 720, segments: cleanSegments(segs, 1280, 720),
	}
	mux := hlsOriginHandler([]variantSpec{video})
	mux.HandleFunc("/master-subs.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n"+
			"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"English\","+
			"LANGUAGE=\"en\",DEFAULT=YES,AUTOSELECT=YES,URI=\"subs.m3u8\"\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.640028\",SUBTITLES=\"subs\"\n"+
			"720p/index.m3u8\n", syntheticBandwidth)
	})
	mux.HandleFunc("/subs.m3u8", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 0; i < segs; i++ {
			fmt.Fprintf(&b, "#EXTINF:2.000,\nsub%d.vtt\n", i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		_, _ = w.Write([]byte(b.String()))
	})
	for i := 0; i < segs; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/sub%d.vtt", i), func(w http.ResponseWriter, r *http.Request) {
			if spec.brokenSegment == i+1 {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body>404 not found</body></html>"))
				return
			}
			start := float64(i) * segSeconds
			var cues []mediatest.Cue
			if !spec.noCues {
				cues = []mediatest.Cue{{Start: 0.5, End: 1.5, Text: "line"}}
			}
			w.Header().Set("Content-Type", "text/vtt")
			_, _ = w.Write(mediatest.WebVTT(mediatest.WebVTTOptions{
				NoTimestampMap: spec.noTimestampMap,
				MPEGTS:         int64((start + spec.mapOffset) * 90000),
				Cues:           cues,
			}))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// A subtitle rendition sampled without any video rendition beside it has cues on a
// clock whose origin nobody stated. Counting them is still worth something; judging
// their placement is not.
func TestCheckSubtitles_NoVideoToAnchorAgainst(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "en", Kind: manifest.Text},
		segs: []segmentData{{
			seg:    manifest.Segment{Duration: 2, Sequence: 0},
			parsed: true,
			info: media.SegmentInfo{Container: media.ContainerWebVTT, Tracks: []media.Track{{
				Kind: media.Text, Codec: "webvtt", Timescale: 90000,
				HasPTS: true, MinPTS: 900000, MaxPTS: 990000, Samples: 1,
				Cues: 1, CuesRead: true, CueMin: 900000, CueMax: 990000,
				HasCueSpan: true, CuesAnchored: true,
			}}},
		}},
	}
	out := checkSubtitles([]*renditionData{rd}, Defaults())
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "no video rendition") {
		t.Errorf("the finding does not say why: %q", out[0].Message)
	}
}

// A subtitle rendition whose every segment is unreadable has nothing for this check
// to say: the container check has already reported each one, and claiming the cues
// are missing would blame the stream for a segment nobody could read.
func TestCheckSubtitles_NothingReadable(t *testing.T) {
	quiet := []*renditionData{
		// No segments parsed at all.
		{r: manifest.Rendition{Name: "en", Kind: manifest.Text}},
		// Segments parsed, but none of them a subtitle document.
		{r: manifest.Rendition{Name: "fr", Kind: manifest.Text},
			segs: []segmentData{{parsed: true, info: media.SegmentInfo{
				Container: media.ContainerTS,
				Tracks:    []media.Track{{Kind: media.Video, Codec: "h264"}},
			}}}},
		// A rendition that could not be loaded, and a video one.
		{r: manifest.Rendition{Name: "broken", Kind: manifest.Text}, err: errUnusable},
		{r: manifest.Rendition{Name: "720p", Kind: manifest.Video}},
	}
	if out := checkSubtitles(quiet, Defaults()); len(out) != 0 {
		t.Errorf("want no findings, got %+v", out)
	}
}

// The origin is the earliest first timestamp across the sampled video renditions,
// and there is none when no video was sampled or none of it carries timestamps.
func TestMediaOrigin(t *testing.T) {
	vid := func(name string, start int64, ts uint32, hasPTS bool) *renditionData {
		return &renditionData{
			r: manifest.Rendition{Name: name, Kind: manifest.Video},
			segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{{
				Kind: media.Video, Timescale: ts, HasPTS: hasPTS, MinPTS: start, Samples: 50,
			}}}}},
		}
	}
	if at, ok := mediaOrigin([]*renditionData{vid("a", 900000, 90000, true), vid("b", 450000, 90000, true)}); !ok || at != 5 {
		t.Errorf("origin = %v/%v, want 5: the earliest rung wins", at, ok)
	}
	for _, rends := range [][]*renditionData{
		nil,
		{{r: manifest.Rendition{Name: "en", Kind: manifest.Text}}},
		{vid("a", 900000, 90000, false)},
		{vid("a", 900000, 0, true)},
		{{r: manifest.Rendition{Name: "x", Kind: manifest.Video}, err: errUnusable}},
	} {
		if at, ok := mediaOrigin(rends); ok {
			t.Errorf("origin = %v derived from %+v", at, rends)
		}
	}
}

// SC-97: a wrapped rendition gets the same drift check a text one does. A TTML document
// inside a stpp sample states its times on the presentation timeline, so a document
// pointing somewhere else fails exactly the way a WebVTT one with a bad X-TIMESTAMP-MAP
// does — and until the cues were timed, nothing said so.
func TestCheckSubtitles_WrappedCuesDrift(t *testing.T) {
	wrapped := func(cueMin, cueMax int) *renditionData {
		return &renditionData{
			r: manifest.Rendition{Name: "en", Kind: manifest.Text},
			segs: []segmentData{{
				seg:    manifest.Segment{Duration: 2, Sequence: 0},
				parsed: true,
				info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{{
					Kind: media.Text, Codec: "ttml", Timescale: 90000,
					HasPTS: true, MinPTS: 0, MaxPTS: 180000, Samples: 1,
					Cues: 1, CuesRead: true,
					CueMin: cueMin, CueMax: cueMax, HasCueSpan: true,
				}}},
			}},
		}
	}
	// Cues inside the segment's own window: nothing to report.
	for _, f := range checkSubtitles([]*renditionData{wrapped(45000, 135000)}, Defaults()) {
		if f.Status != finding.OK {
			t.Errorf("aligned wrapped cues produced %s: %s", f.Status, f.Message)
		}
	}

	// Cues an hour away from the segment the manifest put them in.
	out := checkSubtitles([]*renditionData{wrapped(3600*90000, 3601*90000)}, Defaults())
	f, ok := findingIn(out, finding.BAD)
	if !ok {
		t.Fatalf("wrapped cues an hour adrift were not reported: %+v", out)
	}
	if !strings.Contains(f.Message, "does not overlap") {
		t.Errorf("the finding does not say what is wrong: %q", f.Message)
	}
}

// The guards inside the drift comparison, each of which leaves a rendition unjudged
// rather than judged against a number nobody measured.
func TestDriftFrom_Guards(t *testing.T) {
	sd := segmentData{
		seg:  manifest.Segment{Duration: 2, Sequence: 0},
		info: media.SegmentInfo{Container: media.ContainerWebVTT},
	}
	starts := map[int]float64{0: 0}

	// No timescale to convert the cue span with.
	noScale := media.Track{Kind: media.Text, Cues: 1}
	if got := driftFrom(starts, sd, noScale, 0, true, Defaults(), 0, 90000); got != "" {
		t.Errorf("a track with no timescale was judged: %q", got)
	}
	// No cues to place.
	noCues := media.Track{Kind: media.Text, Timescale: 90000}
	if got := driftFrom(starts, sd, noCues, 0, true, Defaults(), 0, 0); got != "" {
		t.Errorf("a track with no cues was judged: %q", got)
	}
	// A text rendition with no video origin to anchor its cue clock to.
	text := media.Track{Kind: media.Text, Timescale: 90000, Cues: 1}
	if got := driftFrom(starts, sd, text, 0, false, Defaults(), 3600*90000, 3601*90000); got != "" {
		t.Errorf("a text rendition was judged with no origin: %q", got)
	}
	// And with one, the same cues are adrift.
	if got := driftFrom(starts, sd, text, 0, true, Defaults(), 3600*90000, 3601*90000); got == "" {
		t.Error("cues an hour adrift were not reported")
	}
}
