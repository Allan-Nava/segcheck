package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

func TestParseH264SPS_Cropping(t *testing.T) {
	tests := []struct {
		name         string
		params       mediatest.SPSParams
		wantW, wantH int
	}{
		{
			name: "1920x1080 High: 1088 lines coded, 8 cropped",
			params: mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 119, HeightInMapUnits: 67,
				FrameMBsOnly: 1, FrameCropping: true, CropBottom: 4,
			},
			wantW: 1920, wantH: 1080,
		},
		{
			name: "1280x720 High: no cropping needed",
			params: mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 79, HeightInMapUnits: 44,
				FrameMBsOnly: 1,
			},
			wantW: 1280, wantH: 720,
		},
		{
			name: "640x360 Baseline: the chroma fields are absent at this profile",
			params: mediatest.SPSParams{
				ProfileIDC: 66, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 39, HeightInMapUnits: 22,
				FrameMBsOnly: 1, FrameCropping: true, CropBottom: 4,
			},
			wantW: 640, wantH: 360,
		},
		{
			name: "854x480 High: cropped horizontally too",
			params: mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 53, HeightInMapUnits: 29,
				FrameMBsOnly: 1, FrameCropping: true, CropRight: 5,
			},
			wantW: 854, wantH: 480,
		},
		{
			name: "1920x1080 High with a scaling matrix to skip over",
			params: mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 119, HeightInMapUnits: 67,
				FrameMBsOnly: 1, FrameCropping: true, CropBottom: 4,
				ScalingMatrix: true,
			},
			wantW: 1920, wantH: 1080,
		},
		{
			name: "1920x1080 interlaced: map units are field pairs",
			params: mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 119, HeightInMapUnits: 33,
				FrameMBsOnly: 0, FrameCropping: true, CropBottom: 2,
			},
			wantW: 1920, wantH: 1080,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, h, ok := parseH264SPS(mediatest.SPS(tc.params))
			if !ok {
				t.Fatal("parseH264SPS reported failure")
			}
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("resolution = %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestSPSFor_RoundTripsCommonLadderRungs(t *testing.T) {
	for _, res := range [][2]int{{1920, 1080}, {1280, 720}, {854, 480}, {640, 360}, {426, 240}} {
		w, h, ok := parseH264SPS(mediatest.SPSFor(res[0], res[1]))
		if !ok {
			t.Errorf("%dx%d: parseH264SPS failed", res[0], res[1])
			continue
		}
		if w != res[0] || h != res[1] {
			t.Errorf("%dx%d round-tripped as %dx%d", res[0], res[1], w, h)
		}
	}
}

func TestUnescapeRBSP(t *testing.T) {
	// Without undoing the emulation prevention byte every field after it reads
	// off by eight bits, which is how a resolution check silently goes wrong.
	in := []byte{0x00, 0x00, 0x03, 0x01, 0xFF, 0x00, 0x00, 0x03, 0x02}
	want := []byte{0x00, 0x00, 0x01, 0xFF, 0x00, 0x00, 0x02}
	got := unescapeRBSP(in)
	if len(got) != len(want) {
		t.Fatalf("length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %#x, want %#x (%v)", i, got[i], want[i], got)
		}
	}
}

func TestH264Resolution_FindsSPSAmongOtherNALUs(t *testing.T) {
	es := []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0x10} // access unit delimiter
	es = append(es, 0x00, 0x00, 0x01, 0x67)          // 3-byte start code, SPS
	es = append(es, mediatest.SPSFor(1280, 720)...)
	es = append(es, 0x00, 0x00, 0x01, 0x68, 0xCE, 0x3C, 0x80) // PPS

	w, h, ok := h264Resolution(es)
	if !ok {
		t.Fatal("SPS not found in the elementary stream")
	}
	if w != 1280 || h != 720 {
		t.Errorf("resolution = %dx%d, want 1280x720", w, h)
	}
}

func TestParseH264SPS_TruncatedFailsCleanly(t *testing.T) {
	if _, _, ok := parseH264SPS([]byte{0x64, 0x00}); ok {
		t.Error("a two-byte SPS was accepted; a truncated bitstream must fail rather than guess")
	}
}
