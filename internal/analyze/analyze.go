// Package analyze runs the whole check: read the manifest, sample segments from
// every rendition, parse their bytes, and compare what the container really
// contains against what the manifest claimed.
package analyze

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// Sampling positions.
const (
	FromEdge  = "edge"  // the newest segments: what a joining viewer gets
	FromStart = "start" // the oldest segments still in the playlist
	FromAuto  = "auto"  // edge for live, start for VOD
)

// Options configures a run.
type Options struct {
	// Segments is how many segments to sample per rendition.
	Segments int
	// MaxRenditions caps how many video renditions are inspected; 0 means all.
	MaxRenditions int
	// MaxAudio caps how many audio renditions are inspected.
	MaxAudio int
	// From is where in the playlist to sample: FromEdge, FromStart or FromAuto.
	From string
	// Concurrency bounds simultaneous segment downloads.
	Concurrency int
	// DurationTolerancePct is the allowed drift between the declared segment
	// duration and the real one, as a percentage.
	DurationTolerancePct float64
	// GapToleranceMS is the allowed discrepancy between where a segment should
	// start (previous start + previous duration) and where it does.
	GapToleranceMS float64
	// BitrateTolerancePct is how far the measured bitrate may exceed the
	// declared BANDWIDTH before it is reported.
	BitrateTolerancePct float64
	// FrameRateTolerancePct is how far the measured frame rate may differ from
	// the declared FRAME-RATE. It has to absorb the NTSC rates: a manifest writes
	// 29.97 where the media runs at 30000/1001, and those are the same rate
	// spelled two ways.
	FrameRateTolerancePct float64
	// Now fixes the clock (live-edge maths, DASH template expansion).
	Now func() time.Time
}

// Defaults returns the option set the CLI starts from.
func Defaults() Options {
	return Options{
		Segments:              6,
		MaxRenditions:         0,
		MaxAudio:              1,
		From:                  FromAuto,
		Concurrency:           6,
		DurationTolerancePct:  5,
		GapToleranceMS:        100,
		BitrateTolerancePct:   10,
		FrameRateTolerancePct: 2,
		Now:                   time.Now,
	}
}

// segmentData is one sampled segment: what the manifest said, what came back
// over HTTP, and what the bytes turned out to be.
//
// fetchErr and parseErr are kept apart on purpose: a segment that 404s is a
// delivery problem, one that downloads but will not parse is a packaging
// problem, and they belong to different findings.
type segmentData struct {
	seg      manifest.Segment
	info     media.SegmentInfo
	res      fetch.Response
	fetchErr error
	parseErr error
	parsed   bool
}

// renditionData is one rendition and its sampled segments.
type renditionData struct {
	r    manifest.Rendition
	segs []segmentData
	// err is set when the rendition could not be sampled at all (its media
	// playlist did not load, its template could not be expanded).
	err error
	// initErr is set when the rendition's initialisation segment could not be
	// fetched. The segments still download, but without the init there is no
	// timescale, codec or resolution — so the checks that need those must stay
	// quiet rather than report their absence as a defect in the media.
	initErr error
	// live and targetDuration come from the rendition's own media playlist.
	live           bool
	targetDuration float64
}

