package manifest

import (
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxExpandedSegments bounds how many segment URLs one representation expands
// to. A multi-hour VOD with 2s segments would otherwise build tens of thousands
// of strings that the sampler immediately throws away.
const maxExpandedSegments = 20000

// ---------- MPD document ----------

type mpdDoc struct {
	Type                      string      `xml:"type,attr"`
	AvailabilityStartTime     string      `xml:"availabilityStartTime,attr"`
	PublishTime               string      `xml:"publishTime,attr"`
	MediaPresentationDuration string      `xml:"mediaPresentationDuration,attr"`
	MinimumUpdatePeriod       string      `xml:"minimumUpdatePeriod,attr"`
	BaseURL                   []string    `xml:"BaseURL"`
	Periods                   []mpdPeriod `xml:"Period"`
}

type mpdPeriod struct {
	ID              string           `xml:"id,attr"`
	Start           string           `xml:"start,attr"`
	Duration        string           `xml:"duration,attr"`
	BaseURL         []string         `xml:"BaseURL"`
	AdaptationSets  []mpdAdaptation  `xml:"AdaptationSet"`
	SegmentTemplate *mpdSegTemplate  `xml:"SegmentTemplate"`
	EventStreams    []mpdEventStream `xml:"EventStream"`
}

type mpdEventStream struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Timescale   uint32 `xml:"timescale,attr"`
	Events      []struct {
		PresentationTime *int64 `xml:"presentationTime,attr"`
		Duration         int64  `xml:"duration,attr"`
		ID               string `xml:"id,attr"`
	} `xml:"Event"`
}

type mpdAdaptation struct {
	MimeType                  string `xml:"mimeType,attr"`
	ContentType               string `xml:"contentType,attr"`
	Lang                      string `xml:"lang,attr"`
	Codecs                    string `xml:"codecs,attr"`
	Width                     int    `xml:"width,attr"`
	Height                    int    `xml:"height,attr"`
	FrameRate                 string `xml:"frameRate,attr"`
	AudioSamplingRate         int    `xml:"audioSamplingRate,attr"`
	AudioChannelConfiguration []struct {
		SchemeIDURI string `xml:"schemeIdUri,attr"`
		Value       string `xml:"value,attr"`
	} `xml:"AudioChannelConfiguration"`
	Accessibility []struct {
		SchemeIDURI string `xml:"schemeIdUri,attr"`
		Value       string `xml:"value,attr"`
	} `xml:"Accessibility"`
	BaseURL           []string            `xml:"BaseURL"`
	SegmentTemplate   *mpdSegTemplate     `xml:"SegmentTemplate"`
	Representations   []mpdRepresentation `xml:"Representation"`
	ContentProtection []struct {
		SchemeIDURI string `xml:"schemeIdUri,attr"`
	} `xml:"ContentProtection"`
}

type mpdRepresentation struct {
	ID                        string `xml:"id,attr"`
	Bandwidth                 int    `xml:"bandwidth,attr"`
	Width                     int    `xml:"width,attr"`
	Height                    int    `xml:"height,attr"`
	Codecs                    string `xml:"codecs,attr"`
	MimeType                  string `xml:"mimeType,attr"`
	FrameRate                 string `xml:"frameRate,attr"`
	AudioSamplingRate         int    `xml:"audioSamplingRate,attr"`
	AudioChannelConfiguration []struct {
		SchemeIDURI string `xml:"schemeIdUri,attr"`
		Value       string `xml:"value,attr"`
	} `xml:"AudioChannelConfiguration"`
	BaseURL         []string        `xml:"BaseURL"`
	SegmentTemplate *mpdSegTemplate `xml:"SegmentTemplate"`
	SegmentBase     *struct {
		IndexRange     string `xml:"indexRange,attr"`
		Timescale      int    `xml:"timescale,attr"`
		Initialization struct {
			Range string `xml:"range,attr"`
		} `xml:"Initialization"`
	} `xml:"SegmentBase"`
	SegmentList *struct {
		Duration       int `xml:"duration,attr"`
		Timescale      int `xml:"timescale,attr"`
		Initialization struct {
			SourceURL string `xml:"sourceURL,attr"`
		} `xml:"Initialization"`
		SegmentURLs []struct {
			Media string `xml:"media,attr"`
		} `xml:"SegmentURL"`
	} `xml:"SegmentList"`
}

