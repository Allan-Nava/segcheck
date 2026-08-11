package media

import "testing"

// The ISO-BMFF box plumbing and the individual box readers, exercised directly
// with boxes built field by field.
//
// Everything here is a guard or a version branch that a whole-segment fixture
// cannot reach: a 64-bit box size, a box that extends to the end of the file, a
// version 1 tkhd, an encrypted sample entry that hides its real codec in
// sinf/frma. They are all the same class of defect — the reader either loses a
// track silently or reads a number out of the wrong offset.

func mkbox(typ string, parts ...[]byte) []byte {
	var payload []byte
	for _, p := range parts {
		payload = append(payload, p...)
	}
	out := u32b(uint32(len(payload) + 8))
	out = append(out, []byte(typ)...)
	return append(out, payload...)
}

// mkbox64 writes the 64-bit form: a 32-bit size of 1 means the real size follows
// the type as a 64-bit largesize.
func mkbox64(typ string, payload []byte) []byte {
	out := u32b(1)
	out = append(out, []byte(typ)...)
	out = append(out, u64b(uint64(len(payload)+16))...)
	return append(out, payload...)
}

func u16b(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
func u32b(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
func u64b(v uint64) []byte {
	return append(u32b(uint32(v>>32)), u32b(uint32(v))...)
}

// ---------- integer readers ----------

// Every multi-byte read is bounds-checked, because a truncated box must yield a
// zero the caller can recognise rather than panic mid-parse.
func TestBigEndianReadersGuardShortBuffers(t *testing.T) {
	if got := be16([]byte{0x12}); got != 0 {
		t.Errorf("be16 on 1 byte = %d, want 0", got)
	}
	if got := be16([]byte{0x12, 0x34}); got != 0x1234 {
		t.Errorf("be16 = %#x", got)
	}
	for _, b := range [][]byte{nil, {1}, {1, 2}, {1, 2, 3}} {
		if got := be32(b); got != 0 {
			t.Errorf("be32 on %d bytes = %d, want 0", len(b), got)
		}
	}
	if got := be32([]byte{0x12, 0x34, 0x56, 0x78}); got != 0x12345678 {
		t.Errorf("be32 = %#x", got)
	}
	for n := 0; n < 8; n++ {
		if got := be64(make([]byte, n)); got != 0 {
			t.Errorf("be64 on %d bytes = %d, want 0", n, got)
		}
	}
	if got := be64([]byte{1, 2, 3, 4, 5, 6, 7, 8}); got != 0x0102030405060708 {
		t.Errorf("be64 = %#x", got)
	}
}

// ---------- boxesIn ----------

// size == 1 means the real size is a 64-bit largesize after the type. Segments
// over 4 GiB are rare but init segments written by some packagers use the form
// regardless, and a reader that does not know it sees a 1-byte box and gives up.
func TestBoxesIn_SixtyFourBitBoxSize(t *testing.T) {
	data := mkbox64("moov", []byte("payload!"))
	boxes := boxesIn(data)
	if len(boxes) != 1 {
		t.Fatalf("got %d boxes, want 1", len(boxes))
	}
	if boxes[0].typ != "moov" || string(boxes[0].payload) != "payload!" {
		t.Errorf("box = %q / %q", boxes[0].typ, boxes[0].payload)
	}
}

// A 64-bit size header that is itself cut off must stop the walk rather than
// read the largesize out of whatever follows.
func TestBoxesIn_TruncatedSixtyFourBitHeaderStopsTheWalk(t *testing.T) {
	full := mkbox64("moov", []byte("payload!"))
	for n := 8; n < 16; n++ {
		if got := boxesIn(full[:n]); len(got) != 0 {
			t.Errorf("truncated to %d bytes yielded %d boxes", n, len(got))
		}
	}
}

// size == 0 means the box runs to the end of the enclosing data, which is legal
// for the last box in a file — mdat in a progressive MP4 is the usual case.
func TestBoxesIn_ZeroSizeExtendsToTheEnd(t *testing.T) {
	data := append(u32b(0), []byte("mdat")...)
	data = append(data, []byte("the rest of the file")...)

	boxes := boxesIn(data)
	if len(boxes) != 1 {
		t.Fatalf("got %d boxes, want 1", len(boxes))
	}
	if boxes[0].typ != "mdat" || string(boxes[0].payload) != "the rest of the file" {
		t.Errorf("box = %q / %q", boxes[0].typ, boxes[0].payload)
	}
}

// A declared size smaller than the header it sits in, or larger than the bytes
// present, means the rest cannot be trusted to be boxes at all. The walk keeps
// what it has already read and stops.
func TestBoxesIn_ImpossibleSizesStopTheWalkButKeepEarlierBoxes(t *testing.T) {
	good := mkbox("ftyp", []byte("isom"))

	// A size of 4 is smaller than the 8-byte header.
	tooSmall := append(good, u32b(4)...)
	tooSmall = append(tooSmall, []byte("moov")...)
	if got := boxesIn(tooSmall); len(got) != 1 || got[0].typ != "ftyp" {
		t.Errorf("a size below the header length lost the earlier box: %d boxes", len(got))
	}

	// A size that runs past the end of the data.
	tooBig := append(good, u32b(9999)...)
	tooBig = append(tooBig, []byte("moov")...)
	if got := boxesIn(tooBig); len(got) != 1 || got[0].typ != "ftyp" {
		t.Errorf("an overrunning size lost the earlier box: %d boxes", len(got))
	}

	// Fewer than eight bytes cannot be a box header.
	for n := 0; n < 8; n++ {
		if got := boxesIn(make([]byte, n)); len(got) != 0 {
			t.Errorf("%d bytes yielded %d boxes", n, len(got))
		}
	}
}

// ---------- tkhd / mdhd / hdlr ----------

// tkhd carries the track_ID at a different offset in each version, because
// version 1 widens the creation and modification times to 64 bits. Reading a
// version 1 box at the version 0 offset picks up half a timestamp as the id, and
// the track then fails to pair with its fragment.
func TestParseTkhd_TrackIDOffsetDependsOnTheVersion(t *testing.T) {
	// version 0: 4 version+flags, 4 creation, 4 modification, then track_ID.
	v0 := append([]byte{0, 0, 0, 0}, u32b(0)...)
	v0 = append(v0, u32b(0)...)
	v0 = append(v0, u32b(7)...)          // track_ID
	v0 = append(v0, make([]byte, 60)...) // through to the matrix
	v0 = append(v0, u32b(1920<<16)...)   // width, 16.16 fixed point
	v0 = append(v0, u32b(1080<<16)...)   // height
	if id, w, h := parseTkhd(v0); id != 7 || w != 1920 || h != 1080 {
		t.Errorf("version 0 tkhd = id %d, %dx%d; want id 7, 1920x1080", id, w, h)
	}

	// version 1: the two times are 64-bit, so track_ID sits eight bytes later.
	v1 := append([]byte{1, 0, 0, 0}, u64b(0)...)
	v1 = append(v1, u64b(0)...)
	v1 = append(v1, u32b(9)...) // track_ID
	v1 = append(v1, make([]byte, 60)...)
	v1 = append(v1, u32b(1280<<16)...)
	v1 = append(v1, u32b(720<<16)...)
	if id, w, h := parseTkhd(v1); id != 9 || w != 1280 || h != 720 {
		t.Errorf("version 1 tkhd = id %d, %dx%d; want id 9, 1280x720", id, w, h)
	}
}

func TestParseTkhd_TruncatedYieldsNothing(t *testing.T) {
	for n := 0; n < 4; n++ {
		if id, w, h := parseTkhd(make([]byte, n)); id != 0 || w != 0 || h != 0 {
			t.Errorf("%d-byte tkhd = id %d, %dx%d; want zeros", n, id, w, h)
		}
	}
	// Long enough to have a version but too short for a track_ID: the id stays
	// zero rather than being read out of bounds.
	if id, _, _ := parseTkhd(make([]byte, 6)); id != 0 {
		t.Errorf("short tkhd invented track_ID %d", id)
	}
}

// The timescale is the unit of every timestamp in the segment. Reading it from
// the wrong offset does not fail — it yields a plausible number, and every
// duration computed from it is silently wrong.
func TestParseMdhd_TimescaleOffsetDependsOnTheVersion(t *testing.T) {
	v0 := append([]byte{0, 0, 0, 0}, u32b(0)...)
	v0 = append(v0, u32b(0)...)
	v0 = append(v0, u32b(90000)...)
	if got := parseMdhd(v0); got != 90000 {
		t.Errorf("version 0 mdhd timescale = %d, want 90000", got)
	}

	v1 := append([]byte{1, 0, 0, 0}, u64b(0)...)
	v1 = append(v1, u64b(0)...)
	v1 = append(v1, u32b(48000)...)
	if got := parseMdhd(v1); got != 48000 {
		t.Errorf("version 1 mdhd timescale = %d, want 48000", got)
	}
}

// An unreadable timescale must be 0, which is this tool's protocol for "not
// measurable" — every duration check then stays quiet instead of dividing by a
// guess.
func TestParseMdhd_UnreadableTimescaleIsZero(t *testing.T) {
	for n := 0; n < 4; n++ {
		if got := parseMdhd(make([]byte, n)); got != 0 {
			t.Errorf("%d-byte mdhd = %d, want 0", n, got)
		}
	}
	// A version 0 box that stops before the timescale field.
	if got := parseMdhd(make([]byte, 14)); got != 0 {
		t.Errorf("truncated version 0 mdhd = %d, want 0", got)
	}
	// A version 1 box that stops before its later timescale field.
	short := append([]byte{1, 0, 0, 0}, make([]byte, 16)...)
	if got := parseMdhd(short); got != 0 {
		t.Errorf("truncated version 1 mdhd = %d, want 0", got)
	}
}

func TestParseHdlr(t *testing.T) {
	hdlr := func(handler string) []byte {
		b := append(u32b(0), u32b(0)...)
		return append(b, []byte(handler)...)
	}
	if got := parseHdlr(hdlr("vide")); got != Video {
		t.Errorf("vide = %s, want video", got)
	}
	if got := parseHdlr(hdlr("soun")); got != Audio {
		t.Errorf("soun = %s, want audio", got)
	}
	// Subtitles, metadata and hints are all real handlers this tool does not
	// analyse; they must land as "other" rather than being taken for video.
	for _, h := range []string{"subt", "text", "meta", "hint", "sbtl"} {
		if got := parseHdlr(hdlr(h)); got != Other {
			t.Errorf("%s = %s, want other", h, got)
		}
	}
	for n := 0; n < 12; n++ {
		if got := parseHdlr(make([]byte, n)); got != Other {
			t.Errorf("%d-byte hdlr = %s, want other", n, got)
		}
	}
}

// ---------- stsd ----------

func visualEntry(typ string, w, h uint16) []byte {
	payload := make([]byte, 24) // reserved, data_reference_index, pre_defined
	payload = append(payload, u16b(w)...)
	payload = append(payload, u16b(h)...)
	payload = append(payload, make([]byte, 50)...)
	return mkbox(typ, payload)
}

func stsdBox(entries ...[]byte) []byte {
	b := append(u32b(0), u32b(uint32(len(entries)))...)
	for _, e := range entries {
		b = append(b, e...)
	}
	return b
}

// encvEntry builds an encrypted visual sample entry the way a packager does: the
// full 78-byte VisualSampleEntry prefix — which is where the resolution lives —
// and then the child boxes, sinf among them.
func encvEntry(originalFormat string, w, h uint16) []byte {
	payload := make([]byte, 24)
	payload = append(payload, u16b(w)...)
	payload = append(payload, u16b(h)...)
	payload = append(payload, make([]byte, 78-28)...) // out to the fixed 78 bytes
	payload = append(payload, mkbox("sinf", mkbox("frma", []byte(originalFormat)))...)
	return mkbox("encv", payload)
}

// encaEntry is the audio equivalent: 28 fixed bytes, then the child boxes.
func encaEntry(originalFormat string) []byte {
	payload := make([]byte, 28)
	payload = append(payload, mkbox("sinf", mkbox("frma", []byte(originalFormat)))...)
	return mkbox("enca", payload)
}

// An encrypted track rewrites its sample entry type to encv/enca, and sinf/frma
// preserves the format the encryption replaced.
//
// Recovering it is not cosmetic. Left as "encv", the tracks check compares the
// manifest's declared "h264" against a bitstream reported as "encv" and emits a
// codec-mismatch WARN on every encrypted rendition — a defect reported against
// media that is entirely correct. The resolution has the same problem from the
// other side: "encv" is not in the visual-sample-entry list, so the frame size is
// never read and the resolution check skips the rung in silence.
//
// The child boxes sit after the sample entry's fixed fields — 78 bytes for video,
// 28 for audio — so a search that starts at byte 0 reads the leading reserved
// zeros as a box of size 0 that swallows the whole entry and finds nothing.
func TestParseStsd_EncryptedEntryRecoversTheOriginalCodecAndResolution(t *testing.T) {
	codec, w, h, enc := parseStsd(stsdBox(encvEntry("avc1", 1920, 1080)))
	if !enc {
		t.Error("an encv sample entry was not reported as encrypted")
	}
	if codec != "h264" {
		t.Errorf("codec = %q, want h264 recovered from sinf/frma — as %q it reports a phantom codec mismatch against the manifest", codec, codec)
	}
	if w != 1920 || h != 1080 {
		t.Errorf("resolution = %dx%d, want 1920x1080 — an encrypted rung with no resolution is skipped in silence", w, h)
	}
}

func TestParseStsd_EncryptedAudioRecoversItsCodec(t *testing.T) {
	codec, _, _, enc := parseStsd(stsdBox(encaEntry("mp4a")))
	if !enc {
		t.Error("an enca sample entry was not reported as encrypted")
	}
	if codec != "aac" {
		t.Errorf("codec = %q, want aac recovered from sinf/frma", codec)
	}
}

// An encrypted entry with no sinf to read: still encrypted, and the codec is
// reported as the entry type rather than guessed. The tracks check finds no match
// for it, which is the honest outcome.
func TestParseStsd_EncryptedWithoutSinf(t *testing.T) {
	for _, tc := range []struct{ typ string }{{"encv"}, {"enca"}} {
		codec, _, _, enc := parseStsd(stsdBox(mkbox(tc.typ, make([]byte, 90))))
		if !enc {
			t.Errorf("%s was not reported as encrypted", tc.typ)
		}
		if codec != tc.typ {
			t.Errorf("%s with no sinf = %q, want the type passed through", tc.typ, codec)
		}
	}
	// A sinf whose frma is truncated is equally unreadable.
	payload := make([]byte, 78)
	payload = append(payload, mkbox("sinf", mkbox("frma", []byte("av")))...)
	if codec, _, _, _ := parseStsd(stsdBox(mkbox("encv", payload))); codec != "encv" {
		t.Errorf("a truncated frma yielded codec %q, want encv", codec)
	}
}

func TestParseStsd_UnreadableEntries(t *testing.T) {
	for n := 0; n < 8; n++ {
		if codec, w, h, enc := parseStsd(make([]byte, n)); codec != "" || w != 0 || h != 0 || enc {
			t.Errorf("%d-byte stsd = %q %dx%d enc=%v, want nothing", n, codec, w, h, enc)
		}
	}
	// A well-formed stsd that declares entries but carries none.
	if codec, _, _, _ := parseStsd(stsdBox()); codec != "" {
		t.Errorf("an empty stsd reported codec %q", codec)
	}
}

// A non-visual sample entry has no width or height to read, and a visual one
// that is too short to hold them must not have them read out of bounds.
func TestParseStsd_ResolutionOnlyFromVisualEntries(t *testing.T) {
	if _, w, h, _ := parseStsd(stsdBox(mkbox("mp4a", make([]byte, 40)))); w != 0 || h != 0 {
		t.Errorf("an audio sample entry reported %dx%d", w, h)
	}
	if _, w, h, _ := parseStsd(stsdBox(mkbox("avc1", make([]byte, 20)))); w != 0 || h != 0 {
		t.Errorf("a truncated visual entry reported %dx%d", w, h)
	}
	if codec, w, h, _ := parseStsd(stsdBox(visualEntry("hvc1", 3840, 2160))); codec != "hevc" || w != 3840 || h != 2160 {
		t.Errorf("hvc1 entry = %q %dx%d", codec, w, h)
	}
}

func TestIsVisualSampleEntry(t *testing.T) {
	for _, typ := range []string{"avc1", "avc3", "hvc1", "hev1", "vvc1", "vvi1", "vp08", "vp09", "av01", "dvh1", "dvhe", "mp4v"} {
		if !isVisualSampleEntry(typ) {
			t.Errorf("%s is a visual sample entry", typ)
		}
	}
	for _, typ := range []string{"mp4a", "ac-3", "ec-3", "opus", "encv", "stpp", ""} {
		if isVisualSampleEntry(typ) {
			t.Errorf("%s is not a visual sample entry", typ)
		}
	}
}

// ---------- traf / tfhd / tfdt ----------

func tfhdBox(flags uint32, trackID uint32, tail ...[]byte) []byte {
	b := []byte{0, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	b = append(b, u32b(trackID)...)
	for _, t := range tail {
		b = append(b, t...)
	}
	return mkbox("tfhd", b)
}

// The optional tfhd fields are a flag bitmap, and each present field shifts the
// ones after it. Miscounting the offset reads a default sample duration out of
// the middle of a base-data-offset, which multiplies into the whole segment's
// stated duration.
func TestParseTraf_TfhdOptionalFieldOffsets(t *testing.T) {
	// base-data-offset (0x01) + sample-description-index (0x02) +
	// default-sample-duration (0x08) + default-sample-size (0x10).
	tfhd := tfhdBox(0x01|0x02|0x08|0x10, 1,
		u64b(0x1000), // base-data-offset
		u32b(1),      // sample-description-index
		u32b(3000),   // default-sample-duration
		u32b(150),    // default-sample-size
	)
	// A trun with no per-sample fields, so both defaults have to be used.
	trun := mkbox("trun", trunBox(0, 0, 4, nil))
	traf := append(tfhd, trun...)

	out := map[uint32]*fragTrack{}
	parseTraf(traf, out)

	f := out[1]
	if f == nil {
		t.Fatal("no track parsed from the traf")
	}
	if f.samples != 4 {
		t.Errorf("samples = %d, want 4", f.samples)
	}
	if f.sumDuration != 12000 {
		t.Errorf("sumDuration = %d, want 12000 — a tfhd default was read from the wrong offset", f.sumDuration)
	}
	if f.sumSize != 600 {
		t.Errorf("sumSize = %d, want 600 — a tfhd default was read from the wrong offset", f.sumSize)
	}
}

// The default fields may be flagged but cut off. The offset still has to advance
// so a later field is not read from the missing one's position.
func TestParseTraf_FlaggedButTruncatedDefaults(t *testing.T) {
	tfhd := tfhdBox(0x08|0x10, 1) // both flagged, neither present
	trun := mkbox("trun", trunBox(0, 0, 3, nil))

	out := map[uint32]*fragTrack{}
	parseTraf(append(tfhd, trun...), out)

	f := out[1]
	if f == nil {
		t.Fatal("no track parsed")
	}
	if f.sumDuration != 0 || f.sumSize != 0 {
		t.Errorf("sumDuration %d / sumSize %d, want zeros from absent defaults", f.sumDuration, f.sumSize)
	}
	if f.samples != 3 {
		t.Errorf("samples = %d, want 3", f.samples)
	}
}

// tfdt is the segment's decode time, and it is 32-bit in version 0 and 64-bit in
// version 1. A live stream running long enough overflows 32 bits, which is
// exactly when reading the wrong width starts reporting a discontinuity.
func TestParseTraf_TfdtVersionWidths(t *testing.T) {
	mk := func(tfdt []byte) *fragTrack {
		traf := append(tfhdBox(0, 1), tfdt...)
		traf = append(traf, mkbox("trun", trunBox(0, 0, 1, nil))...)
		out := map[uint32]*fragTrack{}
		parseTraf(traf, out)
		return out[1]
	}

	v0 := mk(mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(90000)...)))
	if v0 == nil || !v0.haveBase || v0.baseDecode != 90000 {
		t.Errorf("version 0 tfdt = %+v, want baseDecode 90000", v0)
	}

	// A value past 2^32, which only the 64-bit form can carry.
	const big = int64(1) << 34
	v1 := mk(mkbox("tfdt", append([]byte{1, 0, 0, 0}, u64b(uint64(big))...)))
	if v1 == nil || !v1.haveBase || v1.baseDecode != big {
		t.Errorf("version 1 tfdt baseDecode = %v, want %d", v1, big)
	}

	// A version 1 tfdt too short for its 64-bit field reports no base at all
	// rather than half a timestamp.
	short := mk(mkbox("tfdt", append([]byte{1, 0, 0, 0}, u32b(5)...)))
	if short == nil {
		t.Fatal("no track parsed")
	}
	if short.baseDecode != 0 {
		t.Errorf("truncated version 1 tfdt = %d, want 0", short.baseDecode)
	}
}

// Several traf boxes can describe one track in one fragment. The segment starts
// at the earliest decode time, not at whichever traf happened to be last.
func TestParseTraf_EarliestDecodeTimeWinsAcrossTrafs(t *testing.T) {
	mk := func(base uint32) []byte {
		traf := append(tfhdBox(0, 1), mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(base)...))...)
		return append(traf, mkbox("trun", trunBox(0, 0, 1, nil))...)
	}
	out := map[uint32]*fragTrack{}
	parseTraf(mk(180000), out) // the later one first
	parseTraf(mk(90000), out)

	if out[1].baseDecode != 90000 {
		t.Errorf("baseDecode = %d, want the earliest, 90000", out[1].baseDecode)
	}
	if out[1].samples != 2 {
		t.Errorf("samples = %d, want 2 accumulated across both trafs", out[1].samples)
	}
}