// Run performs the whole analysis and returns its findings, worst first.
func Run(ctx context.Context, c *fetch.Client, rawurl string, opts Options) finding.Result {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	started := opts.Now()
	res := finding.Result{Source: rawurl, Started: started}

	pl, fs := loadManifest(ctx, c, rawurl, opts)
	res.Findings = append(res.Findings, fs...)
	if pl == nil {
		res.Duration = opts.Now().Sub(started)
		finding.SortWorstFirst(res.Findings)
		return res
	}

	rends, fs := selectRenditions(ctx, c, *pl, opts)
	res.Findings = append(res.Findings, fs...)

	// A single-file DASH representation states where its index is, not where its
	// subsegments are. Reading the index needs a fetch, so it happens here rather
	// than in the manifest package.
	resolveSegmentBase(ctx, c, rends, opts)

	// Sample every selected rendition's segments concurrently.
	sampleAll(ctx, c, rends, opts)

	for _, rd := range rends {
		for _, sd := range rd.segs {
			if sd.fetchErr == nil {
				res.Segments++
				res.Bytes += int64(len(sd.res.Body))
			}
		}
	}

	res.Findings = append(res.Findings, checkFetch(rends)...)
	res.Findings = append(res.Findings, checkInit(rends)...)
	res.Findings = append(res.Findings, checkContainer(rends)...)
	res.Findings = append(res.Findings, checkContinuity(rends, opts)...)
	res.Findings = append(res.Findings, checkDuration(rends, opts)...)
	res.Findings = append(res.Findings, checkBitrate(rends, opts)...)
	res.Findings = append(res.Findings, checkResolution(rends)...)
	res.Findings = append(res.Findings, checkKeyframe(rends)...)
	res.Findings = append(res.Findings, checkFrameRate(rends, opts)...)
	res.Findings = append(res.Findings, checkTracks(rends)...)
	res.Findings = append(res.Findings, checkTimeline(rends, opts)...)
	res.Findings = append(res.Findings, checkEncryption(rends)...)
	res.Findings = append(res.Findings, checkAlignment(rends, opts)...)
	res.Findings = append(res.Findings, checkLadder(*pl)...)

	res.Duration = opts.Now().Sub(started)
	finding.SortWorstFirst(res.Findings)
	return res
}

// loadManifest fetches and parses the top-level manifest. A nil Playlist means
// the run cannot continue, and the findings say why.
func loadManifest(ctx context.Context, c *fetch.Client, rawurl string, opts Options) (*manifest.Playlist, []finding.Finding) {
	resp, err := c.Get(ctx, rawurl, "")
	if err != nil {
		return nil, []finding.Finding{{
			Check: "manifest", Target: rawurl, Status: finding.ERROR,
			Message: fmt.Sprintf("manifest not reachable: %v", err),
		}}
	}
	kind := manifest.Detect(rawurl, resp.ContentType(), resp.Body)

	var pl manifest.Playlist
	if kind == manifest.KindDASH {
		pl, err = manifest.ParseDASH(resp.Body, rawurl, opts.Now())
	} else {
		pl, err = manifest.ParseHLS(resp.Body, rawurl)
	}
	if err != nil {
		return nil, []finding.Finding{{
			Check: "manifest", Target: rawurl, Status: finding.BAD,
			Message: err.Error(),
			Hint:    "check that the URL serves a manifest and not an error page or a redirect to one",
		}}
	}

	kindLabel := "HLS"
	if pl.Kind == manifest.KindDASH {
		kindLabel = "DASH"
	}
	shape := "media playlist"
	if pl.Master {
		shape = fmt.Sprintf("%d renditions", len(pl.Renditions))
	}
	mode := "VOD"
	if pl.Live {
		mode = "live"
	}
	return &pl, []finding.Finding{{
		Check: "manifest", Target: shortTarget(rawurl), Status: finding.OK,
		Message: fmt.Sprintf("%s %s, %s", kindLabel, mode, shape),
	}}
}