type mpdSegTemplate struct {
	Media                  string          `xml:"media,attr"`
	Initialization         string          `xml:"initialization,attr"`
	Timescale              int             `xml:"timescale,attr"`
	Duration               int64           `xml:"duration,attr"`
	StartNumber            *int            `xml:"startNumber,attr"`
	PresentationTimeOffset int64           `xml:"presentationTimeOffset,attr"`
	SegmentTimeline        *mpdSegTimeline `xml:"SegmentTimeline"`
}

type mpdSegTimeline struct {
	S []struct {
		T *int64 `xml:"t,attr"`
		D int64  `xml:"d,attr"`
		R int    `xml:"r,attr"`
	} `xml:"S"`
}

// ParseDASH parses an MPD into the shared model, expanding every
// representation's SegmentTemplate or SegmentTimeline into concrete segment
// URLs. now fixes the clock used to locate the live edge of a dynamic MPD; pass
// time.Now for real use.
func ParseDASH(body []byte, baseURL string, now time.Time) (Playlist, error) {
	var doc mpdDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return Playlist{Kind: KindDASH, URL: baseURL}, fmt.Errorf("invalid MPD: %w", err)
	}
	pl := Playlist{Kind: KindDASH, URL: baseURL, Master: true, Live: doc.Type == "dynamic"}

	base, _ := url.Parse(baseURL)
	base = applyBaseURLs(base, doc.BaseURL)

	var ast time.Time
	if doc.AvailabilityStartTime != "" {
		if t, err := parsePDT(doc.AvailabilityStartTime); err == nil {
			ast = t
		}
	}
	mpdDur, _ := parseISODuration(doc.MediaPresentationDuration)

	for pi, period := range doc.Periods {
		pbase := applyBaseURLs(base, period.BaseURL)
		periodStart, _ := parseISODuration(period.Start)
		periodDur, _ := parseISODuration(period.Duration)
		if periodDur == 0 {
			periodDur = mpdDur
		}
		pl.AdBreaks = append(pl.AdBreaks, dashAdBreaks(period.EventStreams, periodStart)...)

		for ai, as := range period.AdaptationSets {
			abase := applyBaseURLs(pbase, as.BaseURL)
			kind := dashKind(as, ai)
			protected := len(as.ContentProtection) > 0

			for ri, rep := range as.Representations {
				rbase := applyBaseURLs(abase, rep.BaseURL)
				tmpl := firstTemplate(rep.SegmentTemplate, as.SegmentTemplate, period.SegmentTemplate)

				r := Rendition{
					Name:      dashName(rep, as, kind, pi, ai, ri),
					Kind:      kind,
					Language:  as.Lang,
					Bandwidth: rep.Bandwidth,
					Width:     firstNonZero(rep.Width, as.Width),
					Height:    firstNonZero(rep.Height, as.Height),
					Codecs:    firstNonEmpty(rep.Codecs, as.Codecs),
					FrameRate: parseFrameRate(firstNonEmpty(rep.FrameRate, as.FrameRate)),
					// The Representation states its own audio properties when it
					// differs from the set; otherwise the set's values apply to it.
					SampleRate: firstNonZero(rep.AudioSamplingRate, as.AudioSamplingRate),
					Channels: firstNonZero(
						dashChannels(rep.AudioChannelConfiguration),
						dashChannels(as.AudioChannelConfiguration),
					),
					Captions:  dashCaptions(as.Accessibility),
					KeyMethod: dashKeyMethod(protected),
				}

				switch {
				case tmpl != nil:
					init, segs, err := expandTemplate(tmpl, rep, rbase, ast, now, periodStart, periodDur, pl.Live, protected)
					if err != nil {
						r.Unsupported = err.Error()
					}
					r.InitURI = init
					r.Segments = segs
				case rep.SegmentList != nil:
					ts := rep.SegmentList.Timescale
					if ts == 0 {
						ts = 1
					}
					dur := float64(rep.SegmentList.Duration) / float64(ts)
					if src := rep.SegmentList.Initialization.SourceURL; src != "" {
						r.InitURI = Resolve(rbase, src)
					}
					for i, su := range rep.SegmentList.SegmentURLs {
						r.Segments = append(r.Segments, Segment{
							URI:       Resolve(rbase, su.Media),
							Duration:  dur,
							InitURI:   r.InitURI,
							Sequence:  i + 1,
							KeyMethod: dashKeyMethod(protected),
						})
					}
				case rep.SegmentBase != nil:
					// One file: the init segment and the index are byte ranges of
					// it. The subsegments cannot be listed here — only the `sidx`
					// says where they are, and reading it needs a fetch — so the
					// rendition carries the range to ask for instead.
					idx, ok := parseByteRangeAttr(rep.SegmentBase.IndexRange)
					if !ok {
						r.Unsupported = "SegmentBase without a usable @indexRange: nothing states where the subsegments are"
						break
					}
					r.URI = Resolve(rbase, "")
					r.InitURI = r.URI
					r.IndexRange = &idx
					r.SingleFile = true
					if init, ok := parseByteRangeAttr(rep.SegmentBase.Initialization.Range); ok {
						r.InitRange = &init
					}
				case len(rep.BaseURL) > 0 || len(as.BaseURL) > 0:
					r.URI = Resolve(rbase, "")
					if sidecarSubtitle(firstNonEmpty(rep.MimeType, as.MimeType)) {
						// A sidecar subtitle: one file that *is* the subtitle, not an
						// ISO-BMFF container with an index inside it. Sending the index
						// probe after a sidx that cannot exist reported "no segment
						// index found" — an ERROR claiming the tool could not look at a
						// file it could have read whole.
						r.Segments = []Segment{{
							URI:      r.URI,
							Duration: periodDur,
							Sequence: 1,
						}}
						break
					}
					// The on-demand profile: one file, a BaseURL and nothing else.
					// The index is inside the file and its position is not stated,
					// so it has to be found by reading the head — which needs a
					// fetch, and happens in the analysis rather than here. This is
					// the commonest shape of single-file DASH in the wild.
					r.InitURI = r.URI
					r.SingleFile = true
				default:
					r.Unsupported = "representation has no SegmentTemplate, SegmentList or SegmentBase"
				}
				pl.Renditions = append(pl.Renditions, r)
			}
		}
	}
	if len(pl.Renditions) == 0 {
		return pl, fmt.Errorf("MPD declares no Representation")
	}
	return pl, nil
}

