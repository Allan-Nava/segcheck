package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// How the code values in a picture map to light is stated in the VUI, and it is
// the claim behind HDR: transfer characteristic 16 is PQ, 18 is HLG, 1 and 6 are
// the two spellings of SDR. A PQ rung whose samples are BT.709 is tone-mapped
// twice by every device that believes the manifest and once by every device that
// believes the bitstream, so the two halves of the audience see different
// pictures of the same stream.
//
// The trap is that the VUI sits behind two optional blocks whose lengths vary,
// so reaching it means parsing them rather than seeking past them. The writer
// here is the inverse of the reader, and the round trip is what catches a
// bit-level mistake — an aspect ratio that carries an extended SAR moves
// everything after it by thirty-two bits.
func TestParseH264SPS_ColourDescription(t *testing.T) {
	for _, tc := range []struct {
		name string
		vui  mediatest.VUIParams
		want ColourDescription
	}{
		{
			name: "PQ, BT.2020, limited range",
			vui: mediatest.VUIParams{
				VideoSignalType: true, ColourDescription: true,
				Primaries: 9, Transfer: 16, Matrix: 9,
			},
			want: ColourDescription{Primaries: 9, Transfer: 16, Matrix: 9, Stated: true, RangeStated: true},
		},
		{
			name: "HLG behind an extended SAR, full range",
			vui: mediatest.VUIParams{
				AspectRatioIDC: 255, SARWidth: 4, SARHeight: 3, Overscan: true,
				VideoSignalType: true, FullRange: true, ColourDescription: true,
				Primaries: 9, Transfer: 18, Matrix: 9,
			},
			want: ColourDescription{Primaries: 9, Transfer: 18, Matrix: 9, FullRange: true, Stated: true, RangeStated: true},
		},
		{
			name: "SDR, BT.709",
			vui: mediatest.VUIParams{
				AspectRatioIDC:  1,
				VideoSignalType: true, ColourDescription: true,
				Primaries: 1, Transfer: 1, Matrix: 1,
			},
			want: ColourDescription{Primaries: 1, Transfer: 1, Matrix: 1, Stated: true, RangeStated: true},
		},
		{
			name: "a signal type with no colour description states only the range",
			vui:  mediatest.VUIParams{VideoSignalType: true, FullRange: true},
			want: ColourDescription{FullRange: true, RangeStated: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vui := tc.vui
			sps := mediatest.SPS(mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
				FrameCropping: true, CropBottom: 4,
				VUI: &vui,
			})
			got, ok := parseH264Colour(unescapeRBSP(sps))
			if !ok {
				t.Fatal("the SPS states a VUI and none was read")
			}
			if got != tc.want {
				t.Errorf("colour = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// An SPS with no VUI states nothing, and a zero must not read as BT.709 — code
// value 1 is a real primary, so a defaulted zero would be a claim segcheck
// invented.
func TestParseH264SPS_NoVUIStatesNothing(t *testing.T) {
	sps := mediatest.SPSFor(1280, 720)
	got, ok := parseH264Colour(unescapeRBSP(sps))
	if ok && (got.Stated || got.RangeStated) {
		t.Errorf("an SPS with no VUI produced %+v", got)
	}
}

// The same question for HEVC, where the VUI sits behind the short-term
// reference picture sets rather than behind the cropping offsets — a great deal
// more variable-length material to walk exactly.
func TestParseHEVCSPS_ColourDescription(t *testing.T) {
	vui := mediatest.VUIParams{
		VideoSignalType: true, ColourDescription: true,
		Primaries: 9, Transfer: 16, Matrix: 9,
	}
	sps := mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		WidthInLumaSamples: 3840, HeightInLumaSamples: 2160, ChromaFormatIDC: 1,
		// Two reference picture sets, the second predicted from the first: the
		// variable-length material the VUI hides behind, and the part a reader
		// that guesses instead of parsing gets wrong.
		ShortTermRefPicSets:      2,
		InterRefPicSetPrediction: true,
		VUI:                      &vui,
	})
	got, ok := parseHEVCColour(unescapeRBSP(sps))
	if !ok {
		t.Fatal("the HEVC SPS states a VUI and none was read")
	}
	want := ColourDescription{Primaries: 9, Transfer: 16, Matrix: 9, Stated: true, RangeStated: true}
	if got != want {
		t.Errorf("colour = %+v, want %+v", got, want)
	}
}

// And an HEVC SPS with no VUI, for the same reason.
func TestParseHEVCSPS_NoVUIStatesNothing(t *testing.T) {
	got, ok := parseHEVCColour(unescapeRBSP(mediatest.HEVCSPSFor(1920, 1080)))
	if ok && (got.Stated || got.RangeStated) {
		t.Errorf("an HEVC SPS with no VUI produced %+v", got)
	}
}

// In fMP4 the container states the colour, in a `colr` box with an `nclx`
// profile inside the visual sample entry — the same split resolution already
// has: where the container says it, no bitstream reader is needed.
func TestParse_ColrBoxInTheSampleEntry(t *testing.T) {
	init := mediatest.MP4InitColr(1, 90000, 3840, 2160, "hvc1", 9, 16, 9, false)
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	c, ok := track.Colour()
	if !ok {
		t.Fatal("the sample entry carries a colr box and none was read")
	}
	if c.Transfer != TransferPQ || c.Primaries != PrimariesBT2020 || c.Matrix != MatrixBT2020NCL {
		t.Errorf("colour = %+v, want PQ over BT.2020", c)
	}
	if !c.HDR() {
		t.Error("transfer 16 is PQ and did not read as HDR")
	}
}

// A sample entry with no colr box states nothing, and the checks that compare
// against it must stay quiet rather than compare against a zero.
func TestParse_NoColrBoxStatesNothing(t *testing.T) {
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500),
		mediatest.MP4Init(1, 90000, "video", 640, 360))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, _ := info.Track(Video)
	if _, ok := track.Colour(); ok {
		t.Error("a sample entry with no colr box reported a colour description")
	}
}

// A colr box carrying an ICC profile rather than nclx states no code points at
// all. Reading its bytes as though they were primaries and transfer would
// produce a confident wrong answer.
func TestParse_ColrWithAnICCProfileStatesNoCodePoints(t *testing.T) {
	init := mediatest.MP4InitColrICC(1, 90000, 1920, 1080, "avc1")
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, _ := info.Track(Video)
	if _, ok := track.Colour(); ok {
		t.Error("an ICC colour profile was read as nclx code points")
	}
}

// Most real fMP4 carries no `colr` box at all: the colour lives in the VUI of
// the parameter set inside `avcC` or `hvcC`. Apple's own H.264 rungs are exactly
// that shape, so a reader that looked only for a colr box found nothing on the
// majority of the world's content — which is where this was found.
func TestParse_ColourFromTheParameterSetInTheSampleEntry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sampleEntry string
		sps         []byte
		wantTrans   int
	}{
		{
			name:        "H.264 SPS in avcC",
			sampleEntry: "avc1",
			sps: mediatest.SPS(mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
				VUI: &mediatest.VUIParams{
					VideoSignalType: true, ColourDescription: true,
					Primaries: 1, Transfer: 1, Matrix: 1,
				},
			}),
			wantTrans: TransferBT709,
		},
		{
			name:        "HEVC SPS in hvcC",
			sampleEntry: "hvc1",
			sps: mediatest.HEVCSPS(mediatest.HEVCSPSParams{
				WidthInLumaSamples: 3840, HeightInLumaSamples: 2160, ChromaFormatIDC: 1,
				ShortTermRefPicSets: 1,
				VUI: &mediatest.VUIParams{
					VideoSignalType: true, ColourDescription: true,
					Primaries: 9, Transfer: 16, Matrix: 9,
				},
			}),
			wantTrans: TransferPQ,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init := mediatest.MP4InitWithSPS(1, 90000, 1920, 1080, tc.sampleEntry, tc.sps)
			info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			track, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			c, ok := track.Colour()
			if !ok {
				t.Fatal("the parameter set states a colour description and none was read")
			}
			if c.Transfer != tc.wantTrans {
				t.Errorf("transfer = %d, want %d", c.Transfer, tc.wantTrans)
			}
		})
	}
}