// selectRenditions decides what to sample and, for HLS, loads each variant's
// media playlist.
func selectRenditions(ctx context.Context, c *fetch.Client, pl manifest.Playlist, opts Options) ([]*renditionData, []finding.Finding) {
	var findings []finding.Finding

	// A bare HLS media playlist is one implicit rendition.
	if !pl.Master {
		return []*renditionData{{
			r:              manifest.Rendition{Name: "media", URI: pl.URL, Kind: manifest.Video},
			segs:           toSegmentData(sampleSegments(pl.Segments, pl.Live, opts)),
			live:           pl.Live,
			targetDuration: pl.TargetDuration,
		}}, findings
	}

	video := pick(byKind(pl.Renditions, manifest.Video), opts.MaxRenditions)
	audio := pick(byKind(pl.Renditions, manifest.Audio), opts.MaxAudio)
	chosen := append(append([]manifest.Rendition{}, video...), audio...)

	if skipped := len(pl.Renditions) - len(chosen); skipped > 0 {
		findings = append(findings, finding.Finding{
			Check: "manifest", Target: shortTarget(pl.URL), Status: finding.OK,
			Message: fmt.Sprintf("sampling %d of %d renditions (%d video, %d audio)", len(chosen), len(pl.Renditions), len(video), len(audio)),
			Hint:    "raise --renditions / --audio to cover the rest",
		})
	}

	out := make([]*renditionData, 0, len(chosen))
	for _, r := range chosen {
		rd := &renditionData{r: r}
		switch {
		case r.Unsupported != "":
			rd.err = fmt.Errorf("%s", r.Unsupported)
		case len(r.Segments) > 0: // DASH: the MPD already listed the segments
			rd.live = pl.Live
			rd.segs = toSegmentData(sampleSegments(r.Segments, pl.Live, opts))
		case r.SingleFile:
			// A single-file DASH representation. Its URI is the media file, not a
			// playlist, and its segments come from the index that
			// resolveSegmentBase fetches next — loading it as a media playlist
			// would report the file's own bytes as an unparseable manifest.
			rd.live = pl.Live
		case r.URI != "": // HLS: load the variant's media playlist
			sub, err := loadMediaPlaylist(ctx, c, r.URI)
			if err != nil {
				rd.err = err
			} else {
				rd.live = sub.Live
				rd.targetDuration = sub.TargetDuration
				rd.segs = toSegmentData(sampleSegments(sub.Segments, sub.Live, opts))
			}
		default:
			rd.err = fmt.Errorf("rendition has neither a URI nor inline segments")
		}
		out = append(out, rd)
	}
	return out, findings
}

func loadMediaPlaylist(ctx context.Context, c *fetch.Client, rawurl string) (manifest.Playlist, error) {
	resp, err := c.Get(ctx, rawurl, "")
	if err != nil {
		return manifest.Playlist{}, fmt.Errorf("media playlist not reachable: %w", err)
	}
	pl, err := manifest.ParseHLS(resp.Body, rawurl)
	if err != nil {
		return manifest.Playlist{}, fmt.Errorf("media playlist unparseable: %w", err)
	}
	if pl.Master {
		return manifest.Playlist{}, fmt.Errorf("variant URI points at another master playlist")
	}
	return pl, nil
}

// sampleSegments takes the window to inspect: the newest segments for a live
// stream (what a viewer joining now would play) or the oldest for VOD.
func sampleSegments(segs []manifest.Segment, live bool, opts Options) []manifest.Segment {
	n := opts.Segments
	if n <= 0 || n > len(segs) {
		n = len(segs)
	}
	from := opts.From
	if from == "" || from == FromAuto {
		if live {
			from = FromEdge
		} else {
			from = FromStart
		}
	}
	if from == FromEdge {
		return segs[len(segs)-n:]
	}
	return segs[:n]
}

func toSegmentData(segs []manifest.Segment) []segmentData {
	out := make([]segmentData, len(segs))
	for i, s := range segs {
		out[i] = segmentData{seg: s}
	}
	return out
}

func byKind(rs []manifest.Rendition, kind manifest.StreamKind) []manifest.Rendition {
	var out []manifest.Rendition
	for _, r := range rs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// pick reduces a rendition list to at most max entries, keeping the extremes
// and spreading the rest. The top and bottom rungs are where ladder defects
// concentrate, so a capped run must never drop them.
func pick(rs []manifest.Rendition, max int) []manifest.Rendition {
	sorted := append([]manifest.Rendition{}, rs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Bandwidth < sorted[j].Bandwidth })
	if max <= 0 || len(sorted) <= max {
		return sorted
	}
	if max == 1 {
		return sorted[len(sorted)-1:] // the top rung carries the most risk
	}
	out := make([]manifest.Rendition, 0, max)
	step := float64(len(sorted)-1) / float64(max-1)
	seen := map[int]bool{}
	for i := 0; i < max; i++ {
		idx := int(float64(i)*step + 0.5)
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		if seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, sorted[idx])
	}
	return out
}

