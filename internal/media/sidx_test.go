package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-19: the segment index of a single-file DASH representation.
//
// A SegmentBase representation is one file. Nothing in the MPD says where its
// subsegments begin or end — only the `sidx` box does — so without reading it
// segcheck cannot sample such a stream at all, and today reports it as
// unsupported. That is an honest answer but a useless one: every check is
// skipped for the whole rendition.
//
// The offsets are the thing to get right. Each reference gives a *size*, not a
// position, so a reader that mismeasures the box header or drops first_offset
// puts every subsegment boundary in the wrong place and fetches bytes that
// straddle two of them.

func TestParseSIDX(t *testing.T) {
	entries := []mediatest.SIDXEntry{
		{Size: 1000, Duration: 90000, StartsWithSAP: true},
		{Size: 1200, Duration: 90000, StartsWithSAP: true},
		{Size: 900, Duration: 45000, StartsWithSAP: true},
	}
	box := mediatest.SIDX(0, 90000, 0, 0, entries)

	idx, err := ParseSIDX(box, 0)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	if idx.Timescale != 90000 {
		t.Errorf("timescale = %d, want 90000", idx.Timescale)
	}
	if len(idx.Entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(idx.Entries))
	}

	// The first subsegment begins immediately after the index box, and each one
	// after the previous. A size is not a position: this is the arithmetic.
	wantOffsets := []int64{int64(len(box)), int64(len(box)) + 1000, int64(len(box)) + 2200}
	for i, e := range idx.Entries {
		if e.Offset != wantOffsets[i] {
			t.Errorf("entry %d offset = %d, want %d", i, e.Offset, wantOffsets[i])
		}
		if e.Size != int64(entries[i].Size) {
			t.Errorf("entry %d size = %d, want %d", i, e.Size, entries[i].Size)
		}
		if !e.StartsWithSAP {
			t.Errorf("entry %d: starts_with_SAP was not read", i)
		}
	}
	if got := idx.Entries[2].DurationSec(idx.Timescale); got != 0.5 {
		t.Errorf("third duration = %vs, want 0.5s", got)
	}
}

// version 1 widens earliest_presentation_time and first_offset to 64 bits — the
// same widening as tfdt. Reading a version 1 box at the version 0 offsets loses
// eight bytes and every reference after it is read from the wrong place.
func TestParseSIDX_VersionOneWidensTheFixedFields(t *testing.T) {
	entries := []mediatest.SIDXEntry{{Size: 500, Duration: 90000, StartsWithSAP: true}}
	for _, version := range []byte{0, 1} {
		box := mediatest.SIDX(version, 90000, 0, 0, entries)
		idx, err := ParseSIDX(box, 0)
		if err != nil {
			t.Fatalf("version %d: %v", version, err)
		}
		if len(idx.Entries) != 1 {
			t.Fatalf("version %d: got %d entries, want 1", version, len(idx.Entries))
		}
		if idx.Entries[0].Size != 500 {
			t.Errorf("version %d: size = %d, want 500", version, idx.Entries[0].Size)
		}
		if idx.Entries[0].Offset != int64(len(box)) {
			t.Errorf("version %d: offset = %d, want %d", version, idx.Entries[0].Offset, len(box))
		}
	}
}

// first_offset shifts the whole run of subsegments, and it is exactly the field a
// reader is most likely to drop because it is usually zero.
func TestParseSIDX_FirstOffsetShiftsEverySubsegment(t *testing.T) {
	entries := []mediatest.SIDXEntry{
		{Size: 100, Duration: 90000, StartsWithSAP: true},
		{Size: 200, Duration: 90000, StartsWithSAP: true},
	}
	box := mediatest.SIDX(0, 90000, 0, 64, entries)

	idx, err := ParseSIDX(box, 0)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	base := int64(len(box)) + 64
	if idx.Entries[0].Offset != base {
		t.Errorf("first offset = %d, want %d", idx.Entries[0].Offset, base)
	}
	if idx.Entries[1].Offset != base+100 {
		t.Errorf("second offset = %d, want %d", idx.Entries[1].Offset, base+100)
	}
}

