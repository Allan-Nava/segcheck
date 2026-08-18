package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The scheme protecting the samples is a four-character code in `schm` and a
// set of defaults in `tenc`, and the difference between two of them is invisible
// anywhere else: cbcs and cenc encrypt the same media with the same key and
// differ by a box field, so MPDs get copied between them and the content plays
// nowhere.
//
// tenc matters beyond confirming the scheme name. A pattern — crypt_byte_block
// over skip_byte_block — belongs to cbcs and cens and must not appear under
// cenc or cbc1, so the two halves of the container can be checked against each
// other even when the manifest says nothing at all.
func TestParse_TencDefaults(t *testing.T) {
	for _, tc := range []struct {
		name        string
		scheme      string
		crypt, skip int
		wantPattern bool
	}{
		{"cbcs carries a 1:9 pattern", "cbcs", 1, 9, true},
		{"cenc carries none", "cenc", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init := mediatest.MP4InitCENCTenc(1, 90000, 640, 360, "avc1", tc.scheme,
				"9eb4050d-e44b-4802-932e-27d75083e266", tc.crypt, tc.skip)
			info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			track, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			if track.Protection != tc.scheme {
				t.Errorf("Protection = %q, want %q", track.Protection, tc.scheme)
			}
			if track.KeyID != "9eb4050d-e44b-4802-932e-27d75083e266" {
				t.Errorf("KeyID = %q, want the default_KID tenc states", track.KeyID)
			}
			crypt, skip, has := track.CryptPattern()
			if has != tc.wantPattern {
				t.Errorf("CryptPattern present = %v, want %v", has, tc.wantPattern)
			}
			if has && (crypt != tc.crypt || skip != tc.skip) {
				t.Errorf("pattern = %d:%d, want %d:%d", crypt, skip, tc.crypt, tc.skip)
			}
		})
	}
}

// Unprotected media states none of this, and a zero KID must not read as one.
func TestParse_NoTencMeansNoKeyID(t *testing.T) {
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500),
		mediatest.MP4Init(1, 90000, "video", 640, 360))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, _ := info.Track(Video)
	if track.KeyID != "" || track.Protection != "" {
		t.Errorf("unprotected media gained a key id %q and scheme %q", track.KeyID, track.Protection)
	}
	if _, _, has := track.CryptPattern(); has {
		t.Error("unprotected media gained a crypt pattern")
	}
}
