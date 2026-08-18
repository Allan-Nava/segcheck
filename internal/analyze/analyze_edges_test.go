package analyze

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The orchestration paths around the checks: a manifest that does not load, a
// variant that points at another master, a rendition sampled out of a ladder
// larger than the cap, and the sampling defaults.
//
// The rule that runs through all of them is the exit-code contract. A run that
// could not reach the media still has to come back with findings and exit 0 —
// only --exit-on may make the process fail — so every failure here is a finding
// rather than an error returned to the caller.

// ---------- Run ----------

// An origin that cannot be read is an ERROR, not a BAD: the check could not run,
// and ERROR sorts above BAD precisely so the operator sees the coverage hole
// before the findings. Blaming the stream for an unreachable origin is the
// mistake this distinction exists to prevent.
func TestRun_ManifestThatDoesNotLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream error"))
	}))
	defer srv.Close()

	res := runOn(t, srv.URL+"/master.m3u8")
	f, ok := findFinding(res, "manifest", finding.ERROR)
	if !ok {
		t.Fatalf("no ERROR manifest finding:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "not reachable") {
		t.Errorf("message = %q, want it to say the manifest was not reachable", f.Message)
	}
	// Nothing was sampled, and the result still describes the run.
	if res.Segments != 0 {
		t.Errorf("Segments = %d, want 0", res.Segments)
	}
}

// An origin that answers 200 with something that is not a manifest — an HTML
// error page, a login redirect that landed — is a BAD about the manifest, with a
// hint naming the usual cause. This is the branch that is genuinely about the
// content served rather than about reachability.
func TestRun_ManifestThatIsNotAManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Please sign in</body></html>"))
	}))
	defer srv.Close()

	res := runOn(t, srv.URL+"/master.m3u8")
	f, ok := findFinding(res, "manifest", finding.BAD)
	if !ok {
		t.Fatalf("no BAD manifest finding for an HTML page served as a manifest:\n%s", dump(res))
	}
	if f.Hint == "" {
		t.Error("the manifest failure gave no hint")
	}
	if !strings.Contains(f.Hint, "error page") {
		t.Errorf("hint = %q, want it to name the usual cause", f.Hint)
	}
}

// A URL that is not reachable at all takes the same path.
func TestRun_UnreachableOrigin(t *testing.T) {
	res := runOn(t, "http://127.0.0.1:1/master.m3u8")
	if _, ok := findFinding(res, "manifest", finding.ERROR); !ok {
		t.Fatalf("no ERROR manifest finding for an unreachable origin:\n%s", dump(res))
	}
}

// Options.Now defaults to the real clock rather than the zero time. Left at zero,
// every live-edge calculation would place the edge in 1970 and report the whole
// window as unavailable.
func TestRun_DefaultsTheClockWhenNoneIsGiven(t *testing.T) {
	client := fetch.New(fetch.Options{Timeout: 2 * time.Second})
	opts := Defaults()
	opts.Now = nil // the caller did not fix the clock

	// The URL does not resolve; what matters is that Run fills the clock in
	// before anything uses it rather than panicking on a nil function.
	res := Run(context.Background(), client, "http://127.0.0.1:1/master.m3u8", opts)
	if len(res.Findings) == 0 {
		t.Fatal("Run returned no findings at all")
	}
}

