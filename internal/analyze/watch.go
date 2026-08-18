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
	interval := pollInterval(pl, series[0])
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

	return watchFindings(rawurl, series, interval, opts)
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
			obs.edges = append(obs.edges, edgeOf(r.Name, pl.TargetDuration, r.Segments))
		case r.SingleFile:
			// One file with its index inside it. It has no live edge by construction.
		case r.URI != "":
			sub, err := loadMediaPlaylist(ctx, c, r.URI)
			if err != nil {
				obs.edges = append(obs.edges, edgeState{name: r.Name, err: err})
				continue
			}
			obs.edges = append(obs.edges, edgeOf(r.Name, sub.TargetDuration, sub.Segments))
		}
	}
	return obs
}

func edgeOf(name string, target float64, segs []manifest.Segment) edgeState {
	e := edgeState{name: name, target: target, count: len(segs)}
	for _, s := range segs {
		e.span += s.Duration
	}
	if len(segs) > 0 {
		e.newest = segs[len(segs)-1].URI
	}
	return e
}

// pollInterval is how often to re-read, taken from what the manifest itself
// says a player would do: TARGETDURATION in HLS, @minimumUpdatePeriod in DASH.
// Failing both, the longest segment in the window is the best evidence of how
// often a new one appears.
func pollInterval(pl manifest.Playlist, first observation) time.Duration {
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
		return defaultPollInterval
	}
	d := time.Duration(secs * float64(time.Second))
	if d < minPollInterval {
		return minPollInterval
	}
	return d
}

// edgePoint is one rendition's edge at one moment, lifted out of the
// observation series so each rendition can be judged on its own.
type edgePoint struct {
	at     time.Time
	newest string
	target float64
	err    error
}

// watchFindings turns the series of observations into findings, one rendition
// at a time.
func watchFindings(rawurl string, series []observation, interval time.Duration, opts Options) []finding.Finding {
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
			byRendition[e.name] = append(byRendition[e.name], edgePoint{at: o.at, newest: e.newest, target: e.target, err: e.err})
		}
	}
	if len(order) == 0 {
		return append(out, finding.Finding{
			Check: "watch", Target: shortTarget(rawurl), Status: finding.ERROR,
			Message: fmt.Sprintf("no rendition had a live edge to watch over %s", opts.Watch),
		})
	}
	for _, name := range order {
		out = append(out, edgeFindings(name, byRendition[name], interval, opts)...)
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
func edgeFindings(name string, points []edgePoint, interval time.Duration, opts Options) []finding.Finding {
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

	target := edgeTarget(good)
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
