package analyze

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// ---------- delivery ----------

// checkFetch reports whether the sampled segments could be downloaded at all.
func checkFetch(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		label := rendLabel(rd)
		if rd.err != nil {
			out = append(out, finding.Finding{
				Check: "fetch", Target: label, Status: finding.ERROR,
				Message: rd.err.Error(),
			})
			continue
		}
		if len(rd.segs) == 0 {
			out = append(out, finding.Finding{
				Check: "fetch", Target: label, Status: finding.BAD,
				Message: "no segment to sample: the playlist is empty",
			})
			continue
		}

		var failed int
		var bytes int64
		for _, sd := range rd.segs {
			if sd.fetchErr != nil {
				failed++
				out = append(out, finding.Finding{
					Check: "fetch", Target: segLabel(rd, sd), Status: finding.ERROR,
					Message: fmt.Sprintf("segment not fetched: %v", sd.fetchErr),
					Hint:    sd.seg.URI,
				})
				continue
			}
			bytes += int64(len(sd.res.Body))
			if sd.seg.ByteRange != nil && sd.res.Status == 200 {
				out = append(out, finding.Finding{
					Check: "fetch", Target: segLabel(rd, sd), Status: finding.WARN,
					Message: fmt.Sprintf("origin ignored the Range request (%s): answered 200 with the whole resource", sd.seg.ByteRange.Header()),
					Hint:    "byte-range segments need a 206; behind a CDN this usually means range support is off",
				})
			}
			if sd.res.Truncated {
				out = append(out, finding.Finding{
					Check: "fetch", Target: segLabel(rd, sd), Status: finding.WARN,
					Message: "segment larger than the byte cap and truncated: analysis is partial",
					Hint:    "raise --max-bytes",
				})
			}
		}
		if failed < len(rd.segs) {
			out = append(out, finding.Finding{
				Check: "fetch", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%d/%d segments fetched, %s", len(rd.segs)-failed, len(rd.segs), humanBytes(bytes)),
				Value:   finding.Num(float64(bytes)), Unit: "bytes",
			})
		}
	}
	return out
}

// checkInit reports a rendition whose initialisation segment could not be
// fetched. For fMP4 that is fatal to playback, not a detail: without the init a
// player has no timescale, no codec configuration and no decoder to build.
func checkInit(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil || rd.initErr == nil {
			continue
		}
		out = append(out, finding.Finding{
			Check: "init", Target: rendLabel(rd), Status: finding.ERROR,
			Message: rd.initErr.Error(),
			Hint:    "no player can start this rendition without its EXT-X-MAP / initialisation segment; codec, timescale and resolution checks were skipped",
		})
	}
	return out
}

// checkContainer reports whether the downloaded bytes are the media they claim
// to be. Full-segment AES-128 is handled apart: those bytes are *supposed* to be
// unreadable, and calling that a defect would be wrong.
func checkContainer(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		var encryptedOpaque int
		var unsupported string
		containers := map[string]int{}
		for _, sd := range rd.segs {
			if sd.fetchErr != nil {
				continue
			}
			if sd.parseErr != nil {
				if errors.Is(sd.parseErr, media.ErrUnknownContainer) && isFullSegmentEncryption(sd.seg.KeyMethod) {
					encryptedOpaque++
					continue
				}
				// A container segcheck recognises but does not analyse is a
				// limit of this tool. Reporting it as a defect would send an
				// operator hunting for a problem in a healthy stream.
				if errors.Is(sd.parseErr, media.ErrUnsupportedContainer) {
					unsupported = sd.parseErr.Error()
					continue
				}
				out = append(out, finding.Finding{
					Check: "container", Target: segLabel(rd, sd), Status: finding.BAD,
					Message: fmt.Sprintf("segment unparseable: %v", sd.parseErr),
					Hint:    fmt.Sprintf("%d bytes, Content-Type %q — an origin error page served with a 200 lands here", len(sd.res.Body), sd.res.ContentType()),
				})
				continue
			}
			containers[sd.info.Container]++
		}

		label := rendLabel(rd)
		if unsupported != "" {
			out = append(out, finding.Finding{
				Check: "container", Target: label, Status: finding.OK,
				Message: unsupported,
				Hint:    "segcheck can fetch these segments but not inspect their contents yet",
			})
		}
		if encryptedOpaque > 0 {
			out = append(out, finding.Finding{
				Check: "container", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%d segments are full-segment AES-128: content checks need the key and were skipped", encryptedOpaque),
				Hint:    "timeline, duration and resolution cannot be verified for encrypted segments",
			})
		}
		if len(containers) > 1 {
			out = append(out, finding.Finding{
				Check: "container", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("mixed containers in one rendition: %s", describeCounts(containers)),
				Hint:    "players do not all handle a container switch mid-rendition",
			})
		}
		if segs := parsedSegs(rd); len(segs) > 0 && rd.initErr == nil {
			tracks := segs[0].info.Tracks
			var desc []string
			for _, t := range tracks {
				desc = append(desc, t.Describe())
			}
			out = append(out, finding.Finding{
				Check: "container", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%s, %d tracks: %s", segs[0].info.Container, len(tracks), strings.Join(desc, " + ")),
			})
		}
	}
	return out
}

// ---------- timeline ----------