// senc marks per-sample encryption. The encryption state is reported from the
// init segment's sample entry, so the branch exists to be explicit that a senc
// in the fragment does not change the answer — but it must not break the parse.
func TestParseTraf_SencDoesNotDisturbTheParse(t *testing.T) {
	traf := append(tfhdBox(0x08, 1, u32b(3000)), mkbox("senc", make([]byte, 8))...)
	traf = append(traf, mkbox("trun", trunBox(0, 0, 2, nil))...)

	out := map[uint32]*fragTrack{}
	parseTraf(traf, out)
	if out[1] == nil || out[1].samples != 2 || out[1].sumDuration != 6000 {
		t.Errorf("a senc box disturbed the parse: %+v", out[1])
	}
}

// No tfhd, or one too short to hold a track_ID, means there is no track to
// attribute anything to. Guessing an id would merge two tracks' samples.
func TestParseTraf_WithoutAUsableTfhd(t *testing.T) {
	out := map[uint32]*fragTrack{}
	parseTraf(mkbox("trun", trunBox(0, 0, 5, nil)), out)
	if len(out) != 0 {
		t.Errorf("a traf with no tfhd produced %d tracks", len(out))
	}

	short := append(mkbox("tfhd", []byte{0, 0, 0, 0}), mkbox("trun", trunBox(0, 0, 5, nil))...)
	parseTraf(short, out)
	if len(out) != 0 {
		t.Errorf("a traf with a 4-byte tfhd produced %d tracks", len(out))
	}
}

