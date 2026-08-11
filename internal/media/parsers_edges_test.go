package media

import (
	"errors"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The remaining branches of the bitstream and packed-audio readers: the chroma
// formats other than 4:2:0, the two variable-length blocks that sit before the
// resolution in an H.264 parameter set, the ADTS header variants, and the ID3
// frame walk.
//
// The shape of the risk is the same throughout. None of these fail loudly. A
// mismeasured field leaves the reader a few bits out of step and it returns a
// resolution or a duration that looks entirely plausible, which is how a check
// ends up reporting a defect against media that is correct.

// ---------- H.264: chroma formats and crop units ----------

// The crop offsets are counted in chroma samples, so the luma step is the
// subsampling factor — different for each chroma format, and different again for
// interlaced content. Using 4:2:0's factor everywhere silently miscrops.
func TestParseH264SPS_CropUnitsPerChromaFormat(t *testing.T) {
	tests := []struct {
		name         string
		chroma       uint32
		frameMBsOnly uint32
		cropBottom   uint32
		cropRight    uint32
		wantW, wantH int
	}{
		// 4:2:0 progressive: cropUnitX 2, cropUnitY 2. 68 map units = 1088 lines,
		// minus 4*2 = 1080.
		{"4:2:0 progressive", 1, 1, 4, 0, 1920, 1080},
		// 4:2:2 subsamples horizontally only, so the vertical unit is 1.
		{"4:2:2 progressive", 2, 1, 4, 0, 1920, 1084},
		// 4:4:4 is not subsampled: both units are 1.
		{"4:4:4 progressive", 3, 1, 4, 0, 1920, 1084},
		// 4:4:4 horizontally: cropUnitX becomes 1, so the same offset crops half
		// as much as it would in 4:2:0.
		{"4:4:4 crops width one for one", 3, 1, 0, 4, 1916, 1088},
		{"4:2:0 crops width in pairs", 1, 1, 0, 4, 1912, 1088},
		// Interlaced: the frame is two fields, so the height doubles and the
		// vertical crop unit doubles with it.
		{"4:2:0 interlaced", 1, 0, 4, 0, 1920, 2160},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sps := mediatest.SPS(mediatest.SPSParams{
				ProfileIDC:       100,
				ChromaFormatIDC:  tc.chroma,
				WidthInMBsMinus1: 119, // 120 macroblocks = 1920 luma samples
				HeightInMapUnits: 67,  // 68 map units = 1088 lines
				FrameMBsOnly:     tc.frameMBsOnly,
				FrameCropping:    true,
				CropRight:        tc.cropRight,
				CropBottom:       tc.cropBottom,
			})
			w, h, ok := parseH264SPS(sps)
			if !ok {
				t.Fatal("parse failed")
			}
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("got %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

// 4:4:4 states separate_colour_plane_flag, and when it is set the planes are
// coded independently: ChromaArrayType becomes 0, which is not subsampled at all
// whatever chroma_format_idc says.
func TestParseH264SPS_FourFourFourIsReadWithoutASubsamplingFactor(t *testing.T) {
	sps := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC:       100,
		ChromaFormatIDC:  3,
		WidthInMBsMinus1: 119,
		HeightInMapUnits: 67,
		FrameMBsOnly:     1,
		FrameCropping:    true,
		CropBottom:       4,
	})
	w, h, ok := parseH264SPS(sps)
	if !ok {
		t.Fatal("a 4:4:4 parameter set failed to parse")
	}
	if w != 1920 || h != 1084 {
		t.Errorf("got %dx%d, want 1920x1084", w, h)
	}
}

// The scaling matrices are the first variable-length block before the
// resolution. In 4:4:4 there are twelve lists rather than eight, and reading
// eight leaves the rest of the parameter set four lists out of step.
func TestParseH264SPS_ScalingMatrixListCountFollowsTheChromaFormat(t *testing.T) {
	for _, chroma := range []uint32{1, 3} {
		sps := mediatest.SPS(mediatest.SPSParams{
			ProfileIDC:       100,
			ChromaFormatIDC:  chroma,
			WidthInMBsMinus1: 79, // 80 macroblocks = 1280
			HeightInMapUnits: 44, // 45 map units = 720
			FrameMBsOnly:     1,
			ScalingMatrix:    true,
		})
		w, h, ok := parseH264SPS(sps)
		if !ok {
			t.Errorf("chroma %d: a parameter set with scaling matrices failed to parse", chroma)
			continue
		}
		if w != 1280 || h != 720 {
			t.Errorf("chroma %d: got %dx%d, want 1280x720 — the scaling matrices were mismeasured", chroma, w, h)
		}
	}
}

// pic_order_cnt_type 1 is the second variable-length block: a list of frame
// offsets whose length the parameter set states. It sits directly before the
// macroblock counts, so mismeasuring it reads the resolution out of an offset.
func TestParseH264SPS_PicOrderCntTypeOneOffsetList(t *testing.T) {
	for _, offsets := range [][]int32{
		nil,
		{0},
		{1, -1, 2, -2, 16, -16},
	} {
		sps := mediatest.SPS(mediatest.SPSParams{
			ProfileIDC:        100,
			ChromaFormatIDC:   1,
			WidthInMBsMinus1:  79,
			HeightInMapUnits:  44,
			FrameMBsOnly:      1,
			PicOrderCntType:   1,
			OffsetForRefFrame: offsets,
		})
		w, h, ok := parseH264SPS(sps)
		if !ok {
			t.Errorf("%d offsets: parse failed", len(offsets))
			continue
		}
		if w != 1280 || h != 720 {
			t.Errorf("%d offsets: got %dx%d, want 1280x720 — the offset list was mismeasured", len(offsets), w, h)
		}
	}
}

// A declared offset list longer than the standard allows is not a parameter set
// we can trust, and reading it would be an unbounded loop driven by the input.
func TestParseH264SPS_RejectsAnAbsurdOffsetListLength(t *testing.T) {
	// Hand-built: everything up to num_ref_frames_in_pic_order_cnt_cycle, which
	// is given as 300 — past the 256 maximum.
	w := &bitStringWriter{}
	w.u(8, 66) // baseline profile: no chroma block
	w.u(8, 0)
	w.u(8, 40)
	w.ue(0) // seq_parameter_set_id
	w.ue(4) // log2_max_frame_num_minus4
	w.ue(1) // pic_order_cnt_type 1
	w.bit(0)
	w.se(0)
	w.se(0)
	w.ue(300) // num_ref_frames_in_pic_order_cnt_cycle
	if _, _, ok := parseH264SPS(w.bytes()); ok {
		t.Error("an offset list of 300 entries was accepted")
	}
}

// A parameter set that runs out of bits part way through must report failure
// rather than the resolution it had assembled so far from partial reads.
func TestParseH264SPS_ExhaustedBitstreamFails(t *testing.T) {
	full := mediatest.SPSFor(1920, 1080)
	for n := 3; n < len(full); n++ {
		if w, h, ok := parseH264SPS(full[:n]); ok && (w != 1920 || h != 1080) {
			t.Errorf("truncated to %d bytes returned %dx%d as if complete", n, w, h)
		}
	}
}

// An implausible resolution means the reader lost its place, and reporting it
// would send someone hunting a defect that is really a parse failure.
func TestParseH264SPS_RejectsImplausibleResolutions(t *testing.T) {
	// Cropping wider than the coded frame: the width comes out at or below zero.
	sps := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC:       100,
		ChromaFormatIDC:  1,
		WidthInMBsMinus1: 0, // one macroblock: 16 luma samples
		HeightInMapUnits: 0,
		FrameMBsOnly:     1,
		FrameCropping:    true,
		CropRight:        8, // 8 * 2 = 16, the whole width
	})
	if w, h, ok := parseH264SPS(sps); ok {
		t.Errorf("a fully cropped frame parsed as %dx%d", w, h)
	}

	// Beyond the 16384 bound: 2000 macroblocks is 32000 luma samples.
	big := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC:       100,
		ChromaFormatIDC:  1,
		WidthInMBsMinus1: 1999,
		HeightInMapUnits: 45,
		FrameMBsOnly:     1,
	})
	if w, h, ok := parseH264SPS(big); ok {
		t.Errorf("a 32000-sample-wide frame parsed as %dx%d", w, h)
	}
}

