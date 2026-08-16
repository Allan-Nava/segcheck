package media

import (
	"fmt"
)

// The segment index of a single-file DASH representation (SC-19).
//
// A `SegmentBase` representation is one file, and nothing in the MPD says where
// its subsegments begin or end — only the `sidx` box does. Without reading it a
// checker cannot sample such a stream at all, which is why these renditions used
// to come back marked unsupported: an honest answer, but one that skipped every
// check for the whole rendition.
//
// The arithmetic is the part to get right. Each reference states a *size*, not a
// position, so the offsets are cumulative from a base that is itself the end of
// the index box plus `first_offset`. A reader that mismeasures the box header, or
// drops `first_offset` because it is usually zero, puts every boundary in the
// wrong place and then fetches byte ranges that straddle two subsegments.

// SIDXEntry is one reference: where a subsegment sits in the file and how long it
// runs for.
type SIDXEntry struct {
	// Offset is absolute within the file, not relative to the index.
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
	// Duration is in the index's timescale.
	Duration uint32 `json:"duration"`
	// StartsWithSAP marks a subsegment a decoder can be started at — the same
	// property the `keyframe` check reads out of the bitstream, stated here by the
	// container instead.
	StartsWithSAP bool `json:"starts_with_sap,omitempty"`
	// IsIndex marks a reference that points at another index rather than at
	// media. Following one as if it were a subsegment fetches an index box and
	// tries to parse it as a fragment.
	IsIndex bool `json:"is_index,omitempty"`
}

// DurationSec converts the entry's duration into seconds.
func (e SIDXEntry) DurationSec(timescale uint32) float64 {
	if timescale == 0 {
		return 0
	}
	return float64(e.Duration) / float64(timescale)
}

// SIDX is a parsed segment index.
type SIDX struct {
	Timescale uint32      `json:"timescale"`
	Entries   []SIDXEntry `json:"entries"`
}

// maxSIDXEntries bounds how many references one index may declare. A malformed
// count would otherwise drive an allocation from the input.
const maxSIDXEntries = 20000

// ParseSIDX reads a segment index. indexOffset is where the box sits in the file,
// because @indexRange addresses it there and the subsegment offsets it yields are
// absolute — anchoring them at zero would make every fetched range wrong by the
// size of everything preceding the index.
func ParseSIDX(data []byte, indexOffset int64) (SIDX, error) {
	var out SIDX

	sidx, _, indexEnd, ok := findBoxSpan(data, "sidx")
	if !ok {
		return out, fmt.Errorf("no sidx box: a SegmentBase representation must carry one to be addressable")
	}
	// version+flags, reference_ID, timescale.
	if len(sidx) < 12 {
		return out, fmt.Errorf("sidx too short to hold its fixed fields")
	}
	version := sidx[0]
	out.Timescale = be32(sidx[8:])

	off := 12
	var firstOffset uint64
	if version == 0 {
		if len(sidx) < off+8 {
			return out, fmt.Errorf("sidx version 0 too short for its 32-bit time fields")
		}
		firstOffset = uint64(be32(sidx[off+4:]))
		off += 8
	} else {
		// version 1 widens earliest_presentation_time and first_offset to 64 bits,
		// the same widening tfdt has. Reading it at the version 0 offsets loses
		// eight bytes and every reference after it comes from the wrong place.
		if len(sidx) < off+16 {
			return out, fmt.Errorf("sidx version 1 too short for its 64-bit time fields")
		}
		firstOffset = be64(sidx[off+8:])
		off += 16
	}

	if len(sidx) < off+4 {
		return out, fmt.Errorf("sidx too short to hold its reference count")
	}
	count := int(be16(sidx[off+2:]))
	off += 4

	// A count the box cannot hold is a malformed index: trust the bytes present.
	if avail := (len(sidx) - off) / 12; count > avail {
		count = avail
	}
	if count > maxSIDXEntries {
		count = maxSIDXEntries
	}
	if count <= 0 {
		return out, fmt.Errorf("sidx declares no usable reference")
	}

	// The first subsegment begins where the index box ends, plus first_offset,
	// measured from wherever the index itself sits in the file.
	pos := indexOffset + int64(indexEnd) + int64(firstOffset)

	for i := 0; i < count; i++ {
		p := off + i*12
		ref := be32(sidx[p:])
		e := SIDXEntry{
			Offset:        pos,
			Size:          int64(ref & 0x7FFFFFFF),
			IsIndex:       ref&0x80000000 != 0,
			Duration:      be32(sidx[p+4:]),
			StartsWithSAP: be32(sidx[p+8:])&0x80000000 != 0,
		}
		out.Entries = append(out.Entries, e)
		pos += e.Size
	}
	return out, nil
}

