package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// Fuzz targets for the parsers (SC-35).
//
// These parsers eat bytes from the open internet. A segment can be truncated by a
// proxy, served as an HTML error page with a 200, or simply be a file the packager
// wrote wrongly — and a panic on any of that is a crash in someone's CI run, in a
// tool whose whole contract is "exit 0 whenever the check ran".
//
// The seed corpus is built here with mediatest rather than checked in, because no
// binary fixture enters this repository: the builders already know how to produce
// a well-formed segment of each kind, and mutating those is exactly where a fuzzer
// should start. `go test` runs the seeds on every build, so these double as a
// regression suite without anyone opting into fuzzing.
//
// Two properties are asserted throughout:
//
//   - the parser returns rather than panicking, whatever the bytes are;
//   - when it claims success, what it returns is self-consistent. A parser that
//     survives by reporting a 60000x1 frame or a negative offset has not survived,
//     it has moved the failure downstream into a finding about media that never
//     said any such thing.

// sane asserts the invariants every parsed segment has to satisfy, whatever the
// input was.
func sane(t *testing.T, info SegmentInfo) {
	t.Helper()
	for _, tr := range info.Tracks {
		if tr.Width < 0 || tr.Height < 0 || tr.Width > 16384 || tr.Height > 16384 {
			t.Fatalf("implausible resolution %dx%d survived parsing", tr.Width, tr.Height)
		}
		if tr.Samples < 0 {
			t.Fatalf("negative sample count %d", tr.Samples)
		}
		if tr.StatedDur < 0 {
			t.Fatalf("negative stated duration %d", tr.StatedDur)
		}
		// The measurement protocol: a duration is either measurable and positive,
		// or not measurable at all. Zero reported as a measurement is what makes a
		// duration check say the media is 100% shorter than declared.
		if d, ok := tr.DurationSec(); ok && d <= 0 {
			t.Fatalf("DurationSec reported %v as a measurement", d)
		}
		if fps, ok := tr.FrameRateFPS(); ok && (fps <= 0 || fps > 10000) {
			t.Fatalf("FrameRateFPS reported %v as a measurement", fps)
		}
	}
}

func FuzzParseTS(f *testing.F) {
	f.Add(mediatest.TS(0, 3600, 5))
	f.Add(mediatest.TSWithSPS(0, 3600, 5, mediatest.SPSFor(1280, 720)))
	f.Add(mediatest.TSWithHEVCSPS(0, 3600, 5, mediatest.HEVCSPSFor(1280, 720)))
	f.Add(mediatest.TSDropPacket(0, 3600, 5))
	f.Add([]byte("<html><body>404 Not Found</body></html>"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := ParseTS(data)
		if err != nil {
			return
		}
		sane(t, info)
	})
}

func FuzzParseMP4(f *testing.F) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)
	f.Add(mediatest.MP4Segment(1, 1, 0, 3600, 25, 1000), init)
	f.Add(mediatest.MP4SegmentSync(1, 1, 0, 3600, 25, 1000, true), init)
	f.Add(mediatest.MP4SegmentNoDurations(1, 1, 0, 25, 1000),
		mediatest.MP4InitTrex(1, 90000, 1280, 720, 3600, 0))
	f.Add(init, []byte(nil))
	f.Add([]byte("not an mp4 at all"), []byte(nil))

	f.Fuzz(func(t *testing.T, data, init []byte) {
		info, err := ParseMP4(data, init)
		if err != nil {
			return
		}
		sane(t, info)
	})
}

func FuzzParsePackedAudio(f *testing.F) {
	f.Add(mediatest.PackedAudio(90000, 10))
	f.Add(mediatest.PackedAudioNoID3(10))
	f.Add([]byte{0xFF, 0xF1, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Add([]byte("ID3"))

	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := ParsePackedAudio(data)
		if err != nil {
			return
		}
		sane(t, info)
	})
}

// The bitstream readers are fuzzed apart from the containers, because a
// parameter set is where an off-by-one in a bit reader turns into a confident
// wrong resolution rather than a failure.
func FuzzParseSPS(f *testing.F) {
	f.Add(mediatest.SPSFor(1920, 1080))
	f.Add(mediatest.SPSFor(256, 144))
	f.Add(mediatest.HEVCSPSFor(3840, 2160))
	f.Add([]byte{0x67})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Both readers, on the same bytes: one of them is being handed a stream of
		// the wrong codec, which is the case that must fail rather than answer.
		if w, h, ok := parseH264SPS(data); ok {
			if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
				t.Fatalf("H.264 reader returned %dx%d as a success", w, h)
			}
		}
		if w, h, ok := parseHEVCSPS(data); ok {
			if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
				t.Fatalf("HEVC reader returned %dx%d as a success", w, h)
			}
		}
		// And the Annex-B walks over the same bytes.
		h264Keyframes(data)
		hevcKeyframes(data)
	})
}

