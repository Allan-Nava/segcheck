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
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The checks added for conformance, trick play, protection and codec strings,
// called directly with hand-built rendition data — for the same reason the first
// file of these exists. An HTTP fixture cannot easily produce a track that states
// its sample flags but no timestamps, a rendition whose bitstream is opaque, a
// ladder whose rungs disagree about one thing while agreeing about everything
// else, or a trick-play entry holding four pictures. Each of those is a branch
// deciding between a defect an operator must act on and the OK-level "segcheck
// could not look" the rules require whenever the limit is ours.

// syncTrack states outright that it opens on a sync sample, which is the
// container asserting rather than a bitstream walk inferring.
func syncTrack(opens bool) media.Track {
	t := videoTrack()
	t.OpensOnKeyframe, t.HasKeyframe, t.KeyframeKnown, t.KeyframeStated = opens, opens, true, true
	return t
}

// walkedTrack is a track whose answer came from a bitstream walk: known, and not
// an assertion.
func walkedTrack(opens, present, scanned bool) media.Track {
	t := videoTrack()
	t.OpensOnKeyframe, t.HasKeyframe, t.KeyframeKnown, t.KeyframeScanned = opens, present, true, scanned
	return t
}

// ---------- the Apple IDR rule ----------

// The rule reports only what it can stand behind, and the three kinds of evidence
// lead to three different verdicts.
func TestAppleIDRPerSegment_Evidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		track      media.Track
		wantStatus finding.Status
		wantSaid   string
	}{
		{
			name:  "the container states a non-sync first sample",
			track: syncTrack(false), wantStatus: finding.WARN,
			wantSaid: "do not begin with an IDR",
		},
		{
			name:  "the container states a sync first sample",
			track: syncTrack(true), wantStatus: finding.OK,
			wantSaid: "begin with an IDR",
		},
		{
			name:  "a completed walk found no random access point at all",
			track: walkedTrack(false, false, true), wantStatus: finding.WARN,
			wantSaid: "do not begin with an IDR",
		},
		{
			name:  "a walk found one, but not first — decode order settles nothing",
			track: walkedTrack(false, true, false), wantStatus: finding.OK,
			wantSaid: "not settled evidence",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := profileContext{
				rends: []*renditionData{rend("720p", withSegs(okSeg(0, media.ContainerMP4, tc.track)))},
				opts:  Defaults(),
			}
			got := appleIDRPerSegment(ctx)
			if len(got) != 1 {
				t.Fatalf("produced %d findings, want 1: %v", len(got), got)
			}
			if got[0].Status != tc.wantStatus {
				t.Errorf("status = %s, want %s: %s", got[0].Status, tc.wantStatus, got[0].Message)
			}
			if !strings.Contains(got[0].Message, tc.wantSaid) {
				t.Errorf("message %q does not contain %q", got[0].Message, tc.wantSaid)
			}
		})
	}

	// A track that says nothing at all, and a rendition that is not video: both
	// leave the rule silent rather than guessing.
	silent := videoTrack()
	ctx := profileContext{
		rends: []*renditionData{rend("720p", withSegs(okSeg(0, media.ContainerMP4, silent)))},
		opts:  Defaults(),
	}
	if got := appleIDRPerSegment(ctx); len(got) != 0 {
		t.Errorf("a track stating nothing produced %v", got)
	}

	audioRung := rend("audio", withSegs(okSeg(0, media.ContainerMP4, audioTrack())))
	audioRung.r.Kind = manifest.Audio
	ctx = profileContext{rends: []*renditionData{audioRung}, opts: Defaults()}
	if got := appleIDRPerSegment(ctx); len(got) != 0 {
		t.Errorf("an audio rendition was asked about IDRs: %v", got)
	}

	// A rendition whose initialisation segment never arrived: the rule cannot
	// read the samples and must not report on them.
	noInit := rend("720p", withSegs(okSeg(0, media.ContainerMP4, syncTrack(false))))
	noInit.initErr = errFake("init 404")
	ctx = profileContext{rends: []*renditionData{noInit}, opts: Defaults()}
	if got := appleIDRPerSegment(ctx); len(got) != 0 {
		t.Errorf("a rendition with no init produced %v", got)
	}
}

// ---------- the Apple bitrate tier ----------

func TestAppleBitrateTier_Branches(t *testing.T) {
	// An HEVC rung is measured against nothing: the specification's table is
	// H.264, and an efficient encode is not an under-bitrate one.
	hevc := videoTrack()
	hevc.Codec = "hevc"
	ctx := profileContext{
		rends: []*renditionData{rend("2160p", withSegs(okSeg(0, media.ContainerMP4, hevc)))},
		opts:  Defaults(),
	}
	got := appleBitrateTier(ctx)
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "H.264 only") {
		t.Errorf("an HEVC rung produced %v", got)
	}

	// A rung whose media states no resolution falls back to the manifest's.
	sizeless := videoTrack()
	sizeless.Width, sizeless.Height = 0, 0
	rd := rend("720p", withSegs(okSeg(0, media.ContainerMP4, sizeless)))
	rd.r.Width, rd.r.Height = 1280, 720
	ctx = profileContext{rends: []*renditionData{rd}, opts: Defaults()}
	if got := appleBitrateTier(ctx); len(got) != 1 {
		t.Errorf("a rung with only a declared resolution produced %v", got)
	}

	// And one where nothing states a resolution at all: no tier is implied.
	bare := rend("unknown", withSegs(okSeg(0, media.ContainerMP4, sizeless)))
	ctx = profileContext{rends: []*renditionData{bare}, opts: Defaults()}
	if got := appleBitrateTier(ctx); len(got) != 0 {
		t.Errorf("a rung with no resolution anywhere produced %v", got)
	}

	// An audio rendition is not a video rung.
	audioRung := rend("audio", withSegs(okSeg(0, media.ContainerMP4, audioTrack())))
	audioRung.r.Kind = manifest.Audio
	ctx = profileContext{rends: []*renditionData{audioRung}, opts: Defaults()}
	if got := appleBitrateTier(ctx); len(got) != 0 {
		t.Errorf("an audio rendition was measured against a video tier: %v", got)
	}
}

// ---------- the Apple frame rate and duration rules ----------

// A rendition whose rate changes part-way through makes every player re-time its
// renderer, and it is only visible across segments.
func TestAppleFrameRate_VariesWithinARendition(t *testing.T) {
	slow := videoTrack()
	slow.FrameDur = 7200 // half the rate of the other segment
	ctx := profileContext{
		rends: []*renditionData{rend("720p", withSegs(
			okSeg(0, media.ContainerMP4, videoTrack()),
			okSeg(1, media.ContainerMP4, videoTrack()),
			okSeg(2, media.ContainerMP4, slow),
		))},
		opts: Defaults(),
	}
	got := appleFrameRate(ctx)
	var said bool
	for _, f := range got {
		if strings.Contains(f.Message, "varies within the rendition") {
			said = true
			if f.Status != finding.WARN {
				t.Errorf("a rate change was reported %s, want WARN", f.Status)
			}
		}
	}
	if !said {
		t.Errorf("a rate change within a rendition was not reported: %v", got)
	}

	// A ladder mixing unrelated rates.
	odd := videoTrack()
	odd.FrameDur = 3000 // 30fps against 25
	ctx = profileContext{
		rends: []*renditionData{
			rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack()))),
			rend("1080p", withSegs(okSeg(0, media.ContainerMP4, odd))),
		},
		opts: Defaults(),
	}
	said = false
	for _, f := range appleFrameRate(ctx) {
		if strings.Contains(f.Message, "unrelated frame rates") {
			said = true
		}
	}
	if !said {
		t.Error("a ladder mixing unrelated rates was not reported")
	}

	// A rendition whose track states no rate at all leaves the rule silent.
	rateless := videoTrack()
	rateless.FrameDur = 0
	ctx = profileContext{
		rends: []*renditionData{rend("720p", withSegs(okSeg(0, media.ContainerMP4, rateless)))},
		opts:  Defaults(),
	}
	if got := appleFrameRate(ctx); len(got) != 0 {
		t.Errorf("a rendition stating no frame rate produced %v", got)
	}
}

