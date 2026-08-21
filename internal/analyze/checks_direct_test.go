package analyze

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// The checks, called directly with hand-built rendition data.
//
// Going through an HTTP fixture cannot produce most of these states: a truncated
// body, an origin that ignored a Range request, a rendition whose segments are
// encrypted so opaquely that the container cannot be identified, a track layout
// that changes half way through. Each one is a branch that decides between a BAD
// an operator must act on, a WARN, and the OK-level "segcheck could not look"
// that the rules require whenever the limit is ours rather than the stream's.

// ---------- construction helpers ----------

func rend(name string, opts ...func(*renditionData)) *renditionData {
	rd := &renditionData{r: manifest.Rendition{
		Name: name, Kind: manifest.Video, URI: "https://cdn.example.com/hls/" + name + "/index.m3u8",
	}}
	for _, o := range opts {
		o(rd)
	}
	return rd
}

func withSegs(segs ...segmentData) func(*renditionData) {
	return func(rd *renditionData) { rd.segs = segs }
}

// okSeg is a segment that fetched and parsed cleanly.
func okSeg(seq int, container string, tracks ...media.Track) segmentData {
	return segmentData{
		seg:    manifest.Segment{Sequence: seq, URI: fmt.Sprintf("seg%d.ts", seq), Duration: 4},
		res:    fetch.Response{Status: http.StatusOK, Body: make([]byte, 1000)},
		info:   media.SegmentInfo{Container: container, Bytes: 1000, Tracks: tracks},
		parsed: true,
	}
}

func videoTrack() media.Track {
	return media.Track{ID: 1, Kind: media.Video, Codec: "h264", Width: 1280, Height: 720,
		Timescale: 90000, HasPTS: true, MinPTS: 0, MaxPTS: 356400, FrameDur: 3600, Samples: 100}
}

func audioTrack() media.Track {
	return media.Track{ID: 2, Kind: media.Audio, Codec: "aac",
		Timescale: 90000, HasPTS: true, MinPTS: 0, MaxPTS: 356400, FrameDur: 3600, Samples: 100}
}

func findIn(fs []finding.Finding, check string, status finding.Status) (finding.Finding, bool) {
	for _, f := range fs {
		if f.Check == check && f.Status == status {
			return f, true
		}
	}
	return finding.Finding{}, false
}

func dumpFindings(fs []finding.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "  %-5s %-10s %-20s %s\n", f.Status, f.Check, f.Target, f.Message)
	}
	return b.String()
}

// ---------- fetch ----------