// checkContinuity is the reason this tool exists: a manifest cannot tell you
// whether consecutive segments actually join up. Each segment should start where
// the previous one ended; anything else is a gap or an overlap the viewer sees
// as a stutter, and it is invisible to every manifest-only checker.
func checkContinuity(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	tolSec := opts.GapToleranceMS / 1000

	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		label := rendLabel(rd)

		// Transport-level packet loss, before any timeline reasoning.
		ccTotal := 0
		for _, sd := range parsedSegs(rd) {
			for _, t := range sd.info.Tracks {
				ccTotal += t.CCErrors
			}
		}
		if ccTotal > 0 {
			out = append(out, finding.Finding{
				Check: "continuity", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("%d MPEG-TS continuity-counter breaks: packets lost between the packager and here", ccTotal),
				Value:   finding.Num(float64(ccTotal)), Unit: "packets",
				Hint: "packet loss on ingest or in transit; the player will show artefacts or drop the segment",
			})
		}

		segs := parsedSegs(rd)
		if len(segs) < 2 {
			continue
		}

		breaks := 0
		compared := 0
		for i := 1; i < len(segs); i++ {
			prev, cur := segs[i-1], segs[i]
			// Only compare segments that are actually adjacent in the playlist;
			// a fetch failure in between makes the pair meaningless.
			if cur.seg.Sequence != prev.seg.Sequence+1 {
				continue
			}
			pt, ok1 := prev.info.Timeline()
			ct, ok2 := cur.info.Timeline()
			if !ok1 || !ok2 || pt.Timescale == 0 || pt.Timescale != ct.Timescale {
				continue
			}
			prevDur, okd := pt.DurationSec()
			if !okd {
				continue
			}
			compared++

			// Work in ticks so PTS wraparound can be undone, then convert.
			deltaTicks := media.UnwrapDelta(pt.MinPTS, ct.MinPTS)
			actualAdvance := float64(deltaTicks) / float64(pt.Timescale)
			drift := actualAdvance - prevDur

			if cur.seg.Discontinuity {
				// A declared discontinuity licenses the jump; report it so the
				// operator knows it was seen, without calling it a defect.
				if math.Abs(drift) > tolSec {
					out = append(out, finding.Finding{
						Check: "continuity", Target: segLabel(rd, cur), Status: finding.OK,
						Message: fmt.Sprintf("declared discontinuity, timeline jumps %s", signedMS(drift)),
					})
				}
				continue
			}
			if math.Abs(drift) <= tolSec {
				continue
			}
			breaks++
			kind, hint := "gap", "the player has nothing to show for this interval: expect a stall or a skip"
			if drift < 0 {
				kind, hint = "overlap", "the segments cover the same interval twice: expect a stutter or a repeated frame"
			}
			out = append(out, finding.Finding{
				Check: "continuity", Target: segLabel(rd, cur), Status: finding.BAD,
				Message: fmt.Sprintf("%s of %s: previous segment ends at %.3fs, this one starts at %.3fs, with no EXT-X-DISCONTINUITY",
					kind, signedMS(drift), toSec(pt.MinPTS, pt.Timescale)+prevDur, toSec(ct.MinPTS, ct.Timescale)),
				Value: finding.Num(drift * 1000), Unit: "ms",
				Hint: hint,
			})
		}
		if breaks == 0 && compared > 0 {
			out = append(out, finding.Finding{
				Check: "continuity", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("timeline continuous across %d segment boundaries (tolerance %.0fms)", compared, opts.GapToleranceMS),
			})
		}
	}
	return out
}

// checkDuration compares the duration the manifest declares against the media's
// real duration. Drift here is what makes a player's clock diverge from the
// stream and, on live, what slowly eats the live-edge margin.
func checkDuration(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		label := rendLabel(rd)
		var sumDeclared, sumActual float64
		worst := 0.0
		var worstSeg segmentData
		haveWorst := false
		n := 0

		for _, sd := range parsedSegs(rd) {
			t, ok := sd.info.Timeline()
			if !ok {
				continue
			}
			actual, ok := t.DurationSec()
			if !ok || sd.seg.Duration <= 0 {
				continue
			}
			n++
			sumDeclared += sd.seg.Duration
			sumActual += actual

			driftPct := (actual - sd.seg.Duration) / sd.seg.Duration * 100
			if math.Abs(driftPct) > math.Abs(worst) {
				worst, worstSeg, haveWorst = driftPct, sd, true
			}

			// A segment longer than TARGETDURATION violates the HLS spec and
			// makes players that size their buffer from it underrun.
			if rd.targetDuration > 0 && sd.seg.Duration > rd.targetDuration+0.5 {
				out = append(out, finding.Finding{
					Check: "duration", Target: segLabel(rd, sd), Status: finding.BAD,
					Message: fmt.Sprintf("declared %.3fs exceeds EXT-X-TARGETDURATION %.0fs", sd.seg.Duration, rd.targetDuration),
					Hint:    "players size their buffer from TARGETDURATION; a longer segment can underrun it",
				})
			}
		}
		if n == 0 {
			continue
		}

		if haveWorst && math.Abs(worst) > opts.DurationTolerancePct {
			t, _ := worstSeg.info.Timeline()
			actual, _ := t.DurationSec()
			out = append(out, finding.Finding{
				Check: "duration", Target: segLabel(rd, worstSeg), Status: finding.WARN,
				Message: fmt.Sprintf("declared %.3fs, media is %.3fs (%+.1f%%)", worstSeg.seg.Duration, actual, worst),
				Value:   finding.Num(worst), Unit: "%",
				Hint: "worst of the sampled segments; the manifest duration is what the player schedules on",
			})
		}

		// Accumulated drift matters more than any single segment: it is what
		// pulls the player's clock away from the stream over a long session.
		totalPct := (sumActual - sumDeclared) / sumDeclared * 100
		status := finding.OK
		if math.Abs(totalPct) > opts.DurationTolerancePct {
			status = finding.WARN
		}
		out = append(out, finding.Finding{
			Check: "duration", Target: label, Status: status,
			Message: fmt.Sprintf("%d segments: declared %.3fs, media %.3fs (%+.2f%%)", n, sumDeclared, sumActual, totalPct),
			Value:   finding.Num(totalPct), Unit: "%",
		})
	}
	return out
}

// checkTimeline compares a DASH SegmentTimeline's declared @t against the
// segment's own baseMediaDecodeTime. When they disagree the player seeks to a
// time the segment does not contain.
func checkTimeline(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	tolSec := opts.GapToleranceMS / 1000
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		mismatches, compared := 0, 0
		for _, sd := range parsedSegs(rd) {
			if !sd.seg.HasDeclaredStart {
				continue
			}
			t, ok := sd.info.Timeline()
			if !ok || t.Timescale == 0 {
				continue
			}
			compared++
			actual := toSec(t.MinPTS, t.Timescale)
			drift := actual - sd.seg.DeclaredStart
			if math.Abs(drift) <= tolSec {
				continue
			}
			mismatches++
			out = append(out, finding.Finding{
				Check: "timeline", Target: segLabel(rd, sd), Status: finding.BAD,
				Message: fmt.Sprintf("MPD declares the segment starts at %.3fs, the media starts at %.3fs (%s off)", sd.seg.DeclaredStart, actual, signedMS(drift)),
				Value:   finding.Num(drift * 1000), Unit: "ms",
				Hint: "a seek to the declared time lands outside the segment",
			})
		}
		if compared > 0 && mismatches == 0 {
			out = append(out, finding.Finding{
				Check: "timeline", Target: rendLabel(rd), Status: finding.OK,
				Message: fmt.Sprintf("SegmentTimeline matches the media timeline on %d segments", compared),
			})
		}
	}
	return out
}

// ---------- the ladder's claims ----------

