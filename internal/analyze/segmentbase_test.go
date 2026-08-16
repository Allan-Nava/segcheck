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
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-19, the half that needs the network.
//
// ParseDASH is handed bytes and returns a model, so it cannot expand a
// SegmentBase representation itself: the subsegment boundaries live in the `sidx`
// box, and reading it means a range request. That is why the expansion happens
// here — the manifest layer says which bytes to ask for, and this layer asks.
//
// The whole point is that these renditions used to be skipped entirely. A run
// against a single-file DASH stream reported one "unsupported" line and nothing
// else: no continuity, no duration, no resolution, no bitrate.

// singleFileOrigin serves one MPD and one media file, honouring Range requests
// the way a CDN does, and records the ranges it was asked for.
type rangeLog struct {
	mu     sync.Mutex
	ranges []string
}

func (l *rangeLog) add(rh string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ranges = append(l.ranges, rh)
}

func (l *rangeLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.ranges...)
}

// The segment fetches run concurrently, so the handler is called from several
// goroutines at once and the log has to be guarded — `go test -race` catches this
// the moment it is not.
func singleFileOrigin(t *testing.T) (*httptest.Server, *rangeLog) {
	t.Helper()

	entries := []mediatest.SIDXEntry{
		{Size: 3000, Duration: 90000, StartsWithSAP: true},
		{Size: 3200, Duration: 90000, StartsWithSAP: true},
		{Size: 2800, Duration: 90000, StartsWithSAP: true},
	}
	file, idxStart, idxEnd := mediatest.SingleFileDASH(1, 90000, 1280, 720, entries)

	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT3S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v0" bandwidth="2400000" width="1280" height="720" codecs="avc1.4d401f">
        <BaseURL>media.mp4</BaseURL>
        <SegmentBase indexRange="` + itoa(idxStart) + `-` + itoa(idxEnd) + `" timescale="90000">
          <Initialization range="0-` + itoa(idxStart-1) + `"/>
        </SegmentBase>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

	log := &rangeLog{}
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/media.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		rh := r.Header.Get("Range")
		if rh == "" {
			_, _ = w.Write(file)
			return
		}
		log.add(rh)
		var first, last int
		if _, err := fmtSscan(rh, &first, &last); err != nil || first > last || last >= len(file) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", "bytes "+itoa(first)+"-"+itoa(last)+"/"+itoa(len(file)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(file[first : last+1])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, log
}

func fmtSscan(rh string, first, last *int) (int, error) {
	var a, b int
	n, err := sscanRange(rh, &a, &b)
	*first, *last = a, b
	return n, err
}

func sscanRange(rh string, a, b *int) (int, error) {
	s := strings.TrimPrefix(rh, "bytes=")
	fst, lst, ok := strings.Cut(s, "-")
	if !ok {
		return 0, errBadRange
	}
	x, err := atoi(fst)
	if err != nil {
		return 0, err
	}
	y, err := atoi(lst)
	if err != nil {
		return 0, err
	}
	*a, *b = x, y
	return 2, nil
}

// A single-file representation is sampled like any other: the index is fetched,
// read, and turned into segments with byte ranges.
func TestRun_SegmentBaseIsSampled(t *testing.T) {
	srv, rangeLog := singleFileOrigin(t)
	res := runOn(t, srv.URL+"/manifest.mpd")

	// The rendition is no longer skipped.
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "not supported") || strings.Contains(f.Message, "SegmentBase") {
			t.Errorf("a SegmentBase rendition was still reported unsupported: %s", f.Message)
		}
	}
	if res.Segments == 0 {
		t.Fatalf("no segments sampled from a single-file representation:\n%s", dump(res))
	}

	// The index was fetched as a range, and so were the segments — not the whole
	// file each time, which on a real stream would be the entire asset per segment.
	asked := rangeLog.all()
	if len(asked) < 2 {
		t.Fatalf("expected range requests for the index and the segments, got %v", asked)
	}
	for _, rh := range asked {
		if !strings.HasPrefix(rh, "bytes=") {
			t.Errorf("range header = %q", rh)
		}
	}

	// And the checks that were previously skipped now have something to say.
	if !hasCheck(res, "container") {
		t.Errorf("the container check said nothing:\n%s", dump(res))
	}
	if !hasCheck(res, "resolution") {
		t.Errorf("the resolution check said nothing:\n%s", dump(res))
	}
	for _, f := range res.Findings {
		if f.Status == finding.BAD {
			t.Errorf("a well-formed single-file stream produced a BAD: %s — %s", f.Check, f.Message)
		}
	}
}

