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
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// resolveClock is the one place segcheck asks a stream what time it thinks it is,
// and it has to speak four schemes and decline the rest by name. Each is a
// different shape of answer — a header, a body, an attribute — and the one it
// cannot speak has to say so rather than fall back to the clock the element
// exists to distrust.
func TestResolveClock(t *testing.T) {
	answer := time.Date(2026, 8, 10, 12, 0, 30, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("/iso", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(answer.Format(time.RFC3339Nano)))
	})
	mux.HandleFunc("/head", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", answer.Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/nodate", func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Date"] = nil
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/garbage", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("half past four"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	local := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name        string
		sources     []manifest.UTCTiming
		wantOK      bool
		wantSkewMin time.Duration
		unsupported int
	}{
		{
			name:    "http-iso reads the body",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:http-iso:2014", Value: srv.URL + "/iso"}},
			wantOK:  true, wantSkewMin: 29 * time.Second,
		},
		{
			name:    "http-xsdate reads the body too",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:http-xsdate:2014", Value: srv.URL + "/iso"}},
			wantOK:  true, wantSkewMin: 29 * time.Second,
		},
		{
			name:    "http-head reads the Date header",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:http-head:2014", Value: srv.URL + "/head"}},
			wantOK:  true, wantSkewMin: 29 * time.Second,
		},
		{
			name:    "direct carries the time inline",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:direct:2014", Value: answer.Format(time.RFC3339)}},
			wantOK:  true, wantSkewMin: 29 * time.Second,
		},
		{
			name: "the MPD's own fallback order is honoured",
			sources: []manifest.UTCTiming{
				{Scheme: "urn:mpeg:dash:utc:http-iso:2014", Value: srv.URL + "/garbage"},
				{Scheme: "urn:mpeg:dash:utc:http-iso:2014", Value: srv.URL + "/iso"},
			},
			wantOK: true, wantSkewMin: 29 * time.Second,
		},
		{
			// A protocol a zero-dependency binary does not speak. Naming it beats
			// falling back to the local clock in silence.
			name: "ntp is named and skipped",
			sources: []manifest.UTCTiming{
				{Scheme: "urn:mpeg:dash:utc:ntp:2014", Value: "time.example"},
				{Scheme: "urn:mpeg:dash:utc:sntp:2014", Value: "time.example"},
			},
			unsupported: 2,
		},
		{
			name:    "a head response with no Date header",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:http-head:2014", Value: srv.URL + "/nodate"}},
		},
		{
			name:    "a body that is not a time",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:http-iso:2014", Value: srv.URL + "/garbage"}},
		},
		{
			name:    "direct with a value that is not a time",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:direct:2014", Value: "soon"}},
		},
		{
			name:    "a source that will not fetch",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:http-iso:2014", Value: "http://127.0.0.1:1/iso"}},
		},
		{
			name:    "a head source that will not fetch",
			sources: []manifest.UTCTiming{{Scheme: "urn:mpeg:dash:utc:http-head:2014", Value: "http://127.0.0.1:1/head"}},
		},
		{name: "no sources at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveClock(context.Background(), client, tc.sources, local)
			if got.ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", got.ok, tc.wantOK)
			}
			if tc.wantOK && absDuration(got.skew) < tc.wantSkewMin {
				t.Errorf("skew = %v, want at least %v", got.skew, tc.wantSkewMin)
			}
			if len(got.unsupported) != tc.unsupported {
				t.Errorf("unsupported = %v, want %d entries", got.unsupported, tc.unsupported)
			}
		})
	}
}

// waitFor is the real wait every run that is not a test uses, and the branch that
// matters is the cancelled one: Ctrl-C during a --watch must stop the loop rather
// than sit out the interval.
func TestWaitFor(t *testing.T) {
	if err := waitFor(context.Background(), time.Millisecond); err != nil {
		t.Errorf("waitFor returned %v on a completed wait", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitFor(ctx, time.Hour); err == nil {
		t.Error("waitFor sat out an hour on a cancelled context")
	}
}

// The message helpers quote the manifest in whichever form it used, rather than
// segcheck's normalisation of it.
func TestVideoRangeMessageHelpers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rangeName string
		transfer  int
		want      string
	}{
		{"both", "PQ", 16, "PQ (transfer characteristic 16)"},
		{"a name only", "SDR", 0, "VIDEO-RANGE=SDR"},
		{"a code point only", "", 18, "transfer characteristic 18"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaredClaim(tc.rangeName, tc.transfer); got != tc.want {
				t.Errorf("declaredClaim = %q, want %q", got, tc.want)
			}
		})
	}
	// rangeLabel names what it can and falls back to the dynamic-range name.
	if got := rangeLabel(16); got != "PQ" {
		t.Errorf("rangeLabel(16) = %q, want PQ", got)
	}
	if got := rangeLabel(17); got != manifest.RangeSDR {
		t.Errorf("rangeLabel(17) = %q, want SDR for an unnamed assigned curve", got)
	}
}

