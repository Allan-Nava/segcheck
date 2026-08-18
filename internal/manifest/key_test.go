package manifest

import (
	"bytes"
	"testing"
)

// SC-22: what EXT-X-KEY states, and what its silences mean.
//
// The IV attribute is optional, and its absence is a specific instruction rather
// than a missing value: the IV is then the segment's media sequence number. A parser
// that returned zeroes for it would have every such stream decrypt to noise.
func TestParseHLS_KeyAttributes(t *testing.T) {
	m := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:5
#EXT-X-KEY:METHOD=AES-128,URI="k.bin",IV=0x0123456789ABCDEF0123456789ABCDEF
#EXTINF:4.0,
s0.ts
#EXT-X-KEY:METHOD=AES-128,URI="k2.bin"
#EXTINF:4.0,
s1.ts
#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://x",KEYFORMAT="com.apple.streamingkeydelivery"
#EXTINF:4.0,
s2.ts
#EXT-X-KEY:METHOD=NONE
#EXTINF:4.0,
s3.ts
#EXT-X-ENDLIST
`
	pl, err := ParseHLS([]byte(m), "https://e.test/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Segments) != 4 {
		t.Fatalf("segments = %d, want 4", len(pl.Segments))
	}

	withIV := pl.Segments[0]
	if withIV.KeyMethod != "AES-128" {
		t.Errorf("method = %q, want AES-128", withIV.KeyMethod)
	}
	want := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}
	if !bytes.Equal(withIV.KeyIV, want) {
		t.Errorf("IV = %x, want %x", withIV.KeyIV, want)
	}

	// No IV attribute: the field stays empty so the caller knows to use the sequence
	// number, rather than being handed zeroes it would take for an IV.
	if noIV := pl.Segments[1]; noIV.KeyIV != nil {
		t.Errorf("IV = %x, want none", noIV.KeyIV)
	}
	// The key URI is resolved against the playlist, like every other URI: stored raw
	// it is unfetchable from anywhere but the playlist's own directory.
	if withIV.KeyURI != "https://e.test/k.bin" {
		t.Errorf("key URI = %q, want it resolved against the playlist", withIV.KeyURI)
	}
	if pl.Segments[2].KeyFormat != "com.apple.streamingkeydelivery" {
		t.Errorf("keyformat = %q", pl.Segments[2].KeyFormat)
	}
	// METHOD=NONE clears everything, including an IV a previous tag set.
	if none := pl.Segments[3]; none.KeyMethod != "" || none.KeyURI != "" || none.KeyIV != nil || none.KeyFormat != "" {
		t.Errorf("METHOD=NONE left %+v", none)
	}
}

// An IV that is not sixteen bytes of hexadecimal is not an IV. Returning a short or
// mis-sized one would decrypt every segment to noise, which is indistinguishable
// from a wrong key.
func TestParseHexIV(t *testing.T) {
	full := "0x0123456789ABCDEF0123456789ABCDEF"
	if got := parseHexIV(full); len(got) != 16 {
		t.Errorf("parseHexIV(%q) = %x, want 16 bytes", full, got)
	}
	// The prefix is conventional but not required, and its case does not matter.
	if got := parseHexIV("0X0123456789abcdef0123456789ABCDEF"); len(got) != 16 {
		t.Errorf("an 0X prefix was rejected: %x", got)
	}
	if got := parseHexIV("0123456789ABCDEF0123456789ABCDEF"); len(got) != 16 {
		t.Errorf("a bare hex IV was rejected: %x", got)
	}
	for _, in := range []string{"", "0x", "0xABCD", "0x" + "zz" + "0123456789ABCDEF0123456789ABCD", "not hex at all"} {
		if got := parseHexIV(in); got != nil {
			t.Errorf("parseHexIV(%q) = %x, want nil", in, got)
		}
	}
}

// SC-96: a real stream found this one.
//
// A DASH MPD declares protection with ContentProtection on the AdaptationSet, and the
// SegmentTemplate path already carried it onto every segment. The SegmentBase path did
// not — and nothing on the rendition itself said so either, so a single-file protected
// stream produced "segments are encrypted but the manifest declares no key", a BAD on a
// manifest that declares it three times over.
func TestParseDASH_SegmentBaseRenditionCarriesTheProtection(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <ContentProtection value="cbcs" schemeIdUri="urn:mpeg:dash:mp4protection:2011"/>
   <Representation id="v1" bandwidth="1000000" width="1280" height="720" codecs="avc1.4d401f">
    <BaseURL>v1.mp4</BaseURL>
    <SegmentBase indexRange="100-500" timescale="90000">
     <Initialization range="0-99"/>
    </SegmentBase>
   </Representation>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.Renditions) != 1 {
		t.Fatalf("renditions = %d, want 1", len(pl.Renditions))
	}
	r := pl.Renditions[0]
	if !r.SingleFile {
		t.Fatalf("the rendition is not marked single-file: %+v", r)
	}
	// The protection has to be on the rendition, because its segments do not exist yet:
	// they are synthesised later from the index, and that is where it gets stamped on.
	if r.KeyMethod == "" {
		t.Error("a protected SegmentBase rendition states no key method")
	}
}

// An unprotected rendition states none, which is what keeps this from marking every
// stream as encrypted.
func TestParseDASH_UnprotectedRenditionStatesNoKey(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <SegmentTemplate media="v-$Number$.m4s" initialization="v-init.mp4" duration="2" timescale="1"/>
   <Representation id="v1" bandwidth="1000000" width="1280" height="720" codecs="avc1.4d401f"/>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if pl.Renditions[0].KeyMethod != "" {
		t.Errorf("an unprotected rendition states %q", pl.Renditions[0].KeyMethod)
	}
}