// checkBitrate compares the measured bitrate with the declared BANDWIDTH. HLS
// requires BANDWIDTH to be an upper bound on any segment's bitrate: when it is
// under-declared, a player picks the rendition believing it fits the connection
// and then rebuffers.
func checkBitrate(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil || rd.r.Bandwidth <= 0 {
			continue
		}
		label := rendLabel(rd)
		var sumBytes int64
		var sumDur float64
		peak := 0.0
		var peakSeg segmentData
		havePeak := false

		for _, sd := range parsedSegs(rd) {
			t, ok := sd.info.Timeline()
			if !ok {
				continue
			}
			dur, ok := t.DurationSec()
			if !ok || dur <= 0 {
				continue
			}
			sumBytes += int64(len(sd.res.Body))
			sumDur += dur
			if bps := float64(len(sd.res.Body)) * 8 / dur; bps > peak {
				peak, peakSeg, havePeak = bps, sd, true
			}
		}
		if sumDur <= 0 {
			continue
		}
		avg := float64(sumBytes) * 8 / sumDur
		declared := float64(rd.r.Bandwidth)
		limit := declared * (1 + opts.BitrateTolerancePct/100)

		switch {
		case havePeak && peak > limit:
			out = append(out, finding.Finding{
				Check: "bitrate", Target: segLabel(rd, peakSeg), Status: finding.WARN,
				Message: fmt.Sprintf("segment peaks at %s but BANDWIDTH declares %s (%+.0f%%)", humanBitrate(peak), humanBitrate(declared), (peak/declared-1)*100),
				Value:   finding.Num(peak), Unit: "bps",
				Hint: "BANDWIDTH must be an upper bound: under-declaring makes players choose a rendition their connection cannot sustain",
			})
		case avg < declared*0.5:
			out = append(out, finding.Finding{
				Check: "bitrate", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("measured average %s against a declared %s: over-declared by %.1f×", humanBitrate(avg), humanBitrate(declared), declared/avg),
				Value:   finding.Num(avg), Unit: "bps",
				Hint: "an inflated BANDWIDTH holds players on a lower rendition than their connection allows",
			})
		default:
			out = append(out, finding.Finding{
				Check: "bitrate", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("measured %s average / %s peak against a declared %s", humanBitrate(avg), humanBitrate(peak), humanBitrate(declared)),
				Value:   finding.Num(avg), Unit: "bps",
			})
		}
	}
	return out
}

// checkResolution compares the coded resolution in the bitstream against the
// one the manifest advertises. This is the "1080p rung that is really an
// upscaled 720p" defect, and no manifest-only tool can see it.
func checkResolution(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil || rd.initErr != nil || rd.r.Kind != manifest.Video {
			continue
		}
		if rd.r.Width == 0 || rd.r.Height == 0 {
			continue // nothing declared to compare against
		}
		label := rendLabel(rd)
		reported := false
		for _, sd := range parsedSegs(rd) {
			v, ok := sd.info.Track(media.Video)
			if !ok || v.Width == 0 || v.Height == 0 {
				continue
			}
			if v.Width == rd.r.Width && v.Height == rd.r.Height {
				if !reported {
					out = append(out, finding.Finding{
						Check: "resolution", Target: label, Status: finding.OK,
						Message: fmt.Sprintf("coded %dx%d matches the declared resolution", v.Width, v.Height),
					})
					reported = true
				}
				continue
			}
			out = append(out, finding.Finding{
				Check: "resolution", Target: segLabel(rd, sd), Status: finding.BAD,
				Message: fmt.Sprintf("manifest declares %dx%d, the bitstream codes %dx%d", rd.r.Width, rd.r.Height, v.Width, v.Height),
				Hint:    "the rendition is not the resolution the ladder promises: either a mislabelled rung or an upscale",
			})
			reported = true
			break // one report per rendition is enough
		}
	}
	return out
}

// checkTracks verifies each rendition carries the streams it should, with the
// codecs it declares, consistently across its segments.
func checkTracks(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		// Without the initialisation segment a fragment's tracks have no
		// handler and no codec, so they all read as "other". Reporting "no
		// video track" then would blame the media for a fetch failure.
		if rd.err != nil || rd.initErr != nil {
			continue
		}
		segs := parsedSegs(rd)
		if len(segs) == 0 {
			continue
		}
		label := rendLabel(rd)

		if rd.r.Kind == manifest.Video {
			missing := 0
			for _, sd := range segs {
				if _, ok := sd.info.Track(media.Video); !ok {
					missing++
				}
			}
			switch {
			case missing == 0:
			case rd.r.DeclaresVideo():
				// The manifest promised video by resolution or codec and the
				// media does not deliver it.
				out = append(out, finding.Finding{
					Check: "tracks", Target: label, Status: finding.BAD,
					Message: fmt.Sprintf("%d/%d sampled segments carry no video track", missing, len(segs)),
					Hint:    "a video rendition without video: check the packager's track selection",
				})
			case missing == len(segs):
				// The variant declared neither RESOLUTION nor a video codec and
				// carries none: an audio-only variant, which is legitimate.
				out = append(out, finding.Finding{
					Check: "tracks", Target: label, Status: finding.OK,
					Message: "audio-only variant: no RESOLUTION and no video codec declared, and none present",
				})
			default:
				out = append(out, finding.Finding{
					Check: "tracks", Target: label, Status: finding.WARN,
					Message: fmt.Sprintf("%d/%d sampled segments carry no video track, the rest do", missing, len(segs)),
					Hint:    "the variant declares no RESOLUTION, so this may be an audio-only rung with stray video segments",
				})
			}
		}

		// The set of tracks must not change between segments of one rendition:
		// a player builds its decoder pipeline from the first segment.
		shapes := map[string]int{}
		for _, sd := range segs {
			shapes[trackShape(sd.info)]++
		}
		if len(shapes) > 1 {
			out = append(out, finding.Finding{
				Check: "tracks", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("track layout changes between segments: %s", describeCounts(shapes)),
				Hint:    "a mid-rendition track change forces a decoder reset, which most players show as a freeze",
			})
		}

		// Declared CODECS against what the bitstream actually is.
		if rd.r.Codecs != "" {
			for _, kind := range []media.TrackKind{media.Video, media.Audio} {
				t, ok := segs[0].info.Track(kind)
				if !ok || t.Codec == "" {
					continue
				}
				if declared, found := declaredCodec(rd.r.Codecs, kind); found && declared != t.Codec {
					out = append(out, finding.Finding{
						Check: "tracks", Target: label, Status: finding.WARN,
						Message: fmt.Sprintf("CODECS declares %s for %s, the bitstream is %s", declared, kind, t.Codec),
						Hint:    "players use CODECS to decide what they can play before downloading anything",
					})
				}
			}
		}
	}
	return out
}

