package analyze

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
)

// Whether an origin honours a `Range` request is a property of the origin, not
// of a segment, and it is the one delivery fact a stream can be entirely broken
// by while every byte of media on disk is perfect.
//
// Two things make it worth a check of its own.
//
// The first is that a stream packaged as byte ranges of one resource — HLS
// `EXT-X-BYTERANGE`, which is how Apple's own MPEG-TS reference is built, and
// DASH `SegmentBase` with an `indexRange`, which is the whole on-demand profile
// — does not degrade against an origin with range support off. It fails, and it
// fails in a shape that reads as a media defect: every segment a player asks for
// arrives as the entire file, so the timeline overlaps itself, the durations come
// out several hundred percent long, and the transport-stream continuity counters
// break at every join. segcheck reported exactly that before this check existed:
// eight findings about the media and four identical per-segment notes, for one
// misconfiguration on one host. The cause has to be named once, above the
// symptoms, or the operator reads the symptoms.
//
// The second is that `Accept-Ranges: bytes` is a claim and honouring a request is
// the fact, which is this tool's entire job pointed at delivery instead of at
// media. An origin that advertises range support and answers 200 to a Range
// request is lying to every client that reads the header — a player deciding it
// can seek cheaply, a packager deciding it can publish a single-file rendition.
//
// What it is deliberately not: a defect in every stream that meets it. A
// playlist addressing whole resources never sends a Range at all, so an origin
// that ignores one costs that stream nothing beyond a more expensive seek. That
// case is reported at OK — a fact about the delivery path, recorded because the
// next packaging decision depends on it, not a fault anyone should go hunting.
//
// The probe costs nothing on a stream that already uses byte ranges: the answer
// is in the responses the sample already produced. It costs one small request per
// host otherwise.

// byteRangeProbeBytes is how much of a segment the probe asks for. It only has
// to be less than the resource, so that a 206 is distinguishable from a 200 that
// happens to be the whole thing.
const byteRangeProbeBytes = int64(1024)

// byteRangeProbe is one host's answer to a Range request.
type byteRangeProbe struct {
	host string
	// requested is how many bytes were asked for and got how many came back. A
	// 206 that returns more than was asked for did not honour the range either.
	requested, got int64
	status         int
	// advertised is whether the origin sent Accept-Ranges: bytes, which is the
	// claim the measurement is compared against.
	advertised bool
	// needed is whether this stream cannot play without range support: any
	// sampled segment on this host is addressed as a range of a larger resource.
	needed bool
	// segments is how many sampled segments came from this host, which is how
	// many times the old per-segment finding would have said the same thing.
	segments int
	// err is a transport failure, not an HTTP status: the probe could not be
	// made at all, so this host's range support is unknown rather than absent.
	err error
}

// probeByteRanges asks each host that served a segment whether it honours Range.
//
// A host whose segments are already byte ranges has answered the question by
// serving them, so no extra request is made. Everything else gets one.
func probeByteRanges(ctx context.Context, c *fetch.Client, rends []*renditionData) []byteRangeProbe {
	// One entry per host, in the order the hosts were first seen, so a report is
	// stable across runs rather than ordered by a map.
	type pending struct {
		probe byteRangeProbe
		uri   string
		body  int64
		// known is set when a byte-range segment already answered the question.
		known bool
	}
	var order []string
	byHost := map[string]*pending{}

	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		for _, sd := range rd.segs {
			if sd.fetchErr != nil {
				continue
			}
			host := hostOf(sd.seg.URI)
			if host == "" {
				continue
			}
			p, ok := byHost[host]
			if !ok {
				p = &pending{probe: byteRangeProbe{host: host}}
				byHost[host] = p
				order = append(order, host)
			}
			p.probe.segments++
			if strings.EqualFold(sd.res.Header.Get("Accept-Ranges"), "bytes") {
				p.probe.advertised = true
			}
			if sd.seg.ByteRange != nil {
				// The sample itself is the probe. Whatever the origin did with a
				// range it was actually asked for beats anything a synthetic
				// request would show.
				p.probe.needed = true
				if !p.known {
					p.known = true
					p.probe.requested = sd.seg.ByteRange.Length
					p.probe.got = int64(len(sd.res.Body))
					p.probe.status = sd.res.Status
				}
				continue
			}
			if p.uri == "" {
				p.uri, p.body = sd.seg.URI, int64(len(sd.res.Body))
			}
		}
	}

	var out []byteRangeProbe
	for _, host := range order {
		p := byHost[host]
		if p.known {
			out = append(out, p.probe)
			continue
		}
		// Ask for less than the resource holds, so that a 200 with everything is
		// distinguishable from a 206 with what was asked for. A body too small to
		// cut in half cannot make that distinction, so it is not asked.
		want := byteRangeProbeBytes
		if half := p.body / 2; half < want {
			want = half
		}
		if p.uri == "" || want < 1 {
			continue
		}
		res, err := c.Get(ctx, p.uri, fmt.Sprintf("bytes=0-%d", want-1))
		p.probe.requested = want
		p.probe.got = int64(len(res.Body))
		p.probe.status = res.Status
		if strings.EqualFold(res.Header.Get("Accept-Ranges"), "bytes") {
			p.probe.advertised = true
		}
		// A status is an answer; only a request that never completed is unknown.
		if err != nil && res.Status == 0 {
			p.probe.err = err
		}
		out = append(out, p.probe)
	}
	return out
}

