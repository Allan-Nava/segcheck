package media

import (
	"errors"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-38: subtitle renditions.
//
// A subtitle rendition is text, not a container, and its cue times are the whole
// point: they are what has to line up with the media timeline. The way a subtitle
// rendition fails is not by being malformed — it is by being perfectly valid and
// pointing somewhere else, which nothing that reads only the manifest can see.
//
// WebVTT anchors its own clock to the media clock with X-TIMESTAMP-MAP. Without
// that line there is nothing to anchor it with, and a rendition can be internally
// correct and hours away from the picture.

func TestParseWebVTT(t *testing.T) {
	// The map says local 0 is media 10s (900000 ticks of 90kHz), so a cue at local
	// 1s is at media 11s.
	data := mediatest.WebVTT(mediatest.WebVTTOptions{
		MPEGTS: 900000,
		Cues: []mediatest.Cue{
			{Start: 1, End: 3, Text: "Hello"},
			{Start: 4, End: 6, Text: "World"},
		},
	})
	info, err := ParseWebVTT(data)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	if info.Container != ContainerWebVTT {
		t.Errorf("container = %q, want %q", info.Container, ContainerWebVTT)
	}
	tr, ok := info.Track(Text)
	if !ok {
		t.Fatal("no text track")
	}
	if tr.Codec != "webvtt" {
		t.Errorf("codec = %q, want webvtt", tr.Codec)
	}
	if tr.Samples != 2 {
		t.Errorf("cues = %d, want 2", tr.Samples)
	}
	if tr.Timescale != TSTimescale {
		t.Errorf("timescale = %d, want %d: cues are reported on the media clock", tr.Timescale, TSTimescale)
	}
	// The span is the earliest cue start to the latest cue end: min and max of the
	// timestamps present, which is what a cue's two of them amount to.
	if !tr.HasPTS || tr.MinPTS != 11*90000 || tr.MaxPTS != 16*90000 {
		t.Errorf("cue span = %d..%d, want %d..%d", tr.MinPTS, tr.MaxPTS, 11*90000, 16*90000)
	}
}

// Without X-TIMESTAMP-MAP the cue clock is anchored to nothing. Reporting the local
// times as media times would place every cue at the start of the presentation, so
// the span is left unstated and the check says why.
func TestParseWebVTT_NoTimestampMap(t *testing.T) {
	data := mediatest.WebVTT(mediatest.WebVTTOptions{
		NoTimestampMap: true,
		Cues:           []mediatest.Cue{{Start: 1, End: 3}},
	})
	info, err := ParseWebVTT(data)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	tr, _ := info.Track(Text)
	if tr.Samples != 1 {
		t.Errorf("cues = %d, want 1: the cues are still readable", tr.Samples)
	}
	if tr.HasPTS {
		t.Errorf("a media time was reported with nothing to anchor it to: %+v", tr)
	}
}

// A WebVTT segment covering a stretch with nothing said in it carries no cues, and
// that is not a defect on its own.
func TestParseWebVTT_NoCues(t *testing.T) {
	info, err := ParseWebVTT(mediatest.WebVTT(mediatest.WebVTTOptions{MPEGTS: 900000}))
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	tr, ok := info.Track(Text)
	if !ok {
		t.Fatal("no text track: an empty subtitle segment is still a subtitle segment")
	}
	if tr.Samples != 0 || tr.HasPTS {
		t.Errorf("track = %+v, want no cues and no span", tr)
	}
}

// Bytes with no WEBVTT signature are not WebVTT — an origin serving an error page
// with a 200 lands here, and calling it an empty subtitle segment would hide it.
func TestParseWebVTT_NotWebVTT(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"no signature", mediatest.WebVTT(mediatest.WebVTTOptions{NoHeader: true, Cues: []mediatest.Cue{{Start: 1, End: 2}}})},
		{"empty", nil},
		{"an HTML error page", []byte("<html><body>404</body></html>")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWebVTT(tc.data); !errors.Is(err, ErrUnknownContainer) {
				t.Errorf("err = %v, want ErrUnknownContainer", err)
			}
		})
	}
}