// checkKeyframe reports segments that do not open on a random access point
// (SC-16).
//
// A decoder switching into such a segment has no reference picture: it shows
// nothing until the next real keyframe, or it shows corruption. This is the defect
// behind "ABR switching stutters even though the boundaries line up", and it is
// invisible from the manifest — the boundaries really are aligned, the durations
// really are correct, and `alignment` passes. Only the media says otherwise.
//
// The severity is BAD because it breaks playback for everyone who switches, and
// the counterpart is that a rendition which states nothing about its keyframes
// gets an OK-level note instead. An fMP4 fragment need not carry the sample flags,
// and reporting a defect there would flag legal streams by the thousand.
func checkKeyframe(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil || rd.initErr != nil {
			continue
		}
		// Audio is independently decodable frame by frame, so the question does
		// not apply.
		if rd.r.Kind == manifest.Audio {
			continue
		}
		segs := parsedSegs(rd)
		if len(segs) == 0 {
			continue
		}
		label := rendLabel(rd)

		readable := 0
		var noKeyframe []segmentData // no random access point at all: unswitchable
		var lateKeyframe int         // one is present, just not the first picture
		for _, sd := range segs {
			t, ok := sd.info.Track(media.Video)
			if !ok {
				continue
			}
			opens, known := t.StartsOnKeyframe()
			if !known {
				continue
			}
			readable++
			if opens {
				continue
			}
			// It does not open on one. Whether that matters depends on what else is
			// in the segment, and on whether anyone looked.
			present, scanned := t.ContainsKeyframe()
			switch {
			case scanned && !present:
				noKeyframe = append(noKeyframe, sd)
			default:
				lateKeyframe++
			}
		}

		if readable == 0 {
			out = append(out, finding.Finding{
				Check: "keyframe", Target: label, Status: finding.OK,
				Message: "segments do not state whether they open on a keyframe: not verified",
				Hint:    "fMP4 fragments need not carry sample flags, and an MPEG-TS segment's video payload may not have been reached",
			})
			continue
		}

		// The severe case: nothing in the segment a decoder can start from. One
		// finding per rendition, naming the first offender, so a rendition that is
		// wrong throughout does not bury the rest of the report.
		if len(noKeyframe) > 0 {
			out = append(out, finding.Finding{
				Check: "keyframe", Target: segLabel(rd, noKeyframe[0]), Status: finding.BAD,
				Message: fmt.Sprintf("%d/%d verified segments contain no keyframe at all", len(noKeyframe), readable),
				Value:   finding.Num(float64(len(noKeyframe))), Unit: "segments",
				Hint: "a player switching into one of these has no reference picture to start from: the switch shows nothing until the next keyframe, however well the boundaries line up",
			})
			continue
		}

		if lateKeyframe > 0 {
			// Apple's byte-range reference stream does this: a range boundary falls
			// on a transport packet, so the segment carries the tail of the previous
			// picture before its own keyframe. Players start at the keyframe and it
			// plays everywhere, so this is worth stating and not worth alarming over.
			out = append(out, finding.Finding{
				Check: "keyframe", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%d/%d segments carry a keyframe but not as their first picture", lateKeyframe, readable),
				Value:   finding.Num(float64(lateKeyframe)), Unit: "segments",
				Hint: "usual where segment boundaries fall on transport packets rather than access units; players start at the keyframe and discard what precedes it",
			})
			continue
		}

		out = append(out, finding.Finding{
			Check: "keyframe", Target: label, Status: finding.OK,
			Message: fmt.Sprintf("all %d verified segments open on a keyframe", readable),
		})
	}
	return out
}

// checkFrameRate measures the rate the pictures are actually shown at and asks
// two things of it (SC-17).
//
// Against the manifest: `FRAME-RATE` / `@frameRate` is what a player consults to
// decide whether it can decode a rendition *before* downloading any of it. A
// 1080p60 rung declared as 30 will be selected by a device that can only manage
// 30, and then stutter — while the manifest reads perfectly on the way down.
//
// Across the ladder: rungs running at unrelated rates make every switch visibly
// uneven. The deliberate exception is an exact integer relation, because halving
// the rate on the lower rungs is an ordinary and widespread way to save bitrate;
// reporting that would flag a technique in wide use.
func checkFrameRate(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding

	type measured struct {
		rd  *renditionData
		fps float64
	}
	var seen []measured

	for _, rd := range rends {
		if rd.err != nil || rd.initErr != nil || rd.r.Kind == manifest.Audio {
			continue
		}
		// The median of the per-segment rates: one odd segment should not decide
		// what the rendition runs at.
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
		fps := medianFloat(rates)
		seen = append(seen, measured{rd, fps})

		label := rendLabel(rd)
		declared := rd.r.FrameRate
		if declared <= 0 {
			out = append(out, finding.Finding{
				Check: "framerate", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%s measured, no FRAME-RATE declared to compare against", humanFPS(fps)),
				Value:   finding.Num(fps), Unit: "fps",
			})
			continue
		}

		// The tolerance has to absorb the NTSC rates: a manifest writes 29.97
		// where the media runs at 30000/1001, and 23.976 for 24000/1001. Those are
		// the same rate spelled two ways, and flagging them would fire on a large
		// fraction of the world's content.
		if math.Abs(fps-declared)/declared*100 > opts.FrameRateTolerancePct {
			out = append(out, finding.Finding{
				Check: "framerate", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("media runs at %s but FRAME-RATE declares %s", humanFPS(fps), humanFPS(declared)),
				Value:   finding.Num(fps), Unit: "fps",
				Hint: "players use FRAME-RATE to decide what they can decode before downloading anything: a rung that runs faster than it admits gets chosen by devices that cannot sustain it",
			})
			continue
		}
		out = append(out, finding.Finding{
			Check: "framerate", Target: label, Status: finding.OK,
			Message: fmt.Sprintf("%s, as declared", humanFPS(fps)),
			Value:   finding.Num(fps), Unit: "fps",
		})
	}

	if len(seen) < 2 {
		return out
	}

	// The ladder. Compare against the fastest rung: every other rate should be it
	// or an exact fraction of it.
	fastest := seen[0]
	for _, m := range seen[1:] {
		if m.fps > fastest.fps {
			fastest = m
		}
	}
	var odd []string
	for _, m := range seen {
		if !relatedRate(fastest.fps, m.fps, opts.FrameRateTolerancePct) {
			odd = append(odd, fmt.Sprintf("%s %s", rendLabel(m.rd), humanFPS(m.fps)))
		}
	}
	if len(odd) > 0 {
		out = append(out, finding.Finding{
			Check: "framerate", Target: "ladder", Status: finding.WARN,
			Message: fmt.Sprintf("ladder mixes frame rates: %s against %s at %s",
				strings.Join(odd, ", "), rendLabel(fastest.rd), humanFPS(fastest.fps)),
			Hint: "switching between rungs at unrelated rates is visibly uneven; an exact fraction of the top rate is fine, an unrelated one is not",
		})
	}
	return out
}

