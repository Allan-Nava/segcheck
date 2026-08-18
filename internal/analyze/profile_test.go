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

// A conformance rule with no way to turn it off turns a run that was clean
// yesterday into a wall of findings today, on a stream nobody changed. Profiles
// are opt-in for that reason alone, and `none` has to mean none.

func runProfile(t *testing.T, url, profile string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.Profile = profile
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

func TestRun_NoProfileRunsNoRules(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	for _, p := range []string{"", ProfileNone} {
		res := runProfile(t, srv.URL+"/master.m3u8", p)
		if hasCheck(res, "profile") {
			t.Errorf("profile %q produced conformance findings; opt-in has to mean opt-in:\n%s", p, dump(res))
		}
	}
}

// Selecting a profile says which rule set ran, even when everything passes:
// "no findings" and "no rules" look identical in a report otherwise, and an
// operator who asked for conformance needs to know they got it.
func TestRun_SelectingAProfileSaysWhichRuleSetRan(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runProfile(t, srv.URL+"/master.m3u8", ProfileApple)

	if !hasCheck(res, "profile") {
		t.Fatalf("--profile apple produced no profile finding at all:\n%s", dump(res))
	}
	var named bool
	for _, f := range res.Findings {
		if f.Check == "profile" && f.Rule != "" {
			named = true
		}
	}
	if !named {
		t.Errorf("no profile finding names the rule it comes from, so none can be argued with:\n%s", dump(res))
	}
}

// A rule set segcheck does not implement yet is a limit of the tool, not a
// verdict about the stream. It says so at OK level rather than reporting a
// clean pass it never actually made.
func TestRun_UnimplementedProfileSaysSoRatherThanPassing(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runProfile(t, srv.URL+"/master.m3u8", ProfileDASHIF)

	f, ok := findFinding(res, "profile", finding.OK)
	if !ok {
		t.Fatalf("--profile dash-if said nothing at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "not implemented") {
		t.Errorf("an unimplemented rule set did not say so: %q", f.Message)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOfSub(s, sub) >= 0)
}

func indexOfSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---------- SC-59: the measurable subset of Apple's HLS Authoring Spec ----------

// The fixture is fMP4 because the Apple rules need control of three things at
// once that a fixed TS builder cannot give: how many bytes a segment carries
// (bitrate), how long it lasts (duration), and whether it opens on a sync
// sample (IDR).
const (
	appleTimescale = uint32(90000)
	appleSampleDur = uint32(3600) // 25fps
	appleSamples   = 50           // 2s
)

type appleSeg struct {
	samples    int // 0 means appleSamples
	payload    int
	sync       bool
	statesSync bool
}

type appleVariant struct {
	name          string
	width, height int
	bandwidth     int
	sampleDur     uint32 // 0 means appleSampleDur
	segs          []appleSeg
}

// appleLadder is a rung whose measured average lands on Apple's recommended
// tier for its resolution, so the clean case really is clean.
func appleLadder(name string, w, h, kbps, count int) appleVariant {
	payload := kbps * 1000 * 2 / 8 // bytes for `kbps` over a 2s segment
	segs := make([]appleSeg, count)
	for i := range segs {
		segs[i] = appleSeg{payload: payload, sync: true, statesSync: true}
	}
	return appleVariant{
		name: name, width: w, height: h,
		bandwidth: int(float64(kbps) * 1000 * 1.1),
		segs:      segs,
	}
}

func newAppleOrigin(t *testing.T, variants []appleVariant) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
		for _, v := range variants {
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"avc1.640028\"\n%s/index.m3u8\n",
				v.bandwidth, v.width, v.height, v.name)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	for _, v := range variants {
		v := v
		mux.HandleFunc("/"+v.name+"/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:0\n")
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"init.mp4\"\n")
			for i, s := range v.segs {
				fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d.m4s\n", appleSegSeconds(v, s), i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(b.String()))
		})
		mux.HandleFunc("/"+v.name+"/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4Init(1, appleTimescale, "video", v.width, v.height))
		})
		var tfdt int64
		for i, s := range v.segs {
			i, s, start := i, s, tfdt
			tfdt += int64(appleSampleCount(s)) * int64(appleFrameDur(v))
			mux.HandleFunc(fmt.Sprintf("/%s/seg%d.m4s", v.name, i), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				if s.statesSync {
					_, _ = w.Write(mediatest.MP4SegmentSync(1, uint32(i), start, appleFrameDur(v),
						appleSampleCount(s), s.payload, s.sync))
					return
				}
				_, _ = w.Write(mediatest.MP4Segment(1, uint32(i), start, appleFrameDur(v),
					appleSampleCount(s), s.payload))
			})
		}
	}
	return srv.URL + "/master.m3u8"
}

func appleSampleCount(s appleSeg) int {
	if s.samples > 0 {
		return s.samples
	}
	return appleSamples
}

func appleFrameDur(v appleVariant) uint32 {
	if v.sampleDur > 0 {
		return v.sampleDur
	}
	return appleSampleDur
}