// The last segment of a VOD presentation is legitimately short — the content ends
// where it ends — and reporting it would fire on every on-demand asset there is.
func TestAppleSegmentDuration_Branches(t *testing.T) {
	short := videoTrack()
	short.MaxPTS = 36000 // a fifth of the others
	rd := rend("720p", withSegs(
		okSeg(0, media.ContainerMP4, videoTrack()),
		okSeg(1, media.ContainerMP4, videoTrack()),
		okSeg(2, media.ContainerMP4, short),
	))
	ctx := profileContext{rends: []*renditionData{rd}, opts: Defaults()}
	for _, f := range appleSegmentDuration(ctx) {
		if f.Status != finding.OK {
			t.Errorf("a short final VOD segment produced %s: %s", f.Status, f.Message)
		}
	}

	// On a live rendition the same shape is a real inconsistency: nothing ends.
	rd.live = true
	ctx = profileContext{rends: []*renditionData{rd}, opts: Defaults()}
	var said bool
	for _, f := range appleSegmentDuration(ctx) {
		if f.Status == finding.WARN && strings.Contains(f.Message, "vary") {
			said = true
		}
	}
	if !said {
		t.Error("a short segment in the middle of a live window was not reported")
	}

	// One segment states nothing to compare with anything.
	one := rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))
	ctx = profileContext{rends: []*renditionData{one}, opts: Defaults()}
	if got := appleSegmentDuration(ctx); len(got) != 0 {
		t.Errorf("a single segment produced %v", got)
	}

	// A ladder whose rungs use different segment lengths cannot be switched
	// between cleanly, however consistent each rung is with itself.
	long := videoTrack()
	long.MaxPTS = 720000
	ctx = profileContext{
		rends: []*renditionData{
			rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack()), okSeg(1, media.ContainerMP4, videoTrack()))),
			rend("1080p", withSegs(okSeg(0, media.ContainerMP4, long), okSeg(1, media.ContainerMP4, long))),
		},
		opts: Defaults(),
	}
	said = false
	for _, f := range appleSegmentDuration(ctx) {
		if strings.Contains(f.Message, "different segment lengths") {
			said = true
		}
	}
	if !said {
		t.Error("a ladder using two segment lengths was not reported")
	}
}

// A subtitle rendition's timestamps are a cue span, not an extent, so the
// peak-to-average rule must not read them as one.
func TestApplePeakVsAverage_SkipsText(t *testing.T) {
	textRung := rend("subs", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))
	textRung.r.Kind = manifest.Text
	ctx := profileContext{rends: []*renditionData{textRung}, opts: Defaults()}
	if got := applePeakVsAverage(ctx); len(got) != 0 {
		t.Errorf("a subtitle rendition was measured for bitrate: %v", got)
	}

	// A rendition that could not be sampled at all.
	broken := rend("720p")
	broken.err = errFake("playlist 404")
	ctx = profileContext{rends: []*renditionData{broken}, opts: Defaults()}
	if got := applePeakVsAverage(ctx); len(got) != 0 {
		t.Errorf("an unsampled rendition produced %v", got)
	}
}

// ---------- trick play ----------

func TestCheckIFrame_Branches(t *testing.T) {
	iframeRend := manifest.Rendition{Name: "iframe", Kind: manifest.IFrame, Width: 1280, Height: 720}

	// A trick-play playlist that would not load.
	got := checkIFrame([]*iframeData{{r: iframeRend, err: errFake("404")}}, nil, Defaults())
	if len(got) != 1 || got[0].Status != finding.ERROR {
		t.Errorf("an unloadable trick-play playlist produced %v", got)
	}

	// One that loaded and lists nothing: a scrub has nothing to preview with.
	got = checkIFrame([]*iframeData{{r: iframeRend}}, nil, Defaults())
	if len(got) != 1 || got[0].Status != finding.ERROR || !strings.Contains(got[0].Message, "no entries") {
		t.Errorf("an empty trick-play playlist produced %v", got)
	}

	// Entries that arrived and would not parse.
	unparsed := &iframeData{r: iframeRend, segs: []segmentData{{
		seg:      manifest.Segment{Sequence: 0, URI: "if0.m4s"},
		parseErr: errFake("not media"),
	}}}
	got = checkIFrame([]*iframeData{unparsed}, nil, Defaults())
	var sawParse bool
	for _, f := range got {
		if strings.Contains(f.Message, "not readable media") {
			sawParse = true
			if f.Status != finding.BAD {
				t.Errorf("an unparseable range was reported %s", f.Status)
			}
		}
	}
	if !sawParse {
		t.Errorf("an unparseable trick-play range produced %v", got)
	}

	// An entry holding more than one picture: the range swept up the media after
	// the keyframe, and every scrub downloads it.
	multi := syncTrack(true)
	multi.Samples = 4
	id := &iframeData{r: iframeRend, segs: []segmentData{okSeg(0, media.ContainerMP4, multi)}}
	got = iframeKeyframeFindings(id, "iframe")
	var sawMulti bool
	for _, f := range got {
		if strings.Contains(f.Message, "more than one picture") {
			sawMulti = true
			if f.Status != finding.WARN {
				t.Errorf("a multi-picture range was reported %s, want WARN", f.Status)
			}
		}
	}
	if !sawMulti {
		t.Errorf("a range holding four pictures produced %v", got)
	}

	// A walk that found a keyframe somewhere other than first: unsettled, not a
	// defect, exactly as the Apple rule treats it. One picture, so the
	// multi-picture rule above stays out of it.
	single := walkedTrack(false, true, false)
	single.Samples = 1
	id = &iframeData{r: iframeRend, segs: []segmentData{okSeg(0, media.ContainerMP4, single)}}
	got = iframeKeyframeFindings(id, "iframe")
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "not settled evidence") {
		t.Errorf("an unsettled trick-play range produced %v", got)
	}

	// An entry with no video track at all: the range arrived and parsed and
	// segcheck could not read a picture out of it, which is a limit of this tool
	// and has to be said rather than passed off as a clean rung.
	id = &iframeData{r: iframeRend, segs: []segmentData{okSeg(0, media.ContainerMP4, audioTrack())}}
	got = iframeKeyframeFindings(id, "iframe")
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "could not read a picture") {
		t.Errorf("an entry with no picture in it produced %v", got)
	}
}