// relatedRate reports whether fps is the top rate or an exact integer fraction of
// it — 30 against 60, 15 against 60 — within the tolerance.
func relatedRate(top, fps, tolPct float64) bool {
	if fps <= 0 || top <= 0 {
		return false
	}
	ratio := top / fps
	nearest := math.Round(ratio)
	if nearest < 1 {
		return false
	}
	return math.Abs(ratio-nearest)/nearest*100 <= tolPct
}

func medianFloat(xs []float64) float64 {
	s := append([]float64{}, xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// humanFPS drops the decimals a whole rate does not need, so 25 reads as "25fps"
// and 29.97 keeps the part that distinguishes it from 30.
// checkAudio compares an audio rendition's actual format against what the
// manifest claims and against itself. A player configures its decoder and its
// output device from the manifest before it has fetched a byte of media, so a
// contradiction here is not cosmetic: the wrong rate is a pitch shift, the wrong
// channel count is a missing centre channel or a silent surround.
//
// The measurement comes from wherever the format is actually stated — the
// AudioSampleEntry in fMP4, the ADTS header in MPEG-TS and packed audio — and the
// claim from EXT-X-MEDIA CHANNELS or DASH @audioSamplingRate and
// AudioChannelConfiguration. Either side may be silent, and a claim the manifest
// never made cannot be contradicted.
func checkAudio(rends []*renditionData) []finding.Finding {
	var out []finding.Finding

	for _, rd := range rends {
		if rd.err != nil || rd.initErr != nil {
			continue
		}
		label := rendLabel(rd)

		// Collect every distinct format across the sampled segments. More than
		// one means the rendition reconfigures part-way through.
		type format struct {
			rate, channels int
			codec          string
		}
		var formats []format
		for _, sd := range parsedSegs(rd) {
			t, ok := sd.info.Track(media.Audio)
			if !ok {
				continue
			}
			if t.SampleRate <= 0 && t.Channels <= 0 {
				continue
			}
			f := format{t.SampleRate, t.Channels, t.Codec}
			seen := false
			for _, g := range formats {
				if g == f {
					seen = true
					break
				}
			}
			if !seen {
				formats = append(formats, f)
			}
		}
		if len(formats) == 0 {
			// A video-only rendition has nothing for this check to say. One that
			// does carry audio but states no format gets an honest OK: that is a
			// limit of what the segment carries, not a defect in it.
			if !hasAudioTrack(rd) {
				continue
			}
			out = append(out, finding.Finding{
				Check: "audio", Target: label, Status: finding.OK,
				Message: "segments do not state a sampling rate or channel count: not verified",
			})
			continue
		}
		if len(formats) > 1 {
			names := make([]string, 0, len(formats))
			for _, f := range formats {
				names = append(names, humanAudioFormat(f.rate, f.channels))
			}
			out = append(out, finding.Finding{
				Check: "audio", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("audio format changes mid-rendition: %s", strings.Join(names, " then ")),
				Hint:    "a decoder is configured once from the first segment it sees; a rendition that changes rate or channel layout part-way through goes silent or plays noise from that point on",
			})
			continue
		}

		got := formats[0]
		var problems []string
		var value float64
		var unit string
		if want, as, ok := declaredAudioCodec(rd.r.Codecs); ok && got.codec != "" && want != got.codec {
			problems = append(problems, fmt.Sprintf("media is %s but CODECS declares %s", got.codec, as))
		}
		if rd.r.SampleRate > 0 && got.rate > 0 && !ratesAgree(got.rate, rd.r.SampleRate, rd.r.Codecs) {
			problems = append(problems, fmt.Sprintf("media runs at %s but the manifest declares %s",
				humanSampleRate(got.rate), humanSampleRate(rd.r.SampleRate)))
			value, unit = float64(got.rate), "Hz"
		}
		if rd.r.Channels > 0 && got.channels > 0 && rd.r.Channels != got.channels {
			problems = append(problems, fmt.Sprintf("media is %s but the manifest declares %s",
				humanChannels(got.channels), humanChannels(rd.r.Channels)))
			if unit == "" {
				value, unit = float64(got.channels), "channels"
			}
		}
		if len(problems) > 0 {
			out = append(out, finding.Finding{
				Check: "audio", Target: label, Status: finding.BAD,
				Message: strings.Join(problems, "; "),
				Value:   finding.Num(value), Unit: unit,
				Hint: "players choose and configure an audio rendition from these declarations alone: one that does not describe the media gets picked for a device that cannot play it",
			})
			continue
		}

		switch {
		case rd.r.SampleRate > 0 || rd.r.Channels > 0 || rd.r.Codecs != "":
			out = append(out, finding.Finding{
				Check: "audio", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%s, as declared", humanAudioFormat(got.rate, got.channels)),
			})
		default:
			out = append(out, finding.Finding{
				Check: "audio", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%s measured, nothing declared to compare against", humanAudioFormat(got.rate, got.channels)),
			})
		}
	}
	return out
}

// checkCaptions compares the closed captions the manifest declares against the
// caption data the video bitstream actually carries.
//
// This is the defect the check exists for: a manifest declares CC1, the encoder
// stops emitting it, and nothing in the manifest changes — so no manifest-level
// checker will ever notice, and in several countries the obligation is legal
// rather than editorial.
//
// Two asymmetries shape the verdicts. CC1 and CC3 share CEA-608 field 1, and
// separating them means decoding the line-21 control codes, so a channel declared
// over a populated field cannot be confirmed and is not reported: only an empty
// field is a defect. CEA-708 names its services in the DTVCC packet layer, so a
// declared service that is genuinely not there is a defect a reader can be sure
// of.
func checkCaptions(rends []*renditionData) []finding.Finding {
	var out []finding.Finding

	for _, rd := range rends {
		if rd.err != nil || rd.initErr != nil {
			continue
		}
		declared := rd.r.Captions
		if len(declared) == 0 && !rd.r.CaptionsNone && !anyCaptionsFound(rd) {
			// Nothing claimed and nothing found: a finding per rendition saying
			// "no captions" would be noise on most of the world's streams.
			continue
		}

		label := rendLabel(rd)
		got, scanned := captionsSeen(rd)
		if !scanned {
			if len(declared) == 0 {
				continue // nothing claimed, so the hole in coverage costs nothing
			}
			out = append(out, finding.Finding{
				Check: "captions", Target: label, Status: finding.ERROR,
				Message: fmt.Sprintf("%s declared but segcheck could not read the video bitstream to verify it",
					captionList(declared)),
				Hint: "an unsupported or unreadable bitstream is a limit of this tool, not a defect in the stream — the captions may well be there",
			})
			continue
		}

		var missing []string
		for _, c := range declared {
			if !captionCouldBePresent(c.InstreamID, got) {
				missing = append(missing, c.InstreamID)
			}
		}
		switch {
		case len(missing) > 0:
			out = append(out, finding.Finding{
				Check: "captions", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("%s declared but the bitstream carries %s",
					strings.Join(missing, ", "), humanCaptions(got)),
				Value: finding.Num(float64(len(missing))), Unit: "services",
				Hint: "a declared caption track that is not in the media is an accessibility failure a player cannot work around: it offers the option and nothing appears",
			})
		case rd.r.CaptionsNone && got.Any():
			out = append(out, finding.Finding{
				Check: "captions", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("bitstream carries %s but the variant declares CLOSED-CAPTIONS=NONE", humanCaptions(got)),
				Hint:    "a player believes the manifest: captions that are declared absent are never offered, so the toggle is not there to turn on",
			})
		case len(declared) > 0:
			out = append(out, finding.Finding{
				Check: "captions", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%s declared, bitstream carries %s", captionList(declared), humanCaptions(got)),
			})
		case got.Any():
			out = append(out, finding.Finding{
				Check: "captions", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("bitstream carries %s, nothing declared to compare against", humanCaptions(got)),
			})
		default:
			out = append(out, finding.Finding{
				Check: "captions", Target: label, Status: finding.OK,
				Message: "no captions declared and none in the bitstream, as declared",
			})
		}
	}
	return out
}

// captionsSeen unions the caption data found across the sampled segments, and
// reports whether any of them could be walked at all. One segment out of six
// carrying captions still means the rendition has them.
func captionsSeen(rd *renditionData) (media.CaptionPresence, bool) {
	var out media.CaptionPresence
	scanned := false
	for _, sd := range parsedSegs(rd) {
		t, ok := sd.info.Track(media.Video)
		if !ok {
			continue
		}
		if !t.Captions.Scanned {
			continue
		}
		scanned = true
		out.Field1 = out.Field1 || t.Captions.Field1
		out.Field2 = out.Field2 || t.Captions.Field2
		out.Track608 = out.Track608 || t.Captions.Track608
		out.Track708 = out.Track708 || t.Captions.Track708
		for _, svc := range t.Captions.Services {
			if !containsInt(out.Services, svc) {
				out.Services = append(out.Services, svc)
			}
		}
	}
	sort.Ints(out.Services)
	return out, scanned
}

func anyCaptionsFound(rd *renditionData) bool {
	got, _ := captionsSeen(rd)
	return got.Any()
}

// captionCouldBePresent reports whether an INSTREAM-ID may be satisfied by what
// was found. For CEA-608 that is deliberately weaker than "is present": CC1 and
// CC3 share field 1, so a populated field cannot be attributed to one of them and
// must not be called a defect for the other.
func captionCouldBePresent(id string, got media.CaptionPresence) bool {
	switch strings.ToUpper(id) {
	case "CC1", "CC3":
		return got.Field1 || got.Track608
	case "CC2", "CC4":
		return got.Field2 || got.Track608
	}
	if n, ok := captionServiceNumber(id); ok {
		return containsInt(got.Services, n) || got.Track708
	}
	// An INSTREAM-ID this check does not understand is not evidence of anything.
	return true
}

// captionServiceNumber reads the number out of a CEA-708 INSTREAM-ID.
func captionServiceNumber(id string) (int, bool) {
	rest, ok := strings.CutPrefix(strings.ToUpper(id), "SERVICE")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 || n > 63 {
		return 0, false
	}
	return n, true
}

// captionList names the declared channels, in the manifest's own vocabulary.
func captionList(cs []manifest.Caption) string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.InstreamID)
	}
	return strings.Join(ids, ", ")
}