// The index box does not sit at byte zero of the file: @indexRange says where it
// is, and the subsegment offsets are absolute within the file. Anchoring them at
// zero would make every fetched range wrong by the size of ftyp and moov.
func TestParseSIDX_OffsetsAreAbsoluteInTheFile(t *testing.T) {
	entries := []mediatest.SIDXEntry{{Size: 700, Duration: 90000, StartsWithSAP: true}}
	box := mediatest.SIDX(0, 90000, 0, 0, entries)

	const indexAt = 5000
	idx, err := ParseSIDX(box, indexAt)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	want := int64(indexAt + len(box))
	if idx.Entries[0].Offset != want {
		t.Errorf("offset = %d, want %d — the index's own position was not applied", idx.Entries[0].Offset, want)
	}
}

// A reference_type of 1 points at another index rather than at media. Following
// it as if it were a subsegment fetches an index box and tries to parse it as a
// fragment, so it has to be reported rather than silently treated as media.
func TestParseSIDX_HierarchicalReferenceIsMarked(t *testing.T) {
	entries := []mediatest.SIDXEntry{
		{Size: 300, Duration: 90000, StartsWithSAP: true},
		{Size: 400, Duration: 90000, Reference: true},
	}
	idx, err := ParseSIDX(mediatest.SIDX(0, 90000, 0, 0, entries), 0)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	if idx.Entries[0].IsIndex {
		t.Error("a media reference was marked as an index")
	}
	if !idx.Entries[1].IsIndex {
		t.Error("an index reference was not marked")
	}
}

func TestParseSIDX_RejectsWhatIsNotAnIndex(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"not a box", []byte("just some bytes")},
		{"a different box", mediatest.MP4Init(1, 90000, "video", 1280, 720)},
		{"a truncated sidx", mediatest.SIDX(0, 90000, 0, 0, []mediatest.SIDXEntry{{Size: 1, Duration: 1}})[:16]},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if idx, err := ParseSIDX(tc.in, 0); err == nil {
				t.Errorf("parsed as an index: %+v", idx)
			}
		})
	}
}

// A count the box cannot hold is a malformed index. Trusting it would read past
// the end; the references actually present are what stand.
func TestParseSIDX_DeclaredCountBeyondTheBox(t *testing.T) {
	box := mediatest.SIDX(0, 90000, 0, 0, []mediatest.SIDXEntry{
		{Size: 100, Duration: 90000, StartsWithSAP: true},
		{Size: 200, Duration: 90000, StartsWithSAP: true},
	})
	// Overstate reference_count: it is the last two bytes before the references.
	box[len(box)-2*12-2] = 0xFF
	box[len(box)-2*12-1] = 0xFF

	idx, err := ParseSIDX(box, 0)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	if len(idx.Entries) != 2 {
		t.Errorf("got %d entries, want the 2 the box actually holds", len(idx.Entries))
	}
}

// @indexRange may cover more than the index alone — some packagers include the
// styp or a preceding box in it. The subsegment arithmetic is measured from where
// the *index* ends, not from the end of the range handed over.
func TestParseSIDX_IndexNotAloneInTheRange(t *testing.T) {
	entries := []mediatest.SIDXEntry{{Size: 400, Duration: 90000, StartsWithSAP: true}}
	idxBox := mediatest.SIDX(0, 90000, 0, 0, entries)

	// A free box ahead of the index, inside the same range.
	free := append([]byte{0, 0, 0, 16}, []byte("free")...)
	free = append(free, make([]byte, 8)...)
	data := append(append([]byte{}, free...), idxBox...)

	idx, err := ParseSIDX(data, 1000)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	want := int64(1000 + len(free) + len(idxBox))
	if idx.Entries[0].Offset != want {
		t.Errorf("offset = %d, want %d — measured from the end of the range instead of the end of the index", idx.Entries[0].Offset, want)
	}
}