// expandTemplate turns a SegmentTemplate into concrete segment URLs — from its
// SegmentTimeline when it has one, else from @duration plus the wall clock.
func expandTemplate(t *mpdSegTemplate, rep mpdRepresentation, base *url.URL, ast, now time.Time, periodStart, periodDur float64, live, protected bool) (string, []Segment, error) {
	timescale := t.Timescale
	if timescale == 0 {
		timescale = 1
	}
	startNumber := 1
	if t.StartNumber != nil {
		startNumber = *t.StartNumber
	}
	var initURI string
	if t.Initialization != "" {
		initURI = Resolve(base, substituteTemplate(t.Initialization, rep, 0, 0))
	}
	if t.Media == "" {
		return initURI, nil, fmt.Errorf("SegmentTemplate without @media")
	}

	var segs []Segment
	key := dashKeyMethod(protected)

	if tl := t.SegmentTimeline; tl != nil {
		// The timeline states every segment's start and duration outright: the
		// most reliable case, and the one where a declared start can be checked
		// against the tfdt in the segment itself.
		var current int64
		haveCurrent := false
		number := startNumber
		for _, s := range tl.S {
			if s.T != nil {
				current, haveCurrent = *s.T, true
			}
			if !haveCurrent {
				current, haveCurrent = 0, true
			}
			if s.D <= 0 {
				continue
			}
			repeat := s.R
			if repeat < 0 {
				// r="-1" means "repeat until the end of the period"; derive how
				// many that is from the period duration when it is known.
				if periodDur > 0 {
					end := int64(periodDur * float64(timescale))
					repeat = int((end-current)/s.D) - 1
					if repeat < 0 {
						repeat = 0
					}
				} else {
					repeat = 0
				}
			}
			for i := 0; i <= repeat; i++ {
				if len(segs) >= maxExpandedSegments {
					return initURI, segs, nil
				}
				segs = append(segs, Segment{
					URI:      Resolve(base, substituteTemplate(t.Media, rep, number, current)),
					Duration: float64(s.D) / float64(timescale),
					InitURI:  initURI,
					Sequence: number,
					// @t is on the media timeline, the same one tfdt counts on,
					// so this value is directly comparable with the segment's
					// baseMediaDecodeTime. presentationTimeOffset is deliberately
					// not applied: that would move it to the presentation
					// timeline and break the comparison.
					DeclaredStart:    float64(current) / float64(timescale),
					HasDeclaredStart: true,
					KeyMethod:        key,
				})
				current += s.D
				number++
			}
		}
		return initURI, segs, nil
	}

	if t.Duration <= 0 {
		return initURI, nil, fmt.Errorf("SegmentTemplate has neither SegmentTimeline nor @duration")
	}
	segDur := float64(t.Duration) / float64(timescale)

	// Without a timeline the segment list is implied: index i covers
	// [i*segDur, (i+1)*segDur) from the start of the period.
	var count int
	var firstIndex int
	switch {
	case live && !ast.IsZero():
		// Only segments whose whole duration has elapsed are published.
		elapsed := now.Sub(ast).Seconds() - periodStart
		available := int(math.Floor(elapsed / segDur))
		if available <= 0 {
			return initURI, nil, fmt.Errorf("no segment available yet: availabilityStartTime is %s in the future", ast.Sub(now).Truncate(time.Second))
		}
		// Sample the tail: the live edge is what matters, and the head of a
		// long-running live window may have fallen out of the CDN already.
		const window = 12
		firstIndex = available - window
		if firstIndex < 0 {
			firstIndex = 0
		}
		count = available - firstIndex
	case periodDur > 0:
		count = int(math.Ceil(periodDur / segDur))
	default:
		return initURI, nil, fmt.Errorf("static MPD without mediaPresentationDuration: cannot tell how many segments exist")
	}
	if count > maxExpandedSegments {
		count = maxExpandedSegments
	}

	for i := 0; i < count; i++ {
		idx := firstIndex + i
		number := startNumber + idx
		tickTime := int64(float64(idx) * float64(t.Duration))
		segs = append(segs, Segment{
			URI:      Resolve(base, substituteTemplate(t.Media, rep, number, tickTime)),
			Duration: segDur,
			InitURI:  initURI,
			Sequence: number,
			// HasDeclaredStart stays false here: with @duration instead of a
			// SegmentTimeline the media-timeline origin depends on
			// presentationTimeOffset, so an index-derived start is not
			// comparable with the segment's tfdt and would report phantom drift.
			KeyMethod: key,
		})
	}
	return initURI, segs, nil
}