func FuzzParseSIDX(f *testing.F) {
	entries := []mediatest.SIDXEntry{
		{Size: 1000, Duration: 90000, StartsWithSAP: true},
		{Size: 1200, Duration: 45000, StartsWithSAP: true},
	}
	f.Add(mediatest.SIDX(0, 90000, 0, 0, entries), int64(0))
	f.Add(mediatest.SIDX(1, 90000, 0, 64, entries), int64(5000))
	f.Add(mediatest.HierarchicalSIDX(90000, [][]mediatest.SIDXEntry{entries}), int64(0))
	f.Add([]byte("sidx"), int64(0))
	f.Add([]byte{}, int64(0))

	f.Fuzz(func(t *testing.T, data []byte, offset int64) {
		// A wild offset is the caller's business, not the parser's; bound it so the
		// fuzzer explores the box rather than integer overflow in the test.
		if offset < 0 || offset > 1<<40 {
			return
		}
		idx, err := ParseSIDX(data, offset)
		if err != nil {
			return
		}
		for _, e := range idx.Entries {
			if e.Size < 0 {
				t.Fatalf("negative subsegment size %d", e.Size)
			}
			if e.Offset < 0 {
				t.Fatalf("negative subsegment offset %d", e.Offset)
			}
		}
		// Resolution follows the same tree; it must not panic either.
		if _, err := ResolveSIDX(data, offset); err != nil {
			return
		}
	})
}

// Parse is the entry point the analysis actually calls, so it gets a target of
// its own: the container detection in front of the parsers is itself a place to
// hand the wrong reader some bytes.
func FuzzParse(f *testing.F) {
	f.Add(mediatest.TS(0, 3600, 5), []byte(nil))
	f.Add(mediatest.MP4Segment(1, 1, 0, 3600, 25, 1000),
		mediatest.MP4Init(1, 90000, "video", 1280, 720))
	f.Add(mediatest.PackedAudio(90000, 5), []byte(nil))
	f.Add([]byte("#EXTM3U\n"), []byte(nil))

	f.Fuzz(func(t *testing.T, data, init []byte) {
		info, err := Parse(data, init)
		if err != nil {
			return
		}
		sane(t, info)
	})
}

// A subtitle segment is text off the network, which makes it the most directly
// attacker-shaped input any of these readers takes: no length prefixes, no sync
// bytes, just bytes an origin chose.
func FuzzParseWebVTT(f *testing.F) {
	f.Add(mediatest.WebVTT(mediatest.WebVTTOptions{
		MPEGTS: 900000,
		Cues:   []mediatest.Cue{{Start: 1, End: 3, Text: "Hello"}},
	}))
	f.Add(mediatest.WebVTT(mediatest.WebVTTOptions{NoTimestampMap: true, Cues: []mediatest.Cue{{Start: 0, End: 1}}}))
	f.Add([]byte("WEBVTT\n"))
	f.Add([]byte("WEBVTT\nX-TIMESTAMP-MAP=LOCAL:,MPEGTS:\n"))
	f.Add([]byte("WEBVTT\n\n-->\n"))
	f.Add([]byte("WEBVTT\n\n00:00:00.000 --> 99:99:99.999 line:0\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := ParseWebVTT(data)
		if err != nil {
			return
		}
		sane(t, info)
	})
}

func FuzzParseTTML(f *testing.F) {
	f.Add(mediatest.TTML(mediatest.TTMLOptions{Cues: []mediatest.Cue{{Start: 1, End: 3}}}))
	f.Add(mediatest.TTML(mediatest.TTMLOptions{Offset: true, Cues: []mediatest.Cue{{Start: 1, End: 3}}}))
	f.Add([]byte(`<tt/>`))
	f.Add([]byte(`<tt><body><div><p begin="1f" end="2t"/></div></body></tt>`))
	f.Add([]byte(`<tt><body><div><p begin="" end="" dur="5s"/></div></body></tt>`))

	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := ParseTTML(data)
		if err != nil {
			return
		}
		sane(t, info)
	})
}