// sidxPayload builds the fixed part of an index body, so a test can cut it at any
// field boundary. Every one of these truncations has to be an error rather than a
// partial read: an index read from the wrong offset yields byte ranges that
// straddle subsegments, and the fetched bytes then fail to parse as media — which
// would be reported as a defect in the stream.
func TestParseSIDX_TruncatedAtEveryField(t *testing.T) {
	// version 0: 4 version+flags, 4 reference_ID, 4 timescale, 8 time fields,
	// 2 reserved, 2 reference_count.
	full := make([]byte, 24)
	full[8], full[9], full[10], full[11] = 0x00, 0x01, 0x5F, 0x90 // timescale 90000
	for n := 0; n < len(full); n++ {
		if _, err := ParseSIDX(mkbox("sidx", full[:n]), 0); err == nil {
			t.Errorf("a %d-byte version 0 index body parsed", n)
		}
	}

	// version 1 widens the two time fields to 64 bits, so its fixed part is eight
	// bytes longer and the cut points move with it.
	v1 := make([]byte, 32)
	v1[0] = 1
	for n := 0; n < len(v1); n++ {
		if _, err := ParseSIDX(mkbox("sidx", v1[:n]), 0); err == nil {
			t.Errorf("a %d-byte version 1 index body parsed", n)
		}
	}
}

// An index declaring no references describes nothing, and returning an empty
// success would leave the rendition with zero segments and no reason given.
func TestParseSIDX_NoReferences(t *testing.T) {
	body := make([]byte, 24) // reference_count stays 0
	if idx, err := ParseSIDX(mkbox("sidx", body), 0); err == nil {
		t.Errorf("an index with no references parsed: %+v", idx)
	}
}

// The reference count is bounded so a malformed index cannot drive an unbounded
// allocation from the input.
func TestParseSIDX_ReferenceCountIsBounded(t *testing.T) {
	const declared = maxSIDXEntries + 1
	body := make([]byte, 24)
	body[8], body[9], body[10], body[11] = 0x00, 0x01, 0x5F, 0x90
	body[22] = byte(declared >> 8)
	body[23] = byte(declared & 0xFF)
	// Enough bytes present that the count is not clamped by the box first.
	entry := []byte{0, 0, 0x03, 0xE8, 0, 0x01, 0x5F, 0x90, 0x90, 0, 0, 0}
	for i := 0; i < declared; i++ {
		body = append(body, entry...)
	}

	idx, err := ParseSIDX(mkbox("sidx", body), 0)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	if len(idx.Entries) != maxSIDXEntries {
		t.Errorf("got %d entries, want the %d cap", len(idx.Entries), maxSIDXEntries)
	}
}

// An index with no timescale states no duration anyone can convert, and zero is
// the only honest answer — not a division by it.
func TestSIDXEntry_DurationSecWithoutATimescale(t *testing.T) {
	e := SIDXEntry{Duration: 90000}
	if got := e.DurationSec(0); got != 0 {
		t.Errorf("DurationSec with no timescale = %v, want 0", got)
	}
	if got := e.DurationSec(90000); got != 1 {
		t.Errorf("DurationSec = %v, want 1", got)
	}
}

// The index may be written with a 64-bit box size, or as the last box running to
// the end of the range. Both change where the box ends, and that is exactly what
// the subsegment offsets are measured from.
func TestFindBoxSpan_BoxSizeForms(t *testing.T) {
	entries := []mediatest.SIDXEntry{{Size: 250, Duration: 90000, StartsWithSAP: true}}
	body := mediatest.SIDX(0, 90000, 0, 0, entries)[8:] // the payload alone

	sixtyFour := mkbox64("sidx", body)
	idx, err := ParseSIDX(sixtyFour, 0)
	if err != nil {
		t.Fatalf("64-bit sized index: %v", err)
	}
	if idx.Entries[0].Offset != int64(len(sixtyFour)) {
		t.Errorf("64-bit: offset = %d, want %d", idx.Entries[0].Offset, len(sixtyFour))
	}

	// size 0: the box runs to the end of the data handed over.
	toEnd := append(u32b(0), []byte("sidx")...)
	toEnd = append(toEnd, body...)
	idx, err = ParseSIDX(toEnd, 0)
	if err != nil {
		t.Fatalf("size-0 index: %v", err)
	}
	if idx.Entries[0].Offset != int64(len(toEnd)) {
		t.Errorf("size 0: offset = %d, want %d", idx.Entries[0].Offset, len(toEnd))
	}

	// A 64-bit header the range is too short to hold must not be read past.
	for n := 8; n < 16; n++ {
		if _, err := ParseSIDX(sixtyFour[:n], 0); err == nil {
			t.Errorf("a %d-byte 64-bit header parsed", n)
		}
	}
}