// The timeline half needs a video rung to compare against, and has to stay quiet
// when there is none or when neither states a timeline.
func TestIFrameTimelineFindings_Guards(t *testing.T) {
	iframeRend := manifest.Rendition{Name: "iframe", Kind: manifest.IFrame, Width: 1280, Height: 720}
	id := &iframeData{r: iframeRend, segs: []segmentData{okSeg(0, media.ContainerMP4, syncTrack(true))}}

	if got := iframeTimelineFindings(id, "iframe", nil, Defaults()); got != nil {
		t.Errorf("with no video rung to compare against, produced %v", got)
	}

	// A video rung whose segments state no timeline.
	timeless := videoTrack()
	timeless.HasPTS = false
	video := rend("720p", withSegs(okSeg(0, media.ContainerMP4, timeless)))
	if got := iframeTimelineFindings(id, "iframe", []*renditionData{video}, Defaults()); got != nil {
		t.Errorf("with no readable video timeline, produced %v", got)
	}

	// And a trick-play rung whose own entries state none.
	bare := &iframeData{r: iframeRend, segs: []segmentData{okSeg(0, media.ContainerMP4, timeless)}}
	real := rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))
	if got := iframeTimelineFindings(bare, "iframe", []*renditionData{real}, Defaults()); got != nil {
		t.Errorf("with no readable trick-play timeline, produced %v", got)
	}
}

// ---------- parts ----------

func TestPartFindings_Branches(t *testing.T) {
	rd := rend("720p")
	rd.hasParts = true
	rd.partTarget = 0.4
	rd.parts = []partData{
		{part: manifest.Part{URI: "p0.m4s", Sequence: 3, Index: 0}, parseErr: errFake("not media")},
		{part: manifest.Part{URI: "p1.m4s", Sequence: 3, Index: 1}, fetchErr: errFake("404")},
	}
	got := partFindings(rd, "720p", 0.1, Defaults())
	var sawFetch, sawParse bool
	for _, f := range got {
		if strings.Contains(f.Message, "not fetched") {
			sawFetch = true
		}
		if strings.Contains(f.Message, "not readable media") {
			sawParse = true
		}
	}
	if !sawFetch || !sawParse {
		t.Errorf("delivery failures produced %v", got)
	}

	// Parts that arrived and state no timeline: nothing to reconstruct with.
	timeless := videoTrack()
	timeless.HasPTS = false
	rd.parts = []partData{{
		part:   manifest.Part{URI: "p0.m4s", Sequence: 3, Index: 0},
		parsed: true,
		info:   media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{timeless}},
	}}
	got = partFindings(rd, "720p", 0.1, Defaults())
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "states a timeline") {
		t.Errorf("timeless parts produced %v", got)
	}

	// A part longer than PART-TARGET breaks the latency budget the playlist itself
	// promised, and it is the measured length that settles it.
	long := videoTrack()
	long.MinPTS, long.MaxPTS, long.FrameDur = 0, 90000, 3600 // a whole second
	rd.parts = []partData{{
		part:   manifest.Part{URI: "p0.m4s", Sequence: 3, Index: 0},
		parsed: true,
		info:   media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{long}},
	}}
	got = partFindings(rd, "720p", 0.1, Defaults())
	var sawTarget bool
	for _, f := range got {
		if strings.Contains(f.Message, "PART-TARGET") {
			sawTarget = true
			if f.Status != finding.BAD {
				t.Errorf("a part over PART-TARGET was reported %s", f.Status)
			}
		}
	}
	if !sawTarget {
		t.Errorf("a part longer than PART-TARGET produced %v", got)
	}
}

// A rendition that publishes parts and had none sampled, and one where the caller
// switched them off, both say so rather than staying silent.
func TestCheckParts_NothingToCheck(t *testing.T) {
	rd := rend("720p")
	rd.hasParts = true

	opts := Defaults()
	opts.PartSegments = 0
	got := checkParts([]*renditionData{rd}, opts)
	if len(got) != 1 || !strings.Contains(got[0].Message, "--parts 0") {
		t.Errorf("--parts 0 produced %v", got)
	}

	got = checkParts([]*renditionData{rd}, Defaults())
	if len(got) != 1 || !strings.Contains(got[0].Message, "none of the sampled segments") {
		t.Errorf("a rendition with no sampled parts produced %v", got)
	}

	// A rendition that publishes no parts gains no row at all.
	plain := rend("720p")
	if got := checkParts([]*renditionData{plain}, Defaults()); len(got) != 0 {
		t.Errorf("a rendition with no parts produced %v", got)
	}
}

// ---------- the encryption scheme ----------

// HLS states its scheme in EXT-X-KEY rather than in a box, and SAMPLE-AES-CTR is
// cenc by another name.
func TestContainerScheme_FromHLSKeyMethod(t *testing.T) {
	for _, tc := range []struct {
		method string
		want   string
	}{
		{"SAMPLE-AES", "cbcs"},
		{"SAMPLE-AES-CTR", "cenc"},
	} {
		rd := &renditionData{segs: []segmentData{{seg: manifest.Segment{KeyMethod: tc.method}}}}
		scheme, _, _, _, _, _, ok := containerScheme(rd)
		if !ok || scheme != tc.want {
			t.Errorf("%s read as %q (ok=%v), want %q", tc.method, scheme, ok, tc.want)
		}
	}
}

// A manifest that declares a scheme over media stating none is unverified, not
// wrong.
func TestCheckScheme_NothingToCompare(t *testing.T) {
	rd := rend("720p")
	rd.r.KeyScheme = "cbcs"
	got := checkScheme([]*renditionData{rd})
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "no sampled segment states a scheme") {
		t.Errorf("a declared scheme over unreadable media produced %v", got)
	}

	// Media stating a scheme the manifest says nothing about is reported as a
	// fact rather than a mismatch.
	enc := videoTrack()
	enc.Protection = "cenc"
	plain := rend("720p", withSegs(okSeg(0, media.ContainerMP4, enc)))
	got = checkScheme([]*renditionData{plain})
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "declares none") {
		t.Errorf("media stating a scheme with no manifest claim produced %v", got)
	}

	// And a rendition that could not be sampled at all.
	broken := rend("720p")
	broken.err = errFake("404")
	broken.r.KeyScheme = "cenc"
	if got := checkScheme([]*renditionData{broken}); len(got) != 0 {
		t.Errorf("an unsampled rendition produced %v", got)
	}
}

// ---------- availability, DVR and PDT guards ----------

// HLS lists what exists rather than computing it, so none of the availability
// arithmetic applies and none of it may appear in the report.
func TestCheckAvailability_NotADynamicMPD(t *testing.T) {
	hls := manifest.Playlist{Kind: manifest.KindHLS, Live: true}
	if got := checkAvailability(hls, nil, referenceClock{}, nil, Defaults()); got != nil {
		t.Errorf("an HLS playlist produced %v", got)
	}
	static := manifest.Playlist{Kind: manifest.KindDASH}
	if got := checkAvailability(static, nil, referenceClock{}, nil, Defaults()); got != nil {
		t.Errorf("a static MPD produced %v", got)
	}

	// A clock source segcheck cannot speak: named, and the verdict qualified.
	live := manifest.Playlist{Kind: manifest.KindDASH, Live: true, URL: "https://cdn.example/m.mpd"}
	clock := referenceClock{unsupported: []string{"urn:mpeg:dash:utc:ntp:2014"}}
	got := checkAvailability(live, nil, clock, nil, Defaults())
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "does not speak") {
		t.Errorf("an unspeakable clock scheme produced %v", got)
	}

	// A clock that agreed: reported, so the verdict below it can be trusted.
	clock = referenceClock{ok: true, source: "urn:mpeg:dash:utc:http-head:2014"}
	got = checkAvailability(live, nil, clock, nil, Defaults())
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "agrees") {
		t.Errorf("a clock in agreement produced %v", got)
	}
}