// An index that cannot be fetched leaves the rendition unsampleable, and that is
// a failure to look rather than a defect in the media: ERROR, with the reason.
func TestRun_SegmentBaseIndexThatCannotBeFetched(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT3S">
  <Period><AdaptationSet mimeType="video/mp4">
    <Representation id="v0" bandwidth="800000" width="640" height="360">
      <BaseURL>missing.mp4</BaseURL>
      <SegmentBase indexRange="100-500"><Initialization range="0-99"/></SegmentBase>
    </Representation>
  </AdaptationSet></Period>
</MPD>`
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/missing.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/manifest.mpd")
	var said bool
	for _, f := range res.Findings {
		if strings.Contains(strings.ToLower(f.Message), "index") {
			said = true
			if f.Status == finding.BAD {
				t.Errorf("an unfetchable index was blamed on the media: %s", f.Message)
			}
		}
	}
	if !said {
		t.Errorf("an unfetchable index was not explained:\n%s", dump(res))
	}
}

// Bytes that are not an index at all — an origin serving an error page for the
// range — must be reported rather than parsed into nonsense segments.
func TestRun_SegmentBaseIndexThatIsNotAnIndex(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT3S">
  <Period><AdaptationSet mimeType="video/mp4">
    <Representation id="v0" bandwidth="800000" width="640" height="360">
      <BaseURL>media.mp4</BaseURL>
      <SegmentBase indexRange="0-99"/>
    </Representation>
  </AdaptationSet></Period>
</MPD>`
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/media.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("<html><body>not an index</body></html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/manifest.mpd")
	var said bool
	for _, f := range res.Findings {
		if strings.Contains(strings.ToLower(f.Message), "sidx") || strings.Contains(strings.ToLower(f.Message), "index") {
			said = true
		}
	}
	if !said {
		t.Errorf("an unreadable index was not explained:\n%s", dump(res))
	}
	if res.Segments != 0 {
		t.Errorf("%d segments were invented from bytes that are not an index", res.Segments)
	}
}

