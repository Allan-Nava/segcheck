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
		sawHeader       bool
		pendingStream   map[string]string // EXT-X-STREAM-INF awaiting its URI
		pendingDur      float64
		pendingDisc     bool
		pendingRange    *pendingByteRange
		pendingPDT      time.Time
		havePendingPDT  bool
		expectSegment   bool
		endList         bool
		currentMap      string
		currentMapRange *ByteRange
		keyMethod       string
		keyURI          string
		mediaSeq        int
		seqSet          bool
		// lastRangeEnd is where the previous byte-range segment ended inside
		// lastRangeURI. A range with no explicit offset continues from there,
		// which can only be resolved once the segment's URI is known — the tag
		// comes first in the playlist, the URI on the next line.
		lastRangeEnd int64
		lastRangeURI string
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
			uri := attrs["URI"]
			if uri == "" {
				continue // a CLOSED-CAPTIONS entry muxed into the variants
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
			keyURI = attrs["URI"]
			if keyMethod == "NONE" {
				keyMethod, keyURI = "", ""
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
				})
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
				}
				if pendingRange != nil {
					br := pendingRange.resolve(uri, lastRangeURI, lastRangeEnd)
					seg.ByteRange = &br
					lastRangeURI, lastRangeEnd = uri, br.Offset+br.Length
				}
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