// A rendition whose playlist resolved but listed no segments is a real failure —
// an empty live window, a template that expanded to nothing — and it has to be
// reported. Saying nothing would be a rendition that passed without being looked
// at.
func TestCheckFetch_EmptyPlaylist(t *testing.T) {
	got := checkFetch([]*renditionData{rend("720p")})
	f, ok := findIn(got, "fetch", finding.BAD)
	if !ok {
		t.Fatalf("no BAD fetch finding:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "no segment to sample") {
		t.Errorf("message = %q", f.Message)
	}
}

// A segment past the byte cap was only partly downloaded, so every measurement
// from it is partial. That is a limit of the tool, and it has to say so rather
// than report the short duration it measured as a defect.
func TestCheckFetch_TruncatedSegment(t *testing.T) {
	sd := okSeg(1, media.ContainerTS, videoTrack())
	sd.res.Truncated = true

	got := checkFetch([]*renditionData{rend("720p", withSegs(sd))})
	f, ok := findIn(got, "fetch", finding.WARN)
	if !ok {
		t.Fatalf("no WARN for a truncated segment:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "truncated") {
		t.Errorf("message = %q", f.Message)
	}
	if !strings.Contains(f.Hint, "--max-bytes") {
		t.Errorf("hint = %q, want it to name the flag that raises the cap", f.Hint)
	}
}

// A 206 for a byte-range segment is correct and must stay silent.
func TestCheckFetch_HonouredRangeIsNotAFinding(t *testing.T) {
	sd := okSeg(1, media.ContainerTS, videoTrack())
	sd.seg.ByteRange = &manifest.ByteRange{Offset: 1000, Length: 500}
	sd.res.Status = http.StatusPartialContent

	for _, f := range checkFetch([]*renditionData{rend("720p", withSegs(sd))}) {
		if f.Status != finding.OK {
			t.Errorf("a correctly honoured Range produced %s: %s", f.Status, f.Message)
		}
	}
}

// ---------- init ----------

// Without its initialisation segment an fMP4 rendition has no timescale, codec or
// resolution, so several checks cannot run. ERROR is right — it sorts above BAD
// because the operator needs to know the coverage has a hole — and it must not be
// mistaken for a defect in the media.
func TestCheckInit_MissingInitSegment(t *testing.T) {
	rd := rend("720p", withSegs(okSeg(1, media.ContainerMP4, videoTrack())))
	rd.initErr = errors.New("init.mp4: 404 Not Found")

	got := checkInit([]*renditionData{rd})
	f, ok := findIn(got, "init", finding.ERROR)
	if !ok {
		t.Fatalf("no ERROR for a missing init segment:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "404") {
		t.Errorf("message = %q, want the underlying error", f.Message)
	}
	if !strings.Contains(f.Hint, "EXT-X-MAP") {
		t.Errorf("hint = %q, want it to name what is missing", f.Hint)
	}
}

// ---------- container ----------

// A full-segment AES-128 segment cannot be parsed at all without the key. That is
// segcheck's limit, not a broken stream, so it is reported at OK level and the
// content checks stay quiet.
func TestCheckContainer_EncryptedSegmentsAreReportedAsOpaque(t *testing.T) {
	var segs []segmentData
	for i := 1; i <= 3; i++ {
		sd := segmentData{
			seg:      manifest.Segment{Sequence: i, KeyMethod: "AES-128", Duration: 4},
			res:      fetch.Response{Status: http.StatusOK, Body: make([]byte, 1000)},
			parseErr: media.ErrUnknownContainer,
		}
		segs = append(segs, sd)
	}
	got := checkContainer([]*renditionData{rend("720p", withSegs(segs...))})

	f, ok := findIn(got, "container", finding.OK)
	if !ok {
		t.Fatalf("no OK-level finding for encrypted segments:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "AES-128") {
		t.Errorf("message = %q, want it to name the encryption", f.Message)
	}
	// Crucially not a BAD: nothing here says the stream is broken.
	for _, f := range got {
		if f.Status == finding.BAD {
			t.Errorf("encrypted segments produced a BAD: %s", f.Message)
		}
	}
}

// A container segcheck can fetch but not yet parse — MP3 packed audio — is the
// same class of answer: an honest OK saying the tool could not look.
func TestCheckContainer_UnsupportedContainerIsOKLevel(t *testing.T) {
	sd := segmentData{
		seg:      manifest.Segment{Sequence: 1, Duration: 4},
		res:      fetch.Response{Status: http.StatusOK, Body: make([]byte, 1000)},
		parseErr: fmt.Errorf("%w: MPEG audio (MP3) packed audio", media.ErrUnsupportedContainer),
	}
	got := checkContainer([]*renditionData{rend("audio", withSegs(sd))})

	f, ok := findIn(got, "container", finding.OK)
	if !ok {
		t.Fatalf("no OK-level finding for an unsupported container:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "MP3") {
		t.Errorf("message = %q, want it to name the container", f.Message)
	}
	for _, f := range got {
		if f.Status == finding.BAD {
			t.Errorf("an unsupported container produced a BAD: %s", f.Message)
		}
	}
}

// A rendition that switches container mid-stream forces a decoder reset. Players
// vary in how well they survive it, so it is a WARN naming the mix.
func TestCheckContainer_MixedContainersInOneRendition(t *testing.T) {
	got := checkContainer([]*renditionData{rend("720p", withSegs(
		okSeg(1, media.ContainerTS, videoTrack()),
		okSeg(2, media.ContainerMP4, videoTrack()),
		okSeg(3, media.ContainerTS, videoTrack()),
	))})

	f, ok := findIn(got, "container", finding.WARN)
	if !ok {
		t.Fatalf("no WARN for mixed containers:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "mixed containers") {
		t.Errorf("message = %q", f.Message)
	}
	// The counts are sorted, so two runs render identically.
	if !strings.Contains(f.Message, "mp4×1, ts×2") {
		t.Errorf("message = %q, want the sorted counts", f.Message)
	}
}

// ---------- tracks ----------

// A variant that promises video by RESOLUTION and delivers none is a real defect:
// the player will show nothing.
func TestCheckTracks_DeclaredVideoIsMissing(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, audioTrack()),
		okSeg(2, media.ContainerTS, audioTrack()),
	))
	rd.r.Width, rd.r.Height = 1280, 720 // the manifest promised video

	got := checkTracks([]*renditionData{rd})
	f, ok := findIn(got, "tracks", finding.BAD)
	if !ok {
		t.Fatalf("no BAD when declared video is absent:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "2/2") {
		t.Errorf("message = %q, want it to count the affected segments", f.Message)
	}
}

// A variant with neither RESOLUTION nor a video codec that carries no video is an
// audio-only rung — the bottom of many real ladders — and reporting it as a
// defect would be a false positive on an ordinary stream.
func TestCheckTracks_AudioOnlyVariantIsLegitimate(t *testing.T) {
	rd := rend("audio", withSegs(
		okSeg(1, media.ContainerTS, audioTrack()),
		okSeg(2, media.ContainerTS, audioTrack()),
	))
	// Declared as a video-kind variant but promising nothing: no RESOLUTION, no
	// video codec.
	rd.r.Codecs = "mp4a.40.2"

	got := checkTracks([]*renditionData{rd})
	if f, ok := findIn(got, "tracks", finding.BAD); ok {
		t.Errorf("an audio-only variant produced a BAD: %s", f.Message)
	}
	f, ok := findIn(got, "tracks", finding.OK)
	if !ok {
		t.Fatalf("no OK finding for an audio-only variant:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "audio-only") {
		t.Errorf("message = %q", f.Message)
	}
}

// Some segments with video and some without, in a variant that declared nothing,
// is neither clearly fine nor clearly broken: it is a WARN that says what was
// seen.
func TestCheckTracks_SomeSegmentsCarryVideoAndSomeDoNot(t *testing.T) {
	rd := rend("mixed", withSegs(
		okSeg(1, media.ContainerTS, videoTrack(), audioTrack()),
		okSeg(2, media.ContainerTS, audioTrack()),
		okSeg(3, media.ContainerTS, videoTrack(), audioTrack()),
	))
	rd.r.Codecs = "" // nothing declared either way

	got := checkTracks([]*renditionData{rd})
	f, ok := findIn(got, "tracks", finding.WARN)
	if !ok {
		t.Fatalf("no WARN for a partial video track:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "1/3") {
		t.Errorf("message = %q, want it to count the affected segments", f.Message)
	}
}

// The set of tracks must not change between segments: a player builds its decoder
// pipeline from the first one, and a mid-rendition change is a visible freeze.
func TestCheckTracks_TrackLayoutChangesBetweenSegments(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, videoTrack(), audioTrack()),
		okSeg(2, media.ContainerTS, videoTrack()),
		okSeg(3, media.ContainerTS, videoTrack(), audioTrack()),
	))
	rd.r.Width, rd.r.Height = 1280, 720

	got := checkTracks([]*renditionData{rd})
	var found bool
	for _, f := range got {
		if f.Status == finding.WARN && strings.Contains(f.Message, "track layout changes") {
			found = true
			// Sorted counts again, so the message is stable run to run.
			if !strings.Contains(f.Message, "1 video×1") || !strings.Contains(f.Message, "1 video + 1 audio×2") {
				t.Errorf("message = %q, want the sorted shape counts", f.Message)
			}
		}
	}
	if !found {
		t.Fatalf("no WARN for a changing track layout:\n%s", dumpFindings(got))
	}
}

// A rendition that could not be sampled at all, or whose init is missing, has
// nothing to say about tracks — and blaming the media for a fetch failure is
// exactly what the rules forbid.
func TestCheckTracks_StaysQuietWhenThereIsNothingToLookAt(t *testing.T) {
	failed := rend("720p")
	failed.err = errors.New("media playlist: 500")

	noInit := rend("1080p", withSegs(okSeg(1, media.ContainerMP4, videoTrack())))
	noInit.initErr = errors.New("init.mp4: 404")

	empty := rend("360p") // resolved, but no segments

	got := checkTracks([]*renditionData{failed, noInit, empty})
	if len(got) != 0 {
		t.Errorf("checkTracks spoke about renditions it could not look at:\n%s", dumpFindings(got))
	}
}

// ---------- small helpers ----------

// The label is what an operator reads to find the rendition. An audio rendition
// whose name does not already say so is prefixed, so "English" does not look like
// a video rung.
//
// It takes the manifest rendition rather than the sampled one because the watch
// loop re-reads the manifest and never samples: two spellings of one rung in one
// report reads as two rungs, and "150kbps" beside "audio 150kbps" was exactly
// that on a live stream.
func TestRendLabel(t *testing.T) {
	tests := []struct {
		name string
		r    manifest.Rendition
		want string
	}{
		{"video by name", manifest.Rendition{Name: "720p", Kind: manifest.Video}, "720p"},
		{"audio gets a prefix", manifest.Rendition{Name: "English", Kind: manifest.Audio}, "audio English"},
		{"audio already says so", manifest.Rendition{Name: "audio-en", Kind: manifest.Audio}, "audio-en"},
		{
			"no name falls back to the URL tail",
			manifest.Rendition{URI: "https://cdn.example.com/hls/720p/index.m3u8"},
			"…/720p/index.m3u8",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rendLabel(tc.r); got != tc.want {
				t.Errorf("rendLabel = %q, want %q", got, tc.want)
			}
			// The sampled rendition must name itself the same way, since a
			// finding about a segment and a finding about that rendition's live
			// edge sit in the same table.
			if got := rendLabel((&renditionData{r: tc.r}).r); got != tc.want {
				t.Errorf("rendLabel via renditionData = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSegLabel(t *testing.T) {
	rd := &renditionData{r: manifest.Rendition{Name: "720p", Kind: manifest.Video}}
	got := segLabel(rd, segmentData{seg: manifest.Segment{Sequence: 42}})
	if got != "720p seg 42" {
		t.Errorf("segLabel = %q, want \"720p seg 42\"", got)
	}
}

// An unknown timescale means no measurement is possible. Returning zero seconds
// would be a confident wrong number, so callers check the timescale first — this
// pins the guard so it cannot be removed as redundant.
func TestToSec(t *testing.T) {
	if got := toSec(90000, 90000); got != 1 {
		t.Errorf("toSec(90000, 90000) = %v, want 1", got)
	}
	if got := toSec(45000, 90000); got != 0.5 {
		t.Errorf("toSec = %v, want 0.5", got)
	}
	if got := toSec(90000, 0); got != 0 {
		t.Errorf("toSec with an unknown timescale = %v, want 0", got)
	}
}

// Only full-segment AES encryption makes a segment opaque. SAMPLE-AES leaves the
// container readable, so the timeline and duration checks can still run — calling
// it opaque would silence them for no reason.
func TestIsFullSegmentEncryption(t *testing.T) {
	for _, m := range []string{"AES-128", "aes-128", "AES-256", "Aes-256"} {
		if !isFullSegmentEncryption(m) {
			t.Errorf("%q is full-segment encryption", m)
		}
	}
	for _, m := range []string{"", "NONE", "SAMPLE-AES", "SAMPLE-AES-CTR", "cenc"} {
		if isFullSegmentEncryption(m) {
			t.Errorf("%q is not full-segment encryption", m)
		}
	}
}

func TestShortTargetAndIndexOfAny(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://cdn.example.com/hls/720p/index.m3u8", "…/720p/index.m3u8"},
		// A query string is dropped: a signed URL's token is noise in a report,
		// and it can be a credential.
		{"https://cdn.example.com/hls/720p/index.m3u8?token=secret", "…/720p/index.m3u8"},
		{"https://cdn.example.com/hls/720p/index.m3u8#frag", "…/720p/index.m3u8"},
		// Too few slashes to shorten: returned whole.
		{"index.m3u8", "index.m3u8"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := shortTarget(tc.in); got != tc.want {
			t.Errorf("shortTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if got := indexOfAny("abc?def", "?#"); got != 3 {
		t.Errorf("indexOfAny = %d, want 3", got)
	}
	if got := indexOfAny("abc#def", "?#"); got != 3 {
		t.Errorf("indexOfAny = %d, want 3", got)
	}
	if got := indexOfAny("abcdef", "?#"); got != -1 {
		t.Errorf("indexOfAny with no match = %d, want -1", got)
	}
	if got := indexOfAny("", "?#"); got != -1 {
		t.Errorf("indexOfAny on an empty string = %d, want -1", got)
	}
}
