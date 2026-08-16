package analyze

import (
	"errors"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// SC-17, at the check level. Two questions, and they fail differently.
//
// Against the manifest: `FRAME-RATE` is what a player consults to decide whether
// it can decode a rendition *before* downloading any of it. A 60fps rung declared
// as 30 gets chosen by a device that can only manage 30, and then stutters — and
// the manifest reads perfectly on the way down.
//
// Across the ladder: rungs at different rates make switching visibly uneven. An
// exact integer relation is the deliberate exception — halving the rate on the
// bottom rungs is an ordinary way to save bitrate — so reporting it would flag a
// technique in wide use.

// fpsTrack is a video track whose timestamps run at the given rate.
func fpsTrack(fps float64) media.Track {
	t := videoTrack()
	t.FrameDur = int64(float64(t.Timescale) / fps)
	return t
}

func fpsRend(name string, declared, measured float64) *renditionData {
	rd := rend(name, withSegs(
		okSeg(1, media.ContainerTS, fpsTrack(measured)),
		okSeg(2, media.ContainerTS, fpsTrack(measured)),
	))
	rd.r.Width, rd.r.Height = 1280, 720
	rd.r.FrameRate = declared
	return rd
}

// The defect: the media runs at a rate the manifest does not admit to.
func TestCheckFrameRate_MeasuredDisagreesWithDeclared(t *testing.T) {
	got := checkFrameRate([]*renditionData{fpsRend("720p", 30, 50)}, Defaults())

	f, ok := findIn(got, "framerate", finding.WARN)
	if !ok {
		t.Fatalf("no WARN when the media runs at a different rate:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "50") || !strings.Contains(f.Message, "30") {
		t.Errorf("message = %q, want both the measured and the declared rate", f.Message)
	}
	if f.Hint == "" {
		t.Error("no hint explaining what a wrong FRAME-RATE costs")
	}
	if f.Value == nil || *f.Value < 49 || *f.Value > 51 {
		t.Errorf("Value = %v, want the measured rate so machine consumers need not parse prose", f.Value)
	}
}

// 29.97 declared against 30000/1001 measured is the same rate written two ways,
// and flagging it would fire on a large fraction of the world's content.
func TestCheckFrameRate_NTSCRoundingIsNotADisagreement(t *testing.T) {
	for _, tc := range []struct{ declared, measured float64 }{
		{29.97, 30000.0 / 1001.0},
		{30, 30000.0 / 1001.0},
		{23.976, 24000.0 / 1001.0},
		{59.94, 60000.0 / 1001.0},
		{25, 25},
	} {
		got := checkFrameRate([]*renditionData{fpsRend("720p", tc.declared, tc.measured)}, Defaults())
		for _, f := range got {
			if finding.AtLeast(f.Status, finding.WARN) {
				t.Errorf("declared %v vs measured %v produced %s: %s", tc.declared, tc.measured, f.Status, f.Message)
			}
		}
	}
}

// A manifest that declares no FRAME-RATE at all cannot be disagreed with. The
// measurement is still worth reporting, at OK.
func TestCheckFrameRate_NoDeclaredRateIsNotADefect(t *testing.T) {
	rd := fpsRend("720p", 0, 25)
	got := checkFrameRate([]*renditionData{rd}, Defaults())

	for _, f := range got {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("an undeclared rate produced %s: %s", f.Status, f.Message)
		}
	}
	f, ok := findIn(got, "framerate", finding.OK)
	if !ok {
		t.Fatalf("no finding at all:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "25") {
		t.Errorf("message = %q, want the measured rate reported", f.Message)
	}
}

// ---------- across the ladder ----------

// Rungs at unrelated rates make every switch uneven, and nothing in the manifest
// reveals it when both declare honestly.
func TestCheckFrameRate_LadderWithUnrelatedRates(t *testing.T) {
	got := checkFrameRate([]*renditionData{
		fpsRend("1080p", 0, 25),
		fpsRend("720p", 0, 30),
	}, Defaults())

	var found bool
	for _, f := range got {
		if f.Status == finding.WARN && strings.Contains(f.Message, "ladder") {
			found = true
			if !strings.Contains(f.Message, "25") || !strings.Contains(f.Message, "30") {
				t.Errorf("message = %q, want both rates named", f.Message)
			}
		}
	}
	if !found {
		t.Fatalf("a ladder mixing 25 and 30 was not reported:\n%s", dumpFindings(got))
	}
}

// Halving the rate on the lower rungs is a deliberate, widespread way to save
// bitrate. Reporting it would flag a technique in wide use, so an exact integer
// relation is left alone.
func TestCheckFrameRate_IntegerDivisorLadderIsNotADefect(t *testing.T) {
	for _, rates := range [][]float64{
		{30, 15},
		{60, 30},
		{60, 30, 15},
		{50, 25},
	} {
		var rends []*renditionData
		for i, fps := range rates {
			rends = append(rends, fpsRend(string(rune('a'+i)), 0, fps))
		}
		got := checkFrameRate(rends, Defaults())
		for _, f := range got {
			if finding.AtLeast(f.Status, finding.WARN) {
				t.Errorf("rates %v produced %s: %s", rates, f.Status, f.Message)
			}
		}
	}
}

// One rate throughout is the healthy ladder, and it is reported so the run says
// the check ran rather than staying silent.
func TestCheckFrameRate_ConsistentLadderIsReported(t *testing.T) {
	got := checkFrameRate([]*renditionData{
		fpsRend("1080p", 25, 25),
		fpsRend("720p", 25, 25),
	}, Defaults())

	for _, f := range got {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("a consistent ladder produced %s: %s", f.Status, f.Message)
		}
	}
	if len(got) == 0 {
		t.Error("a consistent ladder produced no finding at all")
	}
}

// ---------- the silences ----------

// Nothing measurable means nothing to say. A rendition that failed to load, one
// whose init is missing, and one whose timestamps give no frame duration must all
// produce nothing above OK.
func TestCheckFrameRate_StaysQuietWhenItCannotMeasure(t *testing.T) {
	failed := rend("720p")
	failed.err = errors.New("media playlist: 500")

	noInit := fpsRend("1080p", 30, 50)
	noInit.initErr = errors.New("init.mp4: 404")

	// Timestamps present but no median gap: a single-sample segment.
	unmeasurable := rend("360p", withSegs(okSeg(1, media.ContainerTS, media.Track{
		ID: 1, Kind: media.Video, Codec: "h264", Timescale: 90000, HasPTS: true, Samples: 1,
	})))
	unmeasurable.r.FrameRate = 30

	got := checkFrameRate([]*renditionData{failed, noInit, unmeasurable, rend("240p")}, Defaults())
	for _, f := range got {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("an unmeasurable rendition produced %s: %s — %s", f.Status, f.Check, f.Message)
		}
	}
}