// The DVR probe reports each of its three outcomes distinctly, because a 404 and
// an error page fail a scrub the same way and are fixed differently.
func TestCheckDVR_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		probe      *dvrProbe
		wantStatus finding.Status
		wantSaid   string
	}{
		{
			name:       "the oldest segment is not there",
			probe:      &dvrProbe{label: "720p", depth: 60, probed: true, fetchErr: errFake("HTTP 404")},
			wantStatus: finding.BAD, wantSaid: "not on the origin",
		},
		{
			name:       "it answered with something that is not media",
			probe:      &dvrProbe{label: "720p", depth: 60, probed: true, parseErr: errFake("not media")},
			wantStatus: finding.BAD, wantSaid: "not readable media",
		},
		{
			name:       "it is there and it parses",
			probe:      &dvrProbe{label: "720p", depth: 60, probed: true, parsed: true},
			wantStatus: finding.OK, wantSaid: "still fetches and parses",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkDVR(tc.probe)
			if len(got) != 1 || got[0].Status != tc.wantStatus {
				t.Fatalf("produced %v, want one %s", got, tc.wantStatus)
			}
			if !strings.Contains(got[0].Message, tc.wantSaid) {
				t.Errorf("message %q does not contain %q", got[0].Message, tc.wantSaid)
			}
		})
	}
}

// A segment carrying a wall clock that did not parse, and one whose track states
// no timeline, are both unusable — and the check has to skip them rather than
// compare against a zero.
func TestPDTPoints_SkipsWhatItCannotUse(t *testing.T) {
	stamped := manifest.Segment{Sequence: 0, URI: "a.ts", Duration: 2, HasPDT: true}
	timeless := videoTrack()
	timeless.HasPTS = false

	rd := &renditionData{segs: []segmentData{
		{seg: stamped},                       // never parsed
		{seg: manifest.Segment{Sequence: 1}}, // no wall clock
		{seg: stamped, parsed: true, info: media.SegmentInfo{Tracks: []media.Track{timeless}}},
	}}
	if got := pdtPoints(rd); len(got) != 0 {
		t.Errorf("pdtPoints returned %d points from segments it cannot use", len(got))
	}
}

// The ladder half compares the offset between wall clock and media, and only
// where the media already agrees — otherwise `alignment` owns the finding and
// reporting it twice would double-count one bug.
func TestPDTLadderFindings_OnlyWhereTheMediaAgrees(t *testing.T) {
	rd := rend("720p")
	byRendition := map[string][]pdtPoint{
		"720p":  {{sd: segmentData{seg: manifest.Segment{Sequence: 0}}, start: 0}},
		"1080p": {{sd: segmentData{seg: manifest.Segment{Sequence: 0}}, start: 30}},
	}
	other := rend("1080p")
	got := pdtLadderFindings([]*renditionData{rd, other}, byRendition, []string{"720p", "1080p"}, 0.1)
	if len(got) != 0 {
		t.Errorf("rungs whose media disagrees produced a pdt finding: %v", got)
	}

	// A single rung has nothing to compare with.
	got = pdtLadderFindings([]*renditionData{rd}, map[string][]pdtPoint{"720p": {{}}}, []string{"720p"}, 0.1)
	if got != nil {
		t.Errorf("one rung produced %v", got)
	}
}

// ---------- codec strings over renditions the check must skip ----------

func TestCheckCodecString_SkipsWhatItCannotJudge(t *testing.T) {
	// A rendition with no CODECS attribute at all.
	rd := rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))
	if got := checkCodecString([]*renditionData{rd}); len(got) != 0 {
		t.Errorf("a rendition with no CODECS produced %v", got)
	}

	// A video rendition whose media states no profile.
	rd.r.Codecs = "avc1.640028"
	if got := checkCodecString([]*renditionData{rd}); len(got) != 0 {
		t.Errorf("media stating no profile produced %v", got)
	}

	// An audio-only rendition: the video half must not fire, and the audio half
	// has nothing to compare against either.
	audioRung := rend("audio", withSegs(okSeg(0, media.ContainerMP4, audioTrack())))
	audioRung.r.Kind = manifest.Audio
	audioRung.r.Codecs = "mp4a.40.2"
	got := checkCodecString([]*renditionData{audioRung})
	if len(got) != 1 || !strings.Contains(got[0].Message, "no configuration") {
		t.Errorf("an audio rendition stating no configuration produced %v", got)
	}
}

// ---------- the sampling passes, and what they do when a fetch fails ----------