// substituteTemplate resolves the $Identifier$ placeholders, honouring the
// optional printf width ($Number%05d$).
func substituteTemplate(tmpl string, rep mpdRepresentation, number int, tick int64) string {
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if tmpl[i] != '$' {
			b.WriteByte(tmpl[i])
			i++
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '$')
		if end < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		token := tmpl[i+1 : i+1+end]
		i += end + 2
		if token == "" { // "$$" is a literal dollar
			b.WriteByte('$')
			continue
		}
		name, format, hasFormat := strings.Cut(token, "%")
		if hasFormat {
			format = "%" + format
		}
		switch name {
		case "RepresentationID":
			b.WriteString(rep.ID)
		case "Bandwidth":
			b.WriteString(formatOrDecimal(format, hasFormat, int64(rep.Bandwidth)))
		case "Number":
			b.WriteString(formatOrDecimal(format, hasFormat, int64(number)))
		case "Time":
			b.WriteString(formatOrDecimal(format, hasFormat, tick))
		default:
			// Unknown identifier: leave it in place so the resulting 404 points
			// at the template rather than at a silently wrong URL.
			b.WriteString("$" + token + "$")
		}
	}
	return b.String()
}

func formatOrDecimal(format string, hasFormat bool, v int64) string {
	if hasFormat {
		return fmt.Sprintf(format, v)
	}
	return strconv.FormatInt(v, 10)
}

