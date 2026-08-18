package analyze

import (
	"fmt"
	"math"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// The measurable subset of Apple's HLS Authoring Specification for Apple
// Devices: the rules that constrain the media rather than the manifest, which
// is to say the ones segcheck can settle and a manifest linter cannot.
//
// Each rule quotes the requirement in words and puts the measured value beside
// the limit, because "fails rule 3.4" without a number is unactionable — and
// because Apple renumbers the document between revisions, so a section number
// is the least durable way to identify a requirement.

func init() {
	// Assigned here rather than in the literal so the rules can refer to helpers
	// declared later in the file without an initialisation cycle.
	appleRules = []profileRule{
		// peak segment bit rate within 200% of the average
		{id: "apple:peak-vs-average", run: applePeakVsAverage},
		// segment durations consistent within a rendition and across the ladder
		{id: "apple:segment-duration", run: appleSegmentDuration},
		// every segment begins with an IDR
		{id: "apple:idr-per-segment", run: appleIDRPerSegment},
		// average bit rate near the tier the resolution implies
		{id: "apple:bitrate-tier", run: appleBitrateTier},
		// frame rate constant within a rendition and shared across the ladder
		{id: "apple:frame-rate", run: appleFrameRate},
	}
}

// applePeakBudget is the specification's own number: the peak bit rate should
// be no more than 200% of the average.
const applePeakBudget = 2.0

func applePeakVsAverage(ctx profileContext) []finding.Finding {
	var out []finding.Finding
	for _, rd := range ctx.rends {
		if rd.err != nil || rd.r.Kind == manifest.Text {
			continue
		}
		avg, peak, peakSeg, ok := measuredBitrates(rd)
		if !ok {
			continue
		}
		ratio := peak / avg
		if ratio > applePeakBudget {
			out = append(out, finding.Finding{
				Target: segLabel(rd, peakSeg), Status: finding.WARN,
				Message: fmt.Sprintf("segment peaks at %s, %.0f%% of the %s average — the specification asks for no more than 200%%",
					humanBitrate(peak), ratio*100, humanBitrate(avg)),
				Value: finding.Num(ratio * 100), Unit: "%",
				Hint: "a player whose connection is sized for the average stalls on this segment; the buffer has to absorb the whole excess in one segment time",
			})
			continue
		}
		out = append(out, finding.Finding{
			Target: rendLabel(rd.r), Status: finding.OK,
			Message: fmt.Sprintf("peak %s is %.0f%% of the %s average, within the 200%% the specification asks for",
				humanBitrate(peak), ratio*100, humanBitrate(avg)),
			Value: finding.Num(ratio * 100), Unit: "%",
		})
	}
	return out
}

// appleSegmentDurationTolerancePct is how far a segment's measured duration may
// sit from its rendition's usual one. The specification asks for consistent
// durations without stating a number, so this band is segcheck's and the hint
// says so; it is wide enough that rounding a 2.002s segment to 2s never fires.
const appleSegmentDurationTolerancePct = 10.0

func appleSegmentDuration(ctx profileContext) []finding.Finding {
	var out []finding.Finding
	for _, rd := range ctx.rends {
		if rd.err != nil || rd.r.Kind == manifest.Text {
			continue
		}
		durs := measuredDurations(rd)
		// The last segment of a VOD presentation is legitimately short — the
		// content ends where it ends — so it is excluded rather than reported.
		if !rd.live && len(durs) > 1 && durs[len(durs)-1] < durs[0] {
			durs = durs[:len(durs)-1]
		}
		if len(durs) < 2 {
			continue
		}
		typical := medianFloat(durs)
		if typical <= 0 {
			continue
		}
		worst, worstPct := 0.0, 0.0
		for _, d := range durs {
			if pct := math.Abs(d-typical) / typical * 100; pct > worstPct {
				worst, worstPct = d, pct
			}
		}
		label := rendLabel(rd.r)
		if worstPct > appleSegmentDurationTolerancePct {
			out = append(out, finding.Finding{
				Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("segment durations vary: %.3fs against a usual %.3fs (%+.0f%%) across %d segments — the specification asks for consistent durations",
					worst, typical, (worst/typical-1)*100, len(durs)),
				Value: finding.Num(worstPct), Unit: "%",
				Hint: "a player's buffer maths assumes segments are the length they usually are; the tolerance here is segcheck's, since the specification states none",
			})
			continue
		}
		out = append(out, finding.Finding{
			Target: label, Status: finding.OK,
			Message: fmt.Sprintf("%d segments all within %.0f%% of %.3fs", len(durs), appleSegmentDurationTolerancePct, typical),
			Value:   finding.Num(typical), Unit: "s",
		})
	}

	// And across the ladder: a rung whose segments are a different length from
	// its neighbours' cannot be switched into cleanly, however consistent it is
	// with itself.
	type rung struct {
		label   string
		typical float64
	}
	var rungs []rung
	for _, rd := range ctx.rends {
		if rd.err != nil || rd.r.Kind != manifest.Video {
			continue
		}
		if durs := measuredDurations(rd); len(durs) > 0 {
			rungs = append(rungs, rung{rendLabel(rd.r), medianFloat(durs)})
		}
	}
	if len(rungs) >= 2 {
		lo, hi := rungs[0], rungs[0]
		for _, r := range rungs[1:] {
			if r.typical < lo.typical {
				lo = r
			}
			if r.typical > hi.typical {
				hi = r
			}
		}
		if lo.typical > 0 && (hi.typical-lo.typical)/lo.typical*100 > appleSegmentDurationTolerancePct {
			out = append(out, finding.Finding{
				Target: "ladder", Status: finding.WARN,
				Message: fmt.Sprintf("rungs use different segment lengths: %s at %.3fs against %s at %.3fs",
					lo.label, lo.typical, hi.label, hi.typical),
				Value: finding.Num((hi.typical - lo.typical) * 1000), Unit: "ms",
				Hint: "segment boundaries have to line up across the ladder for an ABR switch to be seamless",
			})
		}
	}
	return out
}

// appleIDRPerSegment reports only what segcheck can stand behind.
//
// The specification requires every segment to begin with an IDR, but "it does
// not" means two different things depending on where the answer came from. In
// fMP4 the trun's first-sample flags state it outright and there is nothing to
// argue with. In MPEG-TS the answer comes from walking the bitstream in decode
// order, where with B-frames the first coded picture need not be the first
// presented one and the reader may simply not have reached the IDR — which is
// why the `keyframe` check has always treated that gently, and why escalating
// it here turned Apple's own reference stream into a conformance failure.
//
// So: a container that states the first sample is not a sync sample fails, a
// completed walk that found no random access point at all fails, and a walk
// that found one somewhere other than first is reported as unsettled.
func appleIDRPerSegment(ctx profileContext) []finding.Finding {
	var out []finding.Finding
	for _, rd := range ctx.rends {
		if rd.err != nil || rd.initErr != nil || rd.r.Kind != manifest.Video {
			continue
		}
		verified, bad, unsettled := 0, 0, 0
		var first segmentData
		for _, sd := range parsedSegs(rd) {
			t, ok := sd.info.Track(media.Video)
			if !ok || bitstreamOpaque(sd) {
				continue
			}
			opens, stated := t.OpensOnStatedKeyframe()
			if stated {
				verified++
				if !opens {
					if bad == 0 {
						first = sd
					}
					bad++
				}
				continue
			}
			starts, known := t.StartsOnKeyframe()
			if !known {
				continue
			}
			verified++
			if starts {
				continue
			}
			if present, scanned := t.ContainsKeyframe(); scanned && !present {
				if bad == 0 {
					first = sd
				}
				bad++
				continue
			}
			unsettled++
		}
		if verified == 0 {
			continue
		}
		label := rendLabel(rd.r)
		if bad > 0 {
			out = append(out, finding.Finding{
				Target: segLabel(rd, first), Status: finding.WARN,
				Message: fmt.Sprintf("%d of %d segments do not begin with an IDR — the specification requires every segment to begin with one", bad, verified),
				Value:   finding.Num(float64(bad)), Unit: "segments",
				Hint: "a segment that does not open on an IDR cannot be switched into: a decoder arriving there has no reference picture",
			})
			continue
		}
		if unsettled > 0 {
			out = append(out, finding.Finding{
				Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%d of %d segments carry a random access point but not as the first coded picture; in decode order that is not settled evidence either way", unsettled, verified),
				Value:   finding.Num(float64(unsettled)), Unit: "segments",
			})
			continue
		}
		out = append(out, finding.Finding{
			Target: label, Status: finding.OK,
			Message: fmt.Sprintf("all %d verified segments begin with an IDR", verified),
			Value:   finding.Num(float64(verified)), Unit: "segments",
		})
	}
	return out
}

// appleTier is one row of the specification's recommended bit-rate ladder:
// a resolution and the average bit rate Apple recommends for it.
type appleTier struct {
	width, height int
	kbps          float64
}

// appleAVCTiers is the H.264/AVC SDR table from the specification's bit-rate
// variants section. HEVC and HDR have tables of their own and are deliberately
// absent: comparing an HEVC rung against an H.264 recommendation would report
// every efficient encode as under-bitrate.
var appleAVCTiers = []appleTier{
	{416, 234, 145},
	{640, 360, 365},
	{768, 432, 730},
	{960, 540, 2000},
	{1280, 720, 3000},
	{1920, 1080, 6000},
	{2560, 1440, 9000},
	{3840, 2160, 16000},
}

// appleTierBand is how far from the recommendation a rung may sit before it is
// worth saying so. The specification publishes recommended averages, not a
// range, so this band is segcheck's own and every finding says so: a rung at
// half the recommended rate is a deliberate choice often enough that a narrow
// band would be noise, and one at a twentieth is a misconfigured encoder.
const appleTierBand = 2.0

func appleBitrateTier(ctx profileContext) []finding.Finding {
	var out []finding.Finding
	for _, rd := range ctx.rends {
		if rd.err != nil || rd.r.Kind != manifest.Video {
			continue
		}
		avg, _, _, ok := measuredBitrates(rd)
		if !ok {
			continue
		}
		w, h := codedSize(rd)
		if w == 0 || h == 0 {
			w, h = rd.r.Width, rd.r.Height
		}
		if w == 0 || h == 0 {
			continue // nothing states a resolution, so no tier is implied
		}
		label := rendLabel(rd.r)

		// The table is H.264 SDR. A rung in any other codec is measured against
		// nothing rather than against the wrong number.
		if codec, ok := videoCodecOf(rd); ok && codec != "h264" {
			out = append(out, finding.Finding{
				Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%s at %s: the specification's bit-rate table covers H.264 only, so there is no tier to compare against",
					humanBitrate(avg), codec),
				Value: finding.Num(avg), Unit: "bps",
			})
			continue
		}

		tier, ok := nearestAppleTier(w, h)
		if !ok {
			continue
		}
		want := tier.kbps * 1000
		ratio := avg / want
		if ratio > appleTierBand || ratio < 1/appleTierBand {
			out = append(out, finding.Finding{
				Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("measured %s against the %.0f kbps the specification recommends for %dx%d (%.2f×)",
					humanBitrate(avg), tier.kbps, tier.width, tier.height, ratio),
				Value: finding.Num(avg), Unit: "bps",
				Hint: "the specification publishes recommended averages, not a range; the 0.5×–2× band this fired outside of is segcheck's own",
			})
			continue
		}
		out = append(out, finding.Finding{
			Target: label, Status: finding.OK,
			Message: fmt.Sprintf("measured %s against the %.0f kbps recommended for %dx%d (%.2f×)",
				humanBitrate(avg), tier.kbps, tier.width, tier.height, ratio),
			Value: finding.Num(avg), Unit: "bps",
		})
	}
	return out
}

