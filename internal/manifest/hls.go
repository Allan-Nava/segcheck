package manifest

import (
	"bufio"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ParseHLS parses a master or media playlist. baseURL resolves the relative
// URIs that almost every real playlist uses.
func ParseHLS(body []byte, baseURL string) (Playlist, error) {
	pl := Playlist{Kind: KindHLS, URL: baseURL}
	base, _ := url.Parse(baseURL)

	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)

	var (
		sawHeader      bool
		pendingStream  map[string]string // EXT-X-STREAM-INF awaiting its URI
		pendingDur     float64
		pendingDisc    bool
		pendingRange   *pendingByteRange
		pendingPDT     time.Time
		havePendingPDT bool
		// pdtCursor is where the wall clock stands after the last segment
		// emitted. A playlist states EXT-X-PROGRAM-DATE-TIME once and leaves a
		// client to add the declared durations forward from there, so every later
		// segment carries a real claim about the real world even without a tag of
		// its own — and a checker that only looked at the tagged segments would
		// look at one segment per playlist, and at none at all when sampling the
		// live edge.
		pdtCursor       time.Time
		havePDTCursor   bool
		expectSegment   bool
		endList         bool
		currentMap      string
		currentMapRange *ByteRange
		keyMethod       string
		keyURI          string
		keyFormat       string
		keyIV           []byte
		mediaSeq        int
		seqSet          bool
		// lastRangeEnd is where the previous byte-range segment ended inside
		// lastRangeURI. A range with no explicit offset continues from there,
		// which can only be resolved once the segment's URI is known — the tag
		// comes first in the playlist, the URI on the next line.
		lastRangeEnd int64
		lastRangeURI string
		// pendingParts are the EXT-X-PART lines seen since the last segment URI.
		// They belong to the segment whose URI comes *after* them — a part is
		// published before the segment it makes up exists, which is the whole
		// point of low latency — and the ones still pending at the end of the
		// playlist belong to the segment being published right now.
		pendingParts []Part
		// lastPartRangeEnd and lastPartRangeURI carry the "continue from the
		// previous range" rule across parts, which have their own chain: every
		// part of a segment is usually a range of the same growing file.
		lastPartRangeEnd int64
		lastPartRangeURI string
		// ccGroups holds the CLOSED-CAPTIONS declarations by GROUP-ID, and
		// ccGroupOf which group each variant named. Both are resolved after the
		// scan: nothing in the spec requires the EXT-X-MEDIA entries to come
		// before the variant that references them.
		ccGroups  = map[string][]Caption{}
		ccGroupOf = map[int]string{}
	)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !sawHeader {
			if !strings.HasPrefix(line, "#EXTM3U") {
				return pl, fmt.Errorf("not an HLS playlist: missing #EXTM3U")
			}
			sawHeader = true
			continue
		}

		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pl.Master = true
			pendingStream = parseAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))

		case strings.HasPrefix(line, "#EXT-X-I-FRAME-STREAM-INF:"):
			// Unlike EXT-X-STREAM-INF this tag carries its URI as an attribute, so
			// there is no following line to wait for and the rendition is complete
			// here.
			pl.Master = true
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-I-FRAME-STREAM-INF:"))
			if uri := attrs["URI"]; uri != "" {
				w, h := attrResolution(attrs)
				pl.Renditions = append(pl.Renditions, Rendition{
					Name:      renditionName(attrs, w, h),
					URI:       Resolve(base, uri),
					Kind:      IFrame,
					Bandwidth: attrInt(attrs, "BANDWIDTH"),
					Width:     w,
					Height:    h,
					Codecs:    attrs["CODECS"],
				})
			}

		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			pl.Master = true
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			kind := Audio
			switch strings.ToUpper(attrs["TYPE"]) {
			case "AUDIO":
				kind = Audio
			case "SUBTITLES", "CLOSED-CAPTIONS":
				kind = Text
			case "VIDEO":
				kind = Video
			}
			if strings.EqualFold(attrs["TYPE"], "CLOSED-CAPTIONS") {
				// Captions are muxed into the video, so there is nothing to fetch
				// and nothing to make a rendition of. The entry is a claim about
				// what the variants that name its group already carry.
				if id := attrs["INSTREAM-ID"]; id != "" {
					g := attrs["GROUP-ID"]
					ccGroups[g] = append(ccGroups[g], Caption{
						InstreamID: strings.ToUpper(id),
						Language:   attrs["LANGUAGE"],
						Name:       attrs["NAME"],
						GroupID:    g,
					})
				}
				continue
			}
			uri := attrs["URI"]
			if uri == "" {
				continue // an entry with nothing at the other end of it
			}
			name := attrs["NAME"]
			if name == "" {
				name = attrs["LANGUAGE"]
			}
			pl.Renditions = append(pl.Renditions, Rendition{
				Name:     name,
				URI:      Resolve(base, uri),
				Kind:     kind,
				Language: attrs["LANGUAGE"],
				GroupID:  attrs["GROUP-ID"],
				Channels: parseHLSChannels(attrs["CHANNELS"]),
			})

		case strings.HasPrefix(line, "#EXT-X-DATERANGE:"):
			// A DATERANGE is only an ad break when it carries an SCTE35 attribute.
			// Without one it is a chapter, a programme boundary, or anything else an
			// operator chose to mark, and treating it as a break would have the check
			// hunting a splice point nobody signalled.
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-DATERANGE:"))
			out, hasOut := attrs["SCTE35-OUT"]
			in, hasIn := attrs["SCTE35-IN"]
			cmd, hasCmd := attrs["SCTE35-CMD"]
			if hasOut || hasIn || hasCmd {
				b := AdBreak{
					ID:              attrs["ID"],
					OutOfNetwork:    hasOut,
					Duration:        attrFloat(attrs, "DURATION"),
					PlannedDuration: attrFloat(attrs, "PLANNED-DURATION"),
					Tag:             "EXT-X-DATERANGE",
					Section:         parseHexSection(firstNonEmpty(out, in, cmd)),
				}
				if ts, err := parsePDT(attrs["START-DATE"]); err == nil {
					b.Start, b.HasStart = ts, true
				}
				pl.AdBreaks = append(pl.AdBreaks, b)
			}

		case strings.HasPrefix(line, "#EXT-X-CUE-OUT"), line == "#EXT-X-CUE-IN":
			// CUE-OUT and CUE-IN are not in the specification, but they are what
			// most packagers emit. They sit between segments, so the segment that
			// follows is where the break begins — no wall clock needed, and a
			// boundary by construction. CUE-OUT-CONT only restates a break already
			// under way.
			if strings.HasPrefix(line, "#EXT-X-CUE-OUT-CONT") {
				break
			}
			b := AdBreak{
				OutOfNetwork: line != "#EXT-X-CUE-IN",
				Sequence:     mediaSeq + len(pl.Segments),
				HasSequence:  true,
				Tag:          strings.SplitN(line, ":", 2)[0],
			}
			if rest := strings.TrimPrefix(line, "#EXT-X-CUE-OUT"); b.OutOfNetwork && rest != "" {
				// The duration is written either bare or as DURATION=n.
				rest = strings.TrimPrefix(rest, ":")
				if d, err := strconv.ParseFloat(strings.TrimSpace(rest), 64); err == nil {
					b.Duration = d
				} else {
					b.Duration = attrFloat(parseAttrs(rest), "DURATION")
				}
			}
			pl.AdBreaks = append(pl.AdBreaks, b)

		case strings.HasPrefix(line, "#EXT-X-PART-INF:"):
			pl.PartTarget = attrFloat(parseAttrs(strings.TrimPrefix(line, "#EXT-X-PART-INF:")), "PART-TARGET")

		case strings.HasPrefix(line, "#EXT-X-SERVER-CONTROL:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-SERVER-CONTROL:"))
			pl.PartHoldBack = attrFloat(attrs, "PART-HOLD-BACK")
			pl.CanBlockReload = strings.EqualFold(attrs["CAN-BLOCK-RELOAD"], "YES")

		case strings.HasPrefix(line, "#EXT-X-PART:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-PART:"))
			uri := attrs["URI"]
			if uri == "" {
				break // a part with no URI is nothing a player can fetch
			}
			part := Part{
				URI:         Resolve(base, uri),
				Duration:    attrFloat(attrs, "DURATION"),
				Independent: strings.EqualFold(attrs["INDEPENDENT"], "YES"),
				Gap:         strings.EqualFold(attrs["GAP"], "YES"),
				Sequence:    mediaSeq + len(pl.Segments),
				Index:       len(pendingParts),
			}
			if p := parseByteRange(attrs["BYTERANGE"]); p != nil {
				br := p.resolve(part.URI, lastPartRangeURI, lastPartRangeEnd)
				part.ByteRange = &br
				lastPartRangeURI, lastPartRangeEnd = part.URI, br.Offset+br.Length
			}
			pendingParts = append(pendingParts, part)

		case strings.HasPrefix(line, "#EXT-X-PRELOAD-HINT:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-PRELOAD-HINT:"))
			if uri := attrs["URI"]; uri != "" {
				pl.PreloadHint = &PreloadHint{
					Type:            strings.ToUpper(attrs["TYPE"]),
					URI:             Resolve(base, uri),
					ByteRangeStart:  int64(attrFloat(attrs, "BYTERANGE-START")),
					ByteRangeLength: int64(attrFloat(attrs, "BYTERANGE-LENGTH")),
				}
			}

		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			pl.TargetDuration, _ = strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64)

		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			mediaSeq, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			pl.MediaSequence = mediaSeq
			seqSet = true

		case strings.HasPrefix(line, "#EXTINF:"):
			pendingDur = extinfDuration(line)
			expectSegment = true

		case line == "#EXT-X-DISCONTINUITY":
			pendingDisc = true

		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if uri := attrs["URI"]; uri != "" {
				currentMap = Resolve(base, uri)
				currentMapRange = nil
				// BYTERANGE here has no "continue from the previous range"
				// rule: a missing offset means the start of the resource.
				if p := parseByteRange(attrs["BYTERANGE"]); p != nil {
					br := p.resolve("", "", 0)
					currentMapRange = &br
				}
			}

		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			keyMethod = strings.ToUpper(attrs["METHOD"])
			// Resolved against the playlist, like every other URI here: stored raw it
			// is unfetchable from anywhere but the playlist's own directory.
			keyURI = ""
			if u := attrs["URI"]; u != "" {
				keyURI = Resolve(base, u)
			}
			keyFormat = attrs["KEYFORMAT"]
			keyIV = parseHexIV(attrs["IV"])
			if keyMethod == "NONE" {
				keyMethod, keyURI, keyFormat, keyIV = "", "", "", nil
			}

		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			pendingRange = parseByteRange(strings.TrimPrefix(line, "#EXT-X-BYTERANGE:"))

		case strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"):
			if ts, err := parsePDT(strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:")); err == nil {
				pendingPDT, havePendingPDT = ts, true
			}

		case line == "#EXT-X-ENDLIST":
			endList = true

		case strings.HasPrefix(line, "#"):
			// Any other tag: not needed for segment analysis.

		default: // a URI line closes whichever tag was pending
			switch {
			case pendingStream != nil:
				w, h := attrResolution(pendingStream)
				pl.Renditions = append(pl.Renditions, Rendition{
					Name:         renditionName(pendingStream, w, h),
					URI:          Resolve(base, line),
					Kind:         streamInfKind(pendingStream, h),
					Bandwidth:    attrInt(pendingStream, "BANDWIDTH"),
					AvgBandwidth: attrInt(pendingStream, "AVERAGE-BANDWIDTH"),
					Width:        w,
					Height:       h,
					Codecs:       pendingStream["CODECS"],
					FrameRate:    attrFloat(pendingStream, "FRAME-RATE"),
					AudioGroup:   pendingStream["AUDIO"],
					// NONE is a positive claim that there are no captions; an
					// absent attribute claims nothing either way.
					CaptionsNone: strings.EqualFold(pendingStream["CLOSED-CAPTIONS"], "NONE"),
				})
				if g := pendingStream["CLOSED-CAPTIONS"]; g != "" && !strings.EqualFold(g, "NONE") {
					ccGroupOf[len(pl.Renditions)-1] = g
				}
				pendingStream = nil

			case expectSegment:
				uri := Resolve(base, line)
				seg := Segment{
					URI:           uri,
					Duration:      pendingDur,
					Discontinuity: pendingDisc,
					InitURI:       currentMap,
					InitRange:     currentMapRange,
					Sequence:      mediaSeq + len(pl.Segments),
					PDT:           pendingPDT,
					HasPDT:        havePendingPDT,
					KeyMethod:     keyMethod,
					KeyURI:        keyURI,
					KeyFormat:     keyFormat,
					KeyIV:         keyIV,
				}
				if pendingRange != nil {
					br := pendingRange.resolve(uri, lastRangeURI, lastRangeEnd)
					seg.ByteRange = &br
					lastRangeURI, lastRangeEnd = uri, br.Offset+br.Length
				}
				// A segment with no tag of its own inherits the running wall
				// clock. After a discontinuity with no fresh tag there is none:
				// the timeline restarted and the old anchor says nothing about
				// what follows, so segcheck would be comparing the media against
				// a number it made up itself.
				if !seg.HasPDT && havePDTCursor {
					seg.PDT, seg.HasPDT, seg.PDTDerived = pdtCursor, true, true
				}
				if seg.Discontinuity && !havePendingPDT {
					seg.PDT, seg.HasPDT, seg.PDTDerived = time.Time{}, false, false
					havePDTCursor = false
				}
				if seg.HasPDT {
					pdtCursor, havePDTCursor = seg.PDT.Add(time.Duration(seg.Duration*float64(time.Second))), true
				}
				seg.Parts = pendingParts
				pendingParts = nil
				pl.Segments = append(pl.Segments, seg)
				pendingDur, pendingDisc, pendingRange = 0, false, nil
				havePendingPDT = false
				expectSegment = false
			}
		}
	}
	if err := sc.Err(); err != nil {
		return pl, err
	}
	if !sawHeader {
		return pl, fmt.Errorf("empty playlist")
	}
	if !pl.Master && len(pl.Segments) == 0 {
		return pl, fmt.Errorf("playlist has neither variants nor segments")
	}
	// Attach the caption claims now that every group and every variant has been
	// seen, whichever order they appeared in.
	for i, g := range ccGroupOf {
		pl.Renditions[i].Captions = ccGroups[g]
	}
	// Whatever parts are still pending belong to the segment being published
	// right now, which has no URI line yet. Attaching them to the last complete
	// segment would count its media twice.
	pl.PendingParts = pendingParts
	// A media playlist without EXT-X-ENDLIST is a sliding window: live.
	pl.Live = !pl.Master && !endList
	_ = seqSet
	return pl, nil
}

