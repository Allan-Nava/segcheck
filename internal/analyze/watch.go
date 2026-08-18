package analyze

import (
	"context"
	"fmt"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
)

// The live edge is the one claim a single-shot check cannot arbitrate. A
// packager that stopped publishing an hour ago still serves a playlist whose
// every segment downloads, parses and lines up perfectly — the stream is
// flawless and frozen. Only a second look, a TARGETDURATION later, tells the
// two apart, which is what --watch is: re-read the manifest on the interval it
// says a player would, and report what the edge did in between.

// defaultPollInterval is the re-read interval for a playlist that states none:
// no HLS TARGETDURATION, no DASH @minimumUpdatePeriod, no segment duration to
// infer one from. Six seconds is a common segment length and a poll that is
// wrong in that direction only costs a request.
const defaultPollInterval = 6 * time.Second

// minPollInterval keeps a low-latency ladder from being polled as fast as its
// parts arrive. Below a second the loop measures the network rather than the
// packager.
const minPollInterval = time.Second

// edgeState is where one rendition's live edge stood at one moment.
//
// newest is the URI of the last segment in the window, and it is the edge's
// identity on purpose: a media sequence number is an HLS idea, a DASH
// SegmentTimeline index is renumbered every time the window slides, and the URI
// is the one thing both formats change when — and only when — a new segment is
// published.
type edgeState struct {
	name   string  // the rendition, as a finding target
	newest string  // URI of the newest segment
	count  int     // segments in the window
	span   float64 // their total declared duration, in seconds
	target float64 // the re-read interval the manifest implies, 0 when it states none
	err    error   // this rendition's playlist could not be re-read
	// uris and durs are the whole window, in order. They are what makes the
	// distance between two polls measurable rather than merely visible: finding
	// the previous poll's newest segment in this one and summing what follows it
	// is how much media the packager published in between, and comparing that
	// with the wall clock is the only thing that shows an edge advancing every
	// time it is looked at and still losing ground.
	uris []string
	durs []float64
}

// observation is one poll of the whole manifest.
type observation struct {
	at    time.Time
	edges []edgeState
	err   error // the manifest itself did not load, so no edge was read at all
}

// watchLiveEdge re-reads the manifest for opts.Watch and reports what each
// rendition's live edge did: whether it advanced, how far apart new segments
// arrived, and whether it ever stopped.
func watchLiveEdge(ctx context.Context, c *fetch.Client, rawurl string, pl manifest.Playlist, opts Options) []finding.Finding {
	if !pl.Live {
		return []finding.Finding{{
			Check: "watch", Target: shortTarget(rawurl), Status: finding.OK,
			Message: "playlist is VOD: there is no live edge to watch",
			Hint:    "--watch only has something to observe on a live stream",
		}}
	}

	series := []observation{pollEdges(ctx, c, rawurl, opts)}
	interval, stated := pollInterval(pl, series[0])
	if interval > opts.Watch {
		interval = opts.Watch
	}

	start := opts.Now()
	for {
		elapsed := opts.Now().Sub(start)
		if elapsed >= opts.Watch {
			break
		}
		wait := interval
		if remaining := opts.Watch - elapsed; wait > remaining {
			wait = remaining
		}
		if err := opts.Sleep(ctx, wait); err != nil {
			break // cancelled: report on what was seen rather than nothing
		}
		series = append(series, pollEdges(ctx, c, rawurl, opts))
	}

	return watchFindings(rawurl, series, interval, stated, opts)
}

// pollEdges reads the manifest and every selected rendition's playlist once,
// and returns where each live edge stands. It deliberately fetches no media:
// the segments were sampled at the start of the run, and downloading them again
// on every poll would turn a check into a load test.
func pollEdges(ctx context.Context, c *fetch.Client, rawurl string, opts Options) observation {
	obs := observation{at: opts.Now()}

	resp, err := c.Get(ctx, rawurl, "")
	if err != nil {
		obs.err = fmt.Errorf("manifest not reachable: %w", err)
		return obs
	}
	var pl manifest.Playlist
	if manifest.Detect(rawurl, resp.ContentType(), resp.Body) == manifest.KindDASH {
		pl, err = manifest.ParseDASH(resp.Body, rawurl, opts.Now())
	} else {
		pl, err = manifest.ParseHLS(resp.Body, rawurl)
	}
	if err != nil {
		obs.err = err
		return obs
	}

	if !pl.Master {
		obs.edges = append(obs.edges, edgeOf("media", pl.TargetDuration, pl.Segments))
		return obs
	}
	for _, r := range chooseRenditions(pl, opts).all() {
		switch {
		case r.Unsupported != "":
			// segcheck could not expand this rendition in the first place; it has
			// no edge to watch and saying so once per poll would be noise.
		case len(r.Segments) > 0: // DASH: the MPD lists them
			obs.edges = append(obs.edges, edgeOf(rendLabel(r), pl.TargetDuration, r.Segments))
		case r.SingleFile:
			// One file with its index inside it. It has no live edge by construction.
		case r.URI != "":
			sub, err := loadMediaPlaylist(ctx, c, r.URI)
			if err != nil {
				obs.edges = append(obs.edges, edgeState{name: rendLabel(r), err: err})
				continue
			}
			obs.edges = append(obs.edges, edgeOf(rendLabel(r), sub.TargetDuration, sub.Segments))
		}
	}
	return obs
}