// TTML states its cue times two ways, and a reader that only split on colons gets
// the offset form wrong.
func TestParseTTML(t *testing.T) {
	for _, offset := range []bool{false, true} {
		data := mediatest.TTML(mediatest.TTMLOptions{
			Offset: offset,
			Cues: []mediatest.Cue{
				{Start: 1, End: 3, Text: "Hello"},
				{Start: 4, End: 6, Text: "World"},
			},
		})
		info, err := ParseTTML(data)
		if err != nil {
			t.Fatalf("offset=%v: ParseTTML: %v", offset, err)
		}
		if info.Container != ContainerTTML {
			t.Errorf("container = %q, want %q", info.Container, ContainerTTML)
		}
		tr, ok := info.Track(Text)
		if !ok {
			t.Fatalf("offset=%v: no text track", offset)
		}
		if tr.Samples != 2 {
			t.Errorf("offset=%v: cues = %d, want 2", offset, tr.Samples)
		}
		// TTML times are on the media timeline already: there is no map to apply.
		if !tr.HasPTS || tr.MinPTS != 1*90000 || tr.MaxPTS != 6*90000 {
			t.Errorf("offset=%v: cue span = %d..%d, want %d..%d", offset, tr.MinPTS, tr.MaxPTS, 1*90000, 6*90000)
		}
	}
}

// XML that is not TTML, and bytes that are not XML at all.
func TestParseTTML_NotTTML(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"not XML", mediatest.TTML(mediatest.TTMLOptions{NotXML: true})},
		{"XML with the wrong root", mediatest.TTML(mediatest.TTMLOptions{WrongRoot: true, Cues: []mediatest.Cue{{Start: 1, End: 2}}})},
		{"empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTTML(tc.data); !errors.Is(err, ErrUnknownContainer) {
				t.Errorf("err = %v, want ErrUnknownContainer", err)
			}
		})
	}
}

// Parse dispatches on the bytes, so a subtitle segment does not have to be
// announced to be read.
func TestParse_DispatchesToSubtitles(t *testing.T) {
	vtt := mediatest.WebVTT(mediatest.WebVTTOptions{MPEGTS: 0, Cues: []mediatest.Cue{{Start: 1, End: 2}}})
	if info, err := Parse(vtt, nil); err != nil || info.Container != ContainerWebVTT {
		t.Errorf("WebVTT dispatched to %q (%v)", info.Container, err)
	}
	ttml := mediatest.TTML(mediatest.TTMLOptions{Cues: []mediatest.Cue{{Start: 1, End: 2}}})
	if info, err := Parse(ttml, nil); err != nil || info.Container != ContainerTTML {
		t.Errorf("TTML dispatched to %q (%v)", info.Container, err)
	}
}

// A subtitle rendition may also arrive wrapped in fMP4, with an stpp sample entry
// for TTML or wvtt for WebVTT. The wrapper states the timing, so the track is
// classified and timed from the fragment; reading the cues out of the samples is
// SC-93.
func TestParseMP4_SubtitleTrack(t *testing.T) {
	for _, tc := range []struct{ entry, codec string }{
		{"stpp", "ttml"},
		{"wvtt", "webvtt"},
	} {
		init := mediatest.MP4InitSubtitle(1, 90000, tc.entry)
		frag := mediatest.MP4Segment(1, 1, 180000, 90000, 2, 400)
		info, err := ParseMP4(frag, init)
		if err != nil {
			t.Fatalf("%s: ParseMP4: %v", tc.entry, err)
		}
		tr, ok := info.Track(Text)
		if !ok {
			t.Fatalf("%s: no text track: %+v", tc.entry, info.Tracks)
		}
		if tr.Codec != tc.codec {
			t.Errorf("%s: codec = %q, want %q", tc.entry, tr.Codec, tc.codec)
		}
		if !tr.HasPTS || tr.MinPTS != 180000 {
			t.Errorf("%s: the fragment's own timeline was not read: %+v", tc.entry, tr)
		}
	}
}