// Audio renditions have no frame rate, and must not be drawn into the ladder
// comparison either.
func TestCheckFrameRate_IgnoresAudio(t *testing.T) {
	video := fpsRend("720p", 25, 25)
	audio := rend("audio", withSegs(okSeg(1, media.ContainerMP4, audioTrack())))
	audio.r.Kind = manifest.Audio

	got := checkFrameRate([]*renditionData{video, audio}, Defaults())
	for _, f := range got {
		if strings.Contains(f.Target, "audio") {
			t.Errorf("checkFrameRate reported on an audio rendition: %s", f.Message)
		}
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("produced %s: %s", f.Status, f.Message)
		}
	}
}

// A segment carrying no video track has no frame rate to contribute. It must be
// passed over rather than counted, or a rendition with one stray audio-only
// segment reports a rate measured from fewer segments than it claims.
func TestCheckFrameRate_SkipsSegmentsWithNoVideoTrack(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, fpsTrack(25)),
		okSeg(2, media.ContainerTS, audioTrack()),
	))
	rd.r.Width, rd.r.Height = 1280, 720
	rd.r.FrameRate = 25

	got := checkFrameRate([]*renditionData{rd}, Defaults())
	for _, f := range got {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("a segment with no video track produced %s: %s", f.Status, f.Message)
		}
	}
	if _, ok := findIn(got, "framerate", finding.OK); !ok {
		t.Errorf("no finding from the one segment that had video:\n%s", dumpFindings(got))
	}
}

// relatedRate decides which ladders are legitimate, so its contract is pinned
// directly: the top rate itself, and exact integer fractions of it, are fine.
func TestRelatedRate(t *testing.T) {
	const tol = 2.0
	tests := []struct {
		name     string
		top, fps float64
		want     bool
	}{
		{"the top rate itself", 60, 60, true},
		{"half", 60, 30, true},
		{"a quarter", 60, 15, true},
		{"a third", 30, 10, true},
		{"25 under 50", 50, 25, true},
		// NTSC pairs: 59.94 and 29.97 are exactly two to one.
		{"NTSC half", 60000.0 / 1001.0, 30000.0 / 1001.0, true},
		// Unrelated: 30 is not an integer fraction of 25.
		{"30 under 25", 25, 30, false},
		{"25 under 30", 30, 25, false},
		{"24 under 30", 30, 24, false},
		// The guards. Neither is reachable from the check — the top rate is the
		// maximum of positive measurements — but the function is used by value and
		// must not divide by zero or accept a rate above the top.
		{"zero rate", 60, 0, false},
		{"zero top", 0, 30, false},
		{"negative", 60, -30, false},
		{"faster than the top", 30, 60, false},
		// More than twice the top rate: the ratio rounds below one, which is the
		// separate guard. math.Round(0.5) is 1, so 60 against 30 does not reach it.
		{"far above the top", 30, 120, false},
	}
	for _, tc := range tests {
		if got := relatedRate(tc.top, tc.fps, tol); got != tc.want {
			t.Errorf("%s: relatedRate(%v, %v) = %v, want %v", tc.name, tc.top, tc.fps, got, tc.want)
		}
	}
}
