package analyze

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
)

// A dynamic MPD does not list what exists. It states availabilityStartTime and
// leaves a client to work out, by arithmetic against "now", which segment is
// the newest — so the live edge is a computed claim, and the clock it is
// computed against is part of the claim.
//
// That is why an MPD may name its own time sources: the client's clock is not
// to be trusted, and a machine thirty seconds fast asks for segments the
// packager has not made yet and gets 404s that read as a CDN fault. A checker
// that ignores UTCTiming measures its own clock instead of the stream, and
// reports the operator's laptop as a broken origin.

// clockSkewTolerance is how far the local clock may sit from the source the MPD
// names before it is worth saying so. Below a second the measurement is mostly
// the round trip.
const clockSkewTolerance = time.Second

// referenceClock is the answer to "what time does this stream think it is".
type referenceClock struct {
	now    time.Time
	source string        // the scheme that answered, for the finding to quote
	skew   time.Duration // reference minus local
	ok     bool
	// unsupported names the schemes the MPD offered that segcheck cannot speak,
	// when none of the ones it can worked.
	unsupported []string
}

// resolveClock asks the time sources the MPD names, in the order it lists them
// — that order is the MPD's own fallback preference.
func resolveClock(ctx context.Context, c *fetch.Client, sources []manifest.UTCTiming, local time.Time) referenceClock {
	out := referenceClock{}
	for _, s := range sources {
		scheme := strings.ToLower(s.Scheme)
		switch {
		case strings.Contains(scheme, "utc:direct"):
			if t, err := parsePDTLike(s.Value); err == nil {
				out.now, out.source, out.ok = t, s.Scheme, true
			}
		case strings.Contains(scheme, "utc:http-head"):
			resp, err := c.Get(ctx, s.Value, "bytes=0-0")
			if err != nil {
				continue
			}
			if d := resp.Header.Get("Date"); d != "" {
				if t, err := http.ParseTime(d); err == nil {
					out.now, out.source, out.ok = t.Add(resp.Duration/2), s.Scheme, true
				}
			}
		case strings.Contains(scheme, "utc:http-iso"), strings.Contains(scheme, "utc:http-xsdate"):
			resp, err := c.Get(ctx, s.Value, "")
			if err != nil {
				continue
			}
			if t, err := parsePDTLike(string(resp.Body)); err == nil {
				// The answer describes the moment the server wrote it; half the
				// round trip has gone by since.
				out.now, out.source, out.ok = t.Add(resp.Duration/2), s.Scheme, true
			}
		default:
			// NTP and SNTP are protocols this tool does not speak, and a
			// zero-dependency binary is not about to. Saying so is better than
			// silently falling back to the clock the element exists to distrust.
			out.unsupported = append(out.unsupported, s.Scheme)
			continue
		}
		if out.ok {
			out.skew = out.now.Sub(local)
			return out
		}
	}
	return out
}

// availabilityProbe is what the origin said about the segment just past the
// live edge — the one the MPD's arithmetic says does not exist yet.
type availabilityProbe struct {
	label   string
	uri     string
	present bool
	probed  bool
}

// probeNextSegments asks each selected video rendition's origin for the segment
// the MPD says is not there yet. It is one small ranged request per rendition,
// and it is the only way to see the harmless direction of clock skew: media
// that exists while every spec-following player waits for it.
func probeNextSegments(ctx context.Context, c *fetch.Client, pl manifest.Playlist, rends []*renditionData) []availabilityProbe {
	var out []availabilityProbe
	if !pl.Live || pl.Kind != manifest.KindDASH {
		return nil
	}
	for _, rd := range rends {
		if rd.err != nil || rd.r.Kind != manifest.Video || rd.r.NextSegment == nil {
			continue
		}
		p := availabilityProbe{label: rendLabel(rd.r), uri: rd.r.NextSegment.URI, probed: true}
		// A byte range keeps the probe to a few bytes on an origin that honours
		// it, and a whole segment on one that does not — which is still bounded
		// by --max-bytes and happens once per rendition.
		resp, err := c.Get(ctx, p.uri, "bytes=0-1023")
		p.present = err == nil && resp.Status < 400
		out = append(out, p)
		// One rendition is enough to establish the packager's position relative
		// to the MPD's clock; probing every rung would say the same thing N times.
		break
	}
	return out
}

