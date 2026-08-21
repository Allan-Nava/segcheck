package analyze

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// byteRangeOptions describes an origin's Range behaviour, which is the thing
// under test: whether it honours a Range request, whether it advertises that it
// does, and whether the stream it serves cannot play without it.
type byteRangeOptions struct {
	honour     bool // answer 206 with exactly the slice asked for
	advertise  bool // Accept-Ranges: bytes on every response
	byteRanges bool // the playlist addresses one resource with EXT-X-BYTERANGE
	// refuse answers 416 to any Range request, which is what a misconfigured
	// proxy in front of an object store does.
	refuse bool
}

const byteRangeSegCount = 4

// newByteRangeOrigin serves four 2s MPEG-TS segments, either as their own resources
// or as byte ranges of one, and answers Range requests however the options say.
func newByteRangeOrigin(t *testing.T, o byteRangeOptions) string {
	t.Helper()

	bodies := make([][]byte, byteRangeSegCount)
	for i := range bodies {
		bodies[i] = mediatest.TSWithSPS(int64(i)*segTicks, frameDur, segFrames,
			mediatest.SPSFor(1280, 720))
	}

	// The byte-range form is one resource the playlist cuts up, which is how
	// Apple's own MPEG-TS reference is packaged.
	var joined []byte
	offsets := make([]int64, byteRangeSegCount)
	for i, b := range bodies {
		offsets[i] = int64(len(joined))
		joined = append(joined, b...)
	}

	serve := func(w http.ResponseWriter, r *http.Request, body []byte) {
		if o.advertise {
			w.Header().Set("Accept-Ranges", "bytes")
		}
		w.Header().Set("Content-Type", "video/mp2t")
		hdr := r.Header.Get("Range")
		if hdr == "" {
			_, _ = w.Write(body)
			return
		}
		if o.refuse {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if !o.honour {
			// The whole resource, with a 200. The bytes are then not the ones
			// the caller asked for, and nothing in the status says so.
			_, _ = w.Write(body)
			return
		}
		start, end, ok := parseTestByteRange(hdr, int64(len(body)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start : end+1])
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.640028\"\nindex.m3u8\n",
			syntheticBandwidth)
	})

	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 0; i < byteRangeSegCount; i++ {
			if o.byteRanges {
				fmt.Fprintf(&b, "#EXT-X-BYTERANGE:%d@%d\n#EXTINF:%.3f,\nmain.ts\n",
					len(bodies[i]), offsets[i], segSeconds)
				continue
			}
			fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d.ts\n", segSeconds, i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	mux.HandleFunc("/main.ts", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, joined)
	})
	for i := range bodies {
		body := bodies[i]
		mux.HandleFunc(fmt.Sprintf("/seg%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			serve(w, r, body)
		})
	}
	return srv.URL + "/master.m3u8"
}

