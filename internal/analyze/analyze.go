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
	// Key is the AES-128 content key for a full-segment-encrypted stream. It never
	// arrives on the command line: the CLI reads it from a file or from the
	// environment, because a key in argv lands in shell history and in every CI log
	// that echoes its own invocation.
	Key []byte
	// FetchKeys allows the key to be fetched from the URI EXT-X-KEY states. It is
	// off by default and deliberately so: pointing a checker at a key server is a
	// request to a system that logs, rate-limits and sometimes bills, and it is not
	// something to do because a manifest mentioned a URL.
	FetchKeys bool
	// MaxText caps how many subtitle renditions are inspected. They are cheap —
	// a subtitle segment is kilobytes where a video one is megabytes — but a
	// presentation can carry forty languages, and sampling all of them by default
	// would multiply the request count for something most runs do not need.
	MaxText int
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
	// MaxIFrame caps how many EXT-X-I-FRAME-STREAM-INF trick-play rungs are
	// inspected. They are cheap — one picture per entry — but a ladder can carry
	// one per resolution.
	MaxIFrame int
	// POPs are extra edge addresses to ask for the same segments, so a stale or
	// incomplete one can be found. Each costs a second copy of the sample, which
	// is why the list is empty unless a caller asks.
	POPs []string
	// ClearLead is the length of unencrypted lead-in the operator asked their
	// packager for. Zero means they did not say, and the measured lead is then
	// reported rather than judged: how much of a presentation is deliberately
	// readable is a choice nothing in the manifest records.
	ClearLead time.Duration
	// Profile selects a conformance rule set: ProfileNone (the default),
	// ProfileApple or ProfileDASHIF. It is opt-in because a conformance rule with
	// no way to turn it off turns a run that was clean yesterday into a wall of
	// findings today, on a stream nobody changed.
	Profile string
	// PartSegments is how many of the sampled segments have their EXT-X-PART
	// parts fetched and compared with the segment they make up. The cap is on
	// segments rather than parts because half a segment's parts cannot answer
	// whether they reconstruct it. Zero switches the low-latency checks off.
	PartSegments int
	// Watch is how long to keep re-reading the manifest after the segments have
	// been checked, observing what the live edge does. Zero is a single shot,
	// which is every run that did not ask for otherwise.
	Watch time.Duration
	// StallTolerance is how many re-read intervals the live edge may go without
	// a new segment before --watch calls it a stall. Two is ordinary jitter on a
	// playlist polled at TARGETDURATION; three is a segment that never arrived.
	StallTolerance float64
	// Now fixes the clock (live-edge maths, DASH template expansion).
	Now func() time.Time
	// Sleep waits for d, or until ctx is cancelled. It is injectable for the
	// same reason Now is: the watch tests drive a fake clock, and a loop that
	// really waited a TARGETDURATION per poll would take minutes to assert one
	// finding.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Defaults returns the option set the CLI starts from.
