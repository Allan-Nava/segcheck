package analyze

import (
	"context"
	"fmt"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// Single-file DASH representations (SC-19).
//
// A `SegmentBase` representation is one file. The MPD says where its index is but
// not where its subsegments are — only the `sidx` box does — so the expansion
// cannot happen in the manifest package, which is handed bytes and does no I/O.
// It happens here, where the fetch client is: the manifest layer states the range
// to ask for, and this layer asks and reads.
//
// Until this existed these renditions were reported unsupported, which was honest
// and useless: one line saying so, and every other check skipped for the whole
// rendition — no continuity, no duration, no resolution, no bitrate.

// resolveSegmentBase fetches and reads the index of every rendition that needs
// one, filling in its segments.
//
// A rendition whose index cannot be fetched or read is left with an error rather
// than with an empty segment list, because the two mean different things: one is
// a rendition nobody could look at, the other a rendition with nothing in it.
func resolveSegmentBase(ctx context.Context, c *fetch.Client, rends []*renditionData, opts Options) {
	for _, rd := range rends {
		if rd.err != nil || !rd.r.SingleFile || len(rd.segs) > 0 {
			continue
		}

		// Two shapes. SegmentBase@indexRange states where the index is, so one
		// range request gets exactly it. The on-demand profile states only a
		// BaseURL, so the index has to be found by reading the head of the file —
		// which is the commonest shape of single-file DASH in the wild.
		rng := indexProbeRange
		offset := int64(0)
		if rd.r.IndexRange != nil {
			rng = rd.r.IndexRange.Header()
			offset = rd.r.IndexRange.Offset
		}

		resp, err := c.Get(ctx, rd.r.URI, rng)
		if err != nil {
			rd.err = fmt.Errorf("segment index (%s) not fetched: %w", rng, err)
			continue
		}
		// The initialisation segment. With SegmentBase the MPD states it; under
		// the on-demand profile it is everything before the index — ftyp and moov
		// — and without it the fragments parse with no timescale and every
		// duration reads as zero.
		initRange := rd.r.InitRange
		if initRange == nil {
			if start, ok := media.IndexStart(resp.Body); ok && start > 0 {
				initRange = &manifest.ByteRange{Offset: offset, Length: int64(start)}
			}
		}

		segs, err := expandSegmentBase(rd.r.URI, resp.Body, offset, initRange, opts)
		if err != nil {
			if rd.r.IndexRange == nil {
				// The index was looked for rather than pointed at, so say that the
				// search came up empty rather than blaming the file: a moov larger
				// than the probe puts the index past what was read.
				rd.err = fmt.Errorf("no segment index found in the first %d bytes of the file: %w", indexProbeBytes, err)
				continue
			}
			rd.err = fmt.Errorf("segment index (%s) unreadable: %w", rng, err)
			continue
		}
		rd.segs = toSegmentData(sampleSegments(segs, false, opts))
	}
}

// indexProbeBytes is how much of a single-file representation to read when the
// MPD does not say where the index is. The on-demand layout puts ftyp, moov and
// sidx ahead of the media, so this has to clear the initialisation segment — a
// moov for a long asset with many samples is the thing that makes it large — while
// staying far below fetching the file itself, which is the whole asset.
const indexProbeBytes = 512 << 10

var indexProbeRange = fmt.Sprintf("bytes=0-%d", indexProbeBytes-1)

// expandSegmentBase turns an index into concrete segments. indexOffset is where
// the index sits in the file, because the offsets the index yields are absolute
// and anchoring them at zero would make every range wrong by the size of
// everything that precedes it.
func expandSegmentBase(uri string, index []byte, indexOffset int64, initRange *manifest.ByteRange, opts Options) ([]manifest.Segment, error) {
	// ResolveSIDX rather than ParseSIDX: real on-demand files carry a two-level
	// index, and stopping at the root finds only references to leaf indexes.
	idx, err := media.ResolveSIDX(index, indexOffset)
	if err != nil {
		return nil, err
	}

	var out []manifest.Segment
	for _, e := range idx.Entries {
		out = append(out, manifest.Segment{
			URI:      uri,
			Duration: e.DurationSec(idx.Timescale),
			Sequence: len(out) + 1,
			// One file, so the init segment is a range of it too — carried per
			// segment because that is the only place the fetcher looks for a
			// ranged initialisation.
			InitURI:   uri,
			InitRange: initRange,
			ByteRange: &manifest.ByteRange{Offset: e.Offset, Length: e.Size},
		})
	}
	// ResolveSIDX already fails when the tree describes no media, so out is never
	// empty here.
	return out, nil
}