// ---------- ParseMP4 ----------

// An initialisation segment on its own has no fragments, so there is no timeline
// to report — but it does declare the tracks, and describing them is what lets
// the init check say the codec and resolution are as promised.
func TestParseMP4_InitSegmentAloneDescribesItsTracks(t *testing.T) {
	init := moovWith(
		trakBox(1, "vide", 90000, visualEntry("avc1", 1920, 1080)),
		trakBox(2, "soun", 48000, mkbox("mp4a", make([]byte, 28))),
	)
	info, err := ParseMP4(init, nil)
	if err != nil {
		t.Fatalf("ParseMP4 on an init segment alone: %v", err)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(info.Tracks))
	}
	// Sorted by track_ID so the output of two runs is identical.
	if info.Tracks[0].ID != 1 || info.Tracks[1].ID != 2 {
		t.Errorf("track ids = %d, %d; want 1, 2 in order", info.Tracks[0].ID, info.Tracks[1].ID)
	}
	if info.Tracks[0].Kind != Video || info.Tracks[0].Codec != "h264" {
		t.Errorf("track 1 = %s / %s", info.Tracks[0].Kind, info.Tracks[0].Codec)
	}
	if info.Tracks[0].Width != 1920 || info.Tracks[0].Height != 1080 {
		t.Errorf("track 1 resolution = %dx%d", info.Tracks[0].Width, info.Tracks[0].Height)
	}
	if info.Tracks[1].Kind != Audio || info.Tracks[1].Timescale != 48000 {
		t.Errorf("track 2 = %s / timescale %d", info.Tracks[1].Kind, info.Tracks[1].Timescale)
	}
	// No fragments means no timestamps, and saying otherwise would invent a
	// timeline for a file that carries no media.
	if info.Tracks[0].HasPTS {
		t.Error("an init segment alone reported presentation timestamps")
	}
}

