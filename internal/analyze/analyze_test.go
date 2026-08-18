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
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The end-to-end tests plant one defect at a time in a synthetic origin and
// assert segcheck finds exactly that. A clean stream must come back with no
// finding above OK — a checker that cries wolf on healthy media is worse than
// no checker at all.

const (
	frameDur   = int64(3600)   // 25fps on the 90kHz clock
	segFrames  = 50            // 50 frames = exactly 2s
	segTicks   = int64(180000) // 2s in 90kHz ticks
	segSeconds = 2.0
	// syntheticBandwidth is a BANDWIDTH the synthetic segments can honestly
	// stand behind. They carry one small PES per frame, which works out at
	// roughly 39 kbps over two seconds; a test that declared a realistic 2 Mbps
	// would be over-declaring by 50x and the bitrate check would be right to
	// say so. Both directions get their own test below.
	syntheticBandwidth = 50_000
)

type segSpec struct {
	startPTS      int64
	declaredDur   float64
	discontinuity bool
	// codedWidth/codedHeight embed an SPS so the real resolution is readable.
	codedWidth, codedHeight int
	// hevc codes the segment as HEVC rather than H.264, which is a different
	// stream type in the PMT and a different parameter set to read.
	hevc bool
	// audioRate/audioChannels mux an AAC track alongside the video, which is how
	// most transport-stream ladders are actually delivered.
	audioRate, audioChannels int
	// captions608 lists the CEA-608 fields, and captions708 the CEA-708 service
	// numbers, planted in the segment's caption SEI. Empty means no caption SEI
	// at all, which is what a stream that declares captions and lost them looks
	// like.
	captions608 []int
	captions708 []int
	// splices plants SCTE-35 sections on a splice information PID the PMT
	// declares, which is how an encoder tells a packager where the breaks are.
	splices []mediatest.SpliceSpec
	// status, when non-zero, is served instead of the media.
	status int
	// body, when non-nil, is served instead of the media.
	body []byte
}

type variantSpec struct {
	name          string
	bandwidth     int
	width, height int
	codecs        string
	// captions is the raw CLOSED-CAPTIONS attribute for this variant, and
	// captionIDs the INSTREAM-IDs of the EXT-X-MEDIA group it names.
	captions   string
	captionIDs []string
	// cueOutBefore, when non-zero, writes an EXT-X-CUE-OUT before that 1-based
	// segment and an EXT-X-CUE-IN two segments later.
	cueOutBefore int
	// splicePID declares a splice information PID in every segment of the variant,
	// which is what a real encoder does whether or not a given segment carries a
	// cue. It is set automatically when any segment plants a splice.
	splicePID bool
	segments  []segSpec
}

// cleanSegments builds count consecutive 2s segments starting at t=0.
func cleanSegments(count int, codedW, codedH int) []segSpec {
	out := make([]segSpec, count)
	for i := range out {
		out[i] = segSpec{
			startPTS:    int64(i) * segTicks,
			declaredDur: segSeconds,
			codedWidth:  codedW,
			codedHeight: codedH,
		}
	}
	return out
}

// newHLSOrigin serves a master playlist, one media playlist per variant, and the
// TS segments themselves.
func newHLSOrigin(t *testing.T, variants []variantSpec) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(hlsOriginHandler(variants))
	t.Cleanup(srv.Close)
	return srv
}

