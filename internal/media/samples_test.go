package media

import (
	"bytes"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-91 and SC-93 both need the same thing first: a track's samples located in the
// mdat. They are not in any header — the tfhd names a base, and each trun states an
// offset from it followed by the sizes of the samples that begin there. Without this
// a two-track segment can only be described from its headers, which is how a caption
// track carrying data and one carrying none looked identical.

func TestTrackSamples(t *testing.T) {
	a := [][]byte{[]byte("aaaa"), []byte("bb"), []byte("cccccc")}
	b := [][]byte{[]byte("XY"), []byte("Z")}
	seg := mediatest.MP4SegmentSamples(1,
		mediatest.TrackSamples{TrackID: 1, BaseDecodeTime: 0, SampleDuration: 3600, Samples: a},
		mediatest.TrackSamples{TrackID: 2, BaseDecodeTime: 0, SampleDuration: 3600, Samples: b},
	)

	got := trackSamples(seg, nil)
	if len(got) != 2 {
		t.Fatalf("tracks = %d, want 2: %+v", len(got), got)
	}
	for _, tc := range []struct {
		id   uint32
		want [][]byte
	}{{1, a}, {2, b}} {
		ranges := got[tc.id]
		if len(ranges) != len(tc.want) {
			t.Errorf("track %d: %d samples, want %d", tc.id, len(ranges), len(tc.want))
			continue
		}
		for i, r := range ranges {
			if !bytes.Equal(seg[r.start:r.end], tc.want[i]) {
				t.Errorf("track %d sample %d = %q, want %q", tc.id, i, seg[r.start:r.end], tc.want[i])
			}
		}
	}
}

// A sample of no length carries nothing to read. It is not an error — an empty
// subtitle sample is how CMAF says nothing is said here — but there is no range for
// it, and one that returned a zero-length slice would have every reader downstream
// guarding against it.
func TestTrackSamples_ZeroLengthSamples(t *testing.T) {
	seg := mediatest.MP4SegmentSamples(1, mediatest.TrackSamples{
		TrackID: 1, SampleDuration: 3600,
		Samples: [][]byte{[]byte("aa"), nil, []byte("bb")},
	})
	ranges := trackSamples(seg, nil)[1]
	if len(ranges) != 2 {
		t.Fatalf("samples = %d, want 2: the empty one has no bytes", len(ranges))
	}
	if !bytes.Equal(seg[ranges[0].start:ranges[0].end], []byte("aa")) ||
		!bytes.Equal(seg[ranges[1].start:ranges[1].end], []byte("bb")) {
		t.Error("the samples after an empty one are misplaced")
	}
}

// A sample size taken from the movie-extends default rather than stated per sample,
// which is how a large share of on-demand DASH is packaged.
func TestTrackSamples_DefaultSampleSize(t *testing.T) {
	// A trun with no sample-size flag: three samples of the trex default length.
	// The data offset is measured from the moof, so it depends on the moof's own
	// size — built once to learn it, then again for real. The field is fixed width,
	// so the size does not change.
	build := func(dataOffset uint32) []byte {
		trun := mkbox("trun", u32b(0x000001), u32b(3), u32b(dataOffset))
		tfhd := mkbox("tfhd", u32b(0x020000), u32b(1))
		return mkbox("moof", mkbox("mfhd", u32b(0), u32b(1)), mkbox("traf", tfhd, trun))
	}
	moof := build(uint32(len(build(0)) + 8)) // the mdat header follows the moof
	seg := append(moof, mkbox("mdat", []byte("ABCDEFGHIJKLMNOPQRSTUVWX"))...)

	ranges := trackSamples(seg, map[uint32]fragDefaults{1: {size: 4}})[1]
	if len(ranges) != 3 {
		t.Fatalf("samples = %d, want 3 from the trex default: %+v", len(ranges), ranges)
	}
	if got := string(seg[ranges[0].start:ranges[0].end]); got != "ABCD" {
		t.Errorf("first sample = %q, want ABCD", got)
	}
}

// A data offset is signed: a fragment may place its samples before the moof that
// describes them, and reading it unsigned puts them four gigabytes away.
func TestTrunSamples_NegativeDataOffset(t *testing.T) {
	// base 100, offset -100, so the samples begin at 0. The offset goes through a
	// variable because Go will not fold a negative constant into a uint32.
	back := int32(-100)
	trun := joinBytes(u32b(0x000201), u32b(1), u32b(uint32(back)), u32b(4))
	ranges := trunSamples(trun, 100, 200, 0, 0)
	if len(ranges) != 1 || ranges[0].start != 0 || ranges[0].end != 4 {
		t.Errorf("ranges = %+v, want one at 0..4", ranges)
	}
}

// A range past the end of what arrived is dropped rather than clamped: a truncated
// segment should yield the samples that came, not a short version of one that did not.
func TestTrunSamples_PastTheEnd(t *testing.T) {
	trun := joinBytes(u32b(0x000201), u32b(2), u32b(0), u32b(4), u32b(400))
	ranges := trunSamples(trun, 0, 10, 0, 0)
	if len(ranges) != 1 {
		t.Errorf("ranges = %+v, want only the sample that fits", ranges)
	}
	// A negative base puts everything outside the data.
	if got := trunSamples(trun, -50, 10, 0, 0); len(got) != 0 {
		t.Errorf("ranges = %+v, want none from a negative base", got)
	}
}

// The shapes that locate nothing at all.
func TestTrackSamples_Unlocatable(t *testing.T) {
	if got := trackSamples(nil, nil); len(got) != 0 {
		t.Errorf("empty data gave %+v", got)
	}
	// A traf with no tfhd names no track.
	moof := mkbox("moof", mkbox("traf", mkbox("trun", u32b(0), u32b(1))))
	if got := trackSamples(append(moof, mkbox("mdat", make([]byte, 16))...), nil); len(got) != 0 {
		t.Errorf("a traf with no tfhd gave %+v", got)
	}
	// A trun too short to hold its own header.
	if got := trunSamples([]byte{0x00, 0x00}, 0, 100, 4, 0); got != nil {
		t.Errorf("a truncated trun gave %+v", got)
	}
	// data-offset-present with no offset there.
	if got := trunSamples(joinBytes(u32b(0x000001), u32b(1)), 0, 100, 4, 0); got != nil {
		t.Errorf("a trun promising an offset it does not carry gave %+v", got)
	}
}

// joinBytes concatenates, which the box helpers do for their own payloads but a bare
// trun under test needs done by hand.
func joinBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// The tfhd fields that shift where the samples are, each of which a reader that
// skipped it would misread the ones after.
func TestTrafSamples_TFHDVariants(t *testing.T) {
	// An explicit base-data-offset instead of default-base-is-moof, plus a
	// sample-description-index and a default-sample-duration before the size — all
	// optional, all shifting the fields after them.
	// flags 0x00001B: base-data-offset, sample-description-index,
	// default-sample-duration, default-sample-size.
	tfhd := mkbox("tfhd",
		u32b(0x00001B), u32b(1),
		u64b(0),    // base-data-offset: the start of the segment
		u32b(1),    // sample-description-index
		u32b(3600), // default-sample-duration
		u32b(4),    // default-sample-size
	)
	// A trun with no per-sample sizes, so the tfhd default is what applies.
	build := func(dataOffset uint32) []byte {
		trun := mkbox("trun", u32b(0x000001), u32b(2), u32b(dataOffset))
		return mkbox("moof", mkbox("mfhd", u32b(0), u32b(1)), mkbox("traf", tfhd, trun))
	}
	moof := build(0)
	// The base is 0 — the start of the segment — so the offset is absolute.
	moof = build(uint32(len(moof) + 8))
	seg := append(moof, mkbox("mdat", []byte("ABCDEFGH"))...)

	ranges := trackSamples(seg, nil)[1]
	if len(ranges) != 2 {
		t.Fatalf("samples = %d, want 2: %+v", len(ranges), ranges)
	}
	if got := string(seg[ranges[0].start:ranges[0].end]); got != "ABCD" {
		t.Errorf("first sample = %q, want ABCD: a tfhd field was skipped wrongly", got)
	}
	if got := string(seg[ranges[1].start:ranges[1].end]); got != "EFGH" {
		t.Errorf("second sample = %q, want EFGH", got)
	}
}

// A trun declaring more samples than its own bytes can describe is trusted only as far
// as the bytes present, and one declaring a negative count is not trusted at all.
func TestTrunSamples_CountBeyondTheBox(t *testing.T) {
	// Two sizes present, ten declared.
	trun := joinBytes(u32b(0x000201), u32b(10), u32b(0), u32b(4), u32b(4))
	if got := trunSamples(trun, 0, 100, 0, 0); len(got) != 2 {
		t.Errorf("ranges = %+v, want the two the box actually describes", got)
	}
	// A count of four billion is bounded by the walk, not by a sign check: int is
	// 64 bits here, so a 32-bit count never comes out negative.
	huge := joinBytes(u32b(0x000001), u32b(0xFFFFFFFF), u32b(0))
	if got := trunSamples(huge, 0, 100, 4, 0); len(got) != 25 {
		t.Errorf("samples = %d, want the 25 that fit in the data", len(got))
	}
	// Per-sample sizes promised but cut off part-way.
	cut := joinBytes(u32b(0x000201), u32b(2), u32b(0), u32b(4), []byte{0x00, 0x00})
	if got := trunSamples(cut, 0, 100, 0, 0); len(got) != 1 {
		t.Errorf("ranges = %+v, want only the size that is there", got)
	}
}

// The sample walk is bounded, so a fragment declaring more samples than any real one
// carries cannot make the walk unbounded.
func TestTrunSamples_WalkIsBounded(t *testing.T) {
	trun := joinBytes(u32b(0x000001), u32b(100000), u32b(0))
	got := trunSamples(trun, 0, 1<<20, 1, 0)
	if len(got) != maxSampleWalk {
		t.Errorf("samples = %d, want the walk to stop at %d", len(got), maxSampleWalk)
	}
	// And a walk that is already at the bound adds nothing.
	if got := trunSamples(trun, 0, 1<<20, 1, maxSampleWalk); len(got) != 0 {
		t.Errorf("samples = %d, want none once the bound is reached", len(got))
	}
}