// sampleIFrames, samplePartsAll and probeDVR each fetch on their own account, and
// each has to survive an origin that answers with nothing useful. An HTTP fixture
// is the only way to reach those branches, because the failure is in the fetch
// rather than in the media.
func TestSamplingPasses_OriginFailures(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/iframe.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		// Two entries as byte ranges of one file, with an init that 404s: the
		// range path and the init-failure path in one fixture.
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n" +
			"#EXT-X-MAP:URI=\"missing-init.mp4\"\n" +
			"#EXT-X-BYTERANGE:200@0\n#EXTINF:2.0,\nall.m4s\n" +
			"#EXT-X-BYTERANGE:200\n#EXTINF:2.0,\nall.m4s\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/gone.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	opts := Defaults()
	opts.Segments = 2

	// --iframes 0 means none, and it has to mean none rather than fewer.
	off := opts
	off.MaxIFrame = 0
	pl := manifest.Playlist{Kind: manifest.KindHLS, Master: true, Renditions: []manifest.Rendition{
		{Name: "iframe", Kind: manifest.IFrame, URI: srv.URL + "/iframe.m3u8"},
	}}
	if got := sampleIFrames(context.Background(), client, pl, off); got != nil {
		t.Errorf("--iframes 0 sampled %d rungs", len(got))
	}

	// A ladder with no trick-play rung at all.
	if got := sampleIFrames(context.Background(), client, manifest.Playlist{Master: true}, opts); got != nil {
		t.Errorf("a ladder with no trick-play rung produced %v", got)
	}

	// A trick-play playlist that 404s.
	gone := manifest.Playlist{Kind: manifest.KindHLS, Master: true, Renditions: []manifest.Rendition{
		{Name: "iframe", Kind: manifest.IFrame, URI: srv.URL + "/gone.m3u8"},
	}}
	got := sampleIFrames(context.Background(), client, gone, opts)
	if len(got) != 1 || got[0].err == nil {
		t.Errorf("an unloadable trick-play playlist produced %v", got)
	}

	// One that loads, whose init and whose ranges all 404. Zero concurrency has
	// to still sample: a zero would mean no worker ever starts.
	zero := opts
	zero.Concurrency = 0
	got = sampleIFrames(context.Background(), client, pl, zero)
	if len(got) != 1 {
		t.Fatalf("sampled %d rungs, want 1", len(got))
	}
	if got[0].initErr == nil {
		t.Error("an initialisation segment that 404s was not recorded")
	}
	for _, sd := range got[0].segs {
		if sd.fetchErr == nil {
			t.Error("a range that 404s was not recorded")
		}
	}

	// samplePartsAll over a part that 404s, and one whose body is not media.
	rd := &renditionData{r: manifest.Rendition{Name: "720p", Kind: manifest.Video}}
	rd.parts = []partData{
		{part: manifest.Part{URI: srv.URL + "/p0.m4s", Sequence: 0, Index: 0}},
		{part: manifest.Part{URI: srv.URL + "/p1.m4s", Sequence: 0, Index: 1,
			ByteRange: &manifest.ByteRange{Length: 100, Offset: 0}}},
	}
	samplePartsAll(context.Background(), client, []*renditionData{rd}, nil, 0)
	for i, pd := range rd.parts {
		if pd.fetchErr == nil && pd.parseErr == nil {
			t.Errorf("part %d neither failed to fetch nor failed to parse", i)
		}
	}

	// probeDVR against an oldest segment that 404s, and one whose body will not
	// parse. Both fail a scrub, and they are fixed differently.
	oldest := manifest.Segment{URI: srv.URL + "/oldest.m4s", Duration: 2}
	live := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video}, window: 60, oldest: &oldest,
	}
	probe := probeDVR(context.Background(), client, manifest.Playlist{Live: true},
		[]*renditionData{live}, nil)
	if probe == nil || probe.fetchErr == nil {
		t.Errorf("a DVR probe against a 404 produced %+v", probe)
	}

	// A VOD playlist is never probed.
	if got := probeDVR(context.Background(), client, manifest.Playlist{}, []*renditionData{live}, nil); got != nil {
		t.Errorf("a VOD playlist was probed for a DVR window: %+v", got)
	}

	// A rendition that states no window, and one already sampled: neither costs a
	// request.
	noWindow := &renditionData{r: manifest.Rendition{Name: "720p", Kind: manifest.Video}}
	if got := probeDVR(context.Background(), client, manifest.Playlist{Live: true},
		[]*renditionData{noWindow}, nil); got != nil {
		t.Errorf("a rendition with no window was probed: %+v", got)
	}
	sampled := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video}, window: 60, oldest: &oldest,
		segs: []segmentData{{seg: oldest, parsed: true}},
	}
	probe = probeDVR(context.Background(), client, manifest.Playlist{Live: true},
		[]*renditionData{sampled}, nil)
	if probe == nil || !probe.parsed {
		t.Errorf("an already-sampled oldest segment was re-fetched instead of reused: %+v", probe)
	}
}

// A trick-play entry that arrives as a byte range and does not parse, plus a
// timeline comparison against a rendition that is not video: the last two
// branches of the trick-play check.
func TestIFrameTimelineFindings_PrefersItsOwnResolution(t *testing.T) {
	iframeRend := manifest.Rendition{Name: "iframe", Kind: manifest.IFrame, Width: 1280, Height: 720}
	id := &iframeData{r: iframeRend, segs: []segmentData{okSeg(0, media.ContainerMP4, syncTrack(true))}}

	// An audio rendition is not a timeline to compare a video rung against.
	audioRung := rend("audio", withSegs(okSeg(0, media.ContainerMP4, audioTrack())))
	audioRung.r.Kind = manifest.Audio
	if got := iframeTimelineFindings(id, "iframe", []*renditionData{audioRung}, Defaults()); got != nil {
		t.Errorf("an audio rendition was used as the video timeline: %v", got)
	}

	// Two video rungs: the one whose resolution the trick-play rung claims wins.
	other := rend("2160p", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))
	other.r.Width, other.r.Height = 3840, 2160
	match := rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))
	match.r.Width, match.r.Height = 1280, 720
	got := iframeTimelineFindings(id, "iframe", []*renditionData{other, match}, Defaults())
	if len(got) != 1 || !strings.Contains(got[0].Message, "720p") {
		t.Errorf("the matching rung was not preferred: %v", got)
	}
}

// pick keeps both ends of a ladder from two upwards and never repeats an index,
// including when the cap is larger than the ladder.
func TestPick_Extremes(t *testing.T) {
	ladder := []manifest.Rendition{
		{Name: "a", Bandwidth: 1}, {Name: "b", Bandwidth: 2},
		{Name: "c", Bandwidth: 3}, {Name: "d", Bandwidth: 4}, {Name: "e", Bandwidth: 5},
	}
	got := pick(ladder, 3)
	if len(got) != 3 || got[0].Name != "a" || got[2].Name != "e" {
		t.Errorf("pick(3) = %v, want both ends and a spread", names(got))
	}
	// A cap of exactly the ladder size returns it whole and sorted.
	if got := pick(ladder, 5); len(got) != 5 {
		t.Errorf("pick(5) over five rungs returned %d", len(got))
	}
	// A cap larger than the ladder, and a cap of zero meaning all.
	if got := pick(ladder, 0); len(got) != 5 {
		t.Errorf("pick(0) returned %d, want all five", len(got))
	}
}

// ---------- the remaining verdict branches ----------

// The codec-string check reports the two directions differently, and the
// below-the-media one is the BAD: a device reads the manifest, decides it cannot
// decode this, and never asks for a segment.
func TestCheckCodecString_DeclaredBelowAndFamilyMismatch(t *testing.T) {
	// Profile and level both declared below what the media codes.
	high := videoTrack()
	high.Profile = media.CodecProfile{Profile: 100, Level: 51, Stated: true}
	rd := rend("720p", withSegs(okSeg(0, media.ContainerMP4, high)))
	rd.r.Codecs = "avc1.4d001e" // Main profile, level 3.0
	got := checkCodecString([]*renditionData{rd})
	var profileBad, levelBad bool
	for _, f := range got {
		if strings.Contains(f.Message, "declares profile") && f.Status == finding.BAD {
			profileBad = true
		}
		if strings.Contains(f.Message, "declares level") && f.Status == finding.BAD {
			levelBad = true
		}
	}
	if !profileBad || !levelBad {
		t.Errorf("a profile and level below the media produced %v", got)
	}

	// A tier mismatch: the high tier raises the ceiling a level allows.
	hev := videoTrack()
	hev.Codec = "hevc"
	hev.Profile = media.CodecProfile{Profile: 2, Level: 153, Tier: 0, Stated: true}
	rd = rend("2160p", withSegs(okSeg(0, media.ContainerMP4, hev)))
	rd.r.Codecs = "hvc1.2.4.H153.B0"
	var tierSaid bool
	for _, f := range checkCodecString([]*renditionData{rd}) {
		if strings.Contains(f.Message, "tier") {
			tierSaid = true
		}
	}
	if !tierSaid {
		t.Error("a tier the media does not code was not reported")
	}

	// ec-3 declared over an ac-3 track: a different decoder, not a different
	// configuration of one.
	ac3 := audioTrack()
	ac3.Codec = "ac3"
	audioRung := rend("audio", withSegs(okSeg(0, media.ContainerMP4, ac3)))
	audioRung.r.Kind = manifest.Audio
	audioRung.r.Codecs = "ec-3"
	got = checkAudioCodecString([]*renditionData{audioRung})
	if len(got) != 1 || got[0].Status != finding.BAD || !strings.Contains(got[0].Message, "ac3") {
		t.Errorf("ec-3 over an ac-3 track produced %v", got)
	}
}

