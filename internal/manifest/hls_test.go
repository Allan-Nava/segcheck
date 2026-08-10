package manifest

import (
	"testing"
)

const masterPlaylist = `#EXTM3U
#EXT-X-VERSION:4
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1200000,AVERAGE-BANDWIDTH=1000000,RESOLUTION=1280x720,FRAME-RATE=25.000,CODECS="avc1.4d401f,mp4a.40.2",AUDIO="aac"
720p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4500000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aac"
1080p/index.m3u8
`

func TestParseHLS_Master(t *testing.T) {
	pl, err := ParseHLS([]byte(masterPlaylist), "https://cdn.example/hls/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if !pl.Master {
		t.Fatal("Master = false, want true")
	}
	if len(pl.Renditions) != 3 {
		t.Fatalf("renditions = %d, want 3 (2 video + 1 audio)", len(pl.Renditions))
	}
	video := pl.VideoRenditions()
	if len(video) != 2 {
		t.Fatalf("video renditions = %d, want 2", len(video))
	}

	first := video[0]
	if first.Name != "720p" {
		t.Errorf("name = %q, want 720p", first.Name)
	}
	if first.Bandwidth != 1200000 || first.AvgBandwidth != 1000000 {
		t.Errorf("bandwidth = %d/%d, want 1200000/1000000", first.Bandwidth, first.AvgBandwidth)
	}
	if first.Width != 1280 || first.Height != 720 {
		t.Errorf("resolution = %dx%d, want 1280x720", first.Width, first.Height)
	}
	if first.FrameRate != 25 {
		t.Errorf("frame rate = %v, want 25", first.FrameRate)
	}
	// The comma inside the quoted CODECS value must not split the attribute.
	if first.Codecs != "avc1.4d401f,mp4a.40.2" {
		t.Errorf("codecs = %q, want %q — the quoted comma was mishandled", first.Codecs, "avc1.4d401f,mp4a.40.2")
	}
	if first.AudioGroup != "aac" {
		t.Errorf("audio group = %q, want aac", first.AudioGroup)
	}
	if want := "https://cdn.example/hls/720p/index.m3u8"; first.URI != want {
		t.Errorf("URI = %q, want %q", first.URI, want)
	}

	audio := byKindTest(pl.Renditions, Audio)
	if len(audio) != 1 {
		t.Fatalf("audio renditions = %d, want 1", len(audio))
	}
	if audio[0].GroupID != "aac" || audio[0].Language != "en" {
		t.Errorf("audio group/lang = %q/%q, want aac/en", audio[0].GroupID, audio[0].Language)
	}
}

const mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:100
#EXT-X-MAP:URI="init.mp4"
#EXT-X-KEY:METHOD=AES-128,URI="https://key.example/k1",IV=0x0123
#EXT-X-PROGRAM-DATE-TIME:2026-08-10T12:00:00.000Z
#EXTINF:6.000,
seg100.m4s
#EXTINF:6.000,
seg101.m4s
#EXT-X-DISCONTINUITY
#EXTINF:5.500,
seg102.m4s
#EXT-X-ENDLIST
`

func TestParseHLS_MediaPlaylist(t *testing.T) {
	pl, err := ParseHLS([]byte(mediaPlaylist), "https://cdn.example/hls/720p/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if pl.Master {
		t.Error("Master = true for a media playlist")
	}
	if pl.Live {
		t.Error("Live = true despite EXT-X-ENDLIST")
	}
	if pl.TargetDuration != 6 {
		t.Errorf("target duration = %v, want 6", pl.TargetDuration)
	}
	if len(pl.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(pl.Segments))
	}

	s0 := pl.Segments[0]
	if s0.Sequence != 100 {
		t.Errorf("first sequence = %d, want 100 (EXT-X-MEDIA-SEQUENCE)", s0.Sequence)
	}
	if want := "https://cdn.example/hls/720p/init.mp4"; s0.InitURI != want {
		t.Errorf("init URI = %q, want %q", s0.InitURI, want)
	}
	if s0.KeyMethod != "AES-128" {
		t.Errorf("key method = %q, want AES-128", s0.KeyMethod)
	}
	if !s0.HasPDT || s0.PDT.Format("15:04:05") != "12:00:00" {
		t.Errorf("PDT = %v (has=%v), want 12:00:00", s0.PDT, s0.HasPDT)
	}
	if s0.Discontinuity {
		t.Error("first segment flagged discontinuous")
	}

	// The EXT-X-KEY and EXT-X-MAP in force carry forward to later segments.
	if pl.Segments[1].KeyMethod != "AES-128" || pl.Segments[1].InitURI == "" {
		t.Error("key/map did not carry to the second segment")
	}
	// EXT-X-DISCONTINUITY belongs to the segment that follows it.
	if !pl.Segments[2].Discontinuity {
		t.Error("third segment not flagged discontinuous")
	}
	if pl.Segments[2].Sequence != 102 {
		t.Errorf("third sequence = %d, want 102", pl.Segments[2].Sequence)
	}
	if pl.Segments[2].Duration != 5.5 {
		t.Errorf("third duration = %v, want 5.5", pl.Segments[2].Duration)
	}
}

func TestParseHLS_LiveWithoutEndList(t *testing.T) {
	body := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\na.ts\n"
	pl, err := ParseHLS([]byte(body), "https://cdn.example/live.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if !pl.Live {
		t.Error("Live = false without EXT-X-ENDLIST")
	}
}

func TestParseHLS_ByteRanges(t *testing.T) {
	body := `#EXTM3U
