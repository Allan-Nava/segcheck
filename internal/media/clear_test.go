package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// Whether a sample is actually encrypted is stated per sample, in `saiz`: a
// sample carrying no encryption information carries none because there is none.
//
// It is the quietest thing in this milestone. Content that ships unprotected
// plays everywhere, nobody files a bug, every check passes, and the first
// signal is a rights-holder audit — so the only way to know is to read the
// per-sample state and say what it found.
func TestParse_PerSampleEncryptionState(t *testing.T) {
	init := mediatest.MP4InitCENCTenc(1, 90000, 640, 360, "avc1", "cenc",
		"9eb4050d-e44b-4802-932e-27d75083e266", 0, 0)

	for _, tc := range []struct {
		name              string
		clearLeading      int
		samples           int
		wantClear, wantEn int
	}{
		{"every sample encrypted", 0, 10, 0, 10},
		{"a clear lead of four samples", 4, 10, 4, 6},
		{"nothing encrypted at all", 10, 10, 10, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seg := mediatest.MP4SegmentSAIZ(1, 0, 0, 3600, tc.samples, 4000, tc.clearLeading)
			info, err := Parse(seg, init)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			track, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			clear, encrypted, known := track.SampleEncryption()
			if !known {
				t.Fatal("the fragment states its per-sample encryption and it was not read")
			}
			if clear != tc.wantClear || encrypted != tc.wantEn {
				t.Errorf("clear/encrypted = %d/%d, want %d/%d", clear, encrypted, tc.wantClear, tc.wantEn)
			}
			if track.LeadingClearSamples != tc.clearLeading {
				t.Errorf("LeadingClearSamples = %d, want %d", track.LeadingClearSamples, tc.clearLeading)
			}
		})
	}
}

// A fragment that states nothing per sample says nothing, and a check must be
// able to tell that from "every sample is in the clear".
func TestParse_NoSaizMeansTheSampleStateIsUnknown(t *testing.T) {
	init := mediatest.MP4InitCENCTenc(1, 90000, 640, 360, "avc1", "cenc",
		"9eb4050d-e44b-4802-932e-27d75083e266", 0, 0)
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, _ := info.Track(Video)
	if _, _, known := track.SampleEncryption(); known {
		t.Error("a fragment with no saiz reported a per-sample encryption state it never stated")
	}
}