// hlsOriginHandler is the origin itself, separate from the loopback server the
// unit tests wrap it in: the container smoke test (docker_test.go) has to serve
// the same stream on an address a container can reach, which httptest's
// 127.0.0.1 listener is not.
func hlsOriginHandler(variants []variantSpec) *http.ServeMux {
	mux := http.NewServeMux()

	// A splice information PID is declared in the PMT of every segment, not only
	// the ones carrying a section. Anything else is a fixture unlike any real
	// stream, and the difference shows up as a spurious track-layout change.
	for i := range variants {
		for _, s := range variants[i].segments {
			if len(s.splices) > 0 {
				variants[i].splicePID = true
			}
		}
	}

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n")
		for _, v := range variants {
			for _, id := range v.captionIDs {
				fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID=\"cc\","+
					"NAME=%q,LANGUAGE=\"en\",INSTREAM-ID=%q\n", id, id)
			}
		}
		for _, v := range variants {
			codecs := v.codecs
			if codecs == "" {
				codecs = "avc1.640028"
			}
			cc := ""
			if v.captions != "" {
				cc = "," + v.captions
			}
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=%q%s\n%s/index.m3u8\n",
				v.bandwidth, v.width, v.height, codecs, cc, v.name)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	for _, v := range variants {
		v := v
		mux.HandleFunc("/"+v.name+"/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
			for i, s := range v.segments {
				if s.discontinuity {
					b.WriteString("#EXT-X-DISCONTINUITY\n")
				}
				if v.cueOutBefore > 0 && i == v.cueOutBefore-1 {
					b.WriteString("#EXT-X-CUE-OUT:8.000\n")
				}
				if v.cueOutBefore > 0 && i == v.cueOutBefore+1 {
					b.WriteString("#EXT-X-CUE-IN\n")
				}
				fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d.ts\n", s.declaredDur, i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(b.String()))
		})

		for i, s := range v.segments {
			s := s
			mux.HandleFunc(fmt.Sprintf("/%s/seg%d.ts", v.name, i), func(w http.ResponseWriter, _ *http.Request) {
				if s.status != 0 {
					w.WriteHeader(s.status)
					_, _ = w.Write([]byte("not found"))
					return
				}
				if s.body != nil {
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write(s.body)
					return
				}
				w.Header().Set("Content-Type", "video/mp2t")
				if s.codedWidth > 0 {
					if v.splicePID {
						_, _ = w.Write(mediatest.TSWithSplice(s.startPTS, frameDur, segFrames,
							mediatest.SPSFor(s.codedWidth, s.codedHeight), s.splices...))
						return
					}
					if len(s.captions608) > 0 || len(s.captions708) > 0 {
						var pkts []mediatest.CCPacket
						for _, f := range s.captions608 {
							pkts = append(pkts, mediatest.CC608(f)...)
						}
						for _, svc := range s.captions708 {
							pkts = append(pkts, mediatest.CC708(svc)...)
						}
						_, _ = w.Write(mediatest.TSWithSEI(s.startPTS, frameDur, segFrames,
							mediatest.SPSFor(s.codedWidth, s.codedHeight),
							mediatest.H264SEICaptions(pkts)))
						return
					}
					if s.audioRate > 0 {
						_, _ = w.Write(mediatest.TSMuxed(s.startPTS, frameDur, segFrames,
							mediatest.SPSFor(s.codedWidth, s.codedHeight), s.audioRate, s.audioChannels))
						return
					}
					if s.hevc {
						_, _ = w.Write(mediatest.TSWithHEVCSPS(s.startPTS, frameDur, segFrames, mediatest.HEVCSPSFor(s.codedWidth, s.codedHeight)))
						return
					}
					_, _ = w.Write(mediatest.TSWithSPS(s.startPTS, frameDur, segFrames, mediatest.SPSFor(s.codedWidth, s.codedHeight)))
					return
				}
				_, _ = w.Write(mediatest.TS(s.startPTS, frameDur, segFrames))
			})
		}
	}
	return mux
}

func runOn(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 8
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	return Run(context.Background(), client, url, opts)
}

func TestRun_CleanStreamHasNoProblems(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
		{name: "1080p", bandwidth: syntheticBandwidth + 10_000, width: 1920, height: 1080, segments: cleanSegments(4, 1920, 1080)},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("clean stream produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if res.Segments != 8 {
		t.Errorf("sampled %d segments, want 8 (4 per variant)", res.Segments)
	}
	// The checks that make this tool worth using must actually have run.
	for _, check := range []string{"continuity", "duration", "resolution", "alignment", "bitrate"} {
		if !hasCheck(res, check) {
			t.Errorf("no %q finding at all: the check did not run", check)
		}
	}
}

