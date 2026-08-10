package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

func TestParseMP4_InitPlusFragment(t *testing.T) {
	const (
		trackID   = uint32(1)
		timescale = uint32(90000)
		baseTime  = int64(180000) // the segment starts two seconds in
		sampleDur = uint32(3600)
		samples   = 50
	)
	init := mediatest.MP4Init(trackID, timescale, "video", 1920, 1080)
	seg := mediatest.MP4Segment(trackID, 7, baseTime, sampleDur, samples, 4096)

	info, err := ParseMP4(seg, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if info.Container != ContainerMP4 {
		t.Errorf("container = %q, want %q", info.Container, ContainerMP4)
	}
	if info.Sequence != 7 {
		t.Errorf("sequence = %d, want 7", info.Sequence)
	}

	track, ok := info.Track(Video)
	if !ok {
		t.Fatalf("no video track in %+v", info.Tracks)
	}
	if track.Codec != "h264" {
		t.Errorf("codec = %q, want h264", track.Codec)
	}
	if track.Width != 1920 || track.Height != 1080 {
		t.Errorf("resolution = %dx%d, want 1920x1080", track.Width, track.Height)
	}
	if track.Timescale != timescale {
		t.Errorf("timescale = %d, want %d", track.Timescale, timescale)
	}
	if track.Samples != samples {
		t.Errorf("samples = %d, want %d", track.Samples, samples)
	}

	// The duration is stated by the container, so it is exact rather than
	// derived from the timestamp span.
	dur, ok := track.DurationSec()
	if !ok {
		t.Fatal("DurationSec not available")
	}
	if dur != 2.0 {
		t.Errorf("DurationSec = %v, want 2", dur)
	}
	start, ok := track.StartSec()
	if !ok {
		t.Fatal("StartSec not available")
	}
	if start != 2.0 {
		t.Errorf("StartSec = %v, want 2 (tfdt %d at timescale %d)", start, baseTime, timescale)
	}
}

func TestParseMP4_AudioInit(t *testing.T) {
	init := mediatest.MP4Init(2, 48000, "audio", 0, 0)
	seg := mediatest.MP4Segment(2, 1, 0, 1024, 47, 2048)

	info, err := ParseMP4(seg, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	track, ok := info.Track(Audio)
	if !ok {
		t.Fatalf("no audio track in %+v", info.Tracks)
	}
	if track.Codec != "aac" {
		t.Errorf("codec = %q, want aac", track.Codec)
	}
	if track.Timescale != 48000 {
		t.Errorf("timescale = %d, want 48000", track.Timescale)
	}
}

func TestParseMP4_WithoutInitStillHasTimeline(t *testing.T) {
	// A media fragment on its own yields timestamps but no timescale, so no
	// duration in seconds: the analyses must degrade rather than invent one.
	seg := mediatest.MP4Segment(1, 3, 90000, 3600, 25, 512)
	info, err := ParseMP4(seg, nil)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(info.Tracks))
	}
	track := info.Tracks[0]
	if track.MinPTS != 90000 {
		t.Errorf("MinPTS = %d, want 90000", track.MinPTS)
	}
	if _, ok := track.DurationSec(); ok {
		t.Error("DurationSec reported a value without a timescale")
	}
}

func TestParse_DetectsContainer(t *testing.T) {
	tsData := mediatest.TS(0, 3600, 10)
	if info, err := Parse(tsData, nil); err != nil || info.Container != ContainerTS {
		t.Errorf("Parse(ts) = %q, %v; want %q, nil", info.Container, err, ContainerTS)
	}
	mp4Data := mediatest.MP4Segment(1, 1, 0, 3600, 10, 256)
	init := mediatest.MP4Init(1, 90000, "video", 640, 360)
	if info, err := Parse(mp4Data, init); err != nil || info.Container != ContainerMP4 {
		t.Errorf("Parse(mp4) = %q, %v; want %q, nil", info.Container, err, ContainerMP4)
	}
}

func TestParse_RejectsErrorPage(t *testing.T) {
	body := []byte(`<!DOCTYPE html><html><head><title>403 Forbidden</title></head></html>`)
	if _, err := Parse(body, nil); err == nil {
		t.Fatal("an HTML error page was accepted as media")
	}
}

func TestBoxesIn_StopsOnImpossibleSize(t *testing.T) {
	// A box claiming to be larger than the data must end the walk instead of
	// slicing out of range.
	data := []byte{0xFF, 0xFF, 0xFF, 0xFF, 'm', 'o', 'o', 'f'}
	if got := boxesIn(data); len(got) != 0 {
		t.Errorf("boxesIn returned %d boxes for an oversized declaration, want 0", len(got))
	}
}