func firstTemplate(ts ...*mpdSegTemplate) *mpdSegTemplate {
	// A Representation-level template overrides the AdaptationSet's, which
	// overrides the Period's; merge so an inherited @timescale/@initialization
	// is not lost when the child only overrides @media.
	var out *mpdSegTemplate
	for i := len(ts) - 1; i >= 0; i-- {
		t := ts[i]
		if t == nil {
			continue
		}
		if out == nil {
			cp := *t
			out = &cp
			continue
		}
		if t.Media != "" {
			out.Media = t.Media
		}
		if t.Initialization != "" {
			out.Initialization = t.Initialization
		}
		if t.Timescale != 0 {
			out.Timescale = t.Timescale
		}
		if t.Duration != 0 {
			out.Duration = t.Duration
		}
		if t.StartNumber != nil {
			out.StartNumber = t.StartNumber
		}
		if t.PresentationTimeOffset != 0 {
			out.PresentationTimeOffset = t.PresentationTimeOffset
		}
		if t.SegmentTimeline != nil {
			out.SegmentTimeline = t.SegmentTimeline
		}
	}
	return out
}

func dashKind(as mpdAdaptation, idx int) StreamKind {
	// Every one of these attributes is optional on the AdaptationSet and may be
	// stated on its Representations instead — the DASH-IF MultiResMPEG2 test case
	// puts mimeType, codecs and the frame size all on the Representation and
	// leaves the set bare. Reading only the set then classified every video rung
	// as audio, and the ladder check reported "no video rendition" on a stream
	// that has four of them.
	mime := strings.ToLower(as.MimeType)
	ct := strings.ToLower(as.ContentType)
	codecs := strings.ToLower(as.Codecs)
	height := as.Height
	if len(as.Representations) > 0 {
		rep := as.Representations[0]
		mime = firstNonEmpty(mime, strings.ToLower(rep.MimeType))
		codecs = firstNonEmpty(codecs, strings.ToLower(rep.Codecs))
		height = firstNonZero(height, rep.Height)
	}

	switch {
	case strings.HasPrefix(mime, "video") || ct == "video":
		return Video
	case strings.HasPrefix(mime, "audio") || ct == "audio":
		return Audio
	case strings.HasPrefix(mime, "text") || ct == "text" || ct == "subtitle":
		return Text
	}
	// No mimeType and no contentType: infer from the codecs string.
	switch {
	case strings.HasPrefix(codecs, "avc"), strings.HasPrefix(codecs, "hev"), strings.HasPrefix(codecs, "hvc"), strings.HasPrefix(codecs, "vp0"), strings.HasPrefix(codecs, "av01"):
		return Video
	case strings.HasPrefix(codecs, "mp4a"), strings.HasPrefix(codecs, "ac-3"), strings.HasPrefix(codecs, "ec-3"), strings.HasPrefix(codecs, "opus"):
		return Audio
	}
	if height > 0 {
		return Video
	}
	return Audio
}