// A master playlist with more renditions than the cap is sampled in part, and the
// run has to say so — otherwise a clean result reads as "the whole ladder is
// fine" when most of it was never fetched.
func TestRun_ReportsWhenTheLadderIsSampledInPart(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "240p", bandwidth: 300000, width: 426, height: 240, segments: cleanSegments(2, 426, 240)},
		{name: "360p", bandwidth: 600000, width: 640, height: 360, segments: cleanSegments(2, 640, 360)},
		{name: "480p", bandwidth: 1200000, width: 854, height: 480, segments: cleanSegments(2, 854, 480)},
		{name: "720p", bandwidth: 2400000, width: 1280, height: 720, segments: cleanSegments(2, 1280, 720)},
		{name: "1080p", bandwidth: 4800000, width: 1920, height: 1080, segments: cleanSegments(2, 1920, 1080)},
	})

	opts := Defaults()
	opts.Segments = 2
	opts.Concurrency = 4
	opts.MaxRenditions = 2
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	res := Run(context.Background(), client, srv.URL+"/master.m3u8", opts)

	var skip finding.Finding
	for _, f := range res.Findings {
		if f.Check == "manifest" && strings.Contains(f.Message, "sampling") {
			skip = f
		}
	}
	if skip.Message == "" {
		t.Fatalf("no manifest finding saying how much of the ladder was sampled:\n%s", dump(res))
	}
	if !strings.Contains(skip.Message, "of 5 renditions") {
		t.Errorf("message = %q, want it to say how many of how many", skip.Message)
	}
	if !strings.Contains(skip.Hint, "--renditions") {
		t.Errorf("hint = %q, want it to name the flag that raises the cap", skip.Hint)
	}
}

// A variant whose media playlist is itself a master is a packaging mistake that
// would otherwise recurse. It is reported against that rendition and the rest of
// the run continues.
func TestRun_VariantPointingAtAnotherMaster(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720\n" +
			"nested.m3u8\n"))
	})
	// The variant URI serves another master playlist.
	mux.HandleFunc("/nested.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720\n" +
			"deeper.m3u8\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/master.m3u8")
	var found bool
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "another master playlist") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a variant pointing at a master was not reported:\n%s", dump(res))
	}
}

// A variant whose media playlist 404s, and one that serves something unparseable:
// both are reported against the rendition rather than failing the run.
func TestRun_MediaPlaylistFailures(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720\n" +
			"missing.m3u8\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=1600000,RESOLUTION=1920x1080\n" +
			"garbage.m3u8\n"))
	})
	mux.HandleFunc("/missing.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/garbage.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("<html>not a playlist</html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/master.m3u8")
	var unreachable, unparseable bool
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "not reachable") {
			unreachable = true
		}
		if strings.Contains(f.Message, "unparseable") {
			unparseable = true
		}
	}
	if !unreachable {
		t.Errorf("a 404 media playlist was not reported as unreachable:\n%s", dump(res))
	}
	if !unparseable {
		t.Errorf("an unparseable media playlist was not reported:\n%s", dump(res))
	}
}

// A live playlist is described as live, so the report says which mode the run was
// in — the live-edge maths and the sampling window depend on it.
func TestRun_LivePlaylistIsDescribedAsLive(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		// No EXT-X-ENDLIST: a live media playlist.
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:100\n" +
			"#EXTINF:4.0,\nseg100.ts\n#EXTINF:4.0,\nseg101.ts\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(tsSegmentBytes())
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/live.m3u8")
	var said bool
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "live") {
			said = true
		}
	}
	if !said {
		t.Errorf("a live playlist was not described as live:\n%s", dump(res))
	}
}

// ---------- pick ----------

// pick spreads the sample across the sorted ladder and always keeps both ends, so
// a cap never silently drops the top rung — the one an operator most wants
// checked. The same index must not be taken twice, however small the ladder.
func TestPick_SpreadsAndDeduplicates(t *testing.T) {
	ladder := []manifest.Rendition{
		{Name: "240p", Bandwidth: 300000, Kind: manifest.Video},
		{Name: "360p", Bandwidth: 600000, Kind: manifest.Video},
		{Name: "720p", Bandwidth: 2400000, Kind: manifest.Video},
	}

	// A cap larger than the ladder cannot repeat a rung.
	got := pick(ladder, 10)
	if len(got) != 3 {
		t.Errorf("pick(10) over 3 renditions returned %d, want 3 with no repeats", len(got))
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.Name] {
			t.Errorf("pick repeated %s", r.Name)
		}
		seen[r.Name] = true
	}

	// Both ends are always present from two upwards.
	got = pick(ladder, 2)
	if len(got) != 2 || got[0].Name != "240p" || got[1].Name != "720p" {
		t.Errorf("pick(2) = %v, want the bottom and top rungs", names(got))
	}

	// One keeps only the top rung.
	if got = pick(ladder, 1); len(got) != 1 || got[0].Name != "720p" {
		t.Errorf("pick(1) = %v, want just the top rung", names(got))
	}

	if got = pick(nil, 3); len(got) != 0 {
		t.Errorf("pick over nothing = %v", names(got))
	}
}