// A real on-demand DASH file carries a two-level index: a top-level `sidx` whose
// references all point at leaf indexes, each leaf describing the media of its
// portion. Sony's DASH-IF test vector is built exactly this way.
//
// A reader that stops at the top level sees only index references and concludes
// the file describes no media at all — which is a wrong answer that looks like a
// broken stream.
func TestParseSIDX_HierarchicalIndexIsFollowed(t *testing.T) {
	leaves := [][]mediatest.SIDXEntry{
		{{Size: 500, Duration: 45000, StartsWithSAP: true}, {Size: 600, Duration: 45000, StartsWithSAP: true}},
		{{Size: 700, Duration: 90000, StartsWithSAP: true}},
	}
	data := mediatest.HierarchicalSIDX(90000, leaves)

	idx, err := ParseSIDX(data, 0)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	// The top level alone is two index references and no media.
	var media int
	for _, e := range idx.Entries {
		if !e.IsIndex {
			media++
		}
	}
	if media != 0 {
		t.Fatalf("the fixture is not hierarchical: %d media references at the top", media)
	}

	// Resolved, it yields the three real subsegments, in order.
	resolved, err := ResolveSIDX(data, 0)
	if err != nil {
		t.Fatalf("ResolveSIDX: %v", err)
	}
	if len(resolved.Entries) != 3 {
		t.Fatalf("got %d subsegments, want 3", len(resolved.Entries))
	}
	wantSizes := []int64{500, 600, 700}
	for i, e := range resolved.Entries {
		if e.IsIndex {
			t.Errorf("entry %d is still an index reference", i)
		}
		if e.Size != wantSizes[i] {
			t.Errorf("entry %d size = %d, want %d", i, e.Size, wantSizes[i])
		}
	}
	// The offsets must land on the media, not on the leaf index that precedes it.
	for i, e := range resolved.Entries {
		if e.Offset <= 0 || e.Offset >= int64(len(data)) {
			t.Errorf("entry %d offset %d is outside the file", i, e.Offset)
		}
	}
}

