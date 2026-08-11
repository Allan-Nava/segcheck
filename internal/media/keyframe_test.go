package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-16: whether a segment opens on a keyframe.
//
// A segment that opens on anything else cannot be switched into. The decoder
// reaching it mid-stream has no reference picture, so it either shows nothing
// until the next real random access point or shows corruption — which is the
// defect behind "ABR switching stutters even though the segment boundaries line
// up perfectly". Nothing in the manifest can reveal it: the boundaries can be
// exactly aligned and every duration correct while every switch still breaks.
//
// The answer has three values, not two, and the third is what keeps this honest.
// An fMP4 fragment need not carry the sync-sample flag at all, and an
// MPEG-TS segment whose video payload was not captured cannot be read either.
// "I could not tell" must be distinguishable from "it does not", or the check
// reports a defect wherever it merely failed to look.

func TestOpensOnKeyframe_H264(t *testing.T) {
	sps := mediatest.SPSFor(1280, 720)

	idr := mediatest.TSWithSPSOpening(0, 3600, 5, sps, mediatest.H264IDRSlice)
	info, err := ParseTS(idr)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	opens, known := tr.StartsOnKeyframe()
	if !known {
		t.Fatal("could not tell whether an H.264 segment opens on a keyframe")
	}
	if !opens {
		t.Error("a segment opening on an IDR slice was reported as not opening on a keyframe")
	}

	// The same segment with an ordinary slice in front: switchable becomes not.
	nonIDR := mediatest.TSWithSPSOpening(0, 3600, 5, sps, mediatest.H264NonIDRSlice)
	info, err = ParseTS(nonIDR)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, _ = info.Track(Video)
	opens, known = tr.StartsOnKeyframe()
	if !known {
		t.Fatal("could not tell whether a non-IDR-opening segment opens on a keyframe")
	}
	if opens {
		t.Error("a segment opening on a non-IDR slice was reported as opening on a keyframe")
	}
}

// HEVC calls them IRAPs and there are several: types 16 through 21 are all random
// access points, so a reader that only recognised IDR_W_RADL would report a
// perfectly switchable CRA-opening segment as broken.
func TestOpensOnKeyframe_HEVC(t *testing.T) {
	sps := mediatest.HEVCSPSFor(1280, 720)

	tests := []struct {
		name    string
		nalType byte
		want    bool
	}{
		{"IDR_W_RADL", mediatest.HEVCIDRWRadl, true},
		{"CRA_NUT", mediatest.HEVCCRANut, true},
		{"TRAIL_R", mediatest.HEVCTrailR, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseTS(mediatest.TSWithHEVCSPSOpening(0, 3600, 5, sps, tc.nalType))
			if err != nil {
				t.Fatalf("ParseTS: %v", err)
			}
			tr, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			opens, known := tr.StartsOnKeyframe()
			if !known {
				t.Fatal("could not tell whether the segment opens on a keyframe")
			}
			if opens != tc.want {
				t.Errorf("opens on keyframe = %v, want %v", opens, tc.want)
			}
		})
	}
}

// fMP4 states it in trun's first-sample-flags rather than in the bitstream, so it
// can be read without touching the video payload at all.
func TestOpensOnKeyframe_FragmentedMP4(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)

	for _, tc := range []struct{ sync bool }{{true}, {false}} {
		seg := mediatest.MP4SegmentSync(1, 1, 0, 3600, 50, 2000, tc.sync)
		info, err := ParseMP4(seg, init)
		if err != nil {
			t.Fatalf("ParseMP4: %v", err)
		}
		tr, ok := info.Track(Video)
		if !ok {
			t.Fatal("no video track")
		}
		opens, known := tr.StartsOnKeyframe()
		if !known {
			t.Fatalf("sync=%v: could not tell whether the fragment opens on a sync sample", tc.sync)
		}
		if opens != tc.sync {
			t.Errorf("sync=%v: opens on keyframe = %v, want %v", tc.sync, opens, tc.sync)
		}
	}
}

