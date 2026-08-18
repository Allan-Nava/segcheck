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

// A trick-play rung is a playlist of byte ranges, each supposed to hold exactly
// one keyframe of the video it belongs to. Both halves of that are claims only
// the media can settle: the offsets are arithmetic a packager did, and nothing
// in the manifest says whether the arithmetic was right.
//
// When it is wrong the scrub preview shows a grey frame, or the same picture
// over and over, or a moment from somewhere else entirely — and it is reported
// as a player bug, because the playlist reads perfectly.
//
// The rung is kept out of `rends` entirely rather than filtered out of each
// check in turn. Every check that reads a segment as an extent of media would
// be wrong about it: one picture where two seconds are expected is a hole in
// the timeline, a duration mismatch and a bitrate ten times the declared. That
// is the same trap subtitle renditions sprang, and this is the cheaper way out
// of it.

// iframeData is one trick-play rung and the entries sampled from it.
type iframeData struct {
	r    manifest.Rendition
	segs []segmentData
	err  error
	// initErr is set when the rung's initialisation segment could not be
	// fetched; without it an fMP4 entry cannot be read at all.
	initErr error
}

// chooseIFrames is the trick-play rungs a run inspects, capped like every other
// kind. They are cheap — one picture per entry — but a ladder can carry one per
// resolution.
func chooseIFrames(pl manifest.Playlist, opts Options) []manifest.Rendition {
	return pick(byKind(pl.Renditions, manifest.IFrame), opts.MaxIFrame)
}