// streamInfKind classifies an EXT-X-STREAM-INF variant. One with no RESOLUTION
// whose CODECS lists audio only is an audio-only variant — the bottom rung of
// many real ladders — and calling it a video rendition would make its missing
// video track look like a defect.
func streamInfKind(attrs map[string]string, height int) StreamKind {
	if codecs := attrs["CODECS"]; codecs != "" {
		switch {
		case hasVideoCodec(codecs):
			return Video
		case hasAudioCodec(codecs):
			return Audio
		}
	}
	if height > 0 {
		return Video
	}
	// Neither attribute says: a variant is video far more often than not, and
	// the tracks check knows to stay quiet when nothing was declared.
	return Video
}

// DeclaresVideo reports whether the manifest itself claims this rendition
// carries video, by resolution or by codec.
func (r Rendition) DeclaresVideo() bool {
	return (r.Width > 0 && r.Height > 0) || hasVideoCodec(r.Codecs)
}

func hasVideoCodec(codecs string) bool {
	return hasAnyCodecPrefix(codecs, "avc1", "avc3", "hvc1", "hev1", "dvh1", "dvhe", "vvc1", "vvi1", "vp08", "vp09", "vp9", "av01", "mp4v")
}

func hasAudioCodec(codecs string) bool {
	return hasAnyCodecPrefix(codecs, "mp4a", "ac-3", "ec-3", "ac-4", "opus", "flac", "alac", "dts")
}