// ---------- sampleAll ----------

// Concurrency is bounded but never zero: a zero would mean no worker ever starts
// and the run would hang or sample nothing.
func TestSampleAll_ZeroConcurrencyStillSamples(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2400000, width: 1280, height: 720, segments: cleanSegments(3, 1280, 720)},
	})

	opts := Defaults()
	opts.Segments = 3
	opts.Concurrency = 0 // must be treated as one, not as none
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	res := Run(context.Background(), client, srv.URL+"/master.m3u8", opts)

	if res.Segments == 0 {
		t.Fatalf("no segments sampled with Concurrency 0:\n%s", dump(res))
	}
}

// ---------- byte-range segments ----------

// HLS byte-range segments carry the Range header on both the media segment and
// the init segment. Without it the whole resource is fetched and the wrong bytes
// are analysed.
func TestSampleAll_ByteRangeSegmentsSendTheRangeHeader(t *testing.T) {
	whole := tsSegmentBytes()
	// The segment fan-out fetches concurrently, so the handler records under a
	// mutex: without one the -race detector fails this test, intermittently.
	var (
		mu     sync.Mutex
		ranges []string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n" +
			"#EXT-X-BYTERANGE:" + itoa(len(whole)) + "@0\n#EXTINF:4.0,\nall.ts\n" +
			"#EXT-X-BYTERANGE:" + itoa(len(whole)) + "@0\n#EXTINF:4.0,\nall.ts\n" +
			"#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/all.ts", func(w http.ResponseWriter, r *http.Request) {
		if rh := r.Header.Get("Range"); rh != "" {
			mu.Lock()
			ranges = append(ranges, rh)
			mu.Unlock()
			w.Header().Set("Content-Type", "video/mp2t")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(whole)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(whole)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/index.m3u8")
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) == 0 {
		t.Fatalf("no Range header was sent for byte-range segments:\n%s", dump(res))
	}
	for _, rh := range ranges {
		if !strings.HasPrefix(rh, "bytes=") {
			t.Errorf("Range header = %q", rh)
		}
	}
}

// ---------- checks: the last measurement guards ----------

// checkTimeline needs a timescale to convert the declared start into seconds. A
// track that has timestamps but no timescale cannot be compared, so it is skipped.
func TestCheckTimeline_SkipsWhenTheTimescaleIsUnknown(t *testing.T) {
	noScale := media.Track{ID: 1, Kind: media.Video, Codec: "h264", HasPTS: true, MinPTS: 90000, Samples: 100}
	sd := okSeg(1, media.ContainerMP4, noScale)
	sd.seg.DeclaredStart, sd.seg.HasDeclaredStart = 1, true

	for _, f := range checkTimeline([]*renditionData{rend("720p", withSegs(sd))}, Defaults()) {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("an unknown timescale produced %s: %s", f.Status, f.Message)
		}
	}
}

// checkContinuity needs the previous segment's duration to know where the next one
// should start. Without it there is no expectation to compare against.
func TestCheckContinuity_SkipsWhenThePreviousDurationIsUnmeasurable(t *testing.T) {
	oneSample := media.Track{
		ID: 1, Kind: media.Video, Codec: "h264",
		Timescale: 90000, HasPTS: true, MinPTS: 90000, MaxPTS: 90000, Samples: 1,
	}
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, oneSample),
		okSeg(2, media.ContainerTS, oneSample),
	))
	for _, f := range checkContinuity([]*renditionData{rd}, Defaults()) {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("an unmeasurable previous duration produced %s: %s", f.Status, f.Message)
		}
	}
}

