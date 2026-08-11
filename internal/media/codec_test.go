package media

import "testing"

// The two codec tables. They are pure lookups, and they are the strings the
// tracks check compares against the manifest's CODECS attribute — so a wrong
// entry does not crash anything, it reports a codec mismatch on media that is
// perfectly fine. The names have to stay the same on both sides of the tool.

func TestStreamKindAndCodec(t *testing.T) {
	tests := []struct {
		streamType byte
		kind       TrackKind
		codec      string
	}{
		{0x01, Video, "mpeg1video"},
		{0x02, Video, "mpeg2video"},
		{0x10, Video, "mpeg4video"},
		{0x1B, Video, "h264"},
		{0x24, Video, "hevc"},
		{0x25, Video, "hevc"},
		{0x33, Video, "vvc"},
		{0xD1, Video, "dirac"},
		{0xEA, Video, "vc1"},
		{0x03, Audio, "mp2"},
		{0x04, Audio, "mp2"},
		{0x0F, Audio, "aac"},
		{0x11, Audio, "aac"},
		{0x1C, Audio, "pcm"},
		{0x81, Audio, "ac3"},
		{0x87, Audio, "eac3"},
		{0x8A, Audio, "eac3"},
	}
	for _, tc := range tests {
		if got := streamKind(tc.streamType); got != tc.kind {
			t.Errorf("streamKind(%#x) = %s, want %s", tc.streamType, got, tc.kind)
		}
		if got := streamCodec(tc.streamType); got != tc.codec {
			t.Errorf("streamCodec(%#x) = %q, want %q", tc.streamType, got, tc.codec)
		}
	}
}

// Every video stream type has to agree between the two functions, because the
// resolution reader is chosen by kind and then dispatched by codec: a type that
// is Video to one and not the other silently loses its resolution.
func TestStreamKind_AgreesWithIsVideoStreamType(t *testing.T) {
	for i := 0; i < 256; i++ {
		st := byte(i)
		if isVideoStreamType(st) != (streamKind(st) == Video) {
			t.Errorf("isVideoStreamType(%#x) disagrees with streamKind", st)
		}
	}
}

// PES private data can be AC-3, subtitles or ID3 depending on a descriptor this
// parser does not read. Naming it would be a guess, and a guessed codec compared
// against the manifest reports a mismatch that is segcheck's fault.
func TestStreamCodec_LeavesAmbiguousTypesUnnamed(t *testing.T) {
	if got := streamCodec(0x06); got != "" {
		t.Errorf("streamCodec(0x06) = %q, want \"\" — PES private data must not be guessed", got)
	}
	if got := streamKind(0x06); got == Video || got == Audio {
		t.Errorf("streamKind(0x06) = %s, want other", got)
	}
}

func TestMP4Codec(t *testing.T) {
	tests := []struct{ sampleEntry, want string }{
		{"avc1", "h264"},
		{"avc3", "h264"},
		{"hvc1", "hevc"},
		{"hev1", "hevc"},
		{"dvh1", "dolbyvision"},
		{"dvhe", "dolbyvision"},
		{"vvc1", "vvc"},
		{"vvi1", "vvc"},
		{"vp08", "vp8"},
		{"vp09", "vp9"},
		{"av01", "av1"},
		{"mp4v", "mpeg4video"},
		{"mp4a", "aac"},
		{"ac-3", "ac3"},
		{"ec-3", "eac3"},
		{"ac-4", "ac4"},
		{"Opus", "opus"},
		{"opus", "opus"},
		{"fLaC", "flac"},
		{"alac", "alac"},
		{"dtsc", "dts"},
		{"dtse", "dts"},
		{"dtsh", "dts"},
		{"dtsl", "dts"},
	}
	for _, tc := range tests {
		if got := mp4Codec(tc.sampleEntry); got != tc.want {
			t.Errorf("mp4Codec(%q) = %q, want %q", tc.sampleEntry, got, tc.want)
		}
	}
}

// A sample entry this table does not know is reported as itself rather than as
// an empty string: an operator reading "reserved" learns more than one reading
// nothing, and the tracks check compares it and finds no match, which is the
// honest outcome.
func TestMP4Codec_UnknownSampleEntryIsPassedThrough(t *testing.T) {
	for _, typ := range []string{"zzzz", "encv", "enca", ""} {
		if got := mp4Codec(typ); got != typ {
			t.Errorf("mp4Codec(%q) = %q, want it passed through unchanged", typ, got)
		}
	}
}

// h264 and hevc are the two names the resolution dispatch in ts.go switches on.
// If either table stopped producing them the dispatch would silently fall to the
// wrong reader, so the exact strings are pinned here.
func TestCodecNamesTheResolutionDispatchDependsOn(t *testing.T) {
	if streamCodec(0x1B) != "h264" {
		t.Error("the H.264 stream type must be named h264")
	}
	if streamCodec(0x24) != "hevc" || streamCodec(0x25) != "hevc" {
		t.Error("both HEVC stream types must be named hevc, or the HEVC resolution reader is never reached")
	}
	if mp4Codec("hvc1") != "hevc" || mp4Codec("hev1") != "hevc" {
		t.Error("fMP4 HEVC sample entries must be named hevc to match the MPEG-TS side")
	}
}
