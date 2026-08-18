package analyze

import (
	"fmt"
	"math"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
)

// EXT-X-PROGRAM-DATE-TIME is the only thing in an HLS playlist that claims a
// time in the real world: this segment starts at 14:03:22Z. Every other claim
// segcheck arbitrates is about a timeline relative to itself.
//
// It is load-bearing well beyond playback. Players seek by it, DVR windows are
// addressed by it, ad decisions are timed against it, and two operators
// comparing notes during an incident quote it at each other. Until this check
// it was parsed and believed, which is the one thing this tool exists not to do.
//
// The mapping is checked three ways: that it never goes backwards, that it
// advances at the same rate as the media it is stamped on, and — the one that
// matters most — that every rung of the ladder agrees. A ladder whose rungs
// disagree makes one seek land in two different places depending on which rung
// the player happened to be on.

// pdtPoint is one segment's claim: the wall clock the manifest stamped on it,
// and where its media really starts.
type pdtPoint struct {
	sd    segmentData
	at    time.Time
	start float64 // media start, in seconds on the rendition's own timeline
}

func checkPDT(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	tolSec := opts.GapToleranceMS / 1000

	byRendition := map[string][]pdtPoint{}
	var order []string
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		// A subtitle rendition's timestamps are a cue span, not a segment extent,
		// so the media start this check needs is not there to be read.
		if rd.r.Kind == manifest.Text {
			continue
		}
		points := pdtPoints(rd)
		if len(points) == 0 {
			continue
		}
		label := rendLabel(rd.r)
		order = append(order, label)
		byRendition[label] = points
		out = append(out, pdtFindings(rd, label, points, tolSec)...)
	}
	if len(order) == 0 {
		return nil // the playlist makes no wall-clock claim; there is nothing to check
	}

	out = append(out, pdtLadderFindings(rends, byRendition, order, tolSec)...)
	return out
}

// pdtPoints is the sampled segments that carry both a wall clock and a readable
// media start, in playlist order.
func pdtPoints(rd *renditionData) []pdtPoint {
	var out []pdtPoint
	for _, sd := range rd.segs {
		if !sd.seg.HasPDT || !sd.parsed {
			continue
		}
		t, ok := sd.info.Timeline()
		if !ok || t.Timescale == 0 {
			continue
		}
		out = append(out, pdtPoint{sd: sd, at: sd.seg.PDT, start: toSec(t.MinPTS, t.Timescale)})
	}
	return out
}

// pdtFindings judges one rendition's mapping against its own media.
func pdtFindings(rd *renditionData, label string, points []pdtPoint, tolSec float64) []finding.Finding {
	var out []finding.Finding
	if len(points) < 2 {
		return out
	}

	compared, drifted, backwards := 0, 0, 0
	var (
		worstDrift float64
		driftAt    pdtPoint
		driftFrom  pdtPoint
		backAt     pdtPoint
		backFrom   pdtPoint
	)
	for i := 1; i < len(points); i++ {
		prev, cur := points[i-1], points[i]
		if cur.sd.seg.Sequence != prev.sd.seg.Sequence+1 {
			continue // not adjacent in the playlist: the pair means nothing
		}
		// A wall clock that goes backwards is wrong whatever the media does, and
		// wrong even across a discontinuity: two moments in the stream then answer
		// to the same time, and a seek resolves to whichever the player finds first.
		if !cur.at.After(prev.at) {
			backwards++
			if backwards == 1 {
				backAt, backFrom = cur, prev
			}
			continue
		}
		// A declared discontinuity restarts the timeline, and the specification
		// requires a fresh EXT-X-PROGRAM-DATE-TIME after one. The rates on either
		// side of it are not comparable.
		if cur.sd.seg.Discontinuity {
			continue
		}
		compared++
		clock := cur.at.Sub(prev.at).Seconds()
		media := cur.start - prev.start
		drift := clock - media
		if math.Abs(drift) > tolSec {
			drifted++
			if math.Abs(drift) > math.Abs(worstDrift) {
				worstDrift, driftAt, driftFrom = drift, cur, prev
			}
		}
	}

	if backwards > 0 {
		out = append(out, finding.Finding{
			Check: "pdt", Target: segLabel(rd, backAt.sd), Status: finding.BAD,
			Message: fmt.Sprintf("EXT-X-PROGRAM-DATE-TIME goes backwards: stamped %s, after a segment stamped %s (%d of the boundaries checked)",
				backAt.at.UTC().Format(time.RFC3339Nano), backFrom.at.UTC().Format(time.RFC3339Nano), backwards),
			Value: finding.Num(backFrom.at.Sub(backAt.at).Seconds()), Unit: "s",
			Hint: "two moments in the stream answer to the same wall clock: a seek resolves to whichever the player finds first",
		})
	}
	if drifted > 0 {
		// The message quotes the pair rather than only their difference:
		// "5.000s where the media advances 2.000s" is diagnosable, "+3000ms" is not.
		out = append(out, finding.Finding{
			Check: "pdt", Target: segLabel(rd, driftAt.sd), Status: finding.BAD,
			Message: fmt.Sprintf("the wall clock advances %.3fs where the media advances %.3fs (%d of %d boundaries)",
				driftAt.at.Sub(driftFrom.at).Seconds(), driftAt.start-driftFrom.start, drifted, compared),
			Value: finding.Num(worstDrift * 1000), Unit: "ms",
			Hint: "seeking by wall clock lands somewhere the manifest never promised; the two drift further apart with every segment",
		})
	}
	if backwards == 0 && drifted == 0 && compared > 0 {
		out = append(out, finding.Finding{
			Check: "pdt", Target: label, Status: finding.OK,
			Message: fmt.Sprintf("the wall clock tracks the media across %d segment boundaries (tolerance %.0fms)", compared, tolSec*1000),
			Value:   finding.Num(float64(compared)), Unit: "boundaries",
		})
	}
	return out
}

