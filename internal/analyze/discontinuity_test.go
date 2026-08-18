package analyze

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// EXT-X-DISCONTINUITY is an instruction to throw a decoder away and start
// again, and it costs whatever a decoder reset costs on the device: a flush, a
// re-initialisation, a visible hitch. The `continuity` check reads it as a
// licence — a timeline jump here is expected rather than a defect — and takes
// the declaration on trust in the other direction, which is the half nobody
// looks at. A tag with nothing behind it is a reset the player performs for no
// reason, and on a packager that emits one per segment it is a hitch per
// segment on a stream whose media is perfectly continuous.

// discSegments builds count clean 2s segments and then plants a real timeline
// reset before segment `at`: everything from there on jumps ten seconds
// forward, which is what a splice into other content looks like.
func discSegments(count, at int, codedW, codedH int) []segSpec {
	segs := cleanSegments(count, codedW, codedH)
	for i := at; i < count; i++ {
		segs[i].startPTS += 10 * 90000
	}
	return segs
}

// A tag with a real reset behind it is the stream working as intended.
func TestRun_AnHonouredDiscontinuityIsNotADefect(t *testing.T) {
	segs := discSegments(4, 2, 1280, 720)
	segs[2].discontinuity = true
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	for _, f := range res.Findings {
		if f.Check == "discontinuity" && f.Status != finding.OK {
			t.Errorf("an honoured discontinuity produced %s: %s", f.Status, f.Message)
		}
	}
	if !hasCheck(res, "discontinuity") {
		t.Errorf("no discontinuity finding at all: the check did not run:\n%s", dump(res))
	}
}

// A tag with nothing behind it: the timeline runs straight through it and the
// media on either side is the same shape.
func TestRun_FindsADiscontinuityThatDiscontinuesNothing(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[2].discontinuity = true
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "discontinuity", finding.BAD)
	if !ok {
		t.Fatalf("a discontinuity with nothing behind it was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Target, "seg") {
		t.Errorf("the finding does not name the segment carrying the tag: %q", f.Target)
	}
}

// RFC 8216 makes EXT-X-DISCONTINUITY signal a change of encoding as well as of
// timestamps: file format, track layout, codec. A tag over a continuous
// timeline where the media really did change shape is doing its job, and
// calling it spurious would report a correct stream.
func TestRun_ADiscontinuityOverAShapeChangeIsNotSpurious(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[2].discontinuity = true
	for i := 2; i < 4; i++ {
		segs[i].codedWidth, segs[i].codedHeight = 640, 360
	}
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	for _, f := range res.Findings {
		if f.Check == "discontinuity" && f.Status != finding.OK {
			t.Errorf("a discontinuity over a real change of encoding produced %s: %s", f.Status, f.Message)
		}
	}
}

// Which timeline a segment sits on is EXT-X-DISCONTINUITY-SEQUENCE plus every
// tag before it, and two rungs carrying the same media at different numbers put
// it on two different timelines. A player switching between them at that point
// places the new segment somewhere the old one never was.
func TestRun_FindsRungsThatDisagreeAboutTheDiscontinuitySequence(t *testing.T) {
	segs := discSegments(4, 2, 1280, 720)
	segs[2].discontinuity = true
	low := discSegments(4, 2, 640, 360)
	low[2].discontinuity = true
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs},
		// The same media, and a playlist that lost count of what rolled out of
		// the window before it.
		{name: "360p", bandwidth: syntheticBandwidth / 4, width: 640, height: 360,
			discontinuitySequence: 3, segments: low},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "discontinuity", finding.BAD)
	if !ok {
		t.Fatalf("two rungs on different timelines were not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "timeline") {
		t.Errorf("the finding does not say what disagrees: %q", f.Message)
	}
}

// A stream with no discontinuities at all must gain no row.
func TestRun_NoDiscontinuityMeansNoDiscontinuityFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "discontinuity") {
		t.Errorf("a stream with no discontinuities produced a discontinuity finding:\n%s", dump(res))
	}
}
