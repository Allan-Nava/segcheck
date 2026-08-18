package manifest

import (
	"testing"
	"time"
)

// VIDEO-RANGE is the manifest's claim about dynamic range, and until now the HLS
// reader did not parse it at all. It is the attribute a player uses to decide
// whether to ask the display for HDR before it has decoded a single frame.
func TestParseHLS_VideoRange(t *testing.T) {
	master := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-STREAM-INF:BANDWIDTH=8000000,RESOLUTION=3840x2160,CODECS="hvc1.2.4.L153.B0",VIDEO-RANGE=PQ
uhd/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.4d401f",VIDEO-RANGE=SDR
hd/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=640x360,CODECS="avc1.4d401f"
sd/index.m3u8
`
	pl, err := ParseHLS([]byte(master), "https://cdn.example/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	want := []string{"PQ", "SDR", ""}
	for i, r := range pl.Renditions {
		if r.VideoRange != want[i] {
			t.Errorf("rendition %d VideoRange = %q, want %q", i, r.VideoRange, want[i])
		}
	}
	// An absent attribute is not SDR. The specification's default is SDR for a
	// *player*, but for a checker "the manifest did not say" and "the manifest
	// says SDR" are different claims, and only the second can be wrong.
	if pl.Renditions[2].VideoRange != "" {
		t.Error("an absent VIDEO-RANGE was filled in with a default")
	}
}

// DASH states the same thing as a CICP code point in a Supplemental or Essential
// property, which is a number rather than a name — and the number is exactly what
// the bitstream states, so the comparison needs no translation at all.
func TestParseDASH_TransferCharacteristicDescriptor(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <EssentialProperty schemeIdUri="urn:mpeg:mpegB:cicp:TransferCharacteristics" value="16"/>
    <SupplementalProperty schemeIdUri="urn:mpeg:mpegB:cicp:ColourPrimaries" value="9"/>
    <SupplementalProperty schemeIdUri="urn:mpeg:mpegB:cicp:MatrixCoefficients" value="9"/>
    <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="3840" height="2160"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if r.Transfer != 16 || r.Primaries != 9 || r.Matrix != 9 {
		t.Errorf("declared colour = %d/%d/%d, want 16/9/9", r.Transfer, r.Primaries, r.Matrix)
	}
	// PQ by name too, so one check can compare against either spelling.
	if r.VideoRange != "PQ" {
		t.Errorf("VideoRange = %q, want PQ derived from transfer 16", r.VideoRange)
	}
}

// An MPD that names no colour states none, and a zero must not read as a code
// point: 0 is reserved in all three registries.
func TestParseDASH_NoColourDescriptorsMeansNoClaim(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="640" height="360"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if r.Transfer != 0 || r.VideoRange != "" {
		t.Errorf("an MPD with no colour descriptors claimed transfer %d, range %q", r.Transfer, r.VideoRange)
	}
}
