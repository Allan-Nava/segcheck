package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// AV1, VP9 and VP8 in fMP4 (SC-42).
//
// None of these needs a bitstream reader the way MPEG-TS H.264 and HEVC do: the
// visual sample entry states the coded size outright, and `av1C` / `vpcC` carry
// profile and level rather than a resolution. So the whole of it rests on the
// sample entry type being recognised as visual — and that is precisely the kind of
// thing that works until someone edits a list.
//
// The failure mode if it breaks is the one this project treats as worst: a codec
// missing from `isVisualSampleEntry` reports no resolution, `resolution` has
// nothing to compare, and the rung is skipped in a silence indistinguishable from
// a pass. It is the same silence HEVC lived in until SC-15, which is why `hvc1`
// was given a test of its own rather than left an assumption. These are that test
// for the codecs that came after.

func TestParseMP4_ModernCodecsReportCodecAndResolution(t *testing.T) {
	tests := []struct {
		entry string
		codec string
		w, h  int
	}{
		{"av01", "av1", 3840, 2160},
		{"vp09", "vp9", 1920, 1080},
		{"vp08", "vp8", 640, 360},
		// The two that already had cover, kept alongside so the table is the whole
		// list of what a ladder can be built from.
		{"avc1", "h264", 1280, 720},
		{"hvc1", "hevc", 3840, 2160},
		{"dvh1", "dolbyvision", 3840, 2160},
	}
	for _, tc := range tests {
		t.Run(tc.entry, func(t *testing.T) {
			init := mediatest.MP4InitCodec(1, 90000, tc.entry, tc.w, tc.h)
			frag := mediatest.MP4Segment(1, 1, 0, 3600, 25, 500)

			info, err := ParseMP4(frag, init)
			if err != nil {
				t.Fatalf("ParseMP4: %v", err)
			}
			tr, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			if tr.Codec != tc.codec {
				t.Errorf("codec = %q, want %q", tr.Codec, tc.codec)
			}
			if tr.Width != tc.w || tr.Height != tc.h {
				t.Errorf("resolution = %dx%d, want %dx%d — the rung would be skipped in silence",
					tr.Width, tr.Height, tc.w, tc.h)
			}
		})
	}
}

// The list itself, stated as a contract rather than left implicit. Every codec a
// ladder can be built from has to be here, or its rungs go unmeasured.
func TestIsVisualSampleEntry_CoversEveryVideoCodecWeName(t *testing.T) {
	for _, entry := range []string{
		"avc1", "avc3", // H.264
		"hvc1", "hev1", // HEVC
		"av01",         // AV1
		"vp08", "vp09", // VP8, VP9
		"vvc1", "vvi1", // VVC
		"dvh1", "dvhe", // Dolby Vision
		"mp4v", // MPEG-4 part 2
	} {
		if !isVisualSampleEntry(entry) {
			t.Errorf("%s is a video sample entry: without it the rung reports no resolution", entry)
		}
		if mp4Codec(entry) == entry {
			t.Errorf("%s has no codec name, so the tracks check has nothing to compare", entry)
		}
	}
}
