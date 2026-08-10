package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

func TestParseTS_TimestampsAndDuration(t *testing.T) {
	// 50 frames at 25fps on the 90kHz clock: exactly 2 seconds of media.
	const (
		startPTS = int64(126000)
		frameDur = int64(3600)
		frames   = 50
	)
	data := mediatest.TS(startPTS, frameDur, frames)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if info.Container != ContainerTS {
		t.Errorf("container = %q, want %q", info.Container, ContainerTS)
	}

	track, ok := info.Track(Video)
	if !ok {
		t.Fatalf("no video track in %+v", info.Tracks)
	}
	if track.Codec != "h264" {
		t.Errorf("codec = %q, want h264", track.Codec)
	}
	if track.Timescale != TSTimescale {
		t.Errorf("timescale = %d, want %d", track.Timescale, TSTimescale)
	}
	if track.Samples != frames {
		t.Errorf("samples = %d, want %d", track.Samples, frames)
	}
	if track.MinPTS != startPTS {
		t.Errorf("MinPTS = %d, want %d", track.MinPTS, startPTS)
	}
	if want := startPTS + int64(frames-1)*frameDur; track.MaxPTS != want {
		t.Errorf("MaxPTS = %d, want %d", track.MaxPTS, want)
	}
	if track.FrameDur != frameDur {
		t.Errorf("FrameDur = %d, want %d", track.FrameDur, frameDur)
	}

	// The PTS span covers 49 frames; the duration must add the last frame back.
	got, ok := track.DurationSec()
	if !ok {
		t.Fatal("DurationSec not available")
	}
	if want := 2.0; got != want {
		t.Errorf("DurationSec = %v, want %v (span alone would be %v)", got, want, float64(frames-1)*float64(frameDur)/TSTimescale)
	}

	if start, _ := track.StartSec(); start != float64(startPTS)/TSTimescale {
		t.Errorf("StartSec = %v, want %v", start, float64(startPTS)/TSTimescale)
	}
	if track.CCErrors != 0 {
		t.Errorf("CCErrors = %d on a clean segment, want 0", track.CCErrors)
	}
}

func TestParseTS_ContinuityCounterBreak(t *testing.T) {
	info, err := ParseTS(mediatest.TSDropPacket(0, 3600, 10))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	track, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	if track.CCErrors != 1 {
		t.Errorf("CCErrors = %d, want 1 (one skipped counter value)", track.CCErrors)
	}
}

func TestParseTS_ResolutionFromSPS(t *testing.T) {
	info, err := ParseTS(mediatest.TSWithSPS(0, 3600, 5, mediatest.SPSFor(1920, 1080)))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	track, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	if track.Width != 1920 || track.Height != 1080 {
		t.Errorf("resolution = %dx%d, want 1920x1080", track.Width, track.Height)
	}
}

func TestParseTS_RejectsNonTS(t *testing.T) {
	if _, err := ParseTS([]byte("<html><body>404 Not Found</body></html>")); err == nil {
		t.Fatal("an HTML error page parsed as MPEG-TS")
	}
}

func TestParseTS_SurvivesLeadingGarbage(t *testing.T) {
	data := append([]byte{0x00, 0x11, 0x22}, mediatest.TS(0, 3600, 10)...)
	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS with a 3-byte prefix: %v", err)
	}
	if _, ok := info.Track(Video); !ok {
		t.Error("video track lost when the segment had a stray prefix")
	}
}

func TestUnwrapDelta(t *testing.T) {
	tests := []struct {
		name string
		a, b int64
		want int64
	}{
		{"forward", 1000, 4600, 3600},
		{"backward", 4600, 1000, -3600},
		{"wrap forward", PTSModulus - 1000, 2600, 3600},
		{"wrap backward", 2600, PTSModulus - 1000, -3600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnwrapDelta(tc.a, tc.b); got != tc.want {
				t.Errorf("UnwrapDelta(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestMedianDelta_IgnoresOneOutlier(t *testing.T) {
	// A single large jump must not move the median: that is why the frame
	// duration is a median and not a mean.
	pts := []int64{0, 3600, 7200, 10800, 900000}
	if got := medianDelta(pts); got != 3600 {
		t.Errorf("medianDelta = %d, want 3600", got)
	}
}