// sample_is_non_sync_sample is one bit in a word packed with neighbours, and
// sample_depends_on two fields away reads 2 for a picture that depends on nothing
// — so a reader looking at the wrong field still gets a plausible answer whenever
// a packager sets both consistently. These words are the ones where the two
// disagree, which is what actually pins the bit position.
func TestSampleIsNonSync_BitPosition(t *testing.T) {
	tests := []struct {
		name    string
		flags   uint32
		nonSync bool
	}{
		// Only the bit that matters, with sample_depends_on left at 0 (unknown).
		// Many packagers write exactly this, and a reader watching
		// sample_depends_on would call it a sync sample.
		{"non-sync bit alone", 0x00010000, true},
		// sample_depends_on = 2 (depends on nothing) and the non-sync bit clear:
		// an I-frame, stated both ways.
		{"I-frame", 0x02000000, false},
		// sample_depends_on = 1 (depends on others) but the non-sync bit clear.
		// Contradictory, and the spec's sync bit is the one that decides.
		{"depends on others, sync bit clear", 0x01000000, false},
		// Both set: an ordinary predicted picture.
		{"predicted picture", 0x01010000, true},
		{"nothing set", 0x00000000, false},
		// The degradation priority occupies the low sixteen bits and must not be
		// mistaken for flags.
		{"degradation priority set", 0x0000FFFF, false},
	}
	for _, tc := range tests {
		if got := sampleIsNonSync(tc.flags); got != tc.nonSync {
			t.Errorf("%s: sampleIsNonSync(%#08x) = %v, want %v", tc.name, tc.flags, got, tc.nonSync)
		}
	}
}

// A fragment whose first sample states only the non-sync bit, read end to end.
// This is the case a reader watching sample_depends_on gets wrong.
func TestOpensOnKeyframe_NonSyncBitAlone(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)

	traf := trafBox(
		tfhdBox(0x08, 1, u32b(3600)),
		mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(0)...)),
		// first-sample-flags present, carrying the non-sync bit and nothing else.
		mkbox("trun", trunBox(0, trunFirstSampleFlags, 50, u32b(0x00010000))),
	)
	info, err := ParseMP4(moofBox(1, traf), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	opens, known := tr.StartsOnKeyframe()
	if !known {
		t.Fatal("a stated first-sample-flags word was not read")
	}
	if opens {
		t.Error("a fragment whose first sample sets sample_is_non_sync_sample was reported as opening on a keyframe")
	}
}

// Several traf boxes can describe one track in one fragment. Only the first says
// where the segment opens; a later one that happens to be a sync sample must not
// overwrite the verdict, or a segment opening on a predicted picture reads as
// switchable.
func TestOpensOnKeyframe_FirstTrafDecides(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)

	opening := trafBox(
		tfhdBox(0x08, 1, u32b(3600)),
		mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(0)...)),
		mkbox("trun", trunBox(0, trunFirstSampleFlags, 25, u32b(0x01010000))), // not a sync sample
	)
	later := trafBox(
		tfhdBox(0x08, 1, u32b(3600)),
		mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(90000)...)),
		mkbox("trun", trunBox(0, trunFirstSampleFlags, 25, u32b(0x02000000))), // a sync sample
	)
	info, err := ParseMP4(moofBox(1, opening, later), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	opens, known := tr.StartsOnKeyframe()
	if !known {
		t.Fatal("no keyframe verdict")
	}
	if opens {
		t.Error("a later traf's sync sample overwrote the verdict for where the segment opens")
	}
}

// The tfhd default answers when no trun states the flags, which is how a fragment
// whose samples are uniform expresses itself.
func TestOpensOnKeyframe_FromTheTfhdDefault(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)

	// tfhd flags 0x08 default-sample-duration + 0x20 default-sample-flags.
	traf := trafBox(
		tfhdBox(0x08|0x20, 1, u32b(3600), u32b(0x00010000)),
		mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(0)...)),
		mkbox("trun", trunBox(0, 0, 25, nil)),
	)
	info, err := ParseMP4(moofBox(1, traf), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	opens, known := tr.StartsOnKeyframe()
	if !known {
		t.Fatal("the tfhd default-sample-flags were not read")
	}
	if opens {
		t.Error("a tfhd default stating a non-sync sample was reported as opening on a keyframe")
	}
}