// initRef identifies one initialisation segment: a URI plus, for EXT-X-MAP with
// a BYTERANGE, the sub-range of that resource.
type initRef struct {
	uri   string
	rng   string // an HTTP Range header value, empty for the whole resource
	empty bool
}

// initFor returns the initialisation segment a sampled segment needs, falling
// back to the rendition's own (DASH states it once per representation).
func initFor(rd *renditionData, sd segmentData) initRef {
	uri, rangeHeader := sd.seg.InitURI, ""
	if sd.seg.InitRange != nil {
		rangeHeader = sd.seg.InitRange.Header()
	}
	if uri == "" {
		uri, rangeHeader = rd.r.InitURI, ""
	}
	if uri == "" {
		return initRef{empty: true}
	}
	return initRef{uri: uri, rng: rangeHeader}
}

// sampleAll downloads and parses every sampled segment, bounded by
// opts.Concurrency across all renditions together.
func sampleAll(ctx context.Context, c *fetch.Client, rends []*renditionData, opts Options) {
	conc := opts.Concurrency
	if conc <= 0 {
		conc = 1
	}

	// Initialisation segments are resolved first, in their own pass. Doing it
	// lazily from inside the segment fan-out meant a failed fetch was cached as
	// "no init" and every later segment silently lost its codec and timescale —
	// which surfaced as a rendition that appeared to carry no video at all.
	inits := resolveInits(ctx, c, rends, conc)

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, rd := range rends {
		for i := range rd.segs {
			wg.Add(1)
			go func(rd *renditionData, i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				sd := &rd.segs[i]
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
				info, perr := media.Parse(resp.Body, inits[initFor(rd, *sd)].body)
				if perr != nil {
					sd.parseErr = perr
					return
				}
				sd.info = info
				sd.parsed = true
			}(rd, i)
		}
	}
	wg.Wait()
}

type initResult struct {
	body []byte
	err  error
}

// resolveInits fetches every distinct initialisation segment once and records
// which renditions were left without one.
func resolveInits(ctx context.Context, c *fetch.Client, rends []*renditionData, conc int) map[initRef]initResult {
	needed := map[initRef]bool{}
	for _, rd := range rends {
		for _, sd := range rd.segs {
			if ref := initFor(rd, sd); !ref.empty {
				needed[ref] = true
			}
		}
	}
	out := make(map[initRef]initResult, len(needed))
	if len(needed) == 0 {
		return out
	}

	refs := make([]initRef, 0, len(needed))
	for ref := range needed {
		refs = append(refs, ref)
	}
	results := make([]initResult, len(refs))

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref initRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			resp, err := c.Get(ctx, ref.uri, ref.rng)
			if err != nil {
				results[i] = initResult{err: err}
				return
			}
			results[i] = initResult{body: resp.Body}
		}(i, ref)
	}
	wg.Wait()

	for i, ref := range refs {
		out[ref] = results[i]
	}
	// Attribute failures back to the renditions that needed them.
	for _, rd := range rends {
		for _, sd := range rd.segs {
			ref := initFor(rd, sd)
			if ref.empty {
				continue
			}
			if err := out[ref].err; err != nil && rd.initErr == nil {
				rd.initErr = fmt.Errorf("initialisation segment %s not fetched: %w", ref.uri, err)
			}
		}
	}
	return out
}

// shortTarget trims a URL to something readable in a terminal table while
// staying unambiguous: the last two path elements.
func shortTarget(rawurl string) string {
	s := rawurl
	if i := indexOfAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	slash := -1
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			count++
			if count == 2 {
				slash = i
				break
			}
		}
	}
	if slash >= 0 && len(s)-slash < len(s) {
		return "…" + s[slash:]
	}
	return s
}

func indexOfAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}
