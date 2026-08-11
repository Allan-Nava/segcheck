package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// Until this file existed, an HEVC rung in an MPEG-TS segment reported its
// codec and no resolution, so the `resolution` check silently skipped it — a
// silence that reads exactly like a clean bill of health. Every test here is a
// round trip: mediatest writes a parameter set with a resolution known by
// construction, and the parser has to recover it.

func TestParseHEVCSPS_ReadsCodedResolution(t *testing.T) {
	for _, tc := range []struct{ w, h int }{
		{3840, 2160},
		{1920, 1080},
		{1280, 720},
		{960, 540},
		{640, 360},
		{256, 144},
	} {
		sps := mediatest.HEVCSPSFor(tc.w, tc.h)
		w, h, ok := parseHEVCSPS(unescapeRBSP(sps))
		if !ok {
			t.Errorf("%dx%d: parse failed", tc.w, tc.h)
			continue
		}
		if w != tc.w || h != tc.h {
			t.Errorf("round trip gave %dx%d, want %dx%d", w, h, tc.w, tc.h)
		}
	}
}

// The conformance window is HEVC's cropping, and it is counted in chroma
// samples: getting the SubWidthC/SubHeightC multiplier wrong yields a plausible
// number that is quietly wrong, which is worse than failing.
func TestParseHEVCSPS_AppliesTheConformanceWindow(t *testing.T) {
	cases := []struct {
		name         string
		p            mediatest.HEVCSPSParams
		wantW, wantH int
	}{
		{
			// 4:2:0, so each offset unit is 2 luma samples: 1088 - 2*4 = 1080.
			name: "4:2:0 crops 8 lines off a 1088-line frame",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1088,
				ConformanceWindow: true, ConfWinBottom: 4,
			},
			wantW: 1920, wantH: 1080,
		},
		{
			// 4:2:2 subsamples horizontally only, so the vertical unit is 1.
			name: "4:2:2 uses a vertical unit of one",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 2, WidthInLumaSamples: 1920, HeightInLumaSamples: 1088,
				ConformanceWindow: true, ConfWinBottom: 4,
			},
			wantW: 1920, wantH: 1084,
		},
		{
			// 4:4:4 is not subsampled at all: the offsets are luma samples.
			name: "4:4:4 crops one for one",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 3, WidthInLumaSamples: 1920, HeightInLumaSamples: 1088,
				ConformanceWindow: true, ConfWinBottom: 4,
			},
			wantW: 1920, wantH: 1084,
		},
		{
			name: "all four offsets",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1088,
				ConformanceWindow: true,
				ConfWinLeft:       2, ConfWinRight: 2, ConfWinTop: 1, ConfWinBottom: 3,
			},
			wantW: 1912, wantH: 1080,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h, ok := parseHEVCSPS(unescapeRBSP(mediatest.HEVCSPS(tc.p)))
			if !ok {
				t.Fatal("parse failed")
			}
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("got %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

// profile_tier_level carries a variable-length tail once there is more than one
// temporal sub-layer. Misreading it desynchronises every field after it, and
// the resolution comes after it — this is the field that makes an HEVC SPS
// harder than an H.264 one.
func TestParseHEVCSPS_SubLayersDoNotDesynchronise(t *testing.T) {
	for subLayers := uint32(0); subLayers <= 6; subLayers++ {
		p := mediatest.HEVCSPSParams{
			MaxSubLayersMinus1: subLayers,
			ChromaFormatIDC:    1,
			WidthInLumaSamples: 1280, HeightInLumaSamples: 720,
		}
		w, h, ok := parseHEVCSPS(unescapeRBSP(mediatest.HEVCSPS(p)))
		if !ok {
			t.Errorf("sps_max_sub_layers_minus1=%d: parse failed", subLayers)
			continue
		}
		if w != 1280 || h != 720 {
			t.Errorf("sps_max_sub_layers_minus1=%d: got %dx%d, want 1280x720", subLayers, w, h)
		}
	}
}

func TestParseHEVCSPS_TruncatedFailsCleanly(t *testing.T) {
	full := mediatest.HEVCSPSFor(1920, 1080)
	for n := 0; n < len(full); n++ {
		// Must not panic, and must not invent a resolution.
		if w, h, ok := parseHEVCSPS(unescapeRBSP(full[:n])); ok && (w != 1920 || h != 1080) {
			t.Errorf("truncated to %d bytes returned %dx%d as if it were complete", n, w, h)
		}
	}
}

func TestHEVCResolution_FindsSPSAmongOtherNALUs(t *testing.T) {
	es := mediatest.HEVCAnnexB(1920, 1080)
	w, h, ok := hevcResolution(es)
	if !ok {
		t.Fatal("no SPS found in a stream that carries VPS, SPS and PPS")
	}
	if w != 1920 || h != 1080 {
		t.Errorf("got %dx%d, want 1920x1080", w, h)
	}
}

// An HEVC NAL header is two bytes and its type lives in different bits from
// H.264's. Reading one stream with the other reader must fail rather than
// return a number: a confident wrong resolution is worse than none.
func TestHEVCResolution_DoesNotReadAnH264Stream(t *testing.T) {
	h264 := mediatest.AnnexB(mediatest.SPSFor(1920, 1080))
	if w, h, ok := hevcResolution(h264); ok {
		t.Errorf("read an H.264 stream as HEVC and returned %dx%d", w, h)
	}
}

func TestHEVCResolution_EmptyAndGarbage(t *testing.T) {
	for _, es := range [][]byte{nil, {}, {0x00}, {0x00, 0x00, 0x01}, make([]byte, 512)} {
		if _, _, ok := hevcResolution(es); ok {
			t.Errorf("found a resolution in %d bytes of nothing", len(es))
		}
	}
}

// The TS parser has to dispatch on the stream type: an HEVC elementary stream
// read with the H.264 reader finds no SPS and reports no resolution, which is
// the silence this whole file exists to remove.
func TestParseTS_ReadsHEVCResolution(t *testing.T) {
	seg := mediatest.TSWithHEVCSPS(0, 3600, 4, mediatest.HEVCSPSFor(3840, 2160))
	info, err := Parse(seg, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var video *Track
	for i := range info.Tracks {
		if info.Tracks[i].Kind == Video {
			video = &info.Tracks[i]
			break
		}
	}
	if video == nil {
		t.Fatal("no video track in an HEVC segment")
	}
	if video.Codec != "hevc" {
		t.Errorf("codec is %q, want hevc", video.Codec)
	}
	if video.Width != 3840 || video.Height != 2160 {
		t.Errorf("resolution is %dx%d, want 3840x2160 — an HEVC rung must not be skipped in silence", video.Width, video.Height)
	}
}

// The dispatch must not regress H.264: the two readers are not interchangeable.
func TestParseTS_StillReadsH264Resolution(t *testing.T) {
	seg := mediatest.TSWithSPS(0, 3600, 4, mediatest.SPSFor(1280, 720))
	info, err := Parse(seg, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, tr := range info.Tracks {
		if tr.Kind != Video {
			continue
		}
		if tr.Width != 1280 || tr.Height != 720 {
			t.Errorf("H.264 resolution is %dx%d, want 1280x720", tr.Width, tr.Height)
		}
	}
}

// SC-15's other half. In fMP4 the resolution is stated by the container — the
// visual sample entry carries it — so HEVC needs no bitstream reader there.
// That is worth an assertion rather than an assumption: `hvc1` only reports a
// resolution because it is in the visual-sample-entry list, and a codec dropped
// from that list would go silent exactly the way MPEG-TS HEVC used to.
func TestParseMP4_ReadsHEVCResolutionFromTheSampleEntry(t *testing.T) {
	init := mediatest.MP4InitHEVC(1, 90000, 1920, 1080)
	seg := mediatest.MP4Segment(1, 1, 0, 3600, 25, 64)

	info, err := Parse(seg, init)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, tr := range info.Tracks {
		if tr.Kind != Video {
			continue
		}
		if tr.Codec != "hevc" {
			t.Errorf("codec is %q, want hevc", tr.Codec)
		}
		if tr.Width != 1920 || tr.Height != 1080 {
			t.Errorf("resolution is %dx%d, want 1920x1080", tr.Width, tr.Height)
		}
		return
	}
	t.Fatal("no video track")
}