func dashName(rep mpdRepresentation, as mpdAdaptation, kind StreamKind, pi, ai, ri int) string {
	if h := firstNonZero(rep.Height, as.Height); h > 0 {
		return fmt.Sprintf("%dp", h)
	}
	if kind == Audio {
		if as.Lang != "" {
			return "audio-" + as.Lang
		}
		if rep.Bandwidth > 0 {
			return fmt.Sprintf("audio-%dkbps", rep.Bandwidth/1000)
		}
	}
	if rep.ID != "" {
		return rep.ID
	}
	return fmt.Sprintf("p%d-as%d-r%d", pi, ai, ri)
}

// parseByteRangeAttr reads a DASH byte-range attribute, "first-last", where both
// ends are inclusive — 0-851 is 852 bytes, not 851. Getting that off by one
// truncates every initialisation segment by a byte.
func parseByteRangeAttr(s string) (ByteRange, bool) {
	s = strings.TrimSpace(s)
	first, last, ok := strings.Cut(s, "-")
	if !ok {
		return ByteRange{}, false
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	end, err2 := strconv.ParseInt(strings.TrimSpace(last), 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start {
		return ByteRange{}, false
	}
	return ByteRange{Offset: start, Length: end - start + 1}, true
}

func dashKeyMethod(protected bool) string {
	if protected {
		return "CENC"
	}
	return ""
}

// applyBaseURLs resolves the BaseURL elements at one level against the current
// base, in document order.
func applyBaseURLs(base *url.URL, urls []string) *url.URL {
	out := base
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if out == nil {
			out = u
			continue
		}
		out = out.ResolveReference(u)
	}
	return out
}

// parseISODuration parses the xs:duration subset MPDs use: PnYnMnDTnHnMnS.
func parseISODuration(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("not an xs:duration: %q", s)
	}
	datePart, timePart, _ := strings.Cut(strings.TrimPrefix(s, "P"), "T")

	var total float64
	consume := func(part string, units map[byte]float64) error {
		num := strings.Builder{}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if (c >= '0' && c <= '9') || c == '.' {
				num.WriteByte(c)
				continue
			}
			mult, ok := units[c]
			if !ok {
				return fmt.Errorf("unexpected unit %q in duration %q", string(c), s)
			}
			v, err := strconv.ParseFloat(num.String(), 64)
			if err != nil {
				return fmt.Errorf("bad number before %q in duration %q", string(c), s)
			}
			total += v * mult
			num.Reset()
		}
		return nil
	}
	if err := consume(datePart, map[byte]float64{'Y': 365 * 24 * 3600, 'M': 30 * 24 * 3600, 'W': 7 * 24 * 3600, 'D': 24 * 3600}); err != nil {
		return 0, err
	}
	if err := consume(timePart, map[byte]float64{'H': 3600, 'M': 60, 'S': 1}); err != nil {
		return 0, err
	}
	if neg {
		total = -total
	}
	return total, nil
}