// sampleIFrames loads each selected trick-play playlist and fetches the entries
// the sampling window picks out.
func sampleIFrames(ctx context.Context, c *fetch.Client, pl manifest.Playlist, opts Options) []*iframeData {
	if opts.MaxIFrame == 0 {
		return nil
	}
	chosen := chooseIFrames(pl, opts)
	if len(chosen) == 0 {
		return nil
	}

	out := make([]*iframeData, 0, len(chosen))
	for _, r := range chosen {
		id := &iframeData{r: r}
		sub, err := loadMediaPlaylist(ctx, c, r.URI)
		if err != nil {
			id.err = err
		} else {
			id.segs = toSegmentData(sampleSegments(sub.Segments, sub.Live, opts))
		}
		out = append(out, id)
	}

	conc := opts.Concurrency
	if conc <= 0 {
		conc = 1
	}

	// One initialisation segment per distinct reference, fetched once. The byte
	// range matters: Apple's own trick-play playlists put the init in the first
	// few hundred bytes of the very file the entries index into, so fetching the
	// whole resource downloads the asset and then fails to parse it as an init.
	inits := map[initRef][]byte{}
	for _, id := range out {
		for _, sd := range id.segs {
			ref := segInitRef(sd.seg)
			if ref.empty {
				continue
			}
			if _, done := inits[ref]; done {
				break
			}
			resp, err := c.Get(ctx, ref.uri, ref.rng)
			if err != nil {
				id.initErr = fmt.Errorf("initialisation segment %s not fetched: %w", ref.uri, err)
				break
			}
			inits[ref] = resp.Body
			break
		}
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, id := range out {
		for i := range id.segs {
			wg.Add(1)
			go func(id *iframeData, i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				sd := &id.segs[i]
				rangeHeader := ""
				if sd.seg.ByteRange != nil {
					rangeHeader = sd.seg.ByteRange.Header()
				}
				resp, err := c.Get(ctx, sd.seg.URI, rangeHeader)
				sd.res = resp
				if err != nil {
					sd.fetchErr = err
					return
				}
				info, perr := media.Parse(resp.Body, inits[segInitRef(sd.seg)])
				if perr != nil {
					sd.parseErr = perr
					return
				}
				sd.info = info
				sd.parsed = true
			}(id, i)
		}
	}
	wg.Wait()
	return out
}

// checkIFrame settles the two claims a trick-play rung makes: that every range
// resolves to a keyframe, and that the rung sits on the same timeline as the
// video it belongs to.
func checkIFrame(ifs []*iframeData, rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	for _, id := range ifs {
		label := rendLabel(id.r)
		if id.err != nil {
			out = append(out, finding.Finding{
				Check: "iframe", Target: label, Status: finding.ERROR,
				Message: fmt.Sprintf("trick-play playlist could not be read: %v", id.err),
			})
			continue
		}
		if len(id.segs) == 0 {
			out = append(out, finding.Finding{
				Check: "iframe", Target: label, Status: finding.ERROR,
				Message: "trick-play playlist lists no entries",
				Hint:    "a scrub has nothing to preview with",
			})
			continue
		}

		out = append(out, iframeDeliveryFindings(id, label)...)
		out = append(out, iframeKeyframeFindings(id, label)...)
		out = append(out, iframeTimelineFindings(id, label, rends, opts)...)
	}
	return out
}

func iframeDeliveryFindings(id *iframeData, label string) []finding.Finding {
	var unfetched, unparsed []segmentData
	for _, sd := range id.segs {
		switch {
		case sd.fetchErr != nil:
			unfetched = append(unfetched, sd)
		case sd.parseErr != nil:
			unparsed = append(unparsed, sd)
		}
	}
	var out []finding.Finding
	if len(unfetched) > 0 {
		out = append(out, finding.Finding{
			Check: "iframe", Target: label, Status: finding.BAD,
			Message: fmt.Sprintf("%d of %d trick-play ranges not fetched: %v", len(unfetched), len(id.segs), unfetched[0].fetchErr),
			Value:   finding.Num(float64(len(unfetched))), Unit: "entries",
			Hint: "a scrub across this part of the timeline shows nothing at all",
		})
	}
	if len(unparsed) > 0 {
		out = append(out, finding.Finding{
			Check: "iframe", Target: label, Status: finding.BAD,
			Message: fmt.Sprintf("%d of %d trick-play ranges are not readable media: %v", len(unparsed), len(id.segs), unparsed[0].parseErr),
			Value:   finding.Num(float64(len(unparsed))), Unit: "entries",
			Hint: "the byte offsets are arithmetic the packager did, and they landed somewhere that is not the start of a picture",
		})
	}
	return out
}

// iframeKeyframeFindings settles "every range is a keyframe, and nothing else".
//
// It follows the same evidence rule as the Apple IDR profile rule: a container
// that states the first sample is not a sync sample is an assertion, a
// completed bitstream walk that found no random access point is an assertion,
// and a walk that found one somewhere other than first is not settled evidence.
func iframeKeyframeFindings(id *iframeData, label string) []finding.Finding {
	verified, bad, unsettled, multi := 0, 0, 0, 0
	var first segmentData
	for _, sd := range id.segs {
		if !sd.parsed {
			continue
		}
		t, ok := sd.info.Track(media.Video)
		if !ok {
			continue
		}
		// "and to nothing else": where the container states a sample count, an
		// entry holding more than one picture is a range that swept up the media
		// after the keyframe too, which makes every scrub download more than it
		// needs.
		if t.Samples > 1 {
			multi++
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

	var out []finding.Finding
	if bad > 0 {
		out = append(out, finding.Finding{
			Check: "iframe", Target: fmt.Sprintf("%s entry %d", label, first.seg.Sequence), Status: finding.BAD,
			Message: fmt.Sprintf("%d of %d trick-play ranges do not resolve to a keyframe", bad, verified),
			Value:   finding.Num(float64(bad)), Unit: "entries",
			Hint: "the scrub preview shows a grey frame or the last decodable picture; the playlist is well-formed, so nothing but the media says otherwise",
		})
	}
	if multi > 0 {
		out = append(out, finding.Finding{
			Check: "iframe", Target: label, Status: finding.WARN,
			Message: fmt.Sprintf("%d of %d trick-play ranges carry more than one picture", multi, len(id.segs)),
			Value:   finding.Num(float64(multi)), Unit: "entries",
			Hint: "an I-frame range is meant to hold one keyframe; the extra pictures are downloaded on every scrub and thrown away",
		})
	}
	if verified == 0 && bad == 0 {
		// The ranges arrived and parsed, and nothing in them settled the question.
		// A byte range into the middle of a transport stream carries no PAT or PMT,
		// so a reader cannot tell what stream type it is looking at — a limit of
		// segcheck, not a defect in the stream, and one that has to be said out
		// loud because silence here reads as a clean pass.
		parsed := 0
		for _, sd := range id.segs {
			if sd.parsed {
				parsed++
			}
		}
		if parsed > 0 {
			out = append(out, finding.Finding{
				Check: "iframe", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%d ranges fetched and parsed, but segcheck could not read a picture out of any of them: not verified", parsed),
				Hint:    "a byte range into the middle of a transport stream carries no PAT or PMT, so nothing says what stream type it holds",
			})
		}
		return out
	}
	if bad == 0 && verified > 0 {
		msg := fmt.Sprintf("all %d sampled ranges resolve to a keyframe", verified)
		if unsettled > 0 {
			msg = fmt.Sprintf("%d of %d sampled ranges resolve to a keyframe; %d carry one that is not the first coded picture, which in decode order is not settled evidence either way",
				verified-unsettled, verified, unsettled)
		}
		out = append(out, finding.Finding{
			Check: "iframe", Target: label, Status: finding.OK,
			Message: msg,
			Value:   finding.Num(float64(verified)), Unit: "entries",
		})
	}
	return out
}

// iframeTimelineFindings settles "the rung spans the same timeline as the video
// it belongs to". A trick-play rung that is internally perfect and half a
// minute adrift sends every scrub to the wrong moment.
func iframeTimelineFindings(id *iframeData, label string, rends []*renditionData, opts Options) []finding.Finding {
	ifStart, ok := firstMediaStart(id.segs)
	if !ok {
		return nil
	}
	// The video rung whose resolution the trick-play rung claims, falling back to
	// any video rung: they share a timeline, so any of them answers the question.
	var videoStart float64
	var videoLabel string
	found := false
	for _, rd := range rends {
		if rd.err != nil || rd.r.Kind != manifest.Video {
			continue
		}
		s, ok := firstMediaStart(rd.segs)
		if !ok {
			continue
		}
		if !found || (rd.r.Width == id.r.Width && rd.r.Height == id.r.Height) {
			videoStart, videoLabel, found = s, rendLabel(rd.r), true
		}
	}
	if !found {
		return nil
	}

	// One segment of slack: the trick-play entries and the video segments are
	// sampled from the same window, so their first entries describe the same
	// moment give or take one keyframe interval.
	slack := 2.0
	for _, rd := range rends {
		if durs := measuredDurations(rd); len(durs) > 0 {
			slack = medianFloat(durs)
			break
		}
	}
	drift := ifStart - videoStart
	if math.Abs(drift) <= slack {
		return []finding.Finding{{
			Check: "iframe", Target: label, Status: finding.OK,
			Message: fmt.Sprintf("on the same timeline as %s: first keyframe at %.3fs against the video's %.3fs", videoLabel, ifStart, videoStart),
			Value:   finding.Num(drift * 1000), Unit: "ms",
		}}
	}
	return []finding.Finding{{
		Check: "iframe", Target: label, Status: finding.BAD,
		Message: fmt.Sprintf("trick-play rung is on a different timeline from %s: its first keyframe is at %.3fs where the video starts at %.3fs (%s)",
			videoLabel, ifStart, videoStart, signedMS(drift)),
		Value: finding.Num(drift * 1000), Unit: "ms",
		Hint: "every scrub previews one moment and seeks to another; the offsets are right and they point into the wrong part of the timeline",
	}}
}

func firstMediaStart(segs []segmentData) (float64, bool) {
	for _, sd := range segs {
		if !sd.parsed {
			continue
		}
		t, ok := sd.info.Timeline()
		if !ok || t.Timescale == 0 {
			continue
		}
		return toSec(t.MinPTS, t.Timescale), true
	}
	return 0, false
}