// expandSegmentBase is the piece that turns an index into segments, and its
// arithmetic is what decides which bytes get fetched.
func TestExpandSegmentBase(t *testing.T) {
	entries := []mediatest.SIDXEntry{
		{Size: 1000, Duration: 90000, StartsWithSAP: true},
		{Size: 1500, Duration: 45000, StartsWithSAP: true},
	}
	idxBox := mediatest.SIDX(0, 90000, 0, 0, entries)

	segs, err := expandSegmentBase("https://cdn.example.com/d/media.mp4", idxBox, 500, nil, Defaults())
	if err != nil {
		t.Fatalf("expandSegmentBase: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}

	base := int64(500 + len(idxBox))
	if segs[0].ByteRange == nil || segs[0].ByteRange.Offset != base || segs[0].ByteRange.Length != 1000 {
		t.Errorf("first range = %+v, want offset %d length 1000", segs[0].ByteRange, base)
	}
	if segs[1].ByteRange == nil || segs[1].ByteRange.Offset != base+1000 {
		t.Errorf("second offset = %+v, want %d", segs[1].ByteRange, base+1000)
	}
	if segs[0].Duration != 1 || segs[1].Duration != 0.5 {
		t.Errorf("durations = %v, %v; want 1 and 0.5", segs[0].Duration, segs[1].Duration)
	}
	// Every segment points at the same file, and the sequence numbers run in
	// playlist order so continuity can pair them.
	for i, s := range segs {
		if s.URI != "https://cdn.example.com/d/media.mp4" {
			t.Errorf("segment %d URI = %q", i, s.URI)
		}
		if s.Sequence != i+1 {
			t.Errorf("segment %d sequence = %d, want %d", i, s.Sequence, i+1)
		}
	}
}

// A two-level index is followed rather than stopped at. Real on-demand files are
// built this way, and a reader that took the top level for media would hand the
// container parser an index box; one that skipped it would find no media at all.
func TestExpandSegmentBase_FollowsAHierarchicalIndex(t *testing.T) {
	leaves := [][]mediatest.SIDXEntry{
		{{Size: 1000, Duration: 45000, StartsWithSAP: true}, {Size: 1100, Duration: 45000, StartsWithSAP: true}},
		{{Size: 1200, Duration: 90000, StartsWithSAP: true}},
	}
	data := mediatest.HierarchicalSIDX(90000, leaves)

	segs, err := expandSegmentBase("https://cdn.example.com/d/media.mp4", data, 0, nil, Defaults())
	if err != nil {
		t.Fatalf("expandSegmentBase: %v", err)
	}
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want the 3 the leaves describe", len(segs))
	}
	wantLen := []int64{1000, 1100, 1200}
	for i, s := range segs {
		if s.ByteRange == nil || s.ByteRange.Length != wantLen[i] {
			t.Errorf("segment %d range = %+v, want length %d", i, s.ByteRange, wantLen[i])
		}
	}
	if segs[0].Duration != 0.5 || segs[2].Duration != 1 {
		t.Errorf("durations = %v .. %v; want 0.5 and 1", segs[0].Duration, segs[2].Duration)
	}
}

func TestExpandSegmentBase_UnreadableIndex(t *testing.T) {
	if _, err := expandSegmentBase("https://x/media.mp4", []byte("not an index"), 0, nil, Defaults()); err == nil {
		t.Error("bytes that are not an index expanded into segments")
	}
}

var errBadRange = errBadRangeType{}

type errBadRangeType struct{}

func (errBadRangeType) Error() string { return "bad range" }

func atoi(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errBadRange
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadRange
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var _ = context.Background
var _ = fetch.Options{}
var _ = time.Second

// An index whose every reference points at another index describes no media at
// all. Returning an empty segment list would leave the rendition looking sampled
// and empty; it has to say why instead.
func TestExpandSegmentBase_IndexWithNoMedia(t *testing.T) {
	entries := []mediatest.SIDXEntry{
		{Size: 100, Duration: 90000, Reference: true},
		{Size: 200, Duration: 90000, Reference: true},
	}
	// The leaves are not in the bytes to hand, so they cannot be followed — which
	// has to be said rather than silently yielding nothing.
	_, err := expandSegmentBase("https://x/media.mp4",
		mediatest.SIDX(0, 90000, 0, 0, entries), 0, nil, Defaults())
	if err == nil {
		t.Fatal("an index whose leaves were never read expanded into segments")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("err = %v, want it to say the leaves were outside the bytes read", err)
	}
}

// The on-demand profile: only a BaseURL, no SegmentBase at all. The index has to
// be found by reading the head of the file, and the bytes before it are the
// initialisation segment — without which the fragments parse with no timescale
// and every duration reads as zero.
func TestRun_OnDemandProfileWithNoSegmentBase(t *testing.T) {
	const timescale = 90000
	entries := []mediatest.SIDXEntry{
		{Size: 4000, Duration: timescale, StartsWithSAP: true},
		{Size: 4200, Duration: timescale, StartsWithSAP: true},
	}
	// ftyp + moov (with trex) + sidx + the media, which is the on-demand layout.
	init := mediatest.MP4InitTrex(1, timescale, 1280, 720, 3600, 0)
	idx := mediatest.SIDX(0, timescale, 0, 0, entries)
	file := append([]byte{}, init...)
	file = append(file, idx...)
	for i, e := range entries {
		frag := mediatest.MP4SegmentNoDurations(1, uint32(i+1), int64(i)*int64(e.Duration), 25, 0)
		if len(frag) < int(e.Size) {
			frag = append(frag, make([]byte, int(e.Size)-len(frag))...)
		}
		file = append(file, frag[:e.Size]...)
	}

	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT2S">
  <Period><AdaptationSet mimeType="video/mp4">
    <Representation id="v0" bandwidth="2400000" width="1280" height="720" codecs="avc1.4d401f">
      <BaseURL>media.mp4</BaseURL>
    </Representation>
  </AdaptationSet></Period>
</MPD>`

	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/media.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		rh := r.Header.Get("Range")
		if rh == "" {
			_, _ = w.Write(file)
			return
		}
		var first, last int
		if _, err := sscanRange(rh, &first, &last); err != nil {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if last >= len(file) {
			last = len(file) - 1
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(file[first : last+1])
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/manifest.mpd")
	if res.Segments == 0 {
		t.Fatalf("no segments sampled from an on-demand representation:\n%s", dump(res))
	}
	// The init segment was found and applied, so the media has a real duration
	// rather than the zero that makes every boundary look like a gap.
	for _, f := range res.Findings {
		if f.Status == finding.BAD {
			t.Errorf("a well-formed on-demand stream produced a BAD: %s — %s", f.Check, f.Message)
		}
	}
	if !hasCheck(res, "resolution") {
		t.Errorf("the resolution check said nothing, so the init was never applied:\n%s", dump(res))
	}
}

// A file whose index sits past the probe cannot be located. That is the tool
// failing to look, not a defect in the stream, and it has to say which.
func TestRun_OnDemandIndexBeyondTheProbe(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT2S">
  <Period><AdaptationSet mimeType="video/mp4">
    <Representation id="v0" bandwidth="800000" width="640" height="360">
      <BaseURL>media.mp4</BaseURL>
    </Representation>
  </AdaptationSet></Period>
</MPD>`
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})
	mux.HandleFunc("/media.mp4", func(w http.ResponseWriter, r *http.Request) {
		// A big ftyp and nothing else in the probe window: no index to be found.
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(mediatest.MP4Init(1, 90000, "video", 640, 360))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := runOn(t, srv.URL+"/manifest.mpd")
	var said bool
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "no segment index found") {
			said = true
			if f.Status == finding.BAD {
				t.Errorf("failing to find the index was blamed on the media: %s", f.Message)
			}
		}
	}
	if !said {
		t.Errorf("a file with no locatable index was not explained:\n%s", dump(res))
	}
}
