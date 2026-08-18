package analyze

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// A DASH Period is how an ad break, a programme junction and a re-encode are all
// expressed, and it is the one place in a presentation where every timeline
// restarts. That makes a defect at a boundary invisible from either side: each
// period is internally perfect — its segments are continuous, its durations
// match, its ladder is well formed — and the join is the thing nobody looked at.
// Until now segcheck was one of the people not looking: each Period's
// Representations went into the rendition list side by side, as though they were
// competing rungs of one ladder rather than consecutive stretches of one
// presentation.
//
// Two things go wrong at a boundary, and they fail differently.
//
// The timeline: @presentationTimeOffset is the media time at which the period
// begins, and subtracting it is what maps a segment's own clock onto the
// presentation. A packager that restarts a period without restating it — or
// carries the media timeline across the boundary and leaves the offset at zero —
// puts every segment of that period a whole period-start away from where a seek
// expects to land. Playback from the beginning is unaffected, which is why this
// ships: it only breaks for the viewer who seeks or joins mid-presentation.
//
// The encoder: the boundary is where the packaging can legitimately change, and
// also where an unintended change hides. A resolution that changes across the
// join costs a player that cannot reconfigure mid-presentation; a codec that
// changes costs one that chose its decoder from the first period.

// periodOffsetTolerance is how far a period's media may sit from where the MPD
// places it before it is worth reporting, in seconds. Rounding a media timeline
// through a timescale costs a tick or two; half a second is well inside one
// segment and well outside any rounding.
const periodOffsetTolerance = 0.5

// periodShape is one period, with what the manifest claims about it and what its
// media turned out to say.
type periodShape struct {
	index int
	id    string
	start float64
	// offsetErr is how far the period's media sits from where the MPD's own
	// arithmetic puts it: the segment's timestamp, less the presentation-time
	// offset, less the offset into the period the manifest gives that segment.
	// Zero is a period that lands exactly where it says it does.
	offsetErr float64
	measured  bool
	// segDur is the declared length of the segment the error was measured on:
	// how much of a head start the period's first segment is allowed.
	segDur float64
	// rungs is the video shape of the period as the *media* turned out to be —
	// resolution per sampled rendition, sorted, so two periods compare as sets
	// rather than by a rendition order the manifest does not fix.
	rungs  []string
	codecs []string
	label  string
}

func checkPeriod(rends []*renditionData) []finding.Finding {
	periods := periodShapes(rends)
	if len(periods) < 2 {
		// One period is not a boundary, and HLS has none at all. Neither gains a
		// row in the report for a feature the stream does not use.
		return nil
	}

	var out []finding.Finding
	out = append(out, periodTimelineFindings(periods)...)
	out = append(out, periodEncoderFindings(periods)...)
	if len(out) == 0 {
		out = append(out, finding.Finding{
			Check: "period", Target: fmt.Sprintf("%d periods", len(periods)), Status: finding.OK,
			Message: fmt.Sprintf("%d periods join cleanly: each one's media lands where the MPD places it, at the same resolution and codec",
				len(periods)),
			Value: finding.Num(float64(len(periods) - 1)), Unit: "boundaries",
		})
	}
	return out
}

// periodShapes collects one entry per period, in presentation order.
func periodShapes(rends []*renditionData) []periodShape {
	byIndex := map[int]*periodShape{}
	var order []int
	for _, rd := range rends {
		if rd.err != nil || rd.r.PeriodID == "" {
			continue
		}
		p, ok := byIndex[rd.r.PeriodIndex]
		if !ok {
			p = &periodShape{
				index: rd.r.PeriodIndex,
				id:    rd.r.PeriodID,
				start: rd.r.PeriodStart,
				label: periodLabel(rd.r),
			}
			byIndex[rd.r.PeriodIndex] = p
			order = append(order, rd.r.PeriodIndex)
		}
		// Only video, and deliberately. A period boundary is cut on a video
		// segment because cutting there is what the boundary is for; an audio
		// grid has no such obligation and routinely does not divide by it —
		// nomor's own DASH-IF test vector puts 1.96198s AAC segments against a
		// 250s period, so the period's first audio segment begins 0.83s before
		// the period does and the player trims it. Reading audio here reported
		// that correctly built presentation as adrift.
		if rd.r.Kind != manifest.Video {
			continue
		}
		for _, sd := range rd.segs {
			t, ok := sd.info.Track(media.Video)
			if !ok || t.Width == 0 || t.Height == 0 {
				continue
			}
			p.rungs = append(p.rungs, fmt.Sprintf("%dx%d", t.Width, t.Height))
			if t.Codec != "" {
				p.codecs = append(p.codecs, t.Codec)
			}
			break
		}
		if e, dur, ok := renditionOffsetError(rd); ok && (!p.measured || math.Abs(e) > math.Abs(p.offsetErr)) {
			p.offsetErr, p.segDur, p.measured = e, dur, true
		}
	}
	sort.Ints(order)
	out := make([]periodShape, 0, len(order))
	for _, i := range order {
		p := byIndex[i]
		sort.Strings(p.rungs)
		sort.Strings(p.codecs)
		out = append(out, *p)
	}
	return out
}

