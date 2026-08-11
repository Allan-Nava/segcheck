package media

import "testing"

// trun is where an fMP4 states its sample durations, and everything about it is
// optional: which per-sample fields are present is a flag bitmap, and the fields
// that are present have to be stepped over in exactly the right order. A reader
// that miscounts the stride does not fail — it adds a sample size to the
// duration total and reports a segment whose length is wildly wrong, which
// lands as a duration-drift finding against media that is perfectly fine.
//
// parseTrun is exercised here directly, with boxes built field by field, because
// a whole-segment fixture can only carry one flag combination at a time.

// trunBox assembles a trun payload as parseTrun receives it: version, the
// 24-bit flag bitmap, the sample count, then the body.
func trunBox(version byte, flags, count uint32, body []byte) []byte {
	b := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	b = append(b, be32Bytes(count)...)
	return append(b, body...)
}

func be32Bytes(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

const (
	trunDataOffset       = 0x000001
	trunFirstSampleFlags = 0x000004
	trunSampleDuration   = 0x000100
	trunSampleSize       = 0x000200
	trunSampleFlags      = 0x000400
	trunSampleCTOffset   = 0x000800
)

// With no per-sample fields at all, every sample takes the tfhd defaults. This
// is the common shape for CMAF audio, and getting it wrong yields a zero
// duration for a segment that is perfectly well described.
func TestParseTrun_TakesTfhdDefaultsWhenNoPerSampleFieldsArePresent(t *testing.T) {
	var f fragTrack
	parseTrun(trunBox(0, 0, 5, nil), &f, 3000, 100)

	if f.samples != 5 {
		t.Errorf("samples = %d, want 5", f.samples)
	}
	if f.sumDuration != 15000 {
		t.Errorf("sumDuration = %d, want 15000 (5 x the tfhd default of 3000)", f.sumDuration)
	}
	if f.sumSize != 500 {
		t.Errorf("sumSize = %d, want 500 (5 x the tfhd default of 100)", f.sumSize)
	}
	if f.haveCT {
		t.Error("haveCT set by a trun that carries no composition offsets")
	}
}

// Per-sample durations and sizes override the defaults, and each sample's two
// fields are read in order.
func TestParseTrun_ReadsPerSampleDurationsAndSizes(t *testing.T) {
	var body []byte
	for _, s := range []struct{ dur, size uint32 }{{3000, 120}, {3600, 90}, {3000, 110}} {
		body = append(body, be32Bytes(s.dur)...)
		body = append(body, be32Bytes(s.size)...)
	}
	var f fragTrack
	parseTrun(trunBox(0, trunSampleDuration|trunSampleSize, 3, body), &f, 9999, 9999)

	if f.samples != 3 {
		t.Errorf("samples = %d, want 3", f.samples)
	}
	if f.sumDuration != 9600 {
		t.Errorf("sumDuration = %d, want 9600 — the tfhd default leaked in", f.sumDuration)
	}
	if f.sumSize != 320 {
		t.Errorf("sumSize = %d, want 320", f.sumSize)
	}
}

// Only sizes present: durations must come from the default, and the size must be
// read from the start of each sample record rather than from a duration slot
// that is not there.
func TestParseTrun_MixesPerSampleSizesWithDefaultDurations(t *testing.T) {
	var body []byte
	for _, size := range []uint32{120, 90} {
		body = append(body, be32Bytes(size)...)
	}
	var f fragTrack
	parseTrun(trunBox(0, trunSampleSize, 2, body), &f, 3000, 9999)

	if f.sumDuration != 6000 {
		t.Errorf("sumDuration = %d, want 6000 (2 x the default 3000)", f.sumDuration)
	}
	if f.sumSize != 210 {
		t.Errorf("sumSize = %d, want 210", f.sumSize)
	}
}

// Every per-sample field at once, which is the only shape that pins the stride
// of each one.
//
// The fields are read in a fixed order — duration, size, flags, composition
// offset — and each present field advances the cursor for the next. sample_flags
// in particular carries nothing this tool needs, so the only thing that matters
// about it is being stepped over; the mistake is invisible unless a field it
// precedes is also present, because the cursor is re-based on every sample.
// With the composition offset last, dropping any one stride makes the offset be
// read out of the preceding word.
func TestParseTrun_ReadsEveryPerSampleFieldInOrder(t *testing.T) {
	samples := []struct {
		dur, size, flags uint32
		ct               int32
	}{
		{3000, 120, 0x02000000, 1500},
		{3600, 90, 0x01010000, -900},
	}
	var body []byte
	for _, s := range samples {
		body = append(body, be32Bytes(s.dur)...)
		body = append(body, be32Bytes(s.size)...)
		body = append(body, be32Bytes(s.flags)...)
		body = append(body, be32Bytes(uint32(s.ct))...)
	}
	flags := trunSampleDuration | trunSampleSize | trunSampleFlags | trunSampleCTOffset

	var f fragTrack
	parseTrun(trunBox(1, uint32(flags), 2, body), &f, 0, 0)

	if f.samples != 2 {
		t.Errorf("samples = %d, want 2", f.samples)
	}
	if f.sumDuration != 6600 {
		t.Errorf("sumDuration = %d, want 6600", f.sumDuration)
	}
	if f.sumSize != 210 {
		t.Errorf("sumSize = %d, want 210", f.sumSize)
	}
	// -900 is only reachable if duration, size and the flag word were each
	// stepped over by exactly four bytes.
	if !f.haveCT || f.minCTOffset != -900 {
		t.Errorf("minCTOffset = %d (haveCT %v), want -900 — a per-sample stride is wrong, so the composition offset came out of another field",
			f.minCTOffset, f.haveCT)
	}
}

// The composition-time offset is unsigned in a version 0 trun and signed in a
// version 1 one. It is added to the start timestamp, so reading a small negative
// offset as unsigned moves the segment's start forward by about 13 hours and
// reports a gap against the previous segment that does not exist.
func TestParseTrun_CompositionOffsetIsSignedOnlyInVersion1(t *testing.T) {
	// -1000 as a two's-complement 32-bit word.
	negative := int32(-1000)
	body := be32Bytes(uint32(negative))

	var v1 fragTrack
	parseTrun(trunBox(1, trunSampleCTOffset, 1, body), &v1, 0, 0)
	if !v1.haveCT {
		t.Fatal("version 1: no composition offset recorded")
	}
	if v1.minCTOffset != -1000 {
		t.Errorf("version 1: minCTOffset = %d, want -1000 read as signed", v1.minCTOffset)
	}

	var v0 fragTrack
	parseTrun(trunBox(0, trunSampleCTOffset, 1, body), &v0, 0, 0)
	if !v0.haveCT {
		t.Fatal("version 0: no composition offset recorded")
	}
	if v0.minCTOffset != 4294966296 {
		t.Errorf("version 0: minCTOffset = %d, want 4294966296 read as unsigned", v0.minCTOffset)
	}
}

// The offset kept is the minimum across the fragment, not the first one: with
// B-frames the samples are not in presentation order, so the earliest presented
// frame can sit anywhere in the box.
func TestParseTrun_KeepsTheSmallestCompositionOffsetNotTheFirst(t *testing.T) {
	var body []byte
	for _, ct := range []int32{3600, -1800, 7200} {
		body = append(body, be32Bytes(uint32(ct))...)
	}
	var f fragTrack
	parseTrun(trunBox(1, trunSampleCTOffset, 3, body), &f, 0, 0)

	if f.minCTOffset != -1800 {
		t.Errorf("minCTOffset = %d, want -1800, the smallest of the three", f.minCTOffset)
	}
}

// data_offset and first_sample_flags sit between the count and the sample
// records. They are the other way to lose sync with the per-sample fields.
func TestParseTrun_SkipsTheOptionalHeaderFields(t *testing.T) {
	body := append(be32Bytes(0x6C), be32Bytes(0x02000000)...) // data-offset, first-sample-flags
	body = append(body, be32Bytes(3000)...)
	body = append(body, be32Bytes(3600)...)

	var f fragTrack
	parseTrun(trunBox(0, trunDataOffset|trunFirstSampleFlags|trunSampleDuration, 2, body), &f, 0, 0)

	if f.samples != 2 {
		t.Errorf("samples = %d, want 2", f.samples)
	}
	if f.sumDuration != 6600 {
		t.Errorf("sumDuration = %d, want 6600 — a header field was read as a duration", f.sumDuration)
	}
}

// A declared count larger than the box can hold is a malformed segment. Trusting
// the count would read past the records; the bytes actually present win, and the
// sample total has to shrink with them or the mean frame duration is wrong.
func TestParseTrun_ClampsADeclaredCountThatOverrunsTheBox(t *testing.T) {
	body := append(be32Bytes(3000), be32Bytes(3600)...) // room for two, not a thousand

	var f fragTrack
	parseTrun(trunBox(0, trunSampleDuration, 1000, body), &f, 0, 0)

	if f.samples != 2 {
		t.Errorf("samples = %d, want 2 — the declared 1000 was trusted over the bytes present", f.samples)
	}
	if f.sumDuration != 6600 {
		t.Errorf("sumDuration = %d, want 6600", f.sumDuration)
	}
}

// The optional header fields are declared present but the box ends before them,
// so the cursor is already past the end when the sample records would start. The
// available count goes negative, and a negative count must end the parse rather
// than be used as a length.
func TestParseTrun_OptionalHeadersPastTheEndOfTheBox(t *testing.T) {
	// data-offset and first-sample-flags push the offset to 16; the box holds 10.
	trun := trunBox(0, trunDataOffset|trunFirstSampleFlags|trunSampleDuration, 5, []byte{1, 2})

	var f fragTrack
	parseTrun(trun, &f, 3000, 100) // must not panic

	if f.samples != 0 {
		t.Errorf("samples = %d, want 0 — a negative available count was used as a length", f.samples)
	}
	if f.sumDuration != 0 {
		t.Errorf("sumDuration = %d, want 0", f.sumDuration)
	}
}

func TestParseTrun_TruncatedBoxIsIgnored(t *testing.T) {
	for n := 0; n < 8; n++ {
		var f fragTrack
		parseTrun(make([]byte, n), &f, 3000, 100) // must not panic
		if f.samples != 0 {
			t.Errorf("%d-byte trun contributed %d samples", n, f.samples)
		}
	}
}

// Two truns in one traf accumulate rather than replace: a fragment may split its
// samples across several runs, and a reader that overwrites reports only the
// last run's worth of media.
func TestParseTrun_AccumulatesAcrossRuns(t *testing.T) {
	var f fragTrack
	parseTrun(trunBox(0, trunSampleDuration, 2, append(be32Bytes(3000), be32Bytes(3000)...)), &f, 0, 0)
	parseTrun(trunBox(0, trunSampleDuration, 1, be32Bytes(3600)), &f, 0, 0)

	if f.samples != 3 {
		t.Errorf("samples = %d, want 3 across the two runs", f.samples)
	}
	if f.sumDuration != 9600 {
		t.Errorf("sumDuration = %d, want 9600 across the two runs", f.sumDuration)
	}
}
