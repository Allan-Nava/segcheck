package media

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// Subtitle segments.
//
// A subtitle rendition is text, not a container: WebVTT in HLS, TTML/IMSC in DASH,
// and either one wrapped in fMP4 with a `wvtt` or `stpp` sample entry.
//
// The cue times are the whole point. A subtitle rendition rarely fails by being
// malformed; it fails by being perfectly valid and pointing somewhere else, and no
// manifest checker can see that because the manifest says nothing about where the
// cues are.
//
// WebVTT counts on its own clock and anchors it to the media clock with
// X-TIMESTAMP-MAP. Without that line there is nothing to anchor it with, so the
// local cue times are reported as a cue count and no media span at all — reporting
// them as media times would place every cue at the start of the presentation and
// then declare the rendition perfectly aligned.

// maxCuesPerSegment bounds how many cues are counted from one segment. A subtitle
// segment with more than this is malformed rather than talkative, and the count is
// all the checks need.
const maxCuesPerSegment = 100000

// maxSubtitleBytes bounds how much text is scanned. A subtitle segment is a few
// kilobytes; anything of this size is not one.
const maxSubtitleBytes = 8 << 20

// ParseWebVTT reads a WebVTT subtitle segment.
func ParseWebVTT(data []byte) (SegmentInfo, error) {
	info := SegmentInfo{Container: ContainerWebVTT, Bytes: int64(len(data))}
	if !looksWebVTT(data) {
		return info, fmt.Errorf("%w: no WEBVTT signature", ErrUnknownContainer)
	}
	if len(data) > maxSubtitleBytes {
		data = data[:maxSubtitleBytes]
	}

	// MinPTS and MaxPTS are the earliest cue start and the latest cue end: min and
	// max of the timestamps present, which is what a cue's two of them amount to.
	// They are a span rather than a first-and-last because that is what a check
	// comparing against a segment's window needs — a cue continuing across a
	// boundary appears in both segments and overhangs one of them at each end.
	track := Track{ID: 1, Kind: Text, Codec: "webvtt", Timescale: TSTimescale}

	// The map states which 90kHz media timestamp a local cue time corresponds to.
	mapOffset, haveMap := webVTTTimestampMap(data)

	var (
		first, last float64
		haveCue     bool
		cues        int
	)
	for _, line := range strings.Split(string(data), "\n") {
		start, end, ok := parseWebVTTTiming(line)
		if !ok {
			continue
		}
		cues++
		if cues > maxCuesPerSegment {
			break
		}
		if !haveCue || start < first {
			first = start
		}
		if !haveCue || end > last {
			last = end
		}
		haveCue = true
	}
	track.Samples = cues
	// For a text segment the cues *are* the samples, but the check reads Cues so it
	// has one field to read whether the rendition arrived as text or wrapped in fMP4.
	track.Cues, track.CuesRead = cues, true
	if haveCue {
		// The cue span is always reported, with the map applied when there is one.
		// Without one the local times stand: DASH does not use X-TIMESTAMP-MAP and puts
		// cue times on the presentation timeline directly, so dropping them left a real
		// sidecar rendition unplaceable when it was not.
		offset := 0.0
		if haveMap {
			offset = mapOffset
		}
		track.CueMin = int((first + offset) * TSTimescale)
		track.CueMax = int((last + offset) * TSTimescale)
		track.HasCueSpan = true
		track.CuesAnchored = haveMap
		if haveMap {
			// HasPTS says the cue clock is tied to the media clock, which is what an
			// HLS rendition needs the map for.
			track.MinPTS, track.MaxPTS = int64(track.CueMin), int64(track.CueMax)
			track.HasPTS = true
		}
	}
	info.Tracks = append(info.Tracks, track)
	return info, nil
}

