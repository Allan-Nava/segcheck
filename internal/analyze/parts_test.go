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

// Low-latency HLS describes the same media twice: once as segments and once, at
// a finer grain, as the parts published before each segment exists. The parts
// are not slices of the segment a player would otherwise fetch — the packager
// muxes both — so the two descriptions can disagree, and a viewer on the
// low-latency path then gets different media from a viewer on the normal one.
// That disagreement is invisible to anything that reads only the manifest, and
// invisible to anything that fetches only the segments.

const (
	partTimescale  = uint32(90000)
	partSampleDur  = uint32(3600) // 25fps
	partSamples    = 10           // 0.4s per part
	partTicks      = int64(partSamples) * int64(partSampleDur)
	partSeconds    = 0.4
	partsPerSeg    = 3
	partedSegTicks = partTicks * partsPerSeg // 1.2s
	partPayload    = 4000
)

type partSpec struct {
	// tfdtOffset shifts this part's baseMediaDecodeTime away from where the
	// segment's timeline says it should be. Non-zero plants the defect the whole
	// check exists for: parts that do not reconstruct their segment.
	tfdtOffset int64
	// independent is INDEPENDENT=YES in the playlist, and sync whether the media
	// really opens on a sync sample. Setting them apart is the second defect: a
	// part a player is invited to start at, which it cannot decode.
	independent bool
	sync        bool
	statesSync  bool // write first-sample-flags at all
	gap         bool // GAP=YES: a hole the packager declares on purpose
	status      int  // served instead of the media
}

type partedSeg struct {
	parts []partSpec
}

func cleanParts() []partSpec {
	out := make([]partSpec, partsPerSeg)
	for i := range out {
		out[i] = partSpec{independent: i == 0, sync: true, statesSync: i == 0}
	}
	return out
}

// newPartsOrigin serves a live low-latency playlist: plain segments first, then
// the segments the packager is still publishing parts for.
func newPartsOrigin(t *testing.T, plain int, parted []partedSeg) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	total := plain + len(parted)
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:9\n#EXT-X-TARGETDURATION:2\n")
		fmt.Fprintf(&b, "#EXT-X-PART-INF:PART-TARGET=%.5f\n", partSeconds)
		b.WriteString("#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,PART-HOLD-BACK=1.2\n")
		b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
		for i := 0; i < plain; i++ {
			fmt.Fprintf(&b, "#EXTINF:%.5f,\nseg%d.m4s\n", float64(partedSegTicks)/float64(partTimescale), i)
		}
		for si, ps := range parted {
			n := plain + si
			for pi, p := range ps.parts {
				attrs := fmt.Sprintf("DURATION=%.5f,URI=\"seg%d.p%d.m4s\"", partSeconds, n, pi)
				if p.independent {
					attrs += ",INDEPENDENT=YES"
				}
				if p.gap {
					attrs += ",GAP=YES"
				}
				fmt.Fprintf(&b, "#EXT-X-PART:%s\n", attrs)
			}
			fmt.Fprintf(&b, "#EXTINF:%.5f,\nseg%d.m4s\n", float64(partedSegTicks)/float64(partTimescale), n)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Init(1, partTimescale, "video", 1280, 720))
	})

	// The segment is muxed in its own right, on the timeline the playlist
	// implies. It is not a concatenation of the parts, because in a real
	// packager it is not one either — which is precisely how the two can drift.
	for i := 0; i < total; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/seg%d.m4s", i), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4Segment(1, uint32(i), int64(i)*partedSegTicks,
				partSampleDur, partSamples*partsPerSeg, partPayload*partsPerSeg))
		})
	}

	for si, ps := range parted {
		n := plain + si
		for pi, p := range ps.parts {
			n, pi, p := n, pi, p
			mux.HandleFunc(fmt.Sprintf("/seg%d.p%d.m4s", n, pi), func(w http.ResponseWriter, _ *http.Request) {
				if p.status != 0 {
					w.WriteHeader(p.status)
					_, _ = w.Write([]byte("not found"))
					return
				}
				tfdt := int64(n)*partedSegTicks + int64(pi)*partTicks + p.tfdtOffset
				w.Header().Set("Content-Type", "video/mp4")
				if p.statesSync {
					_, _ = w.Write(mediatest.MP4SegmentSync(1, uint32(n*partsPerSeg+pi), tfdt,
						partSampleDur, partSamples, partPayload, p.sync))
					return
				}
				_, _ = w.Write(mediatest.MP4Segment(1, uint32(n*partsPerSeg+pi), tfdt,
					partSampleDur, partSamples, partPayload))
			})
		}
	}
	return srv.URL + "/index.m3u8"
}

func runParts(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	return Run(context.Background(), client, url, opts)
}

