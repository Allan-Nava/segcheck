package mediatest

import (
	"fmt"
	"strings"
)

// Subtitle builders.
//
// A subtitle rendition is delivered as text, not as a container: WebVTT segments
// in HLS, TTML/IMSC in DASH, and either wrapped in fMP4 with an `stpp` or `wvtt`
// sample entry. The cue times are the whole point — they are what has to line up
// with the media timeline, and the way they fail to is by drifting away from it.

// Cue is one subtitle cue, in seconds on the segment's own clock.
type Cue struct {
	Start, End float64
	Text       string
}

// WebVTTOptions controls what the builder plants.
type WebVTTOptions struct {
	// Cues are written in the order given.
	Cues []Cue
	// NoTimestampMap omits the X-TIMESTAMP-MAP line. Without it a player has
	// nothing to anchor the cue clock to the media clock with, which is how a
	// subtitle rendition ends up correct in itself and wrong against the video.
	NoTimestampMap bool
	// MPEGTS and Local are the two halves of X-TIMESTAMP-MAP: the 90kHz media
	// timestamp that the local cue time Local corresponds to.
	MPEGTS int64
	Local  float64
	// NoHeader omits the WEBVTT signature, which makes the bytes not WebVTT at all.
	NoHeader bool
}

// WebVTT builds a WebVTT segment.
func WebVTT(opts WebVTTOptions) []byte {
	var b strings.Builder
	if !opts.NoHeader {
		b.WriteString("WEBVTT\n")
	}
	if !opts.NoTimestampMap {
		fmt.Fprintf(&b, "X-TIMESTAMP-MAP=LOCAL:%s,MPEGTS:%d\n", vttTime(opts.Local), opts.MPEGTS)
	}
	for _, c := range opts.Cues {
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s --> %s\n", vttTime(c.Start), vttTime(c.End))
		text := c.Text
		if text == "" {
			text = "subtitle"
		}
		b.WriteString(text + "\n")
	}
	return []byte(b.String())
}

// vttTime writes the hh:mm:ss.mmm form WebVTT uses.
func vttTime(sec float64) string {
	ms := int64(sec*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// TTMLOptions controls what the TTML builder plants.
type TTMLOptions struct {
	Cues []Cue
	// Offset writes the cue times as offset-time ("4s") rather than clock-time,
	// which is the other form the specification allows and the one a reader that
	// only split on colons gets wrong.
	Offset bool
	// NotXML makes the bytes not parse as XML at all.
	NotXML bool
	// WrongRoot writes a root element that is not tt, so the bytes are XML but not
	// TTML.
	WrongRoot bool
}

// TTML builds a TTML/IMSC subtitle document.
func TTML(opts TTMLOptions) []byte {
	if opts.NotXML {
		return []byte("<tt><body><div><p begin=")
	}
	root := "tt"
	if opts.WrongRoot {
		root = "html"
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<%s xmlns="http://www.w3.org/ns/ttml" xml:lang="en">`+"\n", root)
	b.WriteString("<body><div>\n")
	for _, c := range opts.Cues {
		begin, end := ttmlClock(c.Start), ttmlClock(c.End)
		if opts.Offset {
			begin, end = fmt.Sprintf("%gs", c.Start), fmt.Sprintf("%gs", c.End)
		}
		text := c.Text
		if text == "" {
			text = "subtitle"
		}
		fmt.Fprintf(&b, `<p begin="%s" end="%s">%s</p>`+"\n", begin, end, text)
	}
	fmt.Fprintf(&b, "</div></body>\n</%s>\n", root)
	return []byte(b.String())
}

func ttmlClock(sec float64) string {
	ms := int64(sec*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// MP4InitSubtitle builds an fMP4 init declaring a subtitle track: an stpp sample
// entry for TTML or wvtt for WebVTT, under the `subt` handler.
func MP4InitSubtitle(trackID, timescale uint32, sampleEntryType string) []byte {
	// Both extend SampleEntry directly: six reserved bytes and a data reference
	// index, then the codec's own fields.
	entry := box(sampleEntryType, concat(make([]byte, 6), []byte{0x00, 0x01}))
	return mp4InitFrom(trackID, timescale, "subt", entry, nil, 0, 0)
}