// webVTTTimestampMap reads X-TIMESTAMP-MAP and returns the media time, in seconds,
// that a local cue time of zero corresponds to.
func webVTTTimestampMap(data []byte) (float64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := cutPrefixFold(line, "X-TIMESTAMP-MAP")
		if !ok {
			continue
		}
		rest = strings.TrimPrefix(strings.TrimSpace(rest), "=")

		var (
			local        float64
			mpegts       int64
			haveL, haveM bool
		)
		for _, part := range strings.Split(rest, ",") {
			k, v, found := strings.Cut(strings.TrimSpace(part), ":")
			if !found {
				continue
			}
			switch strings.ToUpper(strings.TrimSpace(k)) {
			case "LOCAL":
				// LOCAL's value is itself a colon-separated timestamp, so Cut has
				// already split it: put it back together.
				if t, ok := parseWebVTTTimestamp(v); ok {
					local, haveL = t, true
				}
			case "MPEGTS":
				if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
					mpegts, haveM = n, true
				}
			}
		}
		if haveM {
			if !haveL {
				local = 0 // LOCAL defaults to the start of the segment
			}
			return float64(mpegts)/TSTimescale - local, true
		}
	}
	return 0, false
}

// parseWebVTTTiming reads a cue timing line: "00:00:01.000 --> 00:00:03.000".
func parseWebVTTTiming(line string) (start, end float64, ok bool) {
	left, right, found := strings.Cut(line, "-->")
	if !found {
		return 0, 0, false
	}
	start, ok = parseWebVTTTimestamp(left)
	if !ok {
		return 0, 0, false
	}
	// Cue settings follow the end timestamp on the same line.
	right = strings.TrimSpace(right)
	if i := strings.IndexAny(right, " \t"); i >= 0 {
		right = right[:i]
	}
	end, ok = parseWebVTTTimestamp(right)
	if !ok {
		return 0, 0, false
	}
	return start, end, true
}

