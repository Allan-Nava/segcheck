package media

import (
	"errors"
	"math"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

func TestParsePackedAudio_TimestampAndDuration(t *testing.T) {
	const (
		startPTS = int64(900000) // 10s on the 90kHz clock
		frames   = 94
	)
	info, err := ParsePackedAudio(mediatest.PackedAudio(startPTS, frames))
	if err != nil {
		t.Fatalf("ParsePackedAudio: %v", err)
	}
	if info.Container != ContainerPackedAudio {
		t.Errorf("container = %q, want %q", info.Container, ContainerPackedAudio)
	}

	track, ok := info.Track(Audio)
	if !ok {
		t.Fatalf("no audio track in %+v", info.Tracks)
	}
	if track.Codec != "aac" {
		t.Errorf("codec = %q, want aac", track.Codec)
	}
	if track.Samples != frames {
		t.Errorf("frames = %d, want %d", track.Samples, frames)
	}
	// Packed audio has no PTS of its own: the ID3 PRIV tag is the only place the
	// timeline exists, and without reading it continuity cannot be checked.
	if !track.HasPTS || track.MinPTS != startPTS {
		t.Errorf("MinPTS = %d (has=%v), want %d from the ID3 tag", track.MinPTS, track.HasPTS, startPTS)
	}
	if info.Channels != 2 {
		t.Errorf("channels = %d, want 2", info.Channels)
	}

	dur, ok := track.DurationSec()
	if !ok {
		t.Fatal("DurationSec not available")
	}
	want := float64(frames) * float64(mediatest.ADTSTicksPerFrame) / 90000
	if math.Abs(dur-want) > 1e-9 {
		t.Errorf("DurationSec = %v, want %v", dur, want)
	}
	if start, _ := track.StartSec(); start != 10 {
		t.Errorf("StartSec = %v, want 10", start)
	}
}

func TestParsePackedAudio_WithoutID3HasNoTimeline(t *testing.T) {
	info, err := ParsePackedAudio(mediatest.PackedAudioNoID3(20))
	if err != nil {
		t.Fatalf("ParsePackedAudio: %v", err)
	}
	track, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if track.HasPTS {
		t.Error("HasPTS is true without an ID3 timestamp: the timeline was invented")
	}
	// The duration is still known, since it comes from counting frames.
	if _, ok := track.DurationSec(); !ok {
		t.Error("DurationSec unavailable although the frames were counted")
	}
}

func TestParsePackedAudio_ToleratesTruncatedFinalFrame(t *testing.T) {
	data := mediatest.PackedAudio(0, 10)
	truncated := data[:len(data)-40] // cut the last frame in half

	info, err := ParsePackedAudio(truncated)
	if err != nil {
		t.Fatalf("ParsePackedAudio on a truncated tail: %v", err)
	}
	track, _ := info.Track(Audio)
	if track.Samples != 9 {
		t.Errorf("frames = %d, want 9 (the partial frame dropped)", track.Samples)
	}
}

func TestParsePackedAudio_MP3IsRecognisedNotRejected(t *testing.T) {
	// An MPEG-1 layer III frame header. segcheck cannot measure it yet, but
	// calling it an unknown container would report a defect in a healthy stream.
	mp3 := []byte{0xFF, 0xFB, 0x90, 0x00}
	mp3 = append(mp3, make([]byte, 400)...)

	_, err := ParsePackedAudio(mp3)
	if !errors.Is(err, ErrUnsupportedContainer) {
		t.Fatalf("error = %v, want ErrUnsupportedContainer", err)
	}
	if errors.Is(err, ErrUnknownContainer) {
		t.Error("MP3 was classified as an unknown container")
	}
}

func TestParse_DetectsPackedAudio(t *testing.T) {
	info, err := Parse(mediatest.PackedAudio(0, 5), nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if info.Container != ContainerPackedAudio {
		t.Errorf("container = %q, want %q", info.Container, ContainerPackedAudio)
	}
}

func TestParsePackedAudio_RejectsGarbage(t *testing.T) {
	if _, err := ParsePackedAudio([]byte("not audio at all, just text")); err == nil {
		t.Fatal("plain text was accepted as packed audio")
	}
}

func TestParseID3_TruncatedTagIsIgnored(t *testing.T) {
	tag := mediatest.ID3Timestamp(12345)
	// Declare a size larger than what is present.
	n, _, ok := parseID3(tag[:len(tag)-5])
	if n != 0 || ok {
		t.Errorf("parseID3 on a truncated tag = (%d, ok=%v), want (0, false)", n, ok)
	}
}

func TestSyncsafe(t *testing.T) {
	// Seven bits per byte: 0x00 0x00 0x02 0x01 is 257, not 513.
	if got := syncsafe([]byte{0x00, 0x00, 0x02, 0x01}); got != 257 {
		t.Errorf("syncsafe = %d, want 257", got)
	}
}