// humanCaptions describes what was found the way the standards name it: CEA-608
// by field, because that is as far as a reader can honestly go, and CEA-708 by
// service number, because the packet layer states it.
func humanCaptions(got media.CaptionPresence) string {
	var parts []string
	switch {
	case got.Field1 && got.Field2:
		parts = append(parts, "CEA-608 fields 1 and 2")
	case got.Field1:
		parts = append(parts, "CEA-608 field 1 (CC1/CC3)")
	case got.Field2:
		parts = append(parts, "CEA-608 field 2 (CC2/CC4)")
	}
	if len(got.Services) > 0 {
		svcs := make([]string, 0, len(got.Services))
		for _, n := range got.Services {
			svcs = append(svcs, fmt.Sprintf("SERVICE%d", n))
		}
		parts = append(parts, "CEA-708 "+strings.Join(svcs, "/"))
	}
	// A CMAF caption track names its standard and no more. Saying which channel it
	// carries would be inventing a measurement, so the report says what is known.
	if got.Track608 && !got.Attributable() {
		parts = append(parts, "a populated CEA-608 caption track (channel not attributable)")
	}
	if got.Track708 && !got.Attributable() {
		parts = append(parts, "a populated CEA-708 caption track (service not attributable)")
	}
	if len(parts) == 0 {
		return "no caption data"
	}
	return strings.Join(parts, " and ")
}

func containsInt(xs []int, n int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}

// audioCodecNames maps the RFC 6381 CODECS prefixes to the names the media
// readers report. A CODECS value carries a profile the bitstream does not state
// ("mp4a.40.2"), so only the sample-entry-shaped prefix is comparable.
var audioCodecNames = map[string]string{
	"mp4a": "aac",
	"ac-3": "ac3",
	"ec-3": "eac3",
	"ac-4": "ac4",
	"opus": "opus",
	"flac": "flac",
	"alac": "alac",
	"dtsc": "dts", "dtse": "dts", "dtsh": "dts", "dtsl": "dts",
	"mp4a.6b": "mp3", "mp4a.69": "mp3",
}

// declaredAudioCodec picks the audio codec out of a CODECS value. A video
// variant's CODECS lists its video codec alongside its audio one, and a value
// naming no audio codec at all — or more than one, which no single rendition can
// honour — states nothing this check can compare.
func declaredAudioCodec(codecs string) (name, as string, ok bool) {
	var found, raw string
	for _, c := range strings.Split(codecs, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// The longer keys are MP3 object types, which share the mp4a prefix, so
		// they are tried before it.
		got := ""
		for _, key := range []string{"mp4a.6b", "mp4a.69"} {
			if strings.HasPrefix(strings.ToLower(c), key) {
				got = audioCodecNames[key]
			}
		}
		if got == "" && len(c) >= 4 {
			got = audioCodecNames[strings.ToLower(c[:4])]
		}
		if got == "" {
			continue // a video codec, or one this table does not know
		}
		if found != "" && found != got {
			return "", "", false // two audio codecs declared: neither is the claim
		}
		found, raw = got, c
	}
	return found, raw, found != ""
}