func edgeOf(name string, target float64, segs []manifest.Segment) edgeState {
	e := edgeState{name: name, target: target, count: len(segs)}
	for _, s := range segs {
		e.span += s.Duration
		e.uris = append(e.uris, s.URI)
		e.durs = append(e.durs, s.Duration)
	}
	if len(segs) > 0 {
		e.newest = segs[len(segs)-1].URI
	}
	return e
}

// pollInterval is how often to re-read, taken from what the manifest itself
// says a player would do: TARGETDURATION in HLS, @minimumUpdatePeriod in DASH.
// Failing both, the segments' own declared duration is the best evidence of how
// often a new one appears — a manifest that says every segment is two seconds
// long is saying one should show up about every two seconds.
//
// The bool is whether the manifest stated anything at all. False means the
// returned interval is segcheck's own default, which is fine to poll on and
// must never be used to judge the stream: a gap measured against an invented
// interval is an invented measurement.
func pollInterval(pl manifest.Playlist, first observation) (time.Duration, bool) {
	secs := pl.TargetDuration
	if pl.UpdatePeriod > 0 && (secs == 0 || pl.UpdatePeriod < secs) {
		secs = pl.UpdatePeriod
	}
	for _, e := range first.edges {
		if e.target > secs {
			secs = e.target
		}
	}
	if secs <= 0 {
		for _, e := range first.edges {
			if e.count > 0 && e.span/float64(e.count) > secs {
				secs = e.span / float64(e.count)
			}
		}
	}
	if secs <= 0 {
		return defaultPollInterval, false
	}
	d := time.Duration(secs * float64(time.Second))
	if d < minPollInterval {
		return minPollInterval, true
	}
	return d, true
}

// edgePoint is one rendition's edge at one moment, lifted out of the
// observation series so each rendition can be judged on its own.
type edgePoint struct {
	at     time.Time
	newest string
	target float64
	err    error
	uris   []string
	durs   []float64
}

// watchFindings turns the series of observations into findings, one rendition
// at a time.
func watchFindings(rawurl string, series []observation, interval time.Duration, stated bool, opts Options) []finding.Finding {
	var out []finding.Finding

	// A manifest that stopped loading part-way through is a hole in the
	// coverage, not a verdict about the edge, so it is reported as one.
	failed := 0
	for _, o := range series {
		if o.err != nil {
			failed++
		}
	}
	if failed == len(series) {
		return append(out, finding.Finding{
			Check: "watch", Target: shortTarget(rawurl), Status: finding.ERROR,
			Message: fmt.Sprintf("the manifest could not be re-read during the %s watch: %v", opts.Watch, series[0].err),
			Value:   finding.Num(float64(failed)), Unit: "polls",
		})
	}
	if failed > 0 {
		out = append(out, finding.Finding{
			Check: "watch", Target: shortTarget(rawurl), Status: finding.ERROR,
			Message: fmt.Sprintf("%d of %d manifest re-reads failed: %v", failed, len(series), firstPollError(series)),
			Value:   finding.Num(float64(failed)), Unit: "polls",
			Hint: "the edge below was judged on the polls that did succeed",
		})
	}

	byRendition := map[string][]edgePoint{}
	var order []string
	for _, o := range series {
		if o.err != nil {
			continue
		}
		for _, e := range o.edges {
			if _, seen := byRendition[e.name]; !seen {
				order = append(order, e.name)
			}
			byRendition[e.name] = append(byRendition[e.name], edgePoint{
				at: o.at, newest: e.newest, target: e.target, err: e.err, uris: e.uris, durs: e.durs,
			})
		}
	}
	if len(order) == 0 {
		return append(out, finding.Finding{
			Check: "watch", Target: shortTarget(rawurl), Status: finding.ERROR,
			Message: fmt.Sprintf("no rendition had a live edge to watch over %s", opts.Watch),
		})
	}
	for _, name := range order {
		out = append(out, edgeFindings(name, byRendition[name], interval, stated, opts)...)
	}
	return out
}

func firstPollError(series []observation) error {
	for _, o := range series {
		if o.err != nil {
			return o.err
		}
	}
	return nil
}