// Neither a moov nor a moof: this is not something we can describe as MP4 at
// all, and the honest answer is the unknown-container error rather than an empty
// success that reads as a segment with no tracks.
func TestParseMP4_NeitherMoovNorMoof(t *testing.T) {
	data := mkbox("ftyp", []byte("isom"))
	if _, err := ParseMP4(data, nil); err != ErrUnknownContainer {
		t.Errorf("err = %v, want ErrUnknownContainer", err)
	}
}

// A packager that numbers the init segment's track 1 and the fragment's track 2
// is out of spec, but it happens. With exactly one of each the pairing is
// unambiguous, so the metadata is applied rather than the track being reported
// as an unknown "other" with no timescale — which would silence every timing
// check on the rendition.
func TestParseMP4_SingleTrackPairsDespiteAMismatchedTrackID(t *testing.T) {
	init := moovWith(trakBox(1, "vide", 90000, visualEntry("avc1", 1280, 720)))

	traf := trafBox(
		tfhdBox(0x08, 2, u32b(3600)),
		mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(90000)...)),
		mkbox("trun", trunBox(0, 0, 25, nil)),
	)
	info, err := ParseMP4(moofBox(7, traf), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(info.Tracks))
	}
	tr := info.Tracks[0]
	if tr.Timescale != 90000 {
		t.Errorf("timescale = %d, want 90000 taken from the init segment despite the id mismatch", tr.Timescale)
	}
	if tr.Kind != Video || tr.Codec != "h264" || tr.Width != 1280 {
		t.Errorf("track = %s / %s / %dx%d", tr.Kind, tr.Codec, tr.Width, tr.Height)
	}
	if info.Sequence != 7 {
		t.Errorf("sequence = %d, want 7", info.Sequence)
	}
}