// parseWebVTTTimestamp reads mm:ss.mmm or hh:mm:ss.mmm.
func parseWebVTTTimestamp(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// A sign has no place in a cue timestamp, and "-00" parses to negative zero,
	// which the per-field check below would let through.
	if strings.ContainsAny(s, "+-") {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total float64
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		// The last field carries the fraction; the ones before it are whole.
		if i < len(parts)-1 && v != float64(int64(v)) {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

// cutPrefixFold is strings.CutPrefix, case-insensitively.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

// looksWebVTT reports whether the bytes carry the WEBVTT signature, which the
// specification requires at the very start — after an optional byte order mark.
func looksWebVTT(data []byte) bool {
	b := data
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	if len(b) < 6 || string(b[:6]) != "WEBVTT" {
		return false
	}
	// The signature must be followed by a space, a tab or a line break.
	if len(b) == 6 {
		return true
	}
	switch b[6] {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// ttmlDoc is the part of a TTML document these checks need: the cue times.
type ttmlDoc struct {
	XMLName xml.Name
	Paras   []struct {
		Begin string `xml:"begin,attr"`
		End   string `xml:"end,attr"`
		Dur   string `xml:"dur,attr"`
	} `xml:"body>div>p"`
}

// ParseTTML reads a TTML or IMSC subtitle document.
func ParseTTML(data []byte) (SegmentInfo, error) {
	info := SegmentInfo{Container: ContainerTTML, Bytes: int64(len(data))}
	if len(data) > maxSubtitleBytes {
		data = data[:maxSubtitleBytes]
	}
	var doc ttmlDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return info, fmt.Errorf("%w: not XML: %v", ErrUnknownContainer, err)
	}
	if doc.XMLName.Local != "tt" {
		return info, fmt.Errorf("%w: XML root is %q, not tt", ErrUnknownContainer, doc.XMLName.Local)
	}

	track := Track{ID: 1, Kind: Text, Codec: "ttml", Timescale: TSTimescale}
	var (
		first, last float64
		haveCue     bool
	)
	for i, p := range doc.Paras {
		if i >= maxCuesPerSegment {
			break
		}
		track.Samples++
		track.Cues++
		begin, okB := parseTTMLTime(p.Begin)
		if !okB {
			continue
		}
		end, okE := parseTTMLTime(p.End)
		if !okE {
			// A cue may state a duration instead of an end.
			if d, ok := parseTTMLTime(p.Dur); ok {
				end, okE = begin+d, true
			}
		}
		if !okE {
			end = begin
		}
		if !haveCue || begin < first {
			first = begin
		}
		if !haveCue || end > last {
			last = end
		}
		haveCue = true
	}
	track.CuesRead = true
	if haveCue {
		// TTML counts on the media timeline already: there is no map to apply.
		track.MinPTS = int64(first * TSTimescale)
		track.MaxPTS = int64(last * TSTimescale)
		track.HasPTS = true
		track.CueMin, track.CueMax = int(track.MinPTS), int(track.MaxPTS)
		track.HasCueSpan, track.CuesAnchored = true, true
	}
	info.Tracks = append(info.Tracks, track)
	return info, nil
}

// parseTTMLTime reads the time expressions TTML allows. Clock-time is
// hh:mm:ss(.fraction); offset-time is a number with a metric suffix. A frame or
// tick offset needs a rate this reader is not given, so it states no time rather
// than a wrong one.
func parseTTMLTime(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, ":") {
		return parseWebVTTTimestamp(s)
	}
	for _, suffix := range []struct {
		unit  string
		scale float64
	}{
		{"ms", 0.001},
		{"h", 3600},
		{"m", 60},
		{"s", 1},
	} {
		num, ok := strings.CutSuffix(s, suffix.unit)
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(num, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		return v * suffix.scale, true
	}
	// "f" is frames and "t" is ticks: both need a rate stated elsewhere in the
	// document, and guessing one would put every cue in the wrong place.
	return 0, false
}

// looksTTML reports whether the bytes plausibly start a TTML document. The root
// element has to be found rather than assumed: an XML declaration, comments and
// processing instructions may all precede it.
func looksTTML(data []byte) bool {
	limit := data
	if len(limit) > 4096 {
		limit = limit[:4096]
	}
	s := string(limit)
	if !strings.Contains(s, "<") {
		return false
	}
	return strings.Contains(s, "<tt ") || strings.Contains(s, "<tt>") ||
		strings.Contains(s, ":tt ") || strings.Contains(s, "http://www.w3.org/ns/ttml")
}

// subtitleSampleCues counts the cues in an fMP4 subtitle track's samples.
//
// A stpp sample is a TTML document; a wvtt sample is a sequence of boxes, where vttc
// holds a cue and vtte says nothing is displayed. Reporting the sample count alone
// could not tell a rendition that says nothing from one that says plenty — which is
// exactly how a subtitle pipeline breaks — so this reads inside them.
//
// It reports false when nothing was readable: zero cues and no cue count are opposite
// answers, and conflating them would report a broken rendition as merely quiet.
func subtitleSampleCues(codec string, data []byte, ranges []sampleRange) (sampleCues, bool) {
	if len(ranges) == 0 {
		return sampleCues{}, false
	}
	var out sampleCues
	read := false
	for _, r := range ranges {
		sample := data[r.start:r.end]
		switch codec {
		case "ttml":
			info, err := ParseTTML(sample)
			if err != nil {
				continue
			}
			read = true
			if t, ok := info.Track(Text); ok {
				out.cues += t.Samples
				// A TTML document counts from the start of the fragment carrying it,
				// so its own span is an offset the caller adds the decode time to.
				if t.HasPTS {
					if !out.haveSpan || t.MinPTS < out.min {
						out.min = t.MinPTS
					}
					if !out.haveSpan || t.MaxPTS > out.max {
						out.max = t.MaxPTS
					}
					out.haveSpan = true
				}
			}
		case "webvtt":
			// The boxes a wvtt sample is made of. An empty sample is a vtte box,
			// which is a statement that nothing is displayed rather than a cue.
			boxes := boxesIn(sample)
			if len(boxes) == 0 {
				continue
			}
			// A wvtt sample times its cue by the sample's own duration rather than
			// inside the payload, so there is no per-cue span to derive: reporting
			// the fragment's would claim a placement nobody stated.
			for _, b := range boxes {
				switch b.typ {
				case "vttc":
					out.cues++
					read = true
				case "vtte":
					read = true
				}
			}
		}
	}
	return out, read
}

// sampleCues is what reading inside a wrapped subtitle track's samples yields.
type sampleCues struct {
	cues     int
	min, max int64
	haveSpan bool
}