// edgeFindings judges one rendition's edge over the whole window.
func edgeFindings(name string, points []edgePoint, interval time.Duration, stated bool, opts Options) []finding.Finding {
	var out []finding.Finding

	var good []edgePoint
	for _, p := range points {
		if p.err == nil {
			good = append(good, p)
		}
	}
	if len(good) == 0 {
		return append(out, finding.Finding{
			Check: "watch", Target: name, Status: finding.ERROR,
			Message: fmt.Sprintf("playlist could not be re-read during the %s watch: %v", opts.Watch, points[0].err),
		})
	}
	if len(good) < 2 {
		return append(out, finding.Finding{
			Check: "watch", Target: name, Status: finding.OK,
			Message: fmt.Sprintf("only one look at the live edge in %s: --watch needs at least two to say anything", opts.Watch),
			Hint:    fmt.Sprintf("raise --watch above the %s re-read interval", interval),
		})
	}

	window := good[len(good)-1].at.Sub(good[0].at)

	if good[0].newest == "" {
		empty := true
		for _, p := range good {
			if p.newest != "" {
				empty = false
			}
		}
		if empty {
			return append(out, finding.Finding{
				Check: "watch", Target: name, Status: finding.BAD,
				Message: fmt.Sprintf("playlist carried no segments at any point in %s", window.Round(time.Second)),
				Value:   finding.Num(window.Seconds()), Unit: "s",
				Hint: "a live playlist with an empty window plays nothing at all",
			})
		}
	}

	// Every moment the edge moved. A move backwards counts as no move: the URI
	// changed, but nothing new was published, and calling that an advance would
	// turn a packager that reset into a healthy one.
	var advances []time.Time
	last := good[0]
	for _, p := range good[1:] {
		if p.newest != last.newest && p.newest != "" {
			advances = append(advances, p.at)
			last = p
		}
	}

	// What this rendition's edge is judged against: its own playlist's
	// TARGETDURATION if it has one, otherwise the run-level interval — but only
	// when the manifest is where that came from. A DASH representation states no
	// TARGETDURATION and most MPDs state no @minimumUpdatePeriod either, so
	// without this fallback every stalled DASH edge is measured exactly and then
	// declared unjudgeable.
	target := edgeTarget(good)
	if target <= 0 && stated {
		target = interval.Seconds()
	}
	if len(advances) == 0 {
		return append(out, finding.Finding{
			Check: "watch", Target: name, Status: finding.BAD,
			Message: fmt.Sprintf("live edge did not advance in %s (%d polls); newest segment is still %s",
				window.Round(time.Second), len(good), shortTarget(good[0].newest)),
			Value: finding.Num(window.Seconds()), Unit: "s",
			Hint: "the packager has stopped publishing, or the CDN is serving a cached playlist",
		})
	}

	// The longest the edge stood still, counting the run-up to the first advance
	// and the tail after the last one: a stream that froze for the final half of
	// the window is broken now, which is the half an operator cares about.
	gap := advances[0].Sub(good[0].at)
	for i := 1; i < len(advances); i++ {
		if d := advances[i].Sub(advances[i-1]); d > gap {
			gap = d
		}
	}
	if d := good[len(good)-1].at.Sub(advances[len(advances)-1]); d > gap {
		gap = d
	}

	out = append(out, edgeRateFindings(name, good, window)...)

	if target <= 0 {
		return append(out, finding.Finding{
			Check: "watch", Target: name, Status: finding.OK,
			Message: fmt.Sprintf("live edge advanced %d times in %s, longest gap %.1fs; the manifest states no re-read interval to judge that against",
				len(advances), window.Round(time.Second), gap.Seconds()),
			Value: finding.Num(gap.Seconds()), Unit: "s",
		})
	}

	limit := target * opts.StallTolerance
	if gap.Seconds() > limit {
		return append(out, finding.Finding{
			Check: "watch", Target: name, Status: finding.BAD,
			Message: fmt.Sprintf("live edge stalled for %.1fs, %.1fx the %.1fs re-read interval (%d advances in %s)",
				gap.Seconds(), gap.Seconds()/target, target, len(advances), window.Round(time.Second)),
			Value: finding.Num(gap.Seconds()), Unit: "s",
			Hint: "a viewer at the edge rebuffers for as long as this lasts; raise --stall-tolerance if the packager is meant to be this bursty",
		})
	}
	return append(out, finding.Finding{
		Check: "watch", Target: name, Status: finding.OK,
		Message: fmt.Sprintf("live edge advanced %d times in %s, longest gap %.1fs against a %.1fs interval",
			len(advances), window.Round(time.Second), gap.Seconds(), target),
		Value: finding.Num(gap.Seconds()), Unit: "s",
	})
}

// edgeTarget is the re-read interval this rendition's own playlist stated,
// which is what its edge is judged against. Zero means it stated none, and an
// unstated interval is not a wrong one.
func edgeTarget(points []edgePoint) float64 {
	var target float64
	for _, p := range points {
		if p.target > target {
			target = p.target
		}
	}
	return target
}

