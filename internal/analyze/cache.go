package analyze

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// A CDN that is not caching a live edge is an origin serving every viewer
// directly, and nothing in the media shows it: every segment arrives, parses and
// lines up perfectly. It surfaces as an origin under load during a peak, which
// is the worst possible moment to start diagnosing it.
//
// The evidence is only in the response headers, and every vendor spells it
// differently — `X-Cache: TCP_HIT from ...` at Akamai, `CF-Cache-Status: HIT` at
// Cloudflare, `X-Cache: HIT, HIT` through two Fastly tiers. A checker that knew
// one vocabulary would call a perfectly warm edge cold, which is why they are all
// read and why an origin that states none of them is reported as unreadable
// rather than as a miss.

// cacheStatus is what a response says about where it came from.
type cacheStatus int

const (
	cacheUnknown cacheStatus = iota
	cacheHit
	cacheMiss
)

// missRatioFloor is how much of a sample has to be a miss before it is worth
// reporting. It is 1.0 deliberately: a live edge always has one cold segment at
// the front — nobody has asked for the newest one yet — so anything short of
// every segment being a miss is the shape of a healthy stream, not a defect.
const missRatioFloor = 1.0

func checkCache(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		var hits, misses, unknown, total int
		var uncacheable []segmentData
		var ages []int
		for _, sd := range rd.segs {
			if sd.fetchErr != nil {
				continue
			}
			total++
			switch cacheStatusOf(sd.res.Header) {
			case cacheHit:
				hits++
			case cacheMiss:
				misses++
			default:
				unknown++
			}
			if age, ok := cacheAge(sd.res.Header); ok {
				ages = append(ages, age)
			}
			if reason := uncacheableReason(sd.res.Header); reason != "" {
				uncacheable = append(uncacheable, sd)
			}
		}
		if total == 0 {
			continue
		}
		label := rendLabel(rd.r)

		// An origin telling caches not to store a segment guarantees every viewer
		// reaches it, whatever the CDN in front of it is configured to do. It is
		// the one finding here that needs no cache status at all to be certain of.
		if len(uncacheable) > 0 {
			out = append(out, finding.Finding{
				Check: "cache", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("%d of %d segments tell caches not to store them (Cache-Control: %s)",
					len(uncacheable), total, uncacheableReason(uncacheable[0].res.Header)),
				Value: finding.Num(float64(len(uncacheable))), Unit: "segments",
				Hint: "every viewer fetches these from the origin however the CDN is configured; on a live stream that is the whole audience",
			})
		}

		if hits+misses == 0 {
			out = append(out, finding.Finding{
				Check: "cache", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("the origin states no cache status on %d segments: hits cannot be told from misses", total),
				Hint:    "no X-Cache, CF-Cache-Status, Age or equivalent header came back",
			})
			continue
		}

		// Every sampled segment a miss. On a live stream that is a CDN not caching
		// the edge at all — the origin is serving every viewer. On VOD it is more
		// often cold content nobody has asked for yet, which is not an incident.
		if misses > 0 && float64(misses)/float64(hits+misses) >= missRatioFloor {
			status, hint := finding.WARN, "on demand this is often just cold content: nothing has asked for these segments yet"
			if rd.live {
				status = finding.BAD
				hint = "the CDN is not caching the live edge, so the origin is serving every viewer directly — which shows up as origin load at the worst possible moment"
			}
			out = append(out, finding.Finding{
				Check: "cache", Target: label, Status: status,
				Message: fmt.Sprintf("%d of %d segments were a cache miss", misses, hits+misses),
				Value:   finding.Num(float64(misses)), Unit: "segments",
				Hint: hint,
			})
			continue
		}

		msg := fmt.Sprintf("%d of %d segments served from cache", hits, hits+misses)
		if len(ages) > 0 {
			lo, hi := ages[0], ages[0]
			for _, a := range ages[1:] {
				if a < lo {
					lo = a
				}
				if a > hi {
					hi = a
				}
			}
			msg += fmt.Sprintf(", ages %d–%ds", lo, hi)
		}
		if unknown > 0 {
			msg += fmt.Sprintf(" (%d stated no status)", unknown)
		}
		out = append(out, finding.Finding{
			Check: "cache", Target: label, Status: finding.OK,
			Message: msg,
			Value:   finding.Num(float64(hits) / float64(hits+misses) * 100), Unit: "%",
		})
	}
	return out
}

// cacheStatusOf reads whichever vendor's header the response carries.
//
// The order matters only in that a stated status beats an inferred one: a
// non-zero Age says the object has been sitting in a cache, which is a hit by
// arithmetic even from a CDN that names no status at all.
func cacheStatusOf(h http.Header) cacheStatus {
	if h == nil {
		return cacheUnknown
	}
	for _, name := range []string{"X-Cache", "CF-Cache-Status", "Akamai-Cache-Status", "X-Cache-Status", "X-Proxy-Cache"} {
		if s := classifyCacheValue(h.Get(name)); s != cacheUnknown {
			return s
		}
	}
	// Some caches state only how many times they have served it.
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Cache-Hits"))); err == nil {
		if n > 0 {
			return cacheHit
		}
		return cacheMiss
	}
	if age, ok := cacheAge(h); ok && age > 0 {
		return cacheHit
	}
	return cacheUnknown
}

// classifyCacheValue reads one header's value. A multi-tier CDN reports one word
// per tier — "HIT, MISS" — and the stream was served from cache if any tier
// served it, because that is the tier the viewer reached.
func classifyCacheValue(v string) cacheStatus {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" {
		return cacheUnknown
	}
	out := cacheUnknown
	for _, part := range strings.Split(v, ",") {
		switch {
		case strings.Contains(part, "HIT"):
			return cacheHit
		case strings.Contains(part, "MISS"), strings.Contains(part, "EXPIRED"),
			strings.Contains(part, "BYPASS"), strings.Contains(part, "DYNAMIC"),
			strings.Contains(part, "REVALIDATED"):
			out = cacheMiss
		}
	}
	return out
}

func cacheAge(h http.Header) (int, bool) {
	if h == nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(h.Get("Age")))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// uncacheableReason names the Cache-Control directive that stops a segment being
// stored, or the empty string when nothing does.
//
// `max-age=0` is deliberately not one of them: it is what a live playlist uses,
// and plenty of CDNs still serve a stale copy while revalidating. The three here
// are the ones that mean "do not keep this".
func uncacheableReason(h http.Header) string {
	if h == nil {
		return ""
	}
	cc := strings.ToLower(h.Get("Cache-Control"))
	for _, directive := range []string{"no-store", "no-cache", "private"} {
		if strings.Contains(cc, directive) {
			return directive
		}
	}
	return ""
}