// se is the signed Exp-Golomb code: even values map to negatives, odd to
// positives. Getting the sign wrong flips every crop offset and every frame
// offset that uses it.
func TestBitReader_SignedExpGolomb(t *testing.T) {
	// Written by mediatest's writer, read back by the reader under test.
	for _, v := range []int32{0, 1, -1, 2, -2, 3, -3, 100, -100} {
		w := &bitStringWriter{}
		w.se(v)
		w.bit(1) // a stop bit so the byte is complete
		r := &bitReader{data: w.bytes()}
		if got := r.se(); got != v {
			t.Errorf("se round trip: wrote %d, read %d", v, got)
		}
	}
}

// A scaling list whose bits run out must stop rather than spin to the end of the
// declared size reading zeros.
func TestSkipScalingList_StopsWhenTheBitsRunOut(t *testing.T) {
	r := &bitReader{data: []byte{0x80}} // one usable bit
	skipScalingList(r, 64)
	if !r.err {
		t.Error("skipScalingList consumed a 64-entry list from one byte without erroring")
	}
}

// ---------- HEVC edges ----------

func TestParseHEVCSPS_RejectsAnImpossibleChromaFormat(t *testing.T) {
	// chroma_format_idc is a ue; 4 and up are not defined.
	w := &bitStringWriter{}
	w.u(4, 0) // sps_video_parameter_set_id
	w.u(3, 0) // sps_max_sub_layers_minus1
	w.bit(1)
	writeHEVCProfileTierLevel(w)
	w.ue(0) // sps_seq_parameter_set_id
	w.ue(7) // chroma_format_idc: out of range
	w.ue(1920)
	w.ue(1080)
	w.bit(0)
	w.bit(1)
	if _, _, ok := parseHEVCSPS(w.bytes()); ok {
		t.Error("a chroma_format_idc of 7 was accepted")
	}
}

