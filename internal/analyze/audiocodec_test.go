package analyze

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The audio counterpart of SC-74, and its classic is the quietest of the set.
// Declaring plain AAC-LC over HE-AAC content means every device that trusts the
// string decodes the base layer only: the whole ladder plays with half the
// intended top end, which sounds like a bad encode rather than a manifest error,
// so it is chased through the encoder for weeks.

// newAudioCodecOrigin serves one protected-free audio AdaptationSet whose
// declared codec string and whose real configuration can be set apart.
func newAudioCodecOrigin(t *testing.T, declaredCodec string, objectType, freqIndex, channelCfg int, sbr, ps bool) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">
      <SegmentTemplate timescale="48000" duration="48000" media="a-$Number$.m4s" initialization="a-init.mp4" startNumber="0">
        <SegmentTimeline><S t="0" d="48000" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a0" bandwidth="128000" codecs="%s" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>`, declaredCodec)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/a-init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4InitESDS(1, 48000, objectType, freqIndex, channelCfg, sbr, ps))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		n := segNumberIn("/seg" + lastDashNumber(req.URL.Path) + ".ts")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Segment(1, uint32(n), int64(n)*48000, 1024, 46, 4000))
	})
	return srv.URL + "/manifest.mpd"
}

// The classic: AAC-LC declared over an HE-AAC stream.
func TestRun_FindsAACLCDeclaredOverHEAAC(t *testing.T) {
	url := newAudioCodecOrigin(t, "mp4a.40.2", 5, 6, 2, true, false)

	res := runChannels(t, url)

	f, ok := findFinding(res, "codecstring", finding.BAD)
	if !ok {
		t.Fatalf("AAC-LC declared over HE-AAC was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "2") || !strings.Contains(f.Message, "5") {
		t.Errorf("the finding does not name both object types: %q", f.Message)
	}
}

// The reverse is *not* a defect, and getting that wrong is how this check first
// reported two public reference streams as broken.
//
// HE-AAC is normally signalled implicitly: the AudioSpecificConfig states an
// AAC-LC core — object type 2 — and the SBR data lives in the payload, where it
// is discovered at decode time. Explicit hierarchical signalling, with 5 or 29 in
// the configuration, is the exception rather than the rule. So `mp4a.40.5` over a
// configuration that says 2 is the ordinary way HE-AAC is carried, and segcheck
// cannot see the SBR data from the configuration alone. It says so instead.
func TestRun_HEAACSignalledImplicitlyIsNotADefect(t *testing.T) {
	url := newAudioCodecOrigin(t, "mp4a.40.5", 2, 3, 2, false, false)

	res := runChannels(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check != "codecstring" {
			continue
		}
		if f.Status != finding.OK {
			t.Errorf("implicitly signalled HE-AAC was reported %s: %s", f.Status, f.Message)
		}
		if strings.Contains(f.Message, "implicitly") {
			said = true
		}
	}
	if !said {
		t.Errorf("implicit SBR signalling was not explained:\n%s", dump(res))
	}
}

// A matching string is clean, and the check names the object type it verified.
func TestRun_AnAudioCodecStringThatMatchesIsClean(t *testing.T) {
	url := newAudioCodecOrigin(t, "mp4a.40.2", 2, 3, 2, false, false)

	res := runChannels(t, url)

	for _, f := range res.Findings {
		if f.Check == "codecstring" && f.Status != finding.OK {
			t.Errorf("a matching audio codec string produced %s: %s", f.Status, f.Message)
		}
	}
	if _, ok := findFinding(res, "codecstring", finding.OK); !ok {
		t.Fatalf("no codecstring finding at all:\n%s", dump(res))
	}
}

// A bare `mp4a` states no object type. That is a limit of the string, not a
// defect in the media, and calling it a mismatch would send someone re-encoding.
func TestRun_ABareMP4AIsNotVerifiable(t *testing.T) {
	url := newAudioCodecOrigin(t, "mp4a", 2, 3, 2, false, false)

	res := runChannels(t, url)

	f, ok := findFinding(res, "codecstring", finding.OK)
	if !ok {
		t.Fatalf("no codecstring finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "not verifiable") {
		t.Errorf("a bare mp4a did not say it was unverifiable: %q", f.Message)
	}
}
