package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-95: partial encryption — a different shape of blindness.
//
// AES-128 encrypts a whole segment, so nothing parses and the honest report is "segcheck
// could not look". CENC and SAMPLE-AES encrypt the *samples* and leave the container
// alone, so the timing checks work perfectly and the bitstream ones cannot work at all.
//
// That second case is worse than the first, because the readers do not fail — they
// succeed and find nothing. A caption scan over encrypted samples reports "scanned, no
// captions", and against a manifest declaring CC1 that is a BAD on media that is
// entirely correct.

func TestParseMP4_CENCSchemeIsRead(t *testing.T) {
	for _, tc := range []struct {
		scheme string
		want   string
	}{
		{"cenc", "cenc"},
		{"cbcs", "cbcs"},
		{"cens", "cens"},
		{"cbc1", "cbc1"},
	} {
		t.Run(tc.scheme, func(t *testing.T) {
			init := mediatest.MP4InitCENC(1, 90000, 1280, 720, "avc1", tc.scheme)
			frag := mediatest.MP4Segment(1, 1, 0, 3600, 25, 2000)
			info, err := ParseMP4(frag, init)
			if err != nil {
				t.Fatalf("ParseMP4: %v", err)
			}
			tr, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			if tr.Protection != tc.want {
				t.Errorf("protection = %q, want %q", tr.Protection, tc.want)
			}
			if !tr.Encrypted {
				t.Error("an encrypted track was not marked as one")
			}
			// The container is readable, which is the whole difference from AES-128.
			if tr.Codec != "h264" || tr.Width != 1280 {
				t.Errorf("the container was not read: %+v", tr)
			}
			if !tr.SamplesEncrypted {
				t.Error("the samples are protected and nothing says so")
			}
		})
	}
}

// The defect: a caption scan over encrypted samples succeeds and finds nothing. Reporting
// that as "scanned, no captions" turns a manifest that correctly declares CC1 into a BAD
// against media that is entirely correct.
func TestParseMP4_EncryptedSamplesAreNotScannedForCaptions(t *testing.T) {
	init := mediatest.MP4InitCENC(1, 90000, 1280, 720, "avc1", "cenc")
	// Bytes that are not NAL units, standing in for encrypted samples.
	frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
		TrackID: 1, SampleDuration: 3600, Samples: [][]byte{make([]byte, 64)},
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	if tr.Captions.Scanned {
		t.Error("encrypted samples were reported as scanned for captions")
	}
	if tr.Captions.Any() {
		t.Errorf("captions were found in encrypted samples: %+v", tr.Captions)
	}
}

// An unencrypted track states no scheme and its samples are readable, which is what
// keeps this from suppressing the bitstream readers on every stream.
func TestParseMP4_UnencryptedTrackIsUnaffected(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 25, 2000)
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	if tr.Protection != "" || tr.SamplesEncrypted {
		t.Errorf("an unencrypted track reported protection: %+v", tr)
	}
	if !tr.Captions.Scanned {
		t.Error("an unencrypted track was not scanned for captions")
	}
}

// An encrypted sample entry with no schm box states no scheme. The samples are still
// protected — the entry type says so — and the readers must still stay out.
func TestParseMP4_EncryptedWithoutSchm(t *testing.T) {
	init := mediatest.MP4InitCENC(1, 90000, 1280, 720, "avc1", "")
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 25, 2000)
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	if tr.Protection != "" {
		t.Errorf("protection = %q, want none", tr.Protection)
	}
	if !tr.SamplesEncrypted {
		t.Error("an encv entry with no schm left the samples readable")
	}
}