func TestParseHEVCSPS_RejectsImplausibleResolutions(t *testing.T) {
	mk := func(width, height, cropRight uint32) []byte {
		return mediatest.HEVCSPS(mediatest.HEVCSPSParams{
			ChromaFormatIDC:     1,
			WidthInLumaSamples:  width,
			HeightInLumaSamples: height,
			ConformanceWindow:   cropRight > 0,
			ConfWinRight:        cropRight,
		})
	}
	// Cropped away entirely.
	if w, h, ok := parseHEVCSPS(mk(16, 16, 8)); ok {
		t.Errorf("a fully cropped frame parsed as %dx%d", w, h)
	}
	// Past the bound.
	if w, h, ok := parseHEVCSPS(mk(20000, 1080, 0)); ok {
		t.Errorf("a 20000-sample-wide frame parsed as %dx%d", w, h)
	}
}

// A NAL too short to hold a header and any payload is skipped rather than read.
func TestHEVCResolution_SkipsUnusablyShortNALUs(t *testing.T) {
	es := []byte{0x00, 0x00, 0x00, 0x01, 0x42, 0x01} // an SPS header and nothing else
	es = append(es, 0x00, 0x00, 0x00, 0x01)
	es = append(es, hevcSPSNAL(1920, 1080)...)

	w, h, ok := hevcResolution(es)
	if !ok {
		t.Fatal("the usable parameter set was not reached past the short NAL")
	}
	if w != 1920 || h != 1080 {
		t.Errorf("got %dx%d, want 1920x1080", w, h)
	}
}

