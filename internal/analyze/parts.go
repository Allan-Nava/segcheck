package analyze

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// Low-latency HLS describes the same media twice: once as segments, and once at
// a finer grain as the EXT-X-PARTs published before each segment exists. The
// parts are not slices of the segment a normal player fetches — a packager
// muxes both — so the two descriptions can disagree, and when they do, a viewer
// on the low-latency path gets different media from a viewer on the normal one.
//
// Nothing that reads only the manifest can see that, and nothing that fetches
// only the segments can either. Both halves have to be downloaded and compared,
// which is exactly the shape of check this tool exists for.

// partData is one sampled part: what the playlist said, what came back, and
// what the bytes turned out to be.
type partData struct {
	part manifest.Part
	// seg is the segment the part belongs to, needed for its initialisation
	// segment and to compare the part's timeline against the segment's own.
	seg      manifest.Segment
	info     media.SegmentInfo
	res      fetch.Response
	fetchErr error
	parseErr error
	parsed   bool
}

// selectParts is which parts a run downloads: every part of the newest
// opts.PartSegments sampled segments that have any.
//
// The cap is on segments rather than on parts because a part only means
// anything alongside its siblings — the question is whether they reconstruct
// their segment, and half a segment's parts cannot answer it. The newest are
// the ones a low-latency player is actually playing.
func selectParts(rd *renditionData, opts Options) []partData {
	if opts.PartSegments <= 0 {
		return nil
	}
	// Choose the segments walking back from the live edge, then read their parts
	// forwards. Reversing the flat list of parts instead would reverse each
	// segment's parts among themselves, and a part list out of order compares
	// the last part's start against the segment's first frame.
	var chosen []segmentData
	for i := len(rd.segs) - 1; i >= 0 && len(chosen) < opts.PartSegments; i-- {
		sd := rd.segs[i]
		if len(sd.seg.Parts) == 0 {
			continue
		}
		// A part of a full-segment-encrypted segment is ciphertext, and a byte
		// range of a CBC stream cannot be decrypted on its own anyway. The check
		// says it could not look rather than reporting noise.
		if isFullSegmentEncryption(sd.seg.KeyMethod) {
			continue
		}
		chosen = append([]segmentData{sd}, chosen...)
	}

	var out []partData
	for _, sd := range chosen {
		for _, p := range sd.seg.Parts {
			if p.Gap {
				continue // the packager is declaring the hole; there is nothing to fetch
			}
			out = append(out, partData{part: p, seg: sd.seg})
		}
	}
	return out
}

// samplePartsAll downloads and parses every selected part, bounded by the same
// concurrency as the segments.
func samplePartsAll(ctx context.Context, c *fetch.Client, rends []*renditionData, inits map[initRef]initResult, conc int) {
	// Bounded but never zero, the same as the segment fan-out: a zero-capacity
	// semaphore means the first send blocks forever and nothing is ever sampled.
	if conc <= 0 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, rd := range rends {
		for i := range rd.parts {
			wg.Add(1)
			go func(rd *renditionData, i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				pd := &rd.parts[i]
				rangeHeader := ""
				if pd.part.ByteRange != nil {
					rangeHeader = pd.part.ByteRange.Header()
				}
				resp, err := c.Get(ctx, pd.part.URI, rangeHeader)
				pd.res = resp
				if err != nil {
					pd.fetchErr = err
					return
				}
				info, perr := media.Parse(resp.Body, inits[initFor(rd, segmentData{seg: pd.seg})].body)
				if perr != nil {
					pd.parseErr = perr
					return
				}
				pd.info = info
				pd.parsed = true
			}(rd, i)
		}
	}
	wg.Wait()
}

// partSpan is where one part's media actually sits on the timeline.
type partSpan struct {
	pd    partData
	start float64
	end   float64
}

// checkParts compares the parts against the segment they make up, and against
// the two claims a part makes on its own: how long it is, and whether a player
// may start at it.
func checkParts(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	tolSec := opts.GapToleranceMS / 1000

	for _, rd := range rends {
		if rd.err != nil || !rd.hasParts {
			continue
		}
		label := rendLabel(rd.r)

		if opts.PartSegments <= 0 {
			out = append(out, finding.Finding{
				Check: "parts", Target: label, Status: finding.OK,
				Message: "playlist publishes EXT-X-PART, not checked (--parts 0)",
				Hint:    "raise --parts to compare the low-latency path against the segments",
			})
			continue
		}
		if len(rd.parts) == 0 {
			out = append(out, finding.Finding{
				Check: "parts", Target: label, Status: finding.OK,
				Message: "playlist publishes EXT-X-PART, but none of the sampled segments carried any to check",
				Hint:    "parts are aged out of the playlist soon after their segment completes; sample the live edge to see them",
			})
			continue
		}

		out = append(out, partFindings(rd, label, tolSec, opts)...)
	}
	return out
}