// DASH states the transfer characteristic as a code point, so the comparison is
// number against number with no mapping in between.
func TestCheckVideoRange_DASHCodePoint(t *testing.T) {
	pq := videoTrack()
	pq.ColourDesc = media.ColourDescription{Primaries: 9, Transfer: 16, Matrix: 9, Stated: true, RangeStated: true}
	rd := rend("2160p", withSegs(okSeg(0, media.ContainerMP4, pq)))
	rd.r.Transfer = 18 // the manifest says HLG, the media codes PQ

	got := checkVideoRange([]*renditionData{rd})
	if len(got) != 1 || got[0].Status != finding.BAD {
		t.Fatalf("a declared code point the media does not carry produced %v", got)
	}
	if !strings.Contains(got[0].Message, "18") || !strings.Contains(got[0].Message, "16") {
		t.Errorf("the finding does not quote both code points: %q", got[0].Message)
	}

	// An audio rendition is never asked about dynamic range.
	audioRung := rend("audio", withSegs(okSeg(0, media.ContainerMP4, audioTrack())))
	audioRung.r.Kind = manifest.Audio
	audioRung.r.VideoRange = "PQ"
	if got := checkVideoRange([]*renditionData{audioRung}); len(got) != 0 {
		t.Errorf("an audio rendition was asked about VIDEO-RANGE: %v", got)
	}
}

// A manifest that declares protection over media no segment of which parsed is a
// hole in the coverage, not a verdict: without the init there are no pssh boxes
// to compare against.
func TestCheckDRM_NothingParsed(t *testing.T) {
	rd := rend("720p", withSegs(segmentData{seg: manifest.Segment{Sequence: 0}}))
	rd.r.DRMSystems = []string{"edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"}
	got := checkDRM([]*renditionData{rd})
	if len(got) != 1 || got[0].Status != finding.ERROR {
		t.Fatalf("a declared system over unreadable media produced %v", got)
	}
	if !strings.Contains(got[0].Message, "no initialisation segment") {
		t.Errorf("the finding does not say why it could not check: %q", got[0].Message)
	}
}

// A trick-play range whose walk completed and found no random access point at all
// is the unambiguous failure, and it is what a grey scrub preview looks like.
func TestIFrameKeyframeFindings_NoRandomAccessPointAtAll(t *testing.T) {
	none := walkedTrack(false, false, true)
	none.Samples = 1
	id := &iframeData{
		r:    manifest.Rendition{Name: "iframe", Kind: manifest.IFrame},
		segs: []segmentData{okSeg(0, media.ContainerMP4, none)},
	}
	got := iframeKeyframeFindings(id, "iframe")
	if len(got) != 1 || got[0].Status != finding.BAD || !strings.Contains(got[0].Message, "do not resolve") {
		t.Errorf("a range with no random access point produced %v", got)
	}
}

// The watch loop's own arithmetic: an interval longer than the window is clamped
// to it, the last wait is trimmed so the loop never overruns, and a cancelled
// wait reports on what was seen rather than nothing.
func TestWatchLiveEdge_IntervalArithmetic(t *testing.T) {
	mux := http.NewServeMux()
	var polls int
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		polls++
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:30\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXTINF:30.0,\nseg%d.ts\n", polls, polls)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clock := time.Unix(0, 0)
	opts := Defaults()
	// A window shorter than the playlist's own re-read interval: the interval is
	// clamped to the window, so the loop still looks twice.
	opts.Watch = 5 * time.Second
	opts.Now = func() time.Time { return clock }
	opts.Sleep = func(_ context.Context, d time.Duration) error {
		clock = clock.Add(d)
		return nil
	}
	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	got := watchLiveEdge(context.Background(), client, srv.URL+"/live.m3u8",
		manifest.Playlist{Kind: manifest.KindHLS, Live: true, TargetDuration: 30}, opts)
	if len(got) == 0 {
		t.Fatal("a clamped interval produced no findings at all")
	}
	if polls < 2 {
		t.Errorf("polled %d times, want at least two looks", polls)
	}

	// A wait that is cancelled: the loop stops and reports on what it saw.
	clock = time.Unix(0, 0)
	opts.Watch = time.Hour
	opts.Sleep = func(_ context.Context, d time.Duration) error {
		clock = clock.Add(d)
		return context.Canceled
	}
	got = watchLiveEdge(context.Background(), client, srv.URL+"/live.m3u8",
		manifest.Playlist{Kind: manifest.KindHLS, Live: true, TargetDuration: 30}, opts)
	if len(got) == 0 {
		t.Error("a cancelled watch produced no findings")
	}
}

// The trailing gap counts: a stream that advanced early and then froze for the
// last half of the window is broken now, which is the half an operator cares
// about.
func TestEdgeFindings_TrailingGap(t *testing.T) {
	opts := Defaults()
	opts.Watch = 40 * time.Second
	points := []edgePoint{
		{at: time.Unix(0, 0), newest: "a", target: 2},
		{at: time.Unix(2, 0), newest: "b", target: 2},
		{at: time.Unix(40, 0), newest: "b", target: 2},
	}
	got := edgeFindings("720p", points, 2*time.Second, true, opts)
	if len(got) != 1 || got[0].Status != finding.BAD || !strings.Contains(got[0].Message, "stalled") {
		t.Errorf("a stall in the tail of the window produced %v", got)
	}
}

// A body that fetches and is not media reaches the parse branch of each of the
// three sampling passes, which is a different finding from a fetch that failed.
func TestSamplingPasses_BodiesThatAreNotMedia(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/iframe.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n" +
			"#EXTINF:2.0,\nif0.m4s\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not media at all</html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	opts := Defaults()
	opts.Segments = 1

	pl := manifest.Playlist{Kind: manifest.KindHLS, Master: true, Renditions: []manifest.Rendition{
		{Name: "iframe", Kind: manifest.IFrame, URI: srv.URL + "/iframe.m3u8"},
	}}
	ifs := sampleIFrames(context.Background(), client, pl, opts)
	if len(ifs) != 1 || len(ifs[0].segs) != 1 || ifs[0].segs[0].parseErr == nil {
		t.Errorf("a trick-play entry that is not media produced %+v", ifs)
	}

	rd := &renditionData{r: manifest.Rendition{Name: "720p", Kind: manifest.Video}}
	rd.parts = []partData{{part: manifest.Part{URI: srv.URL + "/p0.m4s"}}}
	samplePartsAll(context.Background(), client, []*renditionData{rd}, nil, 2)
	if rd.parts[0].parseErr == nil {
		t.Errorf("a part that is not media produced %+v", rd.parts[0])
	}

	oldest := manifest.Segment{URI: srv.URL + "/oldest.m4s", Duration: 2}
	live := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video}, window: 60, oldest: &oldest,
	}
	probe := probeDVR(context.Background(), client, manifest.Playlist{Live: true},
		[]*renditionData{live}, nil)
	if probe == nil || probe.parseErr == nil {
		t.Errorf("a DVR probe against a body that is not media produced %+v", probe)
	}
}

// ---------- the last guards ----------