func TestRun_FindsUndeclaredGap(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	// Half a second of missing media before the third segment, with nothing in
	// the manifest to declare it.
	segs[2].startPTS += 45000
	segs[3].startPTS += 45000

	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2000000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "continuity", finding.BAD)
	if !ok {
		t.Fatalf("the planted 500ms gap was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "gap") {
		t.Errorf("finding does not name the defect a gap: %q", f.Message)
	}
	if !strings.Contains(f.Target, "seg 2") {
		t.Errorf("finding blames %q, want the third segment (seq 2)", f.Target)
	}
	if f.Value == nil || *f.Value < 490 || *f.Value > 510 {
		t.Errorf("reported drift = %v ms, want ~500", f.Value)
	}
}

func TestRun_FindsOverlap(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[2].startPTS -= 18000 // 200ms of media covered twice
	segs[3].startPTS -= 18000

	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2000000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "continuity", finding.BAD)
	if !ok {
		t.Fatalf("the planted 200ms overlap was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "overlap") {
		t.Errorf("finding does not name the defect an overlap: %q", f.Message)
	}
}

func TestRun_DeclaredDiscontinuityIsNotADefect(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[2].startPTS += 900000 // a ten-second jump...
	segs[3].startPTS += 900000
	segs[2].discontinuity = true // ...that the manifest declares

	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2000000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	if f, ok := findFinding(res, "continuity", finding.BAD); ok {
		t.Errorf("a declared discontinuity was reported as a defect: %s", f.Message)
	}
}

func TestRun_FindsResolutionMismatch(t *testing.T) {
	// The manifest sells a 1080p rung; the bitstream codes 720p.
	segs := cleanSegments(4, 1280, 720)
	srv := newHLSOrigin(t, []variantSpec{
		{name: "1080p", bandwidth: 5000000, width: 1920, height: 1080, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "resolution", finding.BAD)
	if !ok {
		t.Fatalf("the mislabelled rung was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "1920x1080") || !strings.Contains(f.Message, "1280x720") {
		t.Errorf("finding does not state both resolutions: %q", f.Message)
	}
}

func TestRun_FindsDurationDrift(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].declaredDur = 3.0 // declared 3s, media is 2s
	}
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2000000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	if _, ok := findFinding(res, "duration", finding.WARN); !ok {
		t.Fatalf("a 33%% duration overstatement was not reported.\n%s", dump(res))
	}
	// Declaring 3s against a TARGETDURATION of 2 is also a spec violation.
	if _, ok := findFinding(res, "duration", finding.BAD); !ok {
		t.Error("a segment longer than EXT-X-TARGETDURATION was not reported")
	}
}

func TestRun_FindsMisalignedRenditions(t *testing.T) {
	aligned := cleanSegments(4, 1280, 720)
	shifted := cleanSegments(4, 1920, 1080)
	for i := range shifted {
		shifted[i].startPTS += 45000 // half a second off the other rendition
	}
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2000000, width: 1280, height: 720, segments: aligned},
		{name: "1080p", bandwidth: 5000000, width: 1920, height: 1080, segments: shifted},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "alignment", finding.BAD)
	if !ok {
		t.Fatalf("misaligned renditions were not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "720p") || !strings.Contains(f.Message, "1080p") {
		t.Errorf("finding does not name both renditions: %q", f.Message)
	}
}

func TestRun_ReportsUnfetchableSegment(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[1].status = http.StatusNotFound

	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2000000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "fetch", finding.ERROR)
	if !ok {
		t.Fatalf("a 404 segment was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "404") {
		t.Errorf("finding does not carry the status code: %q", f.Message)
	}
	// A missing segment must not also produce a phantom continuity break: the
	// pair across the hole is not comparable.
	if cf, ok := findFinding(res, "continuity", finding.BAD); ok {
		t.Errorf("a fetch failure produced a phantom continuity finding: %s", cf.Message)
	}
}

func TestRun_ReportsErrorPageServedAsMedia(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	segs[1].body = []byte("<!DOCTYPE html><html><body>Origin error</body></html>")

	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 2000000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	if _, ok := findFinding(res, "container", finding.BAD); !ok {
		t.Fatalf("an HTML page served with a 200 as a segment was not reported.\n%s", dump(res))
	}
}