// ratesAgree compares a coded sampling rate against a declared one.
//
// They may legitimately differ by exactly a factor of two. HE-AAC codes the
// signal at half rate and rebuilds the top octave with Spectral Band Replication,
// so a track whose AudioSampleEntry says 24 kHz outputs 48 kHz — and DASH's
// @audioSamplingRate states the output rate. Sony's DASH-IF reference stream is
// exactly this shape, and treating it as a mismatch would flag a large share of
// the world's AAC audio as broken.
func ratesAgree(coded, declared int, codecs string) bool {
	if coded == declared {
		return true
	}
	return codecSignalsSBR(codecs) && declared == coded*2
}

// codecSignalsSBR reports whether a CODECS value names an AAC profile that uses
// Spectral Band Replication: object type 5 is HE-AAC, 29 is HE-AAC v2. RFC 6381
// allows the object type to be zero-padded, so "mp4a.40.05" means the same thing
// as "mp4a.40.5".
func codecSignalsSBR(codecs string) bool {
	for _, c := range strings.Split(codecs, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		rest, ok := strings.CutPrefix(c, "mp4a.40.")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		if n == 5 || n == 29 {
			return true
		}
	}
	return false
}

// hasAudioTrack reports whether any sampled segment carried an audio track at
// all. Without one there is no audio to be sane or insane about.
func hasAudioTrack(rd *renditionData) bool {
	for _, sd := range parsedSegs(rd) {
		if _, ok := sd.info.Track(media.Audio); ok {
			return true
		}
	}
	return false
}

// humanAudioFormat names a rate and a layout together, skipping whichever half
// the media did not state.
func humanAudioFormat(rate, channels int) string {
	switch {
	case rate > 0 && channels > 0:
		return humanSampleRate(rate) + " " + humanChannels(channels)
	case rate > 0:
		return humanSampleRate(rate)
	case channels > 0:
		return humanChannels(channels)
	}
	return "unknown format"
}

// humanSampleRate writes a rate the way an operator says it: 48 kHz, 44.1 kHz.
func humanSampleRate(hz int) string {
	return fmt.Sprintf("%g kHz", float64(hz)/1000)
}

// humanChannels names the common layouts. A count with no common name is
// reported as a count rather than guessed at.
func humanChannels(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return fmt.Sprintf("%d channels", n)
	}
}

func humanFPS(fps float64) string {
	if math.Abs(fps-math.Round(fps)) < 0.005 {
		return fmt.Sprintf("%.0ffps", fps)
	}
	return fmt.Sprintf("%.2ffps", fps)
}

// checkEncryption reports disagreements between the manifest's declared
// protection and what the segments carry.
func checkEncryption(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		var declared, observed, total int
		for _, sd := range parsedSegs(rd) {
			total++
			if sd.seg.KeyMethod != "" {
				declared++
			}
			if sd.info.Encrypted() {
				observed++
			}
		}
		if total == 0 {
			continue
		}
		label := rendLabel(rd)
		switch {
		case declared == 0 && observed > 0:
			out = append(out, finding.Finding{
				Check: "encryption", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("%d/%d segments are encrypted but the manifest declares no key", observed, total),
				Hint:    "without EXT-X-KEY / ContentProtection a player has no way to obtain the key: playback fails",
			})
		case declared > 0 && declared < total:
			out = append(out, finding.Finding{
				Check: "encryption", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("only %d/%d segments declare a key: protection changes mid-rendition", declared, total),
			})
		case declared > 0:
			out = append(out, finding.Finding{
				Check: "encryption", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("all %d sampled segments declare a key", total),
			})
		}
	}
	return out
}

// checkAlignment compares where the same segment index starts in different
// video renditions. ABR switching only works if the renditions share one
// timeline: misaligned boundaries make every switch glitch, and this is the
// classic defect of a multi-encoder setup without a shared clock.
func checkAlignment(rends []*renditionData, opts Options) []finding.Finding {
	type point struct {
		label string
		start float64
	}
	bySeq := map[int][]point{}
	videoRends := 0
	for _, rd := range rends {
		if rd.err != nil || rd.r.Kind != manifest.Video {
			continue
		}
		videoRends++
		for _, sd := range parsedSegs(rd) {
			t, ok := sd.info.Timeline()
			if !ok || t.Timescale == 0 {
				continue
			}
			bySeq[sd.seg.Sequence] = append(bySeq[sd.seg.Sequence], point{rendLabel(rd), toSec(t.MinPTS, t.Timescale)})
		}
	}
	if videoRends < 2 {
		return nil
	}

	tolSec := opts.GapToleranceMS / 1000
	var out []finding.Finding
	compared, misaligned := 0, 0
	for _, seq := range sortedIntKeys(bySeq) {
		pts := bySeq[seq]
		if len(pts) < 2 {
			continue
		}
		compared++
		min, max := pts[0], pts[0]
		for _, p := range pts[1:] {
			if p.start < min.start {
				min = p
			}
			if p.start > max.start {
				max = p
			}
		}
		spread := max.start - min.start
		if spread <= tolSec {
			continue
		}
		misaligned++
		out = append(out, finding.Finding{
			Check: "alignment", Target: fmt.Sprintf("seq %d", seq), Status: finding.BAD,
			Message: fmt.Sprintf("renditions start %s apart at the same segment index (%s at %.3fs, %s at %.3fs)",
				signedMS(spread), min.label, min.start, max.label, max.start),
			Value: finding.Num(spread * 1000), Unit: "ms",
			Hint: "the renditions are not on a shared timeline: every ABR switch here glitches",
		})
	}
	if compared > 0 && misaligned == 0 {
		out = append(out, finding.Finding{
			Check: "alignment", Target: "ladder", Status: finding.OK,
			Message: fmt.Sprintf("renditions aligned at %d shared segment indexes (tolerance %.0fms)", compared, opts.GapToleranceMS),
		})
	}
	return out
}