// A flat index resolves to itself, so the recursion costs nothing when there is
// no second level.
func TestResolveSIDX_FlatIndexIsUnchanged(t *testing.T) {
	entries := []mediatest.SIDXEntry{
		{Size: 100, Duration: 90000, StartsWithSAP: true},
		{Size: 200, Duration: 90000, StartsWithSAP: true},
	}
	data := mediatest.SIDX(0, 90000, 0, 0, entries)

	flat, err := ParseSIDX(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveSIDX(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Entries) != len(flat.Entries) {
		t.Fatalf("resolved %d entries, want the same %d", len(resolved.Entries), len(flat.Entries))
	}
	for i := range flat.Entries {
		if resolved.Entries[i] != flat.Entries[i] {
			t.Errorf("entry %d changed: %+v vs %+v", i, resolved.Entries[i], flat.Entries[i])
		}
	}
}

// A leaf that falls outside the bytes to hand cannot be followed. Reporting the
// index reference as if it were media would have the container parser handed an
// index box; leaving it out silently would understate the rendition. It is an
// error naming the situation.
func TestResolveSIDX_LeafBeyondTheBytesRead(t *testing.T) {
	leaves := [][]mediatest.SIDXEntry{{{Size: 400, Duration: 90000, StartsWithSAP: true}}}
	data := mediatest.HierarchicalSIDX(90000, leaves)

	// Cut the buffer just past the top-level index, so the leaf is out of reach.
	top, err := ParseSIDX(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	cut := int(top.Entries[0].Offset)
	if _, err := ResolveSIDX(data[:cut], 0); err == nil {
		t.Error("a leaf index outside the bytes read was resolved anyway")
	}
}

// The index tree is followed to a bounded depth. Without the bound a malformed
// index that references its own position would recurse until the stack gave out.
func TestResolveSIDX_NestingIsBounded(t *testing.T) {
	// An index whose single reference points back at itself.
	entries := []mediatest.SIDXEntry{{Size: 1000, Duration: 90000, Reference: true}}
	box := mediatest.SIDX(0, 90000, 0, 0, entries)
	// Place a copy of the index at the offset the reference points to, and repeat,
	// so each level finds another index rather than running out of bytes.
	data := box
	for i := 0; i < maxSIDXDepth+2; i++ {
		data = append(data, box...)
	}
	if _, err := ResolveSIDX(data, 0); err == nil {
		t.Fatal("a self-referencing index was followed without a depth limit")
	}
}

// A leaf that is itself malformed fails the whole resolution rather than being
// dropped, because a partial segment list understates the rendition.
func TestResolveSIDX_MalformedLeaf(t *testing.T) {
	leaves := [][]mediatest.SIDXEntry{{{Size: 300, Duration: 90000, StartsWithSAP: true}}}
	data := mediatest.HierarchicalSIDX(90000, leaves)

	top, err := ParseSIDX(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the leaf's box type so it is no longer an index.
	at := int(top.Entries[0].Offset)
	copy(data[at+4:at+8], []byte("junk"))

	if _, err := ResolveSIDX(data, 0); err == nil {
		t.Error("a malformed leaf index resolved anyway")
	}
}

// IndexStart is what tells an on-demand file where its initialisation segment
// ends, so it has to find the index behind whatever precedes it.
func TestIndexStart(t *testing.T) {
	entries := []mediatest.SIDXEntry{{Size: 100, Duration: 90000, StartsWithSAP: true}}
	idx := mediatest.SIDX(0, 90000, 0, 0, entries)

	head := mediatest.MP4Init(1, 90000, "video", 1280, 720)
	data := append(append([]byte{}, head...), idx...)
	got, ok := IndexStart(data)
	if !ok {
		t.Fatal("the index was not found behind the initialisation segment")
	}
	if got != len(head) {
		t.Errorf("index starts at %d, want %d — the init segment would be cut short", got, len(head))
	}

	if _, ok := IndexStart(head); ok {
		t.Error("an index was reported in bytes that carry none")
	}
}

// A root index that states no timescale takes it from its leaves — some
// packagers leave it zero at the top. Without that the durations come out as
// zero-divided nonsense.
func TestResolveSIDX_TimescaleFromTheLeafWhenTheRootStatesNone(t *testing.T) {
	leaf := mediatest.SIDX(0, 90000, 0, 0, []mediatest.SIDXEntry{
		{Size: 400, Duration: 45000, StartsWithSAP: true},
	})
	block := append(append([]byte{}, leaf...), make([]byte, 400)...)
	// A root with timescale 0 whose one reference points at that leaf.
	root := mediatest.SIDX(0, 0, 0, 0, []mediatest.SIDXEntry{
		{Size: uint32(len(block)), Duration: 45000, Reference: true},
	})

	idx, err := ResolveSIDX(append(root, block...), 0)
	if err != nil {
		t.Fatalf("ResolveSIDX: %v", err)
	}
	if idx.Timescale != 90000 {
		t.Errorf("timescale = %d, want 90000 taken from the leaf", idx.Timescale)
	}
	if got := idx.Entries[0].DurationSec(idx.Timescale); got != 0.5 {
		t.Errorf("duration = %vs, want 0.5s", got)
	}
}

// A tree whose leaves describe nothing but further references, and no media
// anywhere, has to be reported rather than yielding an empty success.
func TestResolveSIDX_TreeWithNoMediaAnywhere(t *testing.T) {
	// A leaf whose own single reference is also an index, pointing at itself.
	inner := mediatest.SIDX(0, 90000, 0, 0, []mediatest.SIDXEntry{
		{Size: 0, Duration: 90000, Reference: true},
	})
	data := append(append([]byte{}, inner...), inner...)
	if _, err := ResolveSIDX(data, 0); err == nil {
		t.Error("an index tree with no media anywhere resolved successfully")
	}
}
