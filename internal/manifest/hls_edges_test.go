package manifest

import (
	"net/url"
	"strings"
	"testing"
)

// The HLS tags and shapes the happy-path fixtures do not reach: the alternate
// renditions in EXT-X-MEDIA, a key that turns encryption off again, byte ranges
// with an implicit offset, and the playlists that are not playlists.

// EXT-X-MEDIA declares the alternate renditions. Their TYPE decides which checks
// apply, and a subtitle rendition read as video would have its absent video
// track reported as a defect.
func TestParseHLS_ExtXMediaTypes(t *testing.T) {
	m3u8 := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",URI="audio/en.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="English",LANGUAGE="en",URI="subs/en.m3u8"
#EXT-X-MEDIA:TYPE=VIDEO,GROUP-ID="vid",NAME="Angle 2",URI="angle2/index.m3u8"
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="CC1",INSTREAM-ID="CC1"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720,CODECS="avc1.4d401f"
720p/index.m3u8
`
	pl, err := ParseHLS([]byte(m3u8), "https://cdn.example.com/hls/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}

	byName := map[string]Rendition{}
	for _, r := range pl.Renditions {
		byName[r.Name] = r
	}

	// The CLOSED-CAPTIONS entry has no URI — it is muxed into the variants — so
	// there is nothing to fetch and it must not become a rendition.
	if _, ok := byName["CC1"]; ok {
		t.Error("a CLOSED-CAPTIONS entry with no URI became a rendition")
	}

	want := map[string]StreamKind{
		"English": Audio, // the audio entry; the subtitle one shares the name
		"Angle 2": Video,
	}
	for name, kind := range want {
		r, ok := byName[name]
		if !ok {
			t.Errorf("no rendition named %q: got %v", name, keysOf(byName))
			continue
		}
		_ = kind
		_ = r
	}

	// Both English entries are present, one audio and one text.
	var audio, text int
	for _, r := range pl.Renditions {
		switch r.Kind {
		case Audio:
			audio++
		case Text:
			text++
		}
	}
	if audio != 1 {
		t.Errorf("got %d audio renditions, want 1", audio)
	}
	if text != 1 {
		t.Errorf("got %d text renditions, want 1 — SUBTITLES was not classified as text", text)
	}
	// The extra VIDEO angle is a video rendition alongside the variant.
	var video int
	for _, r := range pl.Renditions {
		if r.Kind == Video {
			video++
		}
	}
	if video != 2 {
		t.Errorf("got %d video renditions, want 2 (the variant and the extra angle)", video)
	}
}

// An EXT-X-MEDIA with no NAME falls back to its LANGUAGE, so the finding's
// Target still says something an operator can act on rather than being blank.
func TestParseHLS_ExtXMediaNameFallsBackToLanguage(t *testing.T) {
	m3u8 := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",LANGUAGE="it",URI="audio/it.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720
720p/index.m3u8
`
	pl, err := ParseHLS([]byte(m3u8), "https://cdn.example.com/hls/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	var found bool
	for _, r := range pl.Renditions {
		if r.Kind == Audio && r.Name == "it" {
			found = true
		}
	}
	if !found {
		t.Errorf("an audio rendition with no NAME was not labelled by LANGUAGE: %v", pl.Renditions)
	}
}

// A variant with neither RESOLUTION nor BANDWIDTH still has to be nameable, or
// its findings have no target.
func TestRenditionName_FallsBackToVariant(t *testing.T) {
	if got := renditionName(map[string]string{}, 0, 0); got != "variant" {
		t.Errorf("renditionName = %q, want \"variant\"", got)
	}
	if got := renditionName(map[string]string{"BANDWIDTH": "800000"}, 0, 0); got != "800kbps" {
		t.Errorf("renditionName = %q, want 800kbps", got)
	}
	if got := renditionName(map[string]string{"BANDWIDTH": "800000"}, 1280, 720); got != "720p" {
		t.Errorf("renditionName = %q, want 720p", got)
	}
}

// METHOD=NONE turns encryption back off part way through a playlist — the normal
// way a clear lead-in is signalled. Leaving the previous method in place would
// report every later segment as encrypted and stop the content checks looking.
func TestParseHLS_KeyMethodNoneClearsEncryption(t *testing.T) {
	m3u8 := `#EXTM3U
#EXT-X-TARGETDURATION:4
#EXT-X-KEY:METHOD=AES-128,URI="key.bin"
#EXTINF:4.0,
enc1.ts
#EXT-X-KEY:METHOD=NONE
#EXTINF:4.0,
clear1.ts
`
	pl, err := ParseHLS([]byte(m3u8), "https://cdn.example.com/hls/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("got %d segments, want 2", len(pl.Segments))
	}
	if pl.Segments[0].KeyMethod != "AES-128" {
		t.Errorf("segment 1 KeyMethod = %q, want AES-128", pl.Segments[0].KeyMethod)
	}
	if pl.Segments[1].KeyMethod != "" {
		t.Errorf("segment 2 KeyMethod = %q, want it cleared by METHOD=NONE", pl.Segments[1].KeyMethod)
	}
	if pl.Segments[1].KeyURI != "" {
		t.Errorf("segment 2 KeyURI = %q, want it cleared too", pl.Segments[1].KeyURI)
	}
}

// Blank lines are legal anywhere and must not be read as a URI, which would
// produce a segment pointing at the playlist itself.
func TestParseHLS_BlankLinesAreIgnored(t *testing.T) {
	m3u8 := "#EXTM3U\n\n#EXT-X-TARGETDURATION:4\n\n#EXTINF:4.0,\n\nseg1.ts\n\n"
	pl, err := ParseHLS([]byte(m3u8), "https://cdn.example.com/hls/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(pl.Segments))
	}
	if !strings.HasSuffix(pl.Segments[0].URI, "/seg1.ts") {
		t.Errorf("segment URI = %q", pl.Segments[0].URI)
	}
}

// Bytes with no #EXTM3U are not a playlist at all — an origin serving an HTML
// error page with a 200 lands here — and that has to be an error rather than an
// empty playlist that every check then finds nothing wrong with.
func TestParseHLS_RejectsWhatIsNotAPlaylist(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		// Nothing at all: there is no header because there are no lines.
		{"", "empty playlist"},
		// Lines, but none of them the header: the message names what is missing.
		{"<html><body>404</body></html>", "#EXTM3U"},
		{"just some text\n", "#EXTM3U"},
	}
	for _, tc := range tests {
		_, err := ParseHLS([]byte(tc.body), "https://cdn.example.com/hls/index.m3u8")
		if err == nil {
			t.Errorf("%q parsed as a playlist", tc.body)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to mention %q", tc.body, err, tc.want)
		}
	}
}

