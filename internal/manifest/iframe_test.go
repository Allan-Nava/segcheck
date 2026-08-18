package manifest

import "testing"

// EXT-X-I-FRAME-STREAM-INF is the trick-play rung: a playlist of byte ranges,
// each of which is supposed to be exactly one keyframe. It is unlike every
// other variant in two ways that matter to a parser — the URI is an attribute
// rather than the line that follows, and the entries are byte ranges of a file
// the ordinary rungs also serve.
func TestParseHLS_IFrameStreamInf(t *testing.T) {
	master := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720,CODECS="avc1.4d401f"
720p/index.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=94000,RESOLUTION=1280x720,CODECS="avc1.4d401f",URI="720p/iframe.m3u8"
`
	pl, err := ParseHLS([]byte(master), "https://cdn.example/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Renditions) != 2 {
		t.Fatalf("parsed %d renditions, want 2", len(pl.Renditions))
	}

	// The trick-play rung must not read as a video rung: everything that treats a
	// segment as an extent of media would report a single picture as a hole.
	video := pl.VideoRenditions()
	if len(video) != 1 {
		t.Fatalf("VideoRenditions() = %d, want 1: an I-frame rung is not a video rung", len(video))
	}

	var iframe *Rendition
	for i := range pl.Renditions {
		if pl.Renditions[i].Kind == IFrame {
			iframe = &pl.Renditions[i]
		}
	}
	if iframe == nil {
		t.Fatal("EXT-X-I-FRAME-STREAM-INF was dropped")
	}
	if want := "https://cdn.example/720p/iframe.m3u8"; iframe.URI != want {
		t.Errorf("URI = %q, want %q — it is an attribute here, not the next line", iframe.URI, want)
	}
	if iframe.Bandwidth != 94000 || iframe.Width != 1280 || iframe.Height != 720 {
		t.Errorf("attributes lost: bandwidth=%d %dx%d", iframe.Bandwidth, iframe.Width, iframe.Height)
	}
}

// An I-frame media playlist is byte ranges into a file, with the same
// "continue from the previous range" rule as any other. Getting that wrong
// fetches the same picture repeatedly and never fetches the rest.
func TestParseHLS_IFramePlaylistByteRanges(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n"+
		"#EXT-X-MAP:URI=\"init.mp4\"\n"+
		"#EXT-X-BYTERANGE:9000@1000\n#EXTINF:2.0,\nall.m4s\n"+
		"#EXT-X-BYTERANGE:8000\n#EXTINF:2.0,\nall.m4s\n#EXT-X-ENDLIST\n"),
		"https://cdn.example/720p/iframe.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(pl.Segments))
	}
	if br := pl.Segments[1].ByteRange; br == nil || br.Offset != 10000 || br.Length != 8000 {
		t.Errorf("second range = %+v, want 8000@10000 continuing from the first", br)
	}
}

// An EXT-X-I-FRAME-STREAM-INF with no URI names nothing a player can fetch, and
// adding a rendition for it would put a rung in the report with nowhere to go.
func TestParseHLS_IFrameStreamInfWithNoURI(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-VERSION:7\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=1x1,CODECS=\"avc1.640028\"\nv/index.m3u8\n"+
		"#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=94000,RESOLUTION=1280x720\n"),
		"https://cdn.example/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	for _, r := range pl.Renditions {
		if r.Kind == IFrame {
			t.Errorf("an EXT-X-I-FRAME-STREAM-INF with no URI produced a rendition: %+v", r)
		}
	}
}