// findBoxSpan returns the payload of the first box of type typ and where it
// starts and ends within data.
//
// Both ends matter and for different reasons: the subsegment arithmetic is
// measured from where the index *ends*, and for an on-demand file the index's
// *start* is also the end of the initialisation segment. boxesIn does not report
// positions, and reconstructing them from payload lengths would be wrong for a
// 64-bit box header, so the walk is done here against the same rules.
func findBoxSpan(data []byte, typ string) (payload []byte, start, end int, ok bool) {
	for off := 0; off+8 <= len(data); {
		size := int(be32(data[off:]))
		header := 8
		switch size {
		case 1:
			if off+16 > len(data) {
				return nil, 0, 0, false
			}
			size = int(be64(data[off+8:]))
			header = 16
		case 0:
			size = len(data) - off
		}
		if size < header || off+size > len(data) {
			return nil, 0, 0, false
		}
		if string(data[off+4:off+8]) == typ {
			return data[off+header : off+size], off, off + size, true
		}
		off += size
	}
	return nil, 0, 0, false
}

// maxSIDXDepth bounds how far the index tree is followed. Two levels is what real
// on-demand files use; the bound is there so a malformed index that references
// itself cannot loop.
const maxSIDXDepth = 4

// ResolveSIDX reads an index and follows any references that point at further
// indexes, returning only the media subsegments.
//
// Real on-demand DASH files are built this way — Sony's DASH-IF test vector has a
// top-level index of five references, every one of them pointing at a leaf index
// that describes its portion — so a reader that stops at the first level sees no
// media references at all and concludes the file describes nothing. That is a
// wrong answer shaped exactly like a broken stream.
//
// data must cover the leaves as well as the root: they are read from it rather
// than fetched, since the head of an on-demand file holds the whole index tree.
func ResolveSIDX(data []byte, indexOffset int64) (SIDX, error) {
	return resolveSIDX(data, indexOffset, indexOffset, 0)
}

// bufBase is where data begins in the file, so an entry's absolute offset can be
// turned back into a position within the bytes to hand.
func resolveSIDX(data []byte, bufBase, indexOffset int64, depth int) (SIDX, error) {
	if depth >= maxSIDXDepth {
		return SIDX{}, fmt.Errorf("segment index nested more than %d levels deep", maxSIDXDepth)
	}
	idx, err := ParseSIDX(data[indexOffset-bufBase:], indexOffset)
	if err != nil {
		return SIDX{}, err
	}

	out := SIDX{Timescale: idx.Timescale}
	media := 0
	for _, e := range idx.Entries {
		if !e.IsIndex {
			out.Entries = append(out.Entries, e)
			media++
			continue
		}
		// The reference points at another index, which sits at the start of the
		// range it describes.
		rel := e.Offset - bufBase
		if rel < 0 || rel >= int64(len(data)) {
			return SIDX{}, fmt.Errorf("index reference at offset %d is outside the %d bytes read", e.Offset, len(data))
		}
		leaf, err := resolveSIDX(data, bufBase, e.Offset, depth+1)
		if err != nil {
			return SIDX{}, err
		}
		// A root index need not restate the timescale its leaves carry.
		if out.Timescale == 0 {
			out.Timescale = leaf.Timescale
		}
		out.Entries = append(out.Entries, leaf.Entries...)
		media += len(leaf.Entries)
	}
	if media == 0 {
		return SIDX{}, fmt.Errorf("segment index describes no media subsegment")
	}
	return out, nil
}

// IndexStart returns where the first `sidx` box begins in data.
//
// For a single-file representation under the on-demand profile that position is
// also the end of the initialisation segment: everything before the index is ftyp
// and moov, which is exactly what a player needs before it can decode any
// subsegment. Without it the fragments parse but carry no timescale, and every
// duration then reads as zero — which the duration check reports as the media
// being 100% shorter than declared.
func IndexStart(data []byte) (int, bool) {
	_, start, _, ok := findBoxSpan(data, "sidx")
	return start, ok
}