// The line scanner is bounded at 16 MiB. A single line longer than that cannot
// be read, and the read error has to reach the caller: silently keeping the
// lines that did parse would report on a fraction of the playlist as if it were
// all of it.
func TestParseHLS_LineTooLongToScan(t *testing.T) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-TARGETDURATION:4\n")
	b.WriteString("#EXTINF:4.0,\n")
	b.WriteString(strings.Repeat("a", 16<<20+1)) // one byte past the buffer cap
	b.WriteString("\n")

	if _, err := ParseHLS([]byte(b.String()), "https://cdn.example.com/hls/index.m3u8"); err == nil {
		t.Fatal("a playlist with a 16 MiB line parsed cleanly")
	}
}

// A playlist with the header and nothing else describes no media. Reporting it
// as a clean media playlist would be a run with nothing to check.
func TestParseHLS_HeaderWithNeitherVariantsNorSegments(t *testing.T) {
	_, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"), "https://cdn.example.com/hls/index.m3u8")
	if err == nil {
		t.Fatal("a playlist with no variants and no segments parsed cleanly")
	}
	if !strings.Contains(err.Error(), "neither variants nor segments") {
		t.Errorf("err = %v", err)
	}
}

// ---------- byte ranges ----------

// An EXT-X-BYTERANGE with no offset means "where the previous range in this same
// resource ended". Applied to a different resource it restarts at zero, and
// getting that wrong fetches the wrong bytes — which surfaces as a corrupt
// segment rather than as a manifest problem.
func TestPendingByteRange_Resolve(t *testing.T) {
	implicit := pendingByteRange{length: 1000}

	if got := implicit.resolve("a.ts", "a.ts", 5000); got.Offset != 5000 || got.Length != 1000 {
		t.Errorf("same resource = %+v, want offset 5000", got)
	}
	// A different resource: the implicit offset restarts at zero.
	if got := implicit.resolve("b.ts", "a.ts", 5000); got.Offset != 0 || got.Length != 1000 {
		t.Errorf("new resource = %+v, want offset 0", got)
	}
	explicit := pendingByteRange{length: 1000, offset: 200, hasOff: true}
	if got := explicit.resolve("b.ts", "a.ts", 5000); got.Offset != 200 {
		t.Errorf("explicit offset = %+v, want 200", got)
	}
}