// An elementary stream carrying only parameter sets — a segment whose picture
// data never arrived, or was cut off by the capture cap — states nothing about
// where it opens. Answering from the SPS would be judging a header.
func TestOpensOnKeyframe_ParameterSetsOnly(t *testing.T) {
	// VPS, SPS and PPS and no coded picture at all.
	if v := hevcKeyframes(mediatest.HEVCAnnexB(1280, 720)); v.Known {
		t.Errorf("a parameter-sets-only HEVC stream returned a verdict (%+v)", v)
	}
	if v := h264Keyframes(mediatest.AnnexB(mediatest.SPSFor(1280, 720))); v.Known {
		t.Errorf("a parameter-sets-only H.264 stream returned a verdict (%+v)", v)
	}
}

// Adjacent start codes leave an empty NAL between them, which real streams carry
// as padding. Reading a type out of no bytes would be a read past the end.
func TestOpensOnKeyframe_EmptyAndTruncatedNALUs(t *testing.T) {
	// Two start codes back to back, then a genuine IDR.
	es := []byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01}
	es = append(es, 0x00, 0x00, 0x00, 0x01, 0x65) // the IDR
	v := h264Keyframes(es)
	if !v.Known {
		t.Fatal("the IDR past the empty NAL was not reached")
	}
	if !v.Opens {
		t.Error("an IDR following an empty NAL was not recognised")
	}

	// HEVC's header is two bytes, so a one-byte NAL cannot state a type either.
	hevc := []byte{0x00, 0x00, 0x00, 0x01, 0x26} // one byte only
	hevc = append(hevc, 0x00, 0x00, 0x00, 0x01, 19<<1, 0x01)
	v = hevcKeyframes(hevc)
	if !v.Known {
		t.Fatal("the IRAP past the one-byte NAL was not reached")
	}
	if !v.Opens {
		t.Error("an IDR_W_RADL following a one-byte NAL was not recognised")
	}
}

// A fragment that carries no sync-sample flag says nothing about the matter, and
// the parser has to report that rather than guessing either way. This is the
// `(value, false)` protocol: a check that sees false stays quiet.
func TestOpensOnKeyframe_UnknowableCases(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)

	// MP4Segment writes neither first-sample-flags nor default-sample-flags.
	info, err := ParseMP4(mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	if _, known := tr.StartsOnKeyframe(); known {
		t.Error("a fragment with no sync-sample flag claimed to know whether it opens on a keyframe")
	}

	// An MPEG-TS segment with no video payload captured: the PES packets carry
	// timestamps but nothing the slice type can be read from.
	info, err = ParseTS(mediatest.TS(0, 3600, 5))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, _ = info.Track(Video)
	if opens, known := tr.StartsOnKeyframe(); known {
		t.Errorf("a segment with no readable slice claimed to know (%v)", opens)
	}

	// Audio has no keyframes to speak of, and must not be reported as failing a
	// video-only rule.
	audioInit := mediatest.MP4Init(2, 48000, "audio", 0, 0)
	info, err = ParseMP4(mediatest.MP4SegmentSync(2, 1, 0, 1024, 50, 2000, false), audioInit)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	at, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if _, known := at.StartsOnKeyframe(); known {
		t.Error("an audio track reported a keyframe verdict")
	}
}

// Both of the following were real defects, found by running against Apple's
// reference streams rather than by reasoning — the strict first draft of this
// check reported bipbop as having no keyframe at all. They are the reason
// "there is none" and "nobody looked far enough" are separate answers.

