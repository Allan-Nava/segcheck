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

// Which DRM systems can unlock a stream is claimed twice: by the manifest, in
// ContentProtection or KEYFORMAT, and by the initialisation segment, in its
// pssh boxes. Only the second is what a player actually gets.
//
// A ladder whose MPD advertises PlayReady and whose CMAF init carries only a
// Widevine pssh plays perfectly on Chrome and dies on Xbox and Edge. The
// manifest reads impeccably on the way down, every segment fetches, every
// timing check passes, and the failure arrives as "your app is broken on our
// devices" from a platform certification team.

const (
	drmTimescale = uint32(90000)
	drmSegTicks  = int64(180000)
	drmSamples   = 50
	drmSampleDur = uint32(3600)
	drmPayload   = 12000
	drmBandwidth = 60_000
)

// newDRMOrigin serves a DASH stream whose MPD declares one set of systems and
// whose init advertises another, which is exactly the shape of the incident.
func newDRMOrigin(t *testing.T, declared []string, scheme string, present []string, initScheme string) string {
	return newDRMOriginPSSH(t, declared, nil, scheme, present, initScheme)
}

// newDRMOriginPSSH is the same, with control over which declared systems carry
// their key-acquisition data in the manifest itself.
func newDRMOriginPSSH(t *testing.T, declared, inManifest []string, scheme string, present []string, initScheme string) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		var cp strings.Builder
		if scheme != "" {
			fmt.Fprintf(&cp, `      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value=%q/>`+"\n", scheme)
		}
		carries := map[string]bool{}
		for _, s := range inManifest {
			carries[s] = true
		}
		for _, s := range declared {
			if carries[s] {
				fmt.Fprintf(&cp, `      <ContentProtection schemeIdUri="urn:uuid:%s"><cenc:pssh>AAAANHBzc2gAAAAA</cenc:pssh></ContentProtection>`+"\n", s)
				continue
			}
			fmt.Fprintf(&cp, `      <ContentProtection schemeIdUri="urn:uuid:%s"/>`+"\n", s)
		}
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:cenc="urn:mpeg:cenc:2013" type="static" mediaPresentationDuration="PT8S">
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
%s      <SegmentTemplate timescale="%d" duration="%d" media="seg-$Number$.m4s" initialization="init.mp4" startNumber="0">
        <SegmentTimeline><S t="0" d="%d" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="%d" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`, cp.String(), drmTimescale, drmSegTicks, drmSegTicks, drmBandwidth)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})

	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		if len(present) == 0 && initScheme == "" {
			_, _ = w.Write(mediatest.MP4Init(1, drmTimescale, "video", 1280, 720))
			return
		}
		_, _ = w.Write(mediatest.MP4InitCENCWithPSSH(1, drmTimescale, 1280, 720, "avc1", initScheme, present...))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := segNumberIn(strings.Replace(strings.Replace(r.URL.Path, "/seg-", "/seg", 1), ".m4s", ".ts", 1))
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Segment(1, uint32(n), int64(n)*drmSegTicks, drmSampleDur, drmSamples, drmPayload))
	})
	return srv.URL + "/manifest.mpd"
}

func runDRM(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 2
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// The incident: the manifest promises two systems and the init delivers one.
func TestRun_FindsADRMSystemDeclaredButNotInTheInit(t *testing.T) {
	url := newDRMOrigin(t,
		[]string{mediatest.WidevineSystemID, mediatest.PlayReadySystemID}, "cenc",
		[]string{mediatest.WidevineSystemID}, "cenc")

	res := runDRM(t, url)

	f, ok := findFinding(res, "drm", finding.BAD)
	if !ok {
		t.Fatalf("a declared DRM system missing from the init was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "playready") {
		t.Errorf("the drm finding does not name the missing system: %q", f.Message)
	}
}

// A manifest and an init that agree are healthy, and the check has to say so:
// "the systems match" is the thing a certification ticket needs quoting.
func TestRun_DRMSystemsThatMatchAreClean(t *testing.T) {
	url := newDRMOrigin(t,
		[]string{mediatest.WidevineSystemID, mediatest.PlayReadySystemID}, "cenc",
		[]string{mediatest.WidevineSystemID, mediatest.PlayReadySystemID}, "cenc")

	res := runDRM(t, url)

	f, ok := findFinding(res, "drm", finding.OK)
	if !ok {
		t.Fatalf("matching systems produced no drm finding:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "widevine") || !strings.Contains(f.Message, "playready") {
		t.Errorf("the drm finding does not list what it verified: %q", f.Message)
	}
	for _, f := range res.Findings {
		if f.Check == "drm" && f.Status != finding.OK {
			t.Errorf("matching systems produced %s: %s", f.Status, f.Message)
		}
	}
}

// A system in the init that the manifest never promised is not a playback
// failure — a player picks the system it knows — but it is a packaging surprise
// worth reporting, and it is the shape of a key rotation half-applied.
func TestRun_ReportsADRMSystemInTheInitTheManifestNeverPromised(t *testing.T) {
	url := newDRMOrigin(t,
		[]string{mediatest.WidevineSystemID}, "cenc",
		[]string{mediatest.WidevineSystemID, mediatest.PlayReadySystemID}, "cenc")

	res := runDRM(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "drm" && strings.Contains(f.Message, "does not declare") {
			said = true
			if f.Status != finding.WARN {
				t.Errorf("an undeclared system in the init was reported %s, want WARN", f.Status)
			}
		}
	}
	if !said {
		t.Errorf("a system present only in the init was not reported:\n%s", dump(res))
	}
}

// DASH-IF's guidance puts the key-acquisition data in the MPD, and every real
// multi-DRM vector does: the initialisation segment then carries no pssh at all
// and is right not to. Demanding one reported Axinom's reference stream as
// missing both its systems, which is where this was found.
func TestRun_APSSHInTheManifestIsNotAMissingPSSHInTheInit(t *testing.T) {
	systems := []string{mediatest.WidevineSystemID, mediatest.PlayReadySystemID}
	url := newDRMOriginPSSH(t, systems, systems, "cenc", nil, "cenc")

	res := runDRM(t, url)

	for _, f := range res.Findings {
		if f.Check == "drm" && f.Status != finding.OK {
			t.Errorf("a manifest carrying its own pssh was reported as %s: %s", f.Status, f.Message)
		}
	}
	f, ok := findFinding(res, "drm", finding.OK)
	if !ok {
		t.Fatalf("no drm finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "manifest") {
		t.Errorf("the drm finding does not say where the key data came from: %q", f.Message)
	}
}

// The real incident survives the fix: the manifest declares two systems, carries
// neither one's pssh itself, and the init carries only one of them. The init is
// the acquisition path here, and one system has nothing to acquire from.
func TestRun_FindsAMissingPSSHWhenTheInitIsTheAcquisitionPath(t *testing.T) {
	url := newDRMOriginPSSH(t,
		[]string{mediatest.WidevineSystemID, mediatest.PlayReadySystemID}, nil, "cenc",
		[]string{mediatest.WidevineSystemID}, "cenc")

	res := runDRM(t, url)

	f, ok := findFinding(res, "drm", finding.BAD)
	if !ok {
		t.Fatalf("a declared system missing from an init that carries others was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "playready") {
		t.Errorf("the drm finding does not name the missing system: %q", f.Message)
	}
}

// Unprotected content makes no claim and carries no pssh. There is nothing to
// compare and nothing to say.
func TestRun_UnprotectedContentHasNoDRMFinding(t *testing.T) {
	url := newDRMOrigin(t, nil, "", nil, "")

	res := runDRM(t, url)

	if hasCheck(res, "drm") {
		t.Errorf("unprotected content produced a drm finding:\n%s", dump(res))
	}
}