// profile_tier_level whose bits run out mid-way must leave the reader in an error
// state rather than carrying on and reading the resolution from nothing.
func TestSkipHEVCProfileTierLevel_StopsWhenTheBitsRunOut(t *testing.T) {
	r := &bitReader{data: make([]byte, 13)} // the fixed part only
	skipHEVCProfileTierLevel(r, 6)          // six sub-layers of tail that is not there
	if !r.err {
		t.Error("profile_tier_level consumed six sub-layers of absent bits without erroring")
	}
}

// ---------- medianDelta ----------

// The frame duration is the median gap between timestamps. Fewer than two
// timestamps is no gap at all, and identical timestamps give no positive gap —
// both must report zero rather than a number derived from nothing.
func TestMedianDelta(t *testing.T) {
	tests := []struct {
		name string
		in   []int64
		want int64
	}{
		{"nothing", nil, 0},
		{"one timestamp", []int64{90000}, 0},
		{"even gaps", []int64{0, 3600, 7200, 10800}, 3600},
		{"out of presentation order", []int64{7200, 0, 10800, 3600}, 3600},
		// Every timestamp identical: there is no gap to measure.
		{"all the same", []int64{90000, 90000, 90000}, 0},
		// The median, not the mean, so one discontinuity does not distort it.
		{"one outlier", []int64{0, 3600, 7200, 900000}, 3600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := medianDelta(tc.in); got != tc.want {
				t.Errorf("medianDelta(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ---------- ADTS ----------

// adtsHeader writes the seven- or nine-byte ADTS header for one frame, padded out
// to frameLen.
func adtsHeader(rateIdx, chanCfg byte, frameLen, rawBlocks int, protectionAbsent bool) []byte {
	b := make([]byte, 7)
	b[0] = 0xFF
	b[1] = 0xF1 // MPEG-4, layer 0, protection_absent
	if !protectionAbsent {
		b[1] = 0xF0
	}
	b[2] = (rateIdx << 2) | ((chanCfg >> 2) & 0x01)
	b[3] = ((chanCfg & 0x03) << 6) | byte((frameLen>>11)&0x03)
	b[4] = byte((frameLen >> 3) & 0xFF)
	b[5] = byte((frameLen&0x07)<<5) | 0x1F
	b[6] = byte(rawBlocks - 1)
	if frameLen > len(b) {
		b = append(b, make([]byte, frameLen-len(b))...)
	}
	return b
}

// protection_absent = 0 means a 16-bit CRC follows the fixed header, making the
// header nine bytes rather than seven. The distinction only matters for the
// minimum-length check, but a frame rejected there is a segment reported as not
// being audio at all.
func TestScanADTS_AcceptsFramesWithACRC(t *testing.T) {
	var b []byte
	for i := 0; i < 3; i++ {
		b = append(b, adtsHeader(4, 2, 40, 1, false)...) // 44100 Hz, stereo
	}
	frames, samples, rate, channels, err := scanADTS(b)
	if err != nil {
		t.Fatalf("scanADTS: %v", err)
	}
	if frames != 3 || samples != 3*1024 {
		t.Errorf("frames %d, samples %d; want 3 and 3072", frames, samples)
	}
	if rate != 44100 || channels != 2 {
		t.Errorf("rate %d, channels %d; want 44100 and 2", rate, channels)
	}
}

// number_of_raw_data_blocks_in_frame is a count minus one, and each block is
// 1024 samples. Ignoring it understates the duration of a segment by a factor of
// up to four, which lands as a duration-drift finding.
func TestScanADTS_CountsEveryRawDataBlock(t *testing.T) {
	b := adtsHeader(3, 2, 60, 4, true) // four blocks in one frame
	frames, samples, _, _, err := scanADTS(b)
	if err != nil {
		t.Fatalf("scanADTS: %v", err)
	}
	if frames != 1 {
		t.Errorf("frames = %d, want 1", frames)
	}
	if samples != 4096 {
		t.Errorf("samples = %d, want 4096 — the raw block count was ignored", samples)
	}
}

// Indices 13, 14 and 15 are reserved. A rate of zero would make every duration
// derived from it a division by zero, so the honest answer is a named error.
func TestScanADTS_RejectsAReservedSamplingFrequencyIndex(t *testing.T) {
	for _, idx := range []byte{13, 14, 15} {
		_, _, _, _, err := scanADTS(adtsHeader(idx, 2, 40, 1, true))
		if err == nil {
			t.Errorf("sampling_frequency_index %d was accepted", idx)
			continue
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("index %d: err = %v, want it to name the reserved index", idx, err)
		}
	}
}

// Sync lost before a single frame has been read means the bytes are not ADTS.
// Lost after one or more frames is the normal trailing partial frame, and the
// frames already read must be kept.
func TestScanADTS_SyncLoss(t *testing.T) {
	good := adtsHeader(3, 2, 40, 1, true)

	if _, _, _, _, err := scanADTS([]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66}); !errors.Is(err, ErrUnknownContainer) {
		t.Errorf("err = %v, want ErrUnknownContainer for bytes that never sync", err)
	}

	// Two good frames then rubbish: the two are kept.
	mixed := append(append([]byte{}, good...), good...)
	mixed = append(mixed, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77)
	frames, _, _, _, err := scanADTS(mixed)
	if err != nil {
		t.Fatalf("scanADTS: %v", err)
	}
	if frames != 2 {
		t.Errorf("frames = %d, want the 2 read before sync was lost", frames)
	}
}

// A declared frame length shorter than its own header, or running past the data,
// is unusable. Before any frame has been read that means the stream is not ADTS;
// after one, it is a truncated final frame and the rest stands.
func TestScanADTS_ImpossibleFrameLengths(t *testing.T) {
	// frameLen 3 is shorter than the 7-byte header, on the very first frame.
	if _, _, _, _, err := scanADTS(adtsHeader(3, 2, 3, 1, true)); !errors.Is(err, ErrUnknownContainer) {
		t.Errorf("err = %v, want ErrUnknownContainer for a frame shorter than its header", err)
	}

	// A first frame that overruns the buffer.
	over := adtsHeader(3, 2, 40, 1, true)[:20]
	over[4] = 0xFF // a much longer declared length
	if _, _, _, _, err := scanADTS(over); !errors.Is(err, ErrUnknownContainer) {
		t.Errorf("err = %v, want ErrUnknownContainer for a first frame that overruns", err)
	}

	// One good frame, then a header declaring more than remains.
	trailing := append([]byte{}, adtsHeader(3, 2, 40, 1, true)...)
	trailing = append(trailing, adtsHeader(3, 2, 400, 1, true)[:10]...)
	frames, _, _, _, err := scanADTS(trailing)
	if err != nil {
		t.Fatalf("scanADTS: %v", err)
	}
	if frames != 1 {
		t.Errorf("frames = %d, want 1 with the truncated final frame dropped", frames)
	}
}

// Too few bytes to hold even one header: the loop never runs, and zero frames
// must be reported as "not this container" rather than as an empty success.
func TestScanADTS_TooShortForAnyHeader(t *testing.T) {
	for n := 0; n < 7; n++ {
		if _, _, _, _, err := scanADTS(make([]byte, n)); !errors.Is(err, ErrUnknownContainer) {
			t.Errorf("%d bytes: err = %v, want ErrUnknownContainer", n, err)
		}
	}
}

// A frame whose header is broken makes the whole segment unreadable, and
// ParsePackedAudio has to surface that rather than report a track with no
// samples.
func TestParsePackedAudio_PropagatesAFrameError(t *testing.T) {
	_, err := ParsePackedAudio(adtsHeader(13, 2, 40, 1, true)) // reserved rate
	if err == nil {
		t.Fatal("a segment whose first frame declares a reserved rate parsed cleanly")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("err = %v, want the reserved-index error surfaced", err)
	}
}

// An ID3 tag and nothing else is a real thing to be served — an audio rendition
// whose segment body went missing — and it needs its own message rather than the
// generic unknown-container error.
func TestParsePackedAudio_ID3TagWithNoFrames(t *testing.T) {
	tag := id3Tag(nil)
	_, err := ParsePackedAudio(tag)
	if err == nil {
		t.Fatal("an ID3 tag with no audio parsed cleanly")
	}
	if !strings.Contains(err.Error(), "no audio frames") {
		t.Errorf("err = %v, want it to say there are no audio frames", err)
	}
}

// ---------- ID3 frame walk ----------

// id3Tag wraps frames in an ID3v2.4 header.
func id3Tag(frames []byte) []byte {
	out := []byte{'I', 'D', '3', 4, 0, 0}
	n := len(frames)
	out = append(out, byte(n>>21)&0x7F, byte(n>>14)&0x7F, byte(n>>7)&0x7F, byte(n)&0x7F)
	return append(out, frames...)
}

// id3Frame writes one frame with a syncsafe size, as ID3v2.4 uses.
func id3Frame(id string, body []byte) []byte {
	n := len(body)
	out := append([]byte(id), byte(n>>21)&0x7F, byte(n>>14)&0x7F, byte(n>>7)&0x7F, byte(n)&0x7F)
	out = append(out, 0x00, 0x00) // flags
	return append(out, body...)
}

func privBody(owner string, value []byte) []byte {
	out := append([]byte(owner), 0x00)
	return append(out, value...)
}

// The timestamp frame is not necessarily first: the walk has to step over
// whatever precedes it by exactly its declared size.
func TestFindAppleTimestamp_WalksPastEarlierFrames(t *testing.T) {
	value := []byte{0, 0, 0, 0, 0, 0x01, 0x5F, 0x90} // 90000 in the low bits
	frames := id3Frame("TXXX", []byte("something else"))
	frames = append(frames, id3Frame("PRIV", privBody("com.example.other", []byte{1, 2, 3}))...)
	frames = append(frames, id3Frame("PRIV", privBody(appleTimestampOwner, value))...)

	got, ok := findAppleTimestamp(frames, 4)
	if !ok {
		t.Fatal("the timestamp frame was not found behind two earlier frames")
	}
	if got != 90000 {
		t.Errorf("timestamp = %d, want 90000", got)
	}
}

// ID3v2.3 sizes are plain 32-bit integers, not syncsafe. Reading a v2.3 size as
// syncsafe understates it and the walk lands mid-frame.
func TestFindAppleTimestamp_VersionTwoThreeSizes(t *testing.T) {
	value := []byte{0, 0, 0, 0, 0, 0x01, 0x5F, 0x90}
	body := privBody(appleTimestampOwner, value)
	n := len(body)
	frames := append([]byte("PRIV"), byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	frames = append(frames, 0x00, 0x00)
	frames = append(frames, body...)

	got, ok := findAppleTimestamp(frames, 3)
	if !ok {
		t.Fatal("a v2.3-sized frame was not read")
	}
	if got != 90000 {
		t.Errorf("timestamp = %d, want 90000", got)
	}
}

func TestFindAppleTimestamp_StopsAndSkipsCleanly(t *testing.T) {
	value := []byte{0, 0, 0, 0, 0, 0x01, 0x5F, 0x90}

	tests := []struct {
		name   string
		frames []byte
		major  byte
	}{
		// Padding after the last frame: a zero id means the frames are over, and
		// reading on would interpret padding as a frame header.
		{"padding ends the walk", make([]byte, 40), 4},
		{"nothing at all", nil, 4},
		// A declared size of zero or one that runs past the tag.
		{"zero size", id3Frame("PRIV", nil), 4},
		{"size past the end", append(id3Frame("PRIV", privBody(appleTimestampOwner, value))[:10], 1, 2), 4},
		// The right owner but a value too short to hold a timestamp.
		{"short value", id3Frame("PRIV", privBody(appleTimestampOwner, []byte{1, 2, 3})), 4},
		// A PRIV frame with no NUL at all, so the owner never terminates.
		{"no owner terminator", id3Frame("PRIV", []byte("nonulhere")), 4},
		// The wrong owner: skipped, and the walk continues to the end.
		{"wrong owner", id3Frame("PRIV", privBody("com.example.x", value)), 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if ts, ok := findAppleTimestamp(tc.frames, tc.major); ok {
				t.Errorf("got timestamp %d, want none", ts)
			}
		})
	}
}

// A tag whose declared size exceeds the bytes present is treated as audio rather
// than trusted: trusting it would skip past the real frames.
func TestParseID3_TruncatedTagIsNotTrusted(t *testing.T) {
	tag := id3Tag(make([]byte, 100))
	if n, _, _ := parseID3(tag[:30]); n != 0 {
		t.Errorf("a truncated tag reported length %d, want 0", n)
	}
	for n := 0; n < 10; n++ {
		if got, _, _ := parseID3(make([]byte, n)); got != 0 {
			t.Errorf("%d bytes reported an ID3 length of %d", n, got)
		}
	}
	if got, _, _ := parseID3([]byte("NOTID3....")); got != 0 {
		t.Errorf("non-ID3 bytes reported a length of %d", got)
	}
}

func TestSyncsafeAndIndexByte(t *testing.T) {
	if got := syncsafe([]byte{0x00, 0x00, 0x02, 0x01}); got != 257 {
		t.Errorf("syncsafe = %d, want 257", got)
	}
	// The high bit of every byte is dropped, which is the whole point of the
	// encoding: it can never contain a false sync.
	if got := syncsafe([]byte{0xFF, 0xFF, 0xFF, 0xFF}); got != 0x0FFFFFFF {
		t.Errorf("syncsafe = %#x, want 0x0FFFFFFF", got)
	}
	for n := 0; n < 4; n++ {
		if got := syncsafe(make([]byte, n)); got != 0 {
			t.Errorf("syncsafe on %d bytes = %d, want 0", n, got)
		}
	}

	if got := indexByte([]byte{1, 2, 0, 3}, 0); got != 2 {
		t.Errorf("indexByte = %d, want 2", got)
	}
	if got := indexByte([]byte{1, 2, 3}, 0); got != -1 {
		t.Errorf("indexByte with no match = %d, want -1", got)
	}
	if got := indexByte(nil, 0); got != -1 {
		t.Errorf("indexByte on nothing = %d, want -1", got)
	}
}

// ---------- helpers ----------

// bitStringWriter is a local bit writer, so a test can build a parameter set
// mediatest deliberately does not offer — an out-of-range field, or a truncated
// one.
type bitStringWriter struct{ bits []byte }

func (w *bitStringWriter) bit(v uint32) { w.bits = append(w.bits, byte(v&1)) }

func (w *bitStringWriter) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

func (w *bitStringWriter) ue(v uint32) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	for i := 0; i < n; i++ {
		w.bit(0)
	}
	for i := n; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

func (w *bitStringWriter) se(v int32) {
	if v <= 0 {
		w.ue(uint32(-2 * v))
		return
	}
	w.ue(uint32(2*v - 1))
}

func (w *bitStringWriter) bytes() []byte {
	out := make([]byte, (len(w.bits)+7)/8)
	for i, b := range w.bits {
		if b != 0 {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}

// writeHEVCProfileTierLevel writes the 96-bit fixed part, for a single-sub-layer
// parameter set.
func writeHEVCProfileTierLevel(w *bitStringWriter) {
	w.u(8, 1) // profile_space, tier_flag, profile_idc
	for i := 0; i < 32; i++ {
		w.bit(0)
	}
	for i := 0; i < 48; i++ {
		w.bit(0)
	}
	w.u(8, 93) // general_level_idc
}

// hevcSPSNAL wraps a parameter set for width x height in a two-byte HEVC NAL
// header, with the start code.
func hevcSPSNAL(width, height int) []byte {
	out := []byte{33 << 1, 0x01}
	return append(out, mediatest.HEVCSPSFor(width, height)...)
}