// checkAvailability judges the computed live edge against what the origin
// actually has.
func checkAvailability(pl manifest.Playlist, rends []*renditionData, clock referenceClock, probes []availabilityProbe, opts Options) []finding.Finding {
	if pl.Kind != manifest.KindDASH || !pl.Live {
		return nil // HLS lists what exists; there is no computed claim to check
	}
	var out []finding.Finding
	target := shortTarget(pl.URL)

	switch {
	case clock.ok && absDuration(clock.skew) > clockSkewTolerance:
		out = append(out, finding.Finding{
			Check: "availability", Target: target, Status: finding.WARN,
			Message: fmt.Sprintf("this machine's clock is %s from the time source the MPD names (%s); the live edge below was computed against the MPD's source, not this clock",
				signedSeconds(clock.skew), clock.source),
			Value: finding.Num(clock.skew.Seconds()), Unit: "s",
			Hint: "a player with this machine's clock would ask for segments the packager has not published, and read the 404s as a CDN fault",
		})
	case clock.ok:
		out = append(out, finding.Finding{
			Check: "availability", Target: target, Status: finding.OK,
			Message: fmt.Sprintf("this machine's clock agrees with the time source the MPD names (%s) to within %s",
				clock.source, absDuration(clock.skew).Round(time.Millisecond)),
			Value: finding.Num(clock.skew.Seconds()), Unit: "s",
		})
	case len(clock.unsupported) > 0:
		out = append(out, finding.Finding{
			Check: "availability", Target: target, Status: finding.OK,
			Message: fmt.Sprintf("the MPD's UTCTiming sources are all schemes segcheck does not speak (%s); the live edge was judged against this machine's clock",
				strings.Join(clock.unsupported, ", ")),
			Hint: "a wrong clock here reads as a broken origin, so the verdict below is only as good as this machine's time",
		})
	default:
		out = append(out, finding.Finding{
			Check: "availability", Target: target, Status: finding.OK,
			Message: "the MPD names no UTCTiming source; the live edge was judged against this machine's clock, which is itself the thing under test",
			Hint:    "adding a UTCTiming element is what lets a player — and this check — agree with the packager about what time it is",
		})
	}

	out = append(out, missingEdgeFindings(rends, opts)...)

	for _, p := range probes {
		if !p.probed || !p.present {
			continue
		}
		out = append(out, finding.Finding{
			Check: "availability", Target: p.label, Status: finding.WARN,
			Message: "the segment after the live edge is already served: the packager is ahead of the MPD's own availability window by at least one segment",
			Hint:    "every player following availabilityStartTime waits for media that is already there; the latency is spent for nothing",
		})
	}
	return out
}

// missingEdgeFindings diagnoses the run of segments at the live edge that the
// MPD promised and the origin does not have.
//
// The `fetch` check already reports each 404. What it cannot say is why they
// are all at the newest end and none in the middle, which is the signature of
// the availability arithmetic being ahead of the packager rather than of the
// CDN losing objects.
func missingEdgeFindings(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil || len(rd.segs) == 0 {
			continue
		}
		// Count backwards from the newest: only an unbroken run at the edge is
		// this defect. A hole in the middle is a different problem and `fetch`
		// owns it.
		missing := 0
		for i := len(rd.segs) - 1; i >= 0; i-- {
			if rd.segs[i].fetchErr == nil {
				break
			}
			missing++
		}
		if missing == 0 {
			continue
		}
		behind := float64(missing) * segmentSeconds(rd)
		status := finding.BAD
		msg := fmt.Sprintf("the newest %d segments the MPD's availabilityStartTime promises are not on the origin: the packager is at least %.1fs behind the clock the MPD is computed against",
			missing, behind)
		if missing == len(rd.segs) {
			msg = fmt.Sprintf("none of the %d segments the MPD's availabilityStartTime promises is on the origin: the packager is at least %.1fs behind the clock the MPD is computed against",
				missing, behind)
		}
		out = append(out, finding.Finding{
			Check: "availability", Target: rendLabel(rd.r), Status: status,
			Message: msg,
			Value:   finding.Num(behind), Unit: "s",
			Hint: "every player computes the same edge and gets the same 404s; the cause is a clock, not the CDN",
		})
	}
	return out
}

// segmentSeconds is the declared duration of this rendition's segments, which
// is what one segment of skew is worth.
func segmentSeconds(rd *renditionData) float64 {
	for _, sd := range rd.segs {
		if sd.seg.Duration > 0 {
			return sd.seg.Duration
		}
	}
	return 0
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func signedSeconds(d time.Duration) string {
	return fmt.Sprintf("%+.1fs", d.Seconds())
}

// parsePDTLike accepts the forms a UTCTiming source answers in: the ISO-8601
// spellings http-iso and http-xsdate use, and whatever a direct value carries.
func parsePDTLike(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("not a time: %q", truncateForError(s))
}

func truncateForError(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