// With more than one track a mismatched id cannot be resolved by pairing, so the
// track is reported with no metadata rather than being given another track's
// timescale — a wrong timescale is worse than a missing one.
func TestParseMP4_MismatchedIDWithSeveralTracksStaysUnknown(t *testing.T) {
	init := moovWith(
		trakBox(1, "vide", 90000, visualEntry("avc1", 1280, 720)),
		trakBox(2, "soun", 48000, mkbox("mp4a", make([]byte, 28))),
	)
	traf := trafBox(tfhdBox(0x08, 99, u32b(3600)), mkbox("trun", trunBox(0, 0, 25, nil)))
	info, err := ParseMP4(moofBox(1, traf), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(info.Tracks))
	}
	if info.Tracks[0].Timescale != 0 {
		t.Errorf("timescale = %d, want 0 — an unpairable track must not borrow one", info.Tracks[0].Timescale)
	}
	if info.Tracks[0].Kind != Other {
		t.Errorf("kind = %s, want other", info.Tracks[0].Kind)
	}
}

// The composition offset shifts presentation relative to decode. With B-frames
// the smallest offset is where presentation actually begins, so MinPTS is the
// decode time plus that offset — not the decode time alone.
func TestParseMP4_CompositionOffsetShiftsTheStart(t *testing.T) {
	init := moovWith(trakBox(1, "vide", 90000, visualEntry("avc1", 1280, 720)))

	// Two samples, each 3600 ticks, with composition offsets of +7200 and +3600:
	// presentation starts 3600 after the decode time.
	body := append(u32b(3600), u32b(7200)...)
	body = append(body, u32b(3600)...)
	body = append(body, u32b(3600)...)
	traf := trafBox(
		tfhdBox(0, 1),
		mkbox("tfdt", append([]byte{0, 0, 0, 0}, u32b(90000)...)),
		mkbox("trun", trunBox(1, trunSampleDuration|trunSampleCTOffset, 2, body)),
	)
	info, err := ParseMP4(moofBox(1, traf), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr := info.Tracks[0]
	if !tr.HasPTS {
		t.Fatal("no timestamps")
	}
	if tr.MinPTS != 93600 {
		t.Errorf("MinPTS = %d, want 93600 (decode 90000 + the smallest composition offset 3600)", tr.MinPTS)
	}
	// The interval end is kept as the last timestamp, so it means the same thing
	// as the TS parser's MaxPTS.
	if tr.MaxPTS != 93600+7200-3600 {
		t.Errorf("MaxPTS = %d, want %d", tr.MaxPTS, 93600+7200-3600)
	}
}

// ---------- moov assembly helpers ----------

func trakBox(id uint32, handler string, timescale uint32, sampleEntry []byte) []byte {
	tkhd := append([]byte{0, 0, 0, 0}, u32b(0)...)
	tkhd = append(tkhd, u32b(0)...)
	tkhd = append(tkhd, u32b(id)...)
	tkhd = append(tkhd, make([]byte, 60)...)
	tkhd = append(tkhd, u32b(0)...) // width, left to the sample entry
	tkhd = append(tkhd, u32b(0)...) // height

	mdhd := append([]byte{0, 0, 0, 0}, u32b(0)...)
	mdhd = append(mdhd, u32b(0)...)
	mdhd = append(mdhd, u32b(timescale)...)

	hdlr := append(u32b(0), u32b(0)...)
	hdlr = append(hdlr, []byte(handler)...)
	hdlr = append(hdlr, make([]byte, 12)...)

	stbl := mkbox("stbl", mkbox("stsd", stsdBox(sampleEntry)))
	minf := mkbox("minf", stbl)
	mdia := mkbox("mdia", mkbox("mdhd", mdhd), mkbox("hdlr", hdlr), minf)
	return mkbox("trak", mkbox("tkhd", tkhd), mdia)
}

// moovPayload is what parseMoov receives: the contents of the moov box, since
// ParseMP4 hands it the payload that findBox returned.
func moovPayload(parts ...[]byte) []byte {
	var inner []byte
	for _, p := range parts {
		inner = append(inner, p...)
	}
	return inner
}

// moovWith is the whole box, which is what ParseMP4 itself is given.
func moovWith(traks ...[]byte) []byte {
	return mkbox("moov", moovPayload(traks...))
}

// trafBox wraps the boxes of one track fragment. A moof carries traf boxes, and
// leaving the wrapper out means parseMoofs finds no fragments at all.
func trafBox(parts ...[]byte) []byte {
	return mkbox("traf", parts...)
}

func moofBox(sequence uint32, trafs ...[]byte) []byte {
	parts := [][]byte{mkbox("mfhd", append(u32b(0), u32b(sequence)...))}
	parts = append(parts, trafs...)
	return mkbox("moof", parts...)
}

// A trak with no mdia declares nothing usable, and it must be skipped rather
// than emitted as a track with no timescale that every check then reports on.
func TestParseMoov_TrakWithoutMdiaIsSkipped(t *testing.T) {
	tkhd := append([]byte{0, 0, 0, 0}, make([]byte, 8)...)
	tkhd = append(tkhd, u32b(1)...)
	tkhd = append(tkhd, make([]byte, 68)...)
	bare := mkbox("trak", mkbox("tkhd", tkhd))

	inits := map[uint32]*initTrack{}
	parseMoov(moovPayload(bare), inits)
	if len(inits) != 0 {
		t.Errorf("a trak with no mdia produced %d tracks", len(inits))
	}
}

// A pssh box anywhere in moov means the content is protected, even when the
// sample entries were left as plain avc1 — some packagers do exactly that, and
// missing it would report the segment as clear.
func TestParseMoov_PSSHMarksEveryTrackEncrypted(t *testing.T) {
	moov := moovPayload(
		mkbox("pssh", make([]byte, 16)),
		trakBox(1, "vide", 90000, visualEntry("avc1", 1280, 720)),
	)
	inits := map[uint32]*initTrack{}
	parseMoov(moov, inits)

	if len(inits) != 1 {
		t.Fatalf("got %d tracks, want 1", len(inits))
	}
	if !inits[1].encrypted {
		t.Error("a moov carrying pssh did not mark its track encrypted")
	}
	if inits[1].codec != "h264" {
		t.Errorf("codec = %q, want h264 — pssh must not hide the codec", inits[1].codec)
	}
}

// A tkhd with no track_ID (or none at all) still has to become an addressable
// track, so a synthetic id is assigned by position.
func TestParseMoov_TrackWithoutAnIDGetsOneByPosition(t *testing.T) {
	moov := moovPayload(
		trakBox(0, "vide", 90000, visualEntry("avc1", 1280, 720)),
		trakBox(0, "soun", 48000, mkbox("mp4a", make([]byte, 28))),
	)
	inits := map[uint32]*initTrack{}
	parseMoov(moov, inits)

	if len(inits) != 2 {
		t.Fatalf("got %d tracks, want 2 with synthesised ids: %v", len(inits), inits)
	}
	if inits[1] == nil || inits[2] == nil {
		t.Errorf("ids were not synthesised by position: %v", inits)
	}
}

func TestSortedInitIDsAndSortUint32(t *testing.T) {
	m := map[uint32]*initTrack{7: {}, 1: {}, 3: {}, 2: {}}
	got := sortedInitIDs(m)
	want := []uint32{1, 2, 3, 7}
	if len(got) != len(want) {
		t.Fatalf("sortedInitIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedInitIDs = %v, want %v", got, want)
		}
	}
	// Directly, including the already-sorted and reversed cases, because the map
	// above cannot be relied on to present them in any particular order.
	for _, tc := range [][]uint32{{}, {1}, {2, 1}, {1, 2, 3}, {3, 2, 1}, {5, 1, 4, 2, 3}} {
		in := append([]uint32{}, tc...)
		sortUint32(in)
		for i := 1; i < len(in); i++ {
			if in[i] < in[i-1] {
				t.Errorf("sortUint32(%v) = %v, not sorted", tc, in)
				break
			}
		}
	}
}
