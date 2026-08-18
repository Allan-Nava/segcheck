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

// A trick-play rung is a playlist of byte ranges, each supposed to hold exactly
// one keyframe. Nothing in the manifest can tell you whether a range actually
// lands on one: the offsets are arithmetic and the arithmetic is done by the
// packager. When it is wrong the scrub preview shows a grey frame or the last
// picture over and over, which gets reported as a player bug.
//
// It is also the one rung where every other check in this tool would be wrong:
// a single picture where a check expects two seconds of media reports as a hole
// in the timeline, a duration mismatch and a bitrate ten times the declared.

const (
	ifTimescale = uint32(90000)
	ifSampleDur = uint32(3600)
	ifSegFrames = 50 // 2s
	ifSegTicks  = int64(180000)
)

type ifEntry struct {
	// sync is whether the picture at this range really is a sync sample, and
	// tfdt where it sits on the timeline. Setting either apart from what the
	// playlist implies is how the two defects are planted.
	sync    bool
	tfdt    int64
	missing bool // the range 404s
}

// newIFrameOrigin serves a one-rung ladder plus its trick-play playlist. The
// I-frame entries are separate resources rather than byte ranges of the video,
// which keeps the fixture readable; the byte-range path is covered in the
// manifest tests, where the arithmetic actually lives.
func newIFrameOrigin(t *testing.T, entries []ifEntry) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:7\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.640028\"\n720p/index.m3u8\n"+
			"#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=94000,RESOLUTION=1280x720,CODECS=\"avc1.640028\",URI=\"720p/iframe.m3u8\"\n",
			syntheticBandwidth)
	})

	mux.HandleFunc("/720p/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
		for i := 0; i < len(entries); i++ {
			fmt.Fprintf(&b, "#EXTINF:2.000,\nseg%d.m4s\n", i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	mux.HandleFunc("/720p/iframe.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		b.WriteString("#EXT-X-I-FRAMES-ONLY\n#EXT-X-MAP:URI=\"init.mp4\"\n")
		for i := range entries {
			fmt.Fprintf(&b, "#EXTINF:2.000,\nif%d.m4s\n", i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	mux.HandleFunc("/720p/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mediatest.MP4Init(1, ifTimescale, "video", 1280, 720))
	})

	for i := range entries {
		i := i
		mux.HandleFunc(fmt.Sprintf("/720p/seg%d.m4s", i), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mediatest.MP4SegmentSync(1, uint32(i), int64(i)*ifSegTicks,
				ifSampleDur, ifSegFrames, 12000, true))
		})
	}
	for i, e := range entries {
		i, e := i, e
		mux.HandleFunc(fmt.Sprintf("/720p/if%d.m4s", i), func(w http.ResponseWriter, _ *http.Request) {
			if e.missing {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("no such range"))
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			// One picture, which is what a trick-play entry is.
			_, _ = w.Write(mediatest.MP4SegmentSync(1, uint32(i), e.tfdt, ifSampleDur, 1, 4000, e.sync))
		})
	}
	return srv.URL + "/master.m3u8"
}

func cleanIFrames(n int) []ifEntry {
	out := make([]ifEntry, n)
	for i := range out {
		out[i] = ifEntry{sync: true, tfdt: int64(i) * ifSegTicks}
	}
	return out
}

func runIFrame(t *testing.T, url string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

// A trick-play rung whose ranges land on keyframes on the video's own timeline
// is healthy — and, crucially, must not be dragged through the checks that read
// a segment as an extent of media. A single picture is not a two-second gap.
func TestRun_ATrickPlayRungThatLandsOnKeyframesIsClean(t *testing.T) {
	url := newIFrameOrigin(t, cleanIFrames(4))

	res := runIFrame(t, url)

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("a clean trick-play rung produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "iframe") {
		t.Errorf("no iframe finding at all: the check did not run:\n%s", dump(res))
	}
}

// The defect: a range that resolves to something that is not a keyframe. Every
// offset in the manifest is well-formed and the playlist reads perfectly.
func TestRun_FindsATrickPlayRangeThatIsNotAKeyframe(t *testing.T) {
	entries := cleanIFrames(4)
	entries[2].sync = false
	url := newIFrameOrigin(t, entries)

	res := runIFrame(t, url)

	f, ok := findFinding(res, "iframe", finding.BAD)
	if !ok {
		t.Fatalf("a trick-play range landing on a non-keyframe was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "keyframe") {
		t.Errorf("the iframe finding does not say what is wrong: %q", f.Message)
	}
}

// The other defect: the trick-play rung is internally fine and sits on a
// different timeline from the video it belongs to, so every scrub lands
// somewhere other than where the preview showed.
func TestRun_FindsATrickPlayRungOnADifferentTimeline(t *testing.T) {
	entries := cleanIFrames(4)
	for i := range entries {
		entries[i].tfdt += 90000 * 30 // half a minute adrift
	}
	url := newIFrameOrigin(t, entries)

	res := runIFrame(t, url)

	f, ok := findFinding(res, "iframe", finding.BAD)
	if !ok {
		t.Fatalf("a trick-play rung on a different timeline was not reported:\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "timeline") {
		t.Errorf("the iframe finding does not name the problem: %q", f.Message)
	}
}

// A range that will not fetch is a scrub that shows nothing at all.
func TestRun_FindsAnUnfetchableTrickPlayRange(t *testing.T) {
	entries := cleanIFrames(4)
	entries[1].missing = true
	url := newIFrameOrigin(t, entries)

	res := runIFrame(t, url)

	if _, ok := findFinding(res, "iframe", finding.BAD); !ok {
		t.Fatalf("a trick-play range that 404s was not reported:\n%s", dump(res))
	}
}

// An MPEG-TS trick-play range is a slice of a transport stream, and a slice that
// does not happen to include the PMT tells a reader nothing about what stream
// type it is looking at. That is a limit of segcheck, and it has to be said out
// loud: silence reads as a clean pass on a rung nobody actually checked.
func TestRun_UnreadableTrickPlayRangesSaySoRatherThanStayingSilent(t *testing.T) {
	url := newIFrameOriginTS(t, 3)

	res := runIFrame(t, url)

	var said bool
	for _, f := range res.Findings {
		if f.Check == "iframe" && strings.Contains(f.Message, "could not") {
			said = true
			if f.Status == finding.BAD {
				t.Errorf("a limit of segcheck was reported as a defect in the stream: %s", f.Message)
			}
		}
	}
	if !said {
		t.Errorf("ranges segcheck could not read produced no finding at all:\n%s", dump(res))
	}
}

// A ladder with no trick-play rung must not gain a row for one.
func TestRun_NoIFrameRungMeansNoIFrameFinding(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runOn(t, srv.URL+"/master.m3u8")

	if hasCheck(res, "iframe") {
		t.Errorf("a ladder with no I-frame rung produced an iframe finding:\n%s", dump(res))
	}
}

// newIFrameOriginTS serves trick-play entries that are slices of a transport
// stream taken past its PAT and PMT — the shape a byte-range I-frame playlist
// really produces, and one no reader can classify.
func newIFrameOriginTS(t *testing.T, n int) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.640028\"\n720p/index.m3u8\n"+
			"#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=94000,RESOLUTION=1280x720,CODECS=\"avc1.640028\",URI=\"720p/iframe.m3u8\"\n",
			syntheticBandwidth)
	})
	playlist := func(prefix string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) {
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
			for i := 0; i < n; i++ {
				fmt.Fprintf(&b, "#EXTINF:2.000,\n%s%d.ts\n", prefix, i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(b.String()))
		}
	}
	mux.HandleFunc("/720p/index.m3u8", playlist("seg"))
	mux.HandleFunc("/720p/iframe.m3u8", playlist("if"))

	for i := 0; i < n; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/720p/seg%d.ts", i), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(mediatest.TSWithSPS(int64(i)*segTicks, frameDur, segFrames, mediatest.SPSFor(1280, 720)))
		})
		mux.HandleFunc(fmt.Sprintf("/720p/if%d.ts", i), func(w http.ResponseWriter, _ *http.Request) {
			full := mediatest.TSWithSPS(int64(i)*segTicks, frameDur, segFrames, mediatest.SPSFor(1280, 720))
			w.Header().Set("Content-Type", "video/mp2t")
			// Past the PAT and the PMT: what a byte range into the middle of a
			// transport stream actually contains.
			_, _ = w.Write(full[188*2:])
		})
	}
	return srv.URL + "/master.m3u8"
}