func Defaults() Options {
	return Options{
		Segments:              6,
		MaxRenditions:         0,
		MaxAudio:              1,
		MaxText:               1,
		From:                  FromAuto,
		Concurrency:           6,
		DurationTolerancePct:  5,
		GapToleranceMS:        100,
		BitrateTolerancePct:   10,
		FrameRateTolerancePct: 2,
		MaxIFrame:             1,
		PartSegments:          1,
		Profile:               ProfileNone,
		StallTolerance:        3,
		Now:                   time.Now,
		Sleep:                 waitFor,
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
	// decryptErr is set when a key was available but did not decrypt this segment.
	// It is kept apart from parseErr because the two point at different things: a
	// parse failure is about the stream, a decrypt failure is about the key.
	decryptErr error
	decrypted  bool
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
	// parts are the sampled EXT-X-PART parts, and partTarget the PART-TARGET
	// they are judged against. hasParts records that the playlist publishes
	// parts at all, which is what decides whether the check has anything to say:
	// a stream with none must not gain a row in the report for a feature it does
	// not use.
	parts      []partData
	partTarget float64
	hasParts   bool
	// oldest is the oldest segment the DVR window still promises, and window how
	// far back that window claims to reach in seconds: @timeShiftBufferDepth in
	// DASH, the playlist's own span in HLS. Both are nil/zero for VOD, which
	// promises no window at all.
	oldest *manifest.Segment
	window float64
	// probes span the DVR window, oldest first. They are only ever fetched when
	// the oldest one turns out not to be there, and then only a handful of them:
	// they are how "the window claims sixty seconds" becomes "and the origin
	// holds forty".
	probes []manifest.Segment
	// adBreaks are the ad-break signals declared for this rendition: from its own
	// media playlist in HLS, from the Period's EventStreams in DASH.
	adBreaks []manifest.AdBreak
	// dash marks a rendition from an MPD. It matters for exactly one thing: WebVTT cue
	// times. HLS anchors them with X-TIMESTAMP-MAP and requires it; DASH does not use
	// the tag and puts them on the presentation timeline directly, so its absence is a
	// problem in one format and normal in the other.
	dash bool
}

// Run performs the whole analysis and returns its findings, worst first.
func Run(ctx context.Context, c *fetch.Client, rawurl string, opts Options) finding.Result {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = waitFor
	}
	// wall is this machine's clock, kept apart from opts.Now because opts.Now may
	// be shifted below to the one a dynamic MPD nominates. How long the run took
	// is a fact about the real world: measured against a stream clock thirty
	// seconds behind, it came out negative.
	wall := opts.Now
	started := wall()
	res := finding.Result{Source: rawurl, Started: started}

	pl, clock, fs := loadManifest(ctx, c, rawurl, opts)
	res.Findings = append(res.Findings, fs...)
	// A dynamic MPD's live edge is computed against a clock, and the MPD may name
	// which one. Everything after this point — the segments sampled, the watch
	// loop, DASH template expansion — uses the clock the stream itself nominates
	// rather than this machine's, because this machine's is the thing under test.
	if clock.ok {
		offset := clock.skew
		base := opts.Now
		opts.Now = func() time.Time { return base().Add(offset) }
	}
	if pl == nil {
		res.Duration = wall().Sub(started)
		finding.SortWorstFirst(res.Findings)
		return res
	}

	rends, fs := selectRenditions(ctx, c, *pl, opts)

	// An HLS master playlist carries no liveness signal whatsoever —
	// EXT-X-ENDLIST is a media-playlist tag — so a live ladder parses as VOD
	// until its variants have been loaded. Everything downstream that reasons
	// about a live edge has to be told, and the report has to stop calling a
	// live stream VOD.
	if !pl.Live {
		for _, rd := range rends {
			if rd.live {
				pl.Live = true
				break
			}
		}
	}
	res.Findings = append(res.Findings, manifestShape(rawurl, *pl))
	res.Findings = append(res.Findings, fs...)

	// A single-file DASH representation states where its index is, not where its
	// subsegments are. Reading the index needs a fetch, so it happens here rather
	// than in the manifest package.
	resolveSegmentBase(ctx, c, rends, opts)

	// Trick-play rungs are sampled apart from everything else and never enter
	// rends: their entries are single pictures, and every check that reads a
	// segment as an extent of media would be wrong about one.
	iframes := sampleIFrames(ctx, c, *pl, opts)

	// One small request per run: the segment the MPD says does not exist yet. It
	// is the only way to see a packager that is *ahead* of its own availability
	// window, which costs every player latency without ever raising an error.
	probes := probeNextSegments(ctx, c, *pl, rends)

	// Sample every selected rendition's segments concurrently.
	dvr := sampleAll(ctx, c, rends, *pl, opts)

	for _, rd := range rends {
		for _, sd := range rd.segs {
			if sd.fetchErr == nil {
				res.Segments++
				res.Bytes += int64(len(sd.res.Body))
			}
		}
	}

	res.Findings = append(res.Findings, checkFetch(rends)...)
	res.Findings = append(res.Findings, checkCache(rends)...)
	res.Findings = append(res.Findings, checkPOP(rends, comparePOPs(ctx, c, rends, opts))...)
	res.Findings = append(res.Findings, checkInit(rends)...)
	res.Findings = append(res.Findings, checkContainer(rends)...)
	res.Findings = append(res.Findings, checkContinuity(rends, opts)...)
	res.Findings = append(res.Findings, checkDuration(rends, opts)...)
	res.Findings = append(res.Findings, checkBitrate(rends, opts)...)
	res.Findings = append(res.Findings, checkResolution(rends)...)
	res.Findings = append(res.Findings, checkKeyframe(rends)...)
	res.Findings = append(res.Findings, checkFrameRate(rends, opts)...)
	res.Findings = append(res.Findings, checkAudio(rends)...)
	res.Findings = append(res.Findings, checkCaptions(rends)...)
	res.Findings = append(res.Findings, checkAdBreak(rends, opts)...)
	res.Findings = append(res.Findings, checkSubtitles(rends, opts)...)
	res.Findings = append(res.Findings, checkTracks(rends)...)
	res.Findings = append(res.Findings, checkTimeline(rends, opts)...)
	res.Findings = append(res.Findings, checkEncryption(rends)...)
	res.Findings = append(res.Findings, checkDRM(rends)...)
	res.Findings = append(res.Findings, checkScheme(rends)...)
	res.Findings = append(res.Findings, checkClear(rends, opts)...)
	res.Findings = append(res.Findings, checkVideoRange(rends)...)
	res.Findings = append(res.Findings, checkCodecString(rends)...)
	res.Findings = append(res.Findings, checkAlignment(rends, opts)...)
	res.Findings = append(res.Findings, checkAvailability(*pl, rends, clock, probes, opts)...)
	res.Findings = append(res.Findings, checkDVR(dvr)...)
	res.Findings = append(res.Findings, checkPDT(rends, opts)...)
	res.Findings = append(res.Findings, checkParts(rends, opts)...)
	res.Findings = append(res.Findings, checkIFrame(iframes, rends, opts)...)
	res.Findings = append(res.Findings, checkLadder(*pl)...)
	res.Findings = append(res.Findings, checkPeriod(rends)...)
	res.Findings = append(res.Findings, checkDiscontinuity(rends, opts)...)
	res.Findings = append(res.Findings, checkProfile(*pl, rends, opts)...)

	// The watch loop runs last and takes as long as it was asked to: everything
	// a single look can establish is already in res before the first poll, so an
	// interrupted watch still reports it.
	if opts.Watch > 0 {
		res.Findings = append(res.Findings, watchLiveEdge(ctx, c, rawurl, *pl, opts)...)
	}

	res.Duration = wall().Sub(started)
	finding.SortWorstFirst(res.Findings)
	return res
}