// Each of these is one branch, and each decides between a measurement and
// silence. They are grouped because each needs a rendition shaped slightly
// differently and none needs an origin.
func TestLastGuards(t *testing.T) {
	timeless := videoTrack()
	timeless.HasPTS = false
	noDur := videoTrack()
	noDur.FrameDur, noDur.MaxPTS, noDur.MinPTS = 0, 0, 0

	// measuredBitrates over a segment whose track states no duration.
	rd := rend("720p", withSegs(okSeg(0, media.ContainerMP4, noDur)))
	if _, _, _, ok := measuredBitrates(rd); ok {
		t.Error("measuredBitrates answered from a track with no duration")
	}

	// appleSegmentDuration over durations whose median is zero.
	ctx := profileContext{rends: []*renditionData{
		rend("720p", withSegs(okSeg(0, media.ContainerMP4, noDur), okSeg(1, media.ContainerMP4, noDur))),
	}, opts: Defaults()}
	if got := appleSegmentDuration(ctx); len(got) != 0 {
		t.Errorf("segments of no length produced %v", got)
	}

	// The ladder comparison's ordering, with the shorter rung second.
	long := videoTrack()
	long.MaxPTS = 720000
	ctx = profileContext{rends: []*renditionData{
		rend("1080p", withSegs(okSeg(0, media.ContainerMP4, long), okSeg(1, media.ContainerMP4, long))),
		rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack()), okSeg(1, media.ContainerMP4, videoTrack()))),
	}, opts: Defaults()}
	var said bool
	for _, f := range appleSegmentDuration(ctx) {
		if strings.Contains(f.Message, "different segment lengths") {
			said = true
		}
	}
	if !said {
		t.Error("a ladder whose shorter rung comes second was not reported")
	}

	// appleIDRPerSegment over an encrypted bitstream: nobody looked.
	opaque := okSeg(0, media.ContainerTS, syncTrack(false))
	opaque.seg.KeyMethod = "SAMPLE-AES"
	opaque.info.Tracks[0].Encrypted = true
	ctx = profileContext{rends: []*renditionData{rend("720p", withSegs(opaque))}, opts: Defaults()}
	for _, f := range appleIDRPerSegment(ctx) {
		if f.Status != finding.OK {
			t.Errorf("an encrypted bitstream produced %s: %s", f.Status, f.Message)
		}
	}

	// colourOf over a segment whose video track states no colour.
	if _, ok := colourOf(rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))); ok {
		t.Error("colourOf answered from a track stating no colour")
	}
	// codecProfileOf over one stating no profile.
	if _, ok := codecProfileOf(rend("720p", withSegs(okSeg(0, media.ContainerMP4, videoTrack())))); ok {
		t.Error("codecProfileOf answered from a track stating no profile")
	}
	// clearLeadSeconds over an unparsed segment beside a parsed one.
	mixed := rend("720p")
	mixed.segs = []segmentData{{}, okSeg(1, media.ContainerMP4, videoTrack())}
	if _, ok := clearLeadSeconds(mixed, 4); !ok {
		t.Error("clearLeadSeconds skipped past an unparsed segment and gave up")
	}
	// claimsProtection over an unparsed segment: nothing says protected.
	if claimsProtection(&renditionData{segs: []segmentData{{}}}) {
		t.Error("an unparsed segment was read as protected")
	}

	// pdtLadderFindings where one rung has a point at an index the other lacks.
	a, b := rend("720p"), rend("1080p")
	byRendition := map[string][]pdtPoint{
		"720p":  {{sd: segmentData{seg: manifest.Segment{Sequence: 0}}, at: time.Unix(0, 0)}},
		"1080p": {{sd: segmentData{seg: manifest.Segment{Sequence: 9}}, at: time.Unix(0, 0)}},
	}
	if got := pdtLadderFindings([]*renditionData{a, b}, byRendition, []string{"720p", "1080p"}, 0.1); got != nil {
		t.Errorf("rungs sharing no segment index produced %v", got)
	}

	// A part whose track states no duration, and a segment likewise.
	pr := rend("720p")
	pr.hasParts = true
	pr.parts = []partData{{
		part: manifest.Part{URI: "p0.m4s", Sequence: 0, Index: 0}, parsed: true,
		info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{noDur}},
	}}
	if got := partFindings(pr, "720p", 0.1, Defaults()); len(got) != 1 ||
		!strings.Contains(got[0].Message, "states a timeline") {
		t.Errorf("a part with no duration produced %v", got)
	}
	// A readable part whose segment states no duration.
	pr.parts = []partData{{
		part: manifest.Part{URI: "p0.m4s", Sequence: 0, Index: 0}, parsed: true,
		info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{videoTrack()}},
	}}
	pr.segs = []segmentData{{
		seg: manifest.Segment{Sequence: 0}, parsed: true,
		info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{noDur}},
	}}
	for _, f := range partFindings(pr, "720p", 0.1, Defaults()) {
		if f.Status == finding.BAD {
			t.Errorf("a segment with no duration was compared against: %s", f.Message)
		}
	}

	// iframeKeyframeFindings over a track stating nothing, and over one that opens
	// on a keyframe by a walk rather than an assertion.
	silent := videoTrack()
	silent.Samples = 1 // one picture, so the multi-picture rule stays out of it
	id := &iframeData{
		r:    manifest.Rendition{Name: "iframe", Kind: manifest.IFrame},
		segs: []segmentData{okSeg(0, media.ContainerMP4, silent)},
	}
	if got := iframeKeyframeFindings(id, "iframe"); len(got) != 1 ||
		!strings.Contains(got[0].Message, "could not read a picture") {
		t.Errorf("a track stating nothing produced %v", got)
	}
	walked := walkedTrack(true, true, true)
	walked.Samples = 1
	id.segs = []segmentData{okSeg(0, media.ContainerMP4, walked)}
	if got := iframeKeyframeFindings(id, "iframe"); len(got) != 1 || got[0].Status != finding.OK {
		t.Errorf("a range whose walk found a keyframe first produced %v", got)
	}

	// The codec-string WARN for a declared object type the media does not code,
	// where neither side signals HE-AAC.
	aac := audioTrack()
	aac.AudioCfg = media.AudioConfig{ObjectType: 2, CodedChannels: 2, CodedSampleRate: 48000, Stated: true}
	audioRung := rend("audio", withSegs(okSeg(0, media.ContainerMP4, aac)))
	audioRung.r.Kind = manifest.Audio
	audioRung.r.Codecs = "mp4a.40.23" // AAC-LD: neither the coding nor HE-AAC
	got := checkAudioCodecString([]*renditionData{audioRung})
	if len(got) != 1 || got[0].Status != finding.WARN {
		t.Errorf("an object type neither side signals produced %v", got)
	}

	// pick's clamp and its refusal to take one index twice, on a two-rung ladder
	// sampled at three.
	two := []manifest.Rendition{{Name: "a", Bandwidth: 1}, {Name: "b", Bandwidth: 2}}
	if got := pick(two, 3); len(got) != 2 {
		t.Errorf("pick(3) over two rungs returned %d", len(got))
	}
}

// The DVR probe fetches a byte range when the oldest segment is one, which is how
// Apple's own streams address their media.
func TestProbeDVR_ByteRange(t *testing.T) {
	body := make([]byte, 4096)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Error("the probe did not send a Range header for a byte-range segment")
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[:100])
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldest := manifest.Segment{
		URI: srv.URL + "/all.m4s", Duration: 2,
		ByteRange: &manifest.ByteRange{Length: 100, Offset: 0},
	}
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video}, window: 60, oldest: &oldest,
	}
	probe := probeDVR(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}),
		manifest.Playlist{Live: true}, []*renditionData{rd}, nil)
	if probe == nil {
		t.Fatal("a byte-range oldest segment was not probed")
	}
}