// hostOf is the host:port a URL addresses, which is the unit range support is a
// property of. A relative or unparseable URI has no host and is skipped.
func hostOf(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	return u.Host
}

func checkByteRange(probes []byteRangeProbe) []finding.Finding {
	var out []finding.Finding
	for _, p := range probes {
		switch {
		case p.err != nil:
			// A limit of this tool, not a defect in the stream: the answer is
			// unknown, and an operator needs to know the coverage has that hole.
			out = append(out, finding.Finding{
				Check: "byterange", Target: p.host, Status: finding.ERROR,
				Message: fmt.Sprintf("could not ask %s whether it honours Range: %v", p.host, p.err),
				Hint:    "byte-range support is unverified for this host; nothing about the stream is implied",
			})

		case p.status == 206 && p.got == p.requested:
			out = append(out, finding.Finding{
				Check: "byterange", Target: p.host, Status: finding.OK,
				Message: fmt.Sprintf("%s honours Range: asked for %d bytes, got %d with a 206",
					p.host, p.requested, p.got),
				Value: finding.Num(float64(p.got)), Unit: "bytes",
			})

		case p.status == 206:
			// A 206 is a promise about the body, and a body that is not the size
			// asked for breaks it. A proxy that stamps the status and forwards the
			// whole object does this, and the status alone would clear it.
			out = append(out, finding.Finding{
				Check: "byterange", Target: p.host, Status: byteRangeStatus(p),
				Message: fmt.Sprintf("%s answered 206 but sent %d bytes for a %d-byte range: the status says partial and the body is not",
					p.host, p.got, p.requested),
				Value: finding.Num(float64(p.got)), Unit: "bytes",
				Hint: byteRangeHint(p),
			})

		case p.status == 200:
			msg := fmt.Sprintf("%s does not honour Range: answered 200 with the whole resource (%d bytes) for a %d-byte range",
				p.host, p.got, p.requested)
			if p.advertised {
				msg += ", having advertised Accept-Ranges: bytes"
			}
			out = append(out, finding.Finding{
				Check: "byterange", Target: p.host, Status: byteRangeStatus(p),
				Message: msg,
				Value:   finding.Num(float64(p.got)), Unit: "bytes",
				Hint: byteRangeHint(p),
			})

		default:
			msg := fmt.Sprintf("%s refused a Range request every player may make: HTTP %d for bytes 0-%d of a larger resource",
				p.host, p.status, p.requested-1)
			if p.advertised {
				msg += ", having advertised Accept-Ranges: bytes"
			}
			out = append(out, finding.Finding{
				Check: "byterange", Target: p.host, Status: byteRangeStatus(p),
				Message: msg,
				Value:   finding.Num(float64(p.status)), Unit: "status",
				Hint: byteRangeHint(p),
			})
		}
	}
	return out
}

// byteRangeStatus grades a host that did not honour a range.
//
// The severity is a property of the stream, not of the origin: the same origin
// behaviour is fatal to a stream addressed in ranges and free to one that is
// not. The middle case is an origin whose header promises what it does not do,
// which is worth a warning whatever this stream needs, because the next thing
// that reads the header will believe it.
func byteRangeStatus(p byteRangeProbe) finding.Status {
	switch {
	case p.needed:
		return finding.BAD
	case p.advertised:
		return finding.WARN
	default:
		return finding.OK
	}
}

func byteRangeHint(p byteRangeProbe) string {
	if p.needed {
		return fmt.Sprintf("every one of the %d sampled segments is a range of a larger resource, so a player gets the whole file each time it asks for one: "+
			"the overlapping timeline, over-long durations and continuity breaks reported elsewhere are all downstream of this", p.segments)
	}
	if p.advertised {
		return "this stream never asks for a range, so nothing here is broken — but the header says range support is on and it is not, and a single-file rendition published against this host would not play"
	}
	return "this stream addresses whole resources and never sends a Range, so it costs nothing here; it does rule out byte-range packaging and makes a player's seek more expensive"
}
