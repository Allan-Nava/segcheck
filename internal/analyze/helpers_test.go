package analyze

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// The helpers that turn measurements into the text of a finding. They are small
// enough to look obviously right and they are load-bearing twice over: two of
// them decide whether a check fires at all, and the rest decide whether two
// identical runs render byte-identically, which is what makes segcheck's output
// diffable in an incident doc.

// declaredCodec is the vocabulary bridge between the manifest and the parsers.
// It has to answer with exactly the name the parsers produce, because
// checkTracks compares the two strings directly — a mapping that returns "h265"
// where the parser says "hevc" reports a codec mismatch on every HEVC stream in
// the world.
func TestDeclaredCodec(t *testing.T) {
	tests := []struct {
		name   string
		codecs string
		kind   media.TrackKind
		want   string
		found  bool
	}{
		{"avc1", "avc1.640028", media.Video, "h264", true},
		{"avc3", "avc3.640028", media.Video, "h264", true},
		{"hvc1", "hvc1.2.4.L153.B0", media.Video, "hevc", true},
		{"hev1", "hev1.2.4.L153.B0", media.Video, "hevc", true},
		{"av01", "av01.0.08M.08", media.Video, "av1", true},
		{"vp09", "vp09.00.10.08", media.Video, "vp9", true},
		{"mp4a", "mp4a.40.2", media.Audio, "aac", true},
		{"ac-3", "ac-3", media.Audio, "ac3", true},
		{"ec-3", "ec-3", media.Audio, "eac3", true},
		{"opus", "opus", media.Audio, "opus", true},

		// A muxed variant declares both, and each kind must pick its own.
		{"muxed, video", "avc1.640028,mp4a.40.2", media.Video, "h264", true},
		{"muxed, audio", "avc1.640028,mp4a.40.2", media.Audio, "aac", true},

		// Real manifests carry spaces after the comma and mixed case.
		{"spaces", "avc1.640028, mp4a.40.2", media.Audio, "aac", true},
		{"upper case", "AVC1.640028", media.Video, "h264", true},

		// Asking for a kind the CODECS attribute does not describe is not a
		// defect in the stream: the check has to stay silent rather than compare
		// against a guess.
		{"no audio declared", "avc1.640028", media.Audio, "", false},
		{"no video declared", "mp4a.40.2", media.Video, "", false},
		{"empty", "", media.Video, "", false},

		// A codec this table does not know is a limit of segcheck, not a defect.
		// Returning false keeps the check quiet instead of reporting a mismatch
		// against a codec it never identified.
		{"dolby vision is not mapped", "dvh1.05.01", media.Video, "", false},
		{"unknown fourcc", "zzzz.1.2.3", media.Video, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, found := declaredCodec(tc.codecs, tc.kind)
			if found != tc.found || got != tc.want {
				t.Errorf("declaredCodec(%q, %s) = %q, %v; want %q, %v",
					tc.codecs, tc.kind, got, found, tc.want, tc.found)
			}
		})
	}
}

// Every name declaredCodec can return has to be a name some parser can produce,
// or the comparison in checkTracks can never succeed. This pins the shared
// vocabulary so the two sides cannot drift apart silently.
func TestDeclaredCodec_NamesMatchTheParserVocabulary(t *testing.T) {
	// The names the media parsers emit for the codecs both sides know about.
	parserNames := map[string]bool{
		"h264": true, "hevc": true, "av1": true, "vp9": true,
		"aac": true, "ac3": true, "eac3": true, "opus": true,
	}
	for _, c := range []string{
		"avc1.640028", "avc3.640028", "hvc1.2.4.L153.B0", "hev1.2.4.L153.B0",
		"av01.0.08M.08", "vp09.00.10.08",
		"mp4a.40.2", "ac-3", "ec-3", "opus",
	} {
		for _, kind := range []media.TrackKind{media.Video, media.Audio} {
			if name, ok := declaredCodec(c, kind); ok && !parserNames[name] {
				t.Errorf("declaredCodec(%q, %s) = %q, which no parser ever produces, so the comparison in checkTracks can only ever report a mismatch",
					c, kind, name)
			}
		}
	}
}