// Alignment compares renditions against each other, so one rendition alone has
// nothing to be misaligned with.
func TestCheckAlignment_NeedsAtLeastTwoRenditions(t *testing.T) {
	rd := rend("720p", withSegs(okSeg(1, media.ContainerTS, videoTrack())))
	if got := checkAlignment([]*renditionData{rd}, Defaults()); len(got) != 0 {
		t.Errorf("checkAlignment reported on a single rendition:\n%s", dumpFindings(got))
	}
}

// The earliest start is the reference, whichever rendition it belongs to and
// wherever it appears in the list.
func TestCheckAlignment_ReferenceIsTheEarliestStart(t *testing.T) {
	early := videoTrack()
	early.MinPTS = 0
	late := videoTrack()
	late.MinPTS = 90000 // a full second later: well past any tolerance

	// The earliest start belongs to the rendition declared second.
	rends := []*renditionData{
		rend("1080p", withSegs(okSeg(1, media.ContainerTS, late))),
		rend("720p", withSegs(okSeg(1, media.ContainerTS, early))),
	}
	got := checkAlignment(rends, Defaults())
	if _, ok := findIn(got, "alignment", finding.BAD); !ok {
		if _, ok := findIn(got, "alignment", finding.WARN); !ok {
			t.Fatalf("a one-second misalignment was not reported:\n%s", dumpFindings(got))
		}
	}
}

// A variant's AUDIO attribute names an EXT-X-MEDIA group. If no group by that
// name is ever defined the variant plays mute — silent video, which no
// manifest-shaped check catches unless the reference is actually resolved.
func TestCheckLadder_DanglingAudioGroupReference(t *testing.T) {
	video := func(audioGroup string) manifest.Rendition {
		return manifest.Rendition{
			Name: "720p", Kind: manifest.Video, Width: 1280, Height: 720,
			Bandwidth: 2400000, Codecs: "avc1.640028", AudioGroup: audioGroup,
		}
	}
	audio := manifest.Rendition{
		Name: "audio-en", Kind: manifest.Audio, GroupID: "aud",
		Bandwidth: 128000, Codecs: "mp4a.40.2",
	}

	// The group the variant points at is defined: nothing to report.
	resolved := manifest.Playlist{
		URL: "https://cdn.example.com/hls/master.m3u8", Master: true,
		Renditions: []manifest.Rendition{video("aud"), audio},
	}
	for _, f := range checkLadder(resolved) {
		if strings.Contains(f.Message, "AUDIO group") {
			t.Errorf("a resolved AUDIO group was reported: %s", f.Message)
		}
	}

	// The variant points at a group nobody defines.
	dangling := manifest.Playlist{
		URL: "https://cdn.example.com/hls/master.m3u8", Master: true,
		Renditions: []manifest.Rendition{video("missing-group"), audio},
	}
	got := checkLadder(dangling)
	f, ok := findIn(got, "ladder", finding.BAD)
	if !ok {
		t.Fatalf("a dangling AUDIO group reference was not reported:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "missing-group") {
		t.Errorf("message = %q, want it to name the group", f.Message)
	}
	if !strings.Contains(f.Hint, "without audio") {
		t.Errorf("hint = %q, want it to say what the viewer gets", f.Hint)
	}
}

// ---------- helpers ----------

func names(rs []manifest.Rendition) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

// tsSegmentBytes is one well-formed two-second MPEG-TS segment.
func tsSegmentBytes() []byte {
	return mediatest.TSWithSPS(0, frameDur, segFrames, mediatest.SPSFor(1280, 720))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