func hasAnyCodecPrefix(codecs string, prefixes ...string) bool {
	for _, c := range strings.Split(strings.ToLower(codecs), ",") {
		c = strings.TrimSpace(c)
		for _, p := range prefixes {
			if strings.HasPrefix(c, p) {
				return true
			}
		}
	}
	return false
}

// renditionName labels a variant for output: the resolution when it has one
// (what an operator recognises), else the bitrate.
func renditionName(attrs map[string]string, w, h int) string {
	if h > 0 {
		return fmt.Sprintf("%dp", h)
	}
	if bw := attrInt(attrs, "BANDWIDTH"); bw > 0 {
		return fmt.Sprintf("%dkbps", bw/1000)
	}
	return "variant"
}

func extinfDuration(line string) float64 {
	rest := strings.TrimPrefix(line, "#EXTINF:")
	if i := strings.IndexByte(rest, ','); i >= 0 {
		rest = rest[:i]
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	return d
}

// pendingByteRange is an EXT-X-BYTERANGE whose offset may still be implicit.
// The tag appears one line before the URI it applies to, and an implicit offset
// means "where the previous range in *this same resource* ended" — so it cannot
// be resolved until the URI is read.
type pendingByteRange struct {
	length int64
	offset int64
	hasOff bool
}

// resolve fixes the offset now that the segment's URI is known.
func (p pendingByteRange) resolve(uri, prevURI string, prevEnd int64) ByteRange {
	if p.hasOff {
		return ByteRange{Length: p.length, Offset: p.offset}
	}
	if uri == prevURI {
		return ByteRange{Length: p.length, Offset: prevEnd}
	}
	return ByteRange{Length: p.length, Offset: 0}
}

// parseByteRange parses "LENGTH[@OFFSET]".
func parseByteRange(s string) *pendingByteRange {
	s = strings.TrimSpace(s)
	lenStr, offStr, hasOff := strings.Cut(s, "@")
	length, err := strconv.ParseInt(strings.TrimSpace(lenStr), 10, 64)
	if err != nil || length <= 0 {
		return nil
	}
	out := &pendingByteRange{length: length}
	if hasOff {
		off, err := strconv.ParseInt(strings.TrimSpace(offStr), 10, 64)
		if err != nil {
			return out // a malformed offset falls back to the implicit rule
		}
		out.offset, out.hasOff = off, true
	}
	return out
}

// parsePDT accepts the ISO-8601 forms real packagers emit, including the
// fractional seconds RFC3339 handles and the space-separated variant it does not.
func parsePDT(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable EXT-X-PROGRAM-DATE-TIME %q", s)
}

// parseHLSChannels reads the count out of the CHANNELS attribute, and only when
// that count describes the coded audio.
//
// The value is a slash-separated list. A bare first element is the channel count
// and is directly comparable to what the segments carry. Once a spatial-coding
// identifier follows it, the count is the maximum number of channels the
// *presentation* renders, not the number the bed codes: Dolby Atmos ships as
// "16/JOC" over a 5.1 E-AC-3 bed, and comparing 16 against 6 would report Apple's
// own Atmos reference stream as broken. Such a value is left unstated, because a
// claim this tool cannot compare is not a claim it should pretend to check.
func parseHLSChannels(v string) int {
	if v == "" || strings.Contains(v, "/") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// parseHexIV reads the EXT-X-KEY IV attribute, a 0x-prefixed hexadecimal number of
// exactly sixteen bytes. Anything else is not an IV, and returning a short or
// mis-sized one would decrypt every segment to noise.
func parseHexIV(v string) []byte {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if len(v) > 2 && (v[:2] == "0x" || v[:2] == "0X") {
		v = v[2:]
	}
	if len(v) != 32 {
		return nil
	}
	out := make([]byte, 16)
	for i := 0; i < 16; i++ {
		hi, ok1 := hexDigit(v[i*2])
		lo, ok2 := hexDigit(v[i*2+1])
		if !ok1 || !ok2 {
			return nil
		}
		out[i] = hi<<4 | lo
	}
	return out
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// parseHexSection decodes the hexadecimal-sequence an SCTE35 attribute carries. An
// odd length, a stray character or an empty value is not a section: returning a
// partial one would have the check compare the media against half a header.
func parseHexSection(v string) []byte {
	v = strings.TrimSpace(v)
	if len(v) > 2 && (v[:2] == "0x" || v[:2] == "0X") {
		v = v[2:]
	}
	if v == "" || len(v)%2 != 0 {
		return nil
	}
	out := make([]byte, len(v)/2)
	for i := range out {
		hi, ok1 := hexDigit(v[i*2])
		lo, ok2 := hexDigit(v[i*2+1])
		if !ok1 || !ok2 {
			return nil
		}
		out[i] = hi<<4 | lo
	}
	return out
}