// parseFrameRate accepts both "25" and the "30000/1001" ratio form.
func parseFrameRate(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if num, den, ok := strings.Cut(s, "/"); ok {
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if err1 == nil && err2 == nil && d != 0 {
			return n / d
		}
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// The SCTE-35 EventStream schemes. Both carry ad-break signalling; anything else
// in an EventStream is some other kind of event, and reporting it as a break would
// send the check hunting a splice point nobody signalled.
var scte35EventSchemes = map[string]bool{
	"urn:scte:scte35:2013:xml":     true,
	"urn:scte:scte35:2014:xml+bin": true,
	"urn:scte:scte35:2013:bin":     true,
}

// dashAdBreaks reads the ad-break signals out of a Period's EventStreams. Each
// stream states its own timescale, which defaults to 1 — not to 90000, which is
// what a reader borrowing MPEG-TS's habits would assume and be wrong about by five
// orders of magnitude.
func dashAdBreaks(streams []mpdEventStream, periodStart float64) []AdBreak {
	var out []AdBreak
	for _, es := range streams {
		if !scte35EventSchemes[strings.ToLower(strings.TrimSpace(es.SchemeIDURI))] {
			continue
		}
		ts := es.Timescale
		if ts == 0 {
			ts = 1
		}
		for _, e := range es.Events {
			b := AdBreak{
				ID:  e.ID,
				Tag: "EventStream",
				// An EventStream event marks a break; the section it carries says
				// whether it opens or closes one, and this reader does not decode
				// the XML form. Treating every one as an opening would be a guess,
				// so the direction is left to the media.
				OutOfNetwork: true,
			}
			if e.Duration > 0 {
				b.Duration = float64(e.Duration) / float64(ts)
			}
			if e.PresentationTime != nil {
				b.MediaTime = periodStart + float64(*e.PresentationTime)/float64(ts)
				b.HasMediaTime = true
			}
			out = append(out, b)
		}
	}
	return out
}

// The SCTE schemes DASH declares closed captions with. There is no other way to
// state them: the captions themselves are in the video bitstream.
const (
	cea608Scheme = "urn:scte:dash:cc:cea-608:2015"
	cea708Scheme = "urn:scte:dash:cc:cea-708:2015"
)

// dashCaptions reads the caption claims out of an Accessibility descriptor and
// normalises them onto the HLS INSTREAM-ID vocabulary, so the check has one set of
// names to compare the bitstream against.
//
// The value is a semicolon-separated list. Under the 608 scheme an entry is either
// "CC1=eng" or a bare language, in which case position gives the channel: the
// first is CC1, the second CC2. Under the 708 scheme it is "1=lang:eng", where the
// number is a service. An entry naming neither is skipped rather than guessed at.
func dashCaptions(descs []struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Value       string `xml:"value,attr"`
}) []Caption {
	var out []Caption
	for _, d := range descs {
		scheme := strings.ToLower(strings.TrimSpace(d.SchemeIDURI))
		if scheme != cea608Scheme && scheme != cea708Scheme {
			continue
		}
		for i, entry := range strings.Split(d.Value, ";") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			key, lang := entry, ""
			if j := strings.IndexByte(entry, '='); j >= 0 {
				key, lang = strings.TrimSpace(entry[:j]), strings.TrimSpace(entry[j+1:])
			} else if scheme == cea608Scheme {
				// A bare language list: position is the channel.
				key, lang = fmt.Sprintf("CC%d", i+1), entry
			}
			lang = strings.TrimPrefix(lang, "lang:")

			id := ""
			switch {
			case scheme == cea708Scheme:
				if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= 63 {
					id = fmt.Sprintf("SERVICE%d", n)
				}
			case strings.HasPrefix(strings.ToUpper(key), "CC"):
				id = strings.ToUpper(key)
			}
			if id == "" {
				continue // a value shape this parser does not recognise
			}
			out = append(out, Caption{InstreamID: id, Language: lang})
		}
	}
	return out
}

// sidecarSubtitle reports whether a mime type names a subtitle file rather than a
// container. A text/vtt or application/ttml+xml representation with only a BaseURL is the
// whole subtitle in one file, and there is no box structure in it to index.
func sidecarSubtitle(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "text/vtt", "application/ttml+xml", "application/mp4;codecs=\"stpp\"":
		return true
	}
	return false
}

// mpegChannelConfigScheme is the one AudioChannelConfiguration scheme whose
// @value is a plain channel count.
const mpegChannelConfigScheme = "urn:mpeg:dash:23003:3:audio_channel_configuration:2011"

// dashChannels reads the channel count from an AudioChannelConfiguration. Only
// the MPEG scheme states a count; Dolby's states a channel *mask* in hex
// ("F801" is 5.1), and reading that as a number would claim 63489 channels. An
// unrecognised scheme leaves the count unknown, because the audio check compares
// the media against this number and a wrong claim reads as a defect in the media.
func dashChannels(cfgs []struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Value       string `xml:"value,attr"`
}) int {
	for _, c := range cfgs {
		if c.SchemeIDURI != mpegChannelConfigScheme {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(c.Value)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func firstNonZero(vs ...int) int {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