// pdtLadderFindings compares the rungs with each other.
//
// The comparison is on the *offset* between wall clock and media, not on the
// wall clock alone: a ladder is allowed to put the same content at different
// points on each rung's own timeline, and only if their media agrees must their
// wall clocks agree too. So the offsets are compared only at segment indexes
// where the media already lines up — where it does not, `alignment` has already
// said so, and repeating it here as a second defect would double-count one bug.
func pdtLadderFindings(rends []*renditionData, byRendition map[string][]pdtPoint, order []string, tolSec float64) []finding.Finding {
	video := map[string]bool{}
	for _, rd := range rends {
		if rd.r.Kind == manifest.Video {
			video[rendLabel(rd.r)] = true
		}
	}
	type offset struct {
		label string
		start float64   // media start on this rung
		at    time.Time // the wall clock stamped on it
		off   float64   // at - start: where this rung puts the epoch
	}
	bySeq := map[int][]offset{}
	rungs := 0
	for _, label := range order {
		if !video[label] {
			continue
		}
		rungs++
		for _, p := range byRendition[label] {
			seq := p.sd.seg.Sequence
			bySeq[seq] = append(bySeq[seq], offset{
				label: label, start: p.start, at: p.at,
				off: float64(p.at.UnixNano())/1e9 - p.start,
			})
		}
	}
	if rungs < 2 {
		return nil
	}

	var out []finding.Finding
	compared, disagreed := 0, 0
	for _, seq := range sortedIntKeys(bySeq) {
		offs := bySeq[seq]
		if len(offs) < 2 {
			continue
		}
		// Only where the media already agrees: elsewhere the rungs are on
		// different timelines and `alignment` owns the finding.
		minStart, maxStart := offs[0].start, offs[0].start
		for _, o := range offs[1:] {
			minStart = math.Min(minStart, o.start)
			maxStart = math.Max(maxStart, o.start)
		}
		if maxStart-minStart > tolSec {
			continue
		}
		compared++

		lo, hi := offs[0], offs[0]
		for _, o := range offs[1:] {
			if o.off < lo.off {
				lo = o
			}
			if o.off > hi.off {
				hi = o
			}
		}
		spread := hi.off - lo.off
		if spread <= tolSec {
			continue
		}
		disagreed++
		out = append(out, finding.Finding{
			Check: "pdt", Target: fmt.Sprintf("seq %d", seq), Status: finding.BAD,
			Message: fmt.Sprintf("renditions disagree about the wall clock by %s on the same media: %s stamps it %s, %s stamps it %s",
				signedMS(spread), lo.label, lo.at.UTC().Format(time.RFC3339Nano), hi.label, hi.at.UTC().Format(time.RFC3339Nano)),
			Value: finding.Num(spread * 1000), Unit: "ms",
			Hint: "a seek to a wall-clock time lands in two different places depending on which rung the player is on",
		})
	}
	if compared > 0 && disagreed == 0 {
		out = append(out, finding.Finding{
			Check: "pdt", Target: "ladder", Status: finding.OK,
			Message: fmt.Sprintf("renditions agree on the wall clock at %d shared segment indexes (tolerance %.0fms)", compared, tolSec*1000),
			Value:   finding.Num(float64(compared)), Unit: "segments",
		})
	}
	return out
}
