package analyze

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

func segAt(minPTS int64, timescale uint32, dur uint32, samples int) segmentData {
	return segmentData{
		parsed: true,
		info: media.SegmentInfo{Tracks: []media.Track{{
			Kind: media.Video, Codec: "h264", Width: 1280, Height: 720,
			Timescale: timescale, MinPTS: minPTS,
			MaxPTS:   minPTS + int64(dur)*int64(samples-1),
			FrameDur: int64(dur),
			Samples:  samples,
			HasPTS:   true,
		}}},
	}
}

// A tag between two segments whose timescales differ is honoured by that alone:
// the two do not even count time in the same units, which is as complete a
// timeline break as there is. Reaching this through Run would need a rendition
// that changes timescale mid-stream, which no fixture here builds.
func TestTimelineJumped_AChangedTimescaleIsItselfABreak(t *testing.T) {
	prev := segAt(0, 90000, 3600, 50)
	cur := segAt(0, 48000, 1920, 50)
	jumped, ok := timelineJumped(prev, cur, 0.1)
	if !ok || !jumped {
		t.Errorf("a changed timescale gave jumped=%v ok=%v, want true/true", jumped, ok)
	}

	// And a segment with no readable timeline at all measures nothing rather
	// than measuring zero.
	if _, ok := timelineJumped(segmentData{parsed: true}, cur, 0.1); ok {
		t.Error("a segment with no timeline was reported as measurable")
	}
	// Nor when the duration of the first is unknown: the comparison is against
	// where the previous segment ends, and without a duration there is no end.
	noDur := segmentData{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
		{Kind: media.Video, Timescale: 90000, MinPTS: 0, MaxPTS: 0, HasPTS: true, Samples: 1},
	}}}
	if _, ok := timelineJumped(noDur, segAt(180000, 90000, 3600, 50), 0.1); ok {
		t.Error("a segment whose duration is unknown was reported as measurable")
	}
}

// A segment with no track at all still has to produce a name, because the
// comparison is between two of them and one being empty is the interesting case.
func TestEncodingOf_NoTrack(t *testing.T) {
	if got := encodingOf(media.SegmentInfo{}); got != "no track" {
		t.Errorf("encodingOf(nothing) = %q, want %q", got, "no track")
	}
}

// Rungs whose media is at different times are a different defect, and
// `alignment` is the check that reports it: disagreeing about the timeline is
// only meaningful once the media agrees it is the same moment.
func TestDiscontinuitySequenceFindings_DifferentMomentsAreNotCompared(t *testing.T) {
	rends := []*renditionData{
		{r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
			segs: []segmentData{withSeq(segAt(0, 90000, 3600, 50), 0, 0)}},
		{r: manifest.Rendition{Name: "360p", Kind: manifest.Video},
			segs: []segmentData{withSeq(segAt(90000, 90000, 3600, 50), 0, 4)}},
	}
	if out := discontinuitySequenceFindings(rends, 0.1); out != nil {
		t.Errorf("rungs a second apart were compared anyway: %+v", out)
	}

	// A text rendition is skipped: its timestamps are a cue span, not a segment
	// extent, so it is not evidence about which timeline anything is on.
	rends = append(rends, &renditionData{
		r:    manifest.Rendition{Name: "en", Kind: manifest.Text},
		segs: []segmentData{withSeq(segAt(0, 90000, 3600, 50), 0, 9)},
	})
	for _, f := range discontinuitySequenceFindings(rends, 0.1) {
		if f.Status != finding.OK {
			t.Errorf("a subtitle rendition was read as a timeline: %s", f.Message)
		}
	}
}

// A splice information PID is signalling, not media: many packagers put one in
// the PMT only of the segments that carry a cue, so counting it as part of the
// encoding would make every ad break look like a decoder reconfiguration — which
// is exactly what the tag beside it is there to say it is not.
func TestEncodingOf_SignallingIsNotPartOfTheEncoding(t *testing.T) {
	plain := media.SegmentInfo{Tracks: []media.Track{
		{Kind: media.Video, Codec: "h264", Width: 1280, Height: 720, Timescale: 90000},
	}}
	withSplice := media.SegmentInfo{Tracks: append(append([]media.Track{}, plain.Tracks...),
		media.Track{Kind: media.Other, Codec: "scte35", Timescale: 90000})}
	if a, b := encodingOf(plain), encodingOf(withSplice); a != b {
		t.Errorf("a splice information PID changed the encoding: %q against %q", a, b)
	}
}

// A gap in the sample makes the pair meaningless: the segment before the tag was
// never fetched, so there is nothing to say the timeline ran through it.
func TestCheckDiscontinuity_ANonAdjacentPairSaysNothing(t *testing.T) {
	prev := withSeq(segAt(0, 90000, 3600, 50), 0, 0)
	cur := withSeq(segAt(180000, 90000, 3600, 50), 4, 0)
	cur.seg.Discontinuity = true
	rends := []*renditionData{{
		r:    manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{prev, cur},
	}}
	for _, f := range checkDiscontinuity(rends, Options{GapToleranceMS: 100}) {
		if f.Status != finding.OK {
			t.Errorf("a pair with a fetch failure between them was judged anyway: %s", f.Message)
		}
	}
}

func withSeq(sd segmentData, sequence, discSeq int) segmentData {
	sd.seg = manifest.Segment{Sequence: sequence, DiscontinuitySequence: discSeq}
	return sd
}
