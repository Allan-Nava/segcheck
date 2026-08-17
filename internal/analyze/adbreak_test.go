package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The defect SC-20 exists for: a break is signalled, and no player can cut to it
// because it does not fall on a segment boundary. The ad server is triggered and
// the transition lands mid-picture, or the switch never happens at all — and the
// manifest describes it perfectly either way, so nothing that reads only the
// manifest can tell.

func TestRun_SpliceMidSegmentIsReported(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	// A splice one second into the third segment, which begins at 4s.
	segs[2].splices = []mediatest.SpliceSpec{{
		Command: mediatest.SpliceInsert, PTS: 5 * 90000, OutOfNetwork: true, EventID: 1,
	}}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "adbreak", finding.BAD)
	if !ok {
		t.Fatalf("a splice point one second into a segment was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "1.000s") {
		t.Errorf("finding does not say how far off the boundary it is: %q", f.Message)
	}
}

// A splice exactly on a boundary is the healthy case, and must stay quiet.
func TestRun_SpliceOnABoundaryIsFine(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[2].splices = []mediatest.SpliceSpec{{
		Command: mediatest.SpliceInsert, PTS: 4 * 90000, OutOfNetwork: true, EventID: 1,
	}}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("a splice on a boundary produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	f, ok := findFinding(res, "adbreak", finding.OK)
	if !ok {
		t.Fatalf("no adbreak finding at all: the check did not run.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "1 splice") {
		t.Errorf("finding does not report what was found: %q", f.Message)
	}
}

// A splice_immediate states no time. There is nothing to compare it to, and a
// check that read its zero value as "at the start" would call every one of them
// perfectly aligned — the wrong answer, arrived at confidently.
func TestRun_SpliceWithNoTimeIsNotJudged(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[2].splices = []mediatest.SpliceSpec{{
		Command: mediatest.SpliceInsert, NoPTS: true, OutOfNetwork: true, EventID: 1,
	}}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	if f, ok := findFinding(res, "adbreak", finding.BAD); ok {
		t.Errorf("a splice with no time was judged against a boundary: %q", f.Message)
	}
	f, ok := findFinding(res, "adbreak", finding.OK)
	if !ok {
		t.Fatalf("the signal was not reported at all.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "states no time") {
		t.Errorf("finding does not say why it was not verified: %q", f.Message)
	}
}

// The signalling is entirely absent: nothing declared, nothing inband. There is
// nothing to say, and a finding per rendition would be noise on every stream that
// carries no advertising.
func TestRun_NoAdSignallingIsSilent(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		segments: cleanSegments(4, 1280, 720),
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "adbreak") {
		t.Errorf("a stream with no ad signalling produced an adbreak finding.\n%s", dump(res))
	}
}

// An EXT-X-CUE-OUT and its CUE-IN sit between segments, so the break is on a
// boundary by construction. The media carrying no SCTE-35 of its own is normal for
// a packager that translated the signal, so reporting the pair is right and calling
// it a defect is not.
func TestRun_CueOutPairOnBoundariesIsReported(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		segments: segs, cueOutBefore: 2,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "adbreak", finding.OK)
	if !ok {
		t.Fatalf("a declared cue-out produced no finding.\n%s", dump(res))
	}
	// The out and the in are both declarations, and both land on boundaries.
	if !strings.Contains(f.Message, "2 declared") {
		t.Errorf("finding does not report the declaration: %q", f.Message)
	}
	for _, g := range res.Findings {
		if g.Check == "adbreak" && g.Status != finding.OK {
			t.Errorf("a cue-out on a boundary produced %s: %s", g.Status, g.Message)
		}
	}
}

// ---------- placing a break on the media timeline ----------

// The boundaries come from the media, not the manifest: a video track if there is
// one, an audio track otherwise, and nothing at all when the segments state no
// timeline — in which case every comparison here would be against a number nobody
// measured.
func TestSegmentBoundaries(t *testing.T) {
	vid := func(start int64, dur int64) segmentData {
		return segmentData{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{{
			Kind: media.Video, Timescale: 90000, HasPTS: true,
			MinPTS: start, MaxPTS: start + dur - 3600, FrameDur: 3600, Samples: 50,
		}}}}
	}
	bounds, ok := segmentBoundaries([]segmentData{vid(0, 180000), vid(180000, 180000)})
	if !ok {
		t.Fatal("boundaries were not derived from timestamped segments")
	}
	// Two starts and the end of the last one.
	if len(bounds) != 3 || bounds[0] != 0 || bounds[1] != 2 || bounds[2] != 4 {
		t.Errorf("bounds = %v, want [0 2 4]", bounds)
	}

	// An audio-only rendition still has a timeline.
	aud := segmentData{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{{
		Kind: media.Audio, Timescale: 90000, HasPTS: true, MinPTS: 90000, Samples: 10,
	}}}}
	if _, ok := segmentBoundaries([]segmentData{aud}); !ok {
		t.Error("an audio-only rendition gave no boundaries")
	}

	// No timestamps, no timescale, no tracks: nothing to place anything on.
	for _, sd := range []segmentData{
		{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video, Timescale: 90000}}}},
		{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video, HasPTS: true}}}},
		{parsed: true, info: media.SegmentInfo{}},
	} {
		if _, ok := segmentBoundaries([]segmentData{sd}); ok {
			t.Errorf("boundaries derived from %+v", sd.info)
		}
	}
}

