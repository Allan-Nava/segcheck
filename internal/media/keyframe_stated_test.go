package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// "This segment does not open on a keyframe" means two different things
// depending on where the answer came from, and a conformance rule has to know
// which.
//
// In fMP4 the trun's first-sample flags state it outright: the container is
// asserting that its first sample is not a sync sample, and that is a defect
// with no room to argue. In MPEG-TS the same answer comes from walking the
// bitstream in decode order, where with B-frames the first coded picture need
// not be the first presented one and the reader may simply not have reached the
// IDR — which is why the `keyframe` check has always treated it gently. Reading
// the two as one number turns Apple's own reference stream into a conformance
// failure.
func TestTrack_KeyframeStatedSeparatesTheContainerFromTheBitstream(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 640, 360)

	for _, tc := range []struct {
		name  string
		body  []byte
		want  bool // OpensOnKeyframe
		state bool // the container stated it
	}{
		{"fMP4 states a sync first sample", mediatest.MP4SegmentSync(1, 0, 0, 3600, 10, 500, true), true, true},
		{"fMP4 states a non-sync first sample", mediatest.MP4SegmentSync(1, 0, 0, 3600, 10, 500, false), false, true},
		{"fMP4 states nothing at all", mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Parse(tc.body, init)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			track, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			opens, stated := track.OpensOnStatedKeyframe()
			if stated != tc.state {
				t.Errorf("stated = %v, want %v", stated, tc.state)
			}
			if stated && opens != tc.want {
				t.Errorf("opens = %v, want %v", opens, tc.want)
			}
		})
	}

	// An MPEG-TS segment's answer is inferred, never stated, however confident
	// the walk was.
	ts := mediatest.TSWithSPS(0, 3600, 25, mediatest.SPSFor(640, 360))
	info, err := Parse(ts, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track in the TS segment")
	}
	if _, stated := track.OpensOnStatedKeyframe(); stated {
		t.Error("an MPEG-TS segment claimed its keyframe answer was container-stated; it was walked out of the bitstream")
	}
}
