package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// A CODECS string is not one value, it is three or four: which profile, which
// level, and for HEVC and AV1 which tier. Only the first component has ever been
// read, and the rest are where the interesting failures are.
//
// They fail in opposite directions. A level declared *below* the media's is a
// decoder that rejects the stream up front — the device reads the manifest,
// decides it cannot play this, and never asks for a segment. A profile declared
// *above* the media's silently excludes devices that could have played it
// perfectly: nobody sees an error, the ladder just has fewer viewers on its top
// rung than it should.
func TestParse_CodecProfileFromTheConfigurationRecord(t *testing.T) {
	for _, tc := range []struct {
		name        string
		init        []byte
		wantProfile int
		wantLevel   int
		wantTier    int
	}{
		{
			name:        "avcC states profile and level outright",
			init:        mediatest.MP4InitAVCProfile(1, 90000, 1920, 1080, 100, 0x00, 40),
			wantProfile: 100,
			wantLevel:   40,
		},
		{
			name:        "hvcC states profile, tier and level",
			init:        mediatest.MP4InitHEVCProfile(1, 90000, 3840, 2160, 2, 1, 153),
			wantProfile: 2,
			wantLevel:   153,
			wantTier:    1,
		},
		{
			name:        "av1C states seq_profile, seq_level_idx and seq_tier",
			init:        mediatest.MP4InitAV1Profile(1, 90000, 3840, 2160, 0, 13, 1),
			wantProfile: 0,
			wantLevel:   13,
			wantTier:    1,
		},
		{
			name:        "vpcC states profile and level",
			init:        mediatest.MP4InitVP9Profile(1, 90000, 1920, 1080, 2, 41),
			wantProfile: 2,
			wantLevel:   41,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), tc.init)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			track, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			p, ok := track.CodecProfile()
			if !ok {
				t.Fatal("the configuration record states a profile and none was read")
			}
			if p.Profile != tc.wantProfile || p.Level != tc.wantLevel || p.Tier != tc.wantTier {
				t.Errorf("profile/level/tier = %d/%d/%d, want %d/%d/%d",
					p.Profile, p.Level, p.Tier, tc.wantProfile, tc.wantLevel, tc.wantTier)
			}
		})
	}
}

// In MPEG-TS there is no configuration record: the profile and level are the
// first three bytes of the SPS for H.264, and the profile_tier_level the HEVC
// reader currently walks past.
func TestParse_CodecProfileFromTheBitstream(t *testing.T) {
	ts := mediatest.TSWithSPS(0, 3600, 25, mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
	}))
	info, err := Parse(ts, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	p, ok := track.CodecProfile()
	if !ok {
		t.Fatal("an H.264 SPS states a profile and none was read")
	}
	if p.Profile != 100 || p.Level != 40 {
		t.Errorf("profile/level = %d/%d, want 100/40 as the writer states", p.Profile, p.Level)
	}
}

// Media that states nothing must report nothing: an unstated level is not level
// zero, and a check comparing against it would report every stream as wrong.
func TestParse_NoConfigurationRecordStatesNoProfile(t *testing.T) {
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500),
		mediatest.MP4InitCodec(1, 90000, "vvc1", 1920, 1080))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, _ := info.Track(Video)
	if _, ok := track.CodecProfile(); ok {
		t.Error("a sample entry with no configuration record reported a profile")
	}
}