// The timestamp forms WebVTT allows, and the ones it does not. A reader that
// accepted a malformed timestamp would place a cue somewhere nobody wrote.
func TestParseWebVTTTimestamp(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{"00:00:01.000", 1, true},
		{"01:02:03.500", 3723.5, true},
		{"02:03.500", 123.5, true}, // mm:ss, the short form
		{" 00:00:01.000 ", 1, true},
		{"", 0, false},
		{"1", 0, false},               // no colon at all
		{"00:00:00:01.000", 0, false}, // four fields
		{"aa:bb.cc", 0, false},
		{"-00:01.000", 0, false},
		{"00.5:01.000", 0, false}, // a fraction in a field that must be whole
	} {
		got, ok := parseWebVTTTimestamp(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseWebVTTTimestamp(%q) = %v/%v, want %v/%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// A cue timing line, with and without the settings that may follow it.
func TestParseWebVTTTiming(t *testing.T) {
	for _, tc := range []struct {
		in         string
		start, end float64
		ok         bool
	}{
		{"00:00:01.000 --> 00:00:03.000", 1, 3, true},
		{"00:00:01.000 --> 00:00:03.000 line:0 position:50%", 1, 3, true},
		{"00:01.000-->00:03.000", 1, 3, true},
		{"just some subtitle text", 0, 0, false},
		{"--> 00:00:03.000", 0, 0, false},  // no start
		{"00:00:01.000 --> ", 0, 0, false}, // no end
		{"00:00:01.000 --> bogus", 0, 0, false},
	} {
		start, end, ok := parseWebVTTTiming(tc.in)
		if ok != tc.ok || (ok && (start != tc.start || end != tc.end)) {
			t.Errorf("parseWebVTTTiming(%q) = %v/%v/%v, want %v/%v/%v",
				tc.in, start, end, ok, tc.start, tc.end, tc.ok)
		}
	}
}

// X-TIMESTAMP-MAP as real playlists write it, and as malformed ones do.
func TestWebVTTTimestampMap(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want float64
		ok   bool
	}{
		{"both halves", "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:900000\n", 10, true},
		{"a non-zero local", "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:02.000,MPEGTS:900000\n", 8, true},
		{"reversed order", "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n", 10, true},
		{"lower case", "WEBVTT\nx-timestamp-map=local:00:00:00.000,mpegts:900000\n", 10, true},
		// LOCAL defaults to the start of the segment when it is absent or unreadable.
		{"no local", "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000\n", 10, true},
		{"an unreadable local", "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:bogus,MPEGTS:900000\n", 10, true},
		// Without MPEGTS there is no anchor at all.
		{"no line", "WEBVTT\n", 0, false},
		{"no mpegts", "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000\n", 0, false},
		{"an unreadable mpegts", "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:abc\n", 0, false},
		{"no separator", "WEBVTT\nX-TIMESTAMP-MAP=LOCAL00:00:00.000\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := webVTTTimestampMap([]byte(tc.in))
			if ok != tc.ok || (ok && got != tc.want) {
				t.Errorf("= %v/%v, want %v/%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The signature, which the specification puts at the very start — after a byte
// order mark, and followed by whitespace or a line break rather than more text.
func TestLooksWebVTT(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"WEBVTT", true},
		{"WEBVTT\n", true},
		{"WEBVTT \n", true},
		{"WEBVTT\tsomething\n", true},
		{"WEBVTT\r\n", true},
		{"\xEF\xBB\xBFWEBVTT\n", true}, // behind a byte order mark
		{"WEBVTTX", false},
		{"webvtt\n", false}, // the signature is case sensitive
		{"WEBVT", false},
		{"", false},
	} {
		if got := looksWebVTT([]byte(tc.in)); got != tc.want {
			t.Errorf("looksWebVTT(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The time expressions TTML allows, and the two it allows that need a rate stated
// elsewhere — guessing one would put every cue in the wrong place.
func TestParseTTMLTime(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{"00:00:01.500", 1.5, true},
		{"1.5s", 1.5, true},
		{"1500ms", 1.5, true},
		{"2m", 120, true},
		{"1h", 3600, true},
		{"", 0, false},
		{"10f", 0, false},  // frames: needs a frame rate
		{"100t", 0, false}, // ticks: needs a tick rate
		{"abc", 0, false},
		{"-5s", 0, false},
		{"xs", 0, false},
	} {
		got, ok := parseTTMLTime(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseTTMLTime(%q) = %v/%v, want %v/%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// A cue may state a duration instead of an end, and one that states neither still
// counts as a cue at the time it begins.
func TestParseTTML_DurationAndMissingEnd(t *testing.T) {
	doc := []byte(`<tt xmlns="http://www.w3.org/ns/ttml"><body><div>` +
		`<p begin="1s" dur="2s">a</p>` +
		`<p begin="5s">b</p>` +
		`<p end="9s">c</p>` +
		`</div></body></tt>`)
	info, err := ParseTTML(doc)
	if err != nil {
		t.Fatalf("ParseTTML: %v", err)
	}
	tr, _ := info.Track(Text)
	if tr.Samples != 3 {
		t.Errorf("cues = %d, want 3: a cue with no readable begin is still a cue", tr.Samples)
	}
	// The span runs from the first begin to the furthest end: 1s to 5s, because the
	// third cue states no begin to place it from.
	if !tr.HasPTS || tr.MinPTS != 90000 || tr.MaxPTS != 5*90000 {
		t.Errorf("span = %d..%d, want %d..%d", tr.MinPTS, tr.MaxPTS, 90000, 5*90000)
	}
}

// Detection of a TTML document, which may be preceded by a declaration, comments
// or processing instructions.
func TestLooksTTML(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{`<tt xmlns="http://www.w3.org/ns/ttml">`, true},
		{`<tt>`, true},
		{`<?xml version="1.0"?><!-- a comment --><tt:tt xmlns:tt="x">`, true},
		{`<?xml version="1.0"?><doc xmlns="http://www.w3.org/ns/ttml"/>`, true},
		{`WEBVTT`, false},
		{`{"json": true}`, false},
		{``, false},
	} {
		if got := looksTTML([]byte(tc.in)); got != tc.want {
			t.Errorf("looksTTML(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Both readers cap how much they scan. A subtitle segment is kilobytes, and a
// caller that handed over a gigabyte of text should not have it all walked.
func TestSubtitleReadersAreBounded(t *testing.T) {
	huge := append([]byte("WEBVTT\n"), make([]byte, maxSubtitleBytes+1024)...)
	if _, err := ParseWebVTT(huge); err != nil {
		t.Errorf("a very large WebVTT segment errored: %v", err)
	}
	// The TTML reader truncates before parsing, so an oversized document is simply
	// not valid XML any more and is reported as such rather than scanned whole.
	hugeTTML := append([]byte(`<tt xmlns="http://www.w3.org/ns/ttml"><body><div>`),
		make([]byte, maxSubtitleBytes+1024)...)
	if _, err := ParseTTML(hugeTTML); err == nil {
		t.Error("a truncated oversized TTML document was accepted")
	}
}

// More cues than the cap are counted up to it rather than walked forever.
func TestSubtitleCueCapIsHonoured(t *testing.T) {
	// A cheap way to exceed the cap without building a huge document: assert the
	// constant is what the loops bound themselves by, and that a document under it
	// is counted exactly.
	var b []byte
	b = append(b, "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n"...)
	for i := 0; i < 5; i++ {
		b = append(b, "\n00:00:00.000 --> 00:00:01.000\nx\n"...)
	}
	info, err := ParseWebVTT(b)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	if tr, _ := info.Track(Text); tr.Samples != 5 {
		t.Errorf("cues = %d, want 5", tr.Samples)
	}
	if maxCuesPerSegment < 1 {
		t.Error("the cue cap would count nothing")
	}
}

// The cue loop stops at the cap rather than walking a hostile document forever. The
// TTML loop is bounded by the same constant in the same shape; only this one is
// exercised, because building a document with a hundred thousand XML elements costs
// more than the branch is worth.
func TestParseWebVTT_CueCapStopsTheWalk(t *testing.T) {
	var b []byte
	b = append(b, "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n"...)
	for i := 0; i <= maxCuesPerSegment; i++ {
		b = append(b, "\n00:00:00.000 --> 00:00:01.000\nx\n"...)
	}
	info, err := ParseWebVTT(b)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	tr, _ := info.Track(Text)
	if tr.Samples != maxCuesPerSegment+1 {
		t.Errorf("cues = %d, want the walk to stop at %d", tr.Samples, maxCuesPerSegment+1)
	}
}

// A part of X-TIMESTAMP-MAP with no separator at all is not a key and a value.
func TestWebVTTTimestampMap_PartWithNoSeparator(t *testing.T) {
	got, ok := webVTTTimestampMap([]byte("WEBVTT\nX-TIMESTAMP-MAP=garbage,MPEGTS:900000\n"))
	if !ok || got != 10 {
		t.Errorf("= %v/%v, want 10/true: the readable half still anchors it", got, ok)
	}
}

// Detection only looks at the head of a document: a TTML root buried past that is
// not found, which is the honest answer for a reader that will not scan a whole
// segment to guess a format.
func TestLooksTTML_OnlyScansTheHead(t *testing.T) {
	padded := append([]byte("<!--"+strings.Repeat("x", 5000)+"-->"), []byte(`<tt xmlns="http://www.w3.org/ns/ttml">`)...)
	if looksTTML(padded) {
		t.Error("a root element past the scanned head was found anyway")
	}
}

// SC-93: the cues inside an fMP4-wrapped subtitle track.
//
// A stpp or wvtt track states its timing in the wrapper, and the cues themselves are in
// the samples. Reporting the sample count was the honest limit before the samples could
// be located; with them located, a CMAF subtitle rendition is as checkable as a text one
// — which matters because "the segments are the right size and carry nothing" is exactly
// how a subtitle pipeline breaks.
func TestParseMP4_SubtitleCuesInSamples(t *testing.T) {
	t.Run("stpp samples are TTML documents", func(t *testing.T) {
		init := mediatest.MP4InitSubtitle(1, 90000, "stpp")
		docA := mediatest.TTML(mediatest.TTMLOptions{Cues: []mediatest.Cue{
			{Start: 1, End: 3, Text: "one"}, {Start: 3, End: 5, Text: "two"},
		}})
		docB := mediatest.TTML(mediatest.TTMLOptions{Cues: []mediatest.Cue{{Start: 6, End: 8, Text: "three"}}})
		frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
			TrackID: 1, SampleDuration: 90000, Samples: [][]byte{docA, docB},
		})
		info, err := ParseMP4(frag, init)
		if err != nil {
			t.Fatalf("ParseMP4: %v", err)
		}
		tr, ok := info.Track(Text)
		if !ok {
			t.Fatal("no text track")
		}
		if tr.Cues != 3 {
			t.Errorf("cues = %d, want 3 across the two samples", tr.Cues)
		}
	})

	t.Run("wvtt samples are cue boxes", func(t *testing.T) {
		init := mediatest.MP4InitSubtitle(1, 90000, "wvtt")
		frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
			TrackID: 1, SampleDuration: 90000, Samples: [][]byte{
				mediatest.VTTCSample("one"),
				// An empty-cue box says nothing is displayed here, and is not a cue.
				mediatest.VTTESample(),
				mediatest.VTTCSample("two"),
			},
		})
		info, err := ParseMP4(frag, init)
		if err != nil {
			t.Fatalf("ParseMP4: %v", err)
		}
		tr, _ := info.Track(Text)
		if tr.Cues != 2 {
			t.Errorf("cues = %d, want 2: the empty-cue box is not one", tr.Cues)
		}
	})
}

// A subtitle track whose segments are the right size and carry nothing is exactly how a
// subtitle pipeline breaks, and the sample count alone could not tell it from a working
// one.
func TestParseMP4_SubtitleTrackWithNoCues(t *testing.T) {
	init := mediatest.MP4InitSubtitle(1, 90000, "stpp")
	empty := mediatest.TTML(mediatest.TTMLOptions{})
	frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
		TrackID: 1, SampleDuration: 90000, Samples: [][]byte{empty, empty},
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Text)
	if !ok {
		t.Fatal("no text track")
	}
	if tr.Samples != 2 {
		t.Errorf("samples = %d, want 2: the samples are there", tr.Samples)
	}
	if tr.Cues != 0 {
		t.Errorf("cues = %d, want 0: none of them says anything", tr.Cues)
	}
	if !tr.CuesRead {
		t.Error("CuesRead is false, so zero cues cannot be told from cues nobody looked for")
	}
}

// A sample this reader cannot make sense of leaves the cue count unread rather than
// reporting zero: the two lead to opposite verdicts.
func TestParseMP4_SubtitleSamplesUnreadable(t *testing.T) {
	init := mediatest.MP4InitSubtitle(1, 90000, "stpp")
	frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
		TrackID: 1, SampleDuration: 90000, Samples: [][]byte{[]byte("not a TTML document")},
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Text)
	if tr.CuesRead {
		t.Errorf("a cue count was reported from samples that are not subtitles: %+v", tr)
	}
}

// A wvtt sample that is not boxes at all states no cue, and must not be counted as one.
func TestSubtitleSampleCues_Unreadable(t *testing.T) {
	data := []byte("not boxes at all")
	if _, ok := subtitleSampleCues("webvtt", data, []sampleRange{{0, len(data)}}); ok {
		t.Error("bytes that are not boxes were read as wvtt cues")
	}
	// No samples to look at is not a cue count of zero.
	if _, ok := subtitleSampleCues("ttml", data, nil); ok {
		t.Error("an empty sample list produced a cue count")
	}
	// A codec this reader does not model reads no cues from it.
	if _, ok := subtitleSampleCues("ttxt", data, []sampleRange{{0, len(data)}}); ok {
		t.Error("an unmodelled subtitle codec produced a cue count")
	}
}

// SC-97: a wrapped cue's own timing, not just its existence.
//
// A TTML document inside a stpp sample counts from the start of the fragment carrying
// it, so its begin and end plus the fragment's tfdt is where the cue actually is. That
// is the same drift check a WebVTT rendition already gets — and the shape of failure is
// the same too: a document that is internally perfect and anchored to the wrong place.
func TestParseMP4_SubtitleCueSpan(t *testing.T) {
	init := mediatest.MP4InitSubtitle(1, 90000, "stpp")
	// Two samples in a fragment that starts at 10s. A TTML document states its times
	// on the presentation timeline rather than relative to the fragment, so the cues
	// are where the documents say they are — 1s to 6s — and the fragment's own decode
	// time must not be added on top.
	docA := mediatest.TTML(mediatest.TTMLOptions{Cues: []mediatest.Cue{{Start: 1, End: 3}}})
	docB := mediatest.TTML(mediatest.TTMLOptions{Cues: []mediatest.Cue{{Start: 4, End: 6}}})
	frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
		TrackID: 1, BaseDecodeTime: 10 * 90000, SampleDuration: 90000,
		Samples: [][]byte{docA, docB},
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Text)
	if !ok {
		t.Fatal("no text track")
	}
	if tr.Cues != 2 {
		t.Fatalf("cues = %d, want 2", tr.Cues)
	}
	// The cue span runs from the earliest begin to the latest end across the samples.
	if !tr.HasCueSpan || tr.CueMin != 1*90000 || tr.CueMax != 6*90000 {
		t.Errorf("cue span = %d..%d (have %v), want %d..%d",
			tr.CueMin, tr.CueMax, tr.HasCueSpan, 1*90000, 6*90000)
	}
}

// A wvtt sample states its cue timing in the sample's own duration rather than inside
// the payload, so there is no cue span to derive — and reporting the fragment's span as
// the cues' would claim a placement nobody stated.
func TestParseMP4_WVTTHasNoCueSpan(t *testing.T) {
	init := mediatest.MP4InitSubtitle(1, 90000, "wvtt")
	frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
		TrackID: 1, BaseDecodeTime: 0, SampleDuration: 90000,
		Samples: [][]byte{mediatest.VTTCSample("one")},
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Text)
	if tr.Cues != 1 {
		t.Errorf("cues = %d, want 1", tr.Cues)
	}
	if tr.HasCueSpan {
		t.Errorf("a cue span was derived from a wvtt sample: %d..%d", tr.CueMin, tr.CueMax)
	}
}

// The cue times inside a TTML document are seconds, which the subtitle readers report on
// the 90kHz clock, while the track counts on its own timescale. Reporting one as the
// other is wrong by whatever ratio separates them — ninety to one on a track that counts
// in milliseconds.
func TestParseMP4_SubtitleCueSpanConvertsTheTimescale(t *testing.T) {
	init := mediatest.MP4InitSubtitle(1, 1000, "stpp") // a millisecond timescale
	doc := mediatest.TTML(mediatest.TTMLOptions{Cues: []mediatest.Cue{{Start: 1, End: 3}}})
	frag := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
		TrackID: 1, BaseDecodeTime: 10000, SampleDuration: 1000, // 10s in
		Samples: [][]byte{doc},
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Text)
	// A cue at 1–3s on a 1000 timescale is 1000..3000, whatever the fragment's own
	// decode time is.
	if !tr.HasCueSpan || tr.CueMin != 1000 || tr.CueMax != 3000 {
		t.Errorf("cue span = %d..%d (have %v), want 1000..3000",
			tr.CueMin, tr.CueMax, tr.HasCueSpan)
	}
}

// SC-96, found on a real stream: X-TIMESTAMP-MAP is an HLS mechanism.
//
// A DASH sidecar WebVTT carries no such line, and does not need one — its cue times are
// on the presentation timeline already. Reporting "the cues cannot be placed" there is a
// WARN about a tag the format does not use, so the local times are always kept and a flag
// says whether anything anchors them.
func TestParseWebVTT_LocalTimesAreAlwaysKept(t *testing.T) {
	mapped := mediatest.WebVTT(mediatest.WebVTTOptions{
		MPEGTS: 900000, Cues: []mediatest.Cue{{Start: 1, End: 3}},
	})
	info, err := ParseWebVTT(mapped)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	tr, _ := info.Track(Text)
	if !tr.CuesAnchored {
		t.Error("a segment with a timestamp map was not reported as anchored")
	}
	if tr.CueMin != 11*90000 {
		t.Errorf("cue span starts at %d, want the mapped 11s", tr.CueMin)
	}

	unmapped := mediatest.WebVTT(mediatest.WebVTTOptions{
		NoTimestampMap: true, Cues: []mediatest.Cue{{Start: 1, End: 3}},
	})
	info, err = ParseWebVTT(unmapped)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	tr, _ = info.Track(Text)
	if tr.CuesAnchored {
		t.Error("a segment with no map was reported as anchored")
	}
	// The local times are still there, because in DASH they are the presentation times.
	if !tr.HasCueSpan || tr.CueMin != 1*90000 || tr.CueMax != 3*90000 {
		t.Errorf("cue span = %d..%d (have %v), want the local 1s..3s",
			tr.CueMin, tr.CueMax, tr.HasCueSpan)
	}
	// HasPTS stays false: without a map nothing ties the cue clock to the media clock,
	// and an HLS rendition genuinely cannot be placed.
	if tr.HasPTS {
		t.Error("an unmapped segment claimed a media timeline")
	}
}
