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

// A PQ rung whose samples are really BT.709 is tone-mapped twice by every device
// that believes the manifest and once by every device that believes the
// bitstream, so the two halves of the audience see different pictures of the same
// stream — and neither half sees an error. VIDEO-RANGE is what a player reads to
// decide whether to ask the display for HDR, before it has decoded a frame.

type rangeRung struct {
	name      string
	declared  string // VIDEO-RANGE
	transfer  int    // the colr transfer characteristic the media states
	primaries int
	matrix    int
	noColrBox bool
}

func newRangeOrigin(t *testing.T, rungs []rangeRung) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
		for i, r := range rungs {
			vr := ""
			if r.declared != "" {
				vr = ",VIDEO-RANGE=" + r.declared
			}
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.4d401f\"%s\n%s/index.m3u8\n",
				syntheticBandwidth+i*1000, vr, r.name)
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
			for i := 0; i < 3; i++ {
				fmt.Fprintf(&b, "#EXTINF:2.000,\nseg%d.m4s\n", i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(b.String()))
		})
		mux.HandleFunc("/"+r.name+"/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			if r.noColrBox {
				_, _ = w.Write(mediatest.MP4Init(1, drmTimescale, "video", 1280, 720))
				return
			}
			_, _ = w.Write(mediatest.MP4InitColr(1, drmTimescale, 1280, 720, "avc1",
				r.primaries, r.transfer, r.matrix, false))
		})
		for i := 0; i < 3; i++ {
			i := i
			mux.HandleFunc(fmt.Sprintf("/%s/seg%d.m4s", r.name, i), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = w.Write(mediatest.MP4Segment(1, uint32(i), int64(i)*drmSegTicks, drmSampleDur, drmSamples, 6000))
			})
		}
	}
	return srv.URL + "/master.m3u8"
}

func runRange(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 3
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// The defect: the manifest promises HDR and the samples are BT.709.
func TestRun_FindsAPQRungThatIsReallySDR(t *testing.T) {
	url := newRangeOrigin(t, []rangeRung{
		{name: "uhd", declared: "PQ", primaries: 1, transfer: 1, matrix: 1},
	})

	res := runRange(t, url)

	f, ok := findFinding(res, "videorange", finding.BAD)
	if !ok {
		t.Fatalf("a PQ rung whose samples are BT.709 was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "PQ") || !strings.Contains(f.Message, "BT.709") {
		t.Errorf("the videorange finding does not name both claims: %q", f.Message)
	}
}

// And the other way round, which is worse for the viewer: the media is PQ and
// the manifest says SDR, so a display is never asked for HDR and the picture
// comes out washed out and flat on every device that trusts the manifest.
func TestRun_FindsAnSDRRungThatIsReallyPQ(t *testing.T) {
	url := newRangeOrigin(t, []rangeRung{
		{name: "uhd", declared: "SDR", primaries: 9, transfer: 16, matrix: 9},
	})

	res := runRange(t, url)

	f, ok := findFinding(res, "videorange", finding.BAD)
	if !ok {
		t.Fatalf("an SDR rung whose samples are PQ was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "PQ") {
		t.Errorf("the videorange finding does not name what the media really is: %q", f.Message)
	}
}

// A manifest and a bitstream that agree are healthy, and the check names what it
// verified so an HDR delivery can be signed off on evidence.
func TestRun_AVideoRangeThatMatchesIsClean(t *testing.T) {
	url := newRangeOrigin(t, []rangeRung{
		{name: "uhd", declared: "PQ", primaries: 9, transfer: 16, matrix: 9},
		{name: "hd", declared: "SDR", primaries: 1, transfer: 1, matrix: 1},
	})

	res := runRange(t, url)

	for _, f := range res.Findings {
		if f.Check == "videorange" && f.Status != finding.OK {
			t.Errorf("a matching VIDEO-RANGE produced %s: %s", f.Status, f.Message)
		}
	}
	f, ok := findFinding(res, "videorange", finding.OK)
	if !ok {
		t.Fatalf("no videorange finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "PQ") && !strings.Contains(f.Message, "SDR") {
		t.Errorf("the videorange finding does not name what it verified: %q", f.Message)
	}
}

// Media that states no colour cannot arbitrate the claim, and saying so is the
// honest answer: a manifest promising PQ over media segcheck could not read is
// unverified, not wrong.
func TestRun_UnreadableColourSaysSoRatherThanFailing(t *testing.T) {
	url := newRangeOrigin(t, []rangeRung{
		{name: "uhd", declared: "PQ", noColrBox: true},
	})

	res := runRange(t, url)

	f, ok := findFinding(res, "videorange", finding.OK)
	if !ok {
		t.Fatalf("no videorange finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "states no colour") {
		t.Errorf("the videorange finding does not say why it could not check: %q", f.Message)
	}
}

// A manifest that makes no dynamic-range claim gains no row: an absent
// VIDEO-RANGE is not an SDR one, and there is nothing to be wrong about.
func TestRun_NoVideoRangeClaimMeansNoFinding(t *testing.T) {
	url := newRangeOrigin(t, []rangeRung{
		{name: "hd", primaries: 1, transfer: 1, matrix: 1},
	})

	res := runRange(t, url)

	for _, f := range res.Findings {
		if f.Check == "videorange" && f.Status != finding.OK {
			t.Errorf("a manifest with no VIDEO-RANGE produced %s: %s", f.Status, f.Message)
		}
	}
}