// VideoRangeForTransfer is asymmetric on purpose: PQ and HLG are two specific
// code points, and SDR covers everything else assigned.
func TestVideoRangeForTransfer(t *testing.T) {
	for _, tc := range []struct {
		transfer int
		want     string
	}{
		{16, manifest.RangePQ},
		{18, manifest.RangeHLG},
		{1, manifest.RangeSDR},
		{6, manifest.RangeSDR},
		{14, manifest.RangeSDR},
		{0, ""},
		{2, ""},
		{3, ""},
	} {
		if got := manifest.VideoRangeForTransfer(tc.transfer); got != tc.want {
			t.Errorf("VideoRangeForTransfer(%d) = %q, want %q", tc.transfer, got, tc.want)
		}
	}
}

// A rendition whose segments parsed but state no timeline cannot answer the
// questions built on one, and every caller of these treats the false as "stay
// quiet" rather than as a zero.
func TestTimelessSegmentsAnswerNothing(t *testing.T) {
	// Parsed, with a track that carries no timestamps at all.
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{
			parsed: true,
			info:   media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video, Codec: "h264"}}},
		}},
	}
	if _, ok := firstMediaStart(rd.segs); ok {
		t.Error("firstMediaStart answered from a track with no timestamps")
	}
	if _, ok := clearLeadSeconds(rd, 4); ok {
		t.Error("clearLeadSeconds answered with no frame duration to convert by")
	}
	if got := measuredDurations(rd); len(got) != 0 {
		t.Errorf("measuredDurations = %v, want none", got)
	}
	if _, _, _, ok := measuredBitrates(rd); ok {
		t.Error("measuredBitrates answered from a track with no duration")
	}
	// A codec and a resolution are still readable, and should be read.
	if _, ok := videoCodecOf(rd); !ok {
		t.Error("videoCodecOf declined a track that states its codec")
	}
	// Unparsed segments are skipped by every one of them.
	unparsed := &renditionData{segs: []segmentData{{}}}
	if _, ok := firstMediaStart(unparsed.segs); ok {
		t.Error("firstMediaStart answered from an unparsed segment")
	}
	if _, ok := colourOf(unparsed); ok {
		t.Error("colourOf answered from an unparsed segment")
	}
}

func TestFirstPollError(t *testing.T) {
	if got := firstPollError([]observation{{}, {err: errFake("second")}}); got == nil || got.Error() != "second" {
		t.Errorf("firstPollError = %v, want the first error present", got)
	}
	if got := firstPollError([]observation{{}, {}}); got != nil {
		t.Errorf("firstPollError = %v, want nil when none failed", got)
	}
}

// pollEdges has to survive a manifest that does not load, does not parse, and is
// not a manifest at all — the watch loop reports on what it saw rather than
// falling over.
func TestPollEdges_ManifestFailures(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/notaplaylist.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>404</html>"))
	})
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		// A master whose variant will not load, and a single-file representation
		// with no live edge to read.
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=1x1,CODECS=\"avc1.640028\"\ngone/index.m3u8\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	opts := Defaults()
	opts.Now = func() time.Time { return time.Unix(0, 0) }

	if obs := pollEdges(context.Background(), client, "http://127.0.0.1:1/live.m3u8", opts); obs.err == nil {
		t.Error("an unreachable manifest was not reported")
	}
	if obs := pollEdges(context.Background(), client, srv.URL+"/notaplaylist.m3u8", opts); obs.err == nil {
		t.Error("a body that is not a playlist was not reported")
	}
	obs := pollEdges(context.Background(), client, srv.URL+"/master.m3u8", opts)
	if obs.err != nil {
		t.Fatalf("a master playlist itself failed: %v", obs.err)
	}
	if len(obs.edges) != 1 || obs.edges[0].err == nil {
		t.Errorf("a variant that will not load produced %v, want one edge carrying its error", obs.edges)
	}
}

// A VOD playlist has no live edge, and --watch says so instead of waiting.
func TestWatchLiveEdge_VOD(t *testing.T) {
	opts := Defaults()
	opts.Watch = time.Hour // never spent: the function returns before any wait
	got := watchLiveEdge(context.Background(), nil, "https://cdn.example/v.m3u8",
		manifest.Playlist{Kind: manifest.KindHLS}, opts)
	if len(got) != 1 || !strings.Contains(got[0].Message, "no live edge") {
		t.Errorf("watching VOD produced %v", got)
	}
}