// nearestAppleTier is the row whose pixel count is closest to the rung's, so a
// 1024x576 rung is judged against the 960x540 tier rather than against nothing.
func nearestAppleTier(w, h int) (appleTier, bool) {
	pixels := float64(w * h)
	if pixels <= 0 {
		return appleTier{}, false
	}
	best, bestDist := appleTier{}, math.Inf(1)
	for _, t := range appleAVCTiers {
		d := math.Abs(math.Log(float64(t.width*t.height) / pixels))
		if d < bestDist {
			best, bestDist = t, d
		}
	}
	return best, true
}

func appleFrameRate(ctx profileContext) []finding.Finding {
	var out []finding.Finding
	type rung struct {
		label string
		fps   float64
	}
	var rungs []rung

	for _, rd := range ctx.rends {
		if rd.err != nil || rd.initErr != nil || rd.r.Kind != manifest.Video {
			continue
		}
		var rates []float64
		for _, sd := range parsedSegs(rd) {
			t, ok := sd.info.Track(media.Video)
			if !ok {
				continue
			}
			if fps, ok := t.FrameRateFPS(); ok {
				rates = append(rates, fps)
			}
		}
		if len(rates) == 0 {
			continue
		}
		label := rendLabel(rd.r)
		typical := medianFloat(rates)
		rungs = append(rungs, rung{label, typical})

		// Constant within the rendition: the specification asks for a fixed frame
		// rate, and a rung that changes rate part-way through makes every player
		// re-time its renderer.
		worst := typical
		for _, r := range rates {
			if math.Abs(r-typical) > math.Abs(worst-typical) {
				worst = r
			}
		}
		if typical > 0 && math.Abs(worst-typical)/typical*100 > ctx.opts.FrameRateTolerancePct {
			out = append(out, finding.Finding{
				Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("frame rate varies within the rendition: %s against a usual %s — the specification asks for a constant rate",
					humanFPS(worst), humanFPS(typical)),
				Value: finding.Num(worst), Unit: "fps",
				Hint: "a rate that changes part-way through makes every player re-time its renderer, which shows as a stutter at the change",
			})
		}
	}

	if len(rungs) >= 2 {
		top := rungs[0]
		for _, r := range rungs[1:] {
			if r.fps > top.fps {
				top = r
			}
		}
		var odd []string
		for _, r := range rungs {
			if !relatedRate(top.fps, r.fps, ctx.opts.FrameRateTolerancePct) {
				odd = append(odd, fmt.Sprintf("%s at %s", r.label, humanFPS(r.fps)))
			}
		}
		if len(odd) > 0 {
			out = append(out, finding.Finding{
				Target: "ladder", Status: finding.WARN,
				Message: fmt.Sprintf("rungs run at unrelated frame rates: %s against %s at %s — the specification asks for one rate, or an exact fraction of it, across the ladder",
					joinAnd(odd), top.label, humanFPS(top.fps)),
				Hint: "an ABR switch between unrelated rates is visibly uneven; an exact fraction of the top rate is fine, an unrelated one is not",
			})
		} else if len(out) == 0 {
			out = append(out, finding.Finding{
				Target: "ladder", Status: finding.OK,
				Message: fmt.Sprintf("all %d rungs run at %s or an exact fraction of it", len(rungs), humanFPS(top.fps)),
				Value:   finding.Num(top.fps), Unit: "fps",
			})
		}
	}
	return out
}