// checkLadder inspects the shape of the ladder itself, from the manifest alone.
func checkLadder(pl manifest.Playlist) []finding.Finding {
	if !pl.Master {
		return nil
	}
	video := pl.VideoRenditions()
	if len(video) == 0 {
		return []finding.Finding{{
			Check: "ladder", Target: shortTarget(pl.URL), Status: finding.BAD,
			Message: "no video rendition in the manifest",
		}}
	}
	var out []finding.Finding

	// Duplicate rungs: two renditions at the same resolution and bitrate are
	// bandwidth a player can never use.
	seen := map[string][]string{}
	for _, r := range video {
		key := fmt.Sprintf("%dx%d@%d", r.Width, r.Height, r.Bandwidth)
		seen[key] = append(seen[key], r.Name)
	}
	for key, names := range seen {
		if len(names) > 1 && !strings.HasPrefix(key, "0x0@") {
			out = append(out, finding.Finding{
				Check: "ladder", Target: strings.Join(names, ","), Status: finding.WARN,
				Message: fmt.Sprintf("%d renditions share resolution and bitrate (%s)", len(names), key),
				Hint:    "a duplicate rung adds no adaptivity",
			})
		}
	}

	// A higher resolution at a lower bitrate inverts the ladder: the player's
	// bitrate-driven choice then lowers picture quality.
	sorted := append([]manifest.Rendition{}, video...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Bandwidth < sorted[j-1].Bandwidth; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for i := 1; i < len(sorted); i++ {
		lo, hi := sorted[i-1], sorted[i]
		if lo.Height > 0 && hi.Height > 0 && hi.Height < lo.Height {
			out = append(out, finding.Finding{
				Check: "ladder", Target: hi.Name, Status: finding.WARN,
				Message: fmt.Sprintf("%s (%dp) costs more bandwidth than %s (%dp) but is lower resolution", hi.Name, hi.Height, lo.Name, lo.Height),
				Hint:    "the ladder is inverted at this rung",
			})
		}
	}

	// An AUDIO group a variant points at must exist, or the variant plays mute.
	groups := map[string]bool{}
	for _, r := range pl.Renditions {
		if r.GroupID != "" {
			groups[r.GroupID] = true
		}
	}
	for _, r := range video {
		if r.AudioGroup != "" && !groups[r.AudioGroup] {
			out = append(out, finding.Finding{
				Check: "ladder", Target: r.Name, Status: finding.BAD,
				Message: fmt.Sprintf("AUDIO group %q is referenced but never defined by an EXT-X-MEDIA entry", r.AudioGroup),
				Hint:    "the variant will play without audio",
			})
		}
	}

	missingCodecs := 0
	for _, r := range video {
		if r.Codecs == "" {
			missingCodecs++
		}
	}
	if missingCodecs > 0 {
		out = append(out, finding.Finding{
			Check: "ladder", Target: shortTarget(pl.URL), Status: finding.WARN,
			Message: fmt.Sprintf("%d/%d video renditions declare no CODECS", missingCodecs, len(video)),
			Hint:    "without CODECS a player must download a segment before it knows whether it can decode the rendition",
		})
	}

	if len(out) == 0 {
		out = append(out, finding.Finding{
			Check: "ladder", Target: shortTarget(pl.URL), Status: finding.OK,
			Message: fmt.Sprintf("%d video renditions, %s", len(video), describeLadder(video)),
		})
	}
	return out
}

// ---------- helpers ----------

func parsedSegs(rd *renditionData) []segmentData {
	var out []segmentData
	for _, sd := range rd.segs {
		if sd.parsed {
			out = append(out, sd)
		}
	}
	return out
}

func rendLabel(rd *renditionData) string {
	if rd.r.Name != "" {
		if rd.r.Kind == manifest.Audio && !strings.HasPrefix(rd.r.Name, "audio") {
			return "audio " + rd.r.Name
		}
		return rd.r.Name
	}
	return shortTarget(rd.r.URI)
}

func segLabel(rd *renditionData, sd segmentData) string {
	return fmt.Sprintf("%s seg %d", rendLabel(rd), sd.seg.Sequence)
}

func toSec(ticks int64, timescale uint32) float64 {
	if timescale == 0 {
		return 0
	}
	return float64(ticks) / float64(timescale)
}

// signedMS renders a drift the way an operator reads it: milliseconds under a
// second, seconds above.
func signedMS(sec float64) string {
	if math.Abs(sec) < 1 {
		return fmt.Sprintf("%+.0fms", sec*1000)
	}
	return fmt.Sprintf("%+.3fs", sec)
}

func isFullSegmentEncryption(method string) bool {
	switch strings.ToUpper(method) {
	case "AES-128", "AES-256":
		return true
	}
	return false
}

func trackShape(info media.SegmentInfo) string {
	var parts []string
	for _, kind := range []media.TrackKind{media.Video, media.Audio, media.Other} {
		n := 0
		for _, t := range info.Tracks {
			if t.Kind == kind {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, kind))
		}
	}
	if len(parts) == 0 {
		return "no track"
	}
	return strings.Join(parts, " + ")
}

// declaredCodec maps an RFC 6381 CODECS attribute to the codec name the parsers
// produce, for the given track kind.
func declaredCodec(codecs string, kind media.TrackKind) (string, bool) {
	for _, c := range strings.Split(codecs, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		var name string
		var k media.TrackKind
		switch {
		case strings.HasPrefix(c, "avc1"), strings.HasPrefix(c, "avc3"):
			name, k = "h264", media.Video
		case strings.HasPrefix(c, "hvc1"), strings.HasPrefix(c, "hev1"):
			name, k = "hevc", media.Video
		case strings.HasPrefix(c, "av01"):
			name, k = "av1", media.Video
		case strings.HasPrefix(c, "vp09"), strings.HasPrefix(c, "vp9"):
			name, k = "vp9", media.Video
		case strings.HasPrefix(c, "mp4a"):
			name, k = "aac", media.Audio
		case strings.HasPrefix(c, "ac-3"):
			name, k = "ac3", media.Audio
		case strings.HasPrefix(c, "ec-3"):
			name, k = "eac3", media.Audio
		case strings.HasPrefix(c, "opus"):
			name, k = "opus", media.Audio
		default:
			continue
		}
		if k == kind {
			return name, true
		}
	}
	return "", false
}

func describeCounts(m map[string]int) string {
	var parts []string
	for _, k := range sortedStringKeys(m) {
		parts = append(parts, fmt.Sprintf("%s×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func describeLadder(video []manifest.Rendition) string {
	var parts []string
	for _, r := range video {
		if r.Height > 0 {
			parts = append(parts, fmt.Sprintf("%dp", r.Height))
		} else if r.Bandwidth > 0 {
			parts = append(parts, fmt.Sprintf("%dk", r.Bandwidth/1000))
		}
	}
	return strings.Join(parts, "/")
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KiB", "MiB", "GiB"}
	v := float64(b)
	for _, u := range units {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f TiB", v/unit)
}

func humanBitrate(bps float64) string {
	switch {
	case bps >= 1e6:
		return fmt.Sprintf("%.2f Mbps", bps/1e6)
	case bps >= 1e3:
		return fmt.Sprintf("%.0f kbps", bps/1e3)
	default:
		return fmt.Sprintf("%.0f bps", bps)
	}
}

func sortedIntKeys[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortedStringKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