func TestRun_ReportsPacketLoss(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:0.360,\nseg0.ts\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/seg0.ts", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(mediatest.TSDropPacket(0, frameDur, 10))
	})

	res := runOn(t, srv.URL+"/index.m3u8")
	f, ok := findFinding(res, "continuity", finding.BAD)
	if !ok {
		t.Fatalf("MPEG-TS packet loss was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "continuity-counter") {
		t.Errorf("finding does not mention the continuity counter: %q", f.Message)
	}
}

func TestRun_UnreachableManifestIsAnError(t *testing.T) {
	res := runOn(t, "http://127.0.0.1:1/master.m3u8")
	if _, ok := findFinding(res, "manifest", finding.ERROR); !ok {
		t.Fatalf("an unreachable manifest was not reported.\n%s", dump(res))
	}
	if res.Segments != 0 {
		t.Errorf("segments = %d after a failed manifest, want 0", res.Segments)
	}
}

func TestRun_UnderDeclaredBandwidth(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		// BANDWIDTH must be an upper bound; 1 kbps cannot hold these segments.
		{name: "720p", bandwidth: 1000, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "bitrate", finding.WARN)
	if !ok {
		t.Fatalf("an under-declared BANDWIDTH was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "BANDWIDTH") {
		t.Errorf("finding does not mention BANDWIDTH: %q", f.Message)
	}
}

func TestRun_OverDeclaredBandwidth(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		// An inflated BANDWIDTH holds players on a lower rung than they need to
		// be on, which is a real quality loss rather than a cosmetic error.
		{name: "720p", bandwidth: 20_000_000, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "bitrate", finding.WARN)
	if !ok {
		t.Fatalf("an over-declared BANDWIDTH was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "over-declared") {
		t.Errorf("finding does not name the direction of the error: %q", f.Message)
	}
}

func TestRun_FindsUndefinedAudioGroup(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.640028",AUDIO="missing-group"
720p/index.m3u8
`))
	})
	mux.HandleFunc("/720p/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:2.000,\nseg0.ts\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/720p/seg0.ts", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(mediatest.TSWithSPS(0, frameDur, segFrames, mediatest.SPSFor(1280, 720)))
	})

	res := runOn(t, srv.URL+"/master.m3u8")
	f, ok := findFinding(res, "ladder", finding.BAD)
	if !ok {
		t.Fatalf("a dangling AUDIO group was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "missing-group") {
		t.Errorf("finding does not name the group: %q", f.Message)
	}
}

func TestSampleSegments_WindowEnd(t *testing.T) {
	all := make([]manifest.Segment, 10)
	for i := range all {
		all[i] = manifest.Segment{Sequence: i}
	}
	opts := Defaults()
	opts.Segments = 3

	// On live the newest segments matter — that is what a viewer joining now
	// plays. On VOD the oldest are the deterministic choice.
	live := sampleSegments(all, true, opts)
	if len(live) != 3 || live[0].Sequence != 7 || live[2].Sequence != 9 {
		t.Errorf("live window = %v, want sequences 7,8,9", seqs(live))
	}
	vod := sampleSegments(all, false, opts)
	if len(vod) != 3 || vod[0].Sequence != 0 || vod[2].Sequence != 2 {
		t.Errorf("VOD window = %v, want sequences 0,1,2", seqs(vod))
	}

	// An explicit --from overrides the live/VOD default.
	opts.From = FromStart
	if got := sampleSegments(all, true, opts); got[0].Sequence != 0 {
		t.Errorf("--from start on live gave %v, want sequences 0,1,2", seqs(got))
	}

	// Asking for more segments than exist takes all of them, not a panic.
	opts.From = FromAuto
	opts.Segments = 99
	if got := sampleSegments(all, true, opts); len(got) != 10 {
		t.Errorf("oversized window returned %d segments, want 10", len(got))
	}
}

func TestPick_KeepsTheExtremes(t *testing.T) {
	rends := []manifest.Rendition{
		{Name: "240p", Bandwidth: 500_000},
		{Name: "360p", Bandwidth: 1_000_000},
		{Name: "480p", Bandwidth: 2_000_000},
		{Name: "720p", Bandwidth: 4_000_000},
		{Name: "1080p", Bandwidth: 8_000_000},
	}
	got := pick(rends, 3)
	if len(got) != 3 {
		t.Fatalf("picked %d renditions, want 3", len(got))
	}
	// Ladder defects concentrate at the ends, so a capped run must keep both.
	if got[0].Name != "240p" {
		t.Errorf("lowest rung dropped: got %s first", got[0].Name)
	}
	if got[len(got)-1].Name != "1080p" {
		t.Errorf("top rung dropped: got %s last", got[len(got)-1].Name)
	}

	// A cap of one keeps the top rung, where the risk is highest.
	if one := pick(rends, 1); len(one) != 1 || one[0].Name != "1080p" {
		t.Errorf("pick(1) = %v, want 1080p", one)
	}
	// No cap keeps everything, in bitrate order.
	if all := pick(rends, 0); len(all) != 5 || all[0].Name != "240p" {
		t.Errorf("pick(0) returned %d renditions, want all 5 sorted", len(all))
	}
}

func seqs(segs []manifest.Segment) []int {
	out := make([]int, len(segs))
	for i, s := range segs {
		out[i] = s.Sequence
	}
	return out
}

// ---------- assertions ----------

func findFinding(res finding.Result, check string, status finding.Status) (finding.Finding, bool) {
	for _, f := range res.Findings {
		if f.Check == check && f.Status == status {
			return f, true
		}
	}
	return finding.Finding{}, false
}

func hasCheck(res finding.Result, check string) bool {
	for _, f := range res.Findings {
		if f.Check == check {
			return true
		}
	}
	return false
}

func dump(res finding.Result) string {
	var b strings.Builder
	b.WriteString("findings:\n")
	for _, f := range res.Findings {
		fmt.Fprintf(&b, "  %-5s %-12s %-22s %s\n", f.Status, f.Check, f.Target, f.Message)
	}
	return b.String()
}

// SC-15: before the HEVC reader existed, an HEVC rung reported its codec and no
// resolution, so this check found nothing to compare and said nothing. The
// stream below declares 1080p and codes 720p — the same defect
// TestRun_FindsResolutionMismatch plants in H.264 — and it has to be caught in
// HEVC too, and attributed to the right rendition.
func TestRun_FindsResolutionMismatchInHEVC(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].hevc = true
	}
	srv := newHLSOrigin(t, []variantSpec{
		{name: "1080p", bandwidth: syntheticBandwidth, width: 1920, height: 1080,
			codecs: "hvc1.1.6.L93.B0", segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "resolution", finding.BAD)
	if !ok {
		t.Fatalf("an HEVC rung declaring 1920x1080 while coding 1280x720 was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Target, "1080p") {
		t.Errorf("finding is attributed to %q, not to the 1080p rendition", f.Target)
	}
	for _, want := range []string{"1920x1080", "1280x720"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message does not carry %s, so the operator cannot see the gap: %q", want, f.Message)
		}
	}
}

// An HEVC ladder whose rungs code what they declare must produce nothing above
// OK — a reader that cries wolf on healthy media is worse than no reader.
func TestRun_CleanHEVCStreamHasNoProblems(t *testing.T) {
	mk := func(w, h int) []segSpec {
		segs := cleanSegments(4, w, h)
		for i := range segs {
			segs[i].hevc = true
		}
		return segs
	}
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
			codecs: "hvc1.1.6.L93.B0", segments: mk(1280, 720)},
		{name: "1080p", bandwidth: syntheticBandwidth + 10_000, width: 1920, height: 1080,
			codecs: "hvc1.1.6.L93.B0", segments: mk(1920, 1080)},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("clean HEVC stream produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "resolution") {
		t.Error("no resolution finding at all: the check still skips HEVC in silence")
	}
}
