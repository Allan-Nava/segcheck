package media

import (
	"errors"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-21: MP3 packed audio.
//
// HLS delivers an audio rendition as packed MP3 as often as packed AAC. Until now
// segcheck recognised the format and stopped, which was honest but left the duration
// check with nothing to compare against — so a rendition declaring 6s per segment and
// shipping 4 went unreported.
//
// The frame length follows from the version, the layer, the bitrate and the sampling
// rate together. Getting one of the four wrong walks off into the middle of a frame
// and counts a plausible wrong number of them.

func TestParsePackedAudio_MP3(t *testing.T) {
	// 128 kbps at 44.1 kHz, MPEG-1 Layer III: 1152 samples per frame, so 38 frames
	// is a little over a second.
	const frames = 38
	info, err := ParsePackedAudio(mediatest.PackedMP3(90000, mediatest.MP3Default(frames)))
	if err != nil {
		t.Fatalf("ParsePackedAudio: %v", err)
	}
	tr, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.Codec != "mp3" {
		t.Errorf("codec = %q, want mp3", tr.Codec)
	}
	if tr.Samples != frames {
		t.Errorf("frames = %d, want %d", tr.Samples, frames)
	}
	if tr.SampleRate != 44100 {
		t.Errorf("sample rate = %d, want 44100", tr.SampleRate)
	}
	if tr.Channels != 1 {
		t.Errorf("channels = %d, want 1 (the builder writes mono)", tr.Channels)
	}
	// The duration is what the check needs: 38 frames of 1152 samples at 44.1 kHz.
	wantTicks := int64(frames) * 1152 * TSTimescale / 44100
	if tr.StatedDur != wantTicks {
		t.Errorf("duration = %d ticks, want %d", tr.StatedDur, wantTicks)
	}
	if !tr.HasPTS || tr.MinPTS != 90000 {
		t.Errorf("the ID3 timestamp was not read: %+v", tr)
	}
	if d, ok := tr.DurationSec(); !ok || d < 0.9 || d > 1.1 {
		t.Errorf("duration = %vs, want about 1s", d)
	}
}

// The four fields the frame length depends on, each varied on its own. A reader that
// hardcoded any of them walks off into the middle of a frame.
func TestParsePackedAudio_MP3Variants(t *testing.T) {
	for _, tc := range []struct {
		name       string
		opts       mediatest.MP3Options
		rate       int
		samplesPer int
	}{
		{"MPEG-1 Layer III 128k 44.1k", mediatest.MP3Options{Version: 3, Layer: 1, BitrateIndex: 9, RateIndex: 0, Frames: 10}, 44100, 1152},
		{"MPEG-1 Layer III 320k 48k", mediatest.MP3Options{Version: 3, Layer: 1, BitrateIndex: 14, RateIndex: 1, Frames: 10}, 48000, 1152},
		{"MPEG-1 Layer II", mediatest.MP3Options{Version: 3, Layer: 2, BitrateIndex: 10, RateIndex: 0, Frames: 10}, 44100, 1152},
		{"MPEG-1 Layer I", mediatest.MP3Options{Version: 3, Layer: 3, BitrateIndex: 8, RateIndex: 0, Frames: 10}, 44100, 384},
		// MPEG-2 halves the sampling rates and, in Layer III, the samples per frame.
		{"MPEG-2 Layer III 64k 22.05k", mediatest.MP3Options{Version: 2, Layer: 1, BitrateIndex: 8, RateIndex: 0, Frames: 10}, 22050, 576},
		{"MPEG-2.5 Layer III 32k 11.025k", mediatest.MP3Options{Version: 0, Layer: 1, BitrateIndex: 5, RateIndex: 0, Frames: 10}, 11025, 576},
		// MPEG-2 Layer I has a table of its own, and Layer I keeps 384 samples a
		// frame whatever the version.
		{"MPEG-2 Layer I", mediatest.MP3Options{Version: 2, Layer: 3, BitrateIndex: 6, RateIndex: 0, Frames: 10}, 22050, 384},
		// Padding adds a byte per frame, and a reader that ignored it loses sync.
		{"padded", mediatest.MP3Options{Version: 3, Layer: 1, BitrateIndex: 9, RateIndex: 0, Padding: true, Frames: 10}, 44100, 1152},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParsePackedAudio(mediatest.PackedMP3(0, tc.opts))
			if err != nil {
				t.Fatalf("ParsePackedAudio: %v", err)
			}
			tr, _ := info.Track(Audio)
			if tr.Samples != tc.opts.Frames {
				t.Errorf("frames = %d, want %d: the frame length was misread", tr.Samples, tc.opts.Frames)
			}
			if tr.SampleRate != tc.rate {
				t.Errorf("sample rate = %d, want %d", tr.SampleRate, tc.rate)
			}
			want := int64(tc.opts.Frames) * int64(tc.samplesPer) * TSTimescale / int64(tc.rate)
			if tr.StatedDur != want {
				t.Errorf("duration = %d ticks, want %d", tr.StatedDur, want)
			}
		})
	}
}

