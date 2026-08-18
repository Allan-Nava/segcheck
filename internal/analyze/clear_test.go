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

// This is the most expensive defect in the milestone and the quietest. The
// content ships unprotected, every player plays it, every check passes, nobody
// files a bug, and the first signal is a rights-holder audit.
//
// The only evidence is per-sample: `saiz` says how much encryption information
// each sample carries, and a sample carrying none carries none because there is
// none. The sample entry still says encv, the manifest still declares cenc, and
// the key server still hands out keys nothing uses.

// newClearOrigin serves a protected DASH rendition whose samples are as clear as
// the fixture says, however emphatically the manifest declares otherwise.
func newClearOrigin(t *testing.T, clearLeadingPerSegment []int) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
      <ContentProtection schemeIdUri="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"/>
      <SegmentTemplate timescale="%d" duration="%d" media="seg-$Number$.m4s" initialization="init.mp4" startNumber="0">
        <SegmentTimeline><S t="0" d="%d" r="%d"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="%d" width="1280" height="720" codecs="avc1.4d401f"/>
    </AdaptationSet>
  </Period>
</MPD>`, drmTimescale, drmSegTicks, drmSegTicks, len(clearLeadingPerSegment)-1, drmBandwidth)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4InitCENCTenc(1, drmTimescale, 1280, 720, "avc1", "cenc",
			"9eb4050d-e44b-4802-932e-27d75083e266", 0, 0))
	})
	for i, clear := range clearLeadingPerSegment {
		i, clear := i, clear
		mux.HandleFunc(fmt.Sprintf("/seg-%d.m4s", i), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4SegmentSAIZ(1, uint32(i), int64(i)*drmSegTicks,
				drmSampleDur, drmSamples, drmPayload, clear))
		})
	}
	return srv.URL + "/manifest.mpd"
}

func runClear(t *testing.T, url string, lead time.Duration) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.From = FromStart
	opts.ClearLead = lead
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// The audit finding: the manifest declares cenc and Widevine, the sample entry
// says encv, and not one sample is actually encrypted.
func TestRun_FindsProtectedMediaThatIsEntirelyInTheClear(t *testing.T) {
	url := newClearOrigin(t, []int{drmSamples, drmSamples, drmSamples, drmSamples})

	res := runClear(t, url, 0)

	f, ok := findFinding(res, "clear", finding.BAD)
	if !ok {
		t.Fatalf("media declared protected and shipped in the clear was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "clear") {
		t.Errorf("the clear finding does not say what it found: %q", f.Message)
	}
}

// Media that is encrypted throughout is what was asked for, and the check says
// so: "no findings" and "nobody looked" must not read the same.
func TestRun_FullyEncryptedMediaIsClean(t *testing.T) {
	url := newClearOrigin(t, []int{0, 0, 0, 0})

	res := runClear(t, url, 0)

	f, ok := findFinding(res, "clear", finding.OK)
	if !ok {
		t.Fatalf("fully encrypted media produced no clear finding:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "encrypted") {
		t.Errorf("the clear finding does not say what it verified: %q", f.Message)
	}
	for _, f := range res.Findings {
		if f.Check == "clear" && f.Status != finding.OK {
			t.Errorf("fully encrypted media produced %s: %s", f.Status, f.Message)
		}
	}
}

// A clear lead is a deliberate choice, so its length is measured and reported
// rather than judged — until an operator says what they asked for.
func TestRun_MeasuresAClearLead(t *testing.T) {
	// The whole first segment plus half the second: 2s + 1s = 3s at 25fps.
	url := newClearOrigin(t, []int{drmSamples, drmSamples / 2, 0, 0})

	res := runClear(t, url, 0)

	f, ok := findFinding(res, "clear", finding.OK)
	if !ok {
		t.Fatalf("a clear lead was not measured at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "3.0") {
		t.Errorf("the clear finding does not measure the 3s lead: %q", f.Message)
	}
}

// With --clear-lead the measurement becomes a claim to check, in both
// directions: too long leaves content readable that was meant to be protected,
// too short makes a player wait for a licence before it can show anything.
func TestRun_FindsAClearLeadThatIsNotTheOneAskedFor(t *testing.T) {
	url := newClearOrigin(t, []int{drmSamples, drmSamples, 0, 0}) // 4s of clear lead

	res := runClear(t, url, 2*time.Second)

	f, ok := findFinding(res, "clear", finding.BAD)
	if !ok {
		t.Fatalf("a clear lead twice the length asked for was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "4.0") || !strings.Contains(f.Message, "2") {
		t.Errorf("the clear finding does not put the measurement beside the limit: %q", f.Message)
	}
}

// Unprotected media that never claimed to be protected is not a defect and must
// gain no row: this check is about a promise broken, not about plain content.
func TestRun_ClearUnprotectedMediaHasNoClearFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "clear") {
		t.Errorf("plain unprotected media produced a clear finding:\n%s", dump(res))
	}
}