// waitFor is the real wait: opts.Sleep in every run that is not a test.
func waitFor(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// A live edge must advance at 1x real time. That is not the same claim as "it
// advanced", which is all a stall check can settle: a packager publishing two
// seconds of media every three seconds moves at every single poll and still
// loses a second of ground per three, so the live latency grows without bound
// until the viewer's buffer is gone and they rebuffer at a moment nothing in the
// stream explains. Only the ratio over the whole window shows it.
//
// The other shape is the edge going backwards, which the advance count reads as
// health — the newest segment changed, which is exactly what a working edge
// does. It is a packager that restarted, or a CDN that began answering from a
// POP holding an older playlist, and a viewer at the edge has the timeline
// pulled out from under them.

// edgeRateSlack is how much of the wall clock the ratio may be out by before it
// is worth reporting, on top of a whole segment of granularity. Media is
// published one segment at a time, so a perfectly healthy edge measures a little
// under or over depending on where the polls fall; 20% plus a segment is outside
// that and inside any real drift.
const edgeRateSlack = 0.2

func edgeRateFindings(name string, good []edgePoint, window time.Duration) []finding.Finding {
	var out []finding.Finding

	published, segDur, measured := 0.0, 0.0, false
	for i := 1; i < len(good); i++ {
		prev, cur := good[i-1], good[i]
		if back, from, to := movedBackwards(prev, cur); back {
			out = append(out, finding.Finding{
				Check: "watch", Target: name, Status: finding.BAD,
				Message: fmt.Sprintf("live edge moved backwards: the newest segment went from %s to %s, which a viewer at the edge has already played",
					shortTarget(from), shortTarget(to)),
				Hint: "the packager restarted, or this request reached a POP holding an older playlist; the timeline is pulled out from under anyone watching live",
			})
			return out
		}
		d, ok := mediaPublished(prev, cur)
		if !ok {
			// The window slid past everything the previous poll held, so how much
			// media went by is not knowable from these two. Saying nothing beats
			// guessing at it.
			continue
		}
		published += d
		measured = true
		for _, x := range cur.durs {
			if x > segDur {
				segDur = x
			}
		}
	}
	// Under a few segments of window the ratio is mostly granularity.
	if !measured || segDur <= 0 || window.Seconds() < 4*segDur {
		return out
	}

	elapsed := window.Seconds()
	slack := segDur + edgeRateSlack*elapsed
	switch {
	case elapsed-published > slack:
		out = append(out, finding.Finding{
			Check: "watch", Target: name, Status: finding.BAD,
			Message: fmt.Sprintf("live edge is falling behind real time: %.1fs of media published in %.1fs of wall clock (%.2fx)",
				published, elapsed, published/elapsed),
			Value: finding.Num(elapsed - published), Unit: "s",
			Hint: "the edge advances every time it is looked at and still loses ground, so the live latency grows until the viewer's buffer is gone — which they see as a rebuffer nothing in the stream explains",
		})
	case published-elapsed > slack:
		out = append(out, finding.Finding{
			Check: "watch", Target: name, Status: finding.WARN,
			Message: fmt.Sprintf("live edge is running ahead of real time: %.1fs of media published in %.1fs of wall clock (%.2fx)",
				published, elapsed, published/elapsed),
			Value: finding.Num(published - elapsed), Unit: "s",
			Hint: "a packager catching up after a stall looks like this and is recovering; so does one whose clock is fast, and that one keeps going",
		})
	}
	return out
}

// mediaPublished is how much media appeared between two polls: the durations of
// the segments after the one that was newest last time. The second return is
// false when the previous poll's edge has already fallen out of the window,
// which is a measurement that was missed rather than one that came out zero.
func mediaPublished(prev, cur edgePoint) (float64, bool) {
	if prev.newest == "" {
		return 0, false
	}
	for i, u := range cur.uris {
		if u != prev.newest {
			continue
		}
		total := 0.0
		for _, d := range cur.durs[i+1:] {
			total += d
		}
		return total, true
	}
	return 0, false
}

// movedBackwards says whether this poll's edge sits before the last one's. It
// asks the question in the previous window, where both segments are known to
// have existed: a newest segment that used to be in the middle of the window is
// an edge that has gone back to media a viewer already played.
func movedBackwards(prev, cur edgePoint) (bool, string, string) {
	if cur.newest == "" || cur.newest == prev.newest {
		return false, "", ""
	}
	at, was := -1, -1
	for i, u := range prev.uris {
		if u == cur.newest {
			at = i
		}
		if u == prev.newest {
			was = i
		}
	}
	if at < 0 || was < 0 || at >= was {
		return false, "", ""
	}
	return true, prev.newest, cur.newest
}