// A break outside the sampled window is one nobody looked at, not one that is
// misplaced — the difference between an honest silence and a false accusation.
func TestNearestBoundary(t *testing.T) {
	bounds := []float64{0, 2, 4, 6}
	for _, tc := range []struct {
		at     float64
		off    float64
		within bool
	}{
		{2, 0, true},      // exactly on one
		{2.05, 0, true},   // inside the tolerance
		{3, 1, true},      // a second past the boundary at 2
		{1.5, -0.5, true}, // half a second before the one at 2
		{-1, 0, false},    // before the window
		{7, 0, false},     // after it
	} {
		off, within := nearestBoundary(bounds, tc.at, 0.1)
		if within != tc.within || off != tc.off {
			t.Errorf("nearestBoundary(%v) = %v/%v, want %v/%v", tc.at, off, within, tc.off, tc.within)
		}
	}
	if _, within := nearestBoundary(nil, 1, 0.1); within {
		t.Error("an empty boundary set placed a break inside it")
	}
}

// Three tags state when a break happens three different ways, and one of them
// cannot be placed at all without EXT-X-PROGRAM-DATE-TIME.
func TestDeclaredBreakTime(t *testing.T) {
	seg := func(seq int, start int64, pdt time.Time, havePDT bool) segmentData {
		return segmentData{
			seg: manifest.Segment{Sequence: seq, PDT: pdt, HasPDT: havePDT},
			info: media.SegmentInfo{Tracks: []media.Track{{
				Kind: media.Video, Timescale: 90000, HasPTS: true, MinPTS: start, Samples: 50,
			}}},
			parsed: true,
		}
	}
	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	segs := []segmentData{
		seg(10, 0, base, true),
		seg(11, 180000, base.Add(2*time.Second), true),
	}

	// DASH states it outright.
	if at, ok := declaredBreakTime(manifest.AdBreak{MediaTime: 8, HasMediaTime: true}, segs); !ok || at != 8 {
		t.Errorf("a DASH event gave %v/%v, want 8", at, ok)
	}
	// A cue-out names the segment it precedes, and that segment's own first
	// timestamp is where it begins.
	if at, ok := declaredBreakTime(manifest.AdBreak{Sequence: 11, HasSequence: true}, segs); !ok || at != 2 {
		t.Errorf("a cue-out gave %v/%v, want 2", at, ok)
	}
	// A cue-out naming a segment outside the sample cannot be placed.
	if _, ok := declaredBreakTime(manifest.AdBreak{Sequence: 99, HasSequence: true}, segs); ok {
		t.Error("a cue-out outside the sampled window was placed anyway")
	}
	// A DATERANGE is on the wall clock: its media time is the PDT anchor plus the
	// offset from it.
	dr := manifest.AdBreak{Start: base.Add(5 * time.Second), HasStart: true}
	if at, ok := declaredBreakTime(dr, segs); !ok || at != 5 {
		t.Errorf("a DATERANGE gave %v/%v, want 5", at, ok)
	}
	// Without a PDT there is nothing to anchor it to. SC-51 is where that mapping
	// gets checked in its own right; here it simply cannot be compared.
	noPDT := []segmentData{seg(10, 0, time.Time{}, false)}
	if _, ok := declaredBreakTime(dr, noPDT); ok {
		t.Error("a DATERANGE was placed with no EXT-X-PROGRAM-DATE-TIME to anchor it")
	}
	// A break that states none of the three.
	if _, ok := declaredBreakTime(manifest.AdBreak{}, segs); ok {
		t.Error("a break stating no time at all was placed")
	}

	// An audio-only rendition has a timeline too, and both tags must find it.
	aud := []segmentData{{
		seg: manifest.Segment{Sequence: 11, PDT: base, HasPDT: true},
		info: media.SegmentInfo{Tracks: []media.Track{{
			Kind: media.Audio, Timescale: 90000, HasPTS: true, MinPTS: 180000, Samples: 10,
		}}},
		parsed: true,
	}}
	if at, ok := declaredBreakTime(manifest.AdBreak{Sequence: 11, HasSequence: true}, aud); !ok || at != 2 {
		t.Errorf("a cue-out over audio gave %v/%v, want 2", at, ok)
	}
	if at, ok := declaredBreakTime(manifest.AdBreak{Start: base, HasStart: true}, aud); !ok || at != 2 {
		t.Errorf("a DATERANGE over audio gave %v/%v, want 2", at, ok)
	}

	// A segment with a PDT but no timestamps of its own anchors nothing.
	noTime := []segmentData{{
		seg:    manifest.Segment{Sequence: 11, PDT: base, HasPDT: true},
		info:   media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video, Timescale: 90000}}},
		parsed: true,
	}}
	if _, ok := declaredBreakTime(manifest.AdBreak{Start: base, HasStart: true}, noTime); ok {
		t.Error("a DATERANGE was anchored to a segment with no timestamps")
	}
	if _, ok := declaredBreakTime(manifest.AdBreak{Sequence: 11, HasSequence: true}, noTime); ok {
		t.Error("a cue-out was placed on a segment with no timestamps")
	}
}