// A frame header stating a reserved bitrate index, a reserved sampling rate or a
// reserved layer is not a frame this reader can measure. It stays an unsupported
// container rather than reporting a duration of zero, which the duration check would
// take for a stream eight seconds short.
func TestParsePackedAudio_MP3Unmeasurable(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts mediatest.MP3Options
	}{
		{"a free-format bitrate index", mediatest.MP3Options{Version: 3, Layer: 1, BitrateIndex: 0, RateIndex: 0, Frames: 4}},
		{"a reserved bitrate index", mediatest.MP3Options{Version: 3, Layer: 1, BitrateIndex: 15, RateIndex: 0, Frames: 4}},
		{"a reserved sampling rate", mediatest.MP3Options{Version: 3, Layer: 1, BitrateIndex: 9, RateIndex: 3, Frames: 4}},
		{"a reserved layer", mediatest.MP3Options{Version: 3, Layer: 0, BitrateIndex: 9, RateIndex: 0, Frames: 4}},
		{"a reserved version", mediatest.MP3Options{Version: 1, Layer: 1, BitrateIndex: 9, RateIndex: 0, Frames: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePackedAudio(mediatest.PackedMP3(0, tc.opts))
			if !errors.Is(err, ErrUnsupportedContainer) && !errors.Is(err, ErrUnknownContainer) {
				t.Errorf("err = %v, want an honest refusal to measure", err)
			}
		})
	}
}

// One frame, which is what MP3Frames writes when asked for none.
func TestMP3FrameBuilderDefaultsToOne(t *testing.T) {
	one := mediatest.MP3Frame(mediatest.MP3Options{Version: 3, Layer: 1, BitrateIndex: 9, RateIndex: 0})
	if len(one) != 417 {
		t.Errorf("a single 128k/44.1k frame is %d bytes, want 417", len(one))
	}
}

// The frame header reader on its own, at each thing it refuses. ParsePackedAudio
// rejects some of these earlier through its own sniffing, so they are asserted here
// where the refusal actually lives.
func TestParseMP3Frame_Refusals(t *testing.T) {
	// A well-formed MPEG-1 Layer III frame, for contrast: 128 kbps at 44.1 kHz.
	good := []byte{0xFF, 0xFB, 0x90, 0xC0}
	f, ok := parseMP3Frame(good)
	if !ok || f.size != 417 || f.samples != 1152 || f.sampleRate != 44100 || f.channels != 1 {
		t.Fatalf("a well-formed frame gave %+v/%v", f, ok)
	}

	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"too short", good[:3]},
		{"no sync word", []byte{0x00, 0x00, 0x00, 0x00}},
		{"half a sync word", []byte{0xFF, 0x0B, 0x90, 0xC0}},
		// Version 1 and layer 0 are both reserved, and a length computed from either
		// walks into the next frame.
		{"a reserved version", []byte{0xFF, 0xEB, 0x90, 0xC0}}, // version bits 01
		{"a reserved layer", []byte{0xFF, 0xF9, 0x90, 0xC0}},
		{"a free-format bitrate", []byte{0xFF, 0xFB, 0x00, 0xC0}},
		{"a reserved bitrate", []byte{0xFF, 0xFB, 0xF0, 0xC0}},
		{"a reserved sampling rate", []byte{0xFF, 0xFB, 0x9C, 0xC0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if f, ok := parseMP3Frame(tc.b); ok {
				t.Errorf("accepted %+v", f)
			}
		})
	}

	// Stereo, so the mode bits are read rather than assumed.
	if f, ok := parseMP3Frame([]byte{0xFF, 0xFB, 0x90, 0x00}); !ok || f.channels != 2 {
		t.Errorf("a stereo frame gave %+v/%v", f, ok)
	}
	// Bytes that are not a frame at all yield no scan.
	if _, _, _, _, ok := scanMP3([]byte{0x00, 0x01, 0x02, 0x03}); ok {
		t.Error("scanMP3 accepted bytes that are not frames")
	}
}
