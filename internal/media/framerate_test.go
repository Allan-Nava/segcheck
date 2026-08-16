package media

import (
	"math"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-17: the frame rate the media actually runs at.
//
// It comes from the median gap between presentation timestamps, which is the one
// measure that survives B-frames — the stream is not in presentation order, so
// anything derived from consecutive decode-order timestamps is wrong on real
// content. The median also shrugs off a single discontinuity inside the segment.
//
// The reason to measure it at all is that `FRAME-RATE` is what a player consults
// to decide whether it can decode a rendition at all. A 1080p60 rung declared as
// 30 will be chosen by a device that can only do 1080p30, and then stutter.

func TestFrameRateFPS(t *testing.T) {
	tests := []struct {
		name  string
		track Track
		want  float64
		ok    bool
	}{
		// 3600 ticks on the 90kHz clock is exactly 25fps.
		{"25fps", Track{Kind: Video, Timescale: 90000, FrameDur: 3600, HasPTS: true, Samples: 50}, 25, true},
		{"50fps", Track{Kind: Video, Timescale: 90000, FrameDur: 1800, HasPTS: true, Samples: 50}, 50, true},
		// 30000/1001 — the NTSC rate a manifest writes as 29.97.
		{"29.97fps", Track{Kind: Video, Timescale: 90000, FrameDur: 3003, HasPTS: true, Samples: 50}, 29.97, true},
		{"a 12800 timescale", Track{Kind: Video, Timescale: 12800, FrameDur: 512, HasPTS: true, Samples: 50}, 25, true},

		// The unmeasurable cases. Each must report false rather than a number: a
		// frame rate of zero, or one derived from an unknown clock, would be
		// compared against the manifest and reported as a defect.
		{"no frame duration", Track{Kind: Video, Timescale: 90000, HasPTS: true, Samples: 50}, 0, false},
		{"unknown timescale", Track{Kind: Video, FrameDur: 3600, HasPTS: true, Samples: 50}, 0, false},
		{"no timestamps", Track{Kind: Video, Timescale: 90000, FrameDur: 3600}, 0, false},
		// One sample gives no gap to take a median of.
		{"a single sample", Track{Kind: Video, Timescale: 90000, HasPTS: true, Samples: 1}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.track.FrameRateFPS()
			if ok != tc.ok {
				t.Fatalf("FrameRateFPS ok = %v, want %v", ok, tc.ok)
			}
			if ok && math.Abs(got-tc.want) > 0.01 {
				t.Errorf("FrameRateFPS = %v, want %v", got, tc.want)
			}
		})
	}
}

// End to end from a real segment: the builder lays the timestamps down at a known
// spacing and the parser has to recover the rate from them.
func TestFrameRateFPS_FromAParsedSegment(t *testing.T) {
	for _, tc := range []struct {
		frameDur int64
		want     float64
	}{
		{3600, 25},
		{1800, 50},
		{3003, 29.97},
	} {
		info, err := ParseTS(mediatest.TS(0, tc.frameDur, 30))
		if err != nil {
			t.Fatalf("ParseTS: %v", err)
		}
		tr, ok := info.Track(Video)
		if !ok {
			t.Fatal("no video track")
		}
		got, ok := tr.FrameRateFPS()
		if !ok {
			t.Fatalf("frameDur %d: no frame rate measured", tc.frameDur)
		}
		if math.Abs(got-tc.want) > 0.01 {
			t.Errorf("frameDur %d: measured %v fps, want %v", tc.frameDur, got, tc.want)
		}
	}
}

// The median is what makes this survive real content. A segment carrying one
// discontinuity — or simply arriving out of presentation order, as anything with
// B-frames does — must still report the rate the pictures are actually shown at.
func TestFrameRateFPS_SurvivesOutOfOrderTimestampsAndAnOutlier(t *testing.T) {
	// Presentation timestamps in decode order for an IPBB pattern, plus one
	// large jump that a mean would smear across the whole segment.
	pts := []int64{0, 14400, 3600, 7200, 10800, 18000, 900000}
	got := medianDelta(pts)
	if got != 3600 {
		t.Fatalf("medianDelta = %d, want 3600", got)
	}
	tr := Track{Kind: Video, Timescale: 90000, FrameDur: got, HasPTS: true, Samples: len(pts)}
	fps, ok := tr.FrameRateFPS()
	if !ok {
		t.Fatal("no frame rate measured")
	}
	if math.Abs(fps-25) > 0.01 {
		t.Errorf("measured %v fps, want 25", fps)
	}
}

// Audio has a sample rate, not a frame rate, and reporting one would invite a
// check to compare it against a video rendition's FRAME-RATE.
func TestFrameRateFPS_NotForAudio(t *testing.T) {
	tr := Track{Kind: Audio, Timescale: 48000, FrameDur: 1024, HasPTS: true, Samples: 50}
	if fps, ok := tr.FrameRateFPS(); ok {
		t.Errorf("an audio track reported %v fps", fps)
	}
}