// parseTestByteRange reads the one form segcheck ever sends: bytes=first-last.
func parseTestByteRange(hdr string, size int64) (int64, int64, bool) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(hdr), "bytes=")
	if !ok {
		return 0, 0, false
	}
	first, last, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end, err := strconv.ParseInt(last, 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

// byteRangeFindings is every finding the check produced, which is what the
// once-per-host claim is asserted against.
func byteRangeFindings(res finding.Result) []finding.Finding {
	var out []finding.Finding
	for _, f := range res.Findings {
		if f.Check == "byterange" {
			out = append(out, f)
		}
	}
	return out
}

// An origin that honours Range says so once, for the host, however many
// segments were sampled.
func TestRun_AnOriginThatHonoursRangeIsReportedOncePerHost(t *testing.T) {
	res := runOn(t, newByteRangeOrigin(t, byteRangeOptions{honour: true, advertise: true}))

	fs := byteRangeFindings(res)
	if len(fs) != 1 {
		t.Fatalf("want exactly one byterange finding for one host, got %d:\n%s", len(fs), dump(res))
	}
	if fs[0].Status != finding.OK {
		t.Errorf("an origin that honours Range produced %s: %s", fs[0].Status, fs[0].Message)
	}
	if !strings.Contains(fs[0].Target, "127.0.0.1") {
		t.Errorf("the finding does not name the host it is about: %q", fs[0].Target)
	}
}

// A stream packaged as byte ranges of one resource cannot play at all against an
// origin that ignores Range: every "segment" a player asks for arrives as the
// whole file. That is the one shape of this defect that is fatal.
func TestRun_FindsAnOriginIgnoringRangeOnAByteRangeStream(t *testing.T) {
	res := runOn(t, newByteRangeOrigin(t, byteRangeOptions{byteRanges: true}))

	fs := byteRangeFindings(res)
	if len(fs) != 1 {
		t.Fatalf("want exactly one byterange finding for one host, got %d:\n%s", len(fs), dump(res))
	}
	if fs[0].Status != finding.BAD {
		t.Fatalf("a byte-range stream on an origin that ignores Range produced %s, want BAD: %s",
			fs[0].Status, fs[0].Message)
	}
	if !strings.Contains(fs[0].Message, "200") {
		t.Errorf("the finding does not say what the origin answered: %q", fs[0].Message)
	}
}

// The same origin behaviour on a stream that addresses whole resources is not a
// defect in the stream: nothing a player needs is missing, Range only buys it a
// cheaper seek. Reporting it as bad would send someone hunting a fault that
// costs this stream nothing.
func TestRun_AnOriginIgnoringRangeOnAStreamThatDoesNotNeedItIsNotADefect(t *testing.T) {
	res := runOn(t, newByteRangeOrigin(t, byteRangeOptions{}))

	fs := byteRangeFindings(res)
	if len(fs) != 1 {
		t.Fatalf("want exactly one byterange finding for one host, got %d:\n%s", len(fs), dump(res))
	}
	if finding.AtLeast(fs[0].Status, finding.WARN) {
		t.Errorf("an origin that ignores Range on a stream that never asks for one produced %s: %s",
			fs[0].Status, fs[0].Message)
	}
}

// Accept-Ranges: bytes is a claim, and answering 200 to a Range request is the
// fact. An origin doing both is lying to every client that reads the header —
// which is the whole of this tool's job applied to delivery rather than to media.
func TestRun_FindsAnOriginAdvertisingRangeSupportItDoesNotHave(t *testing.T) {
	res := runOn(t, newByteRangeOrigin(t, byteRangeOptions{advertise: true}))

	fs := byteRangeFindings(res)
	if len(fs) != 1 {
		t.Fatalf("want exactly one byterange finding for one host, got %d:\n%s", len(fs), dump(res))
	}
	if fs[0].Status != finding.WARN {
		t.Fatalf("an origin advertising Accept-Ranges it does not honour produced %s, want WARN: %s",
			fs[0].Status, fs[0].Message)
	}
	if !strings.Contains(fs[0].Message, "Accept-Ranges") {
		t.Errorf("the finding does not name the header that was wrong: %q", fs[0].Message)
	}
}

// A 416 to bytes=0-N of a resource that is longer than N is a refusal, not a
// negotiation. It breaks a byte-range stream exactly as a 200 does, and on a
// stream that needs nothing it is still a fault worth naming, because the
// origin answered an error to a request every player is entitled to make.
func TestRun_FindsAnOriginThatRefusesRangeOutright(t *testing.T) {
	res := runOn(t, newByteRangeOrigin(t, byteRangeOptions{advertise: true, refuse: true}))

	fs := byteRangeFindings(res)
	if len(fs) != 1 {
		t.Fatalf("want exactly one byterange finding for one host, got %d:\n%s", len(fs), dump(res))
	}
	if !finding.AtLeast(fs[0].Status, finding.WARN) {
		t.Errorf("an origin answering 416 to a legitimate range produced %s: %s",
			fs[0].Status, fs[0].Message)
	}
	if !strings.Contains(fs[0].Message, "416") {
		t.Errorf("the finding does not say what the origin answered: %q", fs[0].Message)
	}
}

// The branches an end-to-end run cannot stage. checkByteRange is a pure
// function of the probes, so the awkward answers — a request that never
// completed, a 206 whose body is not the size it promised — are asserted
// against it directly.

// Whether an origin honours Range used to be reported once per byte-range
// segment, which said the same host-level thing four times on a four-segment
// sample and buried it among the media findings it causes. It is one finding
// now, and it names the host rather than a segment.
func TestCheckByteRange_AnIgnoredRangeIsOneFindingAboutTheHost(t *testing.T) {
	got := checkByteRange([]byteRangeProbe{{
		host: "cdn.example", requested: 500, got: 120000,
		status: 200, needed: true, segments: 4,
	}})

	if len(got) != 1 {
		t.Fatalf("want one finding for one host, got %d:\n%s", len(got), dumpFindings(got))
	}
	if got[0].Status != finding.BAD {
		t.Errorf("a byte-range stream against an origin ignoring Range is %s, want BAD", got[0].Status)
	}
	if got[0].Target != "cdn.example" {
		t.Errorf("target = %q, want the host the fact is about", got[0].Target)
	}
	if !strings.Contains(got[0].Hint, "downstream") {
		t.Errorf("the hint does not say the media findings follow from this: %q", got[0].Hint)
	}
}

// A 206 is a promise about the body. A proxy that stamps the status and forwards
// the whole object breaks it, and reading the status alone would clear it —
// which is why the length is compared rather than trusted.
func TestCheckByteRange_A206WithTheWholeBodyDidNotHonourTheRange(t *testing.T) {
	got := checkByteRange([]byteRangeProbe{{
		host: "cdn.example", requested: 1024, got: 40000,
		status: 206, needed: true, segments: 2,
	}})

	f, ok := findIn(got, "byterange", finding.BAD)
	if !ok {
		t.Fatalf("a 206 carrying the whole resource was not reported:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "206") || !strings.Contains(f.Message, "40000") {
		t.Errorf("the finding does not state the status and the length it disagrees with: %q", f.Message)
	}
}

// A probe that never completed is a hole in the coverage, not an answer. It has
// to sort above BAD and say segcheck could not look, because an origin reported
// as not supporting Range on the strength of a dropped connection sends someone
// to reconfigure a CDN that was fine.
func TestCheckByteRange_AProbeThatNeverCompletedIsAnERROR(t *testing.T) {
	got := checkByteRange([]byteRangeProbe{{
		host: "cdn.example", err: errProbeFailed, segments: 3,
	}})

	f, ok := findIn(got, "byterange", finding.ERROR)
	if !ok {
		t.Fatalf("a failed probe was not reported as a limit:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "could not ask") {
		t.Errorf("the finding reads as a verdict rather than a limit: %q", f.Message)
	}
}

var errProbeFailed = errors.New("connection reset by peer")

// hostOf is what groups the findings, so a URI it cannot read must drop out
// rather than collapse every such segment into one nameless host.
func TestHostOf(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://cdn.example/a/seg0.ts", "cdn.example"},
		{"http://127.0.0.1:8080/seg0.ts", "127.0.0.1:8080"},
		{"seg0.ts", ""},
		{"://nonsense", ""},
	} {
		if got := hostOf(tc.in); got != tc.want {
			t.Errorf("hostOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------- probeByteRanges ----------

func probeSeg(uri string, body int, br *manifest.ByteRange) segmentData {
	return segmentData{
		seg:    manifest.Segment{URI: uri, ByteRange: br},
		res:    fetch.Response{Status: http.StatusOK, Body: make([]byte, body), Header: http.Header{}},
		parsed: true,
	}
}

func probeClient() *fetch.Client {
	return fetch.New(fetch.Options{Timeout: 5 * time.Second})
}

// The host is what groups a probe, so a segment URI that names none cannot be
// probed at all. Saying nothing is right: the alternative is collapsing every
// such segment into one nameless host and reporting a verdict about it.
func TestProbeByteRanges_ASegmentWithNoHostIsNotProbed(t *testing.T) {
	got := probeByteRanges(context.Background(), probeClient(),
		[]*renditionData{rend("720p", withSegs(probeSeg("seg0.ts", 4000, nil)))})

	if len(got) != 0 {
		t.Errorf("a relative segment URI produced %d probes, want none: %+v", len(got), got)
	}
}

// The probe has to ask for less than the resource holds, or a 200 with
// everything back is indistinguishable from a 206 with what was asked for. A
// body too small to cut in half cannot make that distinction, so it is not asked
// — an origin is better left unmeasured than measured by a test that cannot fail.
func TestProbeByteRanges_ABodyTooSmallToHalveIsNotProbed(t *testing.T) {
	got := probeByteRanges(context.Background(), probeClient(),
		[]*renditionData{rend("720p", withSegs(probeSeg("https://cdn.example/seg0.ts", 1, nil)))})

	if len(got) != 0 {
		t.Errorf("a 1-byte segment produced %d probes, want none: %+v", len(got), got)
	}
}

// A segment smaller than the standard probe is asked for half of itself, so a
// small-segment stream is still measured rather than skipped.
func TestProbeByteRanges_ASmallSegmentIsAskedForHalfOfItself(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 100)
		if hdr := r.Header.Get("Range"); hdr != "" {
			asked = hdr
			start, end, ok := parseTestByteRange(hdr, int64(len(body)))
			if !ok {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start : end+1])
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	got := probeByteRanges(context.Background(), probeClient(),
		[]*renditionData{rend("720p", withSegs(probeSeg(srv.URL+"/seg0.ts", 100, nil)))})

	if len(got) != 1 {
		t.Fatalf("want one probe, got %d", len(got))
	}
	if asked != "bytes=0-49" {
		t.Errorf("asked for %q, want half of a 100-byte segment", asked)
	}
	if got[0].requested != 50 || got[0].got != 50 || got[0].status != 206 {
		t.Errorf("probe = %+v, want 50 of 50 with a 206", got[0])
	}
	if !got[0].advertised {
		t.Error("Accept-Ranges: bytes on the probe response was not recorded")
	}
}

// A request that never completed is not an origin without range support. The
// error is carried so the finding can say the coverage has a hole, because an
// origin reconfigured on the strength of a dropped connection was fine.
func TestProbeByteRanges_ATransportFailureIsCarriedNotGuessed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close() // nothing is listening now

	got := probeByteRanges(context.Background(), probeClient(),
		[]*renditionData{rend("720p", withSegs(probeSeg(dead+"/seg0.ts", 4000, nil)))})

	if len(got) != 1 {
		t.Fatalf("want one probe, got %d", len(got))
	}
	if got[0].err == nil {
		t.Fatalf("a connection that was refused produced no error: %+v", got[0])
	}
	if got[0].status != 0 {
		t.Errorf("status = %d, want 0: nothing answered", got[0].status)
	}
}

// A byte-range stream is not probed at all: the sample already asked the origin
// for a range and the answer is in the response it gave.
func TestProbeByteRanges_AByteRangeStreamCostsNoExtraRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(make([]byte, 4000))
	}))
	t.Cleanup(srv.Close)

	sd := probeSeg(srv.URL+"/main.ts", 4000, &manifest.ByteRange{Offset: 0, Length: 500})
	got := probeByteRanges(context.Background(), probeClient(),
		[]*renditionData{rend("720p", withSegs(sd))})

	if requests != 0 {
		t.Errorf("%d extra requests were made; the sample already holds the answer", requests)
	}
	if len(got) != 1 {
		t.Fatalf("want one probe, got %d", len(got))
	}
	if !got[0].needed {
		t.Error("a stream addressed in byte ranges was not recorded as needing them")
	}
	if got[0].requested != 500 || got[0].got != 4000 || got[0].status != 200 {
		t.Errorf("probe = %+v, want the sample's own 500-byte ask answered with 4000 and a 200", got[0])
	}
}
