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

// EXT-X-PROGRAM-DATE-TIME is the only thing in an HLS playlist that claims a
// time in the real world: this segment starts at 14:03:22Z. Everything else in
// the manifest describes a timeline relative to itself, and everything else
// segcheck checks compares two such relative timelines.
//
// The claim matters because players seek by it, DVR windows are addressed by
// it, and ad decisions are timed against it. It is also the claim nothing has
// ever compared against the media: the tag is parsed and, until now, believed.

const pdtEpoch = "2026-08-10T12:00:00.000Z"

type pdtSeg struct {
	// startPTS is where the media really starts, and pdt what the manifest
	// claims about it. Setting them apart is how each defect is planted.
	startPTS      int64
	pdt           string // empty writes no tag at all
	discontinuity bool
}

type pdtVariant struct {
	name          string
	width, height int
	segs          []pdtSeg
}

// pdtAt is the epoch plus n seconds, in the form a packager writes.
func pdtAt(sec float64) string {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(sec * float64(time.Second))).Format("2006-01-02T15:04:05.000Z")
}

// cleanPDTSegs is count 2s segments whose wall clock and whose media agree.
func cleanPDTSegs(count int) []pdtSeg {
	out := make([]pdtSeg, count)
	for i := range out {
		out[i] = pdtSeg{startPTS: int64(i) * segTicks, pdt: pdtAt(float64(i) * segSeconds)}
	}
	return out
}

func newPDTOrigin(t *testing.T, variants []pdtVariant) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n")
		for i, v := range variants {
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"avc1.4d401f\"\n%s/index.m3u8\n",
				syntheticBandwidth+i*1000, v.width, v.height, v.name)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	for _, v := range variants {
		v := v
		mux.HandleFunc("/"+v.name+"/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
			for i, s := range v.segs {
				if s.discontinuity {
					b.WriteString("#EXT-X-DISCONTINUITY\n")
				}
				if s.pdt != "" {
					fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", s.pdt)
				}
				fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d.ts\n", segSeconds, i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(b.String()))
		})
		for i, s := range v.segs {
			s := s
			mux.HandleFunc(fmt.Sprintf("/%s/seg%d.ts", v.name, i), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "video/mp2t")
				_, _ = w.Write(mediatest.TSWithSPS(s.startPTS, frameDur, segFrames, mediatest.SPSFor(v.width, v.height)))
			})
		}
	}
	return srv.URL + "/master.m3u8"
}

func runPDT(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// A ladder whose wall clock tracks its media is healthy, and must produce
// nothing above OK — including from the two renditions being compared with each
// other.
func TestRun_PDTThatTracksTheMediaIsClean(t *testing.T) {
	url := newPDTOrigin(t, []pdtVariant{
		{name: "720p", width: 1280, height: 720, segs: cleanPDTSegs(4)},
		{name: "1080p", width: 1920, height: 1080, segs: cleanPDTSegs(4)},
	})

	res := runPDT(t, url)

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("a clean PDT ladder produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "pdt") {
		t.Errorf("no pdt finding at all: the check did not run:\n%s", dump(res))
	}
}

// The wall clock jumps three seconds further than the media does. A player
// seeking to a time inside that jump lands somewhere the manifest never
// promised, and nothing in the manifest is self-contradictory: only the media
// says otherwise.
func TestRun_FindsPDTDriftingFromTheMedia(t *testing.T) {
	segs := cleanPDTSegs(4)
	segs[2].pdt = pdtAt(2*segSeconds + 3) // three seconds of wall clock the media does not have
	segs[3].pdt = pdtAt(3*segSeconds + 3)
	url := newPDTOrigin(t, []pdtVariant{{name: "720p", width: 1280, height: 720, segs: segs}})

	res := runPDT(t, url)

	f, ok := findFinding(res, "pdt", finding.BAD)
	if !ok {
		t.Fatalf("a wall clock that outruns the media was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "5.000s") {
		t.Errorf("the pdt finding does not measure the jump: %q", f.Message)
	}
}

// A wall clock that goes backwards makes two different moments in the stream
// answer to the same time. Seeking, DVR addressing and ad timing all resolve to
// whichever the player happens to find first.
func TestRun_FindsPDTGoingBackwards(t *testing.T) {
	segs := cleanPDTSegs(4)
	segs[2].pdt = pdtAt(-10)
	url := newPDTOrigin(t, []pdtVariant{{name: "720p", width: 1280, height: 720, segs: segs}})

	res := runPDT(t, url)

	f, ok := findFinding(res, "pdt", finding.BAD)
	if !ok {
		t.Fatalf("a wall clock that goes backwards was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "backwards") {
		t.Errorf("the pdt finding does not name what went wrong: %q", f.Message)
	}
}

// The defect this check exists for. Both rungs are internally consistent, both
// carry the same media at the same segment index, and they disagree by two
// seconds about what time it is — so a seek lands in two different places
// depending on which rung the player is on when the user drags the scrubber.
func TestRun_FindsRenditionsThatDisagreeAboutTheWallClock(t *testing.T) {
	skewed := cleanPDTSegs(4)
	for i := range skewed {
		skewed[i].pdt = pdtAt(float64(i)*segSeconds + 2)
	}
	url := newPDTOrigin(t, []pdtVariant{
		{name: "720p", width: 1280, height: 720, segs: cleanPDTSegs(4)},
		{name: "1080p", width: 1920, height: 1080, segs: skewed},
	})

	res := runPDT(t, url)

	f, ok := findFinding(res, "pdt", finding.BAD)
	if !ok {
		t.Fatalf("two rungs disagreeing about the wall clock was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "720p") || !strings.Contains(f.Message, "1080p") {
		t.Errorf("the pdt finding does not name both rungs: %q", f.Message)
	}
}

// A declared discontinuity is the manifest saying the timeline restarts here,
// and the specification requires a fresh EXT-X-PROGRAM-DATE-TIME after one. The
// jump is the packager doing its job, not a defect.
func TestRun_PDTJumpAtADeclaredDiscontinuityIsNotADefect(t *testing.T) {
	segs := cleanPDTSegs(4)
	segs[2].discontinuity = true
	segs[2].startPTS = 0 // the media timeline restarts too
	segs[3].startPTS = segTicks
	segs[2].pdt = pdtAt(3600) // an hour later: a different programme
	segs[3].pdt = pdtAt(3600 + segSeconds)
	url := newPDTOrigin(t, []pdtVariant{{name: "720p", width: 1280, height: 720, segs: segs}})

	res := runPDT(t, url)

	for _, f := range res.Findings {
		if f.Check == "pdt" && f.Status != finding.OK {
			t.Errorf("a declared discontinuity was reported as a PDT defect: %s %s", f.Status, f.Message)
		}
	}
}

// A playlist with no EXT-X-PROGRAM-DATE-TIME makes no wall-clock claim. That is
// not a defect and must not become a row in the report.
func TestRun_NoPDTMeansNoPDTFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "pdt") {
		t.Errorf("a playlist with no PDT produced a pdt finding:\n%s", dump(res))
	}
}