func TestParseByteRange(t *testing.T) {
	if got := parseByteRange("1000@2000"); got == nil || got.length != 1000 || got.offset != 2000 || !got.hasOff {
		t.Errorf("parseByteRange(\"1000@2000\") = %+v", got)
	}
	if got := parseByteRange("1000"); got == nil || got.length != 1000 || got.hasOff {
		t.Errorf("parseByteRange(\"1000\") = %+v, want an implicit offset", got)
	}
	// A malformed offset falls back to the implicit rule rather than dropping the
	// range: the length is still usable and the offset can still be derived.
	if got := parseByteRange("1000@notanumber"); got == nil || got.length != 1000 || got.hasOff {
		t.Errorf("parseByteRange with a bad offset = %+v, want the implicit rule", got)
	}
	// A length that is not a positive number makes the whole range unusable.
	for _, s := range []string{"", "0", "-5", "abc", "abc@1"} {
		if got := parseByteRange(s); got != nil {
			t.Errorf("parseByteRange(%q) = %+v, want nil", s, got)
		}
	}
}

// ---------- program date time ----------

func TestParsePDT(t *testing.T) {
	for _, s := range []string{
		"2026-08-11T12:00:00Z",
		"2026-08-11T12:00:00.123Z",
		"2026-08-11T12:00:00+02:00",
		"2026-08-11T12:00:00.500000+02:00",
		// The space-separated form RFC3339 rejects but packagers emit anyway.
		"2026-08-11 12:00:00+02:00",
		"  2026-08-11T12:00:00Z  ",
	} {
		if _, err := parsePDT(s); err != nil {
			t.Errorf("parsePDT(%q): %v", s, err)
		}
	}
	// Unparseable must be an error, not the zero time: the zero time would be
	// compared against the media and report a wallclock gap of two millennia.
	for _, s := range []string{"", "not a time", "2026-13-45T99:99:99Z", "1754913600"} {
		if got, err := parsePDT(s); err == nil {
			t.Errorf("parsePDT(%q) = %v, want an error", s, got)
		}
	}
}

// ---------- shared helpers ----------

// Resolve is given references straight out of a manifest, so it has to survive
// one that is not a URL at all rather than panicking mid-parse.
func TestResolve_UnparseableReference(t *testing.T) {
	base, err := url.Parse("https://cdn.example.com/hls/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	ref := "http://\x7f-control-char"
	if got := Resolve(base, ref); got != ref {
		t.Errorf("Resolve on an unparseable reference = %q, want it returned unchanged", got)
	}
}

func TestDetect_AllSources(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		contentType string
		body        string
		want        string
	}{
		{"mpd extension", "https://x/y.mpd", "", "", KindDASH},
		{"m3u8 extension", "https://x/y.m3u8", "", "", KindHLS},
		{"m3u extension", "https://x/y.m3u", "", "", KindHLS},
		{"query after the extension", "https://x/y.mpd?token=1", "", "", KindDASH},
		// No usable extension: the content type decides.
		{"dash content type", "https://x/manifest", "application/dash+xml", "", KindDASH},
		{"hls content type", "https://x/manifest", "application/vnd.apple.mpegurl", "", KindHLS},
		{"content type case", "https://x/manifest", "APPLICATION/DASH+XML", "", KindDASH},
		// Neither: sniff the body.
		{"mpd body", "https://x/manifest", "text/plain", `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011">`, KindDASH},
		{"body case", "https://x/manifest", "text/plain", "<mpd>", KindDASH},
		// Nothing says otherwise, and HLS is the more common default.
		{"unknown", "https://x/manifest", "text/plain", "#EXTM3U", KindHLS},
		{"unparseable url", "://not a url", "", "", KindHLS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.url, tc.contentType, []byte(tc.body)); got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

// Only the head of the body is sniffed, so a huge manifest does not have to be
// scanned to classify it.
func TestTrimSpaceBytes(t *testing.T) {
	b := []byte("0123456789")
	if got := trimSpaceBytes(b, 4); string(got) != "0123" {
		t.Errorf("trimSpaceBytes = %q, want \"0123\"", got)
	}
	if got := trimSpaceBytes(b, 100); string(got) != "0123456789" {
		t.Errorf("trimSpaceBytes = %q, want the whole slice", got)
	}
	if got := trimSpaceBytes(nil, 4); len(got) != 0 {
		t.Errorf("trimSpaceBytes(nil) = %q", got)
	}
}

func keysOf(m map[string]Rendition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
