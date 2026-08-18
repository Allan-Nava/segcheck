package analyze

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// CHANNELS is the claim a player acts on before it decodes anything: a receiver
// reads "6" and selects a surround output, then upmixes stereo into it. The
// defect is audible on exactly the systems that were the reason for shipping
// surround in the first place.
//
// The subtlety is HE-AAC v2, which codes a mono core that Parametric Stereo
// renders as stereo — so a declared 2 over a coded 1 is correct. That exemption
// used to be granted on the strength of the codec *string* saying mp4a.40.29,
// which forgave a genuine mismatch on any stream that merely claimed to be
// HE-AAC v2. The AudioSpecificConfig states PS outright, so the exemption is
// granted on evidence now.

type channelRung struct {
	declaredChannels int
	declaredCodec    string
	objectType       int
	freqIndex        int
	channelConfig    int
	ps               bool
}

func newChannelsOrigin(t *testing.T, r channelRung) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">
      <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="%d"/>
      <SegmentTemplate timescale="48000" duration="48000" media="a-$Number$.m4s" initialization="a-init.mp4" startNumber="0">
        <SegmentTimeline><S t="0" d="48000" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a0" bandwidth="128000" codecs="%s" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>`, r.declaredChannels, r.declaredCodec)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/a-init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4InitESDS(1, 48000, r.objectType, r.freqIndex, r.channelConfig, r.ps, r.ps))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		n := segNumberIn("/seg" + lastDashNumber(req.URL.Path) + ".ts")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Segment(1, uint32(n), int64(n)*48000, 1024, 46, 4000))
	})
	return srv.URL + "/manifest.mpd"
}

func runChannels(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 2
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// HE-AAC v2 really does code one channel and render two, and the manifest is
// right to say two. The configuration says so, so the exemption is earned.
func TestRun_HEAACv2MonoCoreDeclaredAsStereoIsCorrect(t *testing.T) {
	url := newChannelsOrigin(t, channelRung{
		declaredChannels: 2, declaredCodec: "mp4a.40.29",
		objectType: 29, freqIndex: 6, channelConfig: 1, ps: true,
	})

	res := runChannels(t, url)

	for _, f := range res.Findings {
		if f.Check == "audio" && f.Status != finding.OK {
			t.Errorf("a correct HE-AAC v2 rendition produced %s: %s", f.Status, f.Message)
		}
	}
}

// The hole SC-81 closes. The codec string says HE-AAC v2, so the old exemption
// applied on its word alone — but the configuration codes a plain AAC-LC mono
// core with no Parametric Stereo at all, so the manifest's "2" is simply wrong
// and every receiver upmixes mono into a stereo pair.
func TestRun_FindsAMonoCoreDeclaredStereoWithNoParametricStereo(t *testing.T) {
	url := newChannelsOrigin(t, channelRung{
		declaredChannels: 2, declaredCodec: "mp4a.40.29",
		objectType: 2, freqIndex: 3, channelConfig: 1, ps: false,
	})

	res := runChannels(t, url)

	f, ok := findFinding(res, "audio", finding.BAD)
	if !ok {
		t.Fatalf("a mono track declared stereo was forgiven on the codec string's word:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "mono") {
		t.Errorf("the audio finding does not name what the media carries: %q", f.Message)
	}
}

// The classic: stereo advertised as 5.1. A receiver selects a surround output and
// upmixes into it, so it is audible on exactly the systems surround was for.
func TestRun_FindsStereoDeclaredAsSurround(t *testing.T) {
	url := newChannelsOrigin(t, channelRung{
		declaredChannels: 6, declaredCodec: "mp4a.40.2",
		objectType: 2, freqIndex: 3, channelConfig: 2,
	})

	res := runChannels(t, url)

	f, ok := findFinding(res, "audio", finding.BAD)
	if !ok {
		t.Fatalf("stereo declared as 5.1 was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "5.1") && !strings.Contains(f.Message, "6") {
		t.Errorf("the audio finding does not quote the declared layout: %q", f.Message)
	}
}

// And a plain match, so the check says what it verified.
func TestRun_AChannelCountThatMatchesIsClean(t *testing.T) {
	url := newChannelsOrigin(t, channelRung{
		declaredChannels: 2, declaredCodec: "mp4a.40.2",
		objectType: 2, freqIndex: 3, channelConfig: 2,
	})

	res := runChannels(t, url)

	for _, f := range res.Findings {
		if f.Check == "audio" && f.Status != finding.OK {
			t.Errorf("a matching channel count produced %s: %s", f.Status, f.Message)
		}
	}
}