// describeCounts renders the message of the "mixed containers" and "track layout
// changes" findings. Its keys come out of a map, and Go randomises map
// iteration, so an implementation that forgot to sort would make two runs over
// the same stream produce different text — and a stream report that does not
// diff cleanly is a stream report nobody trusts.
func TestDescribeCounts_IsSortedAndStableAcrossRuns(t *testing.T) {
	m := map[string]int{"ts": 3, "mp4": 1, "packed-audio": 2}
	const want = "mp4×1, packed-audio×2, ts×3"

	for i := 0; i < 200; i++ {
		if got := describeCounts(m); got != want {
			t.Fatalf("run %d: describeCounts = %q, want %q", i, got, want)
		}
	}
	if got := describeCounts(map[string]int{}); got != "" {
		t.Errorf("describeCounts on nothing = %q, want the empty string", got)
	}
	if got := describeCounts(map[string]int{"1 video + 1 audio": 4}); got != "1 video + 1 audio×4" {
		t.Errorf("describeCounts single = %q", got)
	}
}

func TestSortedStringKeys(t *testing.T) {
	got := sortedStringKeys(map[string]int{"c": 1, "a": 1, "b": 1, "": 1})
	want := []string{"", "a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("sortedStringKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedStringKeys = %v, want %v", got, want)
		}
	}
	if len(sortedStringKeys(nil)) != 0 {
		t.Error("sortedStringKeys(nil) returned something")
	}
}

func TestSortedIntKeys(t *testing.T) {
	got := sortedIntKeys(map[int]string{3: "", 1: "", 2: "", -1: ""})
	want := []int{-1, 1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedIntKeys = %v, want %v", got, want)
		}
	}
}

// trackShape is what the "track layout changes between segments" check compares,
// so two segments with the same tracks must render identically and two with
// different tracks must not.
func TestTrackShape(t *testing.T) {
	tests := []struct {
		name string
		in   media.SegmentInfo
		want string
	}{
		{"muxed", media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video}, {Kind: media.Audio},
		}}, "1 video + 1 audio"},
		// Declared the other way round: the shape must not depend on the order
		// the container happened to list its tracks in, or every segment would
		// look like a layout change.
		{"muxed, reversed", media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Audio}, {Kind: media.Video},
		}}, "1 video + 1 audio"},
		{"video only", media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video}}}, "1 video"},
		{"two audio", media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Audio}, {Kind: media.Audio},
		}}, "2 audio"},
		{"with data", media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video}, {Kind: media.Audio}, {Kind: media.Other},
		}}, "1 video + 1 audio + 1 other"},
		{"nothing", media.SegmentInfo{}, "no track"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := trackShape(tc.in); got != tc.want {
				t.Errorf("trackShape = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDescribeLadder(t *testing.T) {
	tests := []struct {
		name string
		in   []manifest.Rendition
		want string
	}{
		{"heights", []manifest.Rendition{{Height: 1080}, {Height: 720}, {Height: 360}}, "1080p/720p/360p"},
		// A variant with no RESOLUTION falls back to its bandwidth, so the
		// ladder is still describable rather than silently short.
		{"bandwidth fallback", []manifest.Rendition{{Height: 1080}, {Bandwidth: 800000}}, "1080p/800k"},
		{"neither", []manifest.Rendition{{Height: 720}, {}}, "720p"},
		{"empty", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeLadder(tc.in); got != tc.want {
				t.Errorf("describeLadder = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{5 * 1024 * 1024, "5.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{2 * 1024 * 1024 * 1024 * 1024, "2.0 TiB"},
	}
	for _, tc := range tests {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanBitrate(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0 bps"},
		{999, "999 bps"},
		{1000, "1 kbps"},
		{800000, "800 kbps"},
		{1e6, "1.00 Mbps"},
		{5_400_000, "5.40 Mbps"},
	}
	for _, tc := range tests {
		if got := humanBitrate(tc.in); got != tc.want {
			t.Errorf("humanBitrate(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