// Two trick-play rungs sharing one initialisation segment fetch it once.
func TestSampleIFrames_SharedInit(t *testing.T) {
	var initFetches int
	mux := http.NewServeMux()
	playlist := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n" +
			"#EXT-X-MAP:URI=\"/init.mp4\"\n#EXTINF:2.0,\n/if0.m4s\n#EXT-X-ENDLIST\n"))
	}
	mux.HandleFunc("/a.m3u8", playlist)
	mux.HandleFunc("/b.m3u8", playlist)
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		initFetches++
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Init(1, 90000, "video", 1280, 720))
	})
	mux.HandleFunc("/if0.m4s", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4SegmentSync(1, 0, 0, 3600, 1, 4000, true))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := Defaults()
	opts.MaxIFrame = 2
	opts.Segments = 1
	pl := manifest.Playlist{Kind: manifest.KindHLS, Master: true, Renditions: []manifest.Rendition{
		{Name: "a", Kind: manifest.IFrame, Bandwidth: 1, URI: srv.URL + "/a.m3u8"},
		{Name: "b", Kind: manifest.IFrame, Bandwidth: 2, URI: srv.URL + "/b.m3u8"},
	}}
	got := sampleIFrames(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), pl, opts)
	if len(got) != 2 {
		t.Fatalf("sampled %d rungs, want 2", len(got))
	}
	if initFetches != 1 {
		t.Errorf("the shared initialisation segment was fetched %d times, want once", initFetches)
	}
}

// trackless is a segment that fetched and parsed and describes no track at all.
// Five separate checks reach for a track and have to stay quiet when there is
// none, and no HTTP fixture produces this: a container that parses always yields
// something.
func trackless(seq int) segmentData {
	sd := okSeg(seq, media.ContainerMP4)
	sd.info.Tracks = nil
	return sd
}

func TestChecksOverASegmentWithNoTracks(t *testing.T) {
	rd := rend("720p", withSegs(trackless(0), trackless(1)))

	if _, ok := colourOf(rd); ok {
		t.Error("colourOf answered from a segment with no tracks")
	}
	if _, ok := codecProfileOf(rd); ok {
		t.Error("codecProfileOf answered from a segment with no tracks")
	}
	if _, _, _, known := sampleEncryptionOf(rd); known {
		t.Error("sampleEncryptionOf answered from a segment with no tracks")
	}

	ctx := profileContext{rends: []*renditionData{rd}, opts: Defaults()}
	if got := appleBitrateTier(ctx); len(got) != 0 {
		t.Errorf("appleBitrateTier produced %v over a segment with no tracks", got)
	}
	if got := appleFrameRate(ctx); len(got) != 0 {
		t.Errorf("appleFrameRate produced %v over a segment with no tracks", got)
	}

	// And the parts check, whose parts and whose segment both describe no track.
	pr := rend("720p")
	pr.hasParts = true
	pr.parts = []partData{{
		part: manifest.Part{URI: "p0.m4s", Sequence: 0, Index: 0}, parsed: true,
		info: media.SegmentInfo{Container: media.ContainerMP4},
	}}
	if got := partFindings(pr, "720p", 0.1, Defaults()); len(got) != 1 ||
		!strings.Contains(got[0].Message, "states a timeline") {
		t.Errorf("a part with no track produced %v", got)
	}
}

// The watch loop skips renditions with no live edge to read: one segcheck could
// not expand at all, and one that is a single file with its index inside it.
func TestPollEdges_SkipsRenditionsWithNoEdge(t *testing.T) {
	mux := http.NewServeMux()
	// Named .mpd: Detect classifies by extension first, so a DASH body behind an
	// .m3u8 path would be read as HLS.
	mux.HandleFunc("/master.mpd", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprint(w, `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic" availabilityStartTime="2026-08-10T12:00:00Z">
  <Period><AdaptationSet mimeType="video/mp4">
    <Representation id="sb" bandwidth="800000" width="640" height="360"><SegmentBase indexRange="0-800"/></Representation>
  </AdaptationSet></Period>
</MPD>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := Defaults()
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 1, 40, 0, time.UTC) }
	obs := pollEdges(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}),
		srv.URL+"/master.mpd", opts)
	if obs.err != nil {
		t.Fatalf("the manifest itself failed: %v", obs.err)
	}
	if len(obs.edges) != 0 {
		t.Errorf("a single-file representation was polled for a live edge: %v", obs.edges)
	}
}

// A window whose polls all show an empty playlist except one is not an empty
// window, and the check has to notice the one.
func TestEdgeFindings_OneNonEmptyPollIsNotAnEmptyWindow(t *testing.T) {
	opts := Defaults()
	opts.Watch = 20 * time.Second
	points := []edgePoint{
		{at: time.Unix(0, 0), newest: "", target: 2},
		{at: time.Unix(20, 0), newest: "a", target: 2},
	}
	got := edgeFindings("720p", points, 2*time.Second, true, opts)
	for _, f := range got {
		if strings.Contains(f.Message, "no segments at any point") {
			t.Errorf("a window with one non-empty poll was called empty: %s", f.Message)
		}
	}
}

// The final wait of a watch is trimmed to what is left of the window, so the loop
// never overruns the time it was given.
func TestWatchLiveEdge_TrimsTheLastWait(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:3.0,\nseg0.ts\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clock := time.Unix(0, 0)
	var waits []time.Duration
	opts := Defaults()
	// Five seconds at a three-second interval: one full wait, then a two-second
	// remainder.
	opts.Watch = 5 * time.Second
	opts.Now = func() time.Time { return clock }
	opts.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		clock = clock.Add(d)
		return nil
	}
	watchLiveEdge(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}),
		srv.URL+"/live.m3u8",
		manifest.Playlist{Kind: manifest.KindHLS, Live: true, TargetDuration: 3}, opts)
	var total time.Duration
	for _, w := range waits {
		total += w
	}
	if total > opts.Watch {
		t.Errorf("the waits summed to %s, past the %s window", total, opts.Watch)
	}
	if len(waits) < 2 || waits[len(waits)-1] >= 3*time.Second {
		t.Errorf("waits = %v, want a trimmed final one", waits)
	}
}

// pick never takes one index twice, however the spread rounds.
func TestPick_NeverRepeatsAnIndex(t *testing.T) {
	for size := 1; size <= 12; size++ {
		ladder := make([]manifest.Rendition, size)
		for i := range ladder {
			ladder[i] = manifest.Rendition{Name: string(rune('a' + i)), Bandwidth: i + 1}
		}
		for max := 1; max <= size+2; max++ {
			got := pick(ladder, max)
			seen := map[string]bool{}
			for _, r := range got {
				if seen[r.Name] {
					t.Fatalf("pick(%d) over %d rungs repeated %s", max, size, r.Name)
				}
				seen[r.Name] = true
			}
			if len(got) > size {
				t.Fatalf("pick(%d) over %d rungs returned %d", max, size, len(got))
			}
		}
	}
}
