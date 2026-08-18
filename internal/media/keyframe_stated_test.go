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

// An fMP4 fragment need not state anything about sync samples, and real
// trick-play content does not: Apple's own I-frame fragments carry a trun with
// only a data offset, a tfhd with only a duration and a size, and a trex of
// zeroes. Reading a zeroed trex as "sync sample" would call every such fragment
// a keyframe on no evidence at all.
//
// The samples themselves are the evidence. They are length-prefixed NAL units
// where an elementary stream uses start codes, and the same walk that answers
// the question for MPEG-TS answers it here — as an inference, never as a
// container assertion.
func TestParse_FMP4KeyframeFromTheBitstreamWhenTheContainerIsSilent(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 640, 360)

	// nal builds a minimal NAL unit of the given H.264 type with a byte of
	// payload, which is all the walk reads.
	nal := func(typ byte) []byte { return []byte{0x00 | typ, 0x80} }

	idr := mediatest.MP4SegmentWithNALUs(1, 0, 0, 3600, 1, [][]byte{
		nal(9), // access unit delimiter
		nal(7), // SPS
		nal(8), // PPS
		nal(5), // IDR slice
	})
	nonIDR := mediatest.MP4SegmentWithNALUs(1, 0, 0, 3600, 1, [][]byte{
		nal(9),
		nal(1), // an ordinary coded slice, and nothing else
	})

	for _, tc := range []struct {
		name    string
		body    []byte
		opens   bool
		present bool
	}{
		{"a fragment whose sample is an IDR", idr, true, true},
		{"a fragment with no random access point at all", nonIDR, false, false},
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
			opens, known := track.StartsOnKeyframe()
			if !known {
				t.Fatal("the fragment states no sample flags and the bitstream was not read either")
			}
			if opens != tc.opens {
				t.Errorf("opens = %v, want %v", opens, tc.opens)
			}
			present, scanned := track.ContainsKeyframe()
			if !scanned {
				t.Error("the walk did not complete, so a missing keyframe cannot be told from nobody looking")
			}
			if present != tc.present {
				t.Errorf("present = %v, want %v", present, tc.present)
			}
			// It is an inference, not an assertion: a conformance rule must be
			// able to tell the difference.
			if _, stated := track.OpensOnStatedKeyframe(); stated {
				t.Error("a bitstream walk claimed to be a container assertion")
			}
		})
	}
}