func appleSegSeconds(v appleVariant, s appleSeg) float64 {
	return float64(appleSampleCount(s)) * float64(appleFrameDur(v)) / float64(appleTimescale)
}

func appleRuleFinding(res finding.Result, rule string) (finding.Finding, bool) {
	for _, f := range res.Findings {
		if f.Rule == rule {
			return f, true
		}
	}
	return finding.Finding{}, false
}

// A ladder that satisfies the specification must produce nothing above OK under
// the profile. A conformance mode that cries wolf on conformant media is worse
// than no conformance mode.
func TestProfileApple_ConformantLadderIsClean(t *testing.T) {
	url := newAppleOrigin(t, []appleVariant{
		appleLadder("234p", 416, 234, 145, 4),
		appleLadder("360p", 640, 360, 365, 4),
	})

	res := runProfile(t, url, ProfileApple)

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("a conformant ladder produced %s on %s/%s: %s", f.Status, f.Check, f.Rule, f.Message)
		}
	}
	for _, rule := range []string{"apple:peak-vs-average", "apple:idr-per-segment", "apple:bitrate-tier"} {
		if _, ok := appleRuleFinding(res, rule); !ok {
			t.Errorf("rule %s did not run at all:\n%s", rule, dump(res))
		}
	}
}

// The peak-to-average rule: one segment carrying four times the bytes of its
// neighbours. Every EXTINF is honest, the BANDWIDTH covers it, and a player on
// a connection sized for the average stalls on that segment.
func TestProfileApple_FindsAPeakFarAboveTheAverage(t *testing.T) {
	v := appleLadder("360p", 640, 360, 365, 4)
	v.segs[2].payload *= 4
	v.bandwidth *= 4 // declared honestly, so only the profile rule has anything to say
	url := newAppleOrigin(t, []appleVariant{v})

	res := runProfile(t, url, ProfileApple)

	f, ok := appleRuleFinding(res, "apple:peak-vs-average")
	if !ok {
		t.Fatalf("the peak-to-average rule did not run:\n%s", dump(res))
	}
	if f.Status == finding.OK {
		t.Errorf("a peak at 229%% of the average passed: %q", f.Message)
	}
	if !strings.Contains(f.Message, "200%") {
		t.Errorf("the finding does not put the measurement beside the limit: %q", f.Message)
	}
}

// A segment that does not open on an IDR cannot be switched into. The manifest
// says nothing about it either way, which is exactly why the media has to.
func TestProfileApple_FindsASegmentThatDoesNotOpenOnAnIDR(t *testing.T) {
	v := appleLadder("360p", 640, 360, 365, 4)
	v.segs[2].sync = false
	url := newAppleOrigin(t, []appleVariant{v})

	res := runProfile(t, url, ProfileApple)

	f, ok := appleRuleFinding(res, "apple:idr-per-segment")
	if !ok {
		t.Fatalf("the IDR rule did not run:\n%s", dump(res))
	}
	if f.Status == finding.OK {
		t.Errorf("a segment opening on a non-sync sample passed: %q", f.Message)
	}
}

// A rung whose measured bitrate is nowhere near the tier its resolution implies.
// The number beside the limit is the whole point: "fails the bit rate rule" is
// unactionable, "measured 58 kbps against Apple's 6000 kbps for 1920x1080" is not.
func TestProfileApple_FindsARungFarFromItsBitrateTier(t *testing.T) {
	v := appleLadder("1080p", 1920, 1080, 60, 4) // 60 kbps at 1080p
	url := newAppleOrigin(t, []appleVariant{v})

	res := runProfile(t, url, ProfileApple)

	f, ok := appleRuleFinding(res, "apple:bitrate-tier")
	if !ok {
		t.Fatalf("the bitrate-tier rule did not run:\n%s", dump(res))
	}
	if f.Status == finding.OK {
		t.Errorf("60 kbps at 1920x1080 passed the tier rule: %q", f.Message)
	}
	if !strings.Contains(f.Message, "6000") && !strings.Contains(f.Message, "6.0") {
		t.Errorf("the finding does not quote Apple's recommendation: %q", f.Message)
	}
}

// Segments of visibly different lengths inside one rendition. Every EXTINF is
// truthful, so nothing that reads the manifest can object, and a player's
// buffer maths is built on segments being the length they usually are.
func TestProfileApple_FindsSegmentDurationsThatWander(t *testing.T) {
	v := appleLadder("360p", 640, 360, 365, 4)
	v.segs[2].samples = 20 // 0.8s where its neighbours are 2s
	url := newAppleOrigin(t, []appleVariant{v})

	res := runProfile(t, url, ProfileApple)

	f, ok := appleRuleFinding(res, "apple:segment-duration")
	if !ok {
		t.Fatalf("the segment-duration rule did not run:\n%s", dump(res))
	}
	if f.Status == finding.OK {
		t.Errorf("segments of 2s and 0.8s in one rendition passed: %q", f.Message)
	}
}