// loadManifest fetches and parses the top-level manifest. A nil Playlist means
// the run cannot continue, and the findings say why.
func loadManifest(ctx context.Context, c *fetch.Client, rawurl string, opts Options) (*manifest.Playlist, referenceClock, []finding.Finding) {
	var clock referenceClock

	resp, err := c.Get(ctx, rawurl, "")
	if err != nil {
		return nil, clock, []finding.Finding{{
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
		return nil, clock, []finding.Finding{{
			Check: "manifest", Target: rawurl, Status: finding.BAD,
			Message: err.Error(),
			Hint:    "check that the URL serves a manifest and not an error page or a redirect to one",
		}}
	}

	// The MPD's segment list was just computed against this machine's clock. If
	// the MPD names a time source of its own, that answer supersedes it and the
	// expansion has to be done again — the whole point of the element is that the
	// first answer may be pointing at segments that do not exist.
	if kind == manifest.KindDASH && pl.Live && len(pl.UTCTiming) > 0 {
		clock = resolveClock(ctx, c, pl.UTCTiming, opts.Now())
		if clock.ok && absDuration(clock.skew) > clockSkewTolerance {
			if corrected, cerr := manifest.ParseDASH(resp.Body, rawurl, clock.now); cerr == nil {
				pl = corrected
			}
		}
	}

	return &pl, clock, nil
}

// manifestShape is the one OK line that says what was loaded. It is emitted
// after the renditions rather than with the parse, because whether an HLS
// ladder is live is not knowable from the master playlist — only its variants
// say so, and a report that called a live stream VOD sat directly above the
// finding about its live edge.
func manifestShape(rawurl string, pl manifest.Playlist) finding.Finding {
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
	return finding.Finding{
		Check: "manifest", Target: shortTarget(rawurl), Status: finding.OK,
		Message: fmt.Sprintf("%s %s, %s", kindLabel, mode, shape),
	}
}

// selectRenditions decides what to sample and, for HLS, loads each variant's
// media playlist.
func selectRenditions(ctx context.Context, c *fetch.Client, pl manifest.Playlist, opts Options) ([]*renditionData, []finding.Finding) {
	var findings []finding.Finding

	// A bare HLS media playlist is one implicit rendition.
	if !pl.Master {
		rd := &renditionData{
			r:              manifest.Rendition{Name: "media", URI: pl.URL, Kind: manifest.Video},
			segs:           toSegmentData(sampleSegments(pl.Segments, pl.Live, opts)),
			live:           pl.Live,
			targetDuration: pl.TargetDuration,
			partTarget:     pl.PartTarget,
			hasParts:       playlistHasParts(pl),
		}
		rd.oldest, rd.window = playlistWindow(pl)
		rd.probes = pl.Segments
		return []*renditionData{rd}, findings
	}

	sel := chooseRenditions(pl, opts)
	video, audio, text := sel.video, sel.audio, sel.text
	chosen := sel.all()
	// Trick-play rungs are sampled separately, but they are renditions in the
	// manifest and the count has to add up or the line reads as a silent drop.
	iframes := len(chooseIFrames(pl, opts))

	if skipped := len(pl.Renditions) - len(chosen) - iframes; skipped > 0 {
		findings = append(findings, finding.Finding{
			Check: "manifest", Target: shortTarget(pl.URL), Status: finding.OK,
			Message: fmt.Sprintf("sampling %d of %d renditions (%d video, %d audio, %d subtitle, %d trick-play)",
				len(chosen)+iframes, len(pl.Renditions), len(video), len(audio), len(text), iframes),
			Hint: "raise --renditions / --audio / --subtitles / --iframes to cover the rest",
		})
	}

	out := make([]*renditionData, 0, len(chosen))
	for _, r := range chosen {
		rd := &renditionData{r: r, dash: pl.Kind == manifest.KindDASH}
		switch {
		case r.Unsupported != "":
			rd.err = fmt.Errorf("%s", r.Unsupported)
		case len(r.Segments) > 0: // DASH: the MPD already listed the segments
			rd.live = pl.Live
			rd.adBreaks = pl.AdBreaks
			rd.oldest, rd.window = r.OldestSegment, pl.TimeShiftBufferDepth
			rd.probes = r.WindowProbes
			// DASH has no EXT-X-PART; low latency there is chunked transfer of a
			// segment that is already listed, with nothing extra to compare.
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
				rd.adBreaks = sub.AdBreaks
				rd.partTarget = sub.PartTarget
				rd.hasParts = playlistHasParts(sub)
				rd.oldest, rd.window = playlistWindow(sub)
				// HLS lists everything it has, so the playlist is its own set of
				// probe points and no template needs re-evaluating.
				rd.probes = sub.Segments
				rd.segs = toSegmentData(sampleSegments(sub.Segments, sub.Live, opts))
			}
		default:
			rd.err = fmt.Errorf("rendition has neither a URI nor inline segments")
		}
		out = append(out, rd)
	}
	return out, findings
}

// chosenRenditions is what a run inspects, kept apart from the sampling so the
// watch loop polls exactly the renditions the report talks about. A watch that
// picked its own subset would report an edge for a rendition nothing else in
// the output mentions.
type chosenRenditions struct {
	video, audio, text []manifest.Rendition
}

func (c chosenRenditions) all() []manifest.Rendition {
	return append(append(append([]manifest.Rendition{}, c.video...), c.audio...), c.text...)
}

func chooseRenditions(pl manifest.Playlist, opts Options) chosenRenditions {
	return chosenRenditions{
		video: pick(byKind(pl.Renditions, manifest.Video), opts.MaxRenditions),
		audio: pick(byKind(pl.Renditions, manifest.Audio), opts.MaxAudio),
		text:  pick(byKind(pl.Renditions, manifest.Text), opts.MaxText),
	}
}

// playlistHasParts reports whether the playlist publishes EXT-X-PART at all.
// PART-TARGET alone is not enough: a playlist can declare the interval and have
// aged every part out of the window.
// playlistWindow is the DVR promise an HLS media playlist makes: it lists every
// segment it still has, so the oldest one and the span they cover are the
// promise itself. A VOD playlist promises nothing — every segment is permanent
// — so it gets no window.
func playlistWindow(pl manifest.Playlist) (*manifest.Segment, float64) {
	if !pl.Live || len(pl.Segments) == 0 {
		return nil, 0
	}
	var span float64
	for _, s := range pl.Segments {
		span += s.Duration
	}
	oldest := pl.Segments[0]
	return &oldest, span
}

func playlistHasParts(pl manifest.Playlist) bool {
	if len(pl.PendingParts) > 0 {
		return true
	}
	for _, s := range pl.Segments {
		if len(s.Parts) > 0 {
			return true
		}
	}
	return false
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
	if ref := segInitRef(sd.seg); !ref.empty {
		return ref
	}
	if rd.r.InitURI == "" {
		return initRef{empty: true}
	}
	return initRef{uri: rd.r.InitURI}
}

// segInitRef is the initialisation segment a single segment names, byte range
// and all. The range is not optional: Apple's own streams put the init in the
// first few hundred bytes of the same file that holds every segment, so
// ignoring it downloads the whole asset and then fails to parse it as an init.
func segInitRef(seg manifest.Segment) initRef {
	if seg.InitURI == "" {
		return initRef{empty: true}
	}
	ref := initRef{uri: seg.InitURI}
	if seg.InitRange != nil {
		ref.rng = seg.InitRange.Header()
	}
	return ref
}

// sampleAll downloads and parses every sampled segment, bounded by
// opts.Concurrency across all renditions together.
func sampleAll(ctx context.Context, c *fetch.Client, rends []*renditionData, pl manifest.Playlist, opts Options) *dvrProbe {
	conc := opts.Concurrency
	if conc <= 0 {
		conc = 1
	}

	// Initialisation segments are resolved first, in their own pass. Doing it
	// lazily from inside the segment fan-out meant a failed fetch was cached as
	// "no init" and every later segment silently lost its codec and timescale —
	// which surfaced as a rendition that appeared to carry no video at all.
	inits := resolveInits(ctx, c, rends, conc)

	// Content keys are resolved before the fan-out for the same reason: a key
	// fetched from inside it would be fetched once per segment, and a failure
	// cached as "no key" would silently leave later segments undecrypted.
	keys := resolveKeys(ctx, c, rends, opts)

	// Which parts to fetch is decided before the fan-out, from the same sampled
	// segments, so the parts ride the same concurrency bound as everything else.
	for _, rd := range rends {
		rd.parts = selectParts(rd, opts)
	}

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
				body := resp.Body
				if key, ok := keys[keyFor(*sd)]; ok {
					plain, derr := media.DecryptAES128(body, key, segmentIV(*sd))
					if derr != nil {
						sd.decryptErr = derr
						return
					}
					body = plain
					sd.decrypted = true
				}
				info, perr := media.Parse(body, inits[initFor(rd, *sd)].body)
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

	// The parts go after the segments rather than alongside them: a part is only
	// meaningful next to the segment it makes up, and that segment has to have
	// been read before there is anything to compare it with.
	samplePartsAll(ctx, c, rends, inits, conc)

	// And the far end of the DVR window, which nothing else looks at: every
	// other check reads the live edge, because that is what a joining viewer
	// gets, and the back of the window is only ever reached by a scrub.
	return probeDVR(ctx, c, pl, rends, inits)
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

// keyRef identifies a content key: the URI it comes from, or the empty string for
// the one supplied on the command line, which applies to every segment.
type keyRef struct {
	uri string
}

// keyFor is the key a segment needs, or a zero keyRef when it needs none.
func keyFor(sd segmentData) keyRef {
	if !isFullSegmentEncryption(sd.seg.KeyMethod) {
		return keyRef{}
	}
	return keyRef{uri: sd.seg.KeyURI}
}

// segmentIV is the initialisation vector for a segment: the one EXT-X-KEY states,
// or the media sequence number as a 128-bit big-endian value when it states none.
// That default is a specific instruction in the specification, not a missing value
// to fill with zeroes — a stream that omits the attribute decrypts to noise under a
// zero IV, and noise is indistinguishable from a wrong key.
func segmentIV(sd segmentData) []byte {
	if len(sd.seg.KeyIV) == media.AESBlockSize {
		return sd.seg.KeyIV
	}
	return media.SequenceIV(sd.seg.Sequence)
}

// resolveKeys works out which content key decrypts which segment, once for the whole
// run.
//
// A key supplied by the caller applies to every encrypted segment. Otherwise the URI
// EXT-X-KEY states is fetched, but only when the caller asked for that: pointing a
// checker at a key server is a request to a system that logs, rate-limits and
// sometimes bills, and a manifest mentioning a URL is not a reason to do it.
func resolveKeys(ctx context.Context, c *fetch.Client, rends []*renditionData, opts Options) map[keyRef][]byte {
	out := map[keyRef][]byte{}

	// Which keys are actually needed, so nothing is fetched for a stream that turns
	// out not to be encrypted.
	needed := map[keyRef]bool{}
	for _, rd := range rends {
		for _, sd := range rd.segs {
			if ref := keyFor(sd); ref != (keyRef{}) || isFullSegmentEncryption(sd.seg.KeyMethod) {
				needed[ref] = true
			}
		}
	}
	if len(needed) == 0 {
		return out
	}

	if len(opts.Key) == 16 {
		for ref := range needed {
			out[ref] = opts.Key
		}
		return out
	}
	if !opts.FetchKeys {
		return out
	}
	for ref := range needed {
		if ref.uri == "" {
			continue
		}
		resp, err := c.Get(ctx, ref.uri, "")
		if err != nil || len(resp.Body) != 16 {
			// A key that will not fetch, or is not sixteen bytes, is not a key. The
			// encryption check reports the segments as unreadable, which is the
			// truth: segcheck could not look.
			continue
		}
		out[ref] = resp.Body
	}
	return out
}