func partFindings(rd *renditionData, label string, tolSec float64, opts Options) []finding.Finding {
	var out []finding.Finding

	// Delivery first: a part that never arrived cannot be compared with anything,
	// and it is a hole in the low-latency path in its own right.
	var unfetched, unparsed []partData
	for _, pd := range rd.parts {
		switch {
		case pd.fetchErr != nil:
			unfetched = append(unfetched, pd)
		case pd.parseErr != nil:
			unparsed = append(unparsed, pd)
		}
	}
	if len(unfetched) > 0 {
		out = append(out, finding.Finding{
			Check: "parts", Target: partLabel(label, unfetched[0].part), Status: finding.BAD,
			Message: fmt.Sprintf("%d of %d parts not fetched: %v", len(unfetched), len(rd.parts), unfetched[0].fetchErr),
			Value:   finding.Num(float64(len(unfetched))), Unit: "parts",
			Hint: "a low-latency player stalls on a missing part where one fetching whole segments would not notice",
		})
	}
	if len(unparsed) > 0 {
		out = append(out, finding.Finding{
			Check: "parts", Target: partLabel(label, unparsed[0].part), Status: finding.BAD,
			Message: fmt.Sprintf("%d of %d parts are not readable media: %v", len(unparsed), len(rd.parts), unparsed[0].parseErr),
			Value:   finding.Num(float64(len(unparsed))), Unit: "parts",
		})
	}

	// A part declared INDEPENDENT invites a player to start playing at it. It is
	// the only claim a part makes about the bitstream, and the bitstream can
	// settle it.
	var notIndependent []partData
	for _, pd := range rd.parts {
		if !pd.parsed || !pd.part.Independent {
			continue
		}
		t, ok := pd.info.Track(media.Video)
		if !ok {
			continue
		}
		opens, known := t.StartsOnKeyframe()
		if known && !opens {
			notIndependent = append(notIndependent, pd)
		}
	}
	if len(notIndependent) > 0 {
		out = append(out, finding.Finding{
			Check: "parts", Target: partLabel(label, notIndependent[0].part), Status: finding.BAD,
			Message: fmt.Sprintf("%d parts declared INDEPENDENT do not open on a keyframe", len(notIndependent)),
			Value:   finding.Num(float64(len(notIndependent))), Unit: "parts",
			Hint: "a player joining at one of these has nothing to decode from: the join shows garbage or nothing at all",
		})
	}

	// Then the timeline, one segment at a time: the parts of a segment have to
	// be contiguous with each other and to cover the segment they make up.
	bySeq := map[int][]partSpan{}
	var order []int
	for _, pd := range rd.parts {
		if !pd.parsed {
			continue
		}
		t, ok := pd.info.Timeline()
		if !ok || t.Timescale == 0 {
			continue
		}
		dur, okd := t.DurationSec()
		if !okd {
			continue
		}
		start := toSec(t.MinPTS, t.Timescale)
		if _, seen := bySeq[pd.part.Sequence]; !seen {
			order = append(order, pd.part.Sequence)
		}
		bySeq[pd.part.Sequence] = append(bySeq[pd.part.Sequence], partSpan{pd: pd, start: start, end: start + dur})
	}
	if len(order) == 0 {
		if len(unfetched) == 0 && len(unparsed) == 0 {
			out = append(out, finding.Finding{
				Check: "parts", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%d parts fetched, but none states a timeline to compare with its segment", len(rd.parts)),
			})
		}
		return out
	}

	var (
		worstGap  float64
		gapSpan   partSpan
		gaps      int
		worstCov  float64
		covSpan   partSpan
		coverages int
		compared  int
	)
	for _, seq := range order {
		spans := bySeq[seq]
		for i := 1; i < len(spans); i++ {
			if spans[i].pd.part.Index != spans[i-1].pd.part.Index+1 {
				continue // not adjacent: a part in between was not readable
			}
			compared++
			drift := spans[i].start - spans[i-1].end
			if math.Abs(drift) > tolSec {
				gaps++
				if math.Abs(drift) > math.Abs(worstGap) {
					worstGap, gapSpan = drift, spans[i]
				}
			}
		}
		// And against the segment itself, which is the whole point: the two
		// descriptions of this media must agree at both ends.
		sd, ok := parsedSegment(rd, seq)
		if !ok {
			continue
		}
		st, ok := sd.info.Timeline()
		if !ok || st.Timescale == 0 {
			continue
		}
		segDur, okd := st.DurationSec()
		if !okd {
			continue
		}
		segStart := toSec(st.MinPTS, st.Timescale)
		compared++
		for _, drift := range []float64{spans[0].start - segStart, spans[len(spans)-1].end - (segStart + segDur)} {
			if math.Abs(drift) > tolSec {
				coverages++
				if math.Abs(drift) > math.Abs(worstCov) {
					worstCov, covSpan = drift, spans[0]
				}
			}
		}
	}

	if gaps > 0 {
		out = append(out, finding.Finding{
			Check: "parts", Target: partLabel(label, gapSpan.pd.part), Status: finding.BAD,
			Message: fmt.Sprintf("%s between consecutive parts: the previous one ends at %.3fs and this one starts at %.3fs (%d of %d boundaries)",
				gapKind(worstGap), gapSpan.start-worstGap, gapSpan.start, gaps, compared),
			Value: finding.Num(worstGap * 1000), Unit: "ms",
			Hint: "a low-latency player plays the parts back to back: this is a stutter or a repeated frame that whole-segment playback never shows",
		})
	}
	if coverages > 0 {
		out = append(out, finding.Finding{
			Check: "parts", Target: partLabel(label, covSpan.pd.part), Status: finding.BAD,
			Message: fmt.Sprintf("the parts do not cover their segment: %s, on %d of the %d boundaries checked",
				signedMS(worstCov), coverages, compared),
			Value: finding.Num(worstCov * 1000), Unit: "ms",
			Hint: "the parts and the segment are two descriptions of the same media, and they disagree: the low-latency and the normal path deliver different content",
		})
	}

	// A part longer than PART-TARGET breaks the latency budget the playlist
	// itself promised, and it is the measured length that settles it.
	if rd.partTarget > 0 {
		over, worst := 0, 0.0
		var worstPart manifest.Part
		for _, spans := range bySeq {
			for _, s := range spans {
				if d := s.end - s.start; d > rd.partTarget+tolSec {
					over++
					if d > worst {
						worst, worstPart = d, s.pd.part
					}
				}
			}
		}
		if over > 0 {
			out = append(out, finding.Finding{
				Check: "parts", Target: partLabel(label, worstPart), Status: finding.BAD,
				Message: fmt.Sprintf("%d parts are longer than the %.3fs PART-TARGET, worst %.3fs of media", over, rd.partTarget, worst),
				Value:   finding.Num(worst), Unit: "s",
				Hint: "PART-HOLD-BACK is derived from PART-TARGET, so a part that overruns it puts the player's target latency out of reach",
			})
		}
	}

	if len(out) == 0 {
		independent := 0
		for _, pd := range rd.parts {
			if pd.part.Independent {
				independent++
			}
		}
		out = append(out, finding.Finding{
			Check: "parts", Target: label, Status: finding.OK,
			Message: fmt.Sprintf("%d parts across %d segments reconstruct their segments (tolerance %.0fms); %d declared INDEPENDENT and open on a keyframe",
				len(rd.parts), len(order), tolSec*1000, independent),
			Value: finding.Num(float64(len(rd.parts))), Unit: "parts",
		})
	}
	return out
}

// parsedSegment is the sampled segment with this media sequence number, when it
// parsed.
func parsedSegment(rd *renditionData, seq int) (segmentData, bool) {
	for _, sd := range rd.segs {
		if sd.seg.Sequence == seq && sd.parsed {
			return sd, true
		}
	}
	return segmentData{}, false
}

func partLabel(rendition string, p manifest.Part) string {
	return fmt.Sprintf("%s seg %d part %d", rendition, p.Sequence, p.Index)
}

func gapKind(drift float64) string {
	if drift < 0 {
		return fmt.Sprintf("overlap of %s", signedMS(drift))
	}
	return fmt.Sprintf("gap of %s", signedMS(drift))
}
