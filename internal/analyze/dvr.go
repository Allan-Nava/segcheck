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
			return p
		}
		// Fetching is not enough. A CDN that serves an error page, or a truncated
		// object, for media it has aged out fails a scrub just as completely as a
		// 404 does, and only parsing tells them apart.
		info, perr := media.Parse(resp.Body, inits[initFor(rd, segmentData{seg: p.seg})].body)
		if perr != nil {
			p.parseErr = perr
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
			Message: fmt.Sprintf("the DVR window claims %.0fs and its oldest segment is not on the origin: %v", p.depth, p.fetchErr),
			Value:   finding.Num(p.depth), Unit: "s",
			Hint: "a viewer scrubbing to the back of the window gets nothing; the manifest's retention and the origin's disagree",
		}}
	case p.parseErr != nil:
		return []finding.Finding{{
			Check: "dvr", Target: p.label, Status: finding.BAD,
			Message: fmt.Sprintf("the DVR window claims %.0fs and its oldest segment is not readable media: %v", p.depth, p.parseErr),
			Value:   finding.Num(p.depth), Unit: "s",
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