// The walk is bounded, and hitting the bound is not evidence of absence. A 1080p
// picture is split across dozens of slices, so a segment whose opening picture
// belongs to the previous GOP can put hundreds of units in front of the keyframe.
func TestAnnexBNALUsLimit_ReportsTruncation(t *testing.T) {
	// Ten units, asked for at most four.
	var es []byte
	for i := 0; i < 10; i++ {
		es = append(es, 0x00, 0x00, 0x00, 0x01, 0x41, byte(i))
	}
	nals, truncated := annexBNALUsLimit(es, 4)
	if len(nals) != 4 {
		t.Errorf("got %d units, want the 4 asked for", len(nals))
	}
	if !truncated {
		t.Error("hitting the cap was not reported as truncation")
	}

	// The same stream with room to spare is not truncated.
	nals, truncated = annexBNALUsLimit(es, 64)
	if len(nals) != 10 {
		t.Errorf("got %d units, want all 10", len(nals))
	}
	if truncated {
		t.Error("a complete walk was reported as truncated")
	}
}

// A walk that ran out of room must not claim the segment has no keyframe.
func TestKeyframes_TruncatedWalkIsNotAVerdictOfAbsence(t *testing.T) {
	// More non-IDR slices than the scan will look at, and no keyframe at all.
	var es []byte
	for i := 0; i <= keyframeScanNALUs; i++ {
		es = append(es, 0x00, 0x00, 0x00, 0x01, 0x41, 0x9A)
	}
	v := h264Keyframes(es)
	if !v.Known {
		t.Fatal("the opening slice was not read")
	}
	if v.Opens {
		t.Error("a non-IDR opening slice was read as a keyframe")
	}
	if v.Scanned {
		t.Error("a truncated walk claimed to have established that there is no keyframe")
	}

	// A short stream with no keyframe *is* a verdict of absence: the walk finished.
	short := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A}
	v = h264Keyframes(short)
	if !v.Scanned {
		t.Error("a complete walk did not count as scanned")
	}
	if v.Present {
		t.Error("a stream with no IDR reported one present")
	}
}

// The elementary-stream capture is bounded too, and a keyframe past that bound is
// one nobody looked for.
func TestTsStream_CaptureCapClearsTheScannedFlag(t *testing.T) {
	full := &tsStream{streamType: 0x1B, lastCC: -1, es: make([]byte, maxESCapture)}
	// Plant a non-IDR opening slice so the walk has something to read.
	copy(full.es, []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A})

	v := full.keyframes(h264Keyframes)
	if !v.Known {
		t.Fatal("the opening slice was not read")
	}
	if v.Scanned {
		t.Error("a capture cut off at the cap claimed to have scanned the whole segment")
	}

	// Below the cap, the flag stands.
	partial := &tsStream{streamType: 0x1B, lastCC: -1,
		es: []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A}}
	if v := partial.keyframes(h264Keyframes); !v.Scanned {
		t.Error("a complete capture was not counted as scanned")
	}
}

// Both accessors refuse to answer for a non-video track. Audio is independently
// decodable frame by frame, so the question does not apply — and answering it
// would invite the check to report on audio rungs.
func TestKeyframeAccessors_OnlyAnswerForVideo(t *testing.T) {
	for _, kind := range []TrackKind{Audio, Other} {
		tr := Track{
			Kind: kind, OpensOnKeyframe: true, HasKeyframe: true,
			KeyframeKnown: true, KeyframeScanned: true,
		}
		if opens, known := tr.StartsOnKeyframe(); known || opens {
			t.Errorf("%s: StartsOnKeyframe = %v, %v; want no answer", kind, opens, known)
		}
		if present, scanned := tr.ContainsKeyframe(); scanned || present {
			t.Errorf("%s: ContainsKeyframe = %v, %v; want no answer", kind, present, scanned)
		}
	}

	// Video does answer, so the guard is not simply refusing everything.
	v := Track{Kind: Video, HasKeyframe: true, KeyframeScanned: true}
	if present, scanned := v.ContainsKeyframe(); !present || !scanned {
		t.Errorf("video: ContainsKeyframe = %v, %v; want true, true", present, scanned)
	}
}
