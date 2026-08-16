package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// `trex` in the initialisation segment states the default sample duration, size
// and flags for a track. A fragment may state none of them itself — no per-sample
// durations, no tfhd defaults — and rely entirely on it, which is how a large
// share of real on-demand DASH is packaged: Sony's DASH-IF test vector carries
// default_sample_duration=1001 in trex and nothing in its fragments.
//
// Ignoring trex does not fail loudly. Every sample duration becomes zero, so the
// segment's stated duration is zero, so `duration` reports the media as 100%
// shorter than declared and `continuity` reports a gap before every segment —
// against a stream that is perfectly correct.

func TestParseMP4_TrexSuppliesTheSampleDuration(t *testing.T) {
	const timescale = 24000
	const defaultDur = 1001 // 23.976fps, exactly what the real file uses

	init := mediatest.MP4InitTrex(1, timescale, 854, 480, defaultDur, 0)
	frag := mediatest.MP4SegmentNoDurations(1, 1, 0, 120, 4000)

	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	if tr.Samples != 120 {
		t.Fatalf("samples = %d, want 120", tr.Samples)
	}

	want := int64(120 * defaultDur)
	if tr.StatedDur != want {
		t.Errorf("stated duration = %d, want %d ticks from the trex default", tr.StatedDur, want)
	}
	dur, ok := tr.DurationSec()
	if !ok {
		t.Fatal("no duration measured: the fragment states none and trex was not read")
	}
	if wantSec := float64(want) / timescale; dur != wantSec {
		t.Errorf("duration = %vs, want %vs", dur, wantSec)
	}
	// And the interval end follows from it, which is what continuity compares.
	if tr.MaxPTS <= tr.MinPTS {
		t.Errorf("MaxPTS %d is not past MinPTS %d: every segment would look like a gap", tr.MaxPTS, tr.MinPTS)
	}
}

// What the fragment states wins over the trex default: trex is the fallback, not
// an override.
func TestParseMP4_FragmentDurationsBeatTheTrexDefault(t *testing.T) {
	init := mediatest.MP4InitTrex(1, 90000, 1280, 720, 9999, 0)
	// This fragment states 3600 per sample in its tfhd.
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000)

	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	if tr.StatedDur != 50*3600 {
		t.Errorf("stated duration = %d, want %d — the trex default overrode the fragment", tr.StatedDur, 50*3600)
	}
}

// An init segment with no mvex at all is the common CMAF case, and it must not
// start inventing durations.
func TestParseMP4_NoTrexIsNotADuration(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)
	frag := mediatest.MP4SegmentNoDurations(1, 1, 0, 50, 2000)

	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	if tr.StatedDur != 0 {
		t.Errorf("stated duration = %d, want 0 when nothing states one", tr.StatedDur)
	}
	if _, ok := tr.DurationSec(); ok {
		t.Error("a duration was measured from a fragment and an init that both state none")
	}
}

// A trex too short to hold its defaults states nothing usable, and reading past
// it would take four bytes of whatever follows for a sample duration.
func TestParseMoov_TruncatedTrexIsIgnored(t *testing.T) {
	full := mediatest.MP4InitTrex(1, 90000, 1280, 720, 3600, 0)
	moov, ok := findBox(full, "moov")
	if !ok {
		t.Fatal("no moov in the fixture")
	}
	mvex, ok := findBox(moov, "mvex")
	if !ok {
		t.Fatal("no mvex in the fixture")
	}
	_ = mvex

	// A moov whose mvex carries a trex of only eight bytes.
	short := mkbox("moov", mkbox("mvex", mkbox("trex", make([]byte, 8))))
	inits := map[uint32]*initTrack{}
	parseMoov(short[8:], inits) // parseMoov takes the payload

	for _, it := range inits {
		if it.trexDuration != 0 {
			t.Errorf("a truncated trex yielded a default duration of %d", it.trexDuration)
		}
	}
}