// A packager whose parts and whose segments describe the same media is healthy,
// and must produce nothing above OK — including from every check that now has
// three extra fragments to look at.
func TestRun_PartsThatReconstructTheirSegmentAreClean(t *testing.T) {
	url := newPartsOrigin(t, 2, []partedSeg{{parts: cleanParts()}, {parts: cleanParts()}})

	res := runParts(t, url)

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("a clean low-latency stream produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "parts") {
		t.Errorf("no parts finding at all: the check did not run:\n%s", dump(res))
	}
}

// The defect low latency is made of: the parts are muxed separately from the
// segment, and one of them lands 200ms away from where its neighbours put it. A
// viewer on the low-latency path sees a hole that a viewer fetching whole
// segments never does, and no manifest check can see it.
func TestRun_FindsPartsThatDoNotReconstructTheSegment(t *testing.T) {
	parts := cleanParts()
	parts[1].tfdtOffset = 18000 // 200ms late on the 90kHz clock
	url := newPartsOrigin(t, 2, []partedSeg{{parts: cleanParts()}, {parts: parts}})

	res := runParts(t, url)

	f, ok := findFinding(res, "parts", finding.BAD)
	if !ok {
		t.Fatalf("a part that does not line up with its segment was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "200") {
		t.Errorf("the parts finding does not measure the 200ms hole: %q", f.Message)
	}
}

// The parts are contiguous with each other and still wrong: every one of them
// sits 200ms away from where the segment's own timeline puts it. Comparing the
// parts only among themselves would call this healthy, and a viewer switching
// between the low-latency and the normal path would jump 200ms.
func TestRun_FindsPartsThatMissTheirSegmentEntirely(t *testing.T) {
	parts := cleanParts()
	for i := range parts {
		parts[i].tfdtOffset = 18000
	}
	url := newPartsOrigin(t, 2, []partedSeg{{parts: cleanParts()}, {parts: parts}})

	res := runParts(t, url)

	f, ok := findFinding(res, "parts", finding.BAD)
	if !ok {
		t.Fatalf("parts shifted wholesale off their segment were not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "do not cover their segment") {
		t.Errorf("the parts finding does not say they miss the segment: %q", f.Message)
	}
}

// INDEPENDENT=YES invites a player to start playing at this part. It is the one
// claim a part makes about the bitstream, and a part that does not open on a
// sync sample cannot be decoded from — the join shows garbage or nothing.
func TestRun_FindsAPartDeclaredIndependentThatIsNot(t *testing.T) {
	parts := cleanParts()
	parts[2] = partSpec{independent: true, statesSync: true, sync: false}
	url := newPartsOrigin(t, 2, []partedSeg{{parts: cleanParts()}, {parts: parts}})

	res := runParts(t, url)

	f, ok := findFinding(res, "parts", finding.BAD)
	if !ok {
		t.Fatalf("a part declared INDEPENDENT that opens on a non-sync sample was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "INDEPENDENT") {
		t.Errorf("the parts finding does not name the claim it disproved: %q", f.Message)
	}
}

// A part that will not fetch is a hole in the low-latency path. A part the
// packager marked GAP=YES is the packager telling us there is nothing there,
// and reporting that is reporting the manifest back at itself.
func TestRun_FindsAnUnfetchablePartButNotADeclaredGap(t *testing.T) {
	missing := cleanParts()
	missing[1].status = 404
	url := newPartsOrigin(t, 2, []partedSeg{{parts: cleanParts()}, {parts: missing}})

	res := runParts(t, url)
	f, ok := findFinding(res, "parts", finding.BAD)
	if !ok {
		t.Fatalf("a part that 404s was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "404") && !strings.Contains(f.Message, "not fetched") {
		t.Errorf("the parts finding does not say the part was unreachable: %q", f.Message)
	}

	declared := cleanParts()
	declared[1].status = 404
	declared[1].gap = true
	url = newPartsOrigin(t, 2, []partedSeg{{parts: cleanParts()}, {parts: declared}})

	res = runParts(t, url)
	for _, f := range res.Findings {
		if f.Check == "parts" && f.Status != finding.OK {
			t.Errorf("a GAP=YES part was reported as a defect: %s %s", f.Status, f.Message)
		}
	}
}

// Parts cost requests, and a ladder of them costs a great many. --parts 0 is how
// an operator says no, and it has to mean no rather than fewer.
func TestRun_PartsFlagOffFetchesNoParts(t *testing.T) {
	url := newPartsOrigin(t, 2, []partedSeg{{parts: cleanParts()}, {parts: cleanParts()}})

	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.PartSegments = 0
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	res := Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)

	for _, f := range res.Findings {
		if f.Check == "parts" && f.Status != finding.OK {
			t.Errorf("--parts 0 still judged the parts: %s %s", f.Status, f.Message)
		}
	}
}

// A stream with no parts at all must not gain a parts finding: the check has
// nothing to say, and saying it anyway is a row in every report for a feature
// most streams do not use.
func TestRun_NoPartsMeansNoPartsFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "parts") {
		t.Errorf("a playlist with no EXT-X-PART produced a parts finding:\n%s", dump(res))
	}
}