// renditionOffsetError is how far this rendition's media sits from where the MPD
// puts it inside its period.
//
// Three claims meet here, and the segment's own timestamp is the one that
// settles them: @presentationTimeOffset says which media time the period begins
// at, the template says how far into the period this particular segment starts,
// and the tfdt says what the media actually counts. Subtract the first from the
// third and it has to equal the second.
//
// It is deliberately the smallest of the sampled segments' errors rather than
// the first. A single segment adrift is a hole in the timeline, which
// `continuity` and `duration` already report; a period placed wrongly is every
// one of its segments adrift by the same amount, and the minimum is what
// survives that distinction.
func renditionOffsetError(rd *renditionData) (float64, float64, bool) {
	best, dur, found := 0.0, 0.0, false
	for _, sd := range rd.segs {
		if !sd.parsed || !sd.seg.HasPeriodOffset {
			continue
		}
		t, ok := sd.info.Timeline()
		if !ok || t.Timescale == 0 {
			continue
		}
		e := toSec(t.MinPTS, t.Timescale) - rd.r.PresentationTimeOffset - sd.seg.PeriodOffset
		if !found || math.Abs(e) < math.Abs(best) {
			best, dur, found = e, sd.seg.Duration, true
		}
	}
	return best, dur, found
}

// periodTimelineFindings reports a period whose media does not sit where the MPD
// places it.
//
// The comparison is against the first period rather than against zero. A whole
// presentation whose media timeline runs from an epoch with no
// @presentationTimeOffset stated is a packaging habit players work around, and
// every period is offset alike so no viewer can tell; a period that drifts away
// from its neighbours is the one that breaks a seek across the boundary, and
// that is what this check is for.
func periodTimelineFindings(periods []periodShape) []finding.Finding {
	var base float64
	haveBase := false
	for _, p := range periods {
		if p.measured {
			base, haveBase = p.offsetErr, true
			break
		}
	}
	if !haveBase {
		return nil
	}
	var out []finding.Finding
	for _, p := range periods {
		if !p.measured {
			continue
		}
		drift := p.offsetErr - base
		// Asymmetric on purpose. Media that starts *after* the period does
		// leaves a hole nothing fills, and a tick of rounding is the only
		// latitude it gets. Media that starts before it is the ordinary case of
		// a segment straddling the boundary: the player trims the head, and it
		// can legitimately be a whole segment early — but no more, because past
		// that the period is showing media that belongs to the one before it.
		if drift <= periodOffsetTolerance && drift >= -(p.segDur+periodOffsetTolerance) {
			continue
		}
		out = append(out, finding.Finding{
			Check: "period", Target: p.label, Status: finding.BAD,
			Message: fmt.Sprintf("%s plays from presentation %.3fs, not the %.3fs the MPD places it at: its media sits %+.3fs from where @presentationTimeOffset maps it",
				p.label, p.start+drift, p.start, drift),
			Value: finding.Num(drift), Unit: "s",
			Hint: "playback from the start is unaffected; a viewer who seeks into this period, or joins the presentation there, lands in the wrong place",
		})
	}
	return out
}

// periodEncoderFindings reports the packaging changing across a boundary, as the
// segments turned out rather than as the manifest declared: a period whose media
// quietly changed shape declares the old one.
func periodEncoderFindings(periods []periodShape) []finding.Finding {
	var out []finding.Finding
	for i := 1; i < len(periods); i++ {
		prev, cur := periods[i-1], periods[i]
		if len(prev.codecs) > 0 && len(cur.codecs) > 0 && !sameStrings(prev.codecs, cur.codecs) {
			out = append(out, finding.Finding{
				Check: "period", Target: cur.label, Status: finding.BAD,
				Message: fmt.Sprintf("the video codec changes across the boundary into %s: %s becomes %s",
					cur.label, strings.Join(prev.codecs, ","), strings.Join(cur.codecs, ",")),
				Hint: "a player chose its decoder from the first period and stops at the join",
			})
			continue
		}
		if len(prev.rungs) > 0 && len(cur.rungs) > 0 && !sameStrings(prev.rungs, cur.rungs) {
			out = append(out, finding.Finding{
				Check: "period", Target: cur.label, Status: finding.WARN,
				Message: fmt.Sprintf("the video ladder changes across the boundary into %s: %s becomes %s",
					cur.label, strings.Join(prev.rungs, ","), strings.Join(cur.rungs, ",")),
				Hint: "legal in DASH, and a player that cannot reconfigure its decoder mid-presentation stalls at the join",
			})
		}
	}
	return out
}

// periodLabel names a period the way an operator reads the MPD: by its position,
// with the id the MPD gave it.
func periodLabel(r manifest.Rendition) string {
	return fmt.Sprintf("period %d %q", r.PeriodIndex+1, r.PeriodID)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