// A break the check cannot place is skipped rather than judged: a DATERANGE with
// no EXT-X-PROGRAM-DATE-TIME anywhere states a wall-clock time and nothing to
// anchor it to.
func TestCheckAdBreak_UnplaceableDeclarationIsSkipped(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{{
			Kind: media.Video, Timescale: 90000, HasPTS: true,
			MinPTS: 0, MaxPTS: 176400, FrameDur: 3600, Samples: 50,
		}}}}},
		adBreaks: []manifest.AdBreak{{
			Start:    time.Date(2026, 8, 17, 10, 0, 5, 0, time.UTC),
			HasStart: true, Tag: "EXT-X-DATERANGE", OutOfNetwork: true,
		}},
	}
	out := checkAdBreak([]*renditionData{rd}, Defaults())
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
}

// A DASH event whose time is not a boundary is the same defect a mid-segment
// splice is, seen from the manifest's side.
func TestCheckAdBreak_DeclaredBreakOffBoundary(t *testing.T) {
	vid := func(start int64) segmentData {
		return segmentData{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{{
			Kind: media.Video, Timescale: 90000, HasPTS: true,
			MinPTS: start, MaxPTS: start + 176400, FrameDur: 3600, Samples: 50,
		}}}}
	}
	rd := &renditionData{
		r:        manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs:     []segmentData{vid(0), vid(180000)},
		adBreaks: []manifest.AdBreak{{MediaTime: 2.6, HasMediaTime: true, Tag: "EventStream", OutOfNetwork: true}},
	}
	out := checkAdBreak([]*renditionData{rd}, Defaults())
	if len(out) != 1 || out[0].Status != finding.BAD {
		t.Fatalf("want one BAD finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "EventStream") {
		t.Errorf("the finding does not name the tag: %q", out[0].Message)
	}

	// The same break moved onto a boundary is fine, and still reported.
	rd.adBreaks[0].MediaTime = 2
	out = checkAdBreak([]*renditionData{rd}, Defaults())
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
}

// Signalling over segments with no timeline of their own: report that it is there
// and that it could not be placed, rather than judging it against nothing.
func TestCheckAdBreak_NoTimelineToPlaceItOn(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{
			Tracks:  []media.Track{{Kind: media.Video}},
			Splices: []media.SplicePoint{{Command: "time_signal", PTS: 90000, HasPTS: true}},
		}}},
	}
	out := checkAdBreak([]*renditionData{rd}, Defaults())
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "no timeline") {
		t.Errorf("the finding does not say why: %q", out[0].Message)
	}
}

// A rendition that could not be sampled, and one with no parsed segments: neither
// has anything to report.
func TestCheckAdBreak_QuietCases(t *testing.T) {
	quiet := []*renditionData{
		{r: manifest.Rendition{Name: "broken"}, err: errUnusable},
		{r: manifest.Rendition{Name: "720p"}, adBreaks: []manifest.AdBreak{{Tag: "EventStream"}}},
	}
	if out := checkAdBreak(quiet, Defaults()); len(out) != 0 {
		t.Errorf("want no findings, got %+v", out)
	}
}

// A splice outside the sampled window is not judged, and the count still reports it.
func TestCheckAdBreak_SpliceOutsideTheWindow(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{
			Tracks: []media.Track{{
				Kind: media.Video, Timescale: 90000, HasPTS: true,
				MinPTS: 0, MaxPTS: 176400, FrameDur: 3600, Samples: 50,
			}},
			// An hour in, far outside the two seconds we sampled.
			Splices: []media.SplicePoint{{Command: "time_signal", PTS: 324000000, HasPTS: true}},
		}}},
	}
	out := checkAdBreak([]*renditionData{rd}, Defaults())
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "1 splice point inband") {
		t.Errorf("the finding does not report the signal: %q", out[0].Message)
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "point", "points"); got != "point" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(2, "point", "points"); got != "points" {
		t.Errorf("plural(2) = %q", got)
	}
}
