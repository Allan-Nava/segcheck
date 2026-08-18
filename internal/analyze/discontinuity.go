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

// EXT-X-DISCONTINUITY is an instruction, not a description: it tells the player
// to throw away the decoder it has and start again. `continuity` reads it as a
// licence — a timeline jump here is expected rather than a defect — and takes the
// declaration on trust in the other direction, which leaves the half nobody looks
// at. A tag with nothing behind it is a reset performed for no reason, and a
// packager that emits one per segment costs a hitch per segment on media that is
// perfectly continuous. It is a defect that reads as a player bug, because
// nothing in the manifest and nothing in the media is individually wrong.
//
// RFC 8216 §4.3.2.3 is what keeps this honest. The tag signals a change in the
// *encoding* as well as in the timestamps — file format, track layout, codec —
// so a tag over a continuous timeline is only spurious when the media on either
// side is the same shape too. Reading timestamps alone would report a correct
// splice into differently encoded content.
//
// The other half is which timeline a segment sits on.
// EXT-X-DISCONTINUITY-SEQUENCE counts the discontinuities that have already
// rolled out of a live window, and every tag still in it adds one more. A player
// uses that number to decide whether two segments belong on the same clock, so
// two rungs carrying the same media at different numbers have put it on two
// different timelines — and the switch between them lands somewhere the other
// rung never was. The media is what settles it: the segments are the same
// moment, measurably, and the manifests disagree about which timeline it is.

func checkDiscontinuity(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	tolSec := opts.GapToleranceMS / 1000

	declared := 0
	for _, rd := range rends {
		if rd.err != nil || rd.r.Kind == manifest.Text || rd.r.Kind == manifest.IFrame {
			continue
		}
		segs := parsedSegs(rd)
		for i := 1; i < len(segs); i++ {
			prev, cur := segs[i-1], segs[i]
			if !cur.seg.Discontinuity {
				continue
			}
			declared++
			// Only an adjacent pair says anything: a fetch failure between the two
			// makes the comparison meaningless.
			if cur.seg.Sequence != prev.seg.Sequence+1 {
				continue
			}
			jumped, ok := timelineJumped(prev, cur, tolSec)
			if !ok || jumped {
				continue
			}
			// RFC 8216 makes the tag signal a change of encoding as well as of
			// timestamps, so a continuous timeline over media that really did
			// change shape is the tag doing its job.
			if encodingOf(prev.info) != encodingOf(cur.info) {
				continue
			}
			out = append(out, finding.Finding{
				Check: "discontinuity", Target: segLabel(rd, cur), Status: finding.BAD,
				Message: fmt.Sprintf("declares EXT-X-DISCONTINUITY and nothing discontinues: the timeline runs straight through it and the media is the same %s",
					encodingOf(cur.info)),
				Hint: "the player flushes and re-initialises its decoder here for nothing, which the viewer sees as a hitch",
			})
		}
	}

	out = append(out, discontinuitySequenceFindings(rends, tolSec)...)

	if declared > 0 && len(out) == 0 {
		out = append(out, finding.Finding{
			Check: "discontinuity", Target: "ladder", Status: finding.OK,
			Message: fmt.Sprintf("%d declared discontinuities, each with a real reset behind it and all rungs on the same timeline", declared),
			Value:   finding.Num(float64(declared)), Unit: "discontinuities",
		})
	}
	return out
}

// timelineJumped says whether the media timeline really breaks between two
// adjacent segments. The second return is false when it could not be measured,
// which is the protocol for staying quiet rather than guessing.
func timelineJumped(prev, cur segmentData, tolSec float64) (bool, bool) {
	pt, ok1 := prev.info.Timeline()
	ct, ok2 := cur.info.Timeline()
	if !ok1 || !ok2 || pt.Timescale == 0 || pt.Timescale != ct.Timescale {
		// A changed timescale is itself a discontinuity: the two segments do not
		// even count in the same units.
		return true, ok1 && ok2
	}
	prevDur, okd := pt.DurationSec()
	if !okd {
		return false, false
	}
	advance := float64(media.UnwrapDelta(pt.MinPTS, ct.MinPTS)) / float64(pt.Timescale)
	return math.Abs(advance-prevDur) > tolSec, true
}

// encodingOf is the shape a decoder is set up for: which tracks, in which
// codecs, at which size. It deliberately reuses trackShape's exclusion of
// signalling tracks — a splice information PID appears only in the segments
// carrying a cue, so counting it would make every ad break look like a
// reconfiguration.
func encodingOf(info media.SegmentInfo) string {
	parts := []string{trackShape(info)}
	var detail []string
	for _, t := range info.Tracks {
		if isSignallingTrack(t) {
			continue
		}
		d := string(t.Kind)
		if t.Codec != "" {
			d += " " + t.Codec
		}
		if t.Width > 0 && t.Height > 0 {
			d += fmt.Sprintf(" %dx%d", t.Width, t.Height)
		}
		detail = append(detail, d)
	}
	sort.Strings(detail)
	parts = append(parts, detail...)
	return strings.Join(parts, ", ")
}

// discontinuitySequenceFindings reports rungs that put the same media on
// different timelines.
func discontinuitySequenceFindings(rends []*renditionData, tolSec float64) []finding.Finding {
	type placement struct {
		label string
		seq   int
		start float64
	}
	bySegment := map[int][]placement{}
	for _, rd := range rends {
		if rd.err != nil || rd.r.Kind == manifest.Text || rd.r.Kind == manifest.IFrame {
			continue
		}
		for _, sd := range parsedSegs(rd) {
			t, ok := sd.info.Timeline()
			if !ok || t.Timescale == 0 {
				continue
			}
			bySegment[sd.seg.Sequence] = append(bySegment[sd.seg.Sequence], placement{
				label: rendLabel(rd.r),
				seq:   sd.seg.DiscontinuitySequence,
				start: toSec(t.MinPTS, t.Timescale),
			})
		}
	}

	var out []finding.Finding
	reported := map[string]bool{}
	for _, index := range sortedIntKeys(bySegment) {
		at := bySegment[index]
		if len(at) < 2 {
			continue
		}
		for i := 1; i < len(at); i++ {
			a, b := at[0], at[i]
			if a.seq == b.seq {
				continue
			}
			// Only when the media agrees that this is the same moment. Rungs whose
			// segments are at different times have a different defect, and
			// `alignment` reports that one.
			if math.Abs(a.start-b.start) > tolSec {
				continue
			}
			key := a.label + "\x00" + b.label
			if reported[key] {
				continue
			}
			reported[key] = true
			out = append(out, finding.Finding{
				Check: "discontinuity", Target: b.label, Status: finding.BAD,
				Message: fmt.Sprintf("%s and %s put the same media at %.3fs on different timelines: discontinuity sequence %d against %d",
					a.label, b.label, a.start, a.seq, b.seq),
				Value: finding.Num(float64(b.seq - a.seq)), Unit: "discontinuities",
				Hint: "a player switching between these rungs places the new segment on a timeline the old one was never on, and stalls",
			})
		}
	}
	return out
}
