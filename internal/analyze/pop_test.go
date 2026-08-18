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
	"github.com/Allan-Nava/segcheck/internal/media"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// One edge serving different bytes from another for the same URL is the defect a
// single-shot check cannot see by construction: it asks one edge, gets a perfect
// answer, and the viewers routed elsewhere are the ones complaining. A stale POP
// holding a segment from before a re-encode plays fine — it plays the wrong
// content.

// newPOPOrigin serves one stream, with a knob for the bytes it returns and for
// segments it does not have at all. Two of these standing in for two edges is
// what the comparison is against.
func newPOPOrigin(t *testing.T, payload byte, missing map[int]bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 0; i < 3; i++ {
			fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d.ts\n", segSeconds, i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})
	for i := 0; i < 3; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/seg%d.ts", i), func(w http.ResponseWriter, _ *http.Request) {
			if missing[i] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "video/mp2t")
			body := mediatest.TSWithSPS(int64(i)*segTicks, frameDur, segFrames, mediatest.SPSFor(1280, 720))
			// One byte of the payload differs between edges, which is what a stale
			// copy of a re-encoded segment looks like from outside.
			body[len(body)-1] = payload
			_, _ = w.Write(body)
		})
	}
	return srv
}

func runPOP(t *testing.T, url string, pops ...string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 3
	opts.Concurrency = 4
	opts.From = FromStart
	opts.POPs = pops
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

func addrOf(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

// The incident: two edges, one of them stale.
func TestRun_FindsPOPsServingDifferentBytes(t *testing.T) {
	a := newPOPOrigin(t, 0x01, nil)
	b := newPOPOrigin(t, 0x02, nil)

	res := runPOP(t, a.URL+"/index.m3u8", addrOf(b))

	f, ok := findFinding(res, "pop", finding.BAD)
	if !ok {
		t.Fatalf("two edges serving different bytes was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, addrOf(b)) {
		t.Errorf("the pop finding does not name the edge that differs: %q", f.Message)
	}
}

// Edges that agree are healthy, and the check says how many segments it compared
// so the answer can be quoted.
func TestRun_POPsThatAgreeAreClean(t *testing.T) {
	a := newPOPOrigin(t, 0x01, nil)
	b := newPOPOrigin(t, 0x01, nil)

	res := runPOP(t, a.URL+"/index.m3u8", addrOf(b))

	for _, f := range res.Findings {
		if f.Check == "pop" && f.Status != finding.OK {
			t.Errorf("edges in agreement produced %s: %s", f.Status, f.Message)
		}
	}
	f, ok := findFinding(res, "pop", finding.OK)
	if !ok {
		t.Fatalf("no pop finding at all:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "3") {
		t.Errorf("the pop finding does not count what it compared: %q", f.Message)
	}
}

// An edge missing a segment another serves is a hole for whichever viewers are
// routed to it, and nothing at the other edge shows it.
func TestRun_FindsAPOPMissingASegment(t *testing.T) {
	a := newPOPOrigin(t, 0x01, nil)
	b := newPOPOrigin(t, 0x01, map[int]bool{1: true})

	res := runPOP(t, a.URL+"/index.m3u8", addrOf(b))

	f, ok := findFinding(res, "pop", finding.BAD)
	if !ok {
		t.Fatalf("an edge missing a segment was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "not fetched") && !strings.Contains(f.Message, "404") {
		t.Errorf("the pop finding does not say what happened: %q", f.Message)
	}
}

// An edge that cannot be reached at all is a hole in the coverage, not a verdict
// about the stream: segcheck could not compare anything.
func TestRun_AnUnreachablePOPIsReportedAsSuch(t *testing.T) {
	a := newPOPOrigin(t, 0x01, nil)

	res := runPOP(t, a.URL+"/index.m3u8", "127.0.0.1:1")

	f, ok := findFinding(res, "pop", finding.ERROR)
	if !ok {
		t.Fatalf("an unreachable edge was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "127.0.0.1:1") {
		t.Errorf("the pop finding does not name the edge: %q", f.Message)
	}
}

// Without --pop nothing extra is fetched and nothing is reported: the comparison
// multiplies the download by the number of edges, so it is opt-in.
func TestRun_NoPOPsMeansNoPOPFinding(t *testing.T) {
	a := newPOPOrigin(t, 0x01, nil)

	res := runPOP(t, a.URL+"/index.m3u8")

	if hasCheck(res, "pop") {
		t.Errorf("a run with no --pop produced a pop finding:\n%s", dump(res))
	}
}

// The comparison's own guards, called directly: an edge asked about a run that
// fetched nothing, a run with a segment that failed at the reference edge, and a
// byte-range segment, whose range has to travel with the request or the edge
// returns a different slice.
func TestComparePOPs_Guards(t *testing.T) {
	client := fetch.New(fetch.Options{Timeout: time.Second})
	opts := Defaults()
	opts.POPs = []string{"127.0.0.1:1"}

	// A run in which nothing was fetched: there is nothing to ask another edge.
	if got := comparePOPs(context.Background(), client, nil, opts); got != nil {
		t.Errorf("comparing against no sampled segments produced %v", got)
	}
	failed := rend("720p")
	failed.segs = []segmentData{{seg: manifest.Segment{URI: "https://x/seg0.ts"}, fetchErr: errFake("404")}}
	if got := comparePOPs(context.Background(), client, []*renditionData{failed}, opts); got != nil {
		t.Errorf("comparing against a rendition whose segments all failed produced %v", got)
	}

	// No POPs at all is the default and costs nothing.
	if got := comparePOPs(context.Background(), client, []*renditionData{failed}, Defaults()); got != nil {
		t.Errorf("a run with no --pop compared %v", got)
	}

	// checkPOP over a run that fetched nothing, and over no comparisons.
	if got := checkPOP(nil, nil); got != nil {
		t.Errorf("checkPOP with nothing to compare produced %v", got)
	}
	if got := checkPOP([]*renditionData{failed}, []popComparison{{addr: "x"}}); got != nil {
		t.Errorf("checkPOP with no reference segments produced %v", got)
	}
	// An edge that answered about a segment the reference run never fetched is
	// simply not compared.
	ref := rend("720p", withSegs(okSeg(0, media.ContainerTS, videoTrack())))
	cmp := popComparison{addr: "edge", results: map[string]popResult{"https://other/seg.ts": {digest: "x"}}}
	if got := checkPOP([]*renditionData{ref}, []popComparison{cmp}); got != nil {
		t.Errorf("an edge answering about an unrelated URL produced %v", got)
	}
}

// A byte-range segment has to be asked for with its range, or the edge returns a
// different slice and every comparison is a false difference.
func TestComparePOPs_CarriesTheByteRange(t *testing.T) {
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, 50))
	}))
	defer srv.Close()

	rd := rend("720p")
	rd.segs = []segmentData{{
		seg: manifest.Segment{URI: srv.URL + "/all.ts", ByteRange: &manifest.ByteRange{Length: 50, Offset: 100}},
		res: fetch.Response{Body: make([]byte, 50)},
	}}
	opts := Defaults()
	opts.POPs = []string{strings.TrimPrefix(srv.URL, "http://")}
	comparePOPs(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), []*renditionData{rd}, opts)

	if len(ranges) != 1 || ranges[0] != "bytes=100-149" {
		t.Errorf("the edge was asked with ranges %v, want the segment's own", ranges)
	}
}

// Zero concurrency must still compare: a zero-capacity semaphore would mean no
// worker ever starts, the same trap the segment and parts fan-outs have.
func TestComparePOPs_ZeroConcurrencyStillCompares(t *testing.T) {
	srv := newPOPOrigin(t, 0x01, nil)
	rd := rend("720p")
	rd.segs = []segmentData{{
		seg: manifest.Segment{URI: srv.URL + "/seg0.ts"},
		res: fetch.Response{Body: []byte("reference")},
	}}
	opts := Defaults()
	opts.Concurrency = 0
	opts.POPs = []string{addrOf(srv)}

	got := comparePOPs(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}),
		[]*renditionData{rd}, opts)
	if len(got) != 1 || len(got[0].results) != 1 {
		t.Fatalf("zero concurrency compared %v", got)
	}
}