#EXT-X-TARGETDURATION:4
#EXTINF:4.0,
#EXT-X-BYTERANGE:75232@0
video.ts
#EXTINF:4.0,
#EXT-X-BYTERANGE:82112
video.ts
#EXT-X-ENDLIST
`
	pl, err := ParseHLS([]byte(body), "https://cdn.example/br.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(pl.Segments))
	}
	if br := pl.Segments[0].ByteRange; br == nil || br.Offset != 0 || br.Length != 75232 {
		t.Fatalf("first range = %+v, want offset 0 length 75232", br)
	}
	// A range with no offset continues where the previous one ended.
	br := pl.Segments[1].ByteRange
	if br == nil || br.Offset != 75232 || br.Length != 82112 {
		t.Fatalf("second range = %+v, want offset 75232 length 82112", br)
	}
	if got, want := br.Header(), "bytes=75232-157343"; got != want {
		t.Errorf("Range header = %q, want %q", got, want)
	}
}

func TestParseHLS_RejectsNonPlaylist(t *testing.T) {
	if _, err := ParseHLS([]byte("<html>404</html>"), "https://cdn.example/x.m3u8"); err == nil {
		t.Fatal("an HTML page parsed as a playlist")
	}
}

func TestParseAttrs(t *testing.T) {
	got := parseAttrs(`BANDWIDTH=1200000,CODECS="avc1.4d401f,mp4a.40.2",RESOLUTION=1280x720,NAME="A,B"`)
	if got["CODECS"] != "avc1.4d401f,mp4a.40.2" {
		t.Errorf("CODECS = %q", got["CODECS"])
	}
	if got["NAME"] != "A,B" {
		t.Errorf("NAME = %q, want A,B", got["NAME"])
	}
	if got["BANDWIDTH"] != "1200000" {
		t.Errorf("BANDWIDTH = %q", got["BANDWIDTH"])
	}
	if got["RESOLUTION"] != "1280x720" {
		t.Errorf("RESOLUTION = %q", got["RESOLUTION"])
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name, url, ctype string
		body             string
		want             string
	}{
		{"mpd extension", "https://x/y.mpd", "", "", KindDASH},
		{"m3u8 extension", "https://x/y.m3u8", "", "", KindHLS},
		{"content type dash", "https://x/y", "application/dash+xml", "", KindDASH},
		{"body sniff", "https://x/manifest", "text/plain", `<?xml version="1.0"?><MPD>`, KindDASH},
		{"default hls", "https://x/manifest", "", "#EXTM3U", KindHLS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.url, tc.ctype, []byte(tc.body)); got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

func byKindTest(rs []Rendition, kind StreamKind) []Rendition {
	var out []Rendition
	for _, r := range rs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func TestParseHLS_AudioOnlyVariantIsNotVideo(t *testing.T) {
	// A variant with no RESOLUTION whose CODECS lists only audio is the
	// audio-only rung many real ladders carry. Classing it as video would make
	// its absent video track look like a packaging defect.
	body := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=41457,CODECS="mp4a.40.2"
audio/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2"
720p/index.m3u8
`
	pl, err := ParseHLS([]byte(body), "https://cdn.example/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Renditions) != 2 {
		t.Fatalf("renditions = %d, want 2", len(pl.Renditions))
	}
	if got := pl.Renditions[0].Kind; got != Audio {
		t.Errorf("audio-only variant classified as %q, want %q", got, Audio)
	}
	if got := pl.Renditions[1].Kind; got != Video {
		t.Errorf("video variant classified as %q, want %q", got, Video)
	}
	if n := len(pl.VideoRenditions()); n != 1 {
		t.Errorf("VideoRenditions = %d, want 1 — the ladder must not count the audio rung", n)
	}
}

func TestRendition_DeclaresVideo(t *testing.T) {
	tests := []struct {
		name string
		r    Rendition
		want bool
	}{
		{"resolution only", Rendition{Width: 1280, Height: 720}, true},
		{"video codec only", Rendition{Codecs: "avc1.4d401f,mp4a.40.2"}, true},
		{"hevc codec", Rendition{Codecs: "hvc1.2.4.L120.90"}, true},
		{"audio codec only", Rendition{Codecs: "mp4a.40.2"}, false},
		{"nothing declared", Rendition{Bandwidth: 500000}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.DeclaresVideo(); got != tc.want {
				t.Errorf("DeclaresVideo = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseHLS_MapByteRange(t *testing.T) {
	// Apple's reference streams put the init segment in the same file as every
	// media segment: ignoring EXT-X-MAP BYTERANGE means downloading the whole
	// asset as the "init".
	body := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:1
#EXT-X-MAP:URI="main.mp4",BYTERANGE="721@0"
#EXTINF:6.00000,
#EXT-X-BYTERANGE:5874288@721
main.mp4
#EXT-X-ENDLIST
`
	pl, err := ParseHLS([]byte(body), "https://cdn.example/v9/prog_index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	s := pl.Segments[0]
	if want := "https://cdn.example/v9/main.mp4"; s.InitURI != want {
		t.Errorf("init URI = %q, want %q", s.InitURI, want)
	}
	if s.InitRange == nil {
		t.Fatal("EXT-X-MAP BYTERANGE was dropped")
	}
	if s.InitRange.Offset != 0 || s.InitRange.Length != 721 {
		t.Errorf("init range = %+v, want offset 0 length 721", s.InitRange)
	}
	if got, want := s.InitRange.Header(), "bytes=0-720"; got != want {
		t.Errorf("init Range header = %q, want %q", got, want)
	}
}
