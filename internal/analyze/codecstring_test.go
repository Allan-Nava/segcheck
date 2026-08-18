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

// The two halves of a CODECS string fail in opposite directions, and a check
// that reported them the same way would be useless for deciding what to do.
//
// A level declared *below* the media's is a device that reads the manifest,
// decides it cannot decode this, and never asks for a segment: the rung is dark
// for that device and nothing in any log says why. A profile or level declared
// *above* the media's excludes devices that could have played it perfectly —
// nobody sees an error, the top rung just has fewer viewers than it should.

type codecRung struct {
	name     string
	declared string // the CODECS attribute
	profile  int
	level    int
}

func newCodecOrigin(t *testing.T, rungs []codecRung) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
		for i, r := range rungs {
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=%q\n%s/index.m3u8\n",
				syntheticBandwidth+i*1000, r.declared, r.name)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})
	for _, r := range rungs {
		r := r
		mux.HandleFunc("/"+r.name+"/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
			b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
			for i := 0; i < 2; i++ {
				fmt.Fprintf(&b, "#EXTINF:2.000,\nseg%d.m4s\n", i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(b.String()))
		})
		mux.HandleFunc("/"+r.name+"/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4InitAVCProfile(1, drmTimescale, 1280, 720, r.profile, 0, r.level))
		})
		for i := 0; i < 2; i++ {
			i := i
			mux.HandleFunc(fmt.Sprintf("/%s/seg%d.m4s", r.name, i), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = w.Write(mediatest.MP4Segment(1, uint32(i), int64(i)*drmSegTicks, drmSampleDur, drmSamples, 6000))
			})
		}
	}
	return srv.URL + "/master.m3u8"
}

func runCodec(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 2
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// A level declared below the media's: the device never asks for a segment.
func TestRun_FindsALevelDeclaredBelowTheMedia(t *testing.T) {
	// avc1.4d001e is Main profile at level 3.0; the media codes level 4.0.
	url := newCodecOrigin(t, []codecRung{{name: "hd", declared: "avc1.4d001e", profile: 0x4d, level: 40}})

	res := runCodec(t, url)

	f, ok := findFinding(res, "codecstring", finding.BAD)
	if !ok {
		t.Fatalf("a level declared below the media's was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "level") {
		t.Errorf("the codecstring finding does not name the field: %q", f.Message)
	}
}

// A level declared above the media's costs viewers rather than playback, so it
// is a warning and the message has to say which way round it is.
func TestRun_ReportsALevelDeclaredAboveTheMedia(t *testing.T) {
	// avc1.4d0033 is Main at level 5.1; the media codes level 3.0.
	url := newCodecOrigin(t, []codecRung{{name: "hd", declared: "avc1.4d0033", profile: 0x4d, level: 30}})

	res := runCodec(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "codecstring" && f.Status == finding.WARN {
			said = true
			if !strings.Contains(f.Message, "above") {
				t.Errorf("the finding does not say which way round it is: %q", f.Message)
			}
		}
	}
	if !said {
		t.Errorf("a level declared above the media's was not reported:\n%s", dump(res))
	}
}

// A profile declared above the media's excludes devices silently.
func TestRun_FindsAProfileThatIsNotTheMediasProfile(t *testing.T) {
	// avc1.640028 is High at level 4.0; the media codes Main (0x4d).
	url := newCodecOrigin(t, []codecRung{{name: "hd", declared: "avc1.640028", profile: 0x4d, level: 40}})

	res := runCodec(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "codecstring" && strings.Contains(f.Message, "profile") {
			said = true
		}
	}
	if !said {
		t.Errorf("a profile the media does not code was not reported:\n%s", dump(res))
	}
}

// A string that matches is healthy, and the check quotes what it verified.
func TestRun_ACodecStringThatMatchesIsClean(t *testing.T) {
	url := newCodecOrigin(t, []codecRung{{name: "hd", declared: "avc1.4d0028", profile: 0x4d, level: 40}})

	res := runCodec(t, url)

	for _, f := range res.Findings {
		if f.Check == "codecstring" && f.Status != finding.OK {
			t.Errorf("a matching codec string produced %s: %s", f.Status, f.Message)
		}
	}
	if _, ok := findFinding(res, "codecstring", finding.OK); !ok {
		t.Fatalf("no codecstring finding at all:\n%s", dump(res))
	}
}

// A string segcheck cannot decompose is a limit of this tool, and reporting it
// as a mismatch would send someone re-encoding perfectly good media.
func TestRun_AnUndecomposableCodecStringIsNotVerifiable(t *testing.T) {
	url := newCodecOrigin(t, []codecRung{{name: "hd", declared: "avc1", profile: 0x4d, level: 40}})

	res := runCodec(t, url)

	f, ok := findFinding(res, "codecstring", finding.OK)
	if !ok {
		t.Fatalf("no codecstring finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "not verifiable") {
		t.Errorf("an undecomposable string did not say so: %q", f.Message)
	}
	for _, f := range res.Findings {
		if f.Check == "codecstring" && f.Status != finding.OK {
			t.Errorf("an undecomposable string produced %s: %s", f.Status, f.Message)
		}
	}
}