// ---------- measurements shared by the rules ----------

// measuredBitrates is the average and peak segment bit rate of a rendition,
// from the bytes that arrived and the media duration they turned out to hold —
// the same pair `bitrate` compares against BANDWIDTH.
func measuredBitrates(rd *renditionData) (avg, peak float64, peakSeg segmentData, ok bool) {
	var sumBytes int64
	var sumDur float64
	for _, sd := range parsedSegs(rd) {
		t, tok := sd.info.Timeline()
		if !tok {
			continue
		}
		dur, dok := t.DurationSec()
		if !dok || dur <= 0 {
			continue
		}
		sumBytes += int64(len(sd.res.Body))
		sumDur += dur
		if bps := float64(len(sd.res.Body)) * 8 / dur; bps > peak {
			peak, peakSeg, ok = bps, sd, true
		}
	}
	if sumDur <= 0 || !ok {
		return 0, 0, segmentData{}, false
	}
	return float64(sumBytes) * 8 / sumDur, peak, peakSeg, true
}

// measuredDurations is how long each sampled segment's media really lasts.
func measuredDurations(rd *renditionData) []float64 {
	var out []float64
	for _, sd := range parsedSegs(rd) {
		t, ok := sd.info.Timeline()
		if !ok {
			continue
		}
		if dur, ok := t.DurationSec(); ok && dur > 0 {
			out = append(out, dur)
		}
	}
	return out
}

// codedSize is the resolution the media actually codes, which is what a tier
// should be chosen by: a rung mislabelled in the manifest would otherwise be
// judged against the tier of a resolution it does not carry.
func codedSize(rd *renditionData) (int, int) {
	for _, sd := range parsedSegs(rd) {
		if t, ok := sd.info.Track(media.Video); ok && t.Width > 0 && t.Height > 0 {
			return t.Width, t.Height
		}
	}
	return 0, 0
}

func videoCodecOf(rd *renditionData) (string, bool) {
	for _, sd := range parsedSegs(rd) {
		if t, ok := sd.info.Track(media.Video); ok && t.Codec != "" {
			return t.Codec, true
		}
	}
	return "", false
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := parts[0]
	for _, p := range parts[1 : len(parts)-1] {
		out += ", " + p
	}
	return out + " and " + parts[len(parts)-1]
}
