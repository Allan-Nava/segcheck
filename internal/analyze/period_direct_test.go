package analyze

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// The branches below are reachable through Run only from a stream this repo has
// no way to build — a period whose media parses but states no timescale, a
// codec that changes across a boundary — so they are exercised on the functions
// themselves rather than left as a hole the smoke run would have to find.

func TestPeriodEncoderFindings_ACodecChangeOutranksAResolutionChange(t *testing.T) {
	periods := []periodShape{
		{label: `period 1 "main"`, rungs: []string{"1280x720"}, codecs: []string{"h264"}},
		{label: `period 2 "ad"`, rungs: []string{"1280x720"}, codecs: []string{"hevc"}},
	}
	out := periodEncoderFindings(periods)
	if len(out) != 1 || out[0].Status != finding.BAD {
		t.Fatalf("a codec change gave %+v, want one BAD", out)
	}
	if !strings.Contains(out[0].Message, "h264") || !strings.Contains(out[0].Message, "hevc") {
		t.Errorf("the finding does not name both codecs: %q", out[0].Message)
	}

	// A period whose media could not be read states neither, and a comparison
	// against nothing is not a finding.
	if out := periodEncoderFindings([]periodShape{{label: "a"}, {label: "b"}}); out != nil {
		t.Errorf("two unread periods produced %+v", out)
	}
}

func TestPeriodTimelineFindings_NothingMeasuredIsNotADefect(t *testing.T) {
	if out := periodTimelineFindings([]periodShape{{label: "a"}, {label: "b"}}); out != nil {
		t.Errorf("periods whose media was never read produced %+v", out)
	}
	// One period measured and one not: the unmeasured one is skipped rather
	// than compared against a zero it never stated.
	out := periodTimelineFindings([]periodShape{
		{label: "a", measured: true, offsetErr: 0, segDur: 2},
		{label: "b"},
	})
	if out != nil {
		t.Errorf("an unmeasured period was compared anyway: %+v", out)
	}
}

func TestRenditionOffsetError_StaysQuietWithoutATimeline(t *testing.T) {
	// A segment the manifest places but whose media has no usable clock: the
	// timescale is the thing that makes a tick a second, and without it the
	// comparison would be against a number that means nothing.
	rd := &renditionData{
		r: manifest.Rendition{Kind: manifest.Video},
		segs: []segmentData{
			{parsed: false, seg: manifest.Segment{HasPeriodOffset: true}},
			{parsed: true, seg: manifest.Segment{}},
			{parsed: true, seg: manifest.Segment{HasPeriodOffset: true},
				info: media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video, Timescale: 0}}}},
		},
	}
	if _, _, ok := renditionOffsetError(rd); ok {
		t.Error("an unreadable timeline was reported as a measurement")
	}
}

func TestSameStrings_DifferentLengthsAreNotTheSame(t *testing.T) {
	if sameStrings([]string{"a"}, []string{"a", "b"}) {
		t.Error("a one-rung period compared equal to a two-rung one")
	}
}
