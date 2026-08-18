package analyze

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// SC-95: partial encryption is a different shape of blindness, and the dangerous one.
//
// AES-128 encrypts the whole segment, so nothing parses and every check honestly says it
// could not look. SAMPLE-AES and CENC encrypt only the samples: the container parses, the
// timing checks work perfectly, and the bitstream readers succeed and find nothing. A
// manifest correctly declaring CC1 over SAMPLE-AES media then gets a BAD saying the
// captions are missing — a defect reported against a stream that is entirely correct.

// The declared protection is named, and what it leaves unverifiable is said outright.
func TestCheckEncryption_NamesTheScheme(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{
			seg:    manifest.Segment{KeyMethod: "SAMPLE-AES", Sequence: 0},
			parsed: true,
			info: media.SegmentInfo{Container: media.ContainerTS, Tracks: []media.Track{{
				Kind: media.Video, Codec: "h264", Timescale: 90000, HasPTS: true, Samples: 50,
			}}},
		}},
	}
	out := checkEncryption([]*renditionData{rd})
	f, ok := findingIn(out, finding.OK)
	if !ok {
		t.Fatalf("want an OK finding, got %+v", out)
	}
	if !strings.Contains(f.Message, "SAMPLE-AES") {
		t.Errorf("the finding does not name the scheme: %q", f.Message)
	}
	if !strings.Contains(f.Message, "samples") {
		t.Errorf("the finding does not say what is protected: %q", f.Message)
	}
}

// The CENC scheme comes from the media rather than the manifest, and is named the same way.
func TestCheckEncryption_NamesTheCENCScheme(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{
			seg:    manifest.Segment{KeyMethod: "SAMPLE-AES-CTR", Sequence: 0},
			parsed: true,
			info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{{
				Kind: media.Video, Codec: "h264", Timescale: 90000, HasPTS: true, Samples: 50,
				Encrypted: true, SamplesEncrypted: true, Protection: "cbcs",
			}}},
		}},
	}
	out := checkEncryption([]*renditionData{rd})
	f, ok := findingIn(out, finding.OK)
	if !ok {
		t.Fatalf("want an OK finding, got %+v", out)
	}
	if !strings.Contains(f.Message, "cbcs") {
		t.Errorf("the finding does not name the scheme: %q", f.Message)
	}
}

// The defect: a manifest that correctly declares CC1 over SAMPLE-AES media. The SEI is
// inside the encrypted samples, so nothing can confirm or deny the captions — and a BAD
// saying they are missing sends an operator hunting a phantom.
func TestCheckCaptions_SampleAESIsNotADefect(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video,
			Captions: []manifest.Caption{{InstreamID: "CC1"}}},
		segs: []segmentData{{
			seg:    manifest.Segment{KeyMethod: "SAMPLE-AES", Sequence: 0},
			parsed: true,
			info: media.SegmentInfo{Container: media.ContainerTS, Tracks: []media.Track{{
				Kind: media.Video, Codec: "h264",
				// The scan ran over ciphertext and found nothing, which is exactly the
				// trap: it reports success.
				Captions: media.CaptionPresence{Scanned: true},
			}}},
		}},
	}
	out := checkCaptions([]*renditionData{rd})
	if f, ok := findingIn(out, finding.BAD); ok {
		t.Errorf("SAMPLE-AES media was reported as missing its captions: %q", f.Message)
	}
	f, ok := findingIn(out, finding.ERROR)
	if !ok {
		t.Fatalf("want an ERROR saying segcheck could not look, got %+v", out)
	}
	if !strings.Contains(f.Message, "could not") {
		t.Errorf("the finding does not say nobody could look: %q", f.Message)
	}
}

// A keyframe verdict read out of an encrypted bitstream is the same trap from the other
// side: the walk finds no random access point because it cannot read one.
func TestCheckKeyframe_SampleAESIsNotADefect(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{
			seg:    manifest.Segment{KeyMethod: "SAMPLE-AES", Sequence: 0},
			parsed: true,
			info: media.SegmentInfo{Container: media.ContainerTS, Tracks: []media.Track{{
				Kind: media.Video, Codec: "h264",
				KeyframeKnown: true, KeyframeScanned: true, HasKeyframe: false,
			}}},
		}},
	}
	for _, f := range checkKeyframe([]*renditionData{rd}) {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("SAMPLE-AES media produced %s: %s", f.Status, f.Message)
		}
	}
}

// A CMAF track whose samples are protected but whose packager stated no scheme still has
// to be reported: the entry type says the samples are ciphertext whatever it is called.
func TestPartialEncryption_NoSchemeStated(t *testing.T) {
	sd := segmentData{
		parsed: true,
		info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{{
			Kind: media.Video, Encrypted: true, SamplesEncrypted: true,
		}}},
	}
	if got := partialEncryption(sd); got != "sample encryption" {
		t.Errorf("partialEncryption = %q, want a generic name", got)
	}

	// And unprotected media states nothing, which is what keeps this from firing on
	// every stream.
	clear := segmentData{parsed: true, info: media.SegmentInfo{
		Container: media.ContainerTS,
		Tracks:    []media.Track{{Kind: media.Video}},
	}}
	if got := partialEncryption(clear); got != "" {
		t.Errorf("partialEncryption on clear media = %q", got)
	}
	if bitstreamOpaque(clear) {
		t.Error("clear media was reported as an opaque bitstream")
	}
	// Full-segment AES-128 is a different problem: nothing parses, so this is not it.
	full := segmentData{parsed: true, seg: manifest.Segment{KeyMethod: "AES-128"},
		info: media.SegmentInfo{Container: media.ContainerTS}}
	if got := partialEncryption(full); got != "" {
		t.Errorf("AES-128 was reported as partial encryption: %q", got)
	}
}
