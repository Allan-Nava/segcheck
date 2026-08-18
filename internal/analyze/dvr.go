package analyze

import (
	"context"
	"fmt"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// A DVR window is a promise about the past: timeShiftBufferDepth in DASH, and
// in HLS the span of the playlist itself. It is the one promise nobody collects
// on purpose — a viewer only reaches the back of the window by scrubbing there,
// so a window that lies surfaces in a complaint rather than in monitoring, days
// after the origin's retention was changed.
//
// Every other check in this tool looks at the live edge, because that is what a
// joining viewer gets. This one looks at the far end for the same reason: it is
// the part of the stream nothing else ever touches.

// dvrProbe is the result of collecting the promise: the oldest segment the
// window claims, fetched and parsed.
type dvrProbe struct {
	label  string
	seg    manifest.Segment
	depth  float64 // how far back the window claims to reach, in seconds
	probed bool

	fetchErr error
	parseErr error
	parsed   bool

	// held is how far back the origin is now known to reach, in seconds, once the
	// promise turned out to be false. It is a lower bound: the bisection lands on
	// a probe point and the real boundary is somewhere before it. measured is
	// false when the bisection could not run — there were no probe points to work
	// with — and then the finding says the window is short without inventing a
	// number for how short.
	held     float64
	measured bool
	// probeFetches is how many extra requests the bisection cost, which is worth
	// saying: it is spent only on a stream already known to be broken.
	probeFetches int
}

// probeDVR fetches the oldest segment the window still promises, for the top
// video rendition only. One segment per run: the question is whether the origin
// honours its retention, and every rung of a ladder is served by the same
// retention policy.
func probeDVR(ctx context.Context, c *fetch.Client, pl manifest.Playlist, rends []*renditionData, inits map[initRef]initResult) *dvrProbe {
	if !pl.Live {
		return nil
	}
	for i := len(rends) - 1; i >= 0; i-- {
		rd := rends[i]
		if rd.err != nil || rd.r.Kind != manifest.Video || rd.oldest == nil || rd.window <= 0 {
			continue
		}
		p := &dvrProbe{label: rendLabel(rd.r), seg: *rd.oldest, depth: rd.window, probed: true}

		// Already sampled — a VOD-style run from the head of the playlist covers
		// it — so there is nothing to ask twice.
		for _, sd := range rd.segs {
			if sd.seg.URI == p.seg.URI {
				p.fetchErr, p.parseErr, p.parsed = sd.fetchErr, sd.parseErr, sd.parsed
				return p
			}
		}

		rangeHeader := ""
		if p.seg.ByteRange != nil {
			rangeHeader = p.seg.ByteRange.Header()
		}
		resp, err := c.Get(ctx, p.seg.URI, rangeHeader)
		if err != nil {
			p.fetchErr = err
			measureRetention(ctx, c, rd, inits, p)
			return p
		}
		// Fetching is not enough. A CDN that serves an error page, or a truncated
		// object, for media it has aged out fails a scrub just as completely as a
		// 404 does, and only parsing tells them apart.
		info, perr := media.Parse(resp.Body, inits[initFor(rd, segmentData{seg: p.seg})].body)
		if perr != nil {
			p.parseErr = perr
			measureRetention(ctx, c, rd, inits, p)
			return p
		}
		_ = info
		p.parsed = true
		return p
	}
	return nil
}

func checkDVR(p *dvrProbe) []finding.Finding {
	if p == nil || !p.probed {
		return nil
	}
	switch {
	case p.fetchErr != nil:
		return []finding.Finding{{
			Check: "dvr", Target: p.label, Status: finding.BAD,
			Message: fmt.Sprintf("the DVR window claims %.0fs and its oldest segment is not on the origin: %v%s",
				p.depth, p.fetchErr, heldSuffix(p)),
			Value: heldValue(p), Unit: "s",
			Hint: "a viewer scrubbing to the back of the window gets nothing; the manifest's retention and the origin's disagree",
		}}
	case p.parseErr != nil:
		return []finding.Finding{{
			Check: "dvr", Target: p.label, Status: finding.BAD,
			Message: fmt.Sprintf("the DVR window claims %.0fs and its oldest segment is not readable media: %v%s",
				p.depth, p.parseErr, heldSuffix(p)),
			Value: heldValue(p), Unit: "s",
			Hint: "the origin answered, so nothing 404s and nothing alerts, but a scrub back to here plays an error page",
		}}
	case p.parsed:
		return []finding.Finding{{
			Check: "dvr", Target: p.label, Status: finding.OK,
			Message: fmt.Sprintf("the oldest segment in the %.0fs DVR window still fetches and parses", p.depth),
			Value:   finding.Num(p.depth), Unit: "s",
		}}
	}
	return nil
}

// Saying the window is short is only half an answer. The number an operator
// changes a retention setting with is how much of it is really there, and it is
// not in the manifest — the manifest is the thing that just turned out to be
// wrong. Finding it means asking for segments between the oldest one claimed and
// the newest one listed, and in DASH those do not appear in the manifest at all:
// the template has to be evaluated at indices nobody asked for, which is what
// Rendition.WindowProbes is for.
//
// It is a bisection because the boundary is monotone — an origin that has aged
// out a segment has aged out everything older — so four requests place it within
// an eighth of the window, and they are only ever spent on a stream already
// known to be broken.

// maxRetentionProbes bounds what the measurement costs. Four halvings of a
// sixteen-point ladder land on the exact probe point.
const maxRetentionProbes = 4

func measureRetention(ctx context.Context, c *fetch.Client, rd *renditionData, inits map[initRef]initResult, p *dvrProbe) {
	probes := rd.probes
	if len(probes) < 2 {
		return
	}
	// The newest probe is the live edge, which the run has already fetched: if
	// even that is gone the stream is broken in a way `fetch` reports, and there
	// is no retention to measure.
	lo, hi := 0, len(probes)-1
	found := -1
	for i := 0; i < maxRetentionProbes && lo <= hi; i++ {
		mid := (lo + hi) / 2
		p.probeFetches++
		if retentionHolds(ctx, c, rd, inits, probes[mid]) {
			found = mid
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	if found < 0 {
		// Nothing in the ladder answered. The origin holds no more than the gap
		// between the last probe that failed and the edge, and reporting that as
		// a depth would overstate what was actually shown, so it is reported as
		// the floor it is: nothing behind the edge was found.
		p.held, p.measured = 0, true
		return
	}
	p.held, p.measured = probeSpan(probes, found), true
}

// probeSpan is how much media lies between a probe point and the live edge,
// which is how far back the origin is now known to reach.
//
// It is a lower bound and reported as one. The bisection lands on the oldest
// probe point that answered, and the real boundary is somewhere between that
// point and the one before it — so the number understates what the origin holds
// and never overstates it. Understating is the only safe direction: an operator
// setting retention from an overstated figure would shorten a window that was
// already too short.
func probeSpan(probes []manifest.Segment, from int) float64 {
	if from >= len(probes)-1 {
		return probes[len(probes)-1].Duration
	}
	span := 0.0
	for i := from; i < len(probes)-1; i++ {
		span += probeGap(probes, i)
	}
	return span
}

// probeGap is the media between two adjacent probe points. The probes are
// samples of the window rather than every segment in it, so the distance is the
// number of segments between their sequence numbers times what a segment lasts —
// which for an HLS playlist, whose probes *are* every segment, is one step and
// that segment's own duration.
func probeGap(probes []manifest.Segment, i int) float64 {
	steps := probes[i+1].Sequence - probes[i].Sequence
	if steps < 1 {
		steps = 1
	}
	return float64(steps) * probes[i].Duration
}

// retentionHolds asks whether one probe point is really on the origin. It is the
// same two questions the DVR probe itself asks, for the same reason: a CDN
// serving an error page for media it has aged out fails a scrub exactly as
// completely as a 404 does.
func retentionHolds(ctx context.Context, c *fetch.Client, rd *renditionData, inits map[initRef]initResult, seg manifest.Segment) bool {
	rangeHeader := ""
	if seg.ByteRange != nil {
		rangeHeader = seg.ByteRange.Header()
	}
	resp, err := c.Get(ctx, seg.URI, rangeHeader)
	if err != nil {
		return false
	}
	_, perr := media.Parse(resp.Body, inits[initFor(rd, segmentData{seg: seg})].body)
	return perr == nil
}

// heldSuffix says what was actually found, and says nothing when nothing was.
func heldSuffix(p *dvrProbe) string {
	if !p.measured {
		return ""
	}
	if p.held <= 0 {
		return fmt.Sprintf(" — %d probes found nothing behind the live edge at all", p.probeFetches)
	}
	return fmt.Sprintf(" — the origin holds at least %.0fs of it, found in %d probes", p.held, p.probeFetches)
}

// heldValue is the measurement machine consumers read: what is really there once
// it has been measured, and the claim that was disproved when it has not.
func heldValue(p *dvrProbe) *float64 {
	if p.measured {
		return finding.Num(p.held)
	}
	return finding.Num(p.depth)
}
